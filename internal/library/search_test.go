package library_test

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestSearchRanksFixtureLibrary(t *testing.T) {
	ix, _ := buildLibrary(t, shelf)

	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"quantum mechanics", []string{"Quantum Mechanics", "Classical Mechanics"}},
		{"QUANTUM MECHANICS", []string{"Quantum Mechanics", "Classical Mechanics"}},
		{"mechanics", []string{"Classical Mechanics", "Quantum Mechanics"}},
		{"physics", []string{"Classical Mechanics", "Quantum Mechanics", "Relativity"}},
		{"programming", []string{
			"Python Programming", "The C Programming Language",
			"C++ Primer", "TypeScript Deep Dive",
		}},
		{"signal processing", []string{"Signal Processing First"}},
		{"statistics with R", []string{"Advanced R"}},
		{"go", []string{"Concurrency In Go"}},
		{"c++", []string{"C++ Primer"}},
		{"typescript", []string{"TypeScript Deep Dive"}},
		{"linear algebra", []string{"Linear Algebra Done Right"}},
		{"thermodynamics", []string{"Loose Notes On Thermodynamics"}},
		{"underwater basket weaving", []string{}},
	} {
		if got := titlesOf(ix.Search(tc.query, 0)); !slices.Equal(got, tc.want) {
			t.Errorf("Search(%q) = %q, want %q", tc.query, got, tc.want)
		}
	}
}

// Regression for v1, where "c" matched the substring in "mechanics" and
// "physics", so Quantum Mechanics outranked The C Programming Language.
func TestSearchCRanksTheCBookFirst(t *testing.T) {
	ix, _ := buildLibrary(t, shelf)

	results := ix.Search("C", 10)
	got := titlesOf(results)
	if len(got) == 0 || got[0] != "The C Programming Language" {
		t.Fatalf("Search(C) = %q, want The C Programming Language first", got)
	}
	for _, unwanted := range []string{"Quantum Mechanics", "Classical Mechanics", "C++ Primer"} {
		if slices.Contains(got, unwanted) {
			t.Errorf("Search(C) = %q, should not contain %q", got, unwanted)
		}
	}
}

// Regression for v1, where a book tagged "go" matched "algorithms" because
// "go" is a substring of it.
func TestSearchAlgorithmsDoesNotMatchGo(t *testing.T) {
	ix, _ := buildLibrary(t, shelf)

	goBook := bookTitled(t, ix, "Concurrency In Go")
	if !slices.Equal(goBook.Topics, []string{"go"}) {
		t.Fatalf("fixture: Concurrency In Go topics = %q, want only go", goBook.Topics)
	}
	got := titlesOf(ix.Search("algorithms", 10))
	if want := []string{"Introduction To Algorithms"}; !slices.Equal(got, want) {
		t.Errorf("Search(algorithms) = %q, want %q", got, want)
	}
}

// Ported from v1's matcher tests: a single-letter language name ranks the
// books about that language above generic programming books.
func TestSearchSingleLetterLanguage(t *testing.T) {
	ix, _ := buildLibrary(t, map[string]string{
		"Programming/The_C_Programming_Language_KR.pdf": pdfBytes,
		"Programming/Effective_C_Robert_C_Seacord.pdf":  pdfBytes,
		"Programming/Python_Programming.pdf":            pdfBytes,
		"Springer/Python_Programming_Fundamentals.pdf":  pdfBytes,
		"Science/Unrelated_Book.pdf":                    pdfBytes,
	})

	got := titlesOf(ix.Search("C programming", 10))
	want := []string{
		"The C Programming Language KR",
		"Effective C Robert C Seacord",
		"Python Programming",
		"Python Programming Fundamentals",
	}
	if !slices.Equal(got, want) {
		t.Errorf("Search(C programming) = %q, want %q", got, want)
	}
}

func TestSearchResultsCarryBooksAndScores(t *testing.T) {
	ix, root := buildLibrary(t, shelf)

	results := ix.Search("quantum mechanics", 0)
	if len(results) == 0 {
		t.Fatal("no results")
	}
	top := results[0]
	if want := filepath.Join(root, "Physics", "Quantum_Mechanics.pdf"); top.Path != want {
		t.Errorf("Path = %q, want %q", top.Path, want)
	}
	if top.Category != "Physics" || top.Format != "pdf" || top.Size == 0 {
		t.Errorf("top result = %+v, want the Physics PDF with its size", top.Book)
	}
	// Exact title 10, shared topics 2*5, title words 2*3.
	if top.Score != 26 {
		t.Errorf("Score = %d, want 26", top.Score)
	}
	for i := 1; i < len(results); i++ {
		if results[i].Score > results[i-1].Score {
			t.Errorf("results not ordered by score: %d before %d", results[i-1].Score, results[i].Score)
		}
	}

	// Results are copies: changing one leaves the index alone.
	top.Topics[0] = "changed"
	if again := ix.Search("quantum mechanics", 1); again[0].Topics[0] == "changed" {
		t.Error("changing a result changed the index")
	}
}

func TestSearchLimit(t *testing.T) {
	ix, _ := buildLibrary(t, shelf)

	all := titlesOf(ix.Search("programming", 0))
	if len(all) != 4 {
		t.Fatalf("Search(programming, 0) = %q, want 4 results", all)
	}
	if got := titlesOf(ix.Search("programming", 2)); !slices.Equal(got, all[:2]) {
		t.Errorf("Search(programming, 2) = %q, want %q", got, all[:2])
	}
	if got := titlesOf(ix.Search("programming", -1)); !slices.Equal(got, all) {
		t.Errorf("Search(programming, -1) = %q, want all %q", got, all)
	}
}

func TestSearchWithoutWordsMatchesNothing(t *testing.T) {
	ix, _ := buildLibrary(t, shelf)

	for _, query := range []string{"", "   ", "the of and", "--- ..."} {
		results := ix.Search(query, 10)
		if results == nil || len(results) != 0 {
			t.Errorf("Search(%q) = %#v, want an empty, non-nil slice", query, results)
		}
	}
}
