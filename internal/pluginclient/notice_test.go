package pluginclient_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/mordor-forge/lamplight/v2/internal/pluginclient"
	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
)

// A search can say what it did less than the plugin was set to, in a notice
// beside its Passages. The notice is the plugin's own text, and reaches
// whoever searched as a reason does: on one line, with what a terminal
// would interpret escaped, and cut short, never refused. A search that says
// nothing has none, and what a plugin says of a search in fields of its own
// is not read.
func TestASearchsNoticeIsCleanedAndCutAsAReasonIs(t *testing.T) {
	ctx := context.Background()
	long := "These Passages were found by keyword alone. " + strings.Repeat("é", plugincontract.MaxNoticeBytes)
	// The mark that turns the direction of what follows, and how study
	// writes it.
	turn, turned := string(rune(0x202e)), fmt.Sprintf(`\u%04x`, 0x202e)
	for _, tc := range []struct {
		name string
		// notice is what the plugin says, and want what study makes of it.
		notice, want string
	}{
		{"as it is", "These Passages were found by keyword alone: the embedding endpoint 127.0.0.1:8080 did not answer within 10s.",
			"These Passages were found by keyword alone: the embedding endpoint 127.0.0.1:8080 did not answer within 10s."},
		{"on one line, with nothing a terminal acts on", "found\x1b[2J by\nkeyword " + turn + "alone\ttoday\x07 ",
			`found\u001b[2J by keyword ` + turned + `alone today\u0007`},
		{"cut short", long, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := plugintest.NewComputer(t)
			// The same text as a search's notice and as a Source's reason.
			search, _ := json.Marshal(map[string]any{"passages": []any{}, "notice": tc.notice})
			status, _ := json.Marshal(map[string]any{"sources": []any{map[string]any{"id": "a.1", "state": "failed", "reason": tc.notice}}})
			address := service(t, plugintest.Config{Results: map[string]string{"search": string(search), "status": string(status)}})
			s, err := pluginclient.Start(ctx, c.Env(""), c.RegisterURL(t, "remote", address), quick(c))
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			defer s.Close()
			found, err := s.Search(ctx, "c", "q", 5, nil)
			if err != nil || found.Passages == nil || len(found.Passages) != 0 {
				t.Fatalf("Search = %+v, %v; want the search answered, notice and all", found, err)
			}
			got := found.Notice
			safe(t, got)
			switch {
			case tc.want != "":
				if got != tc.want {
					t.Errorf("the notice is %q, want %q", got, tc.want)
				}
			case len(got) > plugincontract.MaxNoticeBytes+len("…") || len(got) < plugincontract.MaxNoticeBytes-utf8.UTFMax ||
				!strings.HasSuffix(got, "…") || !strings.HasPrefix(got, "These Passages were found by keyword alone. éé") || !utf8.ValidString(got):
				t.Errorf("a notice of %d bytes is kept as %d bytes, %.60q to %q; want its first %d bytes, cut where a character ends, "+
					"and a mark that it was cut", len(tc.notice), len(got), got, got[max(0, len(got)-8):], plugincontract.MaxNoticeBytes)
			}
			// A reason of the same words comes to the same.
			sources, err := s.Status(ctx, "c")
			if err != nil || len(sources) != 1 || sources[0].Reason != got {
				t.Errorf("as a reason the same words are %+v (%v), and as a notice %q", sources, err, got)
			}
		})
	}

	// No notice, an empty one and one of white space are all none.
	for name, result := range map[string]string{
		"left out":    `{"passages": []}`,
		"empty":       `{"passages": [], "notice": ""}`,
		"white space": `{"passages": [], "notice": " \n\t "}`,
	} {
		c := plugintest.NewComputer(t)
		s := start(t, c, "--result", "search="+result)
		if found, err := s.Search(ctx, "c", "q", 5, nil); err != nil || found.Notice != "" || found.Passages == nil {
			t.Errorf("a search with a notice %s = %+v, %v; want no notice", name, found, err)
		}
	}

	// A plugin may say more of a search in fields of its own, as Shelf does
	// with the kinds of search it used and whether it reranked. The answer
	// is the contract's all the same, and nothing of them is read.
	c := plugintest.NewComputer(t)
	s := start(t, c, "--result", `search={"passages": [{"id": "a", "source": "s.1", "text": "x", "score": 2, "rank": 1}], `+
		`"search": ["keyword"], "reranked": false, "notice": "found by keyword alone", "took_ms": 12}`)
	found, err := s.Search(ctx, "c", "q", 5, nil)
	if err != nil || len(found.Passages) != 1 || found.Notice != "found by keyword alone" {
		t.Fatalf("a search with fields of the plugin's own = %+v, %v; want it accepted, with its Passage and its notice", found, err)
	}
	if data, err := json.Marshal(found); err != nil || strings.Contains(string(data), "reranked") || strings.Contains(string(data), "took_ms") ||
		strings.Contains(string(data), "rank") || strings.Contains(string(data), `"search"`) {
		t.Errorf("what study read of that search holds a field of the plugin's own: %s (%v)", data, err)
	}
}

// study leaves the characters that show nothing out of a plugin's words. So
// it leaves them out before it looks for the parts of a registered address
// that may be a key, and not after: a piece of the path, or the query's
// key, that a service repeats with such a character inside it would be
// found apart, let through, and put together again where it is shown.
func TestAnAddressesKeyIsTakenOutThoughWhatShowsNothingDividesIt(t *testing.T) {
	ctx := context.Background()
	const piece, secret = "t0ken-in-the-path", "s3cr3t-k3y-of-the-learner"
	for hidden, name := range map[rune]string{0x200B: "a zero-width space", 0x00AD: "a soft hyphen", 0xFE0F: "a variation selector",
		0xE0101: "a late variation selector", 0xE0041: "a tag"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			split := func(s string) string {
				return s[:5] + string(hidden) + s[5:9] + string(hidden) + string(hidden) + s[9:]
			}
			said := "These Passages were found by keyword alone: the account " + split(piece) + " has no model for the key " + split(secret) + "."
			c := plugintest.NewComputer(t)
			status, _ := json.Marshal(map[string]any{"sources": []any{map[string]any{"id": "a.1", "state": "failed", "reason": said}}})
			address := service(t, plugintest.Config{Notice: said, Results: map[string]string{"status": string(status)},
				Refusals: map[string]string{"passage": "internal:" + said}})
			address = strings.TrimSuffix(address, "/mcp") + "/" + piece + "/mcp?key=" + secret
			s, err := pluginclient.Start(ctx, c.Env(""), c.RegisterURL(t, "remote", address), quick(c))
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			defer s.Close()
			found, err := s.Search(ctx, "c", "q", 5, nil)
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			sources, err := s.Status(ctx, "c")
			if err != nil || len(sources) != 1 {
				t.Fatalf("Status = %+v, %v", sources, err)
			}
			_, refused := s.Passage(ctx, "c", "a")
			if refused == nil {
				t.Fatal("Passage was not refused")
			}
			const want = "These Passages were found by keyword alone: the account … has no model for the key …."
			for what, got := range map[string]string{"a notice": found.Notice, "a reason": sources[0].Reason, "a refusal": refused.Error()} {
				if !strings.Contains(got, want) {
					t.Errorf("%s that repeats the address's key with %s inside it is shown as %q, want %q", what, name, got, want)
				}
				// Neither whole nor in its pieces, one after the other.
				for _, part := range []string{piece, secret, piece[:5], piece[9:], secret[:5], secret[9:]} {
					if strings.Contains(got, part) {
						t.Errorf("%s that repeats the address's key with %s inside it is shown with %q of it: %s", what, name, part, got)
					}
				}
			}
		})
	}
}

// A service that repeats the address it is registered at in a notice is
// shown without the address's path and query, which may be a key, as in
// everything else it writes.
func TestANoticeIsShownWithoutTheAddressesKey(t *testing.T) {
	ctx := context.Background()
	c := plugintest.NewComputer(t)
	var registered atomic.Pointer[string]
	address := service(t, plugintest.Config{Twist: func(tool string, answer []byte, _ bool) []byte {
		if address := registered.Load(); address != nil {
			return []byte(strings.ReplaceAll(string(answer), "ADDRESS", *address))
		}
		return answer
	}, Notice: "These Passages were found by keyword alone: ADDRESS has no model loaded."})
	address = strings.TrimSuffix(address, "/mcp") + "/t0ken-in-the-path/mcp?key=s3cr3t-k3y-of-the-learner"
	registered.Store(&address)
	s, err := pluginclient.Start(ctx, c.Env(""), c.RegisterURL(t, "remote", address), quick(c))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()
	found, err := s.Search(ctx, "c", "q", 5, nil)
	if want := "These Passages were found by keyword alone: " + pluginclient.Address(address) + "… has no model loaded."; err != nil ||
		found.Notice != want {
		t.Errorf("Search = %+v, %v; want the notice %q", found, err, want)
	}
	if strings.Contains(found.Notice, "s3cr3t") || strings.Contains(found.Notice, "t0ken") {
		t.Errorf("the notice is shown with the address's key: %s", found.Notice)
	}
}
