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
	}
}

// The packages state as their licence the licences of everything they
// carry: every kind that the list in NOTICES names, and no other. A
// dependency that brings a kind of licence the packages do not name fails
// here, and so does a kind the packages name that nothing is under any more.
func TestThePackagesStateEveryKindOfLicenceInTheNotices(t *testing.T) {
	set, err := notices.Read(repo)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(repo, ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Nfpms []struct {
			License string `yaml:"license"`
		} `yaml:"nfpms"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	want := set.Licences()
	if len(config.Nfpms) == 0 || len(want) < 5 {
		t.Fatalf(".goreleaser.yaml has %d packages and the notices are under %v, so this test proves nothing", len(config.Nfpms), want)
	}
	for _, pkg := range config.Nfpms {
		stated := strings.Split(pkg.License, " AND ")
		for _, kind := range want {
			if !slices.Contains(stated, kind) {
				t.Errorf("a part in %s is under %s, which the packages' licence does not name: add it to license in .goreleaser.yaml\n%s",
					notices.Folder, kind, pkg.License)
			}
		}
		for _, kind := range stated {
			if !slices.Contains(want, kind) {
				t.Errorf("the packages' licence names %s, which nothing in %s is under: remove it from license in .goreleaser.yaml", kind, notices.Folder)
			}
		}
	}
}

// aRepository makes a repository that holds the two programs of a release
// and the files given, and nothing of Lamplight's.
func aRepository(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, text := range map[string]string{
		"go.mod":                  "module example.com/app\n\ngo 1.26\n",
		"cmd/study/main.go":       "package main\n\nfunc main() {}\n",
		"cmd/study-shelf/main.go": "package main\n\nfunc main() {}\n",
	} {
		if _, given := files[name]; !given {
			write(t, dir, name, text)
		}
	}
	for name, text := range files {
		write(t, dir, name, text)
	}
	return dir
}

// What is linked is asked of the go command as a release builds: the
// caller's build flags and cgo are not a release's, and either would change
// the answer. Here each would add a file that imports a module nobody has,
// which the go command refuses.
func TestWhatIsLinkedIsAskedAsAReleaseBuilds(t *testing.T) {
	absent := "package main\n\nimport _ \"example.com/absent\"\n"
	repo := aRepository(t, map[string]string{
		"cmd/study/tagged.go":       "//go:build extra\n\n" + absent,
		"cmd/study-shelf/with_c.go": "//go:build cgo\n\n" + absent,
	})
	t.Setenv("GOFLAGS", "-tags=extra")
	t.Setenv("CGO_ENABLED", "1")
	modules, err := notices.Linked(repo)
	if err != nil {
		t.Fatalf("the caller's GOFLAGS or CGO_ENABLED reached the go command:\n%v", err)
	}
	if len(modules) != 0 {
		t.Errorf("two programs that import nothing link %v", modules)
	}

	// The proof that the two files are what the settings keep out.
	for name, file := range map[string]string{"build tag": "cmd/study/tagged.go", "cgo": "cmd/study-shelf/with_c.go"} {
		plain := aRepository(t, map[string]string{file: absent})
		if _, err := notices.Linked(plain); err == nil || !strings.Contains(err.Error(), "example.com/absent") {
			t.Errorf("the file behind the %s, built in, should have failed the listing: %v", name, err)
		}
	}
}

// A module that go.mod replaces is built from other code than its path
// says, and may be under another licence: a person has to say whose notice
// it carries.
func TestAReplacedModuleIsRefused(t *testing.T) {
	repo := aRepository(t, map[string]string{
		"go.mod":            "module example.com/app\n\ngo 1.26\n\nrequire example.com/dep v0.0.0\n\nreplace example.com/dep => ./dep\n",
		"dep/go.mod":        "module example.com/dep\n\ngo 1.26\n",
		"dep/dep.go":        "package dep\n",
		"dep/LICENSE":       mit,
		"cmd/study/main.go": "package main\n\nimport _ \"example.com/dep\"\n\nfunc main() {}\n",
	})
	_, err := notices.Linked(repo)
	if err == nil || !strings.Contains(err.Error(), "example.com/dep is replaced") {
		t.Errorf("a replaced module was not refused by name: %v", err)
	}
}

// What licences.go says of a module that is no longer linked is said of
// nothing. Here no module of Lamplight's is linked at all.
func TestWhatIsWrittenDownOfAModuleNothingLinksIsRefused(t *testing.T) {
	_, err := notices.Read(aRepository(t, nil))
	if err == nil || !strings.Contains(err.Error(), "which no program links") || !strings.Contains(err.Error(), "github.com/spf13/cobra") {
		t.Errorf("a table that speaks of modules no program links was not refused: %v", err)
	}
}

// mit is what any text of the MIT licence says, as far as this package
// checks, with the white space of a real file.
const mit = "Permission is hereby granted, free\r\n  of charge, to whoever reads this  \n"

// module makes a module's folder as the module cache would hold it, for a
// module that licences.go says is under MIT.
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
	return notices.Module{Path: path, Version: "v1.2.3", Dir: dir, Packages: []string{"."}, Licence: "MIT",
		LinkedBy: map[string][]notices.Target{"study-shelf": notices.Targets[2:]}}
}

// The rule is about names alone: kindOf in the package says it in words.
func TestCollectReadsALicenceAndWhatStandsBesideItWhateverTheyAreCalled(t *testing.T) {
	mod := module(t, "example.com/many", map[string]string{
		// Licences.
		"LICENSE":            mit,
		"LICENSE-MIT":        "a second licence",
		"COPYING.LESSER":     "a third",
		"licence.txt":        "in lower case",
		"MIT-LICENSE":        "with the word last",
		"icu-license.html":   "<html>of code it took from elsewhere</html>",
		"UNLICENSE":          "holds the word",
		"COPYRIGHT":          "as musl names its own",
		"LICENSES/MIT.txt":   "one text for each licence, in a folder",
		"LICENSES/README.md": "whatever its name, in that folder",
		// What stands beside one.
		"NOTICE":                 "the notice",
		"THIRD_PARTY_NOTICES.md": "of others",
		"AUTHORS":                "who the authors are",
		"CONTRIBUTORS.md":        "and the contributors",
		// Neither.
		"license.go":          "package many",
		"licenses_test.go":    "package many",
		".licenserc.yaml":     "a tool's settings",
		"PATENTS":             "a grant, which asks nothing of who passes the code on",
		"README.md":           "MIT, it says here",
		"COPYRIGHT-HOLDERS":   "not a name the rule knows",
		"docs/LICENSE":        "in a folder no linked package is in",
		"testdata/LICENSE":    "of test data",
		"inner/unused/NOTICE": "beside a package that is not linked",
		// In and above the folder of a linked package.
		"inner/used/LICENSE": "of code taken from elsewhere, and linked",
		"inner/COPYING":      "above a linked package",
		"inner/AUTHORS":      "of that code",
	})
	mod.Packages = []string{".", "inner/used"}
	found, err := notices.Collect([]notices.Module{mod})
	if err != nil {
		t.Fatal(err)
	}
	var licences, beside []string
	for _, file := range found[0].Files {
		if file.Licence {
			licences = append(licences, file.Path)
		} else {
			beside = append(beside, file.Path)
		}
	}
	slices.Sort(licences)
	slices.Sort(beside)
	wantLicences := []string{"COPYING.LESSER", "COPYRIGHT", "LICENSE", "LICENSE-MIT", "LICENSES/MIT.txt", "LICENSES/README.md",
		"MIT-LICENSE", "UNLICENSE", "icu-license.html", "inner/COPYING", "inner/used/LICENSE", "licence.txt"}
	if !slices.Equal(licences, wantLicences) {
		t.Errorf("Collect read as licences\n%v, want\n%v", licences, wantLicences)
	}
	wantBeside := []string{"AUTHORS", "CONTRIBUTORS.md", "NOTICE", "THIRD_PARTY_NOTICES.md", "inner/AUTHORS"}
	if !slices.Equal(beside, wantBeside) {
		t.Errorf("Collect read beside them\n%v, want\n%v", beside, wantBeside)
	}
	// Byte for byte: a licence is never tidied.
	for _, file := range found[0].Files {
		if file.Path == "LICENSE" && string(file.Data) != mit {
			t.Errorf("the licence was read as %q", file.Data)
		}
	}
}

func TestAModuleWithNoLicenceFileIsRefused(t *testing.T) {
	fine := module(t, "example.com/fine", map[string]string{"LICENSE": mit})
	for name, files := range map[string]map[string]string{
		"nothing but a README":             {"README.md": "MIT, it says here", "inner/unused/LICENSE": mit},
		"a notice and its authors alone":   {"NOTICE": mit, "AUTHORS": "someone"},
		"a grant of patents alone":         {"PATENTS": mit},
		"a licence in Go alone":            {"license.go": "package bare // " + mit},
		"a hidden file alone":              {".LICENSE": mit},
		"a folder of that name, not files": {"LICENSE/README.md": mit},
	} {
		bare := module(t, "example.com/bare", files)
		_, err := notices.Collect([]notices.Module{bare, fine})
		if err == nil {
			t.Fatalf("a module with %s was accepted", name)
		}
		for _, want := range []string{"example.com/bare v1.2.3", "study-shelf on macOS", "no licence file"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: the refusal lacks %q: %v", name, want, err)
			}
		}
		if strings.Contains(err.Error(), "example.com/fine") {
			t.Errorf("%s: the refusal names a module that has its licence: %v", name, err)
		}
	}
}

// Under which licence a module is, a person says, in licences.go. A module
// nobody has read is not released, and neither is one whose files do not
// read as the licence that was written down for it.
func TestAModuleIsHeldToTheLicenceWrittenDownForIt(t *testing.T) {
	for _, tc := range []struct {
		name, licence, text, want string
	}{
		{"nothing is written down", "", mit, "say under which licence it is"},
		{"another licence is written down", "Apache-2.0", mit, "said to be under Apache-2.0"},
		{"one of two is not in its files", "MIT AND BSD-3-Clause", mit, "said to be under BSD-3-Clause"},
		{"a licence nothing knows the wording of", "MIT AND Imagined-1.0", mit, `"Imagined-1.0" is a licence internal/notices knows no wording of`},
	} {
		mod := module(t, "example.com/unread", map[string]string{"LICENSE": tc.text, "NOTICE": "Apache License, Version 2.0, it says beside it"})
		mod.Licence = tc.licence
		_, err := notices.Collect([]notices.Module{mod})
		if err == nil || !strings.Contains(err.Error(), "example.com/unread v1.2.3") || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want a refusal that names the module and says %q, got %v", tc.name, tc.want, err)
		}
	}
	// Both of two, each in a file of its own, and across line breaks.
	mod := module(t, "example.com/two", map[string]string{
		"LICENSE":     mit,
		"LICENSE-BSD": "Redistribution and use in source and\nbinary forms is permitted. Neither the\nname of anyone nor of anything.",
	})
	mod.Licence = "MIT AND BSD-3-Clause"
	if _, err := notices.Collect([]notices.Module{mod}); err != nil {
		t.Errorf("a module under two licences, with a file for each, was refused: %v", err)
	}
}

// PDFium came inside a module whose own licence says nothing of it. A file
// a module embeds is looked at by a person before it is released.
func TestAModuleThatEmbedsAFileIsRefusedUntilItIsLookedAt(t *testing.T) {
	mod := module(t, "example.com/blob", map[string]string{"LICENSE": mit, "engine.wasm": "\x00asm"})
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
	remarked := module(t, "example.com/Mixed/case/v2", map[string]string{"LICENSE.txt": mit, "NOTICE": "a notice"})
	remarked.Remark = "A port of something under another licence, which is not here."
	found, err := notices.Collect([]notices.Module{remarked, module(t, "example.com/plain", map[string]string{"COPYING": mit})})
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
		"modules/example.com/Mixed/case/v2/LICENSE.txt": mit,
		"modules/example.com/Mixed/case/v2/NOTICE":      "a notice",
		"modules/example.com/plain/COPYING":             mit,
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
	// Each module with its licence, and what is remarked of one under it.
	lines := "- `example.com/Mixed/case/v2` v1.2.3 (MIT), in `study-shelf` on macOS: " +
		"[LICENSE.txt](modules/example.com/Mixed/case/v2/LICENSE.txt), [NOTICE](modules/example.com/Mixed/case/v2/NOTICE)\n" +
		"\n  A port of something under another licence, which is not here.\n\n" +
		"- `example.com/plain` v1.2.3 (MIT), in `study-shelf` on macOS: [COPYING](modules/example.com/plain/COPYING)\n"
	if !strings.Contains(index, lines) || !strings.Contains(index, "BSD-3-Clause: [LICENSE](go/LICENSE)") {
		t.Errorf("the list does not name the modules' files and licences, a remark, and Go's licence:\n%s", index)
	}
	if kinds := set.Licences(); !slices.Equal(kinds, []string{"BSD-3-Clause", "MIT"}) {
		t.Errorf("the set is under %v, want Go's licence and the modules'", kinds)
	}
	if strings.Contains(index, "PDFium") {
		t.Errorf("the list speaks of PDFium when no program links it:\n%s", index)
	}
}
