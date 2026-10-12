package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/plugintest"
)

// topicWithAPlugin returns a learner's computer on which the test plugin is
// registered as kb, a Core for it, a Topic c that names the plugin, and
// where the plugin records how it was started and what it was asked.
func topicWithAPlugin(t *testing.T) (c *plugintest.Computer, k *Core, record string) {
	t.Helper()
	ctx := context.Background()
	c = plugintest.NewComputer(t)
	record = filepath.Join(t.TempDir(), "record.json")
	c.Register(t, "kb", plugintest.Command("--record", record)...)
	k, err := Open(Options{
		Getenv: c.Getenv,
		Dir:    c.Home,
		Now:    func() time.Time { return t0 },
		Plugins: PluginOptions{Environ: func() []string { return c.Environ() }, StartTimeout: 20 * time.Second,
			StopGrace: 300 * time.Millisecond, IndexPoll: 10 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.CreateTopic(ctx, TopicSpec{Title: "C", ID: "c"}); err != nil {
		t.Fatal(err)
	}
	if _, err := k.UpdateTopic(ctx, "c", TopicChanges{KnowledgeBase: plugin("kb")}); err != nil {
		t.Fatal(err)
	}
	return c, k, record
}

// A Source can be a symbolic link, and a file inside a Topic is the
// agent's to make one. A plugin is handed where the file really is, which
// is the file study read, and that is what the command shows: so a plugin
// does not read wherever the link leads a moment later, and whoever runs the
// command sees which file it has a plugin read.
func TestAPluginIsHandedWhereAFileReallyIs(t *testing.T) {
	ctx := context.Background()
	c, k, record := topicWithAPlugin(t)
	book := writeBook(t, filepath.Join(c.Home, "Books", "book.txt"), "The book speaks of elimination.\n")
	secret := writeBook(t, filepath.Join(c.Home, "secret.txt"), "A key of the learner's, of the same length.\n")
	link := filepath.Join(c.StudyHome, "c", "notes", "book.txt")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(book, link); err != nil {
		t.Fatal(err)
	}
	added, err := k.AddSource(ctx, SourceSpec{Topic: "c", File: link})
	if err != nil || added.Source.TopicPath != "notes/book.txt" {
		t.Fatalf("AddSource = %+v, %v", added, err)
	}
	// Once study has read the file, the link comes to lead elsewhere.
	repoint := func(string) {
		if err := os.Remove(link); err != nil {
			t.Error(err)
		}
		if err := os.Symlink(secret, link); err != nil {
			t.Error(err)
		}
	}
	beforeIndexAsked.Store(&repoint)
	t.Cleanup(func() { beforeIndexAsked.Store(nil) })
	res, err := k.IndexSources(ctx, IndexSpec{Topic: "c"})
	beforeIndexAsked.Store(nil)
	if err != nil {
		t.Fatalf("IndexSources: %v", err)
	}
	if got := res.Sources[0]; !got.Asked || got.Path != book || got.State != IndexIndexed {
		t.Errorf("the Source that is a link = %+v; want it indexed from %s, where the file really is", got, book)
	}
	handed := ""
	for _, call := range plugintest.ReadCalls(t, record) {
		if call.Tool == "index" {
			handed = call.Indexed().Path
		}
	}
	if handed != book {
		t.Errorf("the plugin was handed %s, want %s: never the link, which leads to %s by now", handed, book, secret)
	}
	// The link leads elsewhere now: the Source's file has changed, and is
	// not handed over again.
	again, err := k.IndexSources(ctx, IndexSpec{Topic: "c", Again: true})
	if err != nil || again.Sources[0].Asked || !strings.Contains(again.Sources[0].Skipped, "has other content now than was added") {
		t.Errorf("IndexSources once the link leads elsewhere = %+v, %v", again.Sources, err)
	}
}

// study reads a file before it hands it to a plugin, and tells the plugin
// the hash of what it read; the plugin reads the file itself a moment
// later, and says the hash of what it read. A file that changes between the
// two is indexed with content that is not the Source's. study does not call
// that indexed: it says what happened, and the next run asks again.
func TestAFileThatChangesWhileItIsIndexed(t *testing.T) {
	ctx := context.Background()
	c, k, record := topicWithAPlugin(t)
	path := writeBook(t, filepath.Join(c.Home, "Books", "book.txt"), "The first edition speaks of elimination.\n")
	added, err := k.AddSource(ctx, SourceSpec{Topic: "c", File: path})
	if err != nil {
		t.Fatal(err)
	}
	src := added.Source

	// The file changes once study has read it, and before the plugin does.
	change := func(source string) {
		if source != src.ID {
			t.Errorf("study is about to ask for %s", source)
		}
		writeBook(t, path, "The second edition speaks of pivots, at greater length.\n")
	}
	beforeIndexAsked.Store(&change)
	t.Cleanup(func() { beforeIndexAsked.Store(nil) })
	res, err := k.IndexSources(ctx, IndexSpec{Topic: "c"})
	beforeIndexAsked.Store(nil)
	if err != nil {
		t.Fatalf("IndexSources: %v", err)
	}
	want := "Knowledge base plugin kb read other content than the Source's: its file changed while it was indexed. " +
		"Run study source index c " + src.ID + " again"
	if got := res.Sources[0]; !got.Asked || got.State != IndexNotIndexed || got.Reason != want {
		t.Errorf("the Source whose file changed = %+v\nwant not indexed, with %q", got, want)
	}
	// The plugin was told the hash of what study read, and holds another.
	calls := plugintest.ReadCalls(t, record)
	var handed string
	for _, call := range calls {
		if call.Tool == "index" {
			handed = call.Indexed().Hash
		}
	}
	if handed != src.Hash {
		t.Errorf("the plugin was told the hash %s, and the Source's is %s", handed, src.Hash)
	}
	// Whoever asks sees the file as changed, and the Source as not indexed
	// because of it. The command does not hand over the file as it is now,
	// so it is not the next action, in a listing or in status: status names
	// the listing, which says what to do about the file.
	list, err := k.ListSources(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	if got := list.Sources[0]; got.State != SourceChanged || got.Indexed.State != IndexNotIndexed ||
		got.Indexed.Reason != "the file here is no longer the one added" || list.Indexing.Action != "" {
		t.Errorf("ListSources = %+v, index %+v, %+v", got.State, got.Indexed, list.Indexing)
	}
	status, err := k.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if x := status.Topics[0].Indexing; x == nil || len(x.Sources) != 1 || x.Sources[0].State != IndexNotIndexed ||
		x.Sources[0].Reason != "the file here is no longer the one added" || x.Action != "study source list c" {
		t.Errorf("status says of the Topic's indexing %+v; want the Source not indexed for its file, and the listing as the action", x)
	}
	again, err := k.IndexSources(ctx, IndexSpec{Topic: "c"})
	if err != nil || again.Sources[0].Asked || !strings.Contains(again.Sources[0].Skipped, "has other content now than was added") {
		t.Errorf("IndexSources with the file still changed = %+v, %v", again.Sources, err)
	}
	// Once the file is the one that was added again, it is indexed: the
	// plugin is asked with the Source's hash, which is not what it holds.
	if err := os.WriteFile(path, []byte("The first edition speaks of elimination.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	restored, err := k.IndexSources(ctx, IndexSpec{Topic: "c"})
	if err != nil || !restored.Sources[0].Asked || restored.Sources[0].State != IndexIndexed || restored.Sources[0].Reason != "" {
		t.Errorf("IndexSources with the file restored = %+v, %v", restored.Sources, err)
	}
	if list, err = k.ListSources(ctx, "c"); err != nil || list.Sources[0].Indexed.State != IndexIndexed || list.Indexing.Action != "" {
		t.Errorf("ListSources afterwards = %+v, %+v, %v", list.Sources[0].Indexed, list.Indexing, err)
	}
}
