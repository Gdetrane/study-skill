package core

import (
	"context"
	"errors"
	"fmt"
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
			ctx := passing{Context: context.Background(), done: make(chan struct{})}
			m.crash = atPoint(crashBeforeLock, ctx.pass)

			err := w.run(ctx, m)
			if CodeOf(err) != CodeBusy || !errors.Is(err, context.DeadlineExceeded) ||
				!strings.Contains(err.Error(), "another study process was still writing to Topic rust") ||
				!strings.Contains(err.Error(), "try again") {
				t.Fatalf("a deadline passed at the lock = %v (%s), want busy", err, CodeOf(err))
			}
			if treeHash(t, dir) != before || hasIntentIn(t, m.home, "rust") {
				t.Error("the command wrote something")
			}
		})
	}
}

func TestCodeOfAStoppedCommand(t *testing.T) {
	stopped, cancel := context.WithCancel(context.Background())
	cancel()
	late, cancelLate := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancelLate()
	for _, tc := range []struct {
		name string
		err  error
		want ErrorCode
	}{
		{"the context's own error", stopped.Err(), CodeCanceled},
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
