package cli_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/cli"
	"github.com/mordor-forge/lamplight/v2/internal/core"
)

// TestAFinishedMilestoneInTheTerminal finishes the only Milestone of Topic c,
// then checks what a learner without an agent sees in status, and how
// study history marks held Events and a wall clock that was behind.
func TestAFinishedMilestoneInTheTerminal(t *testing.T) {
	setGitIdentity(t)
	ctx := context.Background()
	home := withLesson(t)
	c, err := core.Open(options(home, home))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetPhase(ctx, "c", core.PhaseSpec{Lesson: "answer", Phase: core.PhasePracticing}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "c", "practice", "answer", "answer.txt"), []byte("42\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RunCheck(ctx, "c", "answer", core.CheckOptions{}); err != nil {
		t.Fatal(err)
	}
	done, err := c.CompleteLesson(ctx, "c", core.CompleteSpec{Lesson: "answer"})
	if err != nil || done.Next == nil || done.Next.Code != core.NextAssessMilestone {
		t.Fatalf("CompleteLesson = %+v, %v", done, err)
	}

	r := normalised(run(t, home, "status"))
	if r.code != cli.ExitOK {
		t.Fatalf("status: exit %d, %s", r.code, r.stderr)
	}
	golden(t, "status_assess.txt", r.stdout)

	f, err := os.OpenFile(filepath.Join(home, "c", "history.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		// A later Event from a machine whose clock was behind.
		`{"format":1,"id":"zz1","time":"2026-10-02T09:30:00Z","wall":"2026-09-30T09:30:00Z","type":"phase.set","data":{"lesson":"answer","phase":"feedback"}}`,
		// Held: its Phase is unknown, and its Lesson carries an escape.
		`{"format":1,"id":"zz2","time":"2026-10-02T09:31:00Z","wall":"2026-10-02T09:31:00Z","type":"phase.set","data":{"lesson":"answer\u001b[2J","phase":"\u001b]0;pwned\u0007"}}`,
	} {
		if _, err := f.WriteString(line + "\n"); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	h := run(t, home, "history", "c", "--type", "phase.")
	if strings.ContainsRune(h.stdout, '\x1b') {
		t.Errorf("study history writes a terminal escape: %q", h.stdout)
	}
	if !strings.Contains(h.stdout, "Phase set (held)") || !strings.Contains(h.stdout, "Phase set: feedback (answer, clock behind)") {
		t.Errorf("study history does not mark held Events and a clock behind:\n%s", h.stdout)
	}

	if err := os.Remove(filepath.Join(home, "c", "lessons", "answer.md")); err != nil {
		t.Fatal(err)
	}
	if l := run(t, home, "lesson", "answer", "--topic", "c"); !strings.Contains(l.stdout, "lessons/answer.md does not exist") {
		t.Errorf("study lesson capitalises the path it starts with:\n%s", l.stdout)
	}
}

// TestReadViewsNeverWriteEscapes renders a lesson and a history view with an
// escape in every string field: the core keeps text out of them, and the
// renderers make sure of it.
func TestReadViewsNeverWriteEscapes(t *testing.T) {
	const esc = "x\x1b]0;pwned\a"
	d := core.LessonDetail{Topic: esc, Number: esc, ID: esc, Title: esc, Status: esc, Phase: esc, File: esc, Path: esc,
		Milestone: core.LessonPosition{ID: esc, Title: esc}, HeaderError: esc, CheckVersion: esc, ShownCheck: esc,
		Check:       []core.Criterion{{ID: esc, Kind: esc, Describe: esc}},
		BreakPoints: []core.BreakPoint{{ID: esc, Describe: esc}}}
	v := core.HistoryView{Topic: esc, More: true, Entries: []core.HistoryEntry{
		{ID: esc, Type: esc, Summary: esc, Lesson: esc, Item: esc, Held: true, ClockBehind: true}}}
	for name, out := range map[string]string{"lesson": cli.RenderLessonDetail(d), "history": cli.RenderHistory(v),
		"empty history": cli.RenderHistory(core.HistoryView{Topic: esc, Entries: []core.HistoryEntry{}})} {
		// The renderers' own styles may use escapes; the field's must not
		// get through.
		if strings.Contains(out, "\x1b]") || strings.ContainsRune(out, '\a') {
			t.Errorf("the %s view writes an escape: %q", name, out)
		}
	}
}
