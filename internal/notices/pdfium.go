package notices

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

const (
	// pdfiumModule is the module that holds PDFium built: go-pdfium embeds
	// pdfium.wasm, which Shelf's PDF reader runs.
	pdfiumModule = "github.com/klippa-app/go-pdfium"
	// pdfiumFolder is the folder of NOTICES that is kept by hand.
	pdfiumFolder = "pdfium"
	sourcesFile  = "sources.toml"
	// sourcesFormat is the format of sources.toml this code reads.
	sourcesFormat = 1
)

// Sources is NOTICES/pdfium/sources.toml: the file the folder's notices are
// for, and where each of them came from. It is written by hand, as the
// folder's README.md describes, and checked here.
type Sources struct {
	Format int `toml:"format"`
	// Wasm is the file the notices were written for.
	Wasm struct {
		Module  string `toml:"module"`
		Version string `toml:"version"`
		// File is where the module keeps it.
		File   string `toml:"file"`
		SHA256 string `toml:"sha256"`
	} `toml:"wasm"`
	Parts []Part `toml:"part"`
}

// A Part is one thing the file is made of.
type Part struct {
	Name string `toml:"name"`
	// Version is the part's own name for what was built, where it has one.
	Version string `toml:"version"`
	// Licence is the licence the part is under, in SPDX's words: the one it
	// is taken under where it is offered under a choice.
	Licence string `toml:"licence"`
	// Repository and Revision say where the part's source was read.
	Repository string     `toml:"repository"`
	Revision   string     `toml:"revision"`
	Files      []PartFile `toml:"file"`
}

// A PartFile is one licence or notice of a part.
type PartFile struct {
	// Path is the file in NOTICES/pdfium.
	Path string `toml:"path"`
	// Source is the file in the part's repository, at its revision.
	Source string `toml:"source"`
	SHA256 string `toml:"sha256"`
}

// ReadPDFium reads the hand-kept folder and holds it to the go-pdfium among
// modules. It returns nil when no program links go-pdfium and there is no
// folder, and refuses a folder that is there for a go-pdfium nothing links:
// it would be carried as the notices of a file no program holds.
func ReadPDFium(folder string, modules []Module) (*Sources, error) {
	var linked *Module
	for i := range modules {
		if modules[i].Path == pdfiumModule {
			linked = &modules[i]
		}
	}
	_, err := os.Stat(folder)
	switch {
	case linked == nil && errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case linked == nil:
		return nil, fmt.Errorf("no program links %s any more: remove %s", pdfiumModule, folder)
	}
	sources, err := ReadSources(folder)
	if err != nil {
		return nil, err
	}
	return sources, sources.Check(folder, *linked)
}

// ReadSources reads sources.toml in the folder. A format newer than this
// code reads is refused.
func ReadSources(folder string) (*Sources, error) {
	name := filepath.Join(folder, sourcesFile)
	var sources Sources
	md, err := toml.DecodeFile(name, &sources)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if sources.Format != sourcesFormat {
		return nil, fmt.Errorf("%s has format %d, and internal/notices reads format %d", name, sources.Format, sourcesFormat)
	}
	if unknown := md.Undecoded(); len(unknown) != 0 {
		return nil, fmt.Errorf("%s: unknown key %s", name, unknown[0])
	}
	return &sources, nil
}

// Check holds the folder to the go-pdfium that is linked: the notices were
// written for that version and for the pdfium.wasm it embeds, and the folder
// holds each file the list names, unchanged, and no other but README.md,
// which says how the list was made and which NOTICES/README.md links to.
// Each part's files read as the licence the list says the part is under.
//
// A new go-pdfium brings a pdfium.wasm built from another PDFium, perhaps
// with other parts. Nothing here can tell: the check fails until someone has
// looked, as the folder's README.md describes, and written what they found.
func (s *Sources) Check(folder string, linked Module) error {
	name := filepath.Join(folder, sourcesFile)
	if s.Wasm.Module != linked.Path {
		return fmt.Errorf("%s is about %s, not %s", name, s.Wasm.Module, linked.Path)
	}
	again := fmt.Sprintf("find out what its %s was built from and what is inside it, as %s describes, then update %s",
		path.Base(s.Wasm.File), filepath.Join(folder, "README.md"), name)
	if s.Wasm.Version != linked.Version {
		return fmt.Errorf("go.mod names %s %s, and %s was written for %s: %s", linked.Path, linked.Version, folder, s.Wasm.Version, again)
	}
	// The one file of its own that go-pdfium is known to bring. Another
	// would be another program built elsewhere.
	for _, embedded := range linked.Embedded {
		if embedded != s.Wasm.File {
			return fmt.Errorf("%s %s embeds %s, which %s does not name: %s", linked.Path, linked.Version, embedded, name, again)
		}
	}
	if !filepath.IsLocal(filepath.FromSlash(s.Wasm.File)) {
		return fmt.Errorf("%s: %q is not a file inside the module", name, s.Wasm.File)
	}
	if sum, err := sha256File(filepath.Join(linked.Dir, filepath.FromSlash(s.Wasm.File))); err != nil {
		return fmt.Errorf("%s %s: %w", linked.Path, linked.Version, err)
	} else if sum != s.Wasm.SHA256 {
		return fmt.Errorf("%s in %s %s has the SHA-256 %s, and %s was written for %s: %s",
			s.Wasm.File, linked.Path, linked.Version, sum, folder, s.Wasm.SHA256, again)
	}

	var problems []error
	listed := map[string]bool{"README.md": true, sourcesFile: true}
	if _, err := os.Stat(filepath.Join(folder, "README.md")); err != nil {
		problems = append(problems, fmt.Errorf("%s has no README.md, which says how each part was found: %w", folder, err))
	}
	if len(s.Parts) == 0 {
		problems = append(problems, fmt.Errorf("%s names no part", name))
	}
	for _, part := range s.Parts {
		if part.Name == "" || part.Repository == "" || part.Revision == "" || len(part.Files) == 0 {
			problems = append(problems, fmt.Errorf("%s: the part %q needs a name, a repository, a revision and a file", name, part.Name))
		}
		var texts [][]byte
		for _, file := range part.Files {
			if !filepath.IsLocal(filepath.FromSlash(file.Path)) || file.Source == "" {
				problems = append(problems, fmt.Errorf("%s: %s: the file %q needs a path inside the folder and a source", name, part.Name, file.Path))
				continue
			}
			listed[file.Path] = true
			data, err := os.ReadFile(filepath.Join(folder, filepath.FromSlash(file.Path)))
			if err != nil {
				problems = append(problems, fmt.Errorf("%s names a file that is not there: %w", name, err))
				continue
			}
			texts = append(texts, data)
			if sum := sha256Hex(data); sum != file.SHA256 {
				problems = append(problems, fmt.Errorf("%s is not the file %s names: its SHA-256 is %s, not %s. A notice is copied as it is, never edited",
					filepath.Join(folder, filepath.FromSlash(file.Path)), sourcesFile, sum, file.SHA256))
			}
		}
		if err := borneOut(part.Licence, texts...); err != nil {
			problems = append(problems, fmt.Errorf("%s: the part %q: %w", name, part.Name, err))
		}
	}
	err := filepath.WalkDir(folder, func(file string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, err := filepath.Rel(folder, file)
		if err != nil {
			return err
		}
		if !listed[filepath.ToSlash(rel)] {
			problems = append(problems, fmt.Errorf("%s is in no part of %s: say where it came from, or remove it", file, name))
		}
		return nil
	})
	return errors.Join(append(problems, err)...)
}

func sha256File(name string) (string, error) {
	data, err := os.ReadFile(name)
	if err != nil {
		return "", err
	}
	return sha256Hex(data), nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// index is the part of README.md about what is inside pdfium.wasm.
func (s *Sources) index() string {
	var b strings.Builder
	fmt.Fprintf(&b, `
## Inside PDFium

`+"`study-shelf`"+` reads PDFs with PDFium, which `+"`%s`"+` %s
holds built, as the WebAssembly file `+"`%s`"+`. [pdfium/README.md](pdfium/README.md) says what that
file is made of and how that was found out.

`, s.Wasm.Module, s.Wasm.Version, path.Base(s.Wasm.File))
	for _, part := range s.Parts {
		var links []string
		for _, file := range part.Files {
			links = append(links, fmt.Sprintf("[%s](%s)", path.Base(file.Path), path.Join(pdfiumFolder, file.Path)))
		}
		name := part.Name
		if part.Version != "" {
			name += " " + part.Version
		}
		fmt.Fprintf(&b, "- %s (%s): %s\n", name, part.Licence, strings.Join(links, ", "))
	}
	return b.String()
}
