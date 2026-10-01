package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// Topic is one subject the learner is studying, with its own folder and git
// repository inside the Study home.
type Topic struct {
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Goal    string    `json:"goal,omitempty"`
	Path    string    `json:"path"`
	Created time.Time `json:"created"`
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

// topicSettings is the content of topic.toml.
type topicSettings struct {
	Format int    `toml:"format"`
	Title  string `toml:"title"`
	Goal   string `toml:"goal,omitempty"`
}

var topicIDPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// CreateTopic creates a Topic folder in the Study home with its settings, its
// History and its git repository, and makes it the most recent Topic.
//
// The folder is assembled under .lamplight/tmp and renamed into place only
// once it is complete, so an interrupted creation never leaves a half-made
// Topic behind.
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
	topic := Topic{ID: id, Title: title, Goal: goal, Path: filepath.Join(c.home, id), Created: c.now().UTC()}
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
	if err := c.initTopic(ctx, home, staging, topic); err != nil {
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
	_ = c.setRecentTopic(home, id)
	return topic, nil
}

func (c *Core) initTopic(ctx context.Context, home *os.Root, dir string, topic Topic) error {
	root, err := home.OpenRoot(dir)
	if err != nil {
		return internalError("opening "+dir, err)
	}
	defer root.Close()

	var settings bytes.Buffer
	settings.WriteString("# Topic settings. Lamplight rewrites this file; comments are not kept.\n")
	if err := toml.NewEncoder(&settings).Encode(topicSettings{
		Format: FormatVersion, Title: topic.Title, Goal: topic.Goal,
	}); err != nil {
		return internalError("encoding "+topicFile, err)
	}
	if err := writeFileAtomic(root, topicFile, settings.Bytes()); err != nil {
		return err
	}
	if err := writeFileAtomic(root, gitattributes, []byte(historyFile+" merge=union\n")); err != nil {
		return err
	}
	if err := writeFileAtomic(root, gitignore, []byte(checkpoint.DefaultGitignore())); err != nil {
		return err
	}
	data, err := json.Marshal(map[string]string{"title": topic.Title})
	if err != nil {
		return internalError("encoding an Event", err)
	}
	if err := appendEvent(root, event{
		Format: FormatVersion, ID: c.newID(), Time: topic.Created, Type: eventTopicCreated, Data: data,
	}); err != nil {
		return err
	}
	return gitInit(ctx, filepath.Join(c.home, dir))
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

// loadTopic reads one Topic from the Study home.
func loadTopic(home *os.Root, homePath, id string) (Topic, error) {
	root, err := home.OpenRoot(id)
	if err != nil {
		return Topic{}, internalError("opening Topic "+id, err)
	}
	defer root.Close()
	data, err := root.ReadFile(topicFile)
	if err != nil {
		return Topic{}, internalError("reading "+topicFile+" of "+id, err)
	}
	var settings topicSettings
	if _, err := toml.Decode(string(data), &settings); err != nil {
		return Topic{}, corruptf("%s of %s is not valid TOML: %v", topicFile, id, err)
	}
	if settings.Format > FormatVersion {
		return Topic{}, newerFormat(filepath.Join(homePath, id, topicFile), settings.Format)
	}
	topic := Topic{ID: id, Title: settings.Title, Goal: settings.Goal, Path: filepath.Join(homePath, id)}
	events, err := readEvents(root, id)
	if err != nil {
		return Topic{}, err
	}
	for _, ev := range events {
		if ev.Type == eventTopicCreated {
			topic.Created = ev.Time
			break
		}
	}
	return topic, nil
}

func validateTopicID(id string) error {
	if len(id) > 64 || !topicIDPattern.MatchString(id) {
		return invalidf("%q is not a valid Topic id: use lowercase letters, digits and single hyphens, up to 64 characters", id)
	}
	return nil
}

// cleanText trims s and rejects control characters, which an agent could use
// to inject terminal escape sequences into human output.
func cleanText(field, s string, maxRunes int) (string, error) {
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
// file: it writes a uniquely named temporary file, syncs it, and renames it
// over name. Concurrent writers never share a temporary file.
func writeFileAtomic(root *os.Root, name string, data []byte) error {
	tmp := name + ".tmp-" + randomID()
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return internalError("writing "+name, err)
	}
	_, werr := f.Write(data)
	if werr == nil {
		werr = f.Sync()
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
	return nil
}

func alreadyExists(id string) error {
	return &Error{Code: CodeAlreadyExists, Message: "a Topic named " + id + " already exists"}
}
