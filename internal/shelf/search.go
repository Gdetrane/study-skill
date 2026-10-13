package shelf

import (
	"context"
	"errors"
	"fmt"

	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/endpoint"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/index"
)

// searchResult is what Shelf's search gives back: the contract's result,
// and two things of Shelf's own beside it. The contract lets a plugin say
// more than it names, and study reads past what it does not know: these two
// are for whoever asks Shelf itself how a search was made.
//
// What the learner is to know of a search is in the contract's notice,
// which study shows: in words for the learner, what the search did less
// than Shelf was set to, and why, an endpoint that did not answer or Sources
// without vectors, so that such a search is not taken for one that did it
// all. It is left out when there is nothing to say.
type searchResult struct {
	plugincontract.SearchResult
	// Search says how these Passages were found: the kinds of search, as
	// the contract tool names them, that were used for this question.
	Search []string `json:"search"`
	// Reranked says that the reranking endpoint put them in their order.
	Reranked bool `json:"reranked"`
}

const (
	// each is how many Passages each way of searching puts forward for one
	// question: as many as a search may be asked for at most.
	each = plugincontract.MaxSearchLimit
	// byMeaningForOneByWords is how many Passages of the embedding ranking
	// come for each one of the keyword ranking, where the two are put in
	// one order (merge).
	byMeaningForOneByWords = 2
)

// searched is what one search came to.
type searched struct {
	// found are the Passages, the best first, each with its score.
	found []index.Found
	// kinds are the ways of searching that found them, and reranked says
	// that the reranking endpoint ordered them.
	kinds    []string
	reranked bool
	// notices say what the search did less than it was set to.
	notices []string
}

// find answers one search.
//
// By keyword always: the index is there whatever else is, and an exact
// name, a function's or a theorem's, is found by its letters and by nothing
// surer. With an embedding endpoint, by what the question means as well:
// one request gives the question's vector, the Passages nearest it are
// found, and the two rankings are put in one order (merge). With a reranking
// endpoint, one more request has the best of them ordered by a model that
// reads the question beside each Passage.
//
// An endpoint that does not answer takes nothing from the search but what
// it would have added: the search is answered by what is left, and says so.
//
// The question's vector is asked for first, and the collection is then read
// once for both rankings (index.Reading): another copy of Shelf may replace
// a Source's content while a search is under way, and a search gives the
// Source as it was or as it is, never some Passages of each.
func (s *Shelf) find(ctx context.Context, c *index.Collection, in plugincontract.SearchInput) (searched, error) {
	out := searched{kinds: []string{plugincontract.SearchKeyword}}
	var question []float32
	if s.settings.Embedder != nil {
		vector, notice, err := s.question(ctx, c, in)
		if err != nil {
			return out, err
		}
		if question = vector; notice != "" {
			out.notices = append(out.notices, notice)
		}
	}
	reading, err := c.Read(ctx)
	if err != nil {
		return out, err
	}
	defer reading.Close()
	byWords, err := reading.Search(ctx, in.Query, each, in.Sources)
	if err != nil {
		return out, err
	}
	out.found = byWords
	if question != nil {
		e := s.settings.Embedder
		byMeaning, err := reading.Nearest(ctx, index.Made{Model: e.Model(), Document: e.Document()}, question, each, in.Sources)
		if err != nil {
			return out, err
		}
		out.found = merge(byMeaning, byWords)
		out.kinds = append(out.kinds, plugincontract.SearchEmbedding)
	}
	// The reading is over before a reranker is asked: what it orders is in
	// hand, and an endpoint takes its time.
	reading.Close()
	if r := s.settings.Reranker; r != nil && len(out.found) > 1 {
		// The best of them, and at least as many as are asked for: what is
		// returned was all before the reranker.
		candidates := out.found[:min(len(out.found), max(r.Candidates(), in.Limit))]
		texts := make([]string, len(candidates))
		for i, f := range candidates {
			texts[i] = f.Text
		}
		order, err := r.Order(ctx, in.Query, texts)
		var failed *endpoint.Problem
		switch {
		case errors.As(err, &failed):
			out.notices = append(out.notices, "These Passages are in the order the search found them, and not reranked: "+failed.Reason+".")
		case err != nil:
			return out, err
		default:
			reranked := make([]index.Found, len(order))
			for i, ranked := range order {
				reranked[i] = candidates[ranked.Index]
				reranked[i].Score = ranked.Score
			}
			out.found, out.reranked = reranked, true
		}
	}
	if len(out.found) > in.Limit {
		out.found = out.found[:in.Limit]
	}
	return out, nil
}

// question returns the vector of a search's question, to find the Passages
// nearest it by. It returns none, and a notice that says why, when the
// search has to do without: no Source that is searched has vectors a
// question's is compared with, or the endpoint did not give the question's.
// A notice beside a vector says that some of the Sources are searched by
// keyword alone.
//
// The endpoint is asked only when there is a vector to compare the
// question's with.
func (s *Shelf) question(ctx context.Context, c *index.Collection, in plugincontract.SearchInput) (vector []float32, notice string, err error) {
	e := s.settings.Embedder
	sources, err := c.Sources(ctx)
	if err != nil {
		return nil, "", err
	}
	// The Sources that are searched and hold something to find, and those
	// of them whose vectors were made as the settings say now.
	var indexed, fitting []index.Source
	within := map[string]bool{}
	for _, id := range in.Sources {
		within[id] = true
	}
	for _, source := range sources {
		if source.Hash == "" || (in.Sources != nil && !within[source.ID]) {
			continue
		}
		indexed = append(indexed, source)
		if s.fits(source.Made) {
			fitting = append(fitting, source)
		}
	}
	const alone = "These Passages were found by keyword alone: "
	switch {
	case len(indexed) == 0:
		return nil, "", nil // nothing to find, by any way of searching
	case len(fitting) == 0:
		return nil, alone + "none of the Sources searched has vectors of the model " + short(e.Model()) + ", which Shelf's settings name. " +
			"Index them again to have their Passages embedded.", nil
	}
	// The question's vector is of the size the settings name, as the
	// vectors of every Source that fits them are: the endpoint client cuts
	// to it, and refuses a shorter one.
	vector, err = e.Query(ctx, in.Query)
	var failed *endpoint.Problem
	switch {
	case errors.As(err, &failed):
		return nil, alone + failed.Reason + ".", nil
	case err != nil:
		return nil, "", err
	}
	if others := len(indexed) - len(fitting); others > 0 {
		notice = fmt.Sprintf("%d of the %d Sources searched were searched by keyword alone: their Passages have no vectors of the model %s, "+
			"which Shelf's settings name. Index them again to have their Passages embedded.", others, len(indexed), short(e.Model()))
	}
	return vector, notice, nil
}

// merge puts the two rankings of one search in one order: two Passages of
// the embedding ranking, then one of the keyword ranking, and so on, each
// Passage once, where it first comes up.
//
// So the best Passage by what the question means is first, and the best by
// its words is among the first three, whatever the embeddings made of it: a
// Passage that holds a name the question spells, a function's or a
// theorem's, is not lost among Passages that are merely about the same
// thing. And the thirty a reranking endpoint is handed hold the keyword
// ranking's first ten.
//
// Reciprocal rank fusion, the usual way of merging two rankings, was
// measured against this on the vectors of four books and 300 questions.
// With its usual constant of 60 it does neither thing: every Passage that
// is in both lists, about twenty of fifty, comes before one that is first
// in one list alone, so a name only keyword search found stood at place 20,
// and the right Passage was among the first ten for 88 questions in a
// hundred where the embeddings alone had it there for 92. Taken two to one,
// it is there for 92, and among the first thirty for 97 where the
// embeddings alone have 96. With a small constant the fusion finds as much,
// a point more at the top, and promises the keyword ranking's best no
// place: two for one is the one whose order can be said in a sentence. A
// reranker makes the same of either (docs/shelf.md has the tables).
//
// A Passage's score is one over its place: a score says how the Passages of
// one search stand to each other, and nothing else.
func merge(byMeaning, byWords []index.Found) []index.Found {
	out := make([]index.Found, 0, len(byMeaning)+len(byWords))
	taken := map[string]bool{}
	// next takes the best Passage of a ranking that is not taken yet.
	next := func(ranking *[]index.Found) {
		for len(*ranking) > 0 {
			f := (*ranking)[0]
			*ranking = (*ranking)[1:]
			if !taken[f.ID] {
				taken[f.ID] = true
				f.Score = 1 / float64(len(out)+1)
				out = append(out, f)
				return
			}
		}
	}
	for len(byMeaning) > 0 || len(byWords) > 0 {
		for range byMeaningForOneByWords {
			next(&byMeaning)
		}
		next(&byWords)
	}
	return out
}
