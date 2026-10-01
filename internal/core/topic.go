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

	"github.com/BurntSushi/toml"
)

const (
	topicFile      = "topic.toml"
	gitattributes  = ".gitattributes"
	maxTitleLength = 200
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
func (c *Core) CreateTopic(ctx context.Context, spec TopicSpec) (Topic, error) {
	title := strings.TrimSpace(spec.Title)
	if title == "" {
		return Topic{}, invalidf("a Topic needs a title")
	}
	if len(title) > maxTitleLength {
		return Topic{}, invalidf("the title is longer than %d characters", maxTitleLength)
	}
	id := spec.ID
	if id == "" {
		id = slugify(title)
		if id == "" {
			return Topic{}, invalidf("cannot derive a folder name from %q: pass an id such as \"linear-algebra\"", title)
		}
	}
	if err := validateTopicID(id); err != nil {
		return Topic{}, err
	}
	topic := Topic{
		ID:      id,
		Title:   title,
		Goal:    strings.TrimSpace(spec.Goal),
		Path:    filepath.Join(c.home, id),
		Created: c.now().UTC(),
	}
	if _, err := os.Lstat(topic.Path); err == nil {
		return Topic{}, &Error{Code: CodeAlreadyExists, Message: "a Topic named " + id + " already exists"}
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
	if err := home.Mkdir(id, 0o755); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return Topic{}, &Error{Code: CodeAlreadyExists, Message: "a Topic named " + id + " already exists"}
		}
		return Topic{}, internalError("creating "+topic.Path, err)
	}
	if err := c.initTopic(ctx, home, topic); err != nil {
		_ = home.RemoveAll(id)
		return Topic{}, err
	}
	if err := c.setRecentTopic(home, id); err != nil {
		return Topic{}, err
	}
	return topic, nil
}

func (c *Core) initTopic(ctx context.Context, home *os.Root, topic Topic) error {
	root, err := home.OpenRoot(topic.ID)
	if err != nil {
		return internalError("opening "+topic.Path, err)
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
	data, err := json.Marshal(map[string]string{"title": topic.Title})
	if err != nil {
		return internalError("encoding an Event", err)
	}
	if err := appendEvent(root, event{
		Format: FormatVersion, ID: c.newID(), Time: topic.Created, Type: eventTopicCreated, Data: data,
	}); err != nil {
		return err
	}
	return gitInit(ctx, topic.Path)
}

// gitInit makes the Topic folder a git repository. Commits are made later by
// Checkpoints, which never run programs named by repository configuration.
func gitInit(ctx context.Context, dir string) error {
	cmd := exec.CommandContext(ctx, "git", "init", "--quiet", "--initial-branch=main", dir)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return &Error{Code: CodeInternal, Message: "git is required: install git and try again", Err: err}
		}
		return internalError("initialising git in "+dir+": "+strings.TrimSpace(stderr.String()), err)
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
		return Topic{}, invalidf("%s of %s is not valid TOML: %v", topicFile, id, err)
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

// slugify turns a title into a Topic id: "Linear Algebra & Calculus" becomes
// "linear-algebra-calculus".
func slugify(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(title) {
		switch {
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
// file.
func writeFileAtomic(root *os.Root, name string, data []byte) error {
	tmp := name + ".tmp"
	if err := root.WriteFile(tmp, data, 0o644); err != nil {
		return internalError("writing "+name, err)
	}
	if err := root.Rename(tmp, name); err != nil {
		_ = root.Remove(tmp)
		return internalError("replacing "+name, err)
	}
	return nil
}
