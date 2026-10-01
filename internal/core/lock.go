package core

import (
	"context"
	"os"
	"path/filepath"
	"time"
)

// lockWait is how long a write waits for another process to finish writing
// the same Topic before giving up with CodeBusy.
const lockWait = 30 * time.Second

// lockTopic takes the per-Topic write lock, which serialises writes from
// every process: the CLI and the MCP server can run at the same time. It is
// an advisory lock on .lamplight/locks/<topic>.lock in the Study home, local
// and never synced, which the operating system releases if the process dies.
func lockTopic(ctx context.Context, home *os.Root, topicID string) (unlock func(), err error) {
	dir := filepath.Join(localDir, "locks")
	if err := home.MkdirAll(dir, 0o755); err != nil {
		return nil, internalError("creating "+dir, err)
	}
	f, err := home.OpenFile(filepath.Join(dir, topicID+".lock"), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, internalError("opening the lock of Topic "+topicID, err)
	}
	deadline := time.Now().Add(lockWait)
	wait := time.Millisecond
	for {
		locked, err := tryLock(f)
		if err != nil {
			_ = f.Close()
			return nil, internalError("locking Topic "+topicID, err)
		}
		if locked {
			return func() {
				_ = unlockFile(f)
				_ = f.Close()
			}, nil
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, &Error{Code: CodeBusy,
				Message: "another study process has been writing to Topic " + topicID + " for over " + lockWait.String() + ": try again"}
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(wait):
		}
		wait = min(2*wait, 50*time.Millisecond)
	}
}
