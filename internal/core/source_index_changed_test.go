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
func topicWithAPlugin(t *testing.T, flags ...string) (c *plugintest.Computer, k *Core, record string) {
	t.Helper()
	ctx := context.Background()
	c = plugintest.NewComputer(t)
	record = filepath.Join(t.TempDir(), "record.json")
	c.Register(t, "kb", plugintest.Command(append([]string{"--record", record}, flags...)...)...)
	k, err := Open(Options{
		Getenv: c.Getenv,
		Dir:    c.Home,
		Now:    func() time.Time { return t0 },
		// Time enough on a busy computer: nothing here is about a plugin
		// that is late.
		Plugins: PluginOptions{Environ: func() []string { return c.Environ() }, StartTimeout: 20 * time.Second,
			GlanceTimeout: 20 * time.Second, StopGrace: 300 * time.Millisecond, IndexPoll: 10 * time.Millisecond},
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

// A plain file inside a Topic is the agent's to exchange for a link to any
// file of the learner's, once study has read it and before the plugin does.
// The plugin is told the hash of what study read, and keeps nothing of a
// file that is another: so the learner's file does not come to be in the
// index, for a search to give the agent.
func TestAFileExchangedForALinkIsNotIndexed(t *testing.T) {
	ctx := context.Background()
	c, k, _ := topicWithAPlugin(t)
	const private = "PRIVATE KEY the learner never showed anyone"
	secret := writeBook(t, filepath.Join(c.Home, "secret", "id_key"), private+"\n")
	notes := writeBook(t, filepath.Join(c.StudyHome, "c", "refs", "notes.txt"), "Notes on elimination, in a file of the Topic.\n")
	added, err := k.AddSource(ctx, SourceSpec{Topic: "c", File: notes})
	if err != nil {
		t.Fatal(err)
	}
	exchange := func(string) {
		if err := os.Remove(notes); err != nil {
			t.Error(err)
		}
		if err := os.Symlink(secret, notes); err != nil {
			t.Error(err)
		}
	}
	beforeIndexAsked.Store(&exchange)
	t.Cleanup(func() { beforeIndexAsked.Store(nil) })
	res, err := k.IndexSources(ctx, IndexSpec{Topic: "c"})
	beforeIndexAsked.Store(nil)
	if err != nil {
		t.Fatalf("IndexSources: %v", err)
	}
	if got := res.Sources[0]; got.ID != added.Source.ID || got.State != IndexFailed || !strings.Contains(got.Reason, "the file changed since study read it") {
		t.Errorf("the Source whose file was exchanged = %+v; want it failed, since the file is not the one study read", got)
	}
	// Nothing of the learner's file is in the plugin's folder.
	err = filepath.WalkDir(c.DataFolder("kb"), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(data), "PRIVATE KEY") {
			t.Errorf("the plugin's index holds the learner's file, in %s", path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// study reads a file just before it hands it to a plugin, and tells the
// plugin the hash of what it read; the plugin reads the file itself a moment
// later, and keeps nothing of a file that hashes to anything else. So a file
// that changes between the two is not indexed: the Source has failed, with
// the plugin's reason, and its file is no longer the one that was added.
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
	if got := res.Sources[0]; !got.Asked || got.State != IndexFailed || got.Reason != "the file changed since study read it" {
		t.Errorf("the Source whose file changed = %+v\nwant it failed, with the plugin's reason", got)
	}
	// The plugin was told the hash of what study read.
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
	// Whoever asks sees the file as changed, and the Source as failed. The
	// command does not hand over the file as it is now, so it is not the
	// next action, in a listing or in status: status names the listing,
	// which says what to do about the file.
	list, err := k.ListSources(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	if got := list.Sources[0]; got.State != SourceChanged || got.Indexed.State != IndexFailed || list.Indexing.Action != "" {
		t.Errorf("ListSources = %+v, index %+v, %+v", got.State, got.Indexed, list.Indexing)
	}
	// The plugin's reason stays, and no command is offered to try again:
	// the file as it is now would be skipped.
	if got := list.Sources[0].Indexed; got.Reason != "the file changed since study read it" || got.Retry != "" {
		t.Errorf("the failed Source whose file has changed: %+v; want the plugin's reason, and nothing to run", got)
	}
	status, err := k.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if x := status.Topics[0].Indexing; x == nil || len(x.Sources) != 1 || x.Sources[0].State != IndexFailed || x.Action != "study source list c" {
		t.Errorf("status says of the Topic's indexing %+v; want the Source failed, and the listing as the action", x)
	}
	again, err := k.IndexSources(ctx, IndexSpec{Topic: "c"})
	if err != nil || again.Sources[0].Asked || !strings.Contains(again.Sources[0].Skipped, "has other content now than was added") {
		t.Errorf("IndexSources with the file still changed = %+v, %v", again.Sources, err)
	}
	// Once the file is the one that was added again, it is indexed.
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

// A plugin that breaks the contract, and indexes whatever it reads, is not
// believed: it says the hash of what it read, which is not the Source's, so
// study does not call the Source indexed, and says what happened.
func TestAPluginThatIndexesAnotherFileIsNotBelieved(t *testing.T) {
	ctx := context.Background()
	c, k, _ := topicWithAPlugin(t, "--break", plugintest.BreakIndexesWhatItReads)
	path := writeBook(t, filepath.Join(c.Home, "Books", "book.txt"), "The first edition speaks of elimination.\n")
	added, err := k.AddSource(ctx, SourceSpec{Topic: "c", File: path})
	if err != nil {
		t.Fatal(err)
	}
	src := added.Source
	change := func(string) { writeBook(t, path, "The second edition speaks of pivots, at greater length.\n") }
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
	// With the file as it was added again, whoever asks is told that the
	// plugin holds other content, and the command is the next action.
	if err := os.WriteFile(path, []byte("The first edition speaks of elimination.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	list, err := k.ListSources(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	if got := list.Sources[0]; got.State != SourceOK || got.Indexed.State != IndexNotIndexed ||
		!strings.Contains(got.Indexed.Reason, "Knowledge base plugin kb holds other content for it than the Source's") ||
		list.Indexing.Action != "study source index c" {
		t.Errorf("ListSources = %+v, index %+v, %+v", got.State, got.Indexed, list.Indexing)
	}
}
