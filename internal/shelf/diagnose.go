package shelf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/endpoint"
)

// What Shelf depends on besides its own folder are the endpoints its
// settings name: one that gives vectors, and one that orders a search's
// Passages. Shelf starts, speaks the contract and says what is indexed
// whether or not they answer, since none of that asks them. So study asks
// Shelf to test them (the contract's diagnose tool), for study doctor and
// study knowledge-base check: a learner then hears that an endpoint is down
// before a book was read for nothing.
//
// Each endpoint is asked once, as a search asks it: the same request, to the
// same address, with the Recipe's marking and the key, and its answer is
// held to what a search holds it to (endpoint.Embedder.Test,
// endpoint.Reranker.Test). What Shelf sends is text of its own, below, the
// same every time: nothing of a Topic, a Source or a collection, none of
// which the tool is given.

// The names of Shelf's findings.
const (
	findingEmbedding = "embedding"
	findingReranking = "reranking"
)

// What an endpoint is sent to test it: a question, and two short texts for
// a reranker to order.
const testQuestion = "What does a shelf hold?"

var testTexts = []string{"A shelf holds books.", "A lamp gives light."}

// diagnose tests each endpoint the settings name and says what it found: a
// finding for each, the embedding endpoint's first, and none when the
// settings name no endpoint, since Shelf then depends on nothing it could
// test.
//
// The endpoints are asked side by side, each with less time than study
// gives Shelf for the whole answer (Settings.TestTime): an endpoint that
// never answers costs its own finding, and Shelf still answers in time.
//
// A finding's message names an endpoint by its host. It holds no key and
// nothing else of an address: what an endpoint did wrong is an
// endpoint.Problem, whose reason is made so, and ends with what to look at
// when Shelf can say.
func (s *Shelf) diagnose(ctx context.Context, _ json.RawMessage) (any, *plugincontract.ErrorBody) {
	var tests []func() plugincontract.Finding
	if e := s.settings.Embedder; e != nil {
		tests = append(tests, func() plugincontract.Finding {
			vector, answered, err := e.Test(ctx, s.settings.TestTime, testQuestion)
			return found(findingEmbedding, err,
				fmt.Sprintf("%s gave a question's vector, of %d numbers, %s", e.Name(), len(vector), asked(e.Model(), answered)),
				"Without the embedding endpoint no Source is indexed, and a search is by keyword alone.")
		})
	}
	if r := s.settings.Reranker; r != nil {
		tests = append(tests, func() plugincontract.Finding {
			_, answered, err := r.Test(ctx, s.settings.TestTime, testQuestion, testTexts)
			return found(findingReranking, err,
				fmt.Sprintf("%s ordered two texts for a question, %s", r.Name(), asked(r.Model(), answered)),
				"Without the reranking endpoint a search gives its Passages in the order it found them.")
		})
	}
	findings := make([]plugincontract.Finding, len(tests))
	var wg sync.WaitGroup
	for i, test := range tests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			findings[i] = test()
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		// study stopped asking, and reads no answer.
		return nil, refuse(plugincontract.CodeInternal, "Shelf was stopped before it had tested its endpoints")
	}
	return plugincontract.DiagnoseResult{Findings: findings}, nil
}

// asked says which model an endpoint was asked for, and which it says it
// answered with when that is another name. It is not said to be wrong: a
// server names one model in several ways, by its file or with a tag, and
// one that serves a single model answers with it whatever it is asked for.
// Shelf cannot tell these apart, so it says both names and no more.
func asked(model, answered string) string {
	said := "asked for the model " + short(model)
	if answered != "" && answered != model {
		said += ". It names the model it answered with " + short(answered)
	}
	return said
}

// found is the finding for one endpoint's test: all right, with what the
// endpoint did, or not, with why and what Shelf does without the endpoint.
func found(name string, err error, right, without string) plugincontract.Finding {
	var failed *endpoint.Problem
	switch {
	case err == nil:
		return plugincontract.Finding{Name: name, OK: true, Message: right}
	case errors.As(err, &failed):
		// What Shelf does without it is said whole: the reason, which may
		// repeat a server's own words, is what is cut when both do not fit.
		reason := failed.Reason
		if room := plugincontract.MaxFindingBytes - len(". "+without); len(reason) > room {
			reason = plugincontract.Cut(reason, room-len("…")) + "…"
		}
		return plugincontract.Finding{Name: name, Message: reason + ". " + without}
	}
	// The test was stopped: whoever asked reads no answer.
	return plugincontract.Finding{Name: name, Message: "the test was stopped before the endpoint had answered"}
}
