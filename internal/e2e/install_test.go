package e2e

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/shelftest"
)

// TestTheInstallScript runs install.sh the way its one-line command does, as
// a script piped into sh, against a local folder of release files laid out as
// on a GitHub release (ADR-0010). The script's PATH holds only the programs
// it is meant to need, and its HOME is the test's.
//
// Two cases install the study and the study-shelf built from this checkout.
// The others are about the script alone and install stand-ins, which start
// faster than a study built with the race detector.
func TestTheInstallScript(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the study and study-shelf binaries")
	}
	binary, err := os.ReadFile(buildStudy(t))
	if err != nil {
		t.Fatal(err)
	}
	shelf, err := os.ReadFile(shelftest.Shelf(t))
	if err != nil {
		t.Fatal(err)
	}
	built := newRelease(t, binary, shelf, "dev")
	rel := newRelease(t, []byte(standInStudy), []byte(standInShelf), "v0.0.0-standin")
	host := "lamplight_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"

	t.Run("installs study and study-shelf, the man page and completions", func(t *testing.T) {
		in := newInstall(t, built.folder(t, host))
		out := in.mustRun()
		t.Logf("install.sh printed:\n%s", out)

		dest := filepath.Join(in.home, ".local", "bin", "study")
		in.wantStudy(dest, built.version)
		in.wantShelf(dest+"-shelf", built.version)
		if got := names(t, filepath.Dir(dest)); !slices.Equal(got, []string{"study", "study-shelf"}) {
			t.Errorf("the install folder holds %v, want study and study-shelf alone", got)
		}
		// The two are installed to work together: the study that was
		// installed registers the study-shelf beside it, starts it, and
		// they speak the contract.
		in.wantShelfChecked(dest)
		// And the script says how: that study setup registers Shelf, and
		// the command that shows it starts.
		for _, want := range []string{"and register\nShelf, which indexes a Topic's books and web pages and searches them:\n  " + dest + " setup\n",
			"  " + dest + " knowledge-base check shelf\n"} {
			if !strings.Contains(out, want) {
				t.Errorf("install.sh does not print %q", want)
			}
		}
		if strings.Contains(out, "knowledge-base add") {
			t.Errorf("install.sh still has the learner register Shelf by hand:\n%s", out)
		}
		page, err := os.ReadFile(filepath.Join(in.home, ".local", "share", "man", "man1", "study.1.gz"))
		if err != nil || !bytes.Equal(page, built.manPage) {
			t.Errorf("the man page was not installed from the archive: %v", err)
		}
		completions := filepath.Join(in.home, ".config", "fish", "completions", "study.fish")
		if strings.Contains(out, "A package already provides") {
			t.Log("a package on this machine provides study's completions, so study installed none")
		} else if _, err := os.Stat(completions); err != nil {
			t.Errorf("study completion install did not run in the test's home: %v\n%s", err, out)
		}
		// The PATH here does not hold the install folder.
		for _, want := range []string{
			"Installed " + dest + " (study version dev)",
			"Installed " + dest + "-shelf (study-shelf version dev)",
			filepath.Dir(dest) + " is not on your PATH. Run this once in fish:",
			`fish_add_path "` + filepath.Dir(dest) + `"`,
			"  " + dest + " setup\n",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("the output lacks %q:\n%s", want, out)
			}
		}
		if left := names(t, in.tmp); len(left) != 0 {
			t.Errorf("the script left %v in the temporary folder", left)
		}
	})

	t.Run("picks the archive built for the machine", func(t *testing.T) {
		for _, tc := range []struct {
			system, machine, translated, archive string
		}{
			{"Linux", "x86_64", "", "lamplight_linux_amd64.tar.gz"},
			{"Linux", "aarch64", "", "lamplight_linux_arm64.tar.gz"},
			{"Darwin", "arm64", "", "lamplight_darwin_arm64.tar.gz"},
			{"Darwin", "x86_64", "", "lamplight_darwin_amd64.tar.gz"},
			// An Intel shell on Apple silicon, translated by Rosetta.
			{"Darwin", "x86_64", "1", "lamplight_darwin_arm64.tar.gz"},
		} {
			t.Run(tc.system+" "+tc.machine+" "+tc.translated, func(t *testing.T) {
				// The folder holds this one archive, so any other name fails.
				in := newInstall(t, rel.folder(t, tc.archive))
				in.fake("uname", `case $1 in -s) echo `+tc.system+` ;; -m) echo `+tc.machine+` ;; esac`)
				if tc.translated == "" {
					in.fake("sysctl", `echo "sysctl: unknown oid" >&2; exit 1`)
				} else {
					in.fake("sysctl", `echo `+tc.translated)
				}
				in.mustRun()
				in.wantStudy(filepath.Join(in.home, ".local", "bin", "study"), rel.version)
			})
		}
	})

	t.Run("refuses a machine no release is built for", func(t *testing.T) {
		for _, tc := range [][2]string{{"FreeBSD", "amd64"}, {"Linux", "riscv64"}, {"MINGW64_NT-10.0", "x86_64"}} {
			in := newInstall(t, rel.folder(t, host))
			in.fake("uname", `case $1 in -s) echo `+tc[0]+` ;; -m) echo `+tc[1]+` ;; esac`)
			out := in.mustFail()
			for _, want := range []string{
				"there is no study build for " + tc[0] + " on " + tc[1],
				"go install github.com/mordor-forge/lamplight/v2/cmd/study@latest",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("%v: the output lacks %q:\n%s", tc, want, out)
				}
			}
			in.wantNothingInstalled()
		}
	})

	t.Run("refuses a download that fails its checksum, and installs nothing", func(t *testing.T) {
		dist := rel.folder(t, host)
		writeChecksums(t, dist, map[string]string{host: strings.Repeat("0", 64)})
		in := newInstall(t, dist)
		out := in.mustFail()
		if !strings.Contains(out, host+" does not match its checksum") {
			t.Errorf("the output does not name the mismatch:\n%s", out)
		}
		in.wantNothingInstalled()
	})

	t.Run("refuses a download that checksums.txt does not list", func(t *testing.T) {
		dist := rel.folder(t, host)
		// Another file's line, and the archive's name only as part of another.
		writeChecksums(t, dist, map[string]string{
			"lamplight_2.0.0_amd64.deb": strings.Repeat("1", 64),
			"old_" + host:               sha256Hex(rel.archive),
		})
		in := newInstall(t, dist)
		out := in.mustFail()
		if !strings.Contains(out, "checksums.txt does not list "+host) {
			t.Errorf("the output does not say the archive is not listed:\n%s", out)
		}
		in.wantNothingInstalled()
	})

	t.Run("refuses when a release file cannot be downloaded", func(t *testing.T) {
		for _, missing := range []string{host, "checksums.txt"} {
			dist := rel.folder(t, host)
			if err := os.Remove(filepath.Join(dist, missing)); err != nil {
				t.Fatal(err)
			}
			in := newInstall(t, dist)
			out := in.mustFail()
			if !strings.Contains(out, "could not download file://"+dist+"/"+missing) {
				t.Errorf("without %s, the output does not name the failed download:\n%s", missing, out)
			}
			in.wantNothingInstalled()
		}
	})

	t.Run("refuses to install what it cannot check", func(t *testing.T) {
		in := newInstall(t, rel.folder(t, host))
		in.path = tools(t, "sha256sum", "shasum")
		out := in.mustFail()
		if !strings.Contains(out, "needs sha256sum or shasum") {
			t.Errorf("the output does not name the missing program:\n%s", out)
		}
		in.wantNothingInstalled()
	})

	t.Run("checks the download with shasum where there is no sha256sum", func(t *testing.T) {
		if _, err := exec.LookPath("shasum"); err != nil {
			t.Skip("this machine has no shasum")
		}
		in := newInstall(t, rel.folder(t, host))
		in.path = tools(t, "sha256sum")
		in.mustRun()
		in.wantStudy(filepath.Join(in.home, ".local", "bin", "study"), rel.version)

		dist := rel.folder(t, host)
		writeChecksums(t, dist, map[string]string{host: strings.Repeat("0", 64)})
		in = newInstall(t, dist)
		in.path = tools(t, "sha256sum")
		in.mustFail()
		in.wantNothingInstalled()
	})

	t.Run("upgrades a study that is there, and keeps it when the download is bad", func(t *testing.T) {
		in := newInstall(t, rel.folder(t, host))
		dest := filepath.Join(in.home, ".local", "bin", "study")
		writeExecutable(t, dest, "#!/bin/sh\necho study version v0.0.1\n")
		writeExecutable(t, dest+"-shelf", "#!/bin/sh\necho study-shelf version v0.0.1\n")
		// A second name for the file that is there, as a study that is
		// running has: it must keep its content.
		was := filepath.Join(in.home, "the-study-that-was")
		if err := os.Link(dest, was); err != nil {
			t.Fatal(err)
		}

		bad := rel.folder(t, host)
		writeChecksums(t, bad, map[string]string{host: strings.Repeat("0", 64)})
		good := in.dist
		in.dist = bad
		in.mustFail()
		for _, file := range []string{dest, dest + "-shelf"} {
			if data, err := os.ReadFile(file); err != nil || !strings.Contains(string(data), "v0.0.1") {
				t.Errorf("a refused download changed the %s that was installed: %v", filepath.Base(file), err)
			}
		}

		in.dist = good
		in.mustRun()
		in.wantStudy(dest, rel.version)
		in.wantShelf(dest+"-shelf", rel.version)
		if data, err := os.ReadFile(was); err != nil || !strings.Contains(string(data), "v0.0.1") {
			t.Errorf("the upgrade wrote into the study that was there, instead of replacing it: %v", err)
		}
		if got := names(t, filepath.Dir(dest)); !slices.Equal(got, []string{"study", "study-shelf"}) {
			t.Errorf("the install folder holds %v, want study and study-shelf alone", got)
		}
	})

	t.Run("installs nothing when the new study does not run, and keeps the one that is there", func(t *testing.T) {
		broken := newRelease(t, []byte("#!/bin/sh\necho cannot start >&2\nexit 1\n"), []byte(standInShelf), "none")
		in := newInstall(t, broken.folder(t, host))
		dest := filepath.Join(in.home, ".local", "bin", "study")
		refused := func() {
			t.Helper()
			out := in.mustFail()
			for _, want := range []string{"does not run", "cannot start", "nothing was installed"} {
				if !strings.Contains(out, want) {
					t.Errorf("the refusal lacks %q:\n%s", want, out)
				}
			}
		}

		refused()
		if got := names(t, filepath.Dir(dest)); len(got) != 0 {
			t.Errorf("the install folder holds %v after a study that does not run, want nothing", got)
		}

		writeExecutable(t, dest, "#!/bin/sh\necho study version v0.0.1\n")
		refused()
		in.wantStudy(dest, "v0.0.1")
		if got := names(t, filepath.Dir(dest)); len(got) != 1 {
			t.Errorf("the install folder holds %v, want the study that was there alone", got)
		}
	})

	t.Run("installs neither program when the new study-shelf does not run", func(t *testing.T) {
		// A study that runs, beside a study-shelf that does not: the two are
		// one release, and half of one is not installed.
		broken := newRelease(t, []byte(standInStudy), []byte("#!/bin/sh\necho no such library >&2\nexit 127\n"), "v0.0.0-standin")
		in := newInstall(t, broken.folder(t, host))
		dest := filepath.Join(in.home, ".local", "bin", "study")
		refused := func() {
			t.Helper()
			out := in.mustFail()
			for _, want := range []string{"the study-shelf in " + host + " does not run", "no such library", "nothing was installed"} {
				if !strings.Contains(out, want) {
					t.Errorf("the refusal lacks %q:\n%s", want, out)
				}
			}
		}

		refused()
		if got := names(t, filepath.Dir(dest)); len(got) != 0 {
			t.Errorf("the install folder holds %v after a study-shelf that does not run, want nothing", got)
		}

		writeExecutable(t, dest, "#!/bin/sh\necho study version v0.0.1\n")
		writeExecutable(t, dest+"-shelf", "#!/bin/sh\necho study-shelf version v0.0.1\n")
		refused()
		in.wantStudy(dest, "v0.0.1")
		in.wantShelf(dest+"-shelf", "v0.0.1")
		if got := names(t, filepath.Dir(dest)); !slices.Equal(got, []string{"study", "study-shelf"}) {
			t.Errorf("the install folder holds %v, want the two programs that were there alone", got)
		}

		// A folder where study-shelf is to go.
		in = newInstall(t, rel.folder(t, host))
		if err := os.MkdirAll(filepath.Join(in.home, ".local", "bin", "study-shelf"), 0o755); err != nil {
			t.Fatal(err)
		}
		if out := in.mustFail(); !strings.Contains(out, "study-shelf is a folder; nothing was installed") {
			t.Errorf("the output does not say study-shelf is a folder:\n%s", out)
		}
		if got := names(t, filepath.Join(in.home, ".local", "bin")); !slices.Equal(got, []string{"study-shelf"}) {
			t.Errorf("the install folder holds %v, want the folder that was there alone", got)
		}
	})

	t.Run("installs nothing from an archive that holds no study-shelf", func(t *testing.T) {
		// Every release holds both: an archive with study alone is not one.
		half := newRelease(t, []byte(standInStudy), nil, "v0.0.0-standin")
		in := newInstall(t, half.folder(t, host))
		if out := in.mustFail(); !strings.Contains(out, "holds no study-shelf") || !strings.Contains(out, "nothing was installed") {
			t.Errorf("the output does not say that the archive holds no study-shelf:\n%s", out)
		}
		if got := names(t, filepath.Join(in.home, ".local", "bin")); len(got) != 0 {
			t.Errorf("the install folder holds %v, want nothing", got)
		}
	})

	t.Run("installs into the folder STUDY_INSTALL_DIR names", func(t *testing.T) {
		in := newInstall(t, rel.folder(t, host))
		dir := filepath.Join(in.home, "tools")
		in.env = append(in.env, "STUDY_INSTALL_DIR="+dir)
		out := in.mustRun()
		in.wantStudy(filepath.Join(dir, "study"), rel.version)
		in.wantShelf(filepath.Join(dir, "study-shelf"), rel.version)
		// man would not look beside a folder that is not a bin folder.
		if strings.Contains(out, "study.1.gz") {
			t.Errorf("a man page was installed for a folder man does not look beside:\n%s", out)
		}
		for _, sub := range []string{".local/bin", ".local/share/man"} {
			if _, err := os.Stat(filepath.Join(in.home, sub)); err == nil {
				t.Errorf("the script wrote to ~/%s although STUDY_INSTALL_DIR names another folder", sub)
			}
		}

		in = newInstall(t, rel.folder(t, host))
		dir = filepath.Join(in.home, "is-a-folder")
		if err := os.MkdirAll(filepath.Join(dir, "study"), 0o755); err != nil {
			t.Fatal(err)
		}
		in.env = append(in.env, "STUDY_INSTALL_DIR="+dir)
		if out := in.mustFail(); !strings.Contains(out, "study is a folder") {
			t.Errorf("the output does not say study is a folder:\n%s", out)
		}
		if got := names(t, filepath.Join(dir, "study")); len(got) != 0 {
			t.Errorf("the script wrote %v into a folder named study", got)
		}

		// A folder that cannot be made, and one that cannot be written to.
		in = newInstall(t, rel.folder(t, host))
		if err := os.WriteFile(filepath.Join(in.home, "a-file"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		in.env = append(in.env, "STUDY_INSTALL_DIR="+filepath.Join(in.home, "a-file", "bin"))
		if out := in.mustFail(); !strings.Contains(out, "could not create "+filepath.Join(in.home, "a-file", "bin")) {
			t.Errorf("the output does not say the folder could not be made:\n%s", out)
		}
		if os.Getuid() != 0 {
			in = newInstall(t, rel.folder(t, host))
			dir = filepath.Join(in.home, "read-only")
			if err := os.Mkdir(dir, 0o555); err != nil {
				t.Fatal(err)
			}
			in.env = append(in.env, "STUDY_INSTALL_DIR="+dir)
			if out := in.mustFail(); !strings.Contains(out, "could not write "+filepath.Join(dir, "study")) {
				t.Errorf("the output does not say study could not be written:\n%s", out)
			}
			if got := names(t, dir); len(got) != 0 {
				t.Errorf("the script left %v in a folder it could not install into", got)
			}
		}
	})

	t.Run("says how study is reached", func(t *testing.T) {
		line := func(shell string) string {
			in := newInstall(t, rel.folder(t, host))
			in.shell = shell
			return in.mustRun()
		}
		dir := "/.local/bin"
		for shell, want := range map[string]string{
			"/usr/bin/zsh": "is not on your PATH. Add this line to ~/.zshrc, then open a new shell:\n  export PATH=\"",
			"/bin/bash":    "is not on your PATH. Add this line to ~/.bashrc, then open a new shell:\n  export PATH=\"",
			"/bin/tcsh":    "is not on your PATH. Add this line to your shell's startup file:\n  export PATH=\"",
		} {
			if out := line(shell); !strings.Contains(out, dir+" "+want) || !strings.Contains(out, dir+`:$PATH"`) {
				t.Errorf("with SHELL=%s, the output lacks %q:\n%s", shell, want, out)
			}
		}

		// On PATH: nothing to add, and the next step needs no path.
		in := newInstall(t, rel.folder(t, host))
		bin := filepath.Join(in.home, ".local", "bin")
		in.path += string(os.PathListSeparator) + bin
		out := in.mustRun()
		if strings.Contains(out, "on your PATH") || !strings.Contains(out, "\n  study setup\n") {
			t.Errorf("with the install folder on PATH, the output should not mention the PATH, and should end with study setup:\n%s", out)
		}

		// On PATH with a trailing slash, which is the same folder.
		in = newInstall(t, rel.folder(t, host))
		in.path += string(os.PathListSeparator) + filepath.Join(in.home, ".local", "bin") + "/"
		if out := in.mustRun(); strings.Contains(out, "on your PATH") {
			t.Errorf("a trailing slash on the PATH entry made the script mention the PATH:\n%s", out)
		}

		// Another study earlier on PATH would be the one that runs.
		in = newInstall(t, rel.folder(t, host))
		bin = filepath.Join(in.home, ".local", "bin")
		other := filepath.Join(in.home, "go", "bin")
		writeExecutable(t, filepath.Join(other, "study"), "#!/bin/sh\necho study version v0.0.1\n")
		in.path = other + string(os.PathListSeparator) + in.path + string(os.PathListSeparator) + bin
		out = in.mustRun()
		for _, want := range []string{
			"Another study comes first on your PATH: " + filepath.Join(other, "study"),
			"  " + filepath.Join(bin, "study") + " setup\n",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("with another study first on PATH, the output lacks %q:\n%s", want, out)
			}
		}
	})

	t.Run("installs study even when its completions cannot be installed", func(t *testing.T) {
		in := newInstall(t, built.folder(t, host))
		in.shell = "/bin/tcsh"
		out := in.mustRun()
		dest := filepath.Join(in.home, ".local", "bin", "study")
		in.wantStudy(dest, built.version)
		for _, want := range []string{
			"cannot tell your shell from $SHELL", // study's own words
			"Shell completions were not installed. To install them later: " + dest + " completion install",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("the output lacks %q:\n%s", want, out)
			}
		}
	})

	t.Run("downloads from the GitHub release, over HTTPS only", func(t *testing.T) {
		const releases = "https://github.com/mordor-forge/lamplight/releases/"
		for _, tc := range []struct{ version, want string }{
			{"", releases + "latest/download/checksums.txt"},
			{"latest", releases + "latest/download/checksums.txt"},
			{"v2.0.0", releases + "download/v2.0.0/checksums.txt"},
			{"2.1.0-rc.1", releases + "download/v2.1.0-rc.1/checksums.txt"},
		} {
			in := newInstall(t, "")
			if tc.version != "" {
				in.env = append(in.env, "STUDY_VERSION="+tc.version)
			}
			// A curl that records what it was asked for and fetches nothing.
			log := filepath.Join(in.home, "curl.log")
			in.env = append(in.env, "CURL_LOG="+log)
			in.fake("curl", `echo "$*" >>"$CURL_LOG"; exit 22`)
			out := in.mustFail()
			asked, _ := os.ReadFile(log)
			if !strings.HasSuffix(strings.TrimSpace(string(asked)), " "+tc.want) || !strings.Contains(string(asked), "--proto =https ") {
				t.Errorf("STUDY_VERSION=%q: curl was asked for %q, want %s over HTTPS only", tc.version, asked, tc.want)
			}
			if !strings.Contains(out, "could not download "+tc.want) {
				t.Errorf("STUDY_VERSION=%q: the output does not name the failed download:\n%s", tc.version, out)
			}
			in.wantNothingInstalled()
		}

		in := newInstall(t, "")
		in.env = append(in.env, "STUDY_VERSION=v2.0.0/../../evil")
		log := filepath.Join(in.home, "curl.log")
		in.env = append(in.env, "CURL_LOG="+log)
		in.fake("curl", `echo "$*" >>"$CURL_LOG"; exit 22`)
		if out := in.mustFail(); !strings.Contains(out, "STUDY_VERSION must be a release such as v2.0.0") {
			t.Errorf("the output does not refuse the version:\n%s", out)
		}
		if asked, err := os.ReadFile(log); err == nil {
			t.Errorf("curl ran for a version that is not a release name: %s", asked)
		}

		in = newInstall(t, "")
		log = filepath.Join(in.home, "curl.log")
		in.env = append(in.env, "STUDY_DOWNLOAD_URL=http://example.com/dist", "CURL_LOG="+log)
		in.fake("curl", `echo "$*" >>"$CURL_LOG"; exit 22`)
		if out := in.mustFail(); !strings.Contains(out, "STUDY_DOWNLOAD_URL must start with https:// or file://") {
			t.Errorf("the output does not refuse a download without HTTPS:\n%s", out)
		}
		if asked, err := os.ReadFile(log); err == nil {
			t.Errorf("curl ran for an address that is not HTTPS: %s", asked)
		}
	})

	t.Run("explains itself and takes no arguments", func(t *testing.T) {
		in := newInstall(t, rel.folder(t, host))
		out, err := in.run("--help")
		if err != nil || !strings.Contains(out, "STUDY_INSTALL_DIR") {
			t.Errorf("--help: %v\n%s", err, out)
		}
		in.wantNothingInstalled()
		out, err = in.run("v2.0.0")
		if err == nil || !strings.Contains(out, "Usage: sh install.sh") {
			t.Errorf("an argument should be refused with the usage: %v\n%s", err, out)
		}
		in.wantNothingInstalled()
	})
}

// standInStudy answers the two things install.sh asks of study.
const standInStudy = `#!/bin/sh
case "$*" in
--version) echo "study version v0.0.0-standin" ;;
"completion install") echo "Installed completions (a stand-in)" ;;
*)
	echo "the stand-in for study was run with: $*" >&2
	exit 64
	;;
esac
`

// standInShelf answers the one thing install.sh asks of study-shelf.
const standInShelf = `#!/bin/sh
case "$*" in
--version) echo "study-shelf version v0.0.0-standin" ;;
*)
	echo "the stand-in for study-shelf was run with: $*" >&2
	exit 64
	;;
esac
`

// release is what the test puts in a folder of release files: one archive,
// holding a study and a study-shelf that run on this machine whatever
// platform the archive's name claims, and checksums.txt.
type release struct {
	archive []byte
	manPage []byte
	version string // what its study --version prints after "study version "
}

// newRelease makes a release of a study and a study-shelf. A nil shelf makes
// a release that holds none, as one from before Shelf does.
func newRelease(t *testing.T, binary, shelf []byte, version string) release {
	t.Helper()
	var page bytes.Buffer
	zw := gzip.NewWriter(&page)
	_, _ = zw.Write([]byte(".TH STUDY 1\n"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	rel := release{manPage: page.Bytes(), version: version}

	// The layout of a GoReleaser archive: see archives in .goreleaser.yaml.
	var buf bytes.Buffer
	zw = gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	type file struct {
		name string
		mode int64
		data []byte
	}
	files := []file{
		{"LICENSE", 0o644, []byte("MIT License\n")},
		{"README.md", 0o644, []byte("# Lamplight\n")},
		{"completions/study.fish", 0o644, []byte("# not what the script installs\n")},
		{"manpages/study.1.gz", 0o644, rel.manPage},
		{"study", 0o755, binary},
	}
	if shelf != nil {
		files = append(files, file{"study-shelf", 0o755, shelf})
	}
	for _, f := range files {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: f.mode, Size: int64(len(f.data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(f.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	rel.archive = buf.Bytes()
	return rel
}

// folder writes the archive under name, and a checksums.txt that lists it,
// into a new folder.
func (r release) folder(t *testing.T, name string) string {
	t.Helper()
	dist := t.TempDir()
	if err := os.WriteFile(filepath.Join(dist, name), r.archive, 0o644); err != nil {
		t.Fatal(err)
	}
	writeChecksums(t, dist, map[string]string{
		name:                        sha256Hex(r.archive),
		"lamplight_2.0.0_amd64.deb": strings.Repeat("1", 64),
	})
	return dist
}

// writeChecksums writes checksums.txt as GoReleaser does: a hash, two spaces
// and a file name on each line.
func writeChecksums(t *testing.T, dist string, sums map[string]string) {
	t.Helper()
	var b strings.Builder
	for name, sum := range sums {
		fmt.Fprintf(&b, "%s  %s\n", sum, name)
	}
	if err := os.WriteFile(filepath.Join(dist, "checksums.txt"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// installTools are the programs install.sh runs. gzip is there for tar. The
// script's PATH holds links to these and nothing else, so it cannot come to
// need a program this list does not name, and study cannot reach an agent.
var installTools = []string{"uname", "curl", "tar", "gzip", "mktemp", "awk", "sha256sum", "shasum",
	"cp", "mv", "chmod", "mkdir", "rm", "cmp"}

// tools returns a folder that links to installTools, leaving out the ones
// named. Programs this machine lacks are left out too: a Mac may have no
// sha256sum, and the script must then do without.
func tools(t *testing.T, without ...string) string {
	t.Helper()
	dir := t.TempDir()
next:
	for _, name := range installTools {
		for _, w := range without {
			if name == w {
				continue next
			}
		}
		if path, err := exec.LookPath(name); err == nil {
			if err := os.Symlink(path, filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
		}
	}
	return dir
}

// install is one run of install.sh: its release folder and its environment.
type install struct {
	t                *testing.T
	home, tmp, fakes string
	dist             string
	path             string
	shell            string
	env              []string
}

func newInstall(t *testing.T, dist string) *install {
	t.Helper()
	in := &install{t: t, home: t.TempDir(), tmp: t.TempDir(), fakes: t.TempDir(), dist: dist,
		path: tools(t), shell: "/usr/bin/fish"}
	return in
}

// fake puts a shell script in front of the program of that name.
func (in *install) fake(name, body string) {
	in.t.Helper()
	writeExecutable(in.t, filepath.Join(in.fakes, name), "#!/bin/sh\n"+body+"\n")
}

// run pipes install.sh into sh, as curl does, and returns what it printed.
func (in *install) run(args ...string) (string, error) {
	in.t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		in.t.Fatalf("the test pipes install.sh into sh: %v", err)
	}
	script, err := os.ReadFile(filepath.Join("..", "..", "install.sh"))
	if err != nil {
		in.t.Fatal(err)
	}
	cmd := exec.Command(sh, append([]string{"-s", "--"}, args...)...)
	cmd.Stdin = bytes.NewReader(script)
	cmd.Dir = in.home
	cmd.Env = append([]string{
		"HOME=" + in.home,
		"PATH=" + in.fakes + string(os.PathListSeparator) + in.path,
		"SHELL=" + in.shell,
		"TMPDIR=" + in.tmp,
		"STUDY_HOME=" + filepath.Join(in.home, "study"),
		"XDG_CONFIG_HOME=" + filepath.Join(in.home, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(in.home, ".local", "share"),
		"XDG_STATE_HOME=" + filepath.Join(in.home, ".local", "state"),
		"XDG_CACHE_HOME=" + filepath.Join(in.home, ".cache"),
	}, in.env...)
	if in.dist != "" {
		cmd.Env = append(cmd.Env, "STUDY_DOWNLOAD_URL=file://"+in.dist)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (in *install) mustRun() string {
	in.t.Helper()
	out, err := in.run()
	if err != nil {
		in.t.Fatalf("install.sh: %v\n%s", err, out)
	}
	return out
}

func (in *install) mustFail() string {
	in.t.Helper()
	out, err := in.run()
	if err == nil {
		in.t.Fatalf("install.sh succeeded, want it to refuse:\n%s", out)
	}
	return out
}

// wantStudy checks that the release's study is installed at dest and runs.
func (in *install) wantStudy(dest, version string) {
	in.t.Helper()
	info, err := os.Stat(dest)
	if err != nil {
		in.t.Fatalf("study was not installed: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		in.t.Errorf("study has mode %v, want 0755", info.Mode().Perm())
	}
	cmd := exec.Command(dest, "--version")
	cmd.Env = []string{"HOME=" + in.home, "PATH=" + in.path}
	if out, err := cmd.CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "study version "+version {
		in.t.Errorf("the installed study does not run as version %s: %v\n%s", version, err, out)
	}
}

// wantShelf checks that the release's study-shelf is installed at dest and
// runs.
func (in *install) wantShelf(dest, version string) {
	in.t.Helper()
	info, err := os.Stat(dest)
	if err != nil {
		in.t.Fatalf("study-shelf was not installed: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		in.t.Errorf("study-shelf has mode %v, want 0755", info.Mode().Perm())
	}
	cmd := exec.Command(dest, "--version")
	cmd.Env = []string{"HOME=" + in.home, "PATH=" + in.path}
	if out, err := cmd.CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "study-shelf version "+version {
		in.t.Errorf("the installed study-shelf does not run as version %s: %v\n%s", version, err, out)
	}
}

// wantShelfChecked runs the two commands the script prints, as the learner
// does: the installed study's setup registers the study-shelf installed
// beside it, and study checks it, starting it as it starts any plugin, and
// they speak the contract. The home is a temporary folder here, from where
// setup registers nothing unless it is forced to.
func (in *install) wantShelfChecked(study string) {
	in.t.Helper()
	env := []string{"HOME=" + in.home, "PATH=" + filepath.Dir(study) + string(os.PathListSeparator) + in.path,
		"STUDY_HOME=" + filepath.Join(in.home, "study"),
		"XDG_CONFIG_HOME=" + filepath.Join(in.home, ".config"), "XDG_STATE_HOME=" + filepath.Join(in.home, ".local", "state")}
	for _, args := range [][]string{
		{"setup", "--force"},
		{"knowledge-base", "check", "shelf", "--json"},
	} {
		cmd := exec.Command(study, args...)
		cmd.Env, cmd.Dir = env, in.home
		out, err := cmd.CombinedOutput()
		if err != nil {
			in.t.Fatalf("study %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		if args[1] != "check" {
			continue
		}
		var checked struct {
			OK   bool             `json:"ok"`
			Data core.PluginCheck `json:"data"`
		}
		if err := json.Unmarshal(out, &checked); err != nil || !checked.OK {
			in.t.Fatalf("study knowledge-base check shelf: %v\n%s", err, out)
		}
		in.t.Logf("study knowledge-base check shelf --json printed:\n%s", out)
		got := checked.Data
		if got.Contract.Version != 1 || !slices.Equal(got.Contract.Sources, []string{"file", "url"}) ||
			!slices.Equal(got.Contract.Search, []string{"keyword"}) || !slices.Equal(got.Contract.Locations, []string{"pages"}) {
			in.t.Errorf("the installed Shelf says its contract is %+v", got.Contract)
		}
		if got.Server.Name != "study-shelf" || len(got.Command) != 1 || got.Command[0] != study+"-shelf" {
			in.t.Errorf("study checked %+v, started as %v; want the study-shelf that was installed", got.Server, got.Command)
		}
		// Its folder is in the learner's state folder, and holds nothing
		// yet: Shelf writes when it indexes.
		if want := filepath.Join(in.home, ".local", "state", "lamplight", "knowledge-base-plugins", "shelf"); got.DataFolder != want {
			in.t.Errorf("Shelf's folder is %s, want %s", got.DataFolder, want)
		} else if left := names(in.t, want); len(left) != 0 {
			in.t.Errorf("a check left %v in Shelf's folder", left)
		}
	}
}

// wantNothingInstalled checks that a refused run wrote nothing to the home
// and left nothing in the temporary folder.
func (in *install) wantNothingInstalled() {
	in.t.Helper()
	for _, name := range names(in.t, in.home) {
		if name != "curl.log" {
			in.t.Errorf("a refused install wrote %s to the home", name)
		}
	}
	if left := names(in.t, in.tmp); len(left) != 0 {
		in.t.Errorf("a refused install left %v in the temporary folder", left)
	}
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}
