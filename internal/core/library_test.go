package core_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/core"
)

// bookFolder writes a small Library: name -> content.
func bookFolder(t *testing.T, books map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range books {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestLibraryBuildThenSearch(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	books := bookFolder(t, map[string]string{
		"Programming/The_C_Programming_Language.pdf": "%PDF-1.4",
		"Physics/Quantum.Mechanics.PDF":              "%PDF-1.4",
		"Physics/cover.jpg":                          "\xff\xd8",
	})
	// A relative folder resolves against where the command was started.
	c := testCore(t, home, filepath.Dir(books))
	summary, err := c.BuildLibrary(ctx, filepath.Base(books))
	if err != nil {
		t.Fatalf("BuildLibrary: %v", err)
	}
	if summary.Books != 2 || summary.Index != filepath.Join(home, ".lamplight", "library.json") {
		t.Fatalf("summary = %+v", summary)
	}

	results, err := testCore(t, home, "").SearchLibrary(ctx, "C", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Title != "The C Programming Language" {
		t.Fatalf("search C = %+v", results)
	}
	if want := filepath.Join(books, "Programming", "The_C_Programming_Language.pdf"); results[0].Path != want {
		t.Errorf("path = %s, want the absolute path %s", results[0].Path, want)
	}

	none, err := testCore(t, home, "").SearchLibrary(ctx, "organic chemistry", 0)
	if err != nil || len(none) != 0 {
		t.Errorf("a search without matches = %v, %v; want an empty result and no error", none, err)
	}
}

func TestLibraryErrors(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	c := testCore(t, home, "")

	if _, err := c.SearchLibrary(ctx, "physics", 0); core.CodeOf(err) != core.CodeNotFound {
		t.Errorf("search before any build: %v, want not_found", err)
	}
	if _, err := c.SearchLibrary(ctx, "  ", 0); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Errorf("empty query: %v, want invalid_argument", err)
	}
	if _, err := c.SearchLibrary(ctx, "physics", -1); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Errorf("negative limit: %v, want invalid_argument", err)
	}
	if _, err := c.BuildLibrary(ctx, filepath.Join(home, "missing")); core.CodeOf(err) != core.CodeNotFound {
		t.Errorf("missing folder: %v, want not_found", err)
	}

	if err := os.MkdirAll(filepath.Join(home, ".lamplight"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".lamplight", "library.json"), []byte(`{"format": 99, "books": []}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SearchLibrary(ctx, "physics", 0); core.CodeOf(err) != core.CodeNewerFormat {
		t.Errorf("newer index format: %v, want newer_format", err)
	}
}
