package pluginclient_test

import (
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mordor-forge/lamplight/v2/internal/pluginclient"
	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
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
		// The tag block: characters that show nothing and spell out text.
		{"found" + string(rune(0xE0049)) + string(rune(0xE0067)) + string(rune(0xE007F)) + " by keyword", 100, `found\ue0049\ue0067\ue007f by keyword`},
		{string(rune(0xE0001)) + "x" + string(rune(0xE0000)), 100, `\ue0001x\ue0000`},
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
		if !utf8.ValidString(got) || strings.ContainsFunc(got, func(r rune) bool {
			return r < 0x20 || r == 0x7f || (r >= 0x202a && r <= 0x202e) || (r >= 0xE0000 && r <= 0xE007F)
		}) {
			t.Errorf("Clean(%q, %d) = %q, which a terminal would act on, or which shows nothing of what it holds", tc.in, tc.max, got)
		}
	}
}

// Cleaning costs what it keeps, not what it is given: a plugin's message can
// be as long as a message may be, and study shows two thousand bytes of it.
func TestCleaningALongTextCostsWhatItKeeps(t *testing.T) {
	long := strings.Repeat("the index is locked by another process; ", plugincontract.MaxMessageBytes/40)
	if len(long) < 15<<20 {
		t.Fatalf("the text is %d bytes", len(long))
	}
	var got string
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range 10 {
		got = pluginclient.Clean(long, plugincontract.MaxTextBytes)
	}
	runtime.ReadMemStats(&after)
	if !strings.HasPrefix(got, "the index is locked by another process; the index") || !strings.HasSuffix(got, "…") ||
		len(got) > plugincontract.MaxTextBytes+len("…") {
		t.Errorf("Clean kept %d bytes: %.80q…", len(got), got)
	}
	if grown := (after.TotalAlloc - before.TotalAlloc) / 10; grown > 64<<10 {
		t.Errorf("cleaning %d MiB to keep %d bytes allocated %d bytes each time", len(long)>>20, plugincontract.MaxTextBytes, grown)
	}
	// White space without end is no text, and costs nothing either.
	if got := pluginclient.Clean(strings.Repeat(" \n", 8<<20), 100); got != "" {
		t.Errorf("Clean of white space = %q", got)
	}
}

// Of what a program wrote before it failed, the end is kept: it is the last
// line that says why.
func TestTheEndOfWhatAPluginWroteIsKept(t *testing.T) {
	for _, tc := range []struct {
		in   string
		max  int
		want string
	}{
		{"", 100, ""},
		{"short enough", 100, "short enough"},
		{"Traceback (most recent call last):\n  File \"kb.py\", line 3\nKeyError: 'a'\n", 100, `Traceback (most recent call last): File "kb.py", line 3 KeyError: 'a'`},
		{"loading the model... " + strings.Repeat("progress ", 100) + "\nRuntimeError: out of memory\n", 60,
			"…progress progress progress RuntimeError: out of memory"},
		{strings.Repeat("x", 500) + " the end", 20, "…the end"},
		{strings.Repeat("é", 500), 21, "…" + strings.Repeat("é", 10)},
		{"red \x1b[31m" + strings.Repeat("alert ", 50), 40, "…alert alert alert alert alert alert"},
	} {
		got := pluginclient.CleanEnd(tc.in, tc.max)
		if got != tc.want {
			t.Errorf("CleanEnd(%.40q…, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
		}
		if len(got) > tc.max+len("…") || !utf8.ValidString(got) {
			t.Errorf("CleanEnd(%.40q…, %d) kept %d bytes: %q", tc.in, tc.max, len(got), got)
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

// What the MCP library read of a result is written out again as it came. A
// writer for web pages would make six bytes of each < and &, and a plugin's
// answer that is one long text of them six times the size.
func TestAStructuredResultIsWrittenAsItCame(t *testing.T) {
	text := strings.Repeat("<&>", 1000)
	data, ok := pluginclient.Payload(&mcp.CallToolResult{StructuredContent: map[string]any{"text": text}})
	if !ok || !strings.Contains(string(data), text) {
		t.Fatalf("the result was written as %d bytes, ok=%v, and not with its text as it came", len(data), ok)
	}
	if len(data) > len(text)+64 {
		t.Errorf("a text of %d bytes was written as %d", len(text), len(data))
	}
}

// How many values a message holds is counted before the MCP library reads
// it: every list and object, and every member and element after the first.
func TestTheValuesOfAMessageAreCounted(t *testing.T) {
	for raw, want := range map[string]int{
		``:                                 0,
		`"text"`:                           0,
		`{}`:                               1,
		`[[],[],[]]`:                       6,
		`{"a": 1, "b": [1, 2, 3]}`:         5,
		`{"text": "a, b, [c] and {d}"}`:    1,
		`{"text": "she said \"a, b\", c"}`: 1,
		`{"text": "a\\", "more": [1, 2]}`:  4,
	} {
		if got := pluginclient.Values([]byte(raw)); got != want {
			t.Errorf("%s holds %d values, want %d", raw, got, want)
		}
	}
}
