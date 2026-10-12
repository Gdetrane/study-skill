package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
)

// lastEvent returns the last Event of a Topic's History, as it is written.
func lastEvent(t *testing.T, dir string) event {
	t.Helper()
	lines := historyLines(t, dir)
	var ev event
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &ev); err != nil {
		t.Fatal(err)
	}
	return ev
}

func plugin(name string) *KnowledgeBase {
	return &KnowledgeBase{Kind: KnowledgeBasePlugin, Plugin: name}
}

// A Topic names its Knowledge base plugin, and goes back to none. A name is
// recorded by an Event type of its own; none by the one that always
// recorded a kind, with the payload it always had.
func TestATopicNamesItsKnowledgeBasePlugin(t *testing.T) {
	m := newTopic(t)
	dir := filepath.Join(m.home, "c")
	none := &KnowledgeBase{Kind: KnowledgeBaseNone}

	res := m.update(t, TopicChanges{KnowledgeBase: plugin("shelf")})
	if !res.Changed || res.Topic.KnowledgeBase == nil || *res.Topic.KnowledgeBase != *plugin("shelf") {
		t.Fatalf("naming a plugin = %+v", res)
	}
	if ev := lastEvent(t, dir); ev.Type != "knowledge_base_plugin.set" || string(ev.Data) != `{"plugin":"shelf"}` ||
		len(ev.Items) != 1 || ev.Items[0].Item != topicFile {
		t.Errorf("the Event = %s %s, items %+v; want knowledge_base_plugin.set with the name alone", ev.Type, ev.Data, ev.Items)
	}
	data, err := os.ReadFile(filepath.Join(dir, topicFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "[knowledge_base]\n  kind = \"plugin\"\n  plugin = \"shelf\"\n") {
		t.Errorf("topic.toml:\n%s", data)
	}

	// Naming the plugin it has records nothing.
	before := historyLines(t, dir)
	if again := m.update(t, TopicChanges{KnowledgeBase: plugin("shelf")}); again.Changed {
		t.Error("naming the same plugin again recorded a change")
	}
	if !slices.Equal(historyLines(t, dir), before) {
		t.Error("naming the same plugin again recorded an Event")
	}

	// Another plugin is another Event of the same type.
	if res := m.update(t, TopicChanges{KnowledgeBase: plugin("notes-kb")}); !res.Changed || res.Topic.KnowledgeBase.Plugin != "notes-kb" {
		t.Errorf("naming another plugin = %+v", res)
	}
	if ev := lastEvent(t, dir); ev.Type != eventKnowledgeBasePluginSet || string(ev.Data) != `{"plugin":"notes-kb"}` {
		t.Errorf("the Event = %s %s", ev.Type, ev.Data)
	}

	// Back to none: the Event that always recorded a kind, with a kind and
	// nothing else, and a table without the plugin.
	if res := m.update(t, TopicChanges{KnowledgeBase: none}); !res.Changed || *res.Topic.KnowledgeBase != *none {
		t.Errorf("going back to none = %+v", res)
	}
	if ev := lastEvent(t, dir); ev.Type != "knowledge_base.set" || string(ev.Data) != `{"kind":"none"}` {
		t.Errorf("the Event = %s %s; want knowledge_base.set with the kind alone", ev.Type, ev.Data)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, topicFile)); strings.Contains(string(data), "plugin") ||
		!strings.Contains(string(data), "[knowledge_base]\n  kind = \"none\"\n") {
		t.Errorf("topic.toml after going back to none:\n%s", data)
	}
	// Every change went from the version the one before it left.
	checkChain(t, dir)
}

// The Knowledge base a Topic can be given is none, or a plugin with a name a
// plugin can be registered under. Nothing else is recorded: no command line
// and no address has a place in it.
func TestTheKnowledgeBaseATopicCanBeGiven(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	dir := filepath.Join(m.home, "c")
	before := historyLines(t, dir)
	for _, kb := range []KnowledgeBase{
		{Kind: KnowledgeBasePlugin},                                 // no name
		{Kind: KnowledgeBasePlugin, Plugin: "none"},                 // a kind is no name
		{Kind: KnowledgeBasePlugin, Plugin: "plugin"},               // nor is this one
		{Kind: KnowledgeBasePlugin, Plugin: "My Shelf"},             // not the shape of a name
		{Kind: KnowledgeBasePlugin, Plugin: "/usr/bin/study-shelf"}, // a program
		{Kind: KnowledgeBasePlugin, Plugin: "study-shelf --stdio"},  // a command line
		{Kind: KnowledgeBasePlugin, Plugin: "http://localhost:8765/mcp"},
		{Kind: KnowledgeBasePlugin, Plugin: "../shelf"},
		{Kind: KnowledgeBasePlugin, Plugin: "shelf\x1b[2J"},
		{Kind: KnowledgeBasePlugin, Plugin: "shelf\n"},
		{Kind: KnowledgeBasePlugin, Plugin: " shelf"},
		{Kind: KnowledgeBasePlugin, Plugin: strings.Repeat("a", 65)},
		{Kind: KnowledgeBaseNone, Plugin: "shelf"}, // none has no plugin
		{Kind: "shelf"}, // a name is no kind
		{Kind: "Plugin", Plugin: "shelf"},
		{Plugin: "shelf"},
	} {
		if _, err := m.UpdateTopic(ctx, "c", TopicChanges{KnowledgeBase: &kb}); CodeOf(err) != CodeInvalidArgument {
			t.Errorf("%+v: err = %v, want invalid_argument", kb, err)
		} else if strings.ContainsAny(err.Error(), "\x1b\n") {
			t.Errorf("%+v: the error repeats text a terminal would act on: %q", kb, err.Error())
		}
	}
	if !slices.Equal(historyLines(t, dir), before) {
		t.Error("a Knowledge base that was refused recorded an Event")
	}
	// The longest name there is, and the shortest.
	for _, name := range []string{strings.Repeat("a", 64), "k", "kb-2"} {
		if res := m.update(t, TopicChanges{KnowledgeBase: plugin(name)}); !res.Changed || res.Topic.KnowledgeBase.Plugin != name {
			t.Errorf("naming %s = %+v", name, res)
		}
	}
}

// A dry run names no plugin, and says what the real run then does.
func TestNamingAPluginDryRun(t *testing.T) {
	m := newTopic(t)
	dir := filepath.Join(m.home, "c")
	before := historyLines(t, dir)
	dry := m.update(t, TopicChanges{KnowledgeBase: plugin("shelf"), DryRun: true})
	if !dry.Changed || !dry.DryRun || dry.Topic.KnowledgeBase == nil || *dry.Topic.KnowledgeBase != *plugin("shelf") {
		t.Errorf("dry run = %+v", dry)
	}
	if !slices.Equal(historyLines(t, dir), before) || knowledgeBaseOf(readSettings(t, dir)) != nil {
		t.Error("a dry run recorded the plugin")
	}
	real := m.update(t, TopicChanges{KnowledgeBase: plugin("shelf")})
	if real.Changed != dry.Changed || *real.Topic.KnowledgeBase != *dry.Topic.KnowledgeBase || real.Note != dry.Note {
		t.Errorf("the dry run said %+v (%q), and the real run did %+v (%q)", dry.Topic.KnowledgeBase, dry.Note,
			real.Topic.KnowledgeBase, real.Note)
	}
	// Once it is named, both say that nothing changes.
	if dry := m.update(t, TopicChanges{KnowledgeBase: plugin("shelf"), DryRun: true}); dry.Changed {
		t.Error("a dry run of naming the plugin the Topic has says it changes something")
	}
}

// A crash at any point of naming a plugin, or of going back to none, is
// finished by the next write, and a dry run agrees with the real run in the
// meantime. So is a crash while recovery finishes it.
func TestCrashesWhileNamingAPlugin(t *testing.T) {
	ctx := context.Background()
	type op struct {
		name  string
		setup func(t *testing.T, m *machine)
		want  KnowledgeBase
	}
	ops := []op{
		{"knowledge_base_plugin.set", func(*testing.T, *machine) {}, *plugin("shelf")},
		{"knowledge_base_plugin.set over another plugin", func(t *testing.T, m *machine) {
			m.update(t, TopicChanges{KnowledgeBase: plugin("notes-kb")})
		}, *plugin("shelf")},
		{"knowledge_base.set over a plugin", func(t *testing.T, m *machine) {
			m.update(t, TopicChanges{KnowledgeBase: plugin("shelf")})
		}, KnowledgeBase{Kind: KnowledgeBaseNone}},
	}
	run := func(m *machine, want KnowledgeBase, dry bool) (bool, error) {
		res, err := m.UpdateTopic(ctx, "c", TopicChanges{KnowledgeBase: &want, DryRun: dry})
		return res.Changed, err
	}
	check := func(t *testing.T, m *machine, want KnowledgeBase) {
		t.Helper()
		dir := filepath.Join(m.home, "c")
		if kb := knowledgeBaseOf(readSettings(t, dir)); kb == nil || *kb != want {
			t.Errorf("Knowledge base = %+v, want %+v", kb, want)
		}
		if hasIntentFile(t, m) {
			t.Error("the intent marker survived the next write")
		}
		if s := replayFolder(t, dir); len(s.flags) != 0 {
			t.Errorf("flags = %+v", s.flags)
		}
		checkChain(t, dir)
	}
	for _, o := range ops {
		for _, point := range []string{crashAfterIntent, crashAfterEvent, crashAfterItem, crashBeforeClear} {
			t.Run(o.name+"/"+point, func(t *testing.T) {
				m := newTopic(t)
				o.setup(t, m)
				m.crash = crashOnce(point)
				if _, err := run(m, o.want, false); err != nil && !errors.Is(err, errCrash) {
					t.Fatalf("the write failed: %v", err)
				}
				m.crash = nil
				dryChanged, dryErr := run(m, o.want, true)
				realChanged, realErr := run(m, o.want, false)
				if CodeOf(dryErr) != CodeOf(realErr) || (dryErr == nil) != (realErr == nil) || dryChanged != realChanged {
					t.Errorf("dry run (%v, %v) disagrees with the real run (%v, %v)", dryChanged, dryErr, realChanged, realErr)
				}
				check(t, m, o.want)
			})
		}
		// The write is interrupted once the Event is in the History, and
		// recovery is interrupted too, at each of its own points.
		for _, point := range []string{crashRecoveryAfterItem, crashRecoveryBeforeClear} {
			t.Run(o.name+"/"+point, func(t *testing.T) {
				m := newTopic(t)
				o.setup(t, m)
				m.crash = crashOnce(crashAfterEvent)
				if _, err := run(m, o.want, false); !errors.Is(err, errCrash) {
					t.Fatalf("err = %v, want the crash", err)
				}
				m.crash = crashOnce(point)
				if _, err := m.UpdateTopic(ctx, "c", TopicChanges{Goal: ptr("G")}); !errors.Is(err, errCrash) {
					t.Fatalf("err = %v, want the crash during recovery", err)
				}
				if !hasIntentFile(t, m) {
					t.Fatal("a crash during recovery cleared the marker")
				}
				m.crash = nil
				m.update(t, TopicChanges{Goal: ptr("G")})
				check(t, m, o.want)
			})
		}
	}
}

// What the Events of each type hold, as they were and are written, replays
// without a flag: a History with a kind set, then a plugin named, then the
// kind set again.
func TestBothKnowledgeBaseEventsReplay(t *testing.T) {
	m := newTopic(t)
	dir := filepath.Join(m.home, "c")
	m.update(t, TopicChanges{KnowledgeBase: &KnowledgeBase{Kind: KnowledgeBaseNone}})
	m.update(t, TopicChanges{KnowledgeBase: plugin("shelf")})
	m.update(t, TopicChanges{Title: ptr("Systems C")})
	m.update(t, TopicChanges{KnowledgeBase: &KnowledgeBase{Kind: KnowledgeBaseNone}})
	s := checkChain(t, dir)
	var types []string
	for _, ev := range s.applied {
		types = append(types, ev.Type)
	}
	if want := []string{eventTopicCreated, eventKnowledgeBaseSet, eventKnowledgeBasePluginSet, eventTopicUpdated, eventKnowledgeBaseSet}; !slices.Equal(types, want) {
		t.Errorf("the History replays as %v, want %v", types, want)
	}
	// The History shows the name, which is a word, and never any other text
	// a payload holds in its place.
	view, err := m.HistoryOf(context.Background(), "c", HistoryQuery{Type: "knowledge_base"})
	if err != nil || len(view.Entries) != 3 {
		t.Fatalf("HistoryOf = %+v, %v", view, err)
	}
	if got := view.Entries[1].Summary; got != "Knowledge base plugin set: shelf" {
		t.Errorf("the summary of naming a plugin = %q", got)
	}
}

// An Event of the type that records a kind is read as a kind and nothing
// else, whatever its payload holds: finishing one that was interrupted
// writes the kind alone, as it always did, and never a name beside it.
func TestAKindEventNeverNamesAPlugin(t *testing.T) {
	m := newTopic(t)
	dir := filepath.Join(m.home, "c")
	data, err := os.ReadFile(filepath.Join(dir, topicFile))
	if err != nil {
		t.Fatal(err)
	}
	before := contentHash(data, true)
	settings := readSettings(t, dir)
	settings.extra = map[string]any{"knowledge_base": map[string]any{"kind": "plugin"}}
	after, err := encodeTopicSettings(settings)
	if err != nil {
		t.Fatal(err)
	}
	appendLine(t, filepath.Join(dir, historyFile), `{"format":1,"id":"other01","time":"2026-10-02T09:30:00Z",`+
		`"wall":"2026-10-02T09:30:00Z","type":"knowledge_base.set","data":{"kind":"plugin","plugin":"shelf"},`+
		`"items":[{"item":"topic.toml","before":"`+before+`","after":"`+contentHash(after, true)+`"}]}`+"\n")
	m.leaveIntent(t, "other01", eventKnowledgeBaseSet)

	m.update(t, TopicChanges{Goal: ptr("G")}) // the next write finishes the interrupted one
	got, _ := os.ReadFile(filepath.Join(dir, topicFile))
	if strings.Contains(string(got), "shelf") || !strings.Contains(string(got), `kind = "plugin"`) {
		t.Errorf("topic.toml after recovery:\n%s", got)
	}
	// A kind plugin without a name names no plugin.
	topic, err := m.readTopic("c")
	if kb := topic.KnowledgeBase; err != nil || kb == nil || kb.Kind != KnowledgeBasePlugin || kb.Plugin != "" {
		t.Errorf("the Topic's Knowledge base = %+v, %v", topic.KnowledgeBase, err)
	}
	if name, ok := pluginOf(topic.KnowledgeBase); ok {
		t.Errorf("a kind without a name names the plugin %q", name)
	}
	checkChain(t, dir)
}

// An Event that names something no plugin can be called is not one this
// version wrote: recovery stops at it and keeps it, and writes no such name
// into topic.toml.
func TestAPluginEventWithNoNameIsNotApplied(t *testing.T) {
	ctx := context.Background()
	for _, payload := range []string{`{}`, `{"plugin":""}`, `{"plugin":"/usr/bin/kb"}`, `{"plugin":"none"}`, `{"plugin":["shelf"]}`,
		`{"plugin":"shelf\u001b[2J"}`, `{"kind":"plugin"}`, `"shelf"`} {
		t.Run(payload, func(t *testing.T) {
			m := newTopic(t)
			dir := filepath.Join(m.home, "c")
			data, err := os.ReadFile(filepath.Join(dir, topicFile))
			if err != nil {
				t.Fatal(err)
			}
			appendLine(t, filepath.Join(dir, historyFile), `{"format":1,"id":"other01","time":"2026-10-02T09:30:00Z",`+
				`"wall":"2026-10-02T09:30:00Z","type":"knowledge_base_plugin.set","data":`+payload+`,`+
				`"items":[{"item":"topic.toml","before":"`+contentHash(data, true)+`","after":"sha256:`+strings.Repeat("0", 64)+`"}]}`+"\n")
			m.leaveIntent(t, "other01", eventKnowledgeBasePluginSet)
			if _, err := m.UpdateTopic(ctx, "c", TopicChanges{Goal: ptr("G")}); CodeOf(err) != CodeCorrupt ||
				!strings.Contains(err.Error(), "unreadable payload") {
				t.Errorf("the next write = %v, want corrupt", err)
			}
			if got, _ := os.ReadFile(filepath.Join(dir, topicFile)); string(got) != string(data) {
				t.Errorf("topic.toml changed:\n%s", got)
			}
			if !m.hasIntent(t) {
				t.Error("the Event was not kept to be finished")
			}
		})
	}
}

// topic.toml syncs between computers and can be written by hand, so what it
// gives as a plugin's name is any text. A name no plugin can have names no
// plugin, and is shown safe to print and no longer than a name.
func TestAPluginNameWrittenByHand(t *testing.T) {
	m := newTopic(t)
	dir := filepath.Join(m.home, "c")
	data, err := os.ReadFile(filepath.Join(dir, topicFile))
	if err != nil {
		t.Fatal(err)
	}
	for written, shown := range map[string]string{
		`"shelf"`:                            "shelf",
		`"../../bin/sh"`:                     "../../bin/sh",
		`"shelf\u001b[2J\u202e"`:             "shelf [2J",
		`"` + strings.Repeat("x", 500) + `"`: strings.Repeat("x", 63) + "…",
		`42`:                                 "",
		`["shelf"]`:                          "",
	} {
		table := "[knowledge_base]\n  kind = \"plugin\"\n  plugin = " + written + "\n"
		if err := os.WriteFile(filepath.Join(dir, topicFile), append(slices.Clone(data), table...), 0o644); err != nil {
			t.Fatal(err)
		}
		topic, err := m.readTopic("c")
		if err != nil || topic.KnowledgeBase == nil || topic.KnowledgeBase.Kind != KnowledgeBasePlugin || topic.KnowledgeBase.Plugin != shown {
			t.Errorf("plugin = %s: the Topic's Knowledge base = %+v, %v; want the plugin shown as %q", written, topic.KnowledgeBase, err, shown)
		}
		if name, ok := pluginOf(topic.KnowledgeBase); ok != (written == `"shelf"`) {
			t.Errorf("plugin = %s names the plugin %q: %v", written, name, ok)
		}
	}
}

// The name study gives a plugin for a Topic's collection is always one the
// contract takes, whatever the Topic is called; the same for the same Study
// home and Topic; and another for any other Study home or Topic.
func TestCollectionNames(t *testing.T) {
	valid := func(name string) {
		t.Helper()
		if err := plugincontract.CheckCollection(name); err != nil {
			t.Fatalf("the collection %q is not one the contract takes: %v", name, err)
		}
	}
	longest := strings.Repeat("a", 64)
	for _, topic := range []string{"c", "linear-algebra", longest, strings.Repeat("a-", 31) + "aa", strings.Repeat("ab-", 21) + "a",
		// A folder of the Study home is a Topic whatever it is called.
		"My Topic", "UPPER", "a--b", "-a", "a-", "naïve", "..", "a/b", "", strings.Repeat("x", 300), "topic\x00", "\x1b[2J"} {
		name := collectionName("/home/ada/study", topic)
		valid(name)
		if name != collectionName("/home/ada/study", topic) {
			t.Errorf("%q gives two names", topic)
		}
		if validateTopicID(topic) == nil && len(topic) <= 31 && !strings.HasPrefix(name, topic+"-") {
			t.Errorf("the collection of %q is %q, want it to begin with the Topic's id", topic, name)
		}
	}
	if got := collectionName("/home/ada/study", "linear-algebra"); len(got) != len("linear-algebra")+1+32 {
		t.Errorf("the collection of linear-algebra is %q, want the id, a hyphen and 32 characters", got)
	}
	if got := collectionName("/home/ada/study", longest); len(got) != 64 || !strings.HasPrefix(got, strings.Repeat("a", 31)+"-") {
		t.Errorf("the collection of the longest id is %q", got)
	}

	// No two of these share a name: Topics whose ids are the same as far as
	// they are kept, Topics of two Study homes, and a Study home and a Topic
	// whose names run into each other.
	seen := map[string]string{}
	distinct := func(home, topic string) {
		t.Helper()
		name := collectionName(home, topic)
		valid(name)
		pair := home + " and " + topic
		if other, taken := seen[name]; taken && other != pair {
			t.Errorf("%s and %s share the collection %s", other, pair, name)
		}
		seen[name] = pair
	}
	for _, home := range []string{"/home/ada/study", "/home/ada/study2", "/home/grace/study", "/mnt/usb/study", "/home/ada/study/"} {
		for _, topic := range []string{"linear-algebra", "c", longest, longest[:63] + "b", longest[:40], longest[:31], longest[:32],
			strings.Repeat("a", 30) + "-b", strings.Repeat("a", 30) + "-c", "My Topic", "my-topic", "topic"} {
			distinct(home, topic)
		}
	}
	distinct("/home/ada", "study")
	distinct("/home/ada/stud", "y")
	// A Topic named after another Topic's collection is not that
	// collection.
	other := collectionName("/home/ada/study", "c")
	if got := collectionName("/home/ada/study", other); got == other {
		t.Errorf("a Topic called %s shares the collection of c", other)
	}
	// Many at random, of every length an id can have.
	rng := rand.New(rand.NewPCG(1, 2))
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	for i := 0; i < 5000; i++ {
		id := make([]byte, 1+rng.IntN(64))
		for j := range id {
			id[j] = letters[rng.IntN(len(letters))]
			if j > 0 && j < len(id)-1 && id[j-1] != '-' && rng.IntN(6) == 0 {
				id[j] = '-'
			}
		}
		if validateTopicID(string(id)) != nil {
			t.Fatalf("the test made %q, which is no Topic id", id)
		}
		distinct(fmt.Sprintf("/home/learner%d/study", rng.IntN(3)), string(id))
	}
}

// The collection is named after where the Study home really is: the same
// through a link to it, and another once it is moved.
func TestTheCollectionFollowsTheStudyHome(t *testing.T) {
	base := t.TempDir()
	if real, err := filepath.EvalSymlinks(base); err == nil {
		base = real
	}
	home := filepath.Join(base, "study")
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(home, link); err != nil {
		t.Fatal(err)
	}
	at := func(studyHome string) string {
		t.Helper()
		got, err := newMachine(t, studyHome, "id", t0).realHome()
		if err != nil {
			t.Fatal(err)
		}
		return collectionName(got, "c")
	}
	direct := at(home)
	if through := at(link); through != direct {
		t.Errorf("through a link the collection is %s, and directly %s", through, direct)
	}
	moved := filepath.Join(base, "moved")
	if err := os.Rename(home, moved); err != nil {
		t.Fatal(err)
	}
	if after := at(moved); after == direct {
		t.Errorf("a Study home that was moved keeps the collection %s", after)
	}
}
