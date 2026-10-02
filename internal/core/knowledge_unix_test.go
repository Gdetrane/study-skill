//go:build unix

package core

import (
	"context"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A FIFO is never a Source: opening one to hash it would block forever.
func TestAFIFOIsNotASource(t *testing.T) {
	m := newTopic(t)
	fifo := filepath.Join(t.TempDir(), "book.pdf")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("cannot make a FIFO: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := m.AddSource(context.Background(), SourceSpec{Topic: "c", File: fifo})
		done <- err
	}()
	select {
	case err := <-done:
		if CodeOf(err) != CodeInvalidArgument {
			t.Errorf("AddSource(FIFO): err = %v, want invalid_argument", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("AddSource blocked on a FIFO")
	}
}
