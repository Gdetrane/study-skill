package library_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mordor-forge/lamplight/internal/library"
)

const (
	pdfBytes   = "%PDF-1.4 fake"
	ebookBytes = "fake book content"
)

// shelf is the fixture Library shared by the search tests: relative path to
// file content.
var shelf = map[string]string{
	"Programming/The_C_Programming_Language.pdf":             pdfBytes,
	"Programming/Python_Programming.pdf":                     pdfBytes,
	"Programming/C++_Primer.epub":                            ebookBytes,
	"Programming/TypeScript_Deep_Dive.pdf":                   pdfBytes,
	"Go/Concurrency_in_Go.pdf":                               pdfBytes,
	"CS/Introduction_to_Algorithms.pdf":                      pdfBytes,
	"Physics/Quantum_Mechanics.pdf":                          pdfBytes,
	"Physics/Classical.Mechanics.PDF":                        pdfBytes,
	"Science/Physics/Advanced/relativity.pdf":                pdfBytes,
	"Math/Linear_Algebra_Done_Right.pdf":                     pdfBytes,
	"Statistics/Advanced_R.pdf":                              pdfBytes,
	"Engineering/Signal_Processing_First.djvu":               ebookBytes,
	"Fiction/Novel.EPUB":                                     ebookBytes,
	"Unknown/10 (335)/10 - Unknown.1007%2f978-1-4614-6227-9": pdfBytes,
	"loose_notes-on.thermodynamics.pdf":                      pdfBytes,
	"too__many___underscores.mobi":                           ebookBytes,
	// Not books.
	"readme.txt":            "not a book",
	"notes.md":              "# notes",
	"Fiction/doc.docx":      "fake docx",
	"Fiction/metadata.opf":  "<package />",
	"Physics/cover.jpg":     "\xff\xd8\xff\xe0",
	"Programming/empty.bin": "",
}

// writeLibrary creates files under a fresh temporary folder and returns the
// folder with symbolic links resolved, as Build reports it.
func writeLibrary(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func buildLibrary(t *testing.T, files map[string]string) (*library.Index, string) {
	t.Helper()
	root := writeLibrary(t, files)
	ix, err := library.Build(root)
	if err != nil {
		t.Fatalf("Build(%s): %v", root, err)
	}
	return ix, root
}

func bookTitled(t *testing.T, ix *library.Index, title string) library.Book {
	t.Helper()
	for _, b := range ix.Books {
		if b.Title == title {
			return b
		}
	}
	var have []string
	for _, b := range ix.Books {
		have = append(have, b.Title)
	}
	t.Fatalf("no book titled %q; have %q", title, have)
	return library.Book{}
}

func titlesOf(results []library.Result) []string {
	titles := []string{}
	for _, r := range results {
		titles = append(titles, r.Title)
	}
	return titles
}
