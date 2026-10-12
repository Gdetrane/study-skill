//go:build unix

package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/shelftest"
)

// The two programs a release holds, built from this checkout and used as a
// learner uses them: Shelf is registered, a Topic names it, and study source
// index has it index the Topic's Sources, a book that is a PDF and a scan
// that has no text. What is indexed is then said by a study-shelf other
// than the one that indexed it, since study starts one for every question.
func TestABuiltStudyHasTheBuiltShelfIndexABook(t *testing.T) {
	study, shelf := buildStudy(t), shelftest.Shelf(t)
	home := t.TempDir()
	if real, err := filepath.EvalSymlinks(home); err == nil {
		home = real
	}
	state := filepath.Join(home, ".local", "state")
	env := []string{"HOME=" + home, "STUDY_HOME=" + filepath.Join(home, "study"), "PATH=" + os.Getenv("PATH"),
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"), "XDG_STATE_HOME=" + state, "LANG=C.UTF-8"}
	run := func(args ...string) (string, error) {
		t.Helper()
		cmd := exec.Command(study, args...)
		cmd.Env, cmd.Dir = env, home
		out, err := cmd.Output()
		if exit := (*exec.ExitError)(nil); err != nil && asExit(err, &exit) {
			return string(out) + string(exit.Stderr), err
		}
		return string(out), err
	}
	must := func(args ...string) string {
		t.Helper()
		out, err := run(args...)
		if err != nil {
			t.Fatalf("study %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return out
	}
	books := filepath.Join(home, "Books")
	if err := os.MkdirAll(books, 0o755); err != nil {
		t.Fatal(err)
	}
	book, scan := filepath.Join(books, "elimination.pdf"), filepath.Join(books, "scan.pdf")
	shelftest.WritePDF(t, book, [][]string{
		{"Elimination turns a system of equations into a triangular one."},
		{"Back substitution then gives the unknowns, the last one first."},
	})
	if err := os.WriteFile(scan, shelftest.PDF{Pages: [][]string{{"A page that is a picture."}}, Pictures: true}.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	must("topic", "create", "--title", "Linear algebra")
	must("source", "add", "linear-algebra", "--file", book)
	must("source", "add", "linear-algebra", "--file", scan)
	must("knowledge-base", "add", "shelf", "--", shelf)
	must("topic", "update", "linear-algebra", "--knowledge-base", "shelf")

	// listed is what study source list says, which a Shelf started for the
	// question answers. A busy computer may leave it less than its two
	// seconds once, not five times running.
	listed := func() core.SourceList {
		t.Helper()
		var list struct {
			Data core.SourceList `json:"data"`
		}
		for tries := 1; ; tries++ {
			list.Data = core.SourceList{}
			if err := json.Unmarshal([]byte(must("source", "list", "linear-algebra", "--json")), &list); err != nil {
				t.Fatal(err)
			}
			if x := list.Data.Indexing; x == nil || x.Problem == "" || tries == 5 {
				return list.Data
			}
		}
	}
	before := listed()
	if before.Indexing == nil || before.Indexing.Problem != "" || before.Indexing.Action != "study source index linear-algebra" {
		t.Fatalf("before anything is indexed, study source list says %+v", before.Indexing)
	}
	for _, src := range before.Sources {
		if src.Indexed == nil || src.Indexed.State != core.IndexNotIndexed {
			t.Errorf("before anything is indexed, %s is %+v", src.Title, src.Indexed)
		}
	}

	var indexed struct {
		Data core.IndexResult `json:"data"`
	}
	if err := json.Unmarshal([]byte(must("source", "index", "linear-algebra", "--json")), &indexed); err != nil {
		t.Fatal(err)
	}
	if got := indexed.Data.Sources; len(got) != 2 || got[0].State != core.IndexIndexed || got[0].Path != book ||
		got[1].State != core.IndexFailed || !strings.Contains(got[1].Reason, "no text layer") {
		t.Fatalf("study source index = %+v; want the book indexed, and the scan failed for having no text layer", got)
	}
	// The index is in the learner's state folder, outside the Study home,
	// under the collection's name.
	if _, err := os.Stat(filepath.Join(state, "lamplight", "knowledge-base-plugins", "shelf", indexed.Data.Collection+".sqlite")); err != nil {
		t.Errorf("Shelf's index of the collection is not where study gave it a folder: %v", err)
	}

	// Another study-shelf says the same afterwards: the book is indexed, and
	// the scan failed, with the reason the first one gave. A failure is no
	// next action, so there is none.
	after := listed()
	if after.Indexing == nil || after.Indexing.Problem != "" || after.Indexing.Action != "" {
		t.Errorf("afterwards study source list says %+v; want nothing to run", after.Indexing)
	}
	if got := after.Sources; len(got) != 2 || got[0].Indexed == nil || got[0].Indexed.State != core.IndexIndexed ||
		got[1].Indexed == nil || got[1].Indexed.State != core.IndexFailed || !strings.Contains(got[1].Indexed.Reason, "no text layer") {
		t.Errorf("afterwards the Sources are %+v", got)
	}
	// Run again, the book is left as it is. Read into a result of its own:
	// one that was read into before keeps what the new answer leaves out.
	var again struct {
		Data core.IndexResult `json:"data"`
	}
	if err := json.Unmarshal([]byte(must("source", "index", "linear-algebra", "--json")), &again); err != nil {
		t.Fatal(err)
	}
	if got := again.Data.Sources; len(got) != 2 || got[0].Asked || got[0].State != core.IndexIndexed || !got[1].Asked {
		t.Errorf("run again, study source index = %+v; want the book left alone and the scan tried again", got)
	}
}

// asExit reports whether err is a program's exit, and gives it.
func asExit(err error, exit **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*exit = e
	}
	return ok
}
