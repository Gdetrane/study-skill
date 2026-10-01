package core

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/mordor-forge/lamplight/v2/internal/library"
)

// Search limits for the Library.
const (
	DefaultSearchLimit = 10
	MaxSearchLimit     = 100
)

// libraryIndex is where the Library index lives in the Study home. It is
// derived data: deleting it is always safe, and study library build rebuilds
// it.
var libraryIndex = filepath.Join(localDir, "library.json")

// LibrarySummary reports the result of indexing the Library.
type LibrarySummary struct {
	Root    string   `json:"root"`
	Books   int      `json:"books"`
	Skipped []string `json:"skipped"`
	Index   string   `json:"index"`
}

// BuildLibrary indexes the books in folder and saves the index in the Study
// home, replacing any previous index. A relative folder is resolved against
// the folder the learner or agent started in.
func (c *Core) BuildLibrary(ctx context.Context, folder string) (LibrarySummary, error) {
	if strings.TrimSpace(folder) == "" {
		return LibrarySummary{}, invalidf("name the folder that holds your books")
	}
	if !filepath.IsAbs(folder) {
		folder = filepath.Join(c.dir, folder)
	}
	info, err := os.Stat(folder)
	if errors.Is(err, fs.ErrNotExist) {
		return LibrarySummary{}, &Error{Code: CodeNotFound, Message: folder + " does not exist"}
	}
	if err != nil {
		return LibrarySummary{}, internalError("reading "+folder, err)
	}
	if !info.IsDir() {
		return LibrarySummary{}, invalidf("%s is not a folder", folder)
	}
	if err := ctx.Err(); err != nil {
		return LibrarySummary{}, err
	}
	ix, err := library.Build(folder)
	if err != nil {
		return LibrarySummary{}, internalError("indexing "+folder, err)
	}
	if err := os.MkdirAll(filepath.Join(c.home, localDir), 0o755); err != nil {
		return LibrarySummary{}, internalError("creating "+localDir, err)
	}
	path := filepath.Join(c.home, libraryIndex)
	if err := ix.Save(path); err != nil {
		return LibrarySummary{}, internalError("saving the Library index", err)
	}
	skipped := ix.Skipped
	if skipped == nil {
		skipped = []string{}
	}
	return LibrarySummary{Root: ix.Root, Books: len(ix.Books), Skipped: skipped, Index: path}, nil
}

// SearchLibrary ranks the books in the Library index against query. limit is
// clamped to MaxSearchLimit; zero means DefaultSearchLimit. An empty result is
// not an error.
func (c *Core) SearchLibrary(ctx context.Context, query string, limit int) ([]library.Result, error) {
	if strings.TrimSpace(query) == "" {
		return nil, invalidf("search for something, for example \"linear algebra\"")
	}
	switch {
	case limit < 0:
		return nil, invalidf("the limit must be positive")
	case limit == 0:
		limit = DefaultSearchLimit
	case limit > MaxSearchLimit:
		limit = MaxSearchLimit
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := filepath.Join(c.home, libraryIndex)
	ix, err := library.Load(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, &Error{Code: CodeNotFound,
			Message: "the Library has no index yet: run study library build <folder with your books>"}
	case errors.Is(err, library.ErrUnsupportedFormat):
		return nil, &Error{Code: CodeNewerFormat,
			Message: path + " has a format this version of study cannot read: rebuild it with study library build, or upgrade study"}
	case err != nil:
		return nil, internalError("reading the Library index", err)
	}
	return ix.Search(query, limit), nil
}
