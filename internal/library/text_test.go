package library_test

import (
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/library"
)

func TestTitleOf(t *testing.T) {
	for path, want := range map[string]string{
		"/books/the_c_programming_language.pdf": "The C Programming Language",
		"/books/CS.101-notes.epub":              "CS 101 Notes",
		"relative/Kernighan-Ritchie":            "Kernighan Ritchie",
	} {
		if got := library.TitleOf(path); got != want {
			t.Errorf("TitleOf(%q) = %q, want %q", path, got, want)
		}
	}
}
