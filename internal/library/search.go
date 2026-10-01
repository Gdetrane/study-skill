package library

import (
	"cmp"
	"path/filepath"
	"slices"
)

// Points awarded by Search; the package documentation explains each rule.
const (
	exactTitlePoints  = 10
	sharedTopicPoints = 5
	titleWordPoints   = 3
	folderWordPoints  = 1
)

// Result is one book matching a search, with its relevance score.
type Result struct {
	Book
	// Score is the sum of the points the book earned; it is always positive.
	Score int `json:"score"`
}

// Search ranks the books matching query, best first, and returns at most limit
// of them; a limit of zero or less returns every match. A query with no words
// matches nothing. The result is never nil.
func (ix *Index) Search(query string, limit int) []Result {
	results := []Result{}
	queryTokens := tokens(query)
	if len(queryTokens) == 0 {
		return results
	}
	queryWords := slices.Compact(slices.Sorted(slices.Values(queryTokens)))
	queryTopics := topicsOf(query)

	for _, book := range ix.Books {
		score := ix.score(book, queryTokens, queryWords, queryTopics)
		if score == 0 {
			continue
		}
		book.Topics = slices.Clone(book.Topics)
		results = append(results, Result{Book: book, Score: score})
	}

	slices.SortFunc(results, func(a, b Result) int {
		return cmp.Or(
			cmp.Compare(b.Score, a.Score),
			cmp.Compare(a.Title, b.Title),
			cmp.Compare(a.Path, b.Path),
		)
	})
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results
}

func (ix *Index) score(book Book, queryTokens, queryWords, queryTopics []string) int {
	titleTokens := tokens(book.Title)
	score := 0

	if slices.Equal(queryTokens, titleTokens) {
		score += exactTitlePoints
	}
	for _, t := range book.Topics {
		if slices.Contains(queryTopics, t) {
			score += sharedTopicPoints
		}
	}

	var folderTokens []string
	if rel, err := filepath.Rel(ix.Root, book.Path); err == nil {
		for _, folder := range folderNames(rel) {
			folderTokens = append(folderTokens, tokens(folder)...)
		}
	}
	for _, word := range queryWords {
		if slices.Contains(titleTokens, word) {
			score += titleWordPoints
		}
		if slices.Contains(folderTokens, word) {
			score += folderWordPoints
		}
	}
	return score
}
