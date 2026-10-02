package cli_test

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/cli"
	"github.com/mordor-forge/lamplight/v2/internal/core"
)

// hashes stand for the Check and work versions in golden files, and for the
// content hashes of written work.
var hashes = regexp.MustCompile(`sha256:[0-9a-f]{64}`)

// withFullCheck turns withLesson's Lesson into one with a run criterion, a
// rubric item and a held-out criterion, with its Held-out data, and moves it
// to practicing.
func withFullCheck(t *testing.T) string {
	t.Helper()
	home := withLesson(t)
	for name, content := range map[string]string{
		"lessons/answer.md": "---\ncheck:\n" +
			"  - id: answer\n    describe: answer.txt holds 42\n    run: [sh, check.sh]\n" +
			"  - id: explained\n    rubric: The notes say why the answer is 42\n" +
			"  - id: hidden\n    describe: The answer on the test set\n    held_out: [sh, eval.sh]\n" +
			"---\n# The answer\n",
		"practice/answer/eval.sh": `expected=$(cat "$STUDY_HELDOUT_DIR/expected.txt")` + "\n" + `echo "SECRET-$expected"` + "\n" +
			`if [ "$(cat answer.txt)" = "$expected" ]; then s=1; else s=0; fi` + "\n" +
			`printf '{"score":%s,"max":1,"metrics":{"cases":1},"summary":"%s of 1 cases"}' $s $s > "$STUDY_RESULTS"` + "\n",
		".heldout/answer/expected.txt": "42\n",
		"practice/answer/page1.jpg":    "\xff\xd8 photo",
	} {
		path := filepath.Join(home, "c", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c, err := core.Open(options(home, home))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetPhase(context.Background(), "c", core.PhaseSpec{Lesson: "answer", Phase: core.PhasePracticing}); err != nil {
		t.Fatal(err)
	}
	return home
}

func normalised(r result) result {
	r.stdout = eventID.ReplaceAllString(r.stdout, "<id>")
	r.stdout = hashes.ReplaceAllString(r.stdout, "sha256:<hash>")
	return r
}

func TestChecksWithRubricAndHeldOutCriteria(t *testing.T) {
	home := withFullCheck(t)
	topic := filepath.Join(home, "c")
	run := func(args ...string) result {
		t.Helper()
		return normalised(runIn(t, home, topic, args...))
	}

	failed := run("check", "answer")
	if failed.code != cli.ExitOK || strings.Contains(failed.stdout, "SECRET") {
		t.Errorf("study check: exit %d, stdout %s", failed.code, failed.stdout)
	}
	if !strings.Contains(failed.stderr, "Running answer (1 of 2)") || !strings.Contains(failed.stderr, "Running hidden (2 of 2)") {
		t.Errorf("progress on stderr = %q", failed.stderr)
	}
	golden(t, "check_held_out.txt", failed.stdout)

	quiet := run("check", "answer", "--json")
	if quiet.stderr != "" || strings.Contains(quiet.stdout, "SECRET") {
		t.Errorf("study check --json: stderr %q, stdout %s", quiet.stderr, quiet.stdout)
	}

	graded := run("rubric", "grade", "answer", "explained", "--grade", "partly", "--note", "Says what, not why",
		"--looked-at", "page1.jpg", "--json")
	if graded.code != cli.ExitOK {
		t.Errorf("study rubric grade: exit %d, %s", graded.code, graded.stdout)
	}
	golden(t, "rubric_grade.json", graded.stdout)
	golden(t, "rubric_grade_again.txt", run("rubric", "grade", "answer", "explained", "--grade", "partly",
		"--note", "Says what, not why", "--looked-at", "page1.jpg").stdout)
	bad := run("rubric", "grade", "answer", "explained", "--grade", "excellent", "--json")
	if bad.code != cli.ExitUsage {
		t.Errorf("an unknown grade: exit %d, want %d", bad.code, cli.ExitUsage)
	}
	golden(t, "rubric_grade_unknown.json", bad.stdout)

	golden(t, "results.json", run("results", "answer", "--json").stdout)
	golden(t, "results.txt", run("results", "answer").stdout)
}

// TestScoresWithoutAMax: a score read back from a History written by hand,
// or by another version, may have no max; it is shown out of 1.
func TestScoresWithoutAMax(t *testing.T) {
	score := 0.5
	if got := cli.WriteScores(&core.CriterionScores{Score: &score}); !strings.Contains(got, "score 0.5 of 1") {
		t.Errorf("scores = %q", got)
	}
}
