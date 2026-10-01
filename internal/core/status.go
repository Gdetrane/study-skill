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
// was chosen, every Topic, and any Topic that could not be read.
type Status struct {
	StudyHome   string         `json:"study_home"`
	ActiveTopic *ActiveTopic   `json:"active_topic"`
	Topics      []Topic        `json:"topics"`
	Problems    []TopicProblem `json:"problems"`
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
func (c *Core) Status(ctx context.Context) (Status, error) {
	status := Status{StudyHome: c.home, Topics: []Topic{}, Problems: []TopicProblem{}}
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
			// Only a folder without topic.toml is not a Topic; one whose
			// settings cannot be checked is reported, never hidden.
			if !errors.Is(err, fs.ErrNotExist) {
				status.Problems = append(status.Problems, TopicProblem{ID: name, Code: CodeInternal,
					Message: internalError("checking "+topicFile+" of "+name, err).Error()})
			}
			continue
		}
		topic, err := loadTopic(home, c.home, name)
		if err != nil {
			status.Problems = append(status.Problems, TopicProblem{ID: name, Code: CodeOf(err), Message: err.Error()})
			continue
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
		return &ActiveTopic{ID: t.ID, Title: t.Title, ChosenBy: ChosenByOnly, Reason: "it is your only Topic"}
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
