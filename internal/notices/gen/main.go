// Command gen writes NOTICES, the licences and notices of what study and
// study-shelf are built from, from the Go module cache. Run it from the
// repository's root, as make notices does, after a dependency changes, and
// commit what it writes. It needs no network once the modules are in the
// cache, and leaves NOTICES/pdfium, which is kept by hand, as it is.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mordor-forge/lamplight/v2/internal/notices"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "notices: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	if _, err := os.Stat("go.mod"); err != nil {
		return fmt.Errorf("run it from the repository's root: %w", err)
	}
	set, err := notices.Read(".")
	if err != nil {
		return err
	}
	if err := set.Write(notices.Folder); err != nil {
		return err
	}
	files := 1
	for _, notice := range set.Modules {
		files += len(notice.Files)
	}
	fmt.Printf("Wrote %s and %d files: Go's licence, and those of %d modules.\n",
		filepath.Join(notices.Folder, "README.md"), files, len(set.Modules))
	if set.GoKept {
		fmt.Printf("Go's licence was kept as it was: this Go keeps none in its GOROOT.\n")
	}
	return nil
}
