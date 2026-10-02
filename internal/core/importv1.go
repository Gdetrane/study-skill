package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/checkpoint"
)

// Importing a v1 workspace: the study skill's folder with .study-config.json,
// lessons/, practice/, notes/ and .fsrs/, under git. The import copies it,
// git history included, into the Study home as a new Topic and leaves the
// original untouched. It converts what maps onto v2, keeps v1's names as
// ids (lesson-01) so paths quoted in Lesson text keep working, records one
// topic.imported Event with the Lesson completions it can prove, and drops
// the rest, saying so. An adoption Session then turns it into a v2 Topic.

const (
	eventTopicImported = "topic.imported"

	v1ConfigFile   = ".study-config.json"
	v1PlanFile     = "lessons/plan.md"
	v1PlanTarget   = "notes/v1-plan.md"
	v1ConfigTarget = "notes/v1-config.json"
	v1CardsDir     = ".fsrs"
	v1CardsFile    = ".fsrs/cards.json"

	maxV1ConfigBytes = 1 << 20
	maxV1Commits     = 5000
	maxV1Lessons     = 500
	maxV1Sources     = 200
	v1NoteRunes      = 2000
)

// The v1 config versions the importer understands.
const maxV1ConfigVersion = 3

func init() {
	eventKinds[eventTopicImported] = eventKind{apply: applyNothing, replay: replayTopicImported}
}

// ImportSpec describes importing a v1 workspace.
type ImportSpec struct {
	// Dir is the v1 workspace: absolute, starting with ~/, or relative to
	// the folder study started in.
	Dir string
	// ID is the new Topic's id. It defaults to the workspace folder's name,
	// so the Topic keeps v1's name.
	ID     string
	DryRun bool
}

// TopicImport reports an import: what was converted, moved, proven done,
// left open and dropped. A dry run reports the same, writing nothing.
type TopicImport struct {
	Topic Topic `json:"topic"`
	// From is the v1 workspace, absolute; Head its git HEAD.
	From string `json:"from"`
	Head string `json:"head,omitempty"`
	// Converted lists what became part of the v2 Topic, Moved the files
	// moved to their v2 place.
	Converted []ImportNote `json:"converted"`
	Moved     []ImportMove `json:"moved"`
	// Completed are the Lessons proven done, Open the others.
	Completed     []ImportedLesson `json:"completed"`
	Open          []ImportedLesson `json:"open"`
	Sources       []Source         `json:"sources"`
	KnowledgeBase *KnowledgeBase   `json:"knowledge_base,omitempty"`
	Level         string           `json:"level,omitempty"`
	Approach      string           `json:"approach,omitempty"`
	// NextStep is where v1 stopped, for the adoption Session to turn into
	// a Next step.
	NextStep *V1NextStep `json:"v1_next_step,omitempty"`
	// Dropped lists everything that is not carried into v2, and why.
	Dropped []ImportNote `json:"dropped"`
	// Checkpoint is the commit that saved the imported Topic; on failure,
	// CheckpointError says why, and the next Checkpoint saves it.
	Checkpoint      string `json:"checkpoint,omitempty"`
	CheckpointError string `json:"checkpoint_error,omitempty"`
	DryRun          bool   `json:"dry_run,omitempty"`
}

// ImportNote is one thing an import converted or dropped.
type ImportNote struct {
	What   string `json:"what"`
	Detail string `json:"detail,omitempty"`
}

// ImportMove is a file moved to its v2 place.
type ImportMove struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// ImportedLesson is a v1 Lesson, under its v2 id.
type ImportedLesson struct {
	Lesson string `json:"lesson"`
	Title  string `json:"title"`
	// Proof is what proves a completed Lesson done.
	Proof string `json:"proof,omitempty"`
	// V1Status is the Lesson's status in v1's config.
	V1Status string `json:"v1_status,omitempty"`
}

// V1NextStep is where a v1 Session stopped: its phase, its pending action
// and its context.
type V1NextStep struct {
	Phase         string `json:"phase,omitempty"`
	PendingAction string `json:"pending_action,omitempty"`
	Context       string `json:"context,omitempty"`
}

// TopicImported is shown on an imported Topic in status: where it came
// from, when, and whether it has been adopted, which it is once it has a
// Syllabus.
type TopicImported struct {
	From     string      `json:"from"`
	At       time.Time   `json:"at"`
	Adopted  bool        `json:"adopted"`
	NextStep *V1NextStep `json:"v1_next_step,omitempty"`
}

// topicImportedData is the payload of a topic.imported Event.
type topicImportedData struct {
	From          string           `json:"from"`
	Head          string           `json:"head,omitempty"`
	ConfigVersion int              `json:"config_version"`
	ConfigHash    string           `json:"config_hash"`
	Converted     []ImportNote     `json:"converted"`
	Moved         []ImportMove     `json:"moved"`
	Completed     []ImportedLesson `json:"completed"`
	Open          []ImportedLesson `json:"open"`
	Dropped       []ImportNote     `json:"dropped"`
	NextStep      *V1NextStep      `json:"next_step,omitempty"`
}

// importState is a replayed import.
type importState struct {
	event    string
	at       time.Time
	from     string
	nextStep *V1NextStep
}

// v1Config is the part of .study-config.json the importer reads; see v1's
// SKILL.md, "Config Schema (v3)".
type v1Config struct {
	Version            int               `json:"version"`
	Topic              string            `json:"topic"`
	Approach           string            `json:"approach"`
	EndGoal            json.RawMessage   `json:"end_goal"`
	Difficulty         string            `json:"difficulty"`
	DifficultyOverride *string           `json:"difficulty_override"`
	Created            string            `json:"created"`
	Lessons            []v1Lesson        `json:"lessons"`
	SessionState       *v1SessionState   `json:"session_state"`
	Sources            []json.RawMessage `json:"sources"`
	NotebookLM         json.RawMessage   `json:"notebooklm"`
}

type v1Lesson struct {
	Num    int    `json:"num"`
	Title  string `json:"title"`
	File   string `json:"file"`
	Status string `json:"status"`
}

type v1SessionState struct {
	Phase         string  `json:"phase"`
	PendingAction *string `json:"pending_action"`
	Context       *string `json:"context"`
}

// v1Dropped are the config keys v2 does not carry over, with why.
var v1Dropped = map[string]string{
	"template":                   "v2 has no templates: the agent sets up the Workbench with the language's own tools",
	"template_mode":              "v2 has no templates: the agent sets up the Workbench with the language's own tools",
	"mode":                       "v2 has no tutorial or challenge modes beyond the Approach",
	"next_calibration_at_lesson": "v2 has no calibration rounds: Assessments at the end of each Milestone replace them",
	"progress":                   "progress is computed from the History",
	"review":                     "v1's review queue goes with its cards",
	"catalog_path":               "the Library replaces v1's book catalog: study library build",
	"sciagent_skills":            "companions are suggested by the skill when installed",
	"sciagent_primary":           "companions are suggested by the skill when installed",
}

var (
	v1LessonFileNum = regexp.MustCompile(`^(\d{1,3})[-_]`)
	v1DoneStatuses  = map[string]bool{"completed": true, "complete": true, "done": true}
)

// ImportV1 imports a v1 workspace as a new Topic.
func (c *Core) ImportV1(ctx context.Context, spec ImportSpec) (TopicImport, error) {
	src, err := c.v1Dir(spec.Dir)
	if err != nil {
		return TopicImport{}, err
	}
	srcRoot, err := os.OpenRoot(src)
	if err != nil {
		return TopicImport{}, internalError("opening "+src, err)
	}
	defer srcRoot.Close()
	p, err := c.planImport(ctx, srcRoot, src, spec.ID)
	if err != nil {
		return TopicImport{}, err
	}
	if spec.DryRun {
		p.report.DryRun = true
		return p.report, nil
	}
	return c.runImport(ctx, srcRoot, p)
}

// v1Dir resolves and checks the folder to import.
func (c *Core) v1Dir(dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", invalidf("name the v1 workspace to import")
	}
	if strings.ContainsFunc(dir, func(r rune) bool { return r < ' ' || r == 0x7f }) {
		return "", invalidf("the folder's path contains a control character")
	}
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", internalError("finding the home folder", err)
		}
		dir = filepath.Join(home, strings.TrimPrefix(dir, "~"))
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(c.dir, dir)
	}
	dir = filepath.Clean(dir)
	info, err := os.Stat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return "", &Error{Code: CodeNotFound, Message: dir + " does not exist"}
	}
	if err != nil {
		return "", internalError("reading "+dir, err)
	}
	if !info.IsDir() {
		return "", invalidf("%s is not a folder", dir)
	}
	if inside(resolvedPath(c.home), resolvedPath(dir)) {
		return "", invalidf("%s is inside the Study home already: import a v1 workspace from elsewhere", dir)
	}
	return dir, nil
}

// inside reports whether dir is home or below it.
func inside(home, dir string) bool {
	rel, err := filepath.Rel(home, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// importPlan is everything an import will do, worked out from the v1
// workspace without writing anything.
type importPlan struct {
	id, title, goal string
	data            topicImportedData
	report          TopicImport
	// gitattributes is the v1 workspace's .gitattributes, kept after
	// Lamplight's lines; hasGit says whether it is a git repository.
	gitattributes []byte
	hasGit        bool
	// outside are file Sources outside the workspace, by Source id, to
	// remember where they are on this computer.
	outside map[string]foundFile
}

// planImport reads the v1 workspace and plans the import. It writes
// nothing, and reads the workspace's git history only through the hardened
// checkpoint package.
func (c *Core) planImport(ctx context.Context, src *os.Root, srcPath, id string) (*importPlan, error) {
	for _, name := range []string{topicFile, historyFile} {
		if _, err := src.Lstat(name); err == nil {
			return nil, invalidf("%s is a Lamplight Topic already, not a v1 workspace", srcPath)
		}
	}
	raw, err := readSmallFile(src, v1ConfigFile, maxV1ConfigBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, invalidf("%s is not a v1 workspace: it has no %s", srcPath, v1ConfigFile)
	}
	if err != nil {
		return nil, err
	}
	var cfg v1Config
	keys := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &keys); err != nil {
		return nil, corruptf("%s is not a JSON object: %v", v1ConfigFile, err)
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, corruptf("%s cannot be read as a v1 config: %v", v1ConfigFile, err)
	}
	if cfg.Version > maxV1ConfigVersion {
		return nil, invalidf("%s has version %d, which this importer does not know (it reads up to %d)",
			v1ConfigFile, cfg.Version, maxV1ConfigVersion)
	}
	if err := checkV1Git(src); err != nil {
		return nil, err
	}
	walk, err := scanWorkspace(ctx, src)
	if err != nil {
		return nil, err
	}

	p := &importPlan{outside: map[string]foundFile{}}
	sum := sha256.Sum256(raw)
	p.data = topicImportedData{From: srcPath, ConfigVersion: cfg.Version, ConfigHash: "sha256:" + hex.EncodeToString(sum[:]),
		Converted: []ImportNote{}, Moved: []ImportMove{}, Completed: []ImportedLesson{}, Open: []ImportedLesson{},
		Dropped: []ImportNote{}}
	convert := func(what, detail string) {
		p.data.Converted = append(p.data.Converted, ImportNote{What: what, Detail: detail})
	}
	drop := func(what, detail string) {
		p.data.Dropped = append(p.data.Dropped, ImportNote{What: what, Detail: detail})
	}

	// The Topic's id and title.
	if id == "" {
		id = slugify(filepath.Base(srcPath))
	}
	if err := validateTopicID(id); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(filepath.Join(c.home, id)); err == nil {
		return nil, alreadyExists(id)
	}
	p.id = id
	p.title = derivedText(cfg.Topic, maxTitleRunes)
	if p.title == "" {
		p.title = derivedText(filepath.Base(srcPath), maxTitleRunes)
	}
	convert("title", "from topic: "+p.title)
	switch goal := strings.TrimSpace(string(cfg.EndGoal)); {
	case goal == "" || goal == "null":
	default:
		var s string
		if err := json.Unmarshal(cfg.EndGoal, &s); err != nil {
			drop("end_goal", "it is not text")
		} else if g, err := cleanText("goal", s, maxGoalRunes); err != nil {
			drop("end_goal", err.Error())
		} else if g != "" {
			p.goal = g
			convert("goal", "from end_goal")
		}
	}

	// The Level and the Approach.
	level := strings.ToLower(strings.TrimSpace(cfg.Difficulty))
	if cfg.DifficultyOverride != nil && strings.TrimSpace(*cfg.DifficultyOverride) != "" {
		level = strings.ToLower(strings.TrimSpace(*cfg.DifficultyOverride))
		convert("level", "from difficulty_override: "+level)
	} else if level != "" {
		convert("level", "from difficulty: "+level)
	}
	if level != "" {
		if _, err := checkLevel(level); err != nil {
			p.data.Converted = p.data.Converted[:len(p.data.Converted)-1]
			drop("difficulty", fmt.Sprintf("%q is not a Level", clip(level, 40)))
			level = ""
		}
	}
	p.report.Level = level
	switch a := strings.ToLower(strings.TrimSpace(cfg.Approach)); a {
	case "":
	case "concept", "concepts":
		p.report.Approach = ApproachConcepts
	case "project":
		p.report.Approach = ApproachProject
	case "challenge", "challenges":
		p.report.Approach = ApproachChallenges
	default:
		drop("approach", fmt.Sprintf("%q is not an Approach", clip(a, 40)))
	}
	if p.report.Approach != "" {
		convert("approach", "from approach: "+p.report.Approach)
	}

	// Lessons: v1's names become ids, their files move to lessons/<id>.md.
	commits, head, logErr := v1Commits(ctx, srcPath, walk.hasGit)
	p.data.Head = head
	p.hasGit = walk.hasGit
	switch {
	case !walk.hasGit:
		drop("git history", "the workspace is not a git repository: the Topic starts a new one")
	case logErr != nil:
		drop("proof from git", "its history could not be read: "+logErr.Error())
	}
	cardIDs, cardCount := v1CardIDs(src)
	if len(cfg.Lessons) > maxV1Lessons {
		return nil, invalidf("%s lists %d Lessons; the importer takes at most %d", v1ConfigFile, len(cfg.Lessons), maxV1Lessons)
	}
	targets := map[string]bool{}
	seen := map[string]bool{}
	for i, l := range cfg.Lessons {
		lessonID, err := v1LessonID(l)
		if err != nil {
			drop(fmt.Sprintf("lessons[%d]", i), err.Error())
			continue
		}
		if seen[lessonID] {
			drop(fmt.Sprintf("lessons[%d]", i), "a second Lesson numbered like "+lessonID)
			continue
		}
		seen[lessonID] = true
		title := derivedText(l.Title, maxTitleRunes)
		if title == "" {
			title = lessonID
		}
		il := ImportedLesson{Lesson: lessonID, Title: title, V1Status: derivedText(l.Status, 40)}
		file, fileErr := v1RelPath(l.File)
		hasFile := fileErr == nil && file != "" && isRegularFile(src, file)
		target := lessonFile(lessonID)
		switch {
		case !hasFile:
			drop("lesson file of "+lessonID, "v1 names no readable file for it ("+clip(l.File, 80)+")")
		case file != target && !targets[target] && !walk.files[target]:
			p.data.Moved = append(p.data.Moved, ImportMove{From: file, To: target})
			targets[target] = true
		case file != target:
			drop("moving "+file, target+" is taken: the file stays where it is")
		}
		if v1DoneStatuses[strings.ToLower(strings.TrimSpace(l.Status))] {
			if proof := v1Proof(lessonID, l.Num, commits, cardIDs); proof != "" && hasFile {
				il.Proof = proof
				p.data.Completed = append(p.data.Completed, il)
				continue
			}
			il.V1Status += " (not proven: no completion commit or card, or no Lesson file)"
		}
		p.data.Open = append(p.data.Open, il)
	}
	if len(cfg.Lessons) > 0 {
		convert("lessons", fmt.Sprintf("%d Lessons kept under their v1 names", len(seen)))
	}

	// Files moved to their v2 place.
	if walk.files[v1PlanFile] {
		if walk.files[v1PlanTarget] {
			drop("moving "+v1PlanFile, v1PlanTarget+" is taken: the plan stays where it is")
		} else {
			p.data.Moved = append(p.data.Moved, ImportMove{From: v1PlanFile, To: v1PlanTarget})
			convert("plan", "the v1 plan is kept as "+v1PlanTarget+" for the adoption Session")
		}
	}
	if walk.files[v1ConfigTarget] {
		drop("keeping "+v1ConfigFile, v1ConfigTarget+" is taken: the config stays in the git history only")
	} else {
		p.data.Moved = append(p.data.Moved, ImportMove{From: v1ConfigFile, To: v1ConfigTarget})
		convert("config", v1ConfigFile+" becomes "+topicFile+"; the original is kept as "+v1ConfigTarget)
	}
	if walk.dirs[v1CardsDir] {
		drop(v1CardsDir, fmt.Sprintf("v1's lesson-level cards (%d) are not carried over: Cards are written again in the adoption Session; they stay in the git history", cardCount))
	}

	// Sources and the Knowledge base.
	if err := c.planV1Sources(ctx, src, srcPath, cfg, walk, p, convert, drop); err != nil {
		return nil, err
	}

	// Where v1 stopped.
	if st := cfg.SessionState; st != nil {
		ns := V1NextStep{Phase: derivedText(st.Phase, 40)}
		if st.PendingAction != nil {
			ns.PendingAction = derivedText(*st.PendingAction, v1NoteRunes)
		}
		if st.Context != nil {
			ns.Context = derivedText(*st.Context, v1NoteRunes)
		}
		if ns.PendingAction != "" || ns.Context != "" {
			p.data.NextStep = &ns
			convert("session_state", "where v1 stopped, for the Next step")
		}
		drop("session_state.energy and time_budget_minutes", "Energy and time are asked at every Session")
	}

	// Config keys not carried over.
	known := map[string]bool{"version": true, "topic": true, "approach": true, "end_goal": true, "difficulty": true,
		"difficulty_override": true, "created": true, "lessons": true, "session_state": true, "sources": true, "notebooklm": true}
	var rest []string
	for k := range keys {
		if !known[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		why, ok := v1Dropped[k]
		if !ok {
			why = "not known to the importer"
		}
		drop(derivedText(k, 80), why)
	}
	if cfg.Created != "" {
		drop("created", "the Topic's creation is the import; v1's date stays in "+v1ConfigTarget)
	}
	for _, s := range walk.specials {
		drop(s, "not a file, folder or link: not copied")
	}
	p.gitattributes = walk.gitattributes

	r := &p.report
	r.Topic = Topic{ID: id, Title: p.title, Goal: p.goal, Path: filepath.Join(c.home, id), State: TopicActive,
		NewCardsPerDay: NewCardsPerDay, Created: nextEventTime(c.now(), time.Time{})}
	r.From, r.Head = srcPath, p.data.Head
	r.Converted, r.Moved, r.Completed, r.Open, r.Dropped = p.data.Converted, p.data.Moved, p.data.Completed, p.data.Open, p.data.Dropped
	r.NextStep = p.data.NextStep
	if r.Sources == nil {
		r.Sources = []Source{}
	}

	if prior, err := c.findImport(p.data); err != nil {
		return nil, err
	} else if prior != "" {
		return nil, &Error{Code: CodeAlreadyExists, Message: fmt.Sprintf(
			"%s was imported already, as Topic %s: study status shows it", srcPath, prior)}
	}
	return p, nil
}

// planV1Sources maps v1's sources and NotebookLM notebook onto Sources and
// the Knowledge base.
func (c *Core) planV1Sources(ctx context.Context, src *os.Root, srcPath string, cfg v1Config, walk workspaceScan,
	p *importPlan, convert, drop func(what, detail string)) error {
	notebooks := []string{}
	addNotebook := func(id string) {
		if id, err := cleanRef("NotebookLM notebook", id); err == nil && id != "" && !slices.Contains(notebooks, id) {
			notebooks = append(notebooks, id)
		}
	}
	if raw := strings.TrimSpace(string(cfg.NotebookLM)); raw != "" && raw != "null" {
		var s string
		var o map[string]any
		switch {
		case json.Unmarshal(cfg.NotebookLM, &s) == nil:
			addNotebook(s)
		case json.Unmarshal(cfg.NotebookLM, &o) == nil:
			addNotebook(firstString(o, "notebook_id", "notebookId", "notebook", "id"))
		default:
			drop("notebooklm", "it is neither a notebook id nor an object with one")
		}
	}
	if len(cfg.Sources) > maxV1Sources {
		return invalidf("%s lists %d sources; the importer takes at most %d", v1ConfigFile, len(cfg.Sources), maxV1Sources)
	}
	k := &knowledgeState{sources: map[string]*Source{}}
	for i, raw := range cfg.Sources {
		what := fmt.Sprintf("sources[%d]", i)
		var entry map[string]any
		var s string
		switch {
		case json.Unmarshal(raw, &s) == nil:
			entry = map[string]any{"path": s}
		case json.Unmarshal(raw, &entry) == nil:
		default:
			drop(what, "it is neither a path nor an object")
			continue
		}
		addNotebook(firstString(entry, "notebook_id", "notebookId", "notebook"))
		title := derivedText(firstString(entry, "title", "name"), maxTitleRunes)
		var srcItem Source
		var outside *foundFile
		switch file, link := firstString(entry, "path", "file", "pdf"), firstString(entry, "url", "link"); {
		case file != "":
			f, ok, why := v1SourceFile(ctx, src, srcPath, file, walk)
			if !ok {
				drop(what, why)
				continue
			}
			srcItem = Source{Kind: SourceFile, TopicPath: f.topicPath, Hash: f.hash, Size: f.size}
			if f.topicPath == "" {
				srcItem.FileName = derivedText(filepath.Base(f.path), maxTitleRunes)
				outside = &f
			}
			if title == "" {
				title = c.fileTitle(f.path)
			}
		case link != "":
			u, err := cleanURL(link)
			if err != nil {
				drop(what, err.Error())
				continue
			}
			srcItem = Source{Kind: SourceURL, URL: u}
			if title == "" {
				title = derivedText(u, maxTitleRunes)
			}
		default:
			drop(what, "it names no file and no URL")
			continue
		}
		dup := false
		for _, id := range k.order {
			o := k.sources[id]
			if srcItem.Kind == SourceFile && o.Hash == srcItem.Hash || srcItem.Kind == SourceURL && o.URL == srcItem.URL {
				dup = true
			}
		}
		if dup {
			drop(what, "the same "+srcItem.Kind+" as an earlier source")
			continue
		}
		if title == "" {
			title = "Source"
		}
		srcItem.Title = title
		if nlm := firstString(entry, "notebooklm_id", "source_id", "notebooklm_source_id"); nlm != "" && len(notebooks) > 0 {
			if ref, err := cleanRef("NotebookLM id", nlm); err == nil {
				srcItem.NotebookLMID, srcItem.NotebookLMNotebook = ref, notebooks[0]
			}
		}
		srcItem.ID = c.newSourceID(title, k)
		if outside != nil {
			p.outside[srcItem.ID] = *outside
		}
		k.sources[srcItem.ID] = &srcItem
		k.order = append(k.order, srcItem.ID)
		p.report.Sources = append(p.report.Sources, srcItem)
	}
	if n := len(p.report.Sources); n > 0 {
		convert("sources", fmt.Sprintf("%d Sources", n))
	}
	switch len(notebooks) {
	case 0:
	default:
		p.report.KnowledgeBase = &KnowledgeBase{Kind: KnowledgeBaseNotebookLM, Notebook: notebooks[0]}
		convert("notebooklm", "the Knowledge base is the NotebookLM notebook "+notebooks[0])
		for _, other := range notebooks[1:] {
			drop("NotebookLM notebook "+other, "a Topic has one Knowledge base: "+notebooks[0]+" is kept")
		}
	}
	return nil
}

// v1SourceFile finds a v1 source's file: inside the workspace, where the
// copy will have it, or elsewhere on this computer.
func v1SourceFile(ctx context.Context, src *os.Root, srcPath, file string, walk workspaceScan) (foundFile, bool, string) {
	if strings.ContainsFunc(file, func(r rune) bool { return r < ' ' || r == 0x7f }) {
		return foundFile{}, false, "its path contains a control character"
	}
	if file == "~" || strings.HasPrefix(file, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			file = filepath.Join(home, strings.TrimPrefix(file, "~"))
		}
	}
	rel := ""
	if filepath.IsAbs(file) {
		if inside(resolvedPath(srcPath), resolvedPath(file)) {
			if r, err := filepath.Rel(resolvedPath(srcPath), resolvedPath(file)); err == nil {
				rel = filepath.ToSlash(r)
			}
		}
	} else if r, err := v1RelPath(file); err == nil {
		rel = r
	} else {
		return foundFile{}, false, err.Error()
	}
	if rel != "" {
		if !walk.files[rel] {
			return foundFile{}, false, "its file " + clip(rel, 80) + " is not in the workspace"
		}
		hash, size, err := hashInRoot(ctx, src, rel)
		if err != nil {
			return foundFile{}, false, "its file " + clip(rel, 80) + " cannot be read: " + err.Error()
		}
		return foundFile{path: filepath.Join(srcPath, filepath.FromSlash(rel)), topicPath: rel, hash: hash, size: size}, true, ""
	}
	hash, size, mod, err := hashFile(ctx, file)
	if err != nil {
		return foundFile{}, false, "its file " + clip(file, 80) + " cannot be read on this computer: " + err.Error()
	}
	return foundFile{path: file, hash: hash, size: size, modTime: mod}, true, ""
}

// hashInRoot hashes a regular file inside root, never following a link out
// of it and never blocking on a FIFO.
func hashInRoot(ctx context.Context, root *os.Root, rel string) (string, int64, error) {
	f, err := root.OpenFile(filepath.FromSlash(rel), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	if !info.Mode().IsRegular() {
		return "", 0, errNotRegular
	}
	h := sha256.New()
	n, err := io.Copy(h, ctxReader{ctx: ctx, r: f})
	if err != nil {
		return "", 0, err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), n, nil
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// v1LessonID is a v1 Lesson's v2 id: lesson-NN, as v1 named its practice
// folder, from its number or its file's number.
func v1LessonID(l v1Lesson) (string, error) {
	n := l.Num
	if n <= 0 {
		if m := v1LessonFileNum.FindStringSubmatch(path.Base(filepath.ToSlash(l.File))); m != nil {
			n, _ = strconv.Atoi(m[1])
		}
	}
	if n <= 0 || n > 999 {
		return "", fmt.Errorf("a Lesson without a number, so no id can be given to it")
	}
	return fmt.Sprintf("lesson-%02d", n), nil
}

// v1RelPath checks a path v1 recorded inside the workspace.
func v1RelPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("no path")
	}
	if strings.ContainsFunc(p, func(r rune) bool { return r < ' ' || r == 0x7f || r == '\\' }) || filepath.IsAbs(p) {
		return "", fmt.Errorf("%q is not a path inside the workspace", clip(p, 80))
	}
	clean := path.Clean(filepath.ToSlash(p))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%q leads outside the workspace", clip(p, 80))
	}
	return clean, nil
}

// v1Proof says what proves a v1 Lesson done: a commit v1 made when it
// completed the Lesson ("[agent] complete lesson 01"), or the FSRS card v1
// added for it (lesson-01). Without either, a Lesson v1 called completed is
// left open: an import never claims more than v1's own records show.
func v1Proof(lessonID string, num int, commits []checkpoint.Commit, cards map[string]bool) string {
	if num <= 0 {
		num, _ = strconv.Atoi(strings.TrimPrefix(lessonID, "lesson-"))
	}
	pattern := regexp.MustCompile(`(?i)\bcomplete(d)?\s+lesson\s+0*` + strconv.Itoa(num) + `\b`)
	for _, c := range commits {
		if pattern.MatchString(c.Subject) {
			short := c.Hash
			if len(short) > 12 {
				short = short[:12]
			}
			return "commit " + short + ": " + clip(derivedText(c.Subject, 120), 120)
		}
	}
	if cards[lessonID] {
		return "v1's card " + lessonID
	}
	return ""
}

// v1Commits reads the workspace's git history, read-only and through the
// hardened checkpoint package.
func v1Commits(ctx context.Context, dir string, hasGit bool) ([]checkpoint.Commit, string, error) {
	if !hasGit {
		return nil, "", nil
	}
	h, err := checkpoint.Log(ctx, dir, maxV1Commits)
	if err != nil {
		return nil, "", err
	}
	return h.Commits, h.Head, nil
}

// v1CardIDs reads the ids of v1's FSRS cards: a list, or {"cards": [...]}.
func v1CardIDs(src *os.Root) (map[string]bool, int) {
	ids := map[string]bool{}
	raw, err := readSmallFile(src, v1CardsFile, maxV1ConfigBytes*8)
	if err != nil {
		return ids, 0
	}
	var list []map[string]any
	if json.Unmarshal(raw, &list) != nil {
		var wrapped struct {
			Cards []map[string]any `json:"cards"`
		}
		if json.Unmarshal(raw, &wrapped) != nil {
			return ids, 0
		}
		list = wrapped.Cards
	}
	for _, card := range list {
		if id, ok := card["id"].(string); ok {
			ids[id] = true
		}
	}
	return ids, len(list)
}

// readSmallFile reads a regular file inside root, refusing one bigger than
// max and never blocking on a FIFO.
func readSmallFile(root *os.Root, name string, max int64) ([]byte, error) {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, internalError("reading "+name, err)
	}
	if !info.Mode().IsRegular() {
		return nil, invalidf("%s is not a regular file", name)
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, internalError("reading "+name, err)
	}
	if int64(len(data)) > max {
		return nil, invalidf("%s is bigger than %d bytes", name, max)
	}
	return data, nil
}

// checkV1Git refuses a git repository a copy would not carry whole: a
// linked worktree or submodule (.git is a file), one that borrows objects
// from elsewhere (alternates), or one in the middle of a git command.
func checkV1Git(src *os.Root) error {
	info, err := src.Lstat(".git")
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return internalError("reading .git", err)
	}
	if !info.IsDir() {
		return &Error{Code: CodeFailedPrecondition, Message: ".git is not a folder (a linked worktree or a submodule): import the main repository's folder"}
	}
	if _, err := src.Lstat(".git/objects/info/alternates"); err == nil {
		return &Error{Code: CodeFailedPrecondition, Message: "the repository borrows objects from another one (alternates): run git repack -a -d in it first"}
	}
	if _, err := src.Lstat(".git/index.lock"); err == nil {
		return &Error{Code: CodeBusy, Message: "a git command is running in the workspace (.git/index.lock exists): try again once it finishes"}
	}
	return nil
}

// workspaceScan is what a walk of the v1 workspace found.
type workspaceScan struct {
	files, dirs   map[string]bool
	specials      []string
	hasGit        bool
	gitattributes []byte
}

// scanWorkspace walks the workspace without following links, refusing a
// link that leads outside it.
func scanWorkspace(ctx context.Context, src *os.Root) (workspaceScan, error) {
	w := workspaceScan{files: map[string]bool{}, dirs: map[string]bool{}}
	n := 0
	err := fs.WalkDir(src.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return internalError("reading the workspace", err)
		}
		if n++; n%256 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if p == "." {
			return nil
		}
		switch t := d.Type(); {
		case t.IsDir():
			w.dirs[p] = true
			if p == ".git" {
				w.hasGit = true
			}
		case t&fs.ModeSymlink != 0:
			if err := checkWorkspaceLink(src, p); err != nil {
				return err
			}
		case t.IsRegular():
			w.files[p] = true
		default:
			w.specials = append(w.specials, p)
		}
		return nil
	})
	if err != nil {
		return w, err
	}
	if w.files[gitattributes] {
		w.gitattributes, _ = readSmallFile(src, gitattributes, maxV1ConfigBytes)
	}
	return w, nil
}

// checkWorkspaceLink refuses a link that leads outside the workspace.
func checkWorkspaceLink(src *os.Root, p string) error {
	target, err := src.Readlink(p)
	if err != nil {
		return internalError("reading the link "+p, err)
	}
	resolved := path.Join(path.Dir(p), filepath.ToSlash(target))
	if filepath.IsAbs(target) || resolved == ".." || strings.HasPrefix(resolved, "../") {
		return &Error{Code: CodeFailedPrecondition, Message: fmt.Sprintf(
			"the link %s leads outside the workspace (to %s): replace it with a copy, or remove it, then import again",
			clip(p, 120), clip(target, 120))}
	}
	return nil
}

// findImport returns the Topic that imported this workspace already, with
// the same config and git HEAD, if any.
func (c *Core) findImport(d topicImportedData) (string, error) {
	home, err := os.OpenRoot(c.home)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", internalError("opening the Study home", err)
	}
	defer home.Close()
	entries, err := fs.ReadDir(home.FS(), ".")
	if err != nil {
		return "", internalError("listing the Study home", err)
	}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || strings.HasPrefix(name, ".") || !isTopic(home, name) {
			continue
		}
		root, err := home.OpenRoot(name)
		if err != nil {
			continue
		}
		h, err := readHistory(root, name)
		root.Close()
		if err != nil {
			continue
		}
		for _, ev := range h.events {
			if ev.Type != eventTopicImported {
				continue
			}
			var prior topicImportedData
			if json.Unmarshal(ev.Data, &prior) == nil && prior.From == d.From && prior.ConfigHash == d.ConfigHash && prior.Head == d.Head {
				return name, nil
			}
		}
	}
	return "", nil
}

// Points where a test can interrupt an import, as a crash would.
const (
	crashImportCopied  = "import-copied"   // the copy is in staging, no Event is written
	crashImportStaged  = "import-staged"   // the Topic is complete in staging, not moved into place
	crashImportInPlace = "import-in-place" // the Topic is in place, not yet saved by a Checkpoint
)

// runImport carries out a planned import. The Topic is assembled under
// .lamplight/tmp and moved into place only once complete, as CreateTopic
// does: an interrupted import leaves no Topic behind, and importing again
// starts over.
func (c *Core) runImport(ctx context.Context, src *os.Root, p *importPlan) (TopicImport, error) {
	home, err := c.openHome()
	if err != nil {
		return TopicImport{}, err
	}
	defer home.Close()
	scratch := filepath.Join(localDir, "tmp")
	if err := home.MkdirAll(scratch, 0o755); err != nil {
		return TopicImport{}, internalError("creating "+scratch, err)
	}
	staging := filepath.Join(scratch, p.id+"."+randomID())
	if err := home.Mkdir(staging, 0o755); err != nil {
		return TopicImport{}, internalError("creating "+staging, err)
	}
	stagingPath := filepath.Join(c.home, staging)
	fail := func(err error) (TopicImport, error) {
		_ = home.RemoveAll(staging)
		return TopicImport{}, err
	}
	root, err := home.OpenRoot(staging)
	if err != nil {
		return fail(internalError("opening "+staging, err))
	}
	defer root.Close()

	if err := copyWorkspace(ctx, src, root); err != nil {
		return fail(err)
	}
	if err := c.crashAt(crashImportCopied); err != nil {
		return TopicImport{}, err
	}
	for _, m := range p.data.Moved {
		if err := root.MkdirAll(path.Dir(m.To), 0o755); err != nil {
			return fail(internalError("creating "+path.Dir(m.To), err))
		}
		if err := root.Rename(filepath.FromSlash(m.From), filepath.FromSlash(m.To)); err != nil {
			return fail(internalError("moving "+m.From, err))
		}
	}
	if _, err := root.Lstat(v1CardsDir); err == nil {
		if err := root.RemoveAll(v1CardsDir); err != nil {
			return fail(internalError("removing "+v1CardsDir, err))
		}
	}

	wall := c.now()
	created := nextEventTime(wall, time.Time{})
	ev, contents, err := c.prepareEvent(&topicView{root: root}, change{
		Type:  eventTopicCreated,
		Data:  topicCreatedData{Title: p.title, Goal: p.goal},
		Items: []string{topicFile, gitattributes},
	}, created, wall)
	if err != nil {
		return fail(err)
	}
	if err := c.appendEvent(root, p.id, ev); err != nil {
		return fail(err)
	}
	for i, it := range ev.Items {
		if err := writeItem(root, it.Item, contents[i]); err != nil {
			return fail(err)
		}
	}
	if len(p.gitattributes) > 0 {
		lamplight, _ := root.ReadFile(gitattributes)
		merged := append(append(lamplight, []byte("\n# Kept from the v1 workspace\n")...), p.gitattributes...)
		if err := writeFileAtomic(root, gitattributes, merged); err != nil {
			return fail(err)
		}
	}
	if err := mergeGitignore(root); err != nil {
		return fail(err)
	}

	var steps []plan
	if p.report.Level != "" {
		steps = append(steps, planLevel(p.id, p.report.Level))
	}
	if p.report.Approach != "" {
		steps = append(steps, planApproach(p.id, p.report.Approach))
	}
	if kb := p.report.KnowledgeBase; kb != nil {
		steps = append(steps, planKnowledgeBase(p.id, *kb))
	}
	for _, source := range p.report.Sources {
		steps = append(steps, func(*replayed, *topicView) (*change, error) {
			return &change{Type: eventSourceAdded, Data: source, Items: []string{sourceItem(source.ID)}}, nil
		})
	}
	steps = append(steps, func(*replayed, *topicView) (*change, error) {
		return &change{Type: eventTopicImported, Data: p.data}, nil
	})
	for _, step := range steps {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if _, err := c.stagedWrite(root, p.id, step); err != nil {
			return fail(err)
		}
	}
	if !p.hasGit {
		if err := gitInit(ctx, stagingPath); err != nil {
			return fail(err)
		}
	}
	if err := c.crashAt(crashImportStaged); err != nil {
		return TopicImport{}, err
	}
	root.Close()
	if err := home.Rename(staging, p.id); err != nil {
		_ = home.RemoveAll(staging)
		if _, statErr := home.Lstat(p.id); statErr == nil {
			return TopicImport{}, alreadyExists(p.id)
		}
		return TopicImport{}, internalError("moving the imported Topic into place", err)
	}
	for id, f := range p.outside {
		c.rememberLocation(p.id, id, f)
	}
	if err := c.setRecentTopic(home, p.id); err != nil {
		c.log.Warn("could not record the most recent Topic", "topic", p.id, "err", err)
	}
	c.log.Info("topic imported from v1", "topic", p.id, "from", p.report.From)

	r := p.report
	if err := c.crashAt(crashImportInPlace); err != nil {
		return TopicImport{}, err
	}
	if res, err := c.Checkpoint(ctx, CheckpointSpec{Topic: p.id, Role: "agent", Message: "Imported from v1"}); err != nil {
		r.CheckpointError = err.Error()
	} else {
		r.Checkpoint = res.Commit
	}
	if topic, err := c.loadTopic(home, p.id); err == nil {
		r.Topic = topic
	}
	return r, nil
}

// stagedWrite records one change in a Topic being assembled under
// .lamplight/tmp: no other process can see it yet, so it needs neither the
// lock nor an intent marker; a crash leaves only the staging folder.
func (c *Core) stagedWrite(root *os.Root, topicID string, p plan) (*event, error) {
	h, err := readHistory(root, topicID)
	if err != nil {
		return nil, err
	}
	s := replayHistory(h)
	ch, err := p(s, newView(root, s))
	if err != nil || ch == nil {
		return nil, err
	}
	wall := c.now()
	ev, contents, err := c.prepareEvent(newView(root, s), *ch, nextEventTime(wall, s.latest), wall)
	if err != nil {
		return nil, err
	}
	if err := c.appendEvent(root, topicID, ev); err != nil {
		return nil, err
	}
	for i, it := range ev.Items {
		if err := writeItem(root, it.Item, contents[i]); err != nil {
			return nil, err
		}
	}
	return &ev, nil
}

// mergeGitignore adds Lamplight's default .gitignore lines the workspace's
// own .gitignore lacks, after the learner's.
func mergeGitignore(root *os.Root) error {
	current, err := root.ReadFile(gitignore)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return internalError("reading "+gitignore, err)
	}
	have := map[string]bool{}
	for _, line := range strings.Split(string(current), "\n") {
		have[strings.TrimSpace(line)] = true
	}
	var add []string
	for _, line := range strings.Split(checkpoint.DefaultGitignore(), "\n") {
		t := strings.TrimSpace(line)
		if t != "" && !strings.HasPrefix(t, "#") && !have[t] {
			add = append(add, line)
		}
	}
	if len(add) == 0 {
		return nil
	}
	out := string(current)
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	if out != "" {
		out += "\n"
	}
	out += "# Added by Lamplight when the v1 workspace was imported\n" + strings.Join(add, "\n") + "\n"
	return writeFileAtomic(root, gitignore, []byte(out))
}

// copyWorkspace copies the workspace, .git included, into dst: folders,
// regular files with their permissions, and links inside the workspace as
// links. Nothing is followed out of it.
func copyWorkspace(ctx context.Context, src, dst *os.Root) error {
	n := 0
	return fs.WalkDir(src.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return internalError("reading the workspace", err)
		}
		if n++; n%256 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if p == "." {
			return nil
		}
		name := filepath.FromSlash(p)
		switch t := d.Type(); {
		case t.IsDir():
			info, err := d.Info()
			if err != nil {
				return internalError("reading "+p, err)
			}
			if err := dst.Mkdir(name, info.Mode().Perm()|0o700); err != nil {
				return internalError("copying "+p, err)
			}
		case t&fs.ModeSymlink != 0:
			if err := checkWorkspaceLink(src, p); err != nil {
				return err
			}
			target, err := src.Readlink(name)
			if err != nil {
				return internalError("reading the link "+p, err)
			}
			if err := dst.Symlink(target, name); err != nil {
				return internalError("copying the link "+p, err)
			}
		case t.IsRegular():
			if err := copyFileInRoots(ctx, src, dst, name); err != nil {
				return err
			}
		}
		return nil
	})
}

func copyFileInRoots(ctx context.Context, src, dst *os.Root, name string) error {
	in, err := src.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return internalError("reading "+name, err)
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return internalError("reading "+name, err)
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	out, err := dst.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return internalError("copying "+name, err)
	}
	if _, err := io.Copy(out, ctxReader{ctx: ctx, r: in}); err != nil {
		out.Close()
		return internalError("copying "+name, err)
	}
	if err := out.Close(); err != nil {
		return internalError("copying "+name, err)
	}
	return dst.Chtimes(name, info.ModTime(), info.ModTime())
}

// replayTopicImported records the import, and marks the Lessons it proved
// done as done: their completion is the import, with no Attempt.
func replayTopicImported(s *replayed, ev event) error {
	var d topicImportedData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return fmt.Errorf("its payload is unreadable: %v", err)
	}
	for _, l := range append(append([]ImportedLesson{}, d.Completed...), d.Open...) {
		if err := validateEntityID("Lesson", l.Lesson); err != nil {
			return err
		}
	}
	if s.imported != nil {
		s.flag(newFlag(FlagConflict, "", []string{s.imported.event, ev.ID}, "",
			fmt.Sprintf("the Topic was imported twice, by Events %s and %s: the first import counts", s.imported.event, ev.ID)))
		return nil
	}
	s.imported = &importState{event: ev.ID, at: wallOf(ev), from: d.From, nextStep: d.NextStep}
	for _, l := range d.Completed {
		ls := s.study.lesson(l.Lesson)
		if ls.completed == nil {
			ls.completed, ls.completedBy = &lessonCompletedData{Lesson: l.Lesson}, ev.ID
		}
	}
	return nil
}

// addImported fills in an imported Topic's import for status.
func addImported(topic *Topic, s *replayed) {
	if s.imported == nil {
		return
	}
	topic.Imported = &TopicImported{From: s.imported.from, At: s.imported.at,
		Adopted: s.study.syllabus != nil, NextStep: s.imported.nextStep}
}
