package endpoint_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/shelf/endpoint"
)

// refusal is what Shelf says of an embedding endpoint, served below path,
// that refuses a request in the words says gives it: says is handed the
// request, with the Authorization header as it arrived. The endpoint's host
// is written HOST.
func refusal(t *testing.T, path, key string, says func(r *http.Request) string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		// %q writes ASCII as JSON writes it.
		fmt.Fprintf(w, `{"error": {"message": %q}}`, says(r))
	}))
	t.Cleanup(server.Close)
	e := embedder(t, endpoint.Embedding{Address: server.URL + path, Key: key, Model: "m", Dimensions: 4})
	_, err := e.Query(ctx, "a question")
	host := strings.TrimPrefix(server.URL, "http://")
	said := reason(t, err, host, "the embedding endpoint "+host+" answered 403 Forbidden: ")
	_, said, _ = strings.Cut(said, "answered 403 Forbidden: ")
	return strings.TrimSuffix(strings.ReplaceAll(said, host, "HOST"), ". Check the key in Shelf's settings file")
}

// The key Shelf sends is taken out of a server's words wherever it stands
// and whatever it holds. A key may hold what ends a word in a header's
// value, a comma, a semicolon or either quotation mark, and a server that
// repeats the header it was sent repeats such a key after "Bearer": no part
// of it is left behind, bare, after "Bearer", in a quoted header, or twice
// in one sentence.
func TestTheKeyShelfSentIsTakenOutWhateverItHolds(t *testing.T) {
	for _, key := range []string{
		"sk-comma,5b20cc91e7a4", "sk-semicolon;5b20cc91e7a4", `sk-quote"5b20cc91e7a4`, "sk-tick'5b20cc91e7a4",
		`sk,every;mark"in'one-5b20cc91e7a4`, "sk-plain-5b20cc91e7a4",
	} {
		for name, tc := range map[string]struct {
			says func(sent string) string
			want string
		}{
			"bare": {func(string) string { return "the key " + key + " is not one of ours" },
				"the key [the key] is not one of ours"},
			"after Bearer": {func(sent string) string { return "no access for " + sent + " here" },
				"no access for Bearer [the key] here"},
			"in a quoted header": {func(sent string) string { return `refused the header "Authorization": "` + sent + `", sent by you` },
				`refused the header "Authorization": "Bearer [the key]", sent by you`},
			"twice in one sentence": {func(sent string) string { return sent + " is refused, and so is " + key + " by itself, and " + sent },
				"Bearer [the key] is refused, and so is [the key] by itself, and Bearer [the key]"},
			"in quotation marks after Bearer": {func(string) string { return `refused Bearer "` + key + `" and bearer: '` + key + `'` },
				"refused Bearer [the key] and Bearer [the key]"},
		} {
			got := refusal(t, "/v1", key, func(r *http.Request) string { return tc.says(r.Header.Get("Authorization")) })
			if got != tc.want {
				t.Errorf("the key %s, repeated %s: Shelf says %q, want %q", key, name, got, tc.want)
			}
			// No stretch of the key between two of those marks is left.
			for _, piece := range strings.FieldsFunc(key, func(r rune) bool { return strings.ContainsRune(`,;"'`, r) }) {
				if strings.Contains(got, piece) {
					t.Errorf("the key %s, repeated %s: Shelf repeats %q of it: %s", key, name, piece, got)
				}
			}
		}
	}
}

// study leaves the characters that show nothing out of a plugin's words. A
// key, or a name of the address, that a server repeats with such a
// character inside it would be whole again there, where the learner and
// the agent read it. So Shelf leaves them out first, and finds the key and
// the name where it looks for them.
func TestWhatShowsNothingHidesNoKeyAndNoName(t *testing.T) {
	const account = "acct-0123456789abcdef"
	for name, hidden := range map[string]rune{
		"a variation selector": 0xFE0F, "a late variation selector": 0xE0101, "a Hangul filler": 0x3164,
		"a combining grapheme joiner": 0x034F, "a zero-width space": 0x200B, "a word joiner": 0x2060, "a soft hyphen": 0x00AD,
	} {
		split := func(s string) string {
			return s[:5] + string(hidden) + s[5:9] + string(hidden) + string(hidden) + s[9:]
		}
		got := refusal(t, "/"+account+"/v1", key, func(*http.Request) string {
			return "the key " + split(key) + " of the account " + split(account) + " is refused, and so is Bearer " + split(key)
		})
		if want := "the key [the key] of the account [the address] is refused, and so is Bearer [the key]"; got != want {
			t.Errorf("a server repeats the key and the account with %s inside them: Shelf says %q, want %q", name, got, want)
		}
		if strings.ContainsRune(got, hidden) {
			t.Errorf("Shelf repeats %s, U+%04X, which shows nothing: %q", name, hidden, got)
		}
	}
}

// Whatever follows the word "Bearer" in a server's words is a key, Shelf's
// own or not: a gateway that sends its own key on repeats that one. It is
// taken out in any letter case of the word, and also where it stands behind
// a colon, an equals sign or a quotation mark. A word that only begins like
// it is left alone.
func TestWhatFollowsBearerIsTakenOut(t *testing.T) {
	const gateway = "sk-gateway-0123456789abcdef"
	for says, want := range map[string]string{
		"upstream refused Authorization: Bearer " + gateway + ", try later": "upstream refused Authorization: Bearer [the key], try later",
		"upstream refused BEARER " + gateway:                                "upstream refused Bearer [the key]",
		"upstream refused bearer   " + gateway + " twice":                   "upstream refused Bearer [the key] twice",
		`the gateway sent Authorization: Bearer "` + gateway + `" on`:       "the gateway sent Authorization: Bearer [the key] on",
		"the gateway sent Bearer '" + gateway + "' on":                      "the gateway sent Bearer [the key] on",
		"upstream refused Bearer: " + gateway:                               "upstream refused Bearer [the key]",
		"upstream refused Bearer:" + gateway + ".":                          "upstream refused Bearer [the key]",
		"upstream refused authorization=Bearer=" + gateway:                  "upstream refused authorization=Bearer [the key]",
		// As a server writes a header's value inside its own JSON.
		`upstream refused {"authorization": "Bearer = \"` + gateway + `\""}`: `upstream refused {"authorization": "Bearer [the key]"}`,
		// Behind the blank that is no white space, which study shows as a
		// space: the key would follow "Bearer" there.
		"upstream refused Bearer\u2800" + gateway + "\u2800again": "upstream refused Bearer [the key] again",
		// A quotation mark that is never closed.
		`upstream refused Bearer "` + gateway: "upstream refused Bearer [the key]",
		// No key follows these.
		"the bearers of this token are refused": "the bearers of this token are refused",
		"this endpoint takes no Bearer":         "this endpoint takes no Bearer",
	} {
		got := refusal(t, "/v1", key, func(*http.Request) string { return says })
		if got != want || strings.Contains(got, gateway) {
			t.Errorf("a server says %q: Shelf says %q, want %q", says, got, want)
		}
	}
}

// The address but for its host is taken out as the settings gave it and as
// it was sent: a path that holds what a URL escapes is repeated by a server
// in either form, as is a name of it that is long enough to be nobody
// else's. And such a name is taken out in any letter case: a server that
// writes its accounts in capitals writes this one so.
func TestTheAddressIsTakenOutAsItWasSentAndInAnyLetterCase(t *testing.T) {
	const (
		escaped   = "acct%20of%20the%20learner%200123456789"
		unescaped = "acct of the learner 0123456789"
		account   = "acct-0123456789abcdef"
	)
	for name, tc := range map[string]struct {
		path string
		says func(r *http.Request) string
		want string
	}{
		"the path as it was sent": {"/" + escaped + "/v1", func(r *http.Request) string { return "no route for POST " + r.RequestURI + " here" },
			"no route for POST [the address] here"},
		"the path as it reads": {"/" + escaped + "/v1", func(r *http.Request) string { return "no route for POST " + r.URL.Path + " here" },
			"no route for POST [the address] here"},
		"the address as it was sent": {"/" + escaped + "/v1", func(r *http.Request) string { return "nothing at http://" + r.Host + r.RequestURI },
			"nothing at [the address]"},
		"the address as it reads": {"/" + escaped + "/v1", func(r *http.Request) string { return "nothing at http://" + r.Host + r.URL.Path },
			"nothing at [the address]"},
		"the address the settings gave, as it was sent": {"/" + escaped + "/v1", func(r *http.Request) string {
			return "nothing below http://" + r.Host + "/" + escaped + "/v1 for you"
		}, "nothing below [the address] for you"},
		"a name of the path as it was sent": {"/" + escaped + "/v1", func(*http.Request) string { return "the account " + escaped + " has no model" },
			"the account [the address] has no model"},
		"a name of the path as it reads": {"/" + escaped + "/v1", func(*http.Request) string { return "the account " + unescaped + " has no model" },
			"the account [the address] has no model"},
		"a name of the path in capitals": {"/" + account + "/v1", func(*http.Request) string {
			return "the account " + strings.ToUpper(account) + " has no model"
		}, "the account [the address] has no model"},
		"the path in capitals": {"/" + account + "/v1", func(r *http.Request) string { return "no route for POST " + strings.ToUpper(r.URL.Path) },
			"no route for POST [the address]"},
		"a name of the path as it was sent, its escapes in small letters": {"/acct%C3%A9-0123456789/v1", func(*http.Request) string {
			return "the account acct%c3%a9-0123456789 has no model"
		}, "the account [the address] has no model"},
	} {
		got := refusal(t, tc.path, key, tc.says)
		if got != tc.want {
			t.Errorf("a server repeats %s: Shelf says %q, want %q", name, got, tc.want)
		}
		for _, part := range []string{escaped, unescaped, account, strings.ToUpper(account), "0123456789", "/v1", "/V1"} {
			if strings.Contains(got, part) {
				t.Errorf("a server repeats %s: Shelf repeats %q of the address: %s", name, part, got)
			}
		}
	}
}
