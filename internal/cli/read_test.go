package cli_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/cli"
)

// localTime matches the local times study history prints, which depend on
// the machine's time zone.
var localTime = regexp.MustCompile(`\b\d{1,2} [A-Z][a-z]{2} \d{4} \d{2}:\d{2}\b`)

func TestLessonAndHistoryCommands(t *testing.T) {
	setGitIdentity(t)
	home := withFullCheck(t)
	for _, tc := range []struct {
		golden string
		args   []string
		code   int
	}{
		{"lesson.json", []string{"lesson", "answer", "--topic", "c", "--json"}, cli.ExitOK},
		{"lesson.txt", []string{"lesson", "answer", "--topic", "c"}, cli.ExitOK},
		{"lesson_unknown.json", []string{"lesson", "nowhere", "--topic", "c", "--json"}, cli.ExitError},
		{"history.json", []string{"history", "c", "--json"}, cli.ExitOK},
		{"history.txt", []string{"history", "c"}, cli.ExitOK},
		{"history_phases.txt", []string{"history", "c", "--type", "phase.", "--lesson", "answer"}, cli.ExitOK},
		{"history_bad_limit.json", []string{"history", "c", "--limit", "-1", "--json"}, cli.ExitUsage},
	} {
		t.Run(tc.golden, func(t *testing.T) {
			r := normalised(run(t, home, tc.args...))
			if r.code != tc.code {
				t.Errorf("exit %d, want %d (stderr %s)", r.code, tc.code, r.stderr)
			}
			for _, leak := range []string{"SECRET", "expected.txt", ".heldout"} {
				if strings.Contains(r.stdout, leak) {
					t.Errorf("the output shows Held-out data (%s):\n%s", leak, r.stdout)
				}
			}
			golden(t, tc.golden, localTime.ReplaceAllString(r.stdout, "<time>"))
		})
	}
}
