//go:build unix

package core

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// An Assessment or a Syllabus named as a FIFO is refused, never waited on,
// and one larger than the limit is refused.
func TestAssessmentAndSyllabusFilesAreReadSafely(t *testing.T) {
	m := newTopic(t)
	fifo := filepath.Join(t.TempDir(), "assessment.json")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("cannot make a FIFO: %v", err)
	}
	for name, read := range map[string]func() error{
		"Assessment": func() error { _, err := m.ReadAssessmentFile(fifo); return err },
		"Syllabus":   func() error { _, err := m.ReadSyllabusFile(fifo); return err },
	} {
		done := make(chan error, 1)
		go func() { done <- read() }()
		select {
		case err := <-done:
			if CodeOf(err) != CodeInvalidArgument || !strings.Contains(err.Error(), "not a regular file") {
				t.Errorf("%s from a FIFO: err = %v, want invalid_argument", name, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("reading the %s blocked on a FIFO", name)
		}
	}
	big := filepath.Join(t.TempDir(), "big.json")
	if err := os.WriteFile(big, []byte(strings.Repeat(" ", maxAssessmentFileBytes+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ReadAssessmentFile(big); CodeOf(err) != CodeInvalidArgument {
		t.Errorf("an Assessment file over the limit: err = %v", err)
	}
	if _, err := m.ReadAssessmentFile(filepath.Join(t.TempDir(), "missing.json")); CodeOf(err) != CodeNotFound {
		t.Errorf("a missing Assessment file: err = %v", err)
	}
}
