package core

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/mordor-forge/lamplight/v2/internal/checkpoint"
)

const maxCheckpointMessageRunes = 200

// CheckpointSpec describes a Checkpoint to take.
type CheckpointSpec struct {
	// Topic is the ID of the Topic to checkpoint. Required: writes always
	// name their Topic.
	Topic string
	// Role is whose turn just ended, "agent" or "learner": whose work the
	// Checkpoint saves.
	Role string
	// Message summarises the turn. Optional.
	Message string
	// DryRun reports whether a Checkpoint would be made, and any large
	// files it would save, without committing.
	DryRun bool
}

// CheckpointResult reports a Checkpoint.
type CheckpointResult struct {
	Topic string `json:"topic"`
	// Committed is false when nothing changed since the last Checkpoint. In
	// a dry run it says whether a Checkpoint would be made.
	Committed bool `json:"committed"`
	// Commit is the new commit, or the previous one when nothing changed
	// ("" before the first Checkpoint, and in a dry run that would commit).
	Commit     string      `json:"commit"`
	LargeFiles []LargeFile `json:"large_files"`
	DryRun     bool        `json:"dry_run,omitempty"`
}

// LargeFile is a large file the Checkpoint added or changed. Data and model
// files usually belong in .gitignore instead.
type LargeFile struct {
	Path string `json:"path"`
	Size int64  `json:"size_bytes"`
}

// Checkpoint commits the learner's or the agent's work in a Topic, without
// running any program the Topic's git configuration names. See the design's
// Checkpoints section and ADR-0009.
func (c *Core) Checkpoint(ctx context.Context, spec CheckpointSpec) (CheckpointResult, error) {
	role := checkpoint.Role(spec.Role)
	switch {
	case role == "":
		return CheckpointResult{}, invalidf("say whose turn ended: pass the role \"agent\" or \"learner\"")
	case role != checkpoint.Agent && role != checkpoint.Learner:
		return CheckpointResult{}, invalidf("the role must be \"agent\" or \"learner\", not %q", spec.Role)
	}
	message, err := cleanText("message", spec.Message, maxCheckpointMessageRunes)
	if err != nil {
		return CheckpointResult{}, err
	}
	path, err := c.topicPath(spec.Topic)
	if err != nil {
		return CheckpointResult{}, err
	}
	res, err := checkpoint.Take(ctx, path, checkpoint.Options{
		Role: role, Message: message, Time: c.now(), DryRun: spec.DryRun,
	})
	if err != nil {
		return CheckpointResult{}, checkpointError(spec.Topic, err)
	}
	out := CheckpointResult{Topic: spec.Topic, Committed: res.Committed, Commit: res.Commit,
		LargeFiles: []LargeFile{}, DryRun: spec.DryRun}
	for _, f := range res.LargeFiles {
		out.LargeFiles = append(out.LargeFiles, LargeFile{Path: f.Path, Size: f.Size})
	}
	return out, nil
}

// topicPath validates a Topic ID and returns the Topic's folder, refusing
// anything that is not a real Topic folder in the Study home.
func (c *Core) topicPath(id string) (string, error) {
	if strings.TrimSpace(id) == "" {
		return "", invalidf("name the Topic: run study status to see their ids")
	}
	if err := validateTopicID(id); err != nil {
		return "", err
	}
	home, err := c.openHome()
	if err != nil {
		return "", err
	}
	defer home.Close()
	info, err := home.Lstat(id)
	if errors.Is(err, fs.ErrNotExist) {
		return "", &Error{Code: CodeNotFound, Message: "there is no Topic named " + id + ": run study status to see your Topics"}
	}
	if err != nil {
		return "", internalError("reading Topic "+id, err)
	}
	if !info.IsDir() {
		return "", corruptf("%s in the Study home is not a folder", id)
	}
	if _, err := home.Stat(filepath.Join(id, topicFile)); err != nil {
		return "", &Error{Code: CodeNotFound, Message: id + " is not a Topic: it has no " + topicFile}
	}
	return filepath.Join(c.home, id), nil
}

// checkpointError maps the checkpoint package's errors to core errors with
// advice the learner or the agent can act on.
func checkpointError(topic string, err error) error {
	precondition := func(msg string) error {
		return &Error{Code: CodeFailedPrecondition, Message: "cannot checkpoint " + topic + ": " + msg, Err: err}
	}
	switch {
	case errors.Is(err, checkpoint.ErrIndexLocked), errors.Is(err, checkpoint.ErrHeadMoved):
		return &Error{Code: CodeBusy, Message: "cannot checkpoint " + topic + ": another program is using its git repository; try again in a moment", Err: err}
	case errors.Is(err, checkpoint.ErrNotRepository):
		return corruptf("%s is not a git repository: Lamplight creates one with every Topic", topic)
	case errors.Is(err, checkpoint.ErrDetachedHead),
		errors.Is(err, checkpoint.ErrMergeInProgress),
		errors.Is(err, checkpoint.ErrRebaseInProgress),
		errors.Is(err, checkpoint.ErrCherryPickInProgress),
		errors.Is(err, checkpoint.ErrRevertInProgress),
		errors.Is(err, checkpoint.ErrUnmergedPaths),
		errors.Is(err, checkpoint.ErrNoIdentity):
		return precondition(err.Error())
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	}
	return internalError("checkpointing "+topic, err)
}
