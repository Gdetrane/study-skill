package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
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
	maxV1CardsBytes  = 8 << 20
	maxV1Commits     = 5000
	maxV1Lessons     = 500
	maxV1Sources     = 200
	v1NoteRunes      = 2000
	// maxImportedShown bounds each list status shows of an import; the
	// topic.imported Event holds them all.
	maxImportedShown = 100
)

// The v1 config versions the importer understands.
const maxV1ConfigVersion = 3

func init() {
	eventKinds[eventTopicImported] = eventKind{apply: applyTopicImported, replay: replayTopicImported}
}

// ImportSpec describes importing a v1 workspace.
type ImportSpec struct {
	// Dir is the v1 workspace: absolute, starting with ~/, or relative to
	// the folder study started in.
	Dir string
	// ID is the new Topic's id. It defaults to the workspace folder's name,
	// so the Topic keeps v1's name.
	ID string
	// NotDone lists Lessons to keep open even when v1's records prove them
	// done, by their v2 ids (lesson-03).
	NotDone []string
	DryRun  bool
}

// TopicImport reports an import: what was converted, moved, proven done,
// left open and dropped. A dry run reports the same, writing nothing.
type TopicImport struct {
	Topic Topic `json:"topic"`
	// From is the v1 workspace, absolute, with links resolved; Head its git
	// HEAD.
	From string `json:"from"`
	Head string `json:"head,omitempty"`
	// Copy is what the copy holds, .git included.
	Copy ImportCopy `json:"copy"`
	// Converted lists what became part of the v2 Topic, Moved the files
	// moved to their v2 place.
	Converted []ImportNote `json:"converted"`
	Moved     []ImportMove `json:"moved"`
	// Completed are the Lessons proven done, Open the others.
	Completed []ImportedLesson `json:"completed"`
	Open      []ImportedLesson `json:"open"`
	Sources   []Source         `json:"sources"`
	Level     string           `json:"level,omitempty"`
	Approach  string           `json:"approach,omitempty"`
	// NextStep is where v1 stopped, for the adoption Session to turn into
	// a Next step.
	NextStep *V1NextStep `json:"v1_next_step,omitempty"`
	// Dropped lists everything that is not carried into v2, and why: config
	// fields, and files and folders the copy leaves out.
	Dropped []ImportNote `json:"dropped"`
	// Checkpoint is the commit that saved the imported Topic; on failure,
	// CheckpointError says why, and the next Checkpoint saves it.
	Checkpoint      string `json:"checkpoint,omitempty"`
	CheckpointError string `json:"checkpoint_error,omitempty"`
	DryRun          bool   `json:"dry_run,omitempty"`
}

// ImportCopy is the size of an import's copy.
type ImportCopy struct {
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
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
	// Proof is what proves a completed Lesson done: the commit, with its
	// subject, or the card.
	Proof string `json:"proof,omitempty"`
	// V1Status is the Lesson's status in v1's config, and why a Lesson v1
	// called done is open.
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
// from, when, whether it has been adopted, which it is once it has a
// Syllabus, and what the adoption works from.
type TopicImported struct {
	From    string    `json:"from"`
	At      time.Time `json:"at"`
	Adopted bool      `json:"adopted"`
	// Plan is where v1's plan is in the Topic, when v1 wrote one (only its
	// project approach does); Config where v1's config is kept.
	Plan     string      `json:"plan,omitempty"`
	Config   string      `json:"config,omitempty"`
	NextStep *V1NextStep `json:"v1_next_step,omitempty"`
	// Completed, Open and Dropped are the import's report, each cut to its
	// first 100 entries; More counts what was cut, which the topic.imported
	// Event in history.jsonl still holds.
	Completed []ImportedLesson `json:"completed"`
	Open      []ImportedLesson `json:"open"`
	Dropped   []ImportNote     `json:"dropped"`
	More      int              `json:"more,omitempty"`
}

// topicImportedData is the payload of a topic.imported Event.
type topicImportedData struct {
	From          string           `json:"from"`
	Head          string           `json:"head,omitempty"`
	ConfigVersion int              `json:"config_version"`
	ConfigHash    string           `json:"config_hash"`
	Plan          string           `json:"plan,omitempty"`
	Config        string           `json:"config,omitempty"`
	NotDone       []string         `json:"not_done,omitempty"`
	Converted     []ImportNote     `json:"converted"`
	Moved         []ImportMove     `json:"moved"`
	Completed     []ImportedLesson `json:"completed"`
	Open          []ImportedLesson `json:"open"`
	Dropped       []ImportNote     `json:"dropped"`
	NextStep      *V1NextStep      `json:"next_step,omitempty"`
}

// importState is a replayed import.
type importState struct {
	event string
	at    time.Time
	data  topicImportedData
}

// v1Config is the part of .study-config.json the importer reads; see v1's
// SKILL.md, "Config Schema (v3)". Lessons are read one at a time, so one bad
// entry is dropped instead of refusing the import.
type v1Config struct {
	Version            int               `json:"version"`
	Topic              string            `json:"topic"`
	Approach           string            `json:"approach"`
	EndGoal            json.RawMessage   `json:"end_goal"`
	Difficulty         string            `json:"difficulty"`
	DifficultyOverride *string           `json:"difficulty_override"`
	Created            string            `json:"created"`
	Lessons            []json.RawMessage `json:"lessons"`
	SessionState       *v1SessionState   `json:"session_state"`
	Sources            []json.RawMessage `json:"sources"`
	NotebookLM         json.RawMessage   `json:"notebooklm"`
}

type v1Lesson struct {
	Num                 int
	Title, File, Status string
}

type v1SessionState struct {
	Phase         string  `json:"phase"`
	PendingAction *string `json:"pending_action"`
	Context       *string `json:"context"`
}

// v1Dropped are the config keys v2 does not carry over, with why. Every key
// v1 documents is here or converted.
var v1Dropped = map[string]string{
	"template":                      "v2 has no templates: the agent sets up the Workbench with the language's own tools",
	"template_mode":                 "v2 has no templates: the agent sets up the Workbench with the language's own tools",
	"mode":                          "v2 has no tutorial mode: the Approach and the Pace shape the Lessons",
	"next_calibration_at_lesson":    "v2 has no calibration rounds: an Assessment ends each Milestone instead",
	"difficulty_override_at_lesson": "the History records when a Level is set; v2 keeps no Lesson number for it",
	"progress":                      "progress is computed from the History",
	"review":                        "v1's review queue goes with its cards",
	"catalog_path":                  "the Library replaces v1's book catalog: study library build",
	"sciagent_skills":               "companion skills are suggested by the lamplight skill when they are installed",
	"sciagent_primary":              "companion skills are suggested by the lamplight skill when they are installed",
}

// v1LessonDropped are the keys of a v1 Lesson v2 does not carry over.
var v1LessonDropped = map[string]string{
	"metrics": "v1's performance metrics (review rounds, hints, ratings): v2 records Attempts, Hints and Assessments instead",
}

var (
	v1LessonFileNum = regexp.MustCompile(`^(\d{1,3})[-_]`)
	v1DoneStatuses  = map[string]bool{"completed": true, "complete": true, "done": true}
	v1Revert        = regexp.MustCompile(`^Revert "(.*)"$`)
)

// clashingNames are v1 files at the workspace's top that would be taken for
// Lamplight's own: they move to notes/v1-<name>.
var clashingNames = []string{syllabusFile, cardsFile, sourcesFile, tasksFile}

// ImportV1 imports a v1 workspace as a new Topic.
func (c *Core) ImportV1(ctx context.Context, spec ImportSpec) (TopicImport, error) {
	r, err := c.importV1(ctx, spec)
	if err != nil && ctx.Err() != nil {
		return TopicImport{}, &Error{Code: CodeCanceled, Err: ctx.Err(),
			Message: "the import was stopped before it finished, so nothing was imported"}
	}
	return r, err
}

func (c *Core) importV1(ctx context.Context, spec ImportSpec) (TopicImport, error) {
	src, err := c.v1Dir(spec.Dir)
	if err != nil {
		return TopicImport{}, err
	}
	srcRoot, err := os.OpenRoot(src)
	if err != nil {
		return TopicImport{}, internalError("opening "+src, err)
	}
	defer srcRoot.Close()
	if spec.DryRun {
		p, err := c.planImport(ctx, srcRoot, src, spec)
		if err != nil {
			return TopicImport{}, err
		}
		p.report.DryRun = true
		return p.report, nil
	}
	home, err := c.openHome()
	if err != nil {
		return TopicImport{}, err
	}
	defer home.Close()
	// One import at a time: the check that this workspace was not imported
	// already holds until the new Topic is in place.
	unlock, err := lockFile(ctx, home, lockPath(importLock), "imports", "importing a v1 workspace")
	if err != nil {
		return TopicImport{}, err
	}
	defer unlock()
	c.sweepStaging(home)
	p, err := c.planImport(ctx, srcRoot, src, spec)
	if err != nil {
		return TopicImport{}, err
	}
	return c.runImport(ctx, home, srcRoot, p)
}

// v1Dir resolves and checks the folder to import: its real path, links
// resolved, so the same workspace is recognised however it is named.
func (c *Core) v1Dir(dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", invalidf("name the v1 workspace to import")
	}
	if strings.ContainsFunc(dir, func(r rune) bool { return r < ' ' || r == 0x7f }) {
		return "", invalidf("the folder's path contains a control character")
	}
	dir = c.expandPath(dir)
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
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", internalError("resolving "+dir, err)
	}
	home := resolvedPath(c.home)
	if inside(home, real) {
		return "", invalidf("%s is inside the Study home already: import a v1 workspace from elsewhere", dir)
	}
	if inside(real, home) {
		return "", invalidf("the Study home %s is inside %s, so the copy would hold the Study home itself: "+
			"move the Study home out of the workspace, or import a folder that does not hold it", c.home, dir)
	}
	return real, nil
}

// inside reports whether dir is home or below it.
func inside(home, dir string) bool {
	rel, err := filepath.Rel(home, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// importPlan is everything an import will do, worked out from the v1
// workspace without writing anything. The dry run reports it; the import
// carries it out as it is.
type importPlan struct {
	id, title, goal string
	data            topicImportedData
	report          TopicImport
	scan            *workspaceScan
	// outside are file Sources outside the workspace, by Source id, to
	// remember where they are on this computer.
	outside map[string]foundFile
}

// planImport reads the v1 workspace and plans the import. It writes
// nothing, and reads the workspace's git history only through the hardened
// checkpoint package.
func (c *Core) planImport(ctx context.Context, src *os.Root, srcPath string, spec ImportSpec) (*importPlan, error) {
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
	if len(cfg.Lessons) > maxV1Lessons {
		return nil, invalidf("%s lists %d Lessons; the importer takes at most %d", v1ConfigFile, len(cfg.Lessons), maxV1Lessons)
	}
	if len(cfg.Sources) > maxV1Sources {
		return nil, invalidf("%s lists %d sources; the importer takes at most %d", v1ConfigFile, len(cfg.Sources), maxV1Sources)
	}
	hasGit, err := checkV1Git(src)
	if err != nil {
		return nil, err
	}
	scan, err := scanWorkspace(ctx, src, srcPath, hasGit)
	if err != nil {
		return nil, err
	}
	if len(scan.gitLocks) > 0 {
		lock := scan.gitLocks[0]
		return nil, &Error{Code: CodeBusy, Message: fmt.Sprintf("a git command is writing to the workspace's repository "+
			"(%s exists): try again once it finishes. If no git command is running, one that stopped halfway left it "+
			"behind: delete %s, then import again", lock, filepath.Join(srcPath, filepath.FromSlash(lock)))}
	}
	for _, name := range []string{gitignore, gitattributes} {
		if scan.kinds[name] == entryDir {
			return nil, &Error{Code: CodeFailedPrecondition, Message: fmt.Sprintf(
				"%s in the workspace is a folder, where git expects a file: rename it, then import again", name)}
		}
	}

	p := &importPlan{scan: scan, outside: map[string]foundFile{}}
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
	id := spec.ID
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
	notDone := map[string]bool{}
	for _, l := range spec.NotDone {
		if err := validateEntityID("Lesson", l); err != nil {
			return nil, err
		}
		notDone[l] = true
	}
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
	difficulty := strings.ToLower(strings.TrimSpace(cfg.Difficulty))
	level, from := difficulty, "difficulty"
	if cfg.DifficultyOverride != nil && strings.TrimSpace(*cfg.DifficultyOverride) != "" {
		level, from = strings.ToLower(strings.TrimSpace(*cfg.DifficultyOverride)), "difficulty_override"
		if difficulty != "" {
			drop("difficulty", "difficulty_override, the learner's choice, replaces it")
		}
	}
	if level != "" {
		if _, err := checkLevel(level); err != nil {
			drop(from, fmt.Sprintf("%q is not a Level", clip(level, 40)))
		} else {
			p.report.Level = level
			convert("level", "from "+from+": "+level+", recorded as the import's until an Assessment or the learner confirms it")
		}
	}
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

	// The git history, for the proofs.
	commits, head, logNote := v1Commits(ctx, srcPath, hasGit)
	p.data.Head = head
	if logNote != "" {
		drop("git history", logNote)
	}
	cardIDs, cardCount := v1CardIDs(src)

	// Files that would be taken for Lamplight's own move out of the way.
	moves := newMovePlan(scan)
	for _, name := range clashingNames {
		if scan.kinds[name] == 0 || scan.skipped[name] != "" {
			continue
		}
		to := "notes/v1-" + name
		if why := moves.problem(name, to); why != "" {
			return nil, &Error{Code: CodeFailedPrecondition, Message: fmt.Sprintf(
				"%s in the workspace would be taken for Lamplight's own, and it cannot move to %s (%s): "+
					"rename it, then import again", name, to, why)}
		}
		moves.add(name, to, "")
		convert(name, "kept as "+to+": Lamplight keeps its own "+name+" at the top of the Topic")
	}

	// Lessons: v1's names become ids, their files move to lessons/<id>.md.
	seen := map[string]bool{}
	lessonKeys := map[string]int{}
	for i, raw := range cfg.Lessons {
		what := fmt.Sprintf("lessons[%d]", i)
		l, err := decodeV1Lesson(raw, lessonKeys, func(field, why string) { drop(what+"."+field, why) })
		if err != nil {
			drop(what, err.Error())
			continue
		}
		lessonID, err := v1LessonID(l)
		if err != nil {
			drop(what, err.Error())
			continue
		}
		if seen[lessonID] {
			drop(what, "a second Lesson numbered like "+lessonID)
			continue
		}
		seen[lessonID] = true
		title := derivedText(l.Title, maxTitleRunes)
		if title == "" {
			title = lessonID
		}
		il := ImportedLesson{Lesson: lessonID, Title: title, V1Status: derivedText(l.Status, 40)}
		hasFile := moves.lessonFile(lessonID, l.File, drop)
		if v1DoneStatuses[strings.ToLower(strings.TrimSpace(l.Status))] {
			proof := v1Proof(lessonID, commits, cardIDs)
			switch {
			case notDone[lessonID]:
				il.V1Status += " (kept open: --not-done)"
			case proof == "":
				il.V1Status += fmt.Sprintf(" (not proven: no %q commit and no card %s)", v1CompletionSubject(lessonID), lessonID)
			case !hasFile:
				il.V1Status += " (not proven: v1 names no Lesson file under lessons/ for it)"
			default:
				il.Proof = proof
				p.data.Completed = append(p.data.Completed, il)
				continue
			}
		}
		p.data.Open = append(p.data.Open, il)
	}
	if len(seen) > 0 {
		convert("lessons", fmt.Sprintf("%d Lessons kept under their v1 names", len(seen)))
	}
	for _, k := range slices.Sorted(maps.Keys(lessonKeys)) {
		why, ok := v1LessonDropped[k]
		if !ok {
			why = "not known to the importer; it stays in v1's config"
		}
		drop("lessons[]."+derivedText(k, 80), fmt.Sprintf("%s (in %d Lessons)", why, lessonKeys[k]))
	}
	for _, l := range spec.NotDone {
		if !seen[l] {
			return nil, invalidf("--not-done names %s, which the v1 config does not list", l)
		}
	}
	p.data.NotDone = slices.Sorted(maps.Keys(notDone))

	// The plan and the config, kept for the adoption Session.
	switch k := scan.kinds[v1PlanFile]; {
	case k == 0:
	case moves.from[v1PlanFile] != "":
		p.data.Plan = lessonFile(moves.from[v1PlanFile])
	case k != entryFile || scan.skipped[v1PlanFile] != "":
		drop("moving "+v1PlanFile, "it is not a file: it stays where it is")
	default:
		if why := moves.problem(v1PlanFile, v1PlanTarget); why != "" {
			drop("moving "+v1PlanFile, why+": the plan stays where it is")
			p.data.Plan = v1PlanFile
		} else {
			moves.add(v1PlanFile, v1PlanTarget, "")
			p.data.Plan = v1PlanTarget
			convert("plan", "the v1 plan is kept as "+v1PlanTarget+" for the adoption Session")
		}
	}
	if why := moves.problem(v1ConfigFile, v1ConfigTarget); why != "" {
		drop("moving "+v1ConfigFile, why+": the config stays where it is")
		p.data.Config = v1ConfigFile
	} else {
		moves.add(v1ConfigFile, v1ConfigTarget, "")
		p.data.Config = v1ConfigTarget
		convert("config", v1ConfigFile+" becomes "+topicFile+"; the original is kept as "+v1ConfigTarget)
	}
	p.data.Moved = moves.moves
	if scan.kinds[v1CardsDir] != 0 {
		scan.skipped[v1CardsDir] = fmt.Sprintf("v1's lesson-level cards (%d): not carried over, and written again "+
			"in the adoption Session; they stay in the git history", cardCount)
	}

	// Sources and the Knowledge base.
	if err := c.planV1Sources(ctx, src, srcPath, cfg, p, convert, drop); err != nil {
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
			why = "not known to the importer; it stays in v1's config"
		}
		drop(derivedText(k, 80), why)
	}
	if cfg.Created != "" {
		drop("created", "the Topic's creation is the import; v1's date stays in v1's config")
	}

	// The git files, merged.
	if scan.kinds[gitignore] != 0 && scan.skipped[gitignore] == "" {
		convert(gitignore, "the workspace's lines first, then Lamplight's, ending with lines that keep its state files tracked")
	}
	if scan.kinds[gitattributes] != 0 && scan.skipped[gitattributes] == "" {
		convert(gitattributes, "the workspace's lines first, then Lamplight's merge rules for its state files, last so they win")
	}

	// What the copy leaves out.
	for _, s := range slices.Sorted(maps.Keys(scan.skipped)) {
		drop(s, scan.skipped[s])
	}

	r := &p.report
	r.Topic = Topic{ID: id, Title: p.title, Goal: p.goal, Path: filepath.Join(c.home, id), State: TopicActive,
		NewCardsPerDay: NewCardsPerDay, Created: nextEventTime(c.now(), time.Time{})}
	r.From, r.Head = srcPath, p.data.Head
	r.Copy = ImportCopy{Files: scan.files, Bytes: scan.bytes}
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

// decodeV1Lesson reads one entry of v1's lessons. A num must be a whole
// number, written as one ("1", 1.0); a field that is not text is dropped
// through drop; keys it does not read are counted in keys.
func decodeV1Lesson(raw json.RawMessage, keys map[string]int, drop func(field, why string)) (v1Lesson, error) {
	var entry map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entry); err != nil || entry == nil {
		return v1Lesson{}, fmt.Errorf("it is not an object")
	}
	var l v1Lesson
	for _, k := range slices.Sorted(maps.Keys(entry)) {
		v := entry[k]
		switch k {
		case "num":
			n, ok := v1Number(v)
			if !ok {
				return v1Lesson{}, fmt.Errorf("its num, %s, is not a whole number", clip(string(v), 40))
			}
			l.Num = n
		case "title", "file", "status":
			var s string
			if string(v) != "null" && json.Unmarshal(v, &s) != nil {
				drop(k, "it is not text")
				continue
			}
			switch k {
			case "title":
				l.Title = s
			case "file":
				l.File = s
			default:
				l.Status = s
			}
		default:
			keys[k]++
		}
	}
	return l, nil
}

// v1Number reads a Lesson number: a JSON number with no fraction, or digits
// in a string; null is no number.
func v1Number(raw json.RawMessage) (int, bool) {
	s := strings.TrimSpace(string(raw))
	if s == "null" {
		return 0, true
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		s = strings.TrimSpace(str)
		if s == "" || strings.ContainsFunc(s, func(r rune) bool { return r < '0' || r > '9' }) {
			return 0, false
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f != math.Trunc(f) || f < 0 || f > 999 {
		return 0, false
	}
	return int(f), true
}

// v1NotebookLMDropped is why an import leaves out what v1 recorded about
// NotebookLM: its notebooks, and each source's ids in one.
const v1NotebookLMDropped = "Lamplight v2 does not use NotebookLM (ADR-0011)"

// v1NotebookLMKeys are the keys under which a v1 source named its notebook
// and its id there.
var v1NotebookLMKeys = []string{"notebook_id", "notebookId", "notebook", "notebook_url",
	"notebooklm_id", "source_id", "notebooklm_source_id"}

// planV1Sources maps v1's sources onto Sources: the files and the web pages.
// What v1 recorded about NotebookLM is listed as dropped (ADR-0011), and the
// import chooses no Knowledge base.
func (c *Core) planV1Sources(ctx context.Context, src *os.Root, srcPath string, cfg v1Config,
	p *importPlan, convert, drop func(what, detail string)) error {
	if raw := strings.TrimSpace(string(cfg.NotebookLM)); raw != "" && raw != "null" {
		drop("notebooklm", v1NotebookLMDropped)
	}
	type planned struct {
		src  Source
		what string
		// notebookLM are the entry's NotebookLM keys, reported once the
		// Source is known to be imported.
		notebookLM []string
		outside    *foundFile
	}
	var sources []planned
	for i, raw := range cfg.Sources {
		what := fmt.Sprintf("sources[%d]", i)
		var entry map[string]any
		var s string
		switch {
		case json.Unmarshal(raw, &s) == nil:
			entry = map[string]any{"path": s}
		case json.Unmarshal(raw, &entry) == nil && entry != nil:
		default:
			drop(what, "it is neither a path nor an object")
			continue
		}
		var pl planned
		pl.what = what
		for _, k := range v1NotebookLMKeys {
			if v, ok := entry[k]; ok && v != nil {
				pl.notebookLM = append(pl.notebookLM, k)
			}
		}
		title := derivedText(firstString(entry, "title", "name"), maxTitleRunes)
		switch file, link := firstString(entry, "path", "file", "pdf"), firstString(entry, "url", "link"); {
		case file != "":
			f, ok, why := c.v1SourceFile(ctx, src, srcPath, file, p.scan)
			if !ok {
				drop(what, why)
				continue
			}
			pl.src = Source{Kind: SourceFile, TopicPath: f.topicPath, Hash: f.hash, Size: f.size}
			if f.topicPath == "" {
				pl.src.FileName = derivedText(filepath.Base(f.path), maxTitleRunes)
				pl.outside = &f
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
			pl.src = Source{Kind: SourceURL, URL: u}
			if title == "" {
				title = derivedText(u, maxTitleRunes)
			}
		default:
			drop(what, "it names no file and no URL")
			continue
		}
		if title == "" {
			title = "Source"
		}
		pl.src.Title = title
		sources = append(sources, pl)
	}

	k := &knowledgeState{sources: map[string]*Source{}}
	for _, pl := range sources {
		srcItem := pl.src
		dup := false
		for _, id := range k.order {
			o := k.sources[id]
			if srcItem.Kind == SourceFile && o.Hash == srcItem.Hash || srcItem.Kind == SourceURL && o.URL == srcItem.URL {
				dup = true
			}
		}
		if dup {
			drop(pl.what, "the same "+srcItem.Kind+" as an earlier source")
			continue
		}
		if len(pl.notebookLM) > 0 {
			drop(pl.what+"."+strings.Join(pl.notebookLM, " and "), v1NotebookLMDropped+"; the Source itself is imported")
		}
		srcItem.ID = c.newSourceID(srcItem.Title, k)
		if pl.outside != nil {
			p.outside[srcItem.ID] = *pl.outside
		}
		k.sources[srcItem.ID] = &srcItem
		k.order = append(k.order, srcItem.ID)
		p.report.Sources = append(p.report.Sources, srcItem)
	}
	if n := len(p.report.Sources); n > 0 {
		convert("sources", fmt.Sprintf("%d Sources", n))
	}
	return nil
}

// v1SourceFile finds a v1 source's file: inside the workspace, where the
// copy will have it, or elsewhere on this computer.
func (c *Core) v1SourceFile(ctx context.Context, src *os.Root, srcPath, file string, scan *workspaceScan) (foundFile, bool, string) {
	if strings.ContainsFunc(file, func(r rune) bool { return r < ' ' || r == 0x7f }) {
		return foundFile{}, false, "its path contains a control character"
	}
	rel := ""
	if strings.HasPrefix(file, "~") || filepath.IsAbs(file) {
		file = c.expandPath(file)
		if inside(srcPath, resolvedPath(file)) {
			if r, err := filepath.Rel(srcPath, resolvedPath(file)); err == nil {
				rel = filepath.ToSlash(r)
			}
		}
	} else if r, err := v1RelPath(file); err == nil {
		rel = r
	} else {
		return foundFile{}, false, err.Error()
	}
	if rel != "" {
		if scan.kinds[rel] != entryFile || scan.skippedAt(rel) {
			return foundFile{}, false, "its file " + clip(rel, 80) + " is not a file the copy holds"
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

// v1CompletionSubject is the subject of the commit v1 makes when it
// completes a Lesson.
func v1CompletionSubject(lessonID string) string {
	return "[agent] complete lesson " + strings.TrimPrefix(lessonID, "lesson-")
}

// v1Proof says what proves a v1 Lesson done. v1's Lesson Completion
// Contract (references/workspace-lifecycle.md in v1) adds an FSRS card with
// id lesson-NN first, and ends with a commit "[agent] complete lesson NN".
// The newest commit about the Lesson's completion counts, matched whole: a
// revert of it proves nothing. Then the card. Without either, a Lesson v1
// called completed is left open: an import never claims more than v1's own
// records show.
func v1Proof(lessonID string, commits []checkpoint.Commit, cards map[string]bool) string {
	num, _ := strconv.Atoi(strings.TrimPrefix(lessonID, "lesson-"))
	done := regexp.MustCompile(`^\[agent\]\s+complete(d)?\s+lesson\s+0*` + strconv.Itoa(num) + `\s*$`)
	for _, c := range commits {
		subject := strings.TrimSpace(c.Subject)
		if m := v1Revert.FindStringSubmatch(subject); m != nil && done.MatchString(m[1]) {
			return ""
		}
		if done.MatchString(subject) {
			short := c.Hash
			if len(short) > 12 {
				short = short[:12]
			}
			return "commit " + short + ": " + derivedText(subject, 120)
		}
	}
	if cards[lessonID] {
		return "v1's card " + lessonID
	}
	return ""
}

// v1Commits reads the workspace's git history, read-only and through the
// hardened checkpoint package. note says what the proofs miss.
func v1Commits(ctx context.Context, dir string, hasGit bool) (commits []checkpoint.Commit, head, note string) {
	if !hasGit {
		return nil, "", "the workspace is not a git repository: the Topic starts a new one"
	}
	h, err := checkpoint.Log(ctx, dir, maxV1Commits)
	switch {
	case err != nil:
		return nil, "", "its history could not be read, so no commit proves a Lesson done: " + err.Error()
	case h.Truncated || len(h.Commits) >= maxV1Commits:
		note = fmt.Sprintf("only the newest %d commits were read for proofs; the copy keeps them all", len(h.Commits))
	}
	return h.Commits, h.Head, note
}

// v1CardIDs reads the ids of v1's FSRS cards: a list, or {"cards": [...]}.
func v1CardIDs(src *os.Root) (map[string]bool, int) {
	ids := map[string]bool{}
	raw, err := readSmallFile(src, v1CardsFile, maxV1CardsBytes)
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

// planImportedLevel plans setting the Level v1's difficulty gave, recorded
// as the import's: v1's estimate, not the learner's choice.
func planImportedLevel(topicID, level string) plan {
	return func(s *replayed, view *topicView) (*change, error) {
		ch, err := planLevel(topicID, level)(s, view)
		if ch != nil {
			ch.Data = levelSetData{Level: level, Source: LevelFromImport}
		}
		return ch, err
	}
}

// applyTopicImported writes the import's .gitattributes: the workspace's
// lines, then Lamplight's.
func applyTopicImported(ev event, item string, current []byte, exists bool) ([]byte, bool, error) {
	if item != gitattributes {
		return nil, false, corruptf("Event %s (%s) cannot edit %s", ev.ID, ev.Type, item)
	}
	return mergeAttributes(current, exists), true, nil
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
	s.imported = &importState{event: ev.ID, at: wallOf(ev), data: d}
	for _, l := range d.Completed {
		ls := s.study.lesson(l.Lesson)
		if ls.completed == nil {
			ls.completed, ls.completedBy = &lessonCompletedData{Lesson: l.Lesson}, ev.ID
		}
	}
	return nil
}

// adoptionSource says what the adoption Session plans the Syllabus from:
// v1's plan, which only its project approach wrote, or else v1's lesson
// list and the learner's notes.
func adoptionSource(imp *TopicImported) string {
	if imp.Plan != "" {
		return "the v1 plan in " + imp.Plan
	}
	config := imp.Config
	if config == "" {
		config = v1ConfigTarget
	}
	return "v1's lesson list in " + config + " and the notes in notes/"
}

// addImported fills in an imported Topic's import for status.
func addImported(topic *Topic, s *replayed) {
	if s.imported == nil {
		return
	}
	d := s.imported.data
	more := 0
	shown := func(n int) int {
		if n > maxImportedShown {
			more += n - maxImportedShown
			return maxImportedShown
		}
		return n
	}
	topic.Imported = &TopicImported{From: d.From, At: s.imported.at, Adopted: s.study.syllabus != nil,
		Plan: d.Plan, Config: d.Config, NextStep: d.NextStep,
		Completed: append([]ImportedLesson{}, d.Completed[:shown(len(d.Completed))]...),
		Open:      append([]ImportedLesson{}, d.Open[:shown(len(d.Open))]...),
		Dropped:   append([]ImportNote{}, d.Dropped[:shown(len(d.Dropped))]...)}
	topic.Imported.More = more
}
