package plugintest_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/pluginclient"
	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
)

// Started with --record, the test plugin writes what it is asked beside its
// record: each tool, in order, with what it was given. Tests of what study
// asks a plugin read that, and not what study says it asked. And a tool told
// to take its time takes it.
func TestTheTestPluginRecordsWhatItIsAsked(t *testing.T) {
	ctx := context.Background()
	c := plugintest.NewComputer(t)
	record := filepath.Join(t.TempDir(), "record.json")
	if calls := plugintest.ReadCalls(t, record); calls != nil {
		t.Fatalf("a plugin that was never started was asked %v", calls)
	}
	plugin := c.Register(t, "kb", plugintest.Command("--record", record, "--delay", "status=150ms")...)
	conn, err := pluginclient.Start(ctx, c.Env(""), plugin, pluginclient.Options{Environ: c.Environ(), StopGrace: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	book := filepath.Join(c.Home, "book.txt")
	if err := os.WriteFile(book, []byte("A page of a book."), 0o644); err != nil {
		t.Fatal(err)
	}
	source := plugincontract.Source{ID: "book.k3f9a2", Title: "Book", Kind: plugincontract.SourceFile, Path: book,
		Hash: "sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"}
	began := time.Now()
	if _, err := conn.Status(ctx, "linear-algebra"); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(began); took < 150*time.Millisecond {
		t.Errorf("status, told to take 150ms, answered after %s", took)
	}
	if _, err := conn.Index(ctx, "linear-algebra", source); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Cancel(ctx, "linear-algebra", "book.k3f9a2"); err != nil {
		t.Fatal(err)
	}
	calls := plugintest.ReadCalls(t, record)
	if got := plugintest.Tools(calls); !slices.Equal(got, []string{"contract", "status", "index", "cancel"}) {
		t.Fatalf("the plugin recorded %v", got)
	}
	if calls[0].Collection() != "" || calls[0].Source() != "" {
		t.Errorf("the call of contract = %s", calls[0].Args)
	}
	for _, call := range calls[1:] {
		if call.Collection() != "linear-algebra" {
			t.Errorf("%s was recorded with the collection %q: %s", call.Tool, call.Collection(), call.Args)
		}
	}
	if got := calls[2].Indexed(); got != source || calls[2].Source() != source.ID {
		t.Errorf("index was recorded with %+v (%s), want %+v", got, calls[2].Source(), source)
	}
	if calls[3].Source() != source.ID {
		t.Errorf("cancel was recorded for %q", calls[3].Source())
	}
	// A second copy of the plugin adds to the same record of calls.
	conn.Close()
	again, err := pluginclient.Start(ctx, c.Env(""), plugin, pluginclient.Options{Environ: c.Environ(), StopGrace: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	again.Close()
	if got := plugintest.Tools(plugintest.ReadCalls(t, record)); !slices.Equal(got, []string{"contract", "status", "index", "cancel", "contract"}) {
		t.Errorf("after a second start the plugin recorded %v", got)
	}
}
