package library_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/library"
)

func TestBuildFindsEveryBookAndNothingElse(t *testing.T) {
	ix, root := buildLibrary(t, shelf)

	if ix.Root != root {
		t.Errorf("Root = %q, want %q", ix.Root, root)
	}
	if len(ix.Skipped) != 0 {
		t.Errorf("Skipped = %q, want none", ix.Skipped)
	}

	var got []string
	for _, b := range ix.Books {
		rel, err := filepath.Rel(root, b.Path)
		if err != nil || !filepath.IsAbs(b.Path) {
			t.Errorf("Path %q is not an absolute path under %q", b.Path, root)
		}
		got = append(got, filepath.ToSlash(rel))
		if b.Size <= 0 {
			t.Errorf("%s: Size = %d, want the file size", rel, b.Size)
		}
	}
	want := []string{
		"CS/Introduction_to_Algorithms.pdf",
		"Engineering/Signal_Processing_First.djvu",
		"Fiction/Novel.EPUB",
		"Go/Concurrency_in_Go.pdf",
		"Math/Linear_Algebra_Done_Right.pdf",
		"Physics/Classical.Mechanics.PDF",
		"Physics/Quantum_Mechanics.pdf",
		"Programming/C++_Primer.epub",
		"Programming/Python_Programming.pdf",
		"Programming/The_C_Programming_Language.pdf",
		"Programming/TypeScript_Deep_Dive.pdf",
		"Science/Physics/Advanced/relativity.pdf",
		"Statistics/Advanced_R.pdf",
		"Unknown/10 (335)/10 - Unknown.1007%2f978-1-4614-6227-9",
		"loose_notes-on.thermodynamics.pdf",
		"too__many___underscores.mobi",
	}
	if !slices.Equal(got, want) {
		t.Errorf("books =\n%q\nwant\n%q", got, want)
	}
}

// ADR-0003: lowercase suffixes, so mixed-case extensions are found and
// dotted stems still give clean titles.
func TestBuildMixedCaseExtensions(t *testing.T) {
	ix, _ := buildLibrary(t, shelf)

	classical := bookTitled(t, ix, "Classical Mechanics")
	if classical.Format != "pdf" || classical.Category != "Physics" {
		t.Errorf("Classical.Mechanics.PDF = %+v, want format pdf in Physics", classical)
	}
	novel := bookTitled(t, ix, "Novel")
	if novel.Format != "epub" || novel.Category != "Fiction" {
		t.Errorf("Novel.EPUB = %+v, want format epub in Fiction", novel)
	}
	signal := bookTitled(t, ix, "Signal Processing First")
	if signal.Format != "djvu" {
		t.Errorf("Signal_Processing_First.djvu format = %q, want djvu", signal.Format)
	}
}

// ADR-0003: a PDF without a supported suffix is found by its signature and
// keeps its whole name, which often encodes a DOI, as its title.
func TestBuildPDFSignatureWithoutExtension(t *testing.T) {
	ix, root := buildLibrary(t, shelf)

	name := "10 - Unknown.1007%2f978-1-4614-6227-9"
	var found *library.Book
	for i, b := range ix.Books {
		if filepath.Base(b.Path) == name {
			found = &ix.Books[i]
		}
	}
	if found == nil {
		t.Fatalf("signature-only PDF %q not indexed", name)
	}
	if found.Format != "pdf" {
		t.Errorf("Format = %q, want pdf", found.Format)
	}
	if want := "10 Unknown 1007%2f978 1 4614 6227 9"; found.Title != want {
		t.Errorf("Title = %q, want %q", found.Title, want)
	}
	if found.Category != "Unknown" {
		t.Errorf("Category = %q, want Unknown", found.Category)
	}
	if want := filepath.Join(root, "Unknown", "10 (335)", name); found.Path != want {
		t.Errorf("Path = %q, want %q", found.Path, want)
	}
}

func TestBuildCategories(t *testing.T) {
	ix, _ := buildLibrary(t, shelf)

	for title, want := range map[string]string{
		"Loose Notes On Thermodynamics": "Uncategorized",
		"Too Many Underscores":          "Uncategorized",
		"Introduction To Algorithms":    "CS", // acronyms keep their case
		"Relativity":                    "Science",
		"Quantum Mechanics":             "Physics",
	} {
		if got := bookTitled(t, ix, title).Category; got != want {
			t.Errorf("%s: Category = %q, want %q", title, got, want)
		}
	}
}

// ADR-0003: dots, underscores and hyphens in stems become spaces before
// title-casing (the v1 dirname_to_title cases).
func TestBuildTitlesFromSeparatorHeavyNames(t *testing.T) {
	for name, want := range map[string]string{
		"Advanced_Quantum_Mechanics.pdf": "Advanced Quantum Mechanics",
		"intro-to-physics.pdf":           "Intro To Physics",
		"my_book-name.pdf":               "My Book Name",
		"some.book.name.pdf":             "Some Book Name",
		"too__many___underscores.pdf":    "Too Many Underscores",
		"Physics.pdf":                    "Physics",
		"Already Clean.pdf":              "Already Clean",
		" spaced -_. out .pdf":           "Spaced Out",
		"C++_Primer.pdf":                 "C++ Primer",
	} {
		ix, _ := buildLibrary(t, map[string]string{name: pdfBytes})
		if len(ix.Books) != 1 {
			t.Fatalf("%q: got %d books, want 1", name, len(ix.Books))
		}
		if got := ix.Books[0].Title; got != want {
			t.Errorf("%q: Title = %q, want %q", name, got, want)
		}
	}
}

func TestBuildDerivesTopicsAsWholeWords(t *testing.T) {
	ix, _ := buildLibrary(t, shelf)

	for title, want := range map[string][]string{
		"The C Programming Language":    {"c", "programming"},
		"C++ Primer":                    {"c++", "programming"},
		"TypeScript Deep Dive":          {"programming", "typescript"},
		"Concurrency In Go":             {"go"},
		"Advanced R":                    {"r", "statistics"},
		"Introduction To Algorithms":    {"algorithms"},
		"Quantum Mechanics":             {"mechanics", "physics", "quantum mechanics"},
		"Classical Mechanics":           {"mechanics", "physics"},
		"Relativity":                    {"physics", "relativity"},
		"Linear Algebra Done Right":     {"algebra", "linear algebra", "mathematics"},
		"Signal Processing First":       {"engineering", "signal processing"},
		"Loose Notes On Thermodynamics": {"thermodynamics"},
		"Novel":                         {},
	} {
		if got := bookTitled(t, ix, title).Topics; !slices.Equal(got, want) {
			t.Errorf("%s: Topics = %q, want %q", title, got, want)
		}
	}
}

func TestBuildRecognisesLanguagesFromFolders(t *testing.T) {
	ix, _ := buildLibrary(t, map[string]string{
		"Programming/C/kr.pdf":          pdfBytes,
		"Programming/R/tidyverse.pdf":   pdfBytes,
		"Languages/Golang/tour.pdf":     pdfBytes,
		"Languages/C#/in_depth.pdf":     pdfBytes,
		"Web/Node.js/handbook.pdf":      pdfBytes,
		"Mechanics/Physics/statics.pdf": pdfBytes,
	})

	for title, want := range map[string][]string{
		"Kr":        {"c", "programming"},
		"Tidyverse": {"programming", "r"},
		"Tour":      {"go"},
		"In Depth":  {"c#"},
		"Handbook":  {"javascript"},
		"Statics":   {"mechanics", "physics"},
	} {
		if got := bookTitled(t, ix, title).Topics; !slices.Equal(got, want) {
			t.Errorf("%s: Topics = %q, want %q", title, got, want)
		}
	}
}

func TestBuildFollowsSymlinkedRootAndFiles(t *testing.T) {
	target := writeLibrary(t, map[string]string{"Physics/optics.pdf": pdfBytes})
	outside := writeLibrary(t, map[string]string{"elsewhere/Calculus.pdf": pdfBytes})

	if err := os.Symlink(filepath.Join(outside, "elsewhere", "Calculus.pdf"), filepath.Join(target, "Calculus.pdf")); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	link := filepath.Join(t.TempDir(), "books")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}

	ix, err := library.Build(link)
	if err != nil {
		t.Fatal(err)
	}
	if ix.Root != target {
		t.Errorf("Root = %q, want the resolved folder %q", ix.Root, target)
	}
	var titles []string
	for _, b := range ix.Books {
		titles = append(titles, b.Title)
	}
	if want := []string{"Calculus", "Optics"}; !slices.Equal(titles, want) {
		t.Errorf("titles = %q, want %q", titles, want)
	}
}

func TestBuildListsUnreadableFolders(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permissions are not enforced for root")
	}
	root := writeLibrary(t, map[string]string{
		"Open/readable.pdf":   pdfBytes,
		"Locked/hidden.pdf":   pdfBytes,
		"Locked/another.epub": ebookBytes,
	})
	locked := filepath.Join(root, "Locked")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	ix, err := library.Build(root)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(ix.Books) != 1 || ix.Books[0].Title != "Readable" {
		t.Errorf("Books = %+v, want only Readable", ix.Books)
	}
	if want := []string{locked}; !slices.Equal(ix.Skipped, want) {
		t.Errorf("Skipped = %q, want %q", ix.Skipped, want)
	}
}

func TestBuildEmptyFolder(t *testing.T) {
	ix, root := buildLibrary(t, nil)
	if ix.Root != root || len(ix.Books) != 0 {
		t.Errorf("Build(empty) = %+v, want no books under %q", ix, root)
	}
}

func TestBuildRejectsMissingFolderOrFile(t *testing.T) {
	root := writeLibrary(t, map[string]string{"book.pdf": pdfBytes})

	if _, err := library.Build(filepath.Join(root, "missing")); err == nil {
		t.Error("Build(missing folder) succeeded, want an error")
	}
	if _, err := library.Build(filepath.Join(root, "book.pdf")); err == nil {
		t.Error("Build(file) succeeded, want an error")
	}
}
