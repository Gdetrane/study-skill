package library_test

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/library"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	ix, _ := buildLibrary(t, shelf)
	path := filepath.Join(t.TempDir(), ".lamplight", "library.json")

	if err := ix.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := library.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(loaded, ix) {
		t.Errorf("Load(Save(ix)) differs:\n got %+v\nwant %+v", loaded, ix)
	}
	if got, want := titlesOf(loaded.Search("C", 0)), titlesOf(ix.Search("C", 0)); !slices.Equal(got, want) {
		t.Errorf("search after Load = %q, want %q", got, want)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("index is not JSON: %v", err)
	}
	if raw["format"] != float64(1) || library.FormatVersion != 1 {
		t.Errorf("format = %v, want 1", raw["format"])
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("index folder holds %d entries, want only the index", len(entries))
	}
}

func TestSaveReplacesExistingIndex(t *testing.T) {
	first, _ := buildLibrary(t, map[string]string{"Calculus.pdf": pdfBytes})
	second, _ := buildLibrary(t, map[string]string{"Topology.pdf": pdfBytes})
	path := filepath.Join(t.TempDir(), "library.json")

	if err := first.Save(path); err != nil {
		t.Fatal(err)
	}
	if err := second.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := library.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Books) != 1 || loaded.Books[0].Title != "Topology" {
		t.Errorf("Books = %+v, want only Topology", loaded.Books)
	}
}

func TestLoadRejectsUnknownFormats(t *testing.T) {
	for name, content := range map[string]string{
		"newer":   `{"format": 2, "root": "/books", "catalog": {"entries": []}}`,
		"missing": `{"root": "/books", "books": []}`,
	} {
		path := filepath.Join(t.TempDir(), "library.json")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := library.Load(path); !errors.Is(err, library.ErrUnsupportedFormat) {
			t.Errorf("%s format: Load error = %v, want ErrUnsupportedFormat", name, err)
		}
	}
}

func TestLoadErrors(t *testing.T) {
	dir := t.TempDir()

	if _, err := library.Load(filepath.Join(dir, "missing.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Load(missing) error = %v, want fs.ErrNotExist", err)
	}

	garbled := filepath.Join(dir, "garbled.json")
	if err := os.WriteFile(garbled, []byte(`{"format": 1, "books": [`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := library.Load(garbled); err == nil {
		t.Error("Load(garbled) succeeded, want an error")
	}
}
