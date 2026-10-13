package notices_test

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/mordor-forge/lamplight/v2/internal/notices"
)

// TestMain clears the caller's GIT_* variables. A commit hook that runs the
// tests sets GIT_INDEX_FILE, and GIT_DIR from a linked worktree: the go
// command the tests run themselves, to list what the programs link, would
// then work on the repository being committed.
func TestMain(m *testing.M) {
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "GIT_") {
			os.Unsetenv(name)
		}
	}
	os.Exit(m.Run())
}

// The test that runs the go command runs the same under a caller's git
// environment and writes nothing through it.
func TestTheTestsIgnoreTheCallersGitEnvironment(t *testing.T) {
	stray := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestTheNoticesAreOfWhatTheProgramsAreBuiltFrom$", "-test.count=1")
	cmd.Env = append(os.Environ(), "GIT_DIR="+filepath.Join(stray, "repo.git"),
		"GIT_WORK_TREE="+filepath.Join(stray, "tree"), "GIT_INDEX_FILE="+filepath.Join(stray, "index"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("the test under a caller's git environment: %v\n%s", err, out)
	}
	if left, _ := os.ReadDir(stray); len(left) != 0 {
		t.Errorf("git wrote where the caller's environment pointed: %v", left)
	}
}

// repo is the repository's root.
var repo = filepath.Join("..", "..")

// TestTheNoticesAreOfWhatTheProgramsAreBuiltFrom makes the folder again, from
// what the go command says study and study-shelf link for each system a
// release is built for, and compares it with the one in the repository. So a
// dependency cannot be added, changed or dropped without its notice
// following: every archive and package of a release carries the folder as
// it is here.
//
// It also fails when the go-pdfium in go.mod is not the one NOTICES/pdfium
// was written for, and when a file of that folder is not the one its
// sources.toml names: Read checks both.
func TestTheNoticesAreOfWhatTheProgramsAreBuiltFrom(t *testing.T) {
	set, err := notices.Read(repo)
	if err != nil {
		t.Fatalf("what the programs are built from cannot be shipped as it is:\n%v", err)
	}
	if len(set.Modules) == 0 || set.PDFium == nil {
		t.Fatalf("the go command lists %d modules and PDFium's notices are %v, so this test proves nothing", len(set.Modules), set.PDFium)
	}
	if set.GoKept {
		t.Log("this Go keeps no LICENSE in its GOROOT, so Go's licence was not compared: a release of Go, as CI runs, compares it")
	}
	folder := filepath.Join(repo, notices.Folder)

	// Named first, and by what links it: the difference below would only
	// say that a file is missing.
	for _, notice := range set.Modules {
		for _, file := range notice.Files {
			name := filepath.Join(folder, "modules", filepath.FromSlash(notice.Path), filepath.FromSlash(file.Path))
			if _, err := os.Stat(name); err != nil {
				t.Errorf("%s %s, which %s is built from, has no %s in %s: run make notices and commit what it writes",
					notice.Path, notice.Version, notices.LinkedBy(notice.Module), file.Path, notices.Folder)
			}
		}
	}

	fresh := t.TempDir()
	if err := set.Write(fresh); err != nil {
		t.Fatal(err)
	}
	want, got := tree(t, fresh), tree(t, folder)
	for name, data := range want {
		switch have, ok := got[name]; {
		case !ok:
			t.Errorf("%s lacks %s: run make notices and commit what it writes", notices.Folder, name)
		case !bytes.Equal(have, data):
			t.Errorf("%s/%s is not what make notices writes now: run it and commit what it writes", notices.Folder, name)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok && !strings.HasPrefix(name, "pdfium/") {
			t.Errorf("%s holds %s, which nothing a program is built from ships: run make notices and commit what it removes", notices.Folder, name)
		}
	}
}

// tree reads every file under dir, by its path with slashes.
func tree(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	err := filepath.WalkDir(dir, func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, name)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)], err = os.ReadFile(name)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// The notices are of the programs and systems this package lists, so the
// list has to be the release's: a third program, or a build for another
// system, would otherwise be released with the notices of something else.
func TestTheNoticesAreOfEveryBuildOfARelease(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repo, ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Builds []struct {
			ID     string   `yaml:"id"`
			Main   string   `yaml:"main"`
			Env    []string `yaml:"env"`
			Goos   []string `yaml:"goos"`
			Goarch []string `yaml:"goarch"`
			Flags  []string `yaml:"flags"`
			Tags   []string `yaml:"tags"`
		} `yaml:"builds"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	var programs []string
	for _, build := range config.Builds {
		programs = append(programs, build.ID)
		if build.Main != "./cmd/"+build.ID {
			t.Errorf("the build %s is of %s: the notices are of ./cmd/%s", build.ID, build.Main, build.ID)
		}
		var targets []notices.Target
		for _, os := range build.Goos {
			for _, arch := range build.Goarch {
				targets = append(targets, notices.Target{OS: os, Arch: arch})
			}
		}
		if !slices.Equal(targets, notices.Targets) {
			t.Errorf("%s is built for %v, and the notices are of %v", build.ID, targets, notices.Targets)
		}
		// What the go command is asked about: no cgo, and no build tag,
		// either of which changes what is linked.
		if !slices.Contains(build.Env, "CGO_ENABLED=0") {
			t.Errorf("%s is not built with CGO_ENABLED=0, as the notices take it to be", build.ID)
		}
		if len(build.Tags) != 0 || slices.ContainsFunc(build.Flags, func(flag string) bool { return strings.Contains(flag, "tags") }) {
			t.Errorf("%s is built with build tags, which the notices are not of", build.ID)
		}
	}
	if !slices.Equal(programs, notices.Programs) {
		t.Errorf("a release builds %v, and the notices are of %v", programs, notices.Programs)
	}
}

// The folder is of no use in the repository alone: the release's
// configuration has to put it, whole, in every archive and in both kinds of
// package, and state for the packages the licences of what they carry, not
// Lamplight's own alone. CI's snapshot job looks inside what GoReleaser
// builds from this; that job is not required, and this test is.
func TestEveryArchiveAndPackageIsToldToCarryTheNotices(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repo, ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Archives []struct {
			Files []string `yaml:"files"`
		} `yaml:"archives"`
		Nfpms []struct {
			License  string `yaml:"license"`
			Contents []struct {
				Src      string `yaml:"src"`
				Dst      string `yaml:"dst"`
				Type     string `yaml:"type"`
				Packager string `yaml:"packager"`
			} `yaml:"contents"`
		} `yaml:"nfpms"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Archives) == 0 || len(config.Nfpms) == 0 {
		t.Fatalf(".goreleaser.yaml has %d archives and %d packages, so this test proves nothing", len(config.Archives), len(config.Nfpms))
	}
	for _, archive := range config.Archives {
		// Two stars: every file of the folder, however deep.
		if want := notices.Folder + "/**"; !slices.Contains(archive.Files, want) {
			t.Errorf("an archive's files are %v, without %s", archive.Files, want)
		}
	}
	for _, pkg := range config.Nfpms {
		where := map[string]string{}
		for _, content := range pkg.Contents {
			if content.Src == notices.Folder && content.Type == "tree" {
				where[content.Packager] = content.Dst
			}
		}
		if where["deb"] != "/usr/share/doc/lamplight/"+notices.Folder || where["rpm"] != "/usr/share/licenses/lamplight/"+notices.Folder {
			t.Errorf("the packages put the whole folder in %v, want it beside Lamplight's licence in the deb and in the rpm", where)
		}
		for _, kind := range []string{"MIT", "Apache-2.0", "BSD-2-Clause", "BSD-3-Clause"} {
			if !slices.Contains(strings.Split(pkg.License, " AND "), kind) {
				t.Errorf("the packages state the licence %q, which does not name %s", pkg.License, kind)
			}
		}
	}
}

// module makes a module's folder as the module cache would hold it.
func module(t *testing.T, path string, files map[string]string) notices.Module {
	t.Helper()
	dir := t.TempDir()
	for name, text := range files {
		dest := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dest, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return notices.Module{Path: path, Version: "v1.2.3", Dir: dir, Packages: []string{"."},
		LinkedBy: map[string][]notices.Target{"study-shelf": notices.Targets[2:]}}
}

func TestCollectReadsALicenceAndANoticeWhereverAModuleKeepsThem(t *testing.T) {
	mod := module(t, "example.com/many", map[string]string{
		"LICENSE":             "the licence\r\n  as it is  \n",
		"NOTICE":              "the notice",
		"LICENSE-MIT":         "a second licence",
		"COPYING.LESSER":      "a third",
		"licence.txt":         "in lower case",
		"license.go":          "package many",
		"LICENSES/MIT.txt":    "in a folder, which is not read",
		"NOTICES.md":          "not a notice by its name",
		"README.md":           "not one either",
		"inner/used/LICENSE":  "of code taken from elsewhere, and linked",
		"inner/COPYING":       "above a linked package",
		"inner/unused/NOTICE": "beside a package that is not linked",
		"testdata/LICENSE":    "of test data",
	})
	mod.Packages = []string{".", "inner/used"}
	found, err := notices.Collect([]notices.Module{mod})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, file := range found[0].Files {
		names = append(names, file.Path)
	}
	want := []string{"COPYING.LESSER", "LICENSE", "LICENSE-MIT", "NOTICE", "licence.txt", "inner/COPYING", "inner/used/LICENSE"}
	if !slices.Equal(names, want) {
		t.Errorf("Collect read %v, want %v", names, want)
	}
	// Byte for byte: a licence is never tidied.
	if got := string(found[0].Files[1].Data); got != "the licence\r\n  as it is  \n" {
		t.Errorf("the licence was read as %q", got)
	}
}

func TestAModuleWithNoLicenceFileIsRefused(t *testing.T) {
	bare := module(t, "example.com/bare", map[string]string{"README.md": "MIT, it says here", "inner/unused/LICENSE": "not linked"})
	fine := module(t, "example.com/fine", map[string]string{"LICENSE": "a licence"})
	_, err := notices.Collect([]notices.Module{bare, fine})
	if err == nil {
		t.Fatal("a module that ships no licence file was accepted")
	}
	for _, want := range []string{"example.com/bare v1.2.3", "study-shelf on macOS", "no licence file"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal lacks %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "example.com/fine") {
		t.Errorf("the refusal names a module that has its licence: %v", err)
	}
}

// PDFium came inside a module whose own licence says nothing of it. A file
// a module embeds is looked at by a person before it is released.
func TestAModuleThatEmbedsAFileIsRefusedUntilItIsLookedAt(t *testing.T) {
	mod := module(t, "example.com/blob", map[string]string{"LICENSE": "a licence", "engine.wasm": "\x00asm"})
	mod.Embedded = []string{"engine.wasm"}
	_, err := notices.Collect([]notices.Module{mod})
	if err == nil || !strings.Contains(err.Error(), "example.com/blob v1.2.3") || !strings.Contains(err.Error(), "embeds engine.wasm") {
		t.Errorf("a module that embeds a file was not refused by name: %v", err)
	}
}

func TestLinkedBySaysWhichProgramAndWhichSystems(t *testing.T) {
	all := notices.Targets
	for _, tc := range []struct {
		by   map[string][]notices.Target
		want string
	}{
		{map[string][]notices.Target{"study": all}, "study"},
		{map[string][]notices.Target{"study": all, "study-shelf": all}, "study and study-shelf"},
		{map[string][]notices.Target{"study-shelf": all[2:]}, "study-shelf on macOS"},
		{map[string][]notices.Target{"study": all, "study-shelf": all[:2]}, "study and study-shelf on Linux"},
		{map[string][]notices.Target{"study-shelf": all[1:2]}, "study-shelf on linux/arm64"},
	} {
		if got := notices.LinkedBy(notices.Module{LinkedBy: tc.by}); got != tc.want {
			t.Errorf("LinkedBy(%v) = %q, want %q", tc.by, got, tc.want)
		}
	}
}

func TestWriteReplacesWhatItWroteAndKeepsWhatIsKeptByHand(t *testing.T) {
	dir := t.TempDir()
	for name, text := range map[string]string{
		"README.md":                        "an older list",
		"go/LICENSE":                       "an older Go's",
		"modules/example.com/gone/LICENSE": "of a module that is no longer linked",
		"pdfium/README.md":                 "kept by hand",
		"pdfium/zlib/LICENSE":              "kept by hand too",
	} {
		dest := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dest, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	found, err := notices.Collect([]notices.Module{
		module(t, "example.com/Mixed/case/v2", map[string]string{"LICENSE.txt": "a licence", "NOTICE": "a notice"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	set := &notices.Set{Go: []byte("Go's licence"), Modules: found}
	if err := set.Write(dir); err != nil {
		t.Fatal(err)
	}
	got := tree(t, dir)
	index := string(got["README.md"])
	delete(got, "README.md")
	want := map[string]string{
		"go/LICENSE": "Go's licence",
		"modules/example.com/Mixed/case/v2/LICENSE.txt": "a licence",
		"modules/example.com/Mixed/case/v2/NOTICE":      "a notice",
		"pdfium/README.md":                              "kept by hand",
		"pdfium/zlib/LICENSE":                           "kept by hand too",
	}
	if len(got) != len(want) {
		t.Errorf("the folder holds %d files besides its list, want %d", len(got), len(want))
	}
	for name, text := range want {
		if string(got[name]) != text {
			t.Errorf("%s holds %q, want %q", name, got[name], text)
		}
	}
	line := "- `example.com/Mixed/case/v2` v1.2.3, in `study-shelf` on macOS: " +
		"[LICENSE.txt](modules/example.com/Mixed/case/v2/LICENSE.txt), [NOTICE](modules/example.com/Mixed/case/v2/NOTICE)\n"
	if !strings.Contains(index, line) || !strings.Contains(index, "[LICENSE](go/LICENSE)") {
		t.Errorf("the list does not name the module's files and Go's licence:\n%s", index)
	}
	if strings.Contains(index, "PDFium") {
		t.Errorf("the list speaks of PDFium when no program links it:\n%s", index)
	}
}
