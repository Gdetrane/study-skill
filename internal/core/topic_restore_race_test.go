package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// importRace is a removed Topic, rust, and a v1 workspace to import under
// its id: study import holds the lock of the id it imports under, so the
// lock keeps an import and a restore of one id apart.
type importRace struct {
	restoreRace
	src string
}

func newImportRace(t *testing.T) importRace {
	t.Helper()
	r := newRestoreRace(t)
	return importRace{restoreRace: r, src: v1Workspace(t, v1Options{commits: []string{"[user] lesson 1 work"}})}
}

// A restore started while an import under the same id is under way waits for
// the import's lock, although nothing has the id yet, and then finds the id
// taken. Without the lock it would have gone first, and the import, its
// Topic complete, would have been the one refused.
func TestARestoreWaitsForAnImportUnderItsID(t *testing.T) {
	ctx := context.Background()
	r := newImportRace(t)

	// The import stops with its Topic complete in staging, not yet in place.
	hook, staged, release := pauseAt(crashImportStaged)
	r.other.crash = hook
	imported := make(chan error, 1)
	go func() {
		_, err := r.other.ImportV1(ctx, ImportSpec{Dir: r.src, ID: "rust"})
		imported <- err
	}()
	<-staged
	if exists(filepath.Join(r.home, "rust")) {
		t.Fatal("the import is in place before it was let go")
	}

	// The dry run does not wait: it says the restore would.
	dry, err := r.restorer.RestoreTopic(ctx, TopicRestoreSpec{Topic: "rust", DryRun: true})
	if err != nil || !dry.DryRun || !strings.Contains(dry.Note, "holds the lock of rust") || !strings.Contains(dry.Note, "waits") {
		t.Fatalf("dry run during the import = %+v, %v; want a note that the restore waits", dry, err)
	}

	waiting := make(chan struct{})
	r.restorer.crash = atPoint(crashBeforeLock, func() { close(waiting) })
	restored := make(chan error, 1)
	go func() {
		_, err := r.restorer.RestoreTopic(ctx, TopicRestoreSpec{Topic: "rust"})
		restored <- err
	}()
	<-waiting
	// Nothing has the id, so only the lock holds the restore back. Were it
	// not held back, it would be done well within this time.
	select {
	case err := <-restored:
		t.Fatalf("the restore did not wait for the import: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	release()

	if err := <-imported; err != nil {
		t.Fatalf("the import: %v", err)
	}
	err = <-restored
	if CodeOf(err) != CodeAlreadyExists || !strings.Contains(err.Error(), "another Topic is named rust now") {
		t.Fatalf("the restore after the import = %v, want already_exists", err)
	}
	if !exists(r.removed.MovedTo+".json") || treeHash(t, r.removed.MovedTo) != r.removedHash {
		t.Error("the removed Topic or its record was moved or changed")
	}
	status, err := r.restorer.Status(ctx)
	if err != nil || len(status.Topics) != 1 || status.Topics[0].Imported == nil || len(status.Problems) != 0 {
		t.Errorf("status = %+v, %v; want the imported Topic alone", status, err)
	}
	// Without the lock held, the dry run has nothing to note, and says what
	// the restore just said.
	if _, err := r.restorer.RestoreTopic(ctx, TopicRestoreSpec{Topic: "rust", DryRun: true}); CodeOf(err) != CodeAlreadyExists {
		t.Errorf("dry run after the import = %v, want already_exists", err)
	}
}

// The other order: an import under the id started while a restore holds the
// lock waits for it, and is then refused. Its staging folder is gone.
func TestAnImportWaitsForARestoreOfItsID(t *testing.T) {
	ctx := context.Background()
	r := newImportRace(t)

	// The restore stops holding the lock, the id found free.
	hook, checked, release := pauseAt(crashRestoreChecked)
	r.restorer.crash = hook
	restored := make(chan error, 1)
	go func() {
		_, err := r.restorer.RestoreTopic(ctx, TopicRestoreSpec{Topic: "rust"})
		restored <- err
	}()
	<-checked

	imported := make(chan error, 1)
	go func() {
		_, err := r.other.ImportV1(ctx, ImportSpec{Dir: r.src, ID: "rust"})
		imported <- err
	}()
	// Nothing has the id yet, so only the lock holds the import back.
	select {
	case err := <-imported:
		t.Fatalf("the import did not wait for the restore: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	release()

	if err := <-restored; err != nil {
		t.Fatalf("the restore: %v", err)
	}
	if err := <-imported; CodeOf(err) != CodeAlreadyExists {
		t.Fatalf("the import after the restore = %v, want already_exists", err)
	}
	if treeHash(t, filepath.Join(r.home, "rust")) != r.removedHash {
		t.Error("the restored Topic is not what was removed")
	}
	if left, err := os.ReadDir(filepath.Join(r.home, ".lamplight", "tmp")); err != nil || len(left) != 0 {
		t.Errorf("the refused import left staging folders: %v, %v", left, err)
	}
	status, err := r.restorer.Status(ctx)
	if err != nil || len(status.Topics) != 1 || status.Topics[0].Imported != nil || len(status.Problems) != 0 {
		t.Errorf("status = %+v, %v; want the restored Topic alone", status, err)
	}
}

// heldLock takes the lock of rust in home, as another study process would,
// and returns its release.
func heldLock(t *testing.T, home string) func() {
	t.Helper()
	root, err := os.OpenRoot(home)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := lockTopic(context.Background(), root, "rust")
	if err != nil {
		t.Fatal(err)
	}
	return func() {
		unlock()
		root.Close()
	}
}

// A restore stopped while it waits for the lock, as SIGTERM or Ctrl-C stop
// it, says it was canceled, and moved nothing.
func TestARestoreStoppedWhileItWaitsForTheLockIsCanceled(t *testing.T) {
	m, removed := removedTopic(t)
	before := treeHash(t, removed.MovedTo)
	unlock := heldLock(t, m.home)
	defer unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.crash = atPoint(crashBeforeLock, cancel)

	_, err := m.RestoreTopic(ctx, TopicRestoreSpec{Topic: "rust"})
	if CodeOf(err) != CodeCanceled || !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "nothing was restored") {
		t.Fatalf("a restore stopped at the lock = %v (%s), want canceled", err, CodeOf(err))
	}
	if exists(filepath.Join(m.home, "rust")) || treeHash(t, removed.MovedTo) != before || !exists(removed.MovedTo+".json") {
		t.Error("the stopped restore moved something")
	}
	// Stopped before it starts, it says the same.
	if _, err := m.RestoreTopic(ctx, TopicRestoreSpec{Topic: "rust"}); CodeOf(err) != CodeCanceled {
		t.Errorf("a restore stopped before it starts = %v (%s), want canceled", err, CodeOf(err))
	}
	// An error that has nothing to do with the stop keeps its code.
	if _, err := m.RestoreTopic(context.Background(), TopicRestoreSpec{Topic: "haskell"}); CodeOf(err) != CodeNotFound {
		t.Errorf("a refusal without a stop = %v", err)
	}
}

// A removal stopped while it waits for the lock says it was canceled too,
// and moved nothing.
func TestARemovalStoppedWhileItWaitsForTheLockIsCanceled(t *testing.T) {
	gitIdentity(t)
	m := newMachine(t, t.TempDir(), "id", t0)
	if _, err := m.CreateTopic(context.Background(), TopicSpec{Title: "Rust", Goal: "Write a CLI"}); err != nil {
		t.Fatal(err)
	}
	before := treeHash(t, filepath.Join(m.home, "rust"))
	unlock := heldLock(t, m.home)
	defer unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.crash = atPoint(crashBeforeLock, cancel)

	_, err := m.RemoveTopic(ctx, "rust", false)
	if CodeOf(err) != CodeCanceled || !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "nothing was removed") {
		t.Fatalf("a removal stopped at the lock = %v (%s), want canceled", err, CodeOf(err))
	}
	if treeHash(t, filepath.Join(m.home, "rust")) != before || len(removedFolders(t, m.home)) != 0 {
		t.Error("the stopped removal moved something")
	}
	if entries, err := os.ReadDir(filepath.Join(m.home, ".lamplight", removedDir)); err == nil && len(entries) != 0 {
		t.Errorf("the stopped removal left %v in .lamplight/removed", entries)
	}
	if _, err := m.RemoveTopic(ctx, "rust", false); CodeOf(err) != CodeCanceled {
		t.Errorf("a removal stopped before it starts = %v (%s), want canceled", err, CodeOf(err))
	}
	if _, err := m.RemoveTopic(context.Background(), "haskell", false); CodeOf(err) != CodeNotFound {
		t.Errorf("a refusal without a stop = %v", err)
	}
}
