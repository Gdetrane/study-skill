package library

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// topicTable lists every topic with the extra terms that also indicate it. A
// topic's own name is always one of its terms. A term ending in "*" is a stem:
// its last word matches any token that starts with it. Terms are normalized
// with the same tokens function as titles and queries.
var topicTable = []struct {
	name  string
	extra []string
}{
	// Physics
	{"physics", nil},
	{"quantum mechanics", []string{"quantum"}},
	{"mechanics", []string{"mechanic*"}},
	{"relativity", []string{"relativistic"}},
	{"thermodynamics", []string{"thermo*"}},
	{"electromagnetism", []string{"electromagnet*", "electrodynamic*"}},
	{"optics", []string{"optic*"}},
	{"nuclear physics", []string{"nuclear"}},
	{"particle physics", []string{"particle*"}},
	{"astrophysics", []string{"astro*"}},
	{"cosmology", []string{"cosmolog*"}},

	// Chemistry and biology
	{"chemistry", nil},
	{"organic chemistry", []string{"organic"}},
	{"inorganic chemistry", []string{"inorganic"}},
	{"biochemistry", []string{"biochem*"}},
	{"biology", []string{"biolog*"}},
	{"genetics", []string{"genom*"}},
	{"evolution", []string{"evolution*"}},
	{"ecology", []string{"ecolog*"}},

	// Mathematics
	{"mathematics", []string{"math*"}},
	{"algebra", []string{"algebra*"}},
	{"linear algebra", nil},
	{"calculus", nil},
	{"geometry", []string{"geometr*"}},
	{"topology", []string{"topolog*"}},
	{"statistics", []string{"statistic*"}},
	{"probability", []string{"probabilit*"}},
	{"differential equations", []string{"differential equation*"}},
	{"analysis", nil},
	{"number theory", nil},

	// Computing
	{"programming", nil},
	{"algorithms", []string{"algorithm*"}},
	{"data structures", []string{"data structure*"}},
	{"machine learning", nil},
	{"deep learning", nil},
	{"neural networks", []string{"neural"}},
	{"artificial intelligence", []string{"ai"}},
	{"computer science", nil},
	{"software engineering", []string{"software"}},
	{"operating systems", []string{"operating system*"}},
	{"networking", []string{"computer network*", "network protocol*", "tcp"}},
	{"databases", []string{"database*"}},
	{"cryptography", []string{"crypto*"}},
	{"linux", nil},

	// Engineering
	{"engineering", []string{"engineer*"}},
	{"circuits", []string{"circuit*"}},
	{"signal processing", []string{"signal*"}},
	{"control systems", []string{"control system*", "control theory", "feedback control"}},
	{"fluid dynamics", []string{"fluid*"}},
	{"materials science", []string{"material*"}},

	// Programming languages. Short names only ever match a whole token.
	{"c", nil},
	{"c++", []string{"cpp"}},
	{"c#", []string{"csharp"}},
	{"go", []string{"golang"}},
	{"haskell", nil},
	{"java", nil},
	{"javascript", []string{"js"}},
	{"julia", nil},
	{"kotlin", nil},
	{"python", nil},
	{"r", nil},
	{"ruby", nil},
	{"rust", nil},
	{"scala", nil},
	{"sql", nil},
	{"swift", nil},
	{"typescript", nil},
}

// minStemLength keeps stems from turning back into substring matching.
const minStemLength = 4

type term struct {
	words []string // normalized tokens
	stem  bool     // the last word matches as a token prefix
}

type topic struct {
	name  string
	terms []term
}

var topicIndex = compileTopics()

func compileTopics() []topic {
	compiled := make([]topic, 0, len(topicTable))
	for _, entry := range topicTable {
		t := topic{name: entry.name}
		for _, phrase := range append([]string{entry.name}, entry.extra...) {
			t.terms = append(t.terms, compileTerm(phrase))
		}
		compiled = append(compiled, t)
	}
	return compiled
}

func compileTerm(phrase string) term {
	stem := strings.HasSuffix(phrase, "*")
	words := tokens(strings.TrimSuffix(phrase, "*"))
	if len(words) == 0 {
		panic(fmt.Sprintf("library: topic term %q has no words", phrase))
	}
	if stem && utf8.RuneCountInString(words[len(words)-1]) < minStemLength {
		panic(fmt.Sprintf("library: topic stem %q is shorter than %d letters", phrase, minStemLength))
	}
	return term{words: words, stem: stem}
}

// matchesAt reports whether the term occurs at position i of toks.
func (t term) matchesAt(toks []string, i int) bool {
	if i+len(t.words) > len(toks) {
		return false
	}
	last := len(t.words) - 1
	for j, word := range t.words {
		tok := toks[i+j]
		if j == last && t.stem {
			if !strings.HasPrefix(tok, word) {
				return false
			}
			continue
		}
		if tok != word {
			return false
		}
	}
	return true
}

func (t term) occursIn(toks []string) bool {
	for i := range toks {
		if t.matchesAt(toks, i) {
			return true
		}
	}
	return false
}

// topicsOf returns the sorted topics found in the given texts. Each text is
// matched on its own, so a phrase never spans two folder names.
func topicsOf(texts ...string) []string {
	found := []string{}
	for _, text := range texts {
		toks := tokens(text)
		for _, t := range topicIndex {
			if slices.Contains(found, t.name) {
				continue
			}
			for _, tm := range t.terms {
				if tm.occursIn(toks) {
					found = append(found, t.name)
					break
				}
			}
		}
	}
	slices.Sort(found)
	return found
}
