package core

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// removedDir is where removed Topics are kept, in the Study home's .lamplight
// folder: out of the way of status, never deleted. RestoreTopic brings one
// back (topic_restore.go).
const removedDir = "removed"

// Points where a test can interrupt a removal or a restore, as a crash would.
// See Core.crash.
const (
	crashRemoveRecorded = "remove-recorded" // the removal's record is written, the Topic not yet moved
	crashRestoreChecked = "restore-checked" // the lock is held and the id found free, the Topic not yet moved back
	crashRestoreMoved   = "restore-moved"   // the Topic is back, the removal's record still there
)

// TopicRemoval reports a Topic moved out of the Study home.
type TopicRemoval struct {
	Topic string `json:"topic"`
	// MovedTo is the folder the Topic now lives in, whole, git history
	// included: .lamplight/removed/<UTC time>-<topic>.
	MovedTo string `json:"moved_to"`
	// Restore is the command that brings back exactly this removal, under
	// the Topic's id: study topic restore <topic> --from <folder>. It names
	// the folder, so a later removal of the same id does not change what it
	// restores.
	Restore string `json:"restore"`
	// Note says, in a dry run, that a write to the Topic is in progress:
	// the removal waits for it to finish.
	Note   string `json:"note,omitempty"`
	DryRun bool   `json:"dry_run,omitempty"`
}

// RemoveTopic moves a Topic's folder out of the Study home, into
// .lamplight/removed/<time>-<id>, on this computer only: nothing is deleted,
// and the Topic's git remote and other computers keep their copies. It
// takes the Topic's lock, so it waits for a write in progress, and refuses
// while an interrupted write waits to be finished.
//
// It records no Event. Removing moves a folder on one computer and changes
// nothing inside the Topic, so it is this computer's state, like where a
// Source's file is (sourcelocal.go): the rule that every write is an Event
// covers what a Topic holds, and a Topic removed here is unchanged on every
// other computer. What it does write is a record beside the removed folder,
// saying which Topic it was (see removalRecord).
//
// It exists for the learner: to import a v1 workspace again, for example
// with --not-done after the adoption showed a Lesson wrongly proven done.
// Agents have no tool for it.
func (c *Core) RemoveTopic(ctx context.Context, topicID string, dryRun bool) (TopicRemoval, error) {
	out, err := c.removeTopic(ctx, topicID, dryRun)
	if err != nil && ctx.Err() != nil {
		return TopicRemoval{}, &Error{Code: CodeCanceled, Err: ctx.Err(),
			Message: "the removal was stopped before it moved the Topic, so nothing was removed"}
	}
	return out, err
}

func (c *Core) removeTopic(ctx context.Context, topicID string, dryRun bool) (TopicRemoval, error) {
	home, topic, err := c.openTopicFolder(topicID)
	if err != nil {
		return TopicRemoval{}, err
	}
	defer home.Close()
	defer topic.Close()

	if dryRun {
		out, _ := c.removal(home, topicID)
		out.DryRun = true
		if wasInterrupted(home, topicID) {
			return TopicRemoval{}, interruptedWrite(topicID)
		}
		if hasIntent(home, topicID) {
			// A writer holding the lock is still at work; the real run
			// waits for it.
			out.Note = "a write to " + topicID + " is in progress: the removal waits for it to finish"
		}
		return out, nil
	}
	if err := ctx.Err(); err != nil {
		return TopicRemoval{}, err
	}
	unlock, err := c.lockOpenedTopic(ctx, home, topic, topicID)
	if err != nil {
		return TopicRemoval{}, err
	}
	defer unlock()
	// The lock is ours, so a marker left now is a write that was
	// interrupted, not one in progress.
	if hasIntent(home, topicID) {
		return TopicRemoval{}, interruptedWrite(topicID)
	}

	out, when := c.removal(home, topicID)
	rel, err := filepath.Rel(c.home, out.MovedTo)
	if err != nil {
		return TopicRemoval{}, internalError("placing the removed Topic", err)
	}
	dir, folder := filepath.Dir(rel), filepath.Base(rel)
	if err := home.MkdirAll(dir, 0o755); err != nil {
		return TopicRemoval{}, internalError("creating "+dir, err)
	}
	// The record first, then the move. A crash between the two leaves a
	// record without its folder, which is no removal at all and is passed
	// over; the other order would leave a removed Topic whose id nothing
	// says.
	if err := writeRemovalRecord(home, folder, removalRecord{Format: FormatVersion, Topic: topicID, Removed: when}); err != nil {
		return TopicRemoval{}, err
	}
	if err := c.crashAt(crashRemoveRecorded); err != nil {
		return TopicRemoval{}, err
	}
	if err := home.Rename(topicID, rel); err != nil {
		_ = home.Remove(removalRecordPath(folder))
		return TopicRemoval{}, moveError(topicID, out.MovedTo, err)
	}
	syncDir(home, ".")
	syncDir(home, dir)
	c.log.Info("removed a Topic from the Study home", "topic", topicID, "moved_to", out.MovedTo)
	return out, nil
}

// removal names where the Topic would go now, and when that is: the UTC
// time, to the second, then the id, with a random suffix if that name is
// taken, by a folder or by the record of one.
func (c *Core) removal(home *os.Root, topicID string) (TopicRemoval, time.Time) {
	when := c.now().UTC()
	name := when.Format(removedTimeLayout) + "-" + topicID
	for _, taken := range []string{filepath.Join(localDir, removedDir, name), removalRecordPath(name)} {
		if _, err := home.Lstat(taken); err == nil {
			name += "-" + randomID()[:removalSuffixLen]
			break
		}
	}
	movedTo := filepath.Join(c.home, localDir, removedDir, name)
	return TopicRemoval{Topic: topicID, MovedTo: movedTo, Restore: restoreCommand(topicID, name)}, when
}

// restoreCommand is the command that restores the removal kept in folder. A
// Topic id and a removed Topic's folder name hold only lowercase letters,
// digits and hyphens, so neither needs quoting in any shell. Like every
// command study prints, it is for the Study home study is running in.
func restoreCommand(topicID, folder string) string {
	return "study topic restore " + topicID + " --from " + folder
}

func interruptedWrite(topicID string) error {
	return &Error{Code: CodeFailedPrecondition, Message: "a write to Topic " + topicID +
		" was interrupted: finish it first (study checkpoint --topic " + topicID + " --role learner, or any change " +
		"to the Topic), then remove it"}
}

// moveError explains a move the operating system refused.
func moveError(topicID, to string, err error) error {
	switch {
	case errors.Is(err, fs.ErrExist):
		return &Error{Code: CodeBusy, Err: err, Message: to + " appeared while Topic " + topicID + " was being removed: try again"}
	case errors.Is(err, syscall.EXDEV):
		return &Error{Code: CodeFailedPrecondition, Err: err, Message: "Topic " + topicID +
			" and the Study home's .lamplight folder are on different file systems, so the Topic cannot be moved there: " +
			"move the Topic's folder out of the Study home yourself"}
	case errors.Is(err, syscall.EBUSY):
		return &Error{Code: CodeBusy, Err: err, Message: "Topic " + topicID +
			"'s folder is in use or is a mount point: close the programs using it, or unmount it, and try again"}
	}
	return internalError("moving Topic "+topicID+" to "+to, err)
}
