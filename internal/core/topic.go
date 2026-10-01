package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
	"golang.org/x/text/unicode/norm"

	"github.com/mordor-forge/lamplight/v2/internal/checkpoint"
)

const (
	topicFile     = "topic.toml"
	gitattributes = ".gitattributes"
	gitignore     = ".gitignore"
	maxTitleRunes = 200
	maxGoalRunes  = 500
)

// Event types recorded in a Topic's History.
const (
	eventTopicCreated = "topic.created"
	eventTopicUpdated = "topic.updated"
)

// Topic is one subject the learner is studying, with its own folder and git
// repository inside the Study home.
type Topic struct {
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Goal    string    `json:"goal,omitempty"`
	Path    string    `json:"path"`
	Created time.Time `json:"created"`
	// KnowledgeBase is where the Topic's Sources are searched for Evidence;
	// absent until one is chosen.
	KnowledgeBase *KnowledgeBase `json:"knowledge_base,omitempty"`
	// Flags are what replaying the History found that needs the learner's
	// attention.
	Flags []Flag `json:"flags,omitempty"`
	// Resume is where the learner stopped, once the Topic has a Syllabus
	// or a Session: show its Next step first.
	Resume *ResumePoint `json:"resume,omitempty"`
}

// TopicSpec describes a Topic to create.
type TopicSpec struct {
	// Title names the Topic, for example "Linear algebra". Required.
	Title string
	// ID is the Topic's folder name. Derived from Title when empty.
	ID string
	// Goal is what the learner wants to be able to do at the end. Optional.
	Goal string
	// DryRun validates the request and returns the Topic that would be
	// created, without writing anything.
	DryRun bool
}

// TopicChanges describes changes to a Topic's settings. Nil fields stay as
// they are.
type TopicChanges struct {
	Title *string
	// Goal replaces the goal; an empty goal removes it.
	Goal *string
	// KnowledgeBase chooses the Topic's Knowledge base.
	KnowledgeBase *KnowledgeBase
	// DryRun validates the request and returns the Topic as it would be,
	// without writing anything.
	DryRun bool
}

// TopicUpdate is the result of UpdateTopic.
type TopicUpdate struct {
	Topic Topic `json:"topic"`
	// Changed is false when the Topic already had the requested values, so
	// nothing was recorded.
	Changed bool `json:"changed"`
}

// topicSettings is the content of topic.toml.
type topicSettings struct {
	Format int    `toml:"format"`
	Title  string `toml:"title"`
	Goal   string `toml:"goal,omitempty"`

	// extra holds settings this version of study does not know, such as
	// ones a newer version added within the same format, or the learner's
	// own. They are written back unchanged.
	extra map[string]any
}

// topicCreatedData is the payload of a topic.created Event.
type topicCreatedData struct {
	Title string `json:"title"`
	Goal  string `json:"goal,omitempty"`
}

// topicUpdatedData is the payload of a topic.updated Event: the fields that
// changed, with their new values.
type topicUpdatedData struct {
	Title *string `json:"title,omitempty"`
	Goal  *string `json:"goal,omitempty"`
}

var topicIDPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// CreateTopic creates a Topic folder in the Study home with its settings, its
// History and its git repository, and makes it the most recent Topic.
//
// The folder is assembled under .lamplight/tmp and renamed into place only
// once it is complete, so an interrupted creation never leaves a half-made
// Topic behind, and needs neither the Topic lock nor an intent marker.
func (c *Core) CreateTopic(ctx context.Context, spec TopicSpec) (Topic, error) {
	title, err := cleanText("title", spec.Title, maxTitleRunes)
	if err != nil {
		return Topic{}, err
	}
	if title == "" {
		return Topic{}, invalidf("a Topic needs a title")
	}
	goal, err := cleanText("goal", spec.Goal, maxGoalRunes)
	if err != nil {
		return Topic{}, err
	}
	id := spec.ID
	if id == "" {
		if id = slugify(title); id == "" {
			return Topic{}, invalidf("cannot derive a folder name from %q: pass an id such as \"linear-algebra\"", title)
		}
	}
	if err := validateTopicID(id); err != nil {
		return Topic{}, err
	}
	wall := c.now()
	topic := Topic{ID: id, Title: title, Goal: goal, Path: filepath.Join(c.home, id),
		Created: nextEventTime(wall, time.Time{})}
	if _, err := os.Lstat(topic.Path); err == nil {
		return Topic{}, alreadyExists(id)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Topic{}, internalError("checking "+topic.Path, err)
	}
	if spec.DryRun {
		return topic, nil
	}

	home, err := c.openHome()
	if err != nil {
		return Topic{}, err
	}
	defer home.Close()
	scratch := filepath.Join(localDir, "tmp")
	if err := home.MkdirAll(scratch, 0o755); err != nil {
		return Topic{}, internalError("creating "+scratch, err)
	}
	staging := filepath.Join(scratch, id+"."+randomID())
	if err := home.Mkdir(staging, 0o755); err != nil {
		return Topic{}, internalError("creating "+staging, err)
	}
	if err := c.initTopic(ctx, home, staging, topic, wall); err != nil {
		_ = home.RemoveAll(staging)
		return Topic{}, err
	}
	if err := home.Rename(staging, id); err != nil {
		_ = home.RemoveAll(staging)
		if _, statErr := home.Lstat(id); statErr == nil {
			return Topic{}, alreadyExists(id)
		}
		return Topic{}, internalError("moving the new Topic into place", err)
	}
	// The most recent Topic is local convenience state: failing to record it
	// must not turn a successful creation into an error.
	if err := c.setRecentTopic(home, id); err != nil {
		c.log.Warn("could not record the most recent Topic", "topic", id, "err", err)
	}
	c.log.Info("topic created", "topic", id, "path", topic.Path)
	return topic, nil
}

// initTopic writes a new Topic into dir: the topic.created Event, then the
// content it creates, then the git repository.
func (c *Core) initTopic(ctx context.Context, home *os.Root, dir string, topic Topic, wall time.Time) error {
	root, err := home.OpenRoot(dir)
	if err != nil {
		return internalError("opening "+dir, err)
	}
	defer root.Close()

	ev, contents, err := c.prepareEvent(&topicView{root: root}, change{
		Type:  eventTopicCreated,
		Data:  topicCreatedData{Title: topic.Title, Goal: topic.Goal},
		Items: []string{topicFile, gitattributes},
	}, topic.Created, wall)
	if err != nil {
		return err
	}
	if err := c.appendEvent(root, topic.ID, ev); err != nil {
		return err
	}
	for i, it := range ev.Items {
		if err := writeItem(root, it.Item, contents[i]); err != nil {
			return err
		}
	}
	// The .gitignore is the learner's to edit, so it is written once here
	// rather than recorded as an item of the Event.
	if err := writeFileAtomic(root, gitignore, []byte(checkpoint.DefaultGitignore())); err != nil {
		return err
	}
	return gitInit(ctx, filepath.Join(c.home, dir))
}

// UpdateTopic changes a Topic's title, goal or Knowledge base. Asking for
// the values it already has changes nothing and records no Event.
//
// The Knowledge base is recorded by its own Event type, so changing it with
// the title or goal records two Events. Everything is validated first, so
// the second write can only fail for reasons such as a full disk; the error
// then says the first change was recorded.
func (c *Core) UpdateTopic(ctx context.Context, id string, changes TopicChanges) (TopicUpdate, error) {
	var want topicUpdatedData
	if changes.Title != nil {
		title, err := cleanText("title", *changes.Title, maxTitleRunes)
		if err != nil {
			return TopicUpdate{}, err
		}
		if title == "" {
			return TopicUpdate{}, invalidf("a Topic needs a title")
		}
		want.Title = &title
	}
	if changes.Goal != nil {
		goal, err := cleanText("goal", *changes.Goal, maxGoalRunes)
		if err != nil {
			return TopicUpdate{}, err
		}
		want.Goal = &goal
	}
	var kb *KnowledgeBase
	if changes.KnowledgeBase != nil {
		checked, err := checkKnowledgeBase(*changes.KnowledgeBase)
		if err != nil {
			return TopicUpdate{}, err
		}
		kb = &checked
	}
	settingsChange := want.Title != nil || want.Goal != nil
	if !settingsChange && kb == nil {
		return TopicUpdate{}, invalidf("nothing to change: give a new title, goal or Knowledge base")
	}
	changed := false
	// The settings as the write finds them: after recovery, in a dry run too.
	var current topicSettings
	// applied is what the first Event changed, for an error after it.
	var applied topicUpdatedData
	if settingsChange {
		ev, err := c.writeTopic(ctx, id, func(_ *replayed, view *topicView) (*change, error) {
			data, exists, err := view.read(topicFile)
			if err != nil {
				return nil, err
			}
			if !exists {
				return nil, corruptf("%s of %s is missing", topicFile, id)
			}
			if current, err = parseTopicSettings(data, filepath.Join(id, topicFile)); err != nil {
				return nil, err
			}
			var diff topicUpdatedData
			if want.Title != nil && *want.Title != current.Title {
				diff.Title = want.Title
			}
			if want.Goal != nil && *want.Goal != current.Goal {
				diff.Goal = want.Goal
			}
			if diff.Title == nil && diff.Goal == nil {
				return nil, nil
			}
			applied = diff
			return &change{Type: eventTopicUpdated, Data: diff, Items: []string{topicFile}}, nil
		}, changes.DryRun)
		if err != nil {
			return TopicUpdate{}, err
		}
		changed = ev != nil
	}
	if kb != nil {
		ev, err := c.writeTopic(ctx, id, planKnowledgeBase(id, *kb), changes.DryRun)
		if err != nil {
			if changed {
				what := "title and goal of " + id + " were"
				switch {
				case applied.Goal == nil:
					what = "title of " + id + " was"
				case applied.Title == nil:
					what = "goal of " + id + " was"
				}
				return TopicUpdate{}, &Error{Code: CodeOf(err), Err: err, Message: fmt.Sprintf(
					"the %s changed, but not its Knowledge base: %v", what, err)}
			}
			return TopicUpdate{}, err
		}
		changed = changed || ev != nil
	}
	topic, err := c.readTopic(id)
	if err != nil {
		return TopicUpdate{}, err
	}
	if changes.DryRun {
		if settingsChange {
			topic.Title, topic.Goal = current.Title, current.Goal
		}
		if want.Title != nil {
			topic.Title = *want.Title
		}
		if want.Goal != nil {
			topic.Goal = *want.Goal
		}
		if kb != nil {
			topic.KnowledgeBase = kb
		}
	}
	return TopicUpdate{Topic: topic, Changed: changed}, nil
}

func applyTopicCreated(ev event, item string, _ []byte, _ bool) ([]byte, bool, error) {
	var d topicCreatedData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return nil, false, corruptf("Event %s has an unreadable payload: %v", ev.ID, err)
	}
	switch item {
	case topicFile:
		data, err := encodeTopicSettings(topicSettings{Title: d.Title, Goal: d.Goal})
		return data, err == nil, err
	case gitattributes:
		// These files hold one record per line, so a union merge keeps both
		// machines' lines; replay and status flag what conflicts.
		return []byte(historyFile + " merge=union\n" + cardsFile + " merge=union\n" + sourcesFile + " merge=union\n"), true, nil
	}
	return nil, false, corruptf("Event %s (%s) cannot edit %s", ev.ID, ev.Type, item)
}

func applyTopicUpdated(ev event, item string, current []byte, exists bool) ([]byte, bool, error) {
	if item != topicFile {
		return nil, false, corruptf("Event %s (%s) cannot edit %s", ev.ID, ev.Type, item)
	}
	if !exists {
		return nil, false, corruptf("%s is missing", topicFile)
	}
	var d topicUpdatedData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return nil, false, corruptf("Event %s has an unreadable payload: %v", ev.ID, err)
	}
	settings, err := parseTopicSettings(current, topicFile)
	if err != nil {
		return nil, false, err
	}
	if d.Title != nil {
		settings.Title = *d.Title
	}
	if d.Goal != nil {
		settings.Goal = *d.Goal
	}
	data, err := encodeTopicSettings(settings)
	return data, err == nil, err
}

func replayTopicCreated(s *replayed, ev event) error {
	if s.created.IsZero() {
		s.created = ev.Time
	}
	return nil
}

func replayTopicUpdated(s *replayed, _ event) error {
	if s.created.IsZero() {
		return fmt.Errorf("%w: the Topic's creation", errUnknownItem)
	}
	return nil
}

// parseTopicSettings reads topic.toml; where names it in errors.
func parseTopicSettings(data []byte, where string) (topicSettings, error) {
	var settings topicSettings
	md, err := toml.Decode(string(data), &settings)
	if err != nil {
		return settings, corruptf("%s is not valid TOML: %v", where, err)
	}
	if settings.Format > FormatVersion {
		return settings, newerFormat(where, settings.Format)
	}
	if settings.Format < 1 {
		return settings, corruptf("%s has no format number: add format = %d", where, FormatVersion)
	}
	if len(md.Undecoded()) > 0 {
		if _, err := toml.Decode(string(data), &settings.extra); err != nil {
			return settings, corruptf("%s is not valid TOML: %v", where, err)
		}
		for _, known := range []string{"format", "title", "goal"} {
			delete(settings.extra, known)
		}
	}
	return settings, nil
}

// encodeTopicSettings writes topic.toml in the current format: the settings
// Lamplight knows, then any others, sorted, so the same settings always
// give the same bytes.
func encodeTopicSettings(settings topicSettings) ([]byte, error) {
	settings.Format = FormatVersion
	var buf bytes.Buffer
	buf.WriteString("# Topic settings. Lamplight rewrites this file; comments are not kept.\n")
	if err := toml.NewEncoder(&buf).Encode(settings); err != nil {
		return nil, internalError("encoding "+topicFile, err)
	}
	if len(settings.extra) > 0 {
		// The known settings are plain keys, so the others, tables
		// included, can follow them.
		if err := toml.NewEncoder(&buf).Encode(settings.extra); err != nil {
			return nil, internalError("encoding "+topicFile, err)
		}
	}
	return buf.Bytes(), nil
}

// gitInit makes dir a git repository. Commits are made later by Checkpoints,
// which never run programs named by repository configuration.
func gitInit(ctx context.Context, dir string) error {
	cmd := gitCommand(ctx, dir, "init", "--quiet", "--initial-branch=main")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return &Error{Code: CodeInternal, Message: "git is required: install git and try again", Err: err}
		}
		return internalError("initialising git: "+strings.TrimSpace(stderr.String()), err)
	}
	return nil
}

// readTopic reads one Topic from the Study home.
func (c *Core) readTopic(id string) (Topic, error) {
	home, topic, err := c.openTopicFolder(id)
	if err != nil {
		return Topic{}, err
	}
	topic.Close()
	defer home.Close()
	return c.loadTopic(home, id)
}

// loadTopic reads one Topic from the Study home and replays its History.
// Content is authoritative for text, so the title and goal come from
// topic.toml; the History gives the creation time and the flags. It writes
// nothing: an interrupted write is flagged, and the next write finishes it.
func (c *Core) loadTopic(home *os.Root, id string) (Topic, error) {
	root, err := home.OpenRoot(id)
	if err != nil {
		return Topic{}, internalError("opening Topic "+id, err)
	}
	defer root.Close()
	data, exists, err := readItem(root, topicFile)
	if err != nil {
		return Topic{}, err
	}
	if !exists {
		return Topic{}, corruptf("%s of %s is missing", topicFile, id)
	}
	settings, err := parseTopicSettings(data, filepath.Join(c.home, id, topicFile))
	if err != nil {
		return Topic{}, err
	}
	topic := Topic{ID: id, Title: settings.Title, Goal: settings.Goal, Path: filepath.Join(c.home, id),
		KnowledgeBase: knowledgeBaseOf(settings)}
	h, err := readHistory(root, id)
	if err != nil {
		return Topic{}, err
	}
	s := replayHistory(h)
	topic.Created = s.created
	topic.Flags = c.topicFlags(root, s)
	if r := s.study.resume(); !r.empty() {
		topic.Resume = &r
	}
	// A marker while the lock is held is a write in progress, not an
	// interrupted one.
	if hasIntent(home, id) && !lockHeld(home, id) {
		topic.Flags = append(topic.Flags, newFlag(FlagInterruptedWrite, "", nil, "",
			"a write to this Topic was interrupted; the next change to it finishes the write"))
	}
	return topic, nil
}

func validateTopicID(id string) error {
	if len(id) > 64 || !topicIDPattern.MatchString(id) {
		return invalidf("%q is not a valid Topic id: use lowercase letters, digits and single hyphens, up to 64 characters", id)
	}
	return nil
}

// cleanText trims s and rejects invalid UTF-8 and control characters, which
// an agent could use to inject terminal escape sequences into human output.
func cleanText(field, s string, maxRunes int) (string, error) {
	if !utf8.ValidString(s) {
		return "", invalidf("the %s is not valid UTF-8 text", field)
	}
	s = strings.TrimSpace(s)
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", invalidf("the %s contains a control character", field)
		}
	}
	if utf8.RuneCountInString(s) > maxRunes {
		return "", invalidf("the %s is longer than %d characters", field, maxRunes)
	}
	return s, nil
}

// slugify turns a title into a Topic id: "Lineare Algebra für Anfänger"
// becomes "lineare-algebra-fur-anfanger". Accents are dropped; other letters
// outside ASCII separate words.
func slugify(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range norm.NFD.String(strings.ToLower(title)) {
		switch {
		case unicode.Is(unicode.Mn, r):
			// A combining accent: drop it and keep the base letter.
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	slug := strings.TrimSuffix(b.String(), "-")
	if len(slug) > 64 {
		slug = strings.TrimRight(slug[:64], "-")
	}
	return slug
}

// writeFileAtomic replaces name inside root without exposing a half-written
// file: it writes a uniquely named temporary file, syncs it, renames it over
// name, and syncs the folder so the rename survives a power cut. Concurrent
// writers never share a temporary file. Temporary files are hidden and
// named so that the Topic's .gitignore keeps a crash's leftovers out of
// Checkpoints, and recovery removes them.
func writeFileAtomic(root *os.Root, name string, data []byte) error {
	tmp := filepath.Join(filepath.Dir(name), tempPrefix(filepath.Base(name))+randomID())
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return internalError("writing "+name, err)
	}
	_, werr := f.Write(data)
	if werr == nil {
		werr = syncFile(f)
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = root.Remove(tmp)
		return internalError("writing "+name, werr)
	}
	if err := root.Rename(tmp, name); err != nil {
		_ = root.Remove(tmp)
		return internalError("replacing "+name, err)
	}
	syncDir(root, filepath.Dir(name))
	return nil
}

// tempPrefix is how temporary files for base begin: ".<base>.lamplight-tmp-".
// checkpoint.DefaultGitignore ignores the pattern.
func tempPrefix(base string) string { return "." + base + ".lamplight-tmp-" }

// syncDir flushes a folder's entries to disk. It is best effort: some file
// systems cannot sync a folder, and the data itself is already synced.
func syncDir(root *os.Root, dir string) {
	d, err := root.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

func alreadyExists(id string) error {
	return &Error{Code: CodeAlreadyExists, Message: "a Topic named " + id + " already exists"}
}
