package notices_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/notices"
)

const goPdfium = "github.com/klippa-app/go-pdfium"

// zlib is what any text of the zlib licence says, as far as this package
// checks. It ends in a space, as a licence may.
const zlib = "The origin of this software must not be misrepresented \n"

func sum(text string) string {
	hash := sha256.Sum256([]byte(text))
	return hex.EncodeToString(hash[:])
}

// pdfium makes a go-pdfium as the module cache would hold it, and a folder
// of notices written for it.
func pdfium(t *testing.T) (folder string, linked notices.Module) {
	t.Helper()
	linked = module(t, goPdfium, map[string]string{"LICENSE": mit, "internal/pdfium_wasm/pdfium.wasm": "\x00asm, built"})
	linked.Embedded = []string{"internal/pdfium_wasm/pdfium.wasm"}
	folder = filepath.Join(t.TempDir(), "pdfium")
	write(t, folder, "README.md", "how it was found")
	write(t, folder, "pdfium/LICENSE", mit)
	write(t, folder, "zlib/LICENSE", zlib)
	write(t, folder, "sources.toml", `format = 1

[wasm]
module = "`+goPdfium+`"
version = "v1.2.3"
file = "internal/pdfium_wasm/pdfium.wasm"
sha256 = "`+sum("\x00asm, built")+`"

[[part]]
name = "PDFium"
version = "chromium/1"
licence = "MIT"
repository = "https://pdfium.example/pdfium"
revision = "abc"

  [[part.file]]
  path = "pdfium/LICENSE"
  source = "LICENSE"
  sha256 = "`+sum(mit)+`"

[[part]]
name = "zlib"
licence = "Zlib"
repository = "https://chromium.example/zlib"
revision = "def"

  [[part.file]]
  path = "zlib/LICENSE"
  source = "LICENSE"
  sha256 = "`+sum(zlib)+`"
`)
	return folder, linked
}

func write(t *testing.T, folder, name, text string) {
	t.Helper()
	dest := filepath.Join(folder, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// rewrite replaces old with new, once, in the folder's sources.toml.
func rewrite(t *testing.T, folder, old, new string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(folder, "sources.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), old) {
		t.Fatalf("sources.toml does not hold %q", old)
	}
	write(t, folder, "sources.toml", strings.Replace(string(data), old, new, 1))
}

// check reads the folder as Read does, for the modules that are linked.
func check(t *testing.T, folder string, linked ...notices.Module) error {
	t.Helper()
	_, err := notices.ReadPDFium(folder, linked)
	return err
}

// refused checks that the folder is refused, in words that hold every one of
// want.
func refused(t *testing.T, folder string, linked notices.Module, want ...string) {
	t.Helper()
	err := check(t, folder, linked)
	if err == nil {
		t.Fatalf("the folder was accepted, want a refusal that says %q", want)
	}
	for _, text := range want {
		if !strings.Contains(err.Error(), text) {
			t.Errorf("the refusal lacks %q:\n%v", text, err)
		}
	}
}

func TestTheNoticesOfPDFiumAreHeldToTheGoPdfiumThatIsLinked(t *testing.T) {
	t.Run("the folder written for it is accepted", func(t *testing.T) {
		folder, linked := pdfium(t)
		if err := check(t, folder, linked); err != nil {
			t.Fatal(err)
		}
	})

	// A new go-pdfium brings a pdfium.wasm built from another PDFium: the
	// notices are not its own until someone has looked.
	t.Run("another version of go-pdfium", func(t *testing.T) {
		folder, linked := pdfium(t)
		linked.Version = "v1.3.0"
		refused(t, folder, linked, "go.mod names "+goPdfium+" v1.3.0", "was written for v1.2.3", "README.md")
	})

	t.Run("another pdfium.wasm under the same version", func(t *testing.T) {
		folder, linked := pdfium(t)
		if err := os.Chmod(filepath.Join(linked.Dir, "internal", "pdfium_wasm", "pdfium.wasm"), 0o644); err != nil {
			t.Fatal(err)
		}
		write(t, linked.Dir, "internal/pdfium_wasm/pdfium.wasm", "\x00asm, built again")
		refused(t, folder, linked, "internal/pdfium_wasm/pdfium.wasm", sum("\x00asm, built again"))
	})

	t.Run("a second file that go-pdfium embeds", func(t *testing.T) {
		folder, linked := pdfium(t)
		linked.Embedded = append(linked.Embedded, "internal/other/other.wasm")
		refused(t, folder, linked, "embeds internal/other/other.wasm")
	})

	// A licence is copied as it is. One that was tidied, or retyped, is no
	// longer the text its authors published.
	t.Run("a notice that was edited", func(t *testing.T) {
		folder, linked := pdfium(t)
		write(t, folder, "zlib/LICENSE", strings.TrimSuffix(zlib, " \n")+"\n")
		refused(t, folder, linked, filepath.Join("zlib", "LICENSE"), "never edited")
	})

	t.Run("a notice that is gone", func(t *testing.T) {
		folder, linked := pdfium(t)
		if err := os.Remove(filepath.Join(folder, "pdfium", "LICENSE")); err != nil {
			t.Fatal(err)
		}
		refused(t, folder, linked, "names a file that is not there", filepath.Join("pdfium", "LICENSE"))
	})

	t.Run("a file nothing says the source of", func(t *testing.T) {
		folder, linked := pdfium(t)
		write(t, folder, "lcms/LICENSE", "from somewhere")
		refused(t, folder, linked, filepath.Join("lcms", "LICENSE"), "is in no part")
	})

	t.Run("a part without a revision", func(t *testing.T) {
		folder, linked := pdfium(t)
		rewrite(t, folder, `revision = "def"`, "")
		refused(t, folder, linked, `the part "zlib" needs`)
	})

	// Under which licence a part is, the list says, and its files bear out.
	t.Run("a part with no licence written down", func(t *testing.T) {
		folder, linked := pdfium(t)
		rewrite(t, folder, `licence = "Zlib"`, "")
		refused(t, folder, linked, `the part "zlib"`, "nothing says under which licence it is")
	})

	t.Run("a part under a licence its files do not read as", func(t *testing.T) {
		folder, linked := pdfium(t)
		rewrite(t, folder, `licence = "Zlib"`, `licence = "MIT"`)
		refused(t, folder, linked, `the part "zlib"`, "said to be under MIT")
	})

	// The page that says how each part was found, which the list links to.
	t.Run("a folder without its README", func(t *testing.T) {
		folder, linked := pdfium(t)
		if err := os.Remove(filepath.Join(folder, "README.md")); err != nil {
			t.Fatal(err)
		}
		refused(t, folder, linked, "has no README.md")
	})

	t.Run("a list about another module", func(t *testing.T) {
		folder, linked := pdfium(t)
		rewrite(t, folder, `module = "`+goPdfium+`"`, `module = "example.com/other"`)
		refused(t, folder, linked, "is about example.com/other")
	})
}

// The folder goes with go-pdfium: both are there, or neither.
func TestTheNoticesOfPDFiumGoWithGoPdfium(t *testing.T) {
	folder, linked := pdfium(t)
	other := module(t, "example.com/other", map[string]string{"LICENSE": mit})

	// Carried on when nothing holds a pdfium.wasm any more, the folder
	// would be the notices of a file no program holds.
	err := check(t, folder, other)
	if err == nil || !strings.Contains(err.Error(), "no program links "+goPdfium) {
		t.Errorf("a folder for a go-pdfium that nothing links was not refused: %v", err)
	}

	nowhere := filepath.Join(t.TempDir(), "pdfium")
	if sources, err := notices.ReadPDFium(nowhere, []notices.Module{other}); sources != nil || err != nil {
		t.Errorf("with no go-pdfium and no folder: %v, %v, want nothing to say", sources, err)
	}
	if err := check(t, nowhere, other, linked); err == nil {
		t.Error("a go-pdfium with no folder of notices was accepted")
	}
}

func TestSourcesOfANewerFormatAreRefused(t *testing.T) {
	folder, linked := pdfium(t)
	rewrite(t, folder, "format = 1", "format = 2")
	refused(t, folder, linked, "has format 2", "reads format 1")

	// A key this code does not know is not skipped: it may say something
	// the check should have held the folder to.
	folder, linked = pdfium(t)
	rewrite(t, folder, `licence = "Zlib"`, "licence = \"Zlib\"\nlicense = \"Zlib\"")
	refused(t, folder, linked, "unknown key")
}

func TestTheListSaysWhatIsInsidePDFium(t *testing.T) {
	folder, linked := pdfium(t)
	sources, err := notices.ReadPDFium(folder, []notices.Module{linked})
	if err != nil {
		t.Fatal(err)
	}
	found, err := notices.Collect([]notices.Module{linked})
	if err != nil {
		t.Fatalf("go-pdfium, whose pdfium.wasm the folder is for, was refused: %v", err)
	}
	dir := t.TempDir()
	set := &notices.Set{Go: []byte("Go's licence"), Modules: found, PDFium: sources}
	if err := set.Write(dir); err != nil {
		t.Fatal(err)
	}
	index, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"`" + goPdfium + "` v1.2.3",
		"- PDFium chromium/1 (MIT): [LICENSE](pdfium/pdfium/LICENSE)\n",
		"- zlib (Zlib): [LICENSE](pdfium/zlib/LICENSE)\n",
		"[pdfium/README.md](pdfium/README.md)",
	} {
		if !strings.Contains(string(index), want) {
			t.Errorf("the list lacks %q:\n%s", want, index)
		}
	}
	// The licences of its parts are among the set's.
	if kinds := set.Licences(); !slices.Equal(kinds, []string{"BSD-3-Clause", "MIT", "Zlib"}) {
		t.Errorf("the set is under %v, want Go's licence, the module's and those of the parts", kinds)
	}
}
