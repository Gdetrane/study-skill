package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// withCheck replaces the Lesson's Check by one running script, a shell
// script written to practice/answer/check.sh.
func withCheck(t *testing.T, m *machine, script string) {
	t.Helper()
	writeFile(t, m, "practice/answer/check.sh", script)
}

// groupOf reads the process group a Check's shell wrote to $PGID_FILE.
func groupOf(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the Check did not write its process group: %v", err)
	}
	pgid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	return pgid
}

// noOrphans checks that nothing is left in a Check's process group.
func noOrphans(t *testing.T, pgid int) {
	t.Helper()
	if err := syscall.Kill(-pgid, 0); !errors.Is(err, syscall.ESRCH) {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		t.Errorf("processes of the Check's group %d still run (kill 0: %v)", pgid, err)
	}
}

func TestACheckOwnsItsProcesses(t *testing.T) {
	ctx := context.Background()
	pgidFile := filepath.Join(t.TempDir(), "pgid")
	t.Setenv("PGID_FILE", pgidFile)
	for _, tc := range []struct {
		name, script, reason string
		timeout              time.Duration
		within               time.Duration
	}{
		{"a long sleep times out", `echo $$ > "$PGID_FILE"; sleep 30`, "longer than", 200 * time.Millisecond, 4 * time.Second},
		{"a program left in the background", `echo $$ > "$PGID_FILE"; sleep 30 >/dev/null 2>&1 &
exit 0`, "left programs running", time.Minute, 4 * time.Second},
		{"a program in the background holding the output", `echo $$ > "$PGID_FILE"; sleep 30 &
exit 0`, "left programs running", time.Minute, 4 * time.Second},
		{"a program that ignores SIGTERM", `echo $$ > "$PGID_FILE"; trap '' TERM; sleep 30 & wait`, "longer than",
			200 * time.Millisecond, 6 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := learningTopic(t)
			withCheck(t, m, tc.script)
			start := time.Now()
			a, err := m.RunCheck(ctx, "c", "answer", CheckOptions{Timeout: tc.timeout})
			if err != nil {
				t.Fatal(err)
			}
			if took := time.Since(start); took > tc.within {
				t.Errorf("the Check took %s", took)
			}
			if a.Outcome != OutcomeErrored || !strings.Contains(a.Criteria[0].Reason, tc.reason) {
				t.Errorf("Attempt = %+v, want errored: %s", a, tc.reason)
			}
			noOrphans(t, groupOf(t, pgidFile))
		})
	}
}

func TestACancelledCheckStopsEverythingAndRecordsNothing(t *testing.T) {
	pgidFile := filepath.Join(t.TempDir(), "pgid")
	t.Setenv("PGID_FILE", pgidFile)
	m := learningTopic(t)
	withCheck(t, m, `echo $$ > "$PGID_FILE"; sleep 30 & sleep 30`)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for i := 0; i < 100; i++ {
			if _, err := os.Stat(pgidFile); err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		cancel() // what SIGTERM does to study
	}()
	start := time.Now()
	_, err := m.RunCheck(ctx, "c", "answer", CheckOptions{})
	if CodeOf(err) != CodeCanceled {
		t.Fatalf("err = %v, want canceled", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("cancelling took %s", took)
	}
	noOrphans(t, groupOf(t, pgidFile))
	res, err := m.CheckResultsOf(context.Background(), "c", "answer")
	if err != nil || len(res.Attempts) != 0 {
		t.Errorf("a cancelled Check recorded %+v, %v", res.Attempts, err)
	}
}

// TestTheCheckThatCountsIsTheOneShown is the reviewer's sequence: practicing
// shows the Check, the agent then weakens it and runs it; completion must be
// refused, and status must flag the edit.
func TestTheCheckThatCountsIsTheOneShown(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	practicing(t, m)
	writeFile(t, m, "lessons/answer.md", strings.Replace(answerLesson, "[sh, check.sh]", `["true"]`, 1))
	a, err := m.RunCheck(ctx, "c", "answer", CheckOptions{})
	if err != nil || a.Outcome != OutcomePassed {
		t.Fatalf("the weakened Check: %+v, %v", a, err)
	}
	_, err = m.CompleteLesson(ctx, "c", CompleteSpec{Lesson: "answer"})
	if CodeOf(err) != CodeFailedPrecondition || !strings.Contains(err.Error(), "changed since it was shown") {
		t.Fatalf("completing on a Check never shown: err = %v, want failed_precondition", err)
	}
	topic, err := m.readTopic("c")
	if err != nil || !hasFlag(topic.Flags, FlagEditedOutside, checkItem("answer")) {
		t.Fatalf("flags = %+v, %v; want the Check flagged even after an Attempt ran it", topic.Flags, err)
	}

	// Showing the new Check makes it the one that counts, and clears the
	// flag.
	practicing(t, m)
	if _, err := m.RunCheck(ctx, "c", "answer", CheckOptions{}); err != nil {
		t.Fatal(err)
	}
	done, err := m.CompleteLesson(ctx, "c", CompleteSpec{Lesson: "answer"})
	if err != nil || !done.Changed {
		t.Fatalf("completing on the Check shown: %+v, %v", done, err)
	}
	topic, err = m.readTopic("c")
	if err != nil || len(topic.Flags) != 0 {
		t.Errorf("flags = %+v, %v; want none", topic.Flags, err)
	}
}

func TestAnAttemptOnWorkWithABlindSpotIsErrored(t *testing.T) {
	ctx := context.Background()
	m := learningTopic(t)
	practicing(t, m)
	writeFile(t, m, "notes/answer.txt", "42\n")
	if err := os.Remove(filepath.Join(m.home, "c", "practice", "answer", "answer.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../notes/answer.txt", filepath.Join(m.home, "c", "practice", "answer", "answer.txt")); err != nil {
		t.Fatal(err)
	}
	a, err := m.RunCheck(ctx, "c", "answer", CheckOptions{})
	if err != nil || a.Outcome != OutcomeErrored || len(a.Criteria) != 0 || !strings.Contains(a.Reason, "outside") {
		t.Fatalf("a link leading outside: %+v, %v; want an errored Attempt that ran nothing", a, err)
	}
	res, err := m.CheckResultsOf(ctx, "c", "answer")
	if err != nil || res.CanComplete || !strings.Contains(res.Reason, "outside") {
		t.Errorf("check results = %+v, %v", res, err)
	}
}
