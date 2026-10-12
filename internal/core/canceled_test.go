package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// What waits for a Topic's lock without wrapping itself: an ordinary write,
// a Checkpoint and a Session.
var lockWaiters = []struct {
	name string
	run  func(ctx context.Context, m *machine) error
}{
	{"an ordinary write", func(ctx context.Context, m *machine) error {
		goal := "Write a web server"
		_, err := m.UpdateTopic(ctx, "rust", TopicChanges{Goal: &goal})
		return err
	}},
	{"a Checkpoint", func(ctx context.Context, m *machine) error {
		_, err := m.Checkpoint(ctx, CheckpointSpec{Topic: "rust", Role: "learner"})
		return err
	}},
	{"a Session", func(ctx context.Context, m *machine) error {
		_, err := m.OpenSession(ctx, "rust", SessionSpec{Energy: EnergyFull})
		return err
	}},
}

// A command stopped while it waits for the Topic's lock, as SIGTERM or
// Ctrl-C stop it, says it was canceled, whichever operation it is, and
// wrote nothing.
func TestACommandStoppedWhileItWaitsForTheLockIsCanceled(t *testing.T) {
	for _, w := range lockWaiters {
		t.Run(w.name, func(t *testing.T) {
			m := newRemovalRace(t).writer
			dir := filepath.Join(m.home, "rust")
			before := treeHash(t, dir)
			unlock := heldLock(t, m.home)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			m.crash = atPoint(crashBeforeLock, cancel)

			err := w.run(ctx, m)
			unlock()
			if CodeOf(err) != CodeCanceled || !errors.Is(err, context.Canceled) ||
				!strings.Contains(err.Error(), "stopped while it waited for another study process that is writing to Topic rust") {
				t.Fatalf("stopped at the lock = %v (%s), want canceled", err, CodeOf(err))
			}
			// Stopped before it starts, it says the same.
			if err := w.run(ctx, m); CodeOf(err) != CodeCanceled {
				t.Errorf("stopped before it starts = %v (%s), want canceled", err, CodeOf(err))
			}
			if treeHash(t, dir) != before || hasIntentIn(t, m.home, "rust") {
				t.Error("the stopped command wrote something")
			}
			// It is the stop that was reported: nothing else was in its way.
			if err := w.run(context.Background(), m); err != nil {
				t.Errorf("with the lock free and nothing stopping it: %v", err)
			}
		})
	}
}

// passing is a context whose deadline passes when pass is called.
type passing struct {
	context.Context
	done chan struct{}
}

func (p passing) Done() <-chan struct{} { return p.done }

func (p passing) Err() error {
	select {
	case <-p.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func (p passing) pass() { close(p.done) }

func newPassing() passing { return passing{Context: context.Background(), done: make(chan struct{})} }

// busyAtTheLock checks that err is what a deadline that passed in the wait
// for a lock gives: busy, naming what the lock's holder is doing.
func busyAtTheLock(t *testing.T, err error, doing string) {
	t.Helper()
	if CodeOf(err) != CodeBusy || !errors.Is(err, context.DeadlineExceeded) ||
		!strings.Contains(err.Error(), "another study process was still "+doing) ||
		!strings.Contains(err.Error(), "try again") {
		t.Fatalf("a deadline passed at the lock = %v (%s), want busy", err, CodeOf(err))
	}
}

// held takes the lock named name in home, as another study process would,
// and returns its release.
func held(t *testing.T, home, name string) func() {
	t.Helper()
	root, err := os.OpenRoot(home)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := lockFile(context.Background(), root, lockPath(name), name, "holding "+name)
	if err != nil {
		t.Fatal(err)
	}
	return func() {
		unlock()
		root.Close()
	}
}

// A deadline that passes during the wait is not a stop: another process
// held the lock for too long, which is busy, as when the wait itself runs
// out.
func TestADeadlineThatPassesWhileWaitingForTheLockIsBusy(t *testing.T) {
	for _, w := range lockWaiters {
		t.Run(w.name, func(t *testing.T) {
			m := newRemovalRace(t).writer
			dir := filepath.Join(m.home, "rust")
			before := treeHash(t, dir)
			unlock := heldLock(t, m.home)
			defer unlock()
			ctx := newPassing()
			m.crash = atPoint(crashBeforeLock, ctx.pass)

			busyAtTheLock(t, w.run(ctx, m), "writing to Topic rust")
			if treeHash(t, dir) != before || hasIntentIn(t, m.home, "rust") {
				t.Error("the command wrote something")
			}
		})
	}
}

// The operations that say what a stop left behind say it for a stop alone.
// A deadline that passes while one of them waits for a lock is busy, as in
// every other operation, and nothing is moved, imported or recorded.
func TestADeadlineAtTheLockIsBusyInTheOperationsThatSayWhatAStopLeft(t *testing.T) {
	t.Run("RemoveTopic", func(t *testing.T) {
		m := newRemovalRace(t).writer
		dir := filepath.Join(m.home, "rust")
		before := treeHash(t, dir)
		defer heldLock(t, m.home)()
		ctx := newPassing()
		m.crash = atPoint(crashBeforeLock, ctx.pass)

		_, err := m.RemoveTopic(ctx, "rust", false)
		busyAtTheLock(t, err, "writing to Topic rust")
		if treeHash(t, dir) != before || len(removedFolders(t, m.home)) != 0 {
			t.Error("the removal moved something")
		}
	})
	t.Run("RestoreTopic", func(t *testing.T) {
		m, removed := removedTopic(t)
		before := treeHash(t, removed.MovedTo)
		defer heldLock(t, m.home)()
		ctx := newPassing()
		m.crash = atPoint(crashBeforeLock, ctx.pass)

		_, err := m.RestoreTopic(ctx, TopicRestoreSpec{Topic: "rust"})
		busyAtTheLock(t, err, "writing to Topic rust")
		if exists(filepath.Join(m.home, "rust")) || treeHash(t, removed.MovedTo) != before {
			t.Error("the restore moved something")
		}
	})
	t.Run("ImportV1", func(t *testing.T) {
		gitIdentity(t)
		src := v1Workspace(t, v1Options{})
		m := newMachine(t, t.TempDir(), "id", t0)
		// Another import is under way.
		defer held(t, m.home, importLock)()
		ctx := newPassing()
		done := make(chan error, 1)
		go func() {
			_, err := m.ImportV1(ctx, ImportSpec{Dir: src})
			done <- err
		}()
		select {
		case err := <-done:
			t.Fatalf("an import did not wait for the one under way: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		ctx.pass()

		busyAtTheLock(t, <-done, "importing a v1 workspace")
		if exists(filepath.Join(m.home, "go-concurrency")) {
			t.Error("the import left a Topic")
		}
	})
	t.Run("RunCheck", func(t *testing.T) {
		// The Check has run, and waits for the lock to record its Attempt.
		m := learningTopic(t)
		withCheck(t, m, "exit 0\n")
		unlock := held(t, m.home, "c")
		ctx := newPassing()
		m.crash = atPoint(crashBeforeLock, ctx.pass)

		_, err := m.RunCheck(ctx, "c", "answer", CheckOptions{})
		unlock()
		busyAtTheLock(t, err, "writing to Topic c")
		if res, err := m.CheckResultsOf(context.Background(), "c", "answer"); err != nil || len(res.Attempts) != 0 {
			t.Errorf("the Check recorded %+v, %v", res.Attempts, err)
		}
	})
}

// A deadline that passes while a Check's programs run is not a stop either,
// so it is not reported as one. The Check did not finish all the same: its
// programs are stopped and no Attempt is recorded.
func TestADeadlineDuringACheckIsNotAStop(t *testing.T) {
	pgidFile := filepath.Join(t.TempDir(), "pgid")
	t.Setenv("PGID_FILE", pgidFile)
	m := learningTopic(t)
	withCheck(t, m, `echo $$ > "$PGID_FILE"; sleep 30 & sleep 30`)
	ctx := newPassing()
	go func() {
		for i := 0; i < 100; i++ {
			if _, err := os.Stat(pgidFile); err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		ctx.pass()
	}()
	start := time.Now()
	_, err := m.RunCheck(ctx, "c", "answer", CheckOptions{})
	if CodeOf(err) != CodeInternal || !errors.Is(err, context.DeadlineExceeded) ||
		!strings.Contains(err.Error(), "did not finish in the time this command was given") {
		t.Fatalf("a deadline passed during the Check = %v (%s), want internal", err, CodeOf(err))
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("the Check took %s to stop", took)
	}
	noOrphans(t, groupOf(t, pgidFile))
	if res, err := m.CheckResultsOf(context.Background(), "c", "answer"); err != nil || len(res.Attempts) != 0 {
		t.Errorf("the Check recorded %+v, %v", res.Attempts, err)
	}
}

func TestCodeOfAStoppedCommand(t *testing.T) {
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	late, cancelLate := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancelLate()
	if !stopped(ended) || stopped(late) || stopped(context.Background()) {
		t.Errorf("stopped: cancelled %v, past its deadline %v, neither %v; want true, false, false",
			stopped(ended), stopped(late), stopped(context.Background()))
	}
	for _, tc := range []struct {
		name string
		err  error
		want ErrorCode
	}{
		{"the context's own error", ended.Err(), CodeCanceled},
		{"wrapped by another package", fmt.Errorf("git commit: %w", context.Canceled), CodeCanceled},
		{"the cause of an error the core could not classify", internalError("reading a file", context.Canceled), CodeCanceled},
		{"under a wrapper that keeps the code", at("Lesson 1.1", internalError("reading a file", context.Canceled)), CodeCanceled},
		{"an operation's own message", &Error{Code: CodeCanceled, Err: context.Canceled, Message: "nothing was imported"}, CodeCanceled},
		{"an error with a code of its own", &Error{Code: CodeCorrupt, Err: context.Canceled, Message: "damaged"}, CodeCorrupt},
		{"a deadline that passed", late.Err(), CodeInternal},
		{"a deadline that passed, wrapped", internalError("reading a file", context.DeadlineExceeded), CodeInternal},
		{"anything else", errors.New("disk full"), CodeInternal},
	} {
		if got := CodeOf(tc.err); got != tc.want {
			t.Errorf("%s: CodeOf(%v) = %s, want %s", tc.name, tc.err, got, tc.want)
		}
	}
}
