package pluginclient_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mordor-forge/lamplight/v2/internal/pluginclient"
)

// What a plugin wrote reaches a message on one line, cut short, as valid
// text, with nothing in it a terminal would interpret.
func TestWhatAPluginWroteIsCleaned(t *testing.T) {
	for _, tc := range []struct {
		in   string
		max  int
		want string
	}{
		{"database is locked", 100, "database is locked"},
		{"  two\nlines\r\n\tand a tab  ", 100, "two lines and a tab"},
		{"\x1b[31mred\x1b[0m", 100, `\u001b[31mred\u001b[0m`},
		{"title\x1b]0;gotcha\x07", 100, `title\u001b]0;gotcha\u0007`},
		{"a\x00b\x7fc", 100, `a\u0000b\u007fc`},
		{"left" + string(rune(0x202e)) + "right" + string(rune(0x2066)) + "x" + string(rune(0x200f)), 100, "left" + `\u202e` + "right" + `\u2066` + "x" + `\u200f`},
		{"not \xff\xfe text", 100, "not " + string(utf8.RuneError) + " text"},
		{"abcdef", 3, "abc…"},
		{"aéé", 2, "a…"},
		{"", 100, ""},
		{" \n\t ", 100, ""},
		{"déjà vu, 日本語", 100, "déjà vu, 日本語"},
	} {
		got := pluginclient.Clean(tc.in, tc.max)
		if got != tc.want {
			t.Errorf("Clean(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
		}
		if !utf8.ValidString(got) || strings.ContainsFunc(got, func(r rune) bool { return r < 0x20 || r == 0x7f || (r >= 0x202a && r <= 0x202e) }) {
			t.Errorf("Clean(%q, %d) = %q, which a terminal would act on", tc.in, tc.max, got)
		}
	}
}

// Each of a plugin's codes is one of study's documented ones.
func TestAPluginsCodeIsOneOfStudys(t *testing.T) {
	for plugin, study := range map[string]string{
		"invalid_argument": "invalid_argument",
		"not_found":        "not_found",
		"unsupported":      "failed_precondition",
		"internal":         "failed_precondition",
		"":                 "failed_precondition",
		"E_BUSY":           "failed_precondition",
		"canceled":         "failed_precondition",
	} {
		if got := pluginclient.StudyCode(plugin); got != study {
			t.Errorf("StudyCode(%q) = %q, want %q", plugin, got, study)
		}
	}
}
