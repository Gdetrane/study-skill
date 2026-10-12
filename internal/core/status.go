package core

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

const (
	localDir  = ".lamplight"
	stateFile = "state.toml"
)

// How the Active topic was chosen.
const (
	ChosenByFolder = "folder"
	ChosenByRecent = "recent"
	ChosenByOnly   = "only"
)

// Status is where the learner is: the Study home, the Active topic and why it
// was chosen, the one recommended action, every Topic, and any Topic that
// could not be read.
type Status struct {
	StudyHome   string       `json:"study_home"`
	ActiveTopic *ActiveTopic `json:"active_topic"`
	// Recommended is the one action to take next on the Active topic.
	Recommended *Recommendation `json:"recommended,omitempty"`
	// LearnerProfile is the Learner profile's path, when it exists: read it
	// at the start of every Session.
	LearnerProfile string         `json:"learner_profile,omitempty"`
	Topics         []Topic        `json:"topics"`
	Problems       []TopicProblem `json:"problems"`
}

// ActiveTopic is the Topic the agent is working on, with the reason it was
// chosen.
type ActiveTopic struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	ChosenBy string `json:"chosen_by"`
	Reason   string `json:"reason"`
}

// TopicProblem is a Topic that could not be read, so the learner can fix it
// while every other Topic keeps working.
type TopicProblem struct {
	ID      string    `json:"id"`
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

// localState is the machine-local state in the Study home's .lamplight
// folder. It is never synced.
type localState struct {
	Format      int    `toml:"format"`
	RecentTopic string `toml:"recent_topic"`
}

// Status reports where the learner is.
//
// For each Topic that names a Knowledge base plugin it also asks the plugin
// which of the Topic's Sources it has indexed, without waiting for it
// (askPlugins): a plugin that is missing, slow or broken leaves its Topics'
// Sources not known, and status as quick as it would be without it.
func (c *Core) Status(ctx context.Context) (Status, error) { return c.where(ctx, true) }

// ActiveTopic says which Topic the agent is working on, and why, or nil
// when there is none: what Status says of it, and nothing else. It is for a
// command that only has to know which Topic it is about, such as study
// check run inside a Topic's folder. It asks no Knowledge base plugin
// anything: such a command has no use for what is indexed, and must not
// start a plugin, or wait for one, to learn it.
func (c *Core) ActiveTopic(ctx context.Context) (*ActiveTopic, error) {
	status, err := c.where(ctx, false)
	return status.ActiveTopic, err
}

// where is Status, which asks each Topic's Knowledge base plugin what it has
// indexed only when indexing is set.
func (c *Core) where(ctx context.Context, indexing bool) (Status, error) {
	status := Status{StudyHome: c.home, Topics: []Topic{}, Problems: []TopicProblem{}}
	home, err := os.OpenRoot(c.home)
	if errors.Is(err, fs.ErrNotExist) {
		return status, nil
	}
	if err != nil {
		return status, internalError("opening the Study home "+c.home, err)
	}
	defer home.Close()

	asks := map[string]*indexAsk{}
	var named func(t Topic, sources []Source)
	if indexing {
		named = func(t Topic, sources []Source) {
			ask := newIndexAsk(t.ID, t.KnowledgeBase, sources)
			// A Source the plugin has not indexed is looked for on this
			// computer, as a listing of the Sources looks for it, within the
			// time the plugin had. Where it is found is not written down:
			// status changes nothing.
			var l *locator
			ask.fileState = func(ctx context.Context, src Source) string {
				if l == nil {
					l = c.newLocator(ctx, home, t.ID)
				}
				_, state, err := l.locate(src)
				if err != nil {
					return ""
				}
				return state
			}
			asks[t.ID] = ask
		}
	}
	if status.Topics, status.Problems, err = c.listTopics(ctx, home, named); err != nil {
		return status, err
	}
	// In the order of the Topics, so that the same Study home is asked about
	// in the same order every time.
	var asked []*indexAsk
	for i := range status.Topics {
		if ask := asks[status.Topics[i].ID]; ask != nil {
			asked = append(asked, ask)
			status.Topics[i].Indexing = ask.indexing
		}
	}
	if err := c.askPlugins(ctx, asked); err != nil {
		return status, err
	}
	for _, ask := range asked {
		// Nothing to index, and a file that is missing or is another now:
		// the listing of the Sources says what to do about each.
		if x := ask.indexing; ask.held && x.Problem == "" && x.Action == "" {
			x.Action = "study source list " + ask.topic
		}
	}
	state, err := c.readState(home)
	if err != nil {
		return status, err
	}
	status.ActiveTopic = c.activeTopic(status.Topics, len(status.Problems), state.RecentTopic)
	c.addGuidance(home, &status)
	return status, nil
}

// listTopics reads every Topic in the Study home, sorted by ID. Topics that
// cannot be read are returned as problems instead of failing the listing.
// named, when it is not nil, is told each Topic whose Knowledge base is a
// plugin, with the Topic's Sources.
func (c *Core) listTopics(ctx context.Context, home *os.Root, named func(t Topic, sources []Source)) ([]Topic, []TopicProblem, error) {
	topics, problems := []Topic{}, []TopicProblem{}
	entries, err := fs.ReadDir(home.FS(), ".")
	if err != nil {
		return topics, problems, internalError("listing the Study home", err)
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return topics, problems, err
		}
		name := entry.Name()
		if !entry.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		if !isTopic(home, name) {
			continue
		}
		topic, sources, err := c.loadTopicSources(home, name)
		if err != nil {
			problems = append(problems, TopicProblem{ID: name, Code: CodeOf(err), Message: err.Error()})
			continue
		}
		if named != nil && topic.KnowledgeBase != nil && topic.KnowledgeBase.Kind == KnowledgeBasePlugin {
			named(topic, sources)
		}
		topics = append(topics, topic)
	}
	sort.Slice(topics, func(i, j int) bool { return topics[i].ID < topics[j].ID })
	return topics, problems, nil
}

// activeTopic picks the Topic whose folder the agent started in, otherwise the
// most recent Topic, otherwise the only Topic.
func (c *Core) activeTopic(topics []Topic, unreadable int, recent string) *ActiveTopic {
	find := func(id string) *Topic {
		for i := range topics {
			if topics[i].ID == id {
				return &topics[i]
			}
		}
		return nil
	}
	if first, ok := firstFolderBelow(c.home, c.dir); ok {
		if t := find(first); t != nil {
			return &ActiveTopic{ID: t.ID, Title: t.Title, ChosenBy: ChosenByFolder,
				Reason: "you started inside its folder"}
		}
	}
	if t := find(recent); t != nil {
		return &ActiveTopic{ID: t.ID, Title: t.Title, ChosenBy: ChosenByRecent,
			Reason: "it is the Topic you worked on most recently"}
	}
	if len(topics) == 1 {
		t := topics[0]
		reason := "it is your only Topic"
		if unreadable > 0 {
			reason = "it is the only Topic that could be read"
		}
		return &ActiveTopic{ID: t.ID, Title: t.Title, ChosenBy: ChosenByOnly, Reason: reason}
	}
	return nil
}

// firstFolderBelow returns the first path element of dir below home. It
// compares both the paths as given and with symbolic links resolved, so a
// Study home reached through a symlink still matches.
func firstFolderBelow(home, dir string) (string, bool) {
	try := func(home, dir string) (string, bool) {
		rel, err := filepath.Rel(home, dir)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", false
		}
		return strings.Split(rel, string(filepath.Separator))[0], true
	}
	if first, ok := try(home, dir); ok {
		return first, true
	}
	realHome, err1 := filepath.EvalSymlinks(home)
	realDir, err2 := filepath.EvalSymlinks(dir)
	if err1 != nil || err2 != nil {
		return "", false
	}
	return try(realHome, realDir)
}

// openHome opens the Study home, creating it if needed.
func (c *Core) openHome() (*os.Root, error) {
	if err := os.MkdirAll(c.home, 0o755); err != nil {
		return nil, internalError("creating the Study home "+c.home, err)
	}
	home, err := os.OpenRoot(c.home)
	if err != nil {
		return nil, internalError("opening the Study home "+c.home, err)
	}
	return home, nil
}

func (c *Core) readState(home *os.Root) (localState, error) {
	var state localState
	data, err := home.ReadFile(filepath.Join(localDir, stateFile))
	if errors.Is(err, fs.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, internalError("reading local state", err)
	}
	if _, err := toml.Decode(string(data), &state); err != nil {
		// Local state is a convenience; a damaged file must not block studying.
		c.log.Warn("ignoring damaged local state", "path", filepath.Join(c.home, localDir, stateFile), "err", err)
		return localState{}, nil
	}
	if state.Format > FormatVersion {
		return state, newerFormat(filepath.Join(c.home, localDir, stateFile), state.Format)
	}
	return state, nil
}

// setRecentTopic records the most recent Topic. It never overwrites local
// state written by a newer version of study.
func (c *Core) setRecentTopic(home *os.Root, id string) error {
	if _, err := c.readState(home); err != nil {
		return err
	}
	if err := home.MkdirAll(localDir, 0o755); err != nil {
		return internalError("creating "+localDir, err)
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(localState{Format: FormatVersion, RecentTopic: id}); err != nil {
		return internalError("encoding local state", err)
	}
	return writeFileAtomic(home, filepath.Join(localDir, stateFile), buf.Bytes())
}
