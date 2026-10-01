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
// was chosen, and every Topic.
type Status struct {
	StudyHome   string       `json:"study_home"`
	ActiveTopic *ActiveTopic `json:"active_topic"`
	Topics      []Topic      `json:"topics"`
}

// ActiveTopic is the Topic the agent is working on, with the reason it was
// chosen.
type ActiveTopic struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	ChosenBy string `json:"chosen_by"`
	Reason   string `json:"reason"`
}

// localState is the machine-local state in the Study home's .lamplight
// folder. It is never synced.
type localState struct {
	Format      int    `toml:"format"`
	RecentTopic string `toml:"recent_topic"`
}

// Status reports where the learner is.
func (c *Core) Status(ctx context.Context) (Status, error) {
	status := Status{StudyHome: c.home, Topics: []Topic{}}
	home, err := os.OpenRoot(c.home)
	if errors.Is(err, fs.ErrNotExist) {
		return status, nil
	}
	if err != nil {
		return status, internalError("opening the Study home "+c.home, err)
	}
	defer home.Close()

	entries, err := fs.ReadDir(home.FS(), ".")
	if err != nil {
		return status, internalError("listing the Study home", err)
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return status, err
		}
		name := entry.Name()
		if !entry.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		if _, err := home.Stat(filepath.Join(name, topicFile)); err != nil {
			continue
		}
		topic, err := loadTopic(home, c.home, name)
		if err != nil {
			return status, err
		}
		status.Topics = append(status.Topics, topic)
	}
	sort.Slice(status.Topics, func(i, j int) bool { return status.Topics[i].ID < status.Topics[j].ID })

	state, err := c.readState(home)
	if err != nil {
		return status, err
	}
	status.ActiveTopic = c.activeTopic(status.Topics, state.RecentTopic)
	return status, nil
}

// activeTopic picks the Topic whose folder the agent started in, otherwise the
// most recent Topic, otherwise the only Topic.
func (c *Core) activeTopic(topics []Topic, recent string) *ActiveTopic {
	find := func(id string) *Topic {
		for i := range topics {
			if topics[i].ID == id {
				return &topics[i]
			}
		}
		return nil
	}
	if rel, err := filepath.Rel(c.home, c.dir); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		first := strings.Split(rel, string(filepath.Separator))[0]
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
		return &ActiveTopic{ID: t.ID, Title: t.Title, ChosenBy: ChosenByOnly, Reason: "it is your only Topic"}
	}
	return nil
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
		return localState{}, nil
	}
	if state.Format > FormatVersion {
		return state, newerFormat(filepath.Join(c.home, localDir, stateFile), state.Format)
	}
	return state, nil
}

func (c *Core) setRecentTopic(home *os.Root, id string) error {
	if err := home.MkdirAll(localDir, 0o755); err != nil {
		return internalError("creating "+localDir, err)
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(localState{Format: FormatVersion, RecentTopic: id}); err != nil {
		return internalError("encoding local state", err)
	}
	return writeFileAtomic(home, filepath.Join(localDir, stateFile), buf.Bytes())
}
