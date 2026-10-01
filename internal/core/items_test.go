package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// Tests give items inside a file their own Event type and file, so the
// mechanism is proved before Cards (#26) use it for cards.jsonl.
const (
	entriesFile     = "entries.jsonl"
	eventEntriesSet = "test.entries.set"
	entryItemPrefix = entriesFile + "#"
)

func init() {
	fileCodecs = append(fileCodecs, codecEntry{pattern: entriesFile, codec: jsonlCodec})
	eventKinds[eventEntriesSet] = eventKind{apply: applyEntriesSet, replay: replayTopicUpdated}
}

// entriesSetData sets entries by key; a null entry removes it.
type entriesSetData map[string]json.RawMessage

func applyEntriesSet(ev event, item string, _ []byte, _ bool) ([]byte, bool, error) {
	var d entriesSetData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return nil, false, corruptf("Event %s has an unreadable payload: %v", ev.ID, err)
	}
	key, ok := strings.CutPrefix(item, entryItemPrefix)
	if !ok {
		return nil, false, corruptf("Event %s (%s) cannot edit %s", ev.ID, ev.Type, item)
	}
	entry, ok := d[key]
	if !ok {
		return nil, false, corruptf("Event %s (%s) does not set %s", ev.ID, ev.Type, key)
	}
	if string(entry) == "null" {
		return nil, false, nil
	}
	return entry, true, nil
}

// setEntries writes entries to Topic c, one item per entry.
func (m *machine) setEntries(t *testing.T, entries map[string]string) error {
	t.Helper()
	d := entriesSetData{}
	var items []string
	for key, entry := range entries {
		d[key] = json.RawMessage(entry)
		items = append(items, entryItemPrefix+key)
	}
	slices.Sort(items)
	_, err := m.writeTopic(context.Background(), "c", func(*replayed, *topicView) (*change, error) {
		return &change{Type: eventEntriesSet, Data: d, Items: items}, nil
	}, false)
	return err
}

func entry(key, text string) string { return fmt.Sprintf(`{"id":%q,"text":%q}`, key, text) }

func readEntries(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, entriesFile))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestEntriesInOneFileAreSeparateItems: two machines each adding a different
// entry to one file is not a conflict; editing one entry on both is.
func TestEntriesInOneFileAreSeparateItems(t *testing.T) {
	a := newMachine(t, t.TempDir(), "a", t0)
	if _, err := a.CreateTopic(context.Background(), TopicSpec{Title: "C", ID: "c"}); err != nil {
		t.Fatal(err)
	}
	b := newMachine(t, t.TempDir(), "b", t0.Add(time.Minute))
	syncTopic(t, a, b)

	if err := a.setEntries(t, map[string]string{"x": entry("x", "from A")}); err != nil {
		t.Fatal(err)
	}
	if err := b.setEntries(t, map[string]string{"y": entry("y", "from B")}); err != nil {
		t.Fatal(err)
	}
	ours, theirs := historyLines(t, filepath.Join(a.home, "c")), historyLines(t, filepath.Join(b.home, "c"))
	aIntoB := summarize(replayLines(t, unionMerge(theirs, ours)))
	bIntoA := summarize(replayLines(t, unionMerge(ours, theirs)))
	if !reflect.DeepEqual(aIntoB, bIntoA) || len(aIntoB.Flags) != 0 {
		t.Fatalf("adding different entries on two machines:\n A into B: %+v\n B into A: %+v", aIntoB, bIntoA)
	}

	// Both machines now edit entry x from the version A wrote.
	if err := os.WriteFile(filepath.Join(b.home, "c", entriesFile), []byte(readEntries(t, filepath.Join(a.home, "c"))), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.setEntries(t, map[string]string{"x": entry("x", "edited on A")}); err != nil {
		t.Fatal(err)
	}
	if err := b.setEntries(t, map[string]string{"x": entry("x", "edited on B")}); err != nil {
		t.Fatal(err)
	}
	ours, theirs = historyLines(t, filepath.Join(a.home, "c")), historyLines(t, filepath.Join(b.home, "c"))
	merged := summarize(replayLines(t, unionMerge(ours, theirs)))
	if len(merged.Flags) != 1 || merged.Flags[0].Kind != FlagConflict || merged.Flags[0].Item != entryItemPrefix+"x" {
		t.Errorf("flags = %+v, want one conflict on %sx", merged.Flags, entryItemPrefix)
	}
}

// TestAnEntryIsHashedInItsCanonicalForm: reformatting an entry, or editing
// another one, does not change its version.
func TestAnEntryIsHashedInItsCanonicalForm(t *testing.T) {
	m := newTopic(t)
	dir := filepath.Join(m.home, "c")
	if err := m.setEntries(t, map[string]string{"x": entry("x", "one"), "y": entry("y", "two")}); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	read := func() string {
		data, exists, err := readItem(root, entryItemPrefix+"x")
		if err != nil || !exists {
			t.Fatalf("readItem: %v, exists %v", err, exists)
		}
		return contentHash(data, exists)
	}
	before := read()
	reformatted := `{ "id" : "x",   "text": "one" }` + "\n" + `{"id":"y","text":"edited by hand"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, entriesFile), []byte(reformatted), 0o644); err != nil {
		t.Fatal(err)
	}
	if after := read(); after != before {
		t.Error("reformatting an entry, or editing another, changed its version")
	}
	if _, exists, err := readItem(root, entryItemPrefix+"z"); err != nil || exists {
		t.Errorf("a missing entry: exists %v, err %v", exists, err)
	}
	if err := os.WriteFile(filepath.Join(dir, entriesFile), []byte("not json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readItem(root, entryItemPrefix+"x"); CodeOf(err) != CodeCorrupt {
		t.Errorf("an unreadable file: err = %v, want corrupt", err)
	}
}

// TestAMultiItemEventInterruptedPartWayIsFinished: a crash after the first
// of two items leaves the second for recovery, which finishes it.
func TestAMultiItemEventInterruptedPartWayIsFinished(t *testing.T) {
	m := newTopic(t)
	dir := filepath.Join(m.home, "c")
	m.crash = crashOnce(crashAfterItem)
	err := m.setEntries(t, map[string]string{"x": entry("x", "one"), "y": entry("y", "two")})
	if !errors.Is(err, errCrash) {
		t.Fatalf("err = %v, want the crash", err)
	}
	if got := readEntries(t, dir); got != entry("x", "one")+"\n" {
		t.Fatalf("after the crash, %s = %q: want only the first entry", entriesFile, got)
	}
	m.crash = nil
	if err := m.setEntries(t, map[string]string{"z": entry("z", "three")}); err != nil {
		t.Fatal(err)
	}
	want := entry("x", "one") + "\n" + entry("y", "two") + "\n" + entry("z", "three") + "\n"
	if got := readEntries(t, dir); got != want {
		t.Errorf("%s = %q, want %q", entriesFile, got, want)
	}
	if s := replayFolder(t, dir); len(s.flags) != 0 {
		t.Errorf("flags: %+v", s.flags)
	}

	// Removing an entry removes its line and keeps the others byte for byte.
	if err := m.setEntries(t, map[string]string{"y": "null"}); err != nil {
		t.Fatal(err)
	}
	if got := readEntries(t, dir); got != entry("x", "one")+"\n"+entry("z", "three")+"\n" {
		t.Errorf("after removing y: %q", got)
	}
}

func TestItemNamesAreChecked(t *testing.T) {
	for _, item := range []string{
		"", ".", "../outside", "/etc/passwd", "lessons/../../x", ".git/config", "sub/.git/hooks/pre-commit",
		`lessons\x.md`, "entries.jsonl#", "entries.jsonl#a#b", "topic.toml#key",
	} {
		if _, _, err := parseItem(item); CodeOf(err) != CodeCorrupt {
			t.Errorf("parseItem(%q): err = %v, want corrupt", item, err)
		}
	}
	for _, item := range []string{"topic.toml", ".gitattributes", "lessons/intro.md", "entries.jsonl#x.1"} {
		if _, _, err := parseItem(item); err != nil {
			t.Errorf("parseItem(%q): %v", item, err)
		}
	}
}
