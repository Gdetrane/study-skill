package shelf

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/endpoint"
)

// A finding that is not all right ends with what Shelf does without the
// endpoint, whole, however long the reason is: an endpoint is named by its
// host, which the learner chose, and its own words are repeated. What is cut
// to fit what study shows of a finding is the reason, at the end of a
// character.
func TestAFindingKeepsWhatShelfDoesWithoutTheEndpoint(t *testing.T) {
	const without = "Without the embedding endpoint no Source is indexed, and a search is by keyword alone."
	short := &endpoint.Problem{Reason: "the embedding endpoint 127.0.0.1:8080 could not be reached: connection refused"}
	if f := found(findingEmbedding, short, "it answers", without); f.OK || f.Name != "embedding" || f.Message != short.Reason+". "+without {
		t.Errorf("a finding with a short reason = %+v", f)
	}
	for name, reason := range map[string]string{
		"a long host":            "the embedding endpoint " + strings.Repeat("a-very-long-name.", 60) + "example.org could not be reached",
		"words of two bytes":     "the embedding endpoint 127.0.0.1:8080 answered 503 Service Unavailable: " + strings.Repeat("é", 700),
		"just over what fits":    strings.Repeat("x", plugincontract.MaxFindingBytes-len(". "+without)+1),
		"exactly what fits":      strings.Repeat("x", plugincontract.MaxFindingBytes-len(". "+without)),
		"a character at the cut": strings.Repeat("x", plugincontract.MaxFindingBytes-len(". "+without)-4) + "€€€",
	} {
		f := found(findingEmbedding, &endpoint.Problem{Reason: reason}, "it answers", without)
		cut := len(reason) > plugincontract.MaxFindingBytes-len(". "+without)
		switch {
		case f.OK, len(f.Message) > plugincontract.MaxFindingBytes, !utf8.ValidString(f.Message), !strings.HasSuffix(f.Message, ". "+without),
			!strings.HasPrefix(f.Message, reason[:20]):
			t.Errorf("%s: the finding is %d bytes: %s\nwant at most %d, with what Shelf does without the endpoint whole", name, len(f.Message),
				f.Message, plugincontract.MaxFindingBytes)
		case cut != strings.HasSuffix(f.Message, "…. "+without):
			t.Errorf("%s: a reason of %d bytes was cut: %v; the finding is %s", name, len(reason), !cut, f.Message)
		case !cut && f.Message != reason+". "+without:
			t.Errorf("%s: a reason that fits was changed: %s", name, f.Message)
		}
	}
	// All right, it says what the endpoint did; and a test that was stopped
	// is no finding that anything is right.
	if f := found(findingReranking, nil, "it ordered two texts", "Without it."); !f.OK || f.Message != "it ordered two texts" {
		t.Errorf("a finding that is all right = %+v", f)
	}
	if f := found(findingReranking, context.Canceled, "it ordered two texts", "Without it."); f.OK || f.Message == "" {
		t.Errorf("the finding of a test that was stopped = %+v", f)
	}
}

// An endpoint has the test's own time when the settings name none: the
// eight seconds that leave Shelf room to answer within study's ten, and not
// the time an endpoint has for a batch of a Source, which is minutes.
func TestTheTimeOfATestIsItsOwnByDefault(t *testing.T) {
	const eight = 8 * time.Second
	if got := (Settings{}).orDefault().TestTime; got != endpoint.DefaultTestTimeout || endpoint.DefaultTestTimeout != eight {
		t.Errorf("an endpoint has %s to answer Shelf's test by default, want %s, which is to be %s", got, endpoint.DefaultTestTimeout, eight)
	}
	if got := (Settings{TestTime: time.Second}).orDefault().TestTime; got != time.Second {
		t.Errorf("a time the settings name is not kept: %s", got)
	}
}
