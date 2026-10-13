package plugincontract_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
)

// What diagnose gives back is a short list of findings, each under a name of
// its own that is a lowercase word, each saying whether it is all right and
// in a sentence what was found. A plugin that depends on nothing gives none.
func TestFindingsAreHeldToTheContract(t *testing.T) {
	ok := plugincontract.Finding{Name: "embedding", OK: true, Message: "the embedding endpoint answers"}
	for name, findings := range map[string][]plugincontract.Finding{
		"none":                 nil,
		"an empty list":        {},
		"one that is right":    {ok},
		"one that is wrong":    {{Name: "reranking", Message: "the reranking endpoint did not answer"}},
		"a name with a number": {{Name: "endpoint_2", OK: true, Message: "it answers"}},
		// A message is cut short by whoever shows it, and never refused.
		"a long message": {{Name: "embedding", Message: strings.Repeat("the endpoint is down. ", 4000)}},
		"as many as may be": func() []plugincontract.Finding {
			var many []plugincontract.Finding
			for n := range plugincontract.MaxFindings {
				many = append(many, plugincontract.Finding{Name: fmt.Sprintf("part%d", n), OK: true, Message: "it answers"})
			}
			return many
		}(),
		"a name as long as may be": {{Name: strings.Repeat("n", plugincontract.MaxKindBytes), OK: true, Message: "it answers"}},
	} {
		if err := (plugincontract.DiagnoseResult{Findings: findings}).Validate(); err != nil {
			t.Errorf("%s: Validate() = %v", name, err)
		}
	}

	var tooMany []plugincontract.Finding
	for n := range plugincontract.MaxFindings + 1 {
		tooMany = append(tooMany, plugincontract.Finding{Name: fmt.Sprintf("part%d", n), OK: true, Message: "it answers"})
	}
	for name, tc := range map[string]struct {
		findings []plugincontract.Finding
		want     string
	}{
		"one more than may be":    {tooMany, "it holds 17 findings, more than the 16 an answer may hold"},
		"no name":                 {[]plugincontract.Finding{{OK: true, Message: "it answers"}}, "findings[0] has no name"},
		"a name in capitals":      {[]plugincontract.Finding{ok, {Name: "Embedding", Message: "x"}}, `findings[1] is named "Embedding", which is no name`},
		"a name of two words":     {[]plugincontract.Finding{{Name: "the endpoint", Message: "x"}}, "which is no name: a lowercase word"},
		"a name with a colon":     {[]plugincontract.Finding{{Name: "kb:embedding", Message: "x"}}, "which is no name"},
		"a name a terminal obeys": {[]plugincontract.Finding{{Name: "a\x1b[2Jb", Message: "x"}}, `is named "a\x1b[2Jb", which is no name`},
		"a name that is too long": {[]plugincontract.Finding{{Name: strings.Repeat("n", plugincontract.MaxKindBytes+1), Message: "x"}}, "up to 32 characters"},
		"the same name twice":     {[]plugincontract.Finding{ok, ok}, "it holds the finding embedding twice"},
		"no message":              {[]plugincontract.Finding{{Name: "embedding", OK: true}}, "the finding embedding has no message"},
		"a message of spaces":     {[]plugincontract.Finding{{Name: "embedding", Message: " \n\t"}}, "the finding embedding has no message"},
	} {
		refusedFor(t, name, plugincontract.DiagnoseResult{Findings: tc.findings}.Validate(), tc.want)
	}
}

// An answer is held to its shape before it is read: a finding that leaves
// out whether it is all right has not said that it is not, and an answer
// without its list has not said that there is nothing to test.
func TestADiagnoseAnswerIsHeldToItsShape(t *testing.T) {
	typ := reflect.TypeFor[plugincontract.DiagnoseResult]()
	for _, answer := range []string{
		`{"findings": []}`,
		`{"findings": [{"name": "embedding", "ok": false, "message": "it is down"}], "took_ms": 12}`,
	} {
		if problems := plugincontract.Shape([]byte(answer), typ); len(problems) != 0 {
			t.Errorf("%s: %v", answer, problems)
		}
	}
	for answer, want := range map[string]string{
		`{}`:                 `"findings" is missing`,
		`{"findings": null}`: "findings: it is not a list",
		`{"Findings": []}`:   `"findings" is spelled "Findings"`,
		`{"findings": [{"name": "embedding", "message": "it is down"}]}`:               `findings[0]: "ok" is missing`,
		`{"findings": [{"name": "embedding", "ok": "no", "message": "it is down"}]}`:   "findings[0].ok: it is not true or false",
		`{"findings": [{"name": "embedding", "ok": true}]}`:                            `findings[0]: "message" is missing`,
		`{"findings": [{"name": 7, "ok": true, "message": "it answers"}]}`:             "findings[0].name: it is not text",
		`{"findings": [{"name": "embedding", "OK": true, "message": "it answers"}]}`:   `"ok" is spelled "OK"`,
		`{"findings": {"embedding": {"ok": true, "message": "it answers"}}}`:           "findings: it is not a list",
		`{"findings": [{"name": "embedding", "ok": true, "message": ["it answers"]}]}`: "findings[0].message: it is not text",
	} {
		problems := plugincontract.Shape([]byte(answer), typ)
		if !strings.Contains(strings.Join(problems, "; "), want) {
			t.Errorf("%s: %v; want %q", answer, problems, want)
		}
	}
}
