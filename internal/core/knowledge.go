package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/mordor-forge/lamplight/v2/internal/library"
)

// The Knowledge seam (ADR-0007): a Topic's Knowledge base, its Sources, and
// the Evidence its Lessons cite. The core records them and nothing more: it
// never parses a document and never calls a knowledge service. The agent
// reads the Sources itself and records the exact quotes it relied on.
//
// The History is the single source of truth for which Sources exist and
// what they are. sources.jsonl is the readable copy Lamplight writes, one
// line per Source. Neither holds anything that differs between machines:
// where a file is on this computer is local state (see sourcelocal.go).

const (
	sourcesFile = "sources.jsonl"

	eventKnowledgeBaseSet = "knowledge_base.set"
	eventSourceAdded      = "source.added"
	eventSourceUpdated    = "source.updated"

	maxURLRunes  = 2000 // a Source's URL
	maxPathRunes = 4096 // a file path
)

// Knowledge base kinds. "none" is the only one this version has (ADR-0011).
const (
	// KnowledgeBaseNone: there is no Knowledge base; the agent reads the
	// Sources itself.
	KnowledgeBaseNone = "none"
)

// Source kinds.
const (
	SourceFile = "file"
	SourceURL  = "url"
)

// States of a Source in ListSources.
const (
	// SourceOK: the file is on this computer with the recorded content.
	SourceOK = "ok"
	// SourceChanged: the file where the Source was last found has other
	// content now, and the recorded content is nowhere in the Library.
	SourceChanged = "changed"
	// SourceMissing: the file is not on this computer, as far as Lamplight
	// can tell: record where it is with UpdateSource, or rebuild the Library.
	SourceMissing = "missing"
	// SourceUntracked: a line of sources.jsonl that the History does not
	// know, such as one added by hand. It is not a Source until AddSource
	// records it.
	SourceUntracked = "untracked"
)

func init() {
	fileCodecs = append(fileCodecs, codecEntry{pattern: sourcesFile, codec: jsonlHistoryCodec})
	eventKinds[eventKnowledgeBaseSet] = eventKind{apply: applyKnowledgeBaseSet, replay: replayTopicUpdated}
	eventKinds[eventSourceAdded] = eventKind{apply: applySourceAdded, replay: replaySourceAdded}
	eventKinds[eventSourceUpdated] = eventKind{apply: applySourceUpdated, replay: replaySourceUpdated}
}

// KnowledgeBase is where a Topic's Sources are held and searched for
// Evidence. A Topic has at most one; until one is chosen, it behaves as
// "none".
//
// Kind is shown as topic.toml records it, so it can be one this version
// does not know: one a newer version added, or one an earlier build had.
// Such a Topic behaves as "none" too.
type KnowledgeBase struct {
	Kind string `json:"kind"`
}

// checkKnowledgeBase validates a Knowledge base setting: a kind this version
// knows.
func checkKnowledgeBase(kb KnowledgeBase) (KnowledgeBase, error) {
	switch kind := strings.TrimSpace(kb.Kind); kind {
	case KnowledgeBaseNone:
		return KnowledgeBase{Kind: kind}, nil
	case "":
		return KnowledgeBase{}, invalidf("give the Knowledge base kind: %q is the only one this version has", KnowledgeBaseNone)
	default:
		return KnowledgeBase{}, invalidf("the Knowledge base kind must be %q, the only one this version has, not %q",
			KnowledgeBaseNone, kb.Kind)
	}
}

// knowledgeBaseOf reads the Knowledge base from topic.toml's
// [knowledge_base] table, which lives among the settings parseTopicSettings
// keeps as they are; nil when none was chosen.
func knowledgeBaseOf(settings topicSettings) *KnowledgeBase {
	table, ok := settings.extra["knowledge_base"].(map[string]any)
	if !ok {
		return nil
	}
	kind, _ := table["kind"].(string)
	if kind == "" {
		return nil
	}
	return &KnowledgeBase{Kind: kind}
}

// topicKnowledgeBase reads a Topic's Knowledge base through view.
func topicKnowledgeBase(view *topicView, topicID string) (*KnowledgeBase, error) {
	data, exists, err := view.read(topicFile)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, corruptf("%s of %s is missing", topicFile, topicID)
	}
	settings, err := parseTopicSettings(data, filepath.Join(topicID, topicFile))
	if err != nil {
		return nil, err
	}
	return knowledgeBaseOf(settings), nil
}

// planKnowledgeBase plans setting a Topic's Knowledge base; nothing when it
// is already set so.
func planKnowledgeBase(topicID string, kb KnowledgeBase) plan {
	return func(_ *replayed, view *topicView) (*change, error) {
		current, err := topicKnowledgeBase(view, topicID)
		if err != nil {
			return nil, err
		}
		if current != nil && *current == kb {
			return nil, nil
		}
		return &change{Type: eventKnowledgeBaseSet, Data: kb, Items: []string{topicFile}}, nil
	}
}

// applyKnowledgeBaseSet writes the [knowledge_base] table of topic.toml anew,
// with the Event's kind alone: a table's other keys are the settings of the
// kind that had them. Choosing the kind a Topic has records no Event (see
// planKnowledgeBase), so this version's own Events always change the kind.
// An Event that keeps the kind is an earlier build's, moving a Topic from one
// notebook to another (ADR-0011); when recovery finishes one, the table is
// left without the notebook, at a version of its own.
func applyKnowledgeBaseSet(ev event, item string, current []byte, exists bool) ([]byte, bool, error) {
	if item != topicFile {
		return nil, false, corruptf("Event %s (%s) cannot edit %s", ev.ID, ev.Type, item)
	}
	if !exists {
		return nil, false, corruptf("%s is missing", topicFile)
	}
	var kb KnowledgeBase
	if err := json.Unmarshal(ev.Data, &kb); err != nil || kb.Kind == "" {
		return nil, false, corruptf("Event %s has an unreadable payload", ev.ID)
	}
	settings, err := parseTopicSettings(current, topicFile)
	if err != nil {
		return nil, false, err
	}
	if settings.extra == nil {
		settings.extra = map[string]any{}
	}
	settings.extra["knowledge_base"] = map[string]any{"kind": kb.Kind}
	data, err := encodeTopicSettings(settings)
	return data, err == nil, err
}

// Source is a document or web page a Topic learns from, as the History and
// sources.jsonl record it: only what is the same on every machine.
type Source struct {
	// ID is a slug of the title plus a random suffix, such as
	// "modern-operating-systems.k3f9a2", so it never collides across
	// machines.
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	// TopicPath is a file Source inside the Topic's folder, relative to it,
	// with forward slashes. It is found on every machine the Topic syncs to.
	TopicPath string `json:"topic_path,omitempty"`
	// FileName is the name a file Source outside the Topic had when it was
	// added. Where it is on each computer is local state.
	FileName string `json:"file_name,omitempty"`
	// Hash and Size identify a file Source's content, by which it is found
	// again in the Library on any machine.
	Hash string `json:"hash,omitempty"`
	Size int64  `json:"size_bytes,omitempty"`
	// URL is a web page Source's address.
	URL string `json:"url,omitempty"`
}

// knownSourceFields are the fields of Source; others in a line, written by a
// newer version or by an earlier build, are kept as they are.
var knownSourceFields = []string{"id", "kind", "title", "topic_path", "file_name", "hash", "size_bytes", "url"}

var sourceIDPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*\.[a-z0-9]{1,26}$`)

func sourceItem(id string) string { return sourcesFile + "#" + id }

// SourceSpec describes a Source to add: a file or a URL.
type SourceSpec struct {
	Topic string
	// File is the file's path: absolute, starting with ~/, or relative to
	// the folder study started in.
	File string
	URL  string
	// Title defaults to the Library's title for the file, else one derived
	// from its name, or the URL.
	Title  string
	DryRun bool
}

// SourceChanges describes changes to a Source. Nil fields stay as they are.
type SourceChanges struct {
	Title *string
	// Path records where a file Source is on this computer. The file there
	// must have the recorded content. It is local state, not recorded in the
	// History.
	Path   *string
	DryRun bool
}

// SourceResult is the result of AddSource and UpdateSource.
type SourceResult struct {
	Topic  string `json:"topic"`
	Source Source `json:"source"`
	// Path is where a file Source is on this computer, when known.
	Path string `json:"path,omitempty"`
	// Changed is false when the Source already had the requested values.
	Changed bool `json:"changed"`
	DryRun  bool `json:"dry_run,omitempty"`
}

// sourceUpdatedData is the payload of a source.updated Event: the fields that
// changed, with their new values. An Event of an earlier build can name
// fields this version does not have; they are ignored, so such an Event may
// change nothing.
type sourceUpdatedData struct {
	ID    string  `json:"id"`
	Title *string `json:"title,omitempty"`
	// Source is the Source as the History has it after the change. The
	// changed fields above are what replay applies, so edits made on two
	// machines both survive; Source is what sources.jsonl is written from,
	// so the line matches the History whichever copy a union merge left.
	Source *Source `json:"source,omitempty"`
}

// AddSource registers a Source on a Topic. A file is hashed, never parsed, so
// it can be found again on any machine. Adding a file or URL the Topic
// already has is an error that names the existing Source.
func (c *Core) AddSource(ctx context.Context, spec SourceSpec) (SourceResult, error) {
	if err := checkTopicID(spec.Topic); err != nil {
		return SourceResult{}, err
	}
	title, err := cleanText("title", spec.Title, maxTitleRunes)
	if err != nil {
		return SourceResult{}, err
	}
	var src Source
	var file *foundFile
	slugFrom := title
	switch path, link := strings.TrimSpace(spec.File), strings.TrimSpace(spec.URL); {
	case path != "" && link != "":
		return SourceResult{}, invalidf("a Source is a file or a URL, not both")
	case path != "":
		f, err := c.findSourceFile(ctx, spec.Topic, path)
		if err != nil {
			return SourceResult{}, err
		}
		file = &f
		src = Source{Kind: SourceFile, TopicPath: f.topicPath, Hash: f.hash, Size: f.size}
		if f.topicPath == "" {
			src.FileName = derivedText(filepath.Base(f.path), maxTitleRunes)
		}
		if title == "" {
			title = c.fileTitle(f.path)
		}
	case link != "":
		u, err := cleanURL(link)
		if err != nil {
			return SourceResult{}, err
		}
		src = Source{Kind: SourceURL, URL: u}
		if title == "" {
			title = derivedText(u, maxTitleRunes)
			slugFrom = strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://"), "www.")
		}
	default:
		return SourceResult{}, invalidf("give the Source's file or URL")
	}
	src.Title = title
	if slugFrom == "" {
		slugFrom = title
	}

	_, err = c.writeTopic(ctx, spec.Topic, func(s *replayed, _ *topicView) (*change, error) {
		k := s.knowledge()
		for _, id := range k.order {
			other := k.sources[id]
			if src.Kind == SourceFile && other.Hash == src.Hash || src.Kind == SourceURL && other.URL == src.URL {
				return nil, &Error{Code: CodeAlreadyExists, Message: fmt.Sprintf(
					"Topic %s already has this %s as Source %s", spec.Topic, src.Kind, other.ID)}
			}
		}
		src.ID = c.newSourceID(slugFrom, k)
		return &change{Type: eventSourceAdded, Data: src, Items: []string{sourceItem(src.ID)}}, nil
	}, spec.DryRun)
	if err != nil {
		return SourceResult{}, err
	}
	res := SourceResult{Topic: spec.Topic, Source: src, Changed: true, DryRun: spec.DryRun}
	if file != nil {
		res.Path = file.path
		if !spec.DryRun {
			c.rememberLocation(spec.Topic, src.ID, *file)
		}
	}
	return res, nil
}

// UpdateSource changes a Source's title, recorded in the History, or records
// where its file is on this computer, which is local state only. Asking for
// the values it already has changes nothing.
func (c *Core) UpdateSource(ctx context.Context, topicID, sourceID string, changes SourceChanges) (SourceResult, error) {
	if err := checkTopicID(topicID); err != nil {
		return SourceResult{}, err
	}
	if !sourceIDPattern.MatchString(sourceID) {
		return SourceResult{}, invalidf("%q is not a Source id: study source list shows them", sourceID)
	}
	var want sourceUpdatedData
	if changes.Title != nil {
		title, err := cleanText("title", *changes.Title, maxTitleRunes)
		if err != nil {
			return SourceResult{}, err
		}
		if title == "" {
			return SourceResult{}, invalidf("a Source needs a title")
		}
		want.Title = &title
	}
	var file *foundFile
	if changes.Path != nil {
		f, err := c.findSourceFile(ctx, topicID, *changes.Path)
		if err != nil {
			return SourceResult{}, err
		}
		file = &f
	}
	if want.Title == nil && file == nil {
		return SourceResult{}, invalidf("nothing to change: give a new title or path")
	}

	var src Source
	res := SourceResult{Topic: topicID, DryRun: changes.DryRun}
	if want.Title != nil {
		ev, err := c.writeTopic(ctx, topicID, func(s *replayed, _ *topicView) (*change, error) {
			known, ok := s.knowledge().sources[sourceID]
			if !ok {
				return nil, unknownSource(topicID, sourceID)
			}
			src = *known
			if file != nil {
				if err := checkRelocation(src, *file); err != nil {
					return nil, err
				}
			}
			if *want.Title == src.Title {
				return nil, nil
			}
			diff := sourceUpdatedData{ID: sourceID, Title: want.Title}
			src.Title = *want.Title
			after := src
			diff.Source = &after
			return &change{Type: eventSourceUpdated, Data: diff, Items: []string{sourceItem(sourceID)}}, nil
		}, changes.DryRun)
		if err != nil {
			return SourceResult{}, err
		}
		res.Changed = ev != nil
	} else {
		known, err := c.historySource(topicID, sourceID)
		if err != nil {
			return SourceResult{}, err
		}
		src = known
		if err := checkRelocation(src, *file); err != nil {
			return SourceResult{}, err
		}
	}
	res.Source = src
	if file != nil {
		res.Path = file.path
		if c.knownLocation(topicID, sourceID) != file.path {
			res.Changed = true
		}
		if !changes.DryRun {
			c.rememberLocation(topicID, sourceID, *file)
		}
	}
	return res, nil
}

// checkRelocation checks that f can be recorded as where src is: a file
// outside the Topic, with the recorded content.
func checkRelocation(src Source, f foundFile) error {
	switch {
	case src.Kind != SourceFile:
		return invalidf("Source %s is a %s, and only a file Source has a path", src.ID, src.Kind)
	case src.TopicPath != "":
		return invalidf("Source %s is the file %s inside the Topic, found there on every machine: "+
			"restore it there, or add the other file as a new Source", src.ID, src.TopicPath)
	case f.hash != src.Hash:
		return invalidf("%s is not Source %s: its content differs, so add it as a new Source", f.path, src.ID)
	}
	return nil
}

func unknownSource(topicID, sourceID string) error {
	return &Error{Code: CodeNotFound, Message: fmt.Sprintf(
		"the History of %s has no Source %s: study source list %s shows its Sources, and a line added to %s by hand "+
			"is not a Source until study source add records it", topicID, sourceID, topicID, sourcesFile)}
}

// historySource reads one Source from the Topic's History.
func (c *Core) historySource(topicID, sourceID string) (Source, error) {
	home, topic, err := c.openTopicFolder(topicID)
	if err != nil {
		return Source{}, err
	}
	defer home.Close()
	defer topic.Close()
	h, err := readHistory(topic, topicID)
	if err != nil {
		return Source{}, err
	}
	src, ok := replayHistory(h).knowledge().sources[sourceID]
	if !ok {
		return Source{}, unknownSource(topicID, sourceID)
	}
	return *src, nil
}

// SourceStatus is a Source as ListSources finds it on this computer.
type SourceStatus struct {
	Source
	// Path is where a file Source is on this computer, when found.
	Path string `json:"path,omitempty"`
	// State is ok, changed or missing for a file, empty for a URL, which is
	// never fetched, and untracked for a line of sources.jsonl the History
	// does not know.
	State string `json:"state,omitempty"`
}

// SourceList is a Topic's Knowledge base and Sources.
type SourceList struct {
	Topic string `json:"topic"`
	// KnowledgeBase is absent until one is chosen.
	KnowledgeBase *KnowledgeBase `json:"knowledge_base,omitempty"`
	Sources       []SourceStatus `json:"sources"`
}

// ListSources lists a Topic's Sources, as the History records them, and
// finds each file on this computer: inside the Topic, where it was last
// found, or in the Library by its content. Where a file is found is local
// state, updated as needed; nothing is recorded in the History.
func (c *Core) ListSources(ctx context.Context, topicID string) (SourceList, error) {
	home, topic, err := c.openTopicFolder(topicID)
	if err != nil {
		return SourceList{}, err
	}
	defer home.Close()
	defer topic.Close()
	kb, err := topicKnowledgeBase(&topicView{root: topic}, topicID)
	if err != nil {
		return SourceList{}, err
	}
	h, err := readHistory(topic, topicID)
	if err != nil {
		return SourceList{}, err
	}
	k := replayHistory(h).knowledge()
	list := SourceList{Topic: topicID, KnowledgeBase: kb, Sources: []SourceStatus{}}
	l := c.newLocator(ctx, home, topicID)
	for _, id := range k.order {
		src := *k.sources[id]
		st := SourceStatus{Source: src}
		if src.Kind == SourceFile {
			if st.Path, st.State, err = l.locate(src); err != nil {
				return SourceList{}, err
			}
		}
		list.Sources = append(list.Sources, st)
	}
	l.save()
	for _, src := range untrackedSources(topic, k) {
		list.Sources = append(list.Sources, SourceStatus{Source: src, State: SourceUntracked})
	}
	return list, nil
}

// untrackedSources returns the lines of sources.jsonl the History does not
// know, best effort: lines that are not Sources are skipped.
func untrackedSources(topic *os.Root, k *knowledgeState) []Source {
	data, exists, err := readFile(topic, sourcesFile)
	if err != nil || !exists {
		return nil
	}
	var out []Source
	seen := map[string]bool{}
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		var src Source
		if json.Unmarshal(line, &src) != nil || !sourceIDPattern.MatchString(src.ID) || seen[src.ID] {
			continue
		}
		seen[src.ID] = true
		if _, known := k.sources[src.ID]; !known {
			src.Title = derivedText(src.Title, maxTitleRunes)
			out = append(out, src)
		}
	}
	return out
}

// decodeSourceLine reads one line of sources.jsonl, and the fields this
// version does not know.
func decodeSourceLine(line []byte) (Source, map[string]json.RawMessage, error) {
	var src Source
	if err := json.Unmarshal(line, &src); err != nil {
		return Source{}, nil, corruptf("a line of %s is not a Source: %v", sourcesFile, err)
	}
	var extra map[string]json.RawMessage
	if err := json.Unmarshal(line, &extra); err != nil {
		return Source{}, nil, corruptf("a line of %s is not a Source: %v", sourcesFile, err)
	}
	for _, known := range knownSourceFields {
		delete(extra, known)
	}
	return src, extra, nil
}

// encodeSourceLine writes one line of sources.jsonl: the known fields in
// order, then any others, sorted, so the same Source always gives the same
// bytes.
func encodeSourceLine(src Source, extra map[string]json.RawMessage) ([]byte, error) {
	known, err := json.Marshal(src)
	if err != nil {
		return nil, internalError("encoding a Source", err)
	}
	if len(extra) == 0 {
		return known, nil
	}
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf bytes.Buffer
	buf.Write(known[:len(known)-1])
	for _, k := range keys {
		name, _ := json.Marshal(k)
		buf.WriteByte(',')
		buf.Write(name)
		buf.WriteByte(':')
		if err := json.Compact(&buf, extra[k]); err != nil {
			return nil, internalError("encoding a Source", err)
		}
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// applySourceAdded writes a new Source's line from the payload, which is the
// Source. When recovery finishes an interrupted write and the payload is the
// line the Event recorded, it is written as it is (see recordedSourceLine).
func applySourceAdded(ev event, item string, _ []byte, _ bool) ([]byte, bool, error) {
	var src Source
	if err := json.Unmarshal(ev.Data, &src); err != nil || src.ID == "" {
		return nil, false, corruptf("Event %s has an unreadable payload", ev.ID)
	}
	if item != sourceItem(src.ID) {
		return nil, false, corruptf("Event %s (%s) cannot edit %s", ev.ID, ev.Type, item)
	}
	if line, ok := recordedSourceLine(ev, item, ev.Data); ok {
		return line, true, nil
	}
	line, err := encodeSourceLine(src, nil)
	return line, err == nil, err
}

// applySourceUpdated rewrites a Source's line as the History has it after
// the change, keeping the fields this version does not know from the line.
// A union merge can leave two lines for the Source, and the one read here
// need not be the History's version, so the line is written from the
// payload's Source, never from the line's known fields. A line deleted by
// hand stops the write, which says how to restore it.
//
// When recovery finishes an interrupted write, the Event already recorded
// the line it meant to leave. If the payload's Source is that line, it is
// written as it is (see recordedSourceLine, which says when it is not).
func applySourceUpdated(ev event, item string, current []byte, exists bool) ([]byte, bool, error) {
	var d sourceUpdatedData
	if err := json.Unmarshal(ev.Data, &d); err != nil || d.ID == "" {
		return nil, false, corruptf("Event %s has an unreadable payload", ev.ID)
	}
	if item != sourceItem(d.ID) {
		return nil, false, corruptf("Event %s (%s) cannot edit %s", ev.ID, ev.Type, item)
	}
	if !exists {
		return nil, false, corruptf("Source %s is missing from %s: restore the line, for example from the last Checkpoint",
			d.ID, sourcesFile)
	}
	src, extra, err := decodeSourceLine(current)
	if err != nil {
		return nil, false, err
	}
	if d.Source != nil {
		if d.Source.ID != d.ID {
			return nil, false, corruptf("Event %s has an unreadable payload", ev.ID)
		}
		var payload struct {
			Source json.RawMessage `json:"source"`
		}
		if err := json.Unmarshal(ev.Data, &payload); err == nil {
			if line, ok := recordedSourceLine(ev, item, payload.Source); ok {
				return line, true, nil
			}
		}
		src = *d.Source
	} else {
		d.applyTo(&src)
	}
	line, err := encodeSourceLine(src, extra)
	return line, err == nil, err
}

// recordedSourceLine returns source, the Source as an Event's payload holds
// it (the whole payload of source.added, the "source" of source.updated),
// written as a line, when that line is the version of item the Event
// recorded. Only an Event read back from the History has recorded one, so
// this is found when recovery finishes an interrupted write, never while an
// Event is prepared.
//
// The line is then what the Event's writer wrote, whatever fields this
// version knows. It matters for an Event of an earlier build that set,
// changed or removed a field this version does not have (ADR-0011). Writing
// only the fields this version knows would drop such a field from an added
// Source's line, and would leave an updated Source's line as it was, at the
// version the Event changed it from, so that the next change to the Source
// looked like a second machine's.
//
// The payload's Source is not the recorded version when the writer put more
// in the line than the payload holds: an update keeps the fields of the line
// that its writer did not know, such as one added by hand. The caller then
// writes as it always does. For an earlier build's update that changed
// nothing this version knows, that leaves the line as it was, and the next
// change to the Source is flagged as a conflict.
func recordedSourceLine(ev event, item string, source json.RawMessage) ([]byte, bool) {
	recorded := ""
	for _, it := range ev.Items {
		if it.Item == item {
			recorded = it.After
		}
	}
	if recorded == "" || len(source) == 0 {
		return nil, false
	}
	var line bytes.Buffer
	if err := json.Compact(&line, source); err != nil || contentHash(line.Bytes(), true) != recorded {
		return nil, false
	}
	return line.Bytes(), true
}

func (d sourceUpdatedData) applyTo(src *Source) {
	if d.Title != nil {
		src.Title = *d.Title
	}
}

// newSourceID makes an id for a Source: a slug of its title (or address)
// and a random suffix, different from every Source the History knows.
func (c *Core) newSourceID(title string, k *knowledgeState) string {
	slug := slugify(title)
	if len(slug) > 40 {
		slug = strings.TrimRight(slug[:40], "-")
	}
	if slug == "" {
		slug = "source"
	}
	for length := 6; ; length++ {
		var suffix strings.Builder
		for _, r := range strings.ToLower(c.newID()) {
			if suffix.Len() == min(length, 26) {
				break
			}
			if r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
				suffix.WriteRune(r)
			}
		}
		id := slug + "." + suffix.String()
		if _, taken := k.sources[id]; !taken && sourceIDPattern.MatchString(id) {
			return id
		}
	}
}

// fileTitle is the Library's title for the file at path, or one derived from
// its name when the file is not in the Library, made safe to print.
func (c *Core) fileTitle(path string) string {
	title := ""
	if ix, err := library.Load(filepath.Join(c.home, libraryIndex)); err == nil {
		want := resolvedPath(path)
		for _, book := range ix.Books {
			if book.Path == path || resolvedPath(book.Path) == want {
				title = book.Title
				break
			}
		}
	}
	if title == "" {
		title = library.TitleOf(path)
	}
	if title = derivedText(title, maxTitleRunes); title != "" {
		return title
	}
	if name := derivedText(filepath.Base(path), maxTitleRunes); name != "" {
		return name
	}
	return "Untitled Source"
}

// derivedText makes text Lamplight derived, from a file name, a URL or the
// Library, safe to store and print: invalid UTF-8, control and bidi control
// characters go, spaces collapse, and it is cut to maxRunes.
func derivedText(s string, maxRunes int) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > maxRunes {
		s = strings.TrimSpace(string([]rune(s)[:maxRunes-1])) + "…"
	}
	return s
}

func resolvedPath(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return path
}

// cleanURL checks a web page Source's address and normalises it: an absolute
// http or https URL without credentials. The scheme and host are lowercased,
// a default port is dropped, and an international host stays readable, in
// Unicode (NFC), rather than percent-encoded.
func cleanURL(raw string) (string, error) {
	raw, err := cleanText("URL", raw, maxURLRunes)
	if err != nil {
		return "", err
	}
	notWeb := invalidf("%q is not a web address: give an absolute http or https URL", raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Opaque != "" || strings.ContainsAny(raw, " \t") {
		return "", notWeb
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", notWeb
	}
	if u.User != nil {
		return "", invalidf("%q carries a user name or password: give the address without them", raw)
	}
	host := norm.NFC.String(strings.ToLower(u.Hostname()))
	for _, r := range host {
		if unicode.IsControl(r) || unicode.IsSpace(r) || unicode.Is(unicode.Bidi_Control, r) || r == '%' {
			return "", notWeb
		}
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	switch port := u.Port(); {
	case port == "", scheme == "http" && port == "80", scheme == "https" && port == "443":
	default:
		host += ":" + port
	}
	out := scheme + "://" + host + u.EscapedPath()
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	if u.Fragment != "" {
		out += "#" + u.EscapedFragment()
	}
	return out, nil
}

// knowledgeState is what replay knows about a Topic's Sources and Evidence.
type knowledgeState struct {
	// sources are the Sources the History records, by id, in the order
	// they were added.
	sources  map[string]*Source
	order    []string
	evidence []Evidence
	byID     map[string]int // Evidence id → index in evidence
}

func (s *replayed) knowledge() *knowledgeState {
	if s.know == nil {
		s.know = &knowledgeState{sources: map[string]*Source{}, byID: map[string]int{}}
	}
	return s.know
}

func replaySourceAdded(s *replayed, ev event) error {
	if s.created.IsZero() {
		return fmt.Errorf("%w: the Topic's creation", errUnknownItem)
	}
	var src Source
	if err := json.Unmarshal(ev.Data, &src); err != nil || !sourceIDPattern.MatchString(src.ID) {
		return errors.New("its payload is not a Source")
	}
	k := s.knowledge()
	if _, ok := k.sources[src.ID]; !ok {
		k.order = append(k.order, src.ID)
	}
	k.sources[src.ID] = &src
	return nil
}

func replaySourceUpdated(s *replayed, ev event) error {
	var d sourceUpdatedData
	if err := json.Unmarshal(ev.Data, &d); err != nil || d.ID == "" {
		return errors.New("its payload names no Source")
	}
	src, ok := s.knowledge().sources[d.ID]
	if !ok {
		return fmt.Errorf("%w: Source %s", errUnknownItem, d.ID)
	}
	d.applyTo(src)
	return nil
}
