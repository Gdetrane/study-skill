package library

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// FormatVersion is the format number Save writes and the only one Load reads.
const FormatVersion = 1

// ErrUnsupportedFormat is returned by Load for an index file without a format
// number or with one this version does not understand. The index is derived
// data, so rebuilding it is always a valid recovery.
var ErrUnsupportedFormat = errors.New("unsupported Library index format")

type indexFile struct {
	Format  int      `json:"format"`
	Root    string   `json:"root"`
	Books   []Book   `json:"books"`
	Skipped []string `json:"skipped,omitempty"`
}

// Save writes the index to path as JSON, creating parent folders as needed.
// The file is replaced atomically, so a reader never sees a partial index.
func (ix *Index) Save(path string) error {
	books := ix.Books
	if books == nil {
		books = []Book{}
	}
	data, err := json.MarshalIndent(indexFile{
		Format:  FormatVersion,
		Root:    ix.Root,
		Books:   books,
		Skipped: ix.Skipped,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("library: encoding index: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("library: creating index folder: %w", err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("library: writing index: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op once renamed

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("library: writing index: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return fmt.Errorf("library: writing index: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("library: writing index: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("library: writing index: %w", err)
	}
	return nil
}

// Load reads an index written by Save. A missing file gives an error that
// matches fs.ErrNotExist; an unknown format gives ErrUnsupportedFormat.
func Load(path string) (*Index, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("library: reading index: %w", err)
	}

	var header struct {
		Format int `json:"format"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return nil, fmt.Errorf("library: parsing index %s: %w", path, err)
	}
	switch {
	case header.Format == 0:
		return nil, fmt.Errorf("library: index %s has no format number: %w", path, ErrUnsupportedFormat)
	case header.Format != FormatVersion:
		return nil, fmt.Errorf("library: index %s has format %d, but this version reads format %d: %w",
			path, header.Format, FormatVersion, ErrUnsupportedFormat)
	}

	var file indexFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("library: parsing index %s: %w", path, err)
	}
	if file.Books == nil {
		file.Books = []Book{}
	}
	return &Index{Root: file.Root, Books: file.Books, Skipped: file.Skipped}, nil
}
