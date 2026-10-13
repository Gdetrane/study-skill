// Package notices makes NOTICES, the folder of licences and notices of what
// study and study-shelf are built from, which every archive and package of a
// release carries (docs/release.md).
//
// What a program is built from is what the go command says it links, for
// each system a release is built for. go.mod says more: it also names what
// only tests use, and what only systems no release is built for use. A
// module's notice is its licence files as the module cache holds them, and
// the files that stand beside them, byte for byte: which files those are is
// decided by their names (kindOf). Go's own licence stands for the standard
// library.
//
// Two things are written by a person and held to the rest: under which
// licence each module is, in SPDX's words (licences.go), and what is inside
// the pdfium.wasm that go-pdfium embeds, which arrives built, with no
// licence beside it. NOTICES/pdfium holds those texts, and its sources.toml
// says where each came from and which go-pdfium they were written for
// (pdfium.go).
package notices

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// Folder is the folder's name, at the root of the repository and of every
// archive.
const Folder = "NOTICES"

// A Target is a system a release is built for.
type Target struct{ OS, Arch string }

func (t Target) String() string { return t.OS + "/" + t.Arch }

// Programs are the programs of a release and Targets the systems it is built
// for: the builds of .goreleaser.yaml, which a test holds them to.
var (
	Programs = []string{"study", "study-shelf"}
	Targets  = []Target{{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "amd64"}, {"darwin", "arm64"}}
)

// A Module is a module a program links.
type Module struct {
	Path    string
	Version string
	// Dir is where the module cache holds it.
	Dir string
	// Packages are the folders of its packages that are linked, inside Dir,
	// with "." for the module's own.
	Packages []string
	// Embedded are the files those packages embed, inside Dir: what a
	// module brings that is not Go.
	Embedded []string
	// LinkedBy says which program links it, and for which systems.
	LinkedBy map[string][]Target
	// Licence is the licence it is under, in SPDX's words, and Remark what
	// a reader should know besides: what licences.go says of it.
	Licence string
	Remark  string
}

// A Notice is a module and the licence and notice files it ships.
type Notice struct {
	Module
	Files []File
}

// A File is one licence, or one file that stands beside a licence, as its
// module ships it.
type File struct {
	// Path is where the module keeps it, with slashes.
	Path string
	Data []byte
	// Licence says that it is a licence, and not a file beside one.
	Licence bool
}

// A Set is what the folder holds.
type Set struct {
	// Go is Go's own licence, for the runtime and the standard library.
	Go []byte
	// GoKept says that Go's licence is the one the folder already held: the
	// Go that ran keeps none where it could be read.
	GoKept bool
	// Modules are in the order of their paths.
	Modules []Notice
	// PDFium is what is inside pdfium.wasm, when a program links go-pdfium.
	PDFium *Sources
}

// Read finds out what the programs of the repository at repo are built from
// and reads the notice of each part.
func Read(repo string) (*Set, error) {
	modules, err := Linked(repo)
	if err != nil {
		return nil, err
	}
	if gone := unlinked(modules); len(gone) != 0 {
		return nil, fmt.Errorf("internal/notices/licences.go speaks of %s, which no program links: remove it there", strings.Join(gone, ", "))
	}
	set := &Set{}
	if set.Modules, err = Collect(modules); err != nil {
		return nil, err
	}
	folder := filepath.Join(repo, Folder)
	set.Go, err = goLicense(repo)
	if errors.Is(err, errNoGoLicense) {
		// The folder's own is then all there is to go by. Go's releases
		// keep the file, so CI compares it wherever this computer cannot.
		kept, readErr := os.ReadFile(filepath.Join(folder, "go", "LICENSE"))
		if readErr != nil {
			return nil, err
		}
		set.Go, set.GoKept, err = kept, true, nil
	}
	if err != nil {
		return nil, err
	}
	if err := borneOut(goLicence, set.Go); err != nil {
		return nil, fmt.Errorf("Go's licence: %w", err)
	}
	if set.PDFium, err = ReadPDFium(filepath.Join(folder, pdfiumFolder), modules); err != nil {
		return nil, err
	}
	return set, nil
}

// Linked asks the go command what each program of the repository at repo is
// built from, for each system, as a release builds it: without cgo and
// without build tags, whatever the caller's environment asks for. Each
// module comes with what licences.go says of it.
//
// A module that go.mod replaces is refused: its code is another's, and
// nothing here knows whose notice it then carries.
func Linked(repo string) ([]Module, error) {
	found := map[string]*Module{}
	packages := map[string]map[string]bool{}
	embedded := map[string]map[string]bool{}
	for _, program := range Programs {
		for _, target := range Targets {
			listed, err := goList(repo, program, target)
			if err != nil {
				return nil, err
			}
			seen := map[string]bool{}
			for _, pkg := range listed {
				m := pkg.Module
				if pkg.Standard || m == nil || m.Main {
					continue
				}
				if m.Replace != nil {
					return nil, fmt.Errorf("%s is replaced by %s in go.mod: say in internal/notices whose notice a replaced module carries", m.Path, m.Replace.Path)
				}
				mod := found[m.Path]
				if mod == nil {
					mod = &Module{Path: m.Path, Version: m.Version, Dir: m.Dir, LinkedBy: map[string][]Target{}}
					found[m.Path] = mod
					packages[m.Path], embedded[m.Path] = map[string]bool{}, map[string]bool{}
				}
				if !seen[m.Path] {
					seen[m.Path] = true
					mod.LinkedBy[program] = append(mod.LinkedBy[program], target)
				}
				rel, err := filepath.Rel(m.Dir, pkg.Dir)
				if err != nil || !filepath.IsLocal(rel) {
					return nil, fmt.Errorf("%s is in %s, outside its module's folder %s", pkg.ImportPath, pkg.Dir, m.Dir)
				}
				rel = filepath.ToSlash(rel)
				packages[m.Path][rel] = true
				for _, name := range pkg.EmbedFiles {
					embedded[m.Path][path.Join(rel, name)] = true
				}
			}
		}
	}
	var modules []Module
	for name, mod := range found {
		mod.Packages = sorted(packages[name])
		mod.Embedded = sorted(embedded[name])
		mod.Licence, mod.Remark = licences[name], remarks[name]
		modules = append(modules, *mod)
	}
	sort.Slice(modules, func(i, j int) bool { return modules[i].Path < modules[j].Path })
	return modules, nil
}

// unlinked are the modules licences.go speaks of that are not among modules.
// What is said of a module that is no longer linked is said of nothing, and
// would stay in the table as if it were true.
func unlinked(modules []Module) []string {
	spoken := map[string]bool{}
	for _, table := range []map[string]string{licences, remarks} {
		for name := range table {
			spoken[name] = true
		}
	}
	for _, mod := range modules {
		delete(spoken, mod.Path)
	}
	return sorted(spoken)
}

func sorted(set map[string]bool) []string {
	var out []string
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// listedPackage is what go list says of one package of a build.
type listedPackage struct {
	ImportPath string
	Dir        string
	Standard   bool
	EmbedFiles []string
	Module     *struct {
		Path    string
		Version string
		Dir     string
		Main    bool
		Replace *struct{ Path string }
	}
}

// goList lists the packages of one program for one system. The caller's
// build flags are not a release's, so none is passed on.
func goList(repo, program string, target Target) ([]listedPackage, error) {
	cmd := exec.Command("go", "list", "-deps", "-json=ImportPath,Dir,Standard,EmbedFiles,Module", "./cmd/"+program)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "GOOS="+target.OS, "GOARCH="+target.Arch, "CGO_ENABLED=0", "GOFLAGS=")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list -deps ./cmd/%s for %s: %w\n%s", program, target, err, stderr.String())
	}
	var listed []listedPackage
	for dec := json.NewDecoder(bytes.NewReader(out)); ; {
		var pkg listedPackage
		if err := dec.Decode(&pkg); err == io.EOF {
			return listed, nil
		} else if err != nil {
			return nil, fmt.Errorf("go list -deps ./cmd/%s for %s: %w", program, target, err)
		}
		listed = append(listed, pkg)
	}
}

var errNoGoLicense = errors.New("this Go keeps no LICENSE in its GOROOT")

// goLicense reads the licence of the Go that the go command runs. A release
// of Go keeps it at the top of GOROOT. A distribution's package may keep it
// elsewhere, as Arch's does.
func goLicense(repo string) ([]byte, error) {
	cmd := exec.Command("go", "env", "GOROOT")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go env GOROOT: %w", err)
	}
	root := strings.TrimSpace(string(out))
	data, err := os.ReadFile(filepath.Join(root, "LICENSE"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w (%s), and %s holds none to keep: run make notices with a release of Go, which GOTOOLCHAIN can name", errNoGoLicense, root, Folder)
	}
	return data, err
}

// Collect reads the licence files of each module, and the files that stand
// beside them. Each of these is an error, which names the module and what
// links it, since none can be released until a person has looked:
//
//   - a module that ships no licence file: a NOTICE or a list of authors
//     alone is no licence;
//   - a module of which licences.go does not say under which licence it is,
//     or says one that its files do not read as;
//   - a module that embeds a file, other than go-pdfium, whose pdfium.wasm
//     NOTICES/pdfium is for. A file a module embeds is not Go: it may be a
//     program built elsewhere, from parts with licences of their own that
//     no licence file of the module names, which is how PDFium arrived.
func Collect(modules []Module) ([]Notice, error) {
	var notices []Notice
	var missing []error
	for _, mod := range modules {
		files, err := noticeFiles(mod)
		if err != nil {
			return nil, err
		}
		var texts [][]byte
		for _, file := range files {
			if file.Licence {
				texts = append(texts, file.Data)
			}
		}
		if len(texts) == 0 {
			missing = append(missing, fmt.Errorf("%s %s, in %s, ships no licence file that internal/notices knows the name of (looked in %s)",
				mod.Path, mod.Version, LinkedBy(mod), mod.Dir))
			continue
		}
		if mod.Licence == "" {
			missing = append(missing, fmt.Errorf("%s %s, in %s: read its licence files and say under which licence it is, in SPDX's words, in licences in internal/notices/licences.go",
				mod.Path, mod.Version, LinkedBy(mod)))
			continue
		}
		if err := borneOut(mod.Licence, texts...); err != nil {
			missing = append(missing, fmt.Errorf("%s %s, in %s: %w (licences in internal/notices/licences.go)", mod.Path, mod.Version, LinkedBy(mod), err))
			continue
		}
		if unknown := unknownEmbeds(mod); len(unknown) != 0 {
			missing = append(missing, fmt.Errorf("%s %s, in %s, embeds %s: find out what that is made of, then name it in dataFiles in internal/notices if the module's own licence covers it, or keep its notices by hand as %s/%s is kept",
				mod.Path, mod.Version, LinkedBy(mod), strings.Join(unknown, ", "), Folder, pdfiumFolder))
			continue
		}
		notices = append(notices, Notice{Module: mod, Files: files})
	}
	return notices, errors.Join(missing...)
}

// dataFiles are the files a linked module embeds that someone looked at and
// found to be data under the module's own licence, by the module's path.
var dataFiles = map[string][]string{}

// unknownEmbeds are the files a module embeds that nothing accounts for.
func unknownEmbeds(mod Module) []string {
	if mod.Path == pdfiumModule {
		return nil
	}
	var unknown []string
	for _, name := range mod.Embedded {
		if !slices.Contains(dataFiles[mod.Path], name) {
			unknown = append(unknown, name)
		}
	}
	return unknown
}

// noticeFiles reads a module's licence files and the files beside them:
// those at its root, and those in the folder of a linked package or in a
// folder above it, which is where a module keeps the licence of code it took
// from elsewhere. One beside a package that is not linked, or beside test
// data, is not read. In each of those folders, a folder named LICENSES is
// read too: some modules keep one text for each licence there.
func noticeFiles(mod Module) ([]File, error) {
	folders := map[string]bool{".": true}
	for _, pkg := range mod.Packages {
		for dir := path.Clean(pkg); dir != "."; dir = path.Dir(dir) {
			folders[dir] = true
		}
	}
	var files []File
	read := func(dir string, kind func(name string) fileKind) ([]fs.DirEntry, error) {
		entries, err := os.ReadDir(filepath.Join(mod.Dir, filepath.FromSlash(dir)))
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", mod.Path, mod.Version, err)
		}
		for _, entry := range entries {
			k := kind(entry.Name())
			if !entry.Type().IsRegular() || k == neither {
				continue
			}
			name := path.Join(dir, entry.Name())
			data, err := os.ReadFile(filepath.Join(mod.Dir, filepath.FromSlash(name)))
			if err != nil {
				return nil, fmt.Errorf("%s %s: %w", mod.Path, mod.Version, err)
			}
			files = append(files, File{Path: name, Data: data, Licence: k == licence})
		}
		return entries, nil
	}
	for _, dir := range sorted(folders) {
		entries, err := read(dir, kindOf)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() && isLicencesFolder(entry.Name()) {
				if _, err := read(path.Join(dir, entry.Name()), kindInLicencesFolder); err != nil {
					return nil, err
				}
			}
		}
	}
	return files, nil
}

// A fileKind is what a module's file is, as far as its name tells.
type fileKind int

const (
	neither fileKind = iota
	// licence is a text of a licence.
	licence
	// beside is a file a licence stands with and is no licence itself.
	beside
)

// kindOf decides from a file's name alone whether a module ships it as a
// licence, or as a file that stands beside one. The rule, in any case of
// the letters:
//
//   - A licence is a file whose name holds LICENSE, LICENCE or COPYING,
//     wherever in the name: LICENSE, LICENSE.txt, LICENSE-MIT, MIT-LICENSE,
//     icu-license.html, COPYING.LESSER, UNLICENSE. So is one named COPYRIGHT,
//     with or without an extension.
//   - Beside a licence stand a file whose name holds NOTICE, as NOTICE.txt
//     and THIRD_PARTY_NOTICES.md do, which Apache-2.0 has carried along, and
//     one named AUTHORS or CONTRIBUTORS, with or without an extension: many
//     licences name "the authors" as the holders of the copyright, and these
//     files say who they are.
//   - Nothing else is either. PATENTS, which the Go project's modules ship,
//     grants rights to whoever receives the code and asks nothing of whoever
//     passes it on, so it is not carried. A Go file and a hidden file are
//     never one, whatever they are called.
//
// The rule errs towards carrying a file: one that is carried and need not
// be costs a line in the list, and one that is left out is a licence broken.
func kindOf(name string) fileKind {
	if strings.HasPrefix(name, ".") || strings.EqualFold(path.Ext(name), ".go") {
		return neither
	}
	upper := strings.ToUpper(name)
	stem, _, _ := strings.Cut(upper, ".")
	switch {
	case strings.Contains(upper, "LICENSE"), strings.Contains(upper, "LICENCE"), strings.Contains(upper, "COPYING"), stem == "COPYRIGHT":
		return licence
	case strings.Contains(upper, "NOTICE"), stem == "AUTHORS", stem == "CONTRIBUTORS":
		return beside
	}
	return neither
}

// isLicencesFolder reports whether a folder is one that holds a text for
// each licence, as the REUSE layout has it: LICENSES, in any case and
// spelling.
func isLicencesFolder(name string) bool {
	return strings.EqualFold(name, "LICENSES") || strings.EqualFold(name, "LICENCES")
}

// kindInLicencesFolder is kindOf for a file of such a folder, where every
// file is a licence whatever its name: Apache-2.0.txt, MIT.txt.
func kindInLicencesFolder(name string) fileKind {
	if strings.HasPrefix(name, ".") || strings.EqualFold(path.Ext(name), ".go") {
		return neither
	}
	return licence
}

// LinkedBy says in words what links a module: "study", "study and
// study-shelf", "study-shelf on macOS".
func LinkedBy(mod Module) string {
	return linkedBy(mod, func(program string) string { return program })
}

// linkedBy is LinkedBy with each program's name written by name.
func linkedBy(mod Module, name func(program string) string) string {
	var parts []string
	for _, program := range Programs {
		if targets := mod.LinkedBy[program]; len(targets) != 0 {
			parts = append(parts, name(program)+where(targets))
		}
	}
	return strings.Join(parts, " and ")
}

// where says which of the systems a list of targets is: nothing for all of
// them, " on macOS" for every build for macOS, and the targets' own names
// for anything else.
func where(targets []Target) string {
	if len(targets) == len(Targets) {
		return ""
	}
	for _, system := range [][2]string{{"linux", "Linux"}, {"darwin", "macOS"}} {
		var all []Target
		for _, t := range Targets {
			if t.OS == system[0] {
				all = append(all, t)
			}
		}
		if slices.Equal(targets, all) {
			return " on " + system[1]
		}
	}
	var names []string
	for _, t := range targets {
		names = append(names, t.String())
	}
	return " on " + strings.Join(names, ", ")
}

// Write writes the set into the folder at dir: README.md, which lists every
// part, Go's licence in go, and each module's files in modules, under the
// module's path. It replaces what it wrote before and leaves the pdfium
// folder, which is kept by hand, as it is.
func (s *Set) Write(dir string) error {
	for _, name := range []string{"README.md", "go", "modules"} {
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	files := map[string][]byte{
		"README.md":  s.index(),
		"go/LICENSE": s.Go,
	}
	for _, notice := range s.Modules {
		for _, file := range notice.Files {
			files[path.Join("modules", notice.Path, file.Path)] = file.Data
		}
	}
	for name, data := range files {
		dest := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dest, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// index is README.md: what the folder is, and every part with its files.
func (s *Set) index() []byte {
	var b strings.Builder
	b.WriteString(`# Notices

Lamplight's two programs, ` + "`study`" + ` and ` + "`study-shelf`" + `, are built from what is listed here.
This folder holds the licence of each part as its authors publish it, with the files that
stand beside it: its ` + "`NOTICE`" + `, and its list of authors where it has one. Lamplight's own
licence is the ` + "`LICENSE`" + ` file beside this folder.

` + "`make notices`" + ` writes this file and the folders ` + "`go`" + ` and ` + "`modules`" + ` from the Go module
cache: do not edit them. The folder ` + "`pdfium`" + ` is kept by hand, as its ` + "`README.md`" + ` says. A
test fails when a program is built from a module that has no notice here.

Each part is listed with the licence it is under, in the words of the SPDX licence list,
as a person read it in the part's files. Where a part is offered under a choice of
licences, the one named is the one it is taken under.

## Go

Both programs hold Go's runtime and standard library, ` + goLicence + `: [LICENSE](go/LICENSE).

## Go modules

What the go command links into each program, for Linux and macOS, on amd64 and arm64.

`)
	for _, notice := range s.Modules {
		var links []string
		for _, file := range notice.Files {
			links = append(links, fmt.Sprintf("[%s](%s)", file.Path, path.Join("modules", notice.Path, file.Path)))
		}
		in := linkedBy(notice.Module, func(program string) string { return "`" + program + "`" })
		fmt.Fprintf(&b, "- `%s` %s (%s), in %s: %s\n", notice.Path, notice.Version, notice.Licence, in, strings.Join(links, ", "))
		if notice.Remark != "" {
			fmt.Fprintf(&b, "\n  %s\n\n", notice.Remark)
		}
	}
	if s.PDFium != nil {
		b.WriteString(s.PDFium.index())
	}
	return []byte(b.String())
}
