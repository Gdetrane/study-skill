// Package library builds and searches the index behind the Library: the
// learner's whole collection of documents across all Topics.
//
// The interface is four calls: Build walks a folder into an Index, Save and
// Load store it, and Search ranks its books against a query.
//
// # Building
//
// Build walks the folder once (ADR-0003). A regular file is a book when its
// lowercased extension is a supported format (.pdf, or one of the ebook
// formats .azw, .azw3, .cbz, .djvu, .epub, .fb2 and .mobi), or otherwise when
// it starts with the PDF signature "%PDF-". Symbolic links to files are
// followed; symbolic links to folders are not. Entries that cannot be read are
// left out and listed in Index.Skipped.
//
// A book's title comes from its file name: the name without its extension for
// a supported format, or the whole name for a signature match, since such
// names often encode a DOI. Dots, underscores and hyphens become spaces, runs
// of spaces collapse, and the first letter of each word is capitalised, so
// "Classical.Mechanics.PDF" becomes "Classical Mechanics" while "CS" and "2nd"
// stay as they are (v1's Python str.title turned them into "Cs" and "2Nd").
// The category is the first folder below the root, cleaned the same way, or
// "Uncategorized" for a file directly in the root.
//
// # Text normalization
//
// One normalizer turns text into tokens, and it is used for everything: file
// and folder names, titles, topic terms and queries. Text is lowercased and
// split into runs of letters and digits. A run keeps trailing "+" or "#" signs
// that end the word (c++, c#) and an inner "&" between letters or digits
// (k&r). A few English
// stop words (the, of, and, ...) are dropped. Matching always compares whole
// tokens, so "c" never matches "mechanics" and "go" never matches
// "algorithms".
//
// # Topics
//
// Books are tagged with topics from a fixed table. A topic applies when its
// name, or one of its extra terms, appears as a run of whole tokens in the
// title or within a single folder name. A term may end in a stem marked "*"
// that matches the start of a token ("mechanic*" matches "mechanics" and
// "mechanical"). Stems are at least four letters long, so short names such as
// C, R and Go only ever match a whole token (which also means a lone initial,
// as in "Robert C Seacord", counts as the language).
//
// # Searching
//
// Search normalizes the query with the same normalizer, derives the query's
// own topics with the same table, and scores each book:
//
//   - 10 when the query's tokens equal the title's tokens (an exact title);
//   - 5 for each topic the book shares with the query;
//   - 3 for each distinct query token that is a word of the title;
//   - 1 for each distinct query token that is a word of one of the book's
//     folder names (the category included).
//
// Books that score zero are left out. Results are ordered by score, highest
// first, then by title, then by path, so the same query over the same index
// always gives the same order.
//
// # The index file
//
// Save writes the index as JSON with a top-level "format" number. The file is
// derived data: it can be deleted and rebuilt with Build at any time. Load
// refuses a format it does not know with ErrUnsupportedFormat.
package library

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Book is one document in the Library.
type Book struct {
	// Title is derived from the file name.
	Title string `json:"title"`
	// Path is the absolute path of the file.
	Path string `json:"path"`
	// Category is the title-cased first folder below the Library root, or
	// "Uncategorized" for a file directly in the root.
	Category string `json:"category"`
	// Format is the lowercased extension without its dot, such as "pdf" or
	// "epub"; a file recognised by the PDF signature has format "pdf".
	Format string `json:"format"`
	// Size is the file size in bytes.
	Size int64 `json:"size_bytes"`
	// Topics are the topics derived from the title and folder names, sorted.
	Topics []string `json:"topics"`
}

// Index is the searchable catalog of one Library folder. It is derived data
// that Build can recreate at any time. An Index is safe for concurrent
// searches as long as nothing modifies it.
type Index struct {
	// Root is the absolute path of the folder that was scanned, with
	// symbolic links resolved.
	Root string
	// Books are sorted by path.
	Books []Book
	// Skipped lists the absolute paths of entries Build could not read.
	Skipped []string
}

const uncategorized = "Uncategorized"

// bookFormats are the supported extensions: PDF, which knowledge services
// accept directly, and the ebook formats v1 could convert to PDF.
var bookFormats = map[string]bool{
	".pdf":  true,
	".azw":  true,
	".azw3": true,
	".cbz":  true,
	".djvu": true,
	".epub": true,
	".fb2":  true,
	".mobi": true,
}

var pdfSignature = []byte("%PDF-")

// Build walks the folder at root and returns an index of every book in it.
// It fails only when root itself cannot be used; entries below it that cannot
// be read are listed in Index.Skipped.
func Build(root string) (*Index, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("library: resolving %q: %w", root, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("library: opening Library folder: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("library: opening Library folder: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("library: %s is not a folder", resolved)
	}

	ix := &Index{Root: resolved, Books: []Book{}}
	err = filepath.WalkDir(resolved, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == resolved {
				return err
			}
			ix.Skipped = append(ix.Skipped, path)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		book, ok, err := readBook(resolved, path, d)
		if err != nil {
			ix.Skipped = append(ix.Skipped, path)
			return nil
		}
		if ok {
			ix.Books = append(ix.Books, book)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("library: walking %s: %w", resolved, err)
	}

	slices.SortFunc(ix.Books, func(a, b Book) int { return strings.Compare(a.Path, b.Path) })
	slices.Sort(ix.Skipped)
	ix.Skipped = slices.Compact(ix.Skipped)
	return ix, nil
}

// readBook reports whether the entry at path is a book and, if so, describes
// it. root must be the absolute Library folder containing path.
func readBook(root, path string, d fs.DirEntry) (Book, bool, error) {
	info, err := d.Info()
	if err != nil {
		return Book{}, false, err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		if info, err = os.Stat(path); err != nil {
			return Book{}, false, err
		}
	}
	if !info.Mode().IsRegular() {
		return Book{}, false, nil
	}

	name := filepath.Base(path)
	ext := filepath.Ext(name)
	format, titleSource := "", ""
	if bookFormats[strings.ToLower(ext)] {
		format = strings.ToLower(ext[1:])
		titleSource = strings.TrimSuffix(name, ext)
	} else {
		isPDF, err := hasPDFSignature(path)
		if err != nil {
			return Book{}, false, err
		}
		if !isPDF {
			return Book{}, false, nil
		}
		format, titleSource = "pdf", name
	}
	if titleSource == "" {
		titleSource = name
	}

	rel, err := filepath.Rel(root, path)
	if err != nil {
		return Book{}, false, err
	}
	folders := folderNames(rel)
	category := uncategorized
	if len(folders) > 0 {
		category = cleanTitle(folders[0])
	}
	title := cleanTitle(titleSource)

	return Book{
		Title:    title,
		Path:     path,
		Category: category,
		Format:   format,
		Size:     info.Size(),
		Topics:   topicsOf(append(folders, title)...),
	}, true, nil
}

// folderNames returns the folders of a path relative to the Library root,
// outermost first, or nil for a file directly in the root.
func folderNames(rel string) []string {
	dir := filepath.Dir(rel)
	if dir == "." || dir == string(filepath.Separator) {
		return nil
	}
	return strings.Split(dir, string(filepath.Separator))
}

func hasPDFSignature(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	head := make([]byte, len(pdfSignature))
	if _, err := io.ReadFull(f, head); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return false, nil
		}
		return false, err
	}
	return bytes.Equal(head, pdfSignature), nil
}
