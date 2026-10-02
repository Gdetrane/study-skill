package core

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
)

// removedDir is where removed Topics are kept, in the Study home's .lamplight
// folder: out of the way of status, never deleted.
const removedDir = "removed"

// TopicRemoval reports a Topic moved out of the Study home.
type TopicRemoval struct {
	Topic string `json:"topic"`
	// MovedTo is the folder the Topic now lives in, whole, git history
	// included; moving it back restores it.
	MovedTo string `json:"moved_to"`
	DryRun  bool   `json:"dry_run,omitempty"`
}

// RemoveTopic moves a Topic's folder out of the Study home, into
// .lamplight/removed/<time>-<id>. Nothing is deleted: moving the folder back
// restores the Topic. It takes the Topic's lock, so no write is half done, and
// refuses while an interrupted write waits to be finished.
//
// It exists for the learner: to import a v1 workspace again, for example
// with --not-done after the adoption showed a Lesson wrongly proven done.
// Agents have no tool for it.
func (c *Core) RemoveTopic(ctx context.Context, topicID string, dryRun bool) (TopicRemoval, error) {
	home, topic, err := c.openTopicFolder(topicID)
	if err != nil {
		return TopicRemoval{}, err
	}
	defer home.Close()
	_ = topic.Close()

	dir := filepath.Join(localDir, removedDir)
	name := c.now().UTC().Format("20060102-150405") + "-" + topicID
	if _, err := home.Lstat(filepath.Join(dir, name)); err == nil {
		name += "-" + randomID()[:6]
	}
	rel := filepath.Join(dir, name)
	out := TopicRemoval{Topic: topicID, MovedTo: filepath.Join(c.home, rel), DryRun: dryRun}
	interrupted := func() error {
		if !hasIntent(home, topicID) {
			return nil
		}
		return &Error{Code: CodeFailedPrecondition, Message: "a write to Topic " + topicID +
			" was interrupted: finish it first (study checkpoint, or any change to the Topic), then remove it"}
	}
	if err := interrupted(); err != nil {
		return out, err
	}
	if dryRun {
		return out, nil
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}

	unlock, err := lockTopic(ctx, home, topicID)
	if err != nil {
		return out, err
	}
	defer unlock()
	if err := interrupted(); err != nil {
		return out, err
	}
	if err := checkTopicFolder(home, topicID); err != nil {
		return out, err
	}
	if err := home.MkdirAll(dir, 0o755); err != nil {
		return out, internalError("creating "+dir, err)
	}
	if err := home.Rename(topicID, rel); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return out, &Error{Code: CodeBusy, Message: rel + " appeared while Topic " + topicID + " was being removed: try again"}
		}
		return out, internalError("moving Topic "+topicID+" to "+rel, err)
	}
	syncDir(home, ".")
	syncDir(home, dir)
	c.log.Info("removed a Topic from the Study home", "topic", topicID, "moved_to", out.MovedTo)
	return out, nil
}
