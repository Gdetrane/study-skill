package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mordor-forge/lamplight/v2/internal/library"
)

// The Knowledge seam (ADR-0007): a Topic's Knowledge base, its Sources, and
// the Evidence its Lessons cite. The core records them and nothing more: it
// never parses a document and never calls a knowledge service. The agent
// searches the Knowledge base itself, such as a NotebookLM notebook through
// the NotebookLM MCP server, or reads the Sources when there is none, and
// records the exact quotes it relied on.

const (
	sourcesFile = "sources.jsonl"

	eventKnowledgeBaseSet = "knowledge_base.set"
	eventSourceAdded      = "source.added"
	eventSourceUpdated    = "source.updated"
	eventEvidenceRecorded = "evidence.recorded"

	maxRefRunes   = 200  // notebook and NotebookLM Source ids
	maxURLRunes   = 2000 // a Source's URL
	maxQuoteRunes = 4000 // an Evidence quote
	maxPlaceRunes = 200  // an Evidence location
)

// Knowledge base kinds.
const (
	// KnowledgeBaseNotebookLM: the Topic's Sources are in a NotebookLM
	// notebook, which the agent queries through the NotebookLM MCP server.
	KnowledgeBaseNotebookLM = "notebooklm"
	// KnowledgeBaseNone: there is no Knowledge base; the agent reads the
	// Sources itself.
	KnowledgeBaseNone = "none"
)

// Source kinds.
const (
	SourceFile = "file"
	SourceURL  = "url"
)

// Where an Evidence location came from.
const (
	// LocationFromSource: read in the Source itself, such as a printed page
	// number or a section heading.
	LocationFromSource = "source"
	// LocationFromKnowledgeBase: reported by the Knowledge base, such as a
	// NotebookLM citation.
	LocationFromKnowledgeBase = "knowledge_base"
	// LocationFromLearner: given by the learner.
	LocationFromLearner = "learner"
	// LocationFromEstimate: the agent's estimate, which may be off.
	LocationFromEstimate = "estimate"
)

// States of a file Source on this computer, in ListSources.
const (
	// SourceOK: a file of the recorded size is at the recorded path.
	SourceOK = "ok"
	// SourceMoved: the file is gone from its path, but a file with the same
	// content is in the Library at FoundAt.
	SourceMoved = "moved"
	// SourceChanged: a file is at the path, but not the recorded one, and the
	// recorded content is nowhere in the Library.
	SourceChanged = "changed"
	// SourceMissing: the file is gone and not in the Library.
	SourceMissing = "missing"
)

func init() {
	fileCodecs = append(fileCodecs, codecEntry{pattern: sourcesFile, codec: jsonlCodec})
	eventKinds[eventKnowledgeBaseSet] = eventKind{apply: applyKnowledgeBaseSet, replay: replayTopicUpdated}
	eventKinds[eventSourceAdded] = eventKind{apply: applySourceAdded, replay: replaySourceAdded}
	eventKinds[eventSourceUpdated] = eventKind{apply: applySourceUpdated, replay: replaySourceUpdated}
	eventKinds[eventEvidenceRecorded] = eventKind{apply: applyNothing, replay: replayEvidenceRecorded}
}

// KnowledgeBase is where a Topic's Sources are held and searched for
// Evidence. A Topic has at most one; until one is chosen, it behaves as
// "none".
type KnowledgeBase struct {
	Kind string `json:"kind"`
	// Notebook is the NotebookLM notebook's id, for kind notebooklm.
	Notebook string `json:"notebook,omitempty"`
}

// checkKnowledgeBase validates a Knowledge base setting.
func checkKnowledgeBase(kb KnowledgeBase) (KnowledgeBase, error) {
	notebook, err := cleanRef("notebook id", kb.Notebook)
	if err != nil {
		return KnowledgeBase{}, err
	}
	switch kind := strings.TrimSpace(kb.Kind); kind {
	case KnowledgeBaseNotebookLM:
		if notebook == "" {
			return KnowledgeBase{}, invalidf("a notebooklm Knowledge base needs the notebook's id")
		}
		return KnowledgeBase{Kind: kind, Notebook: notebook}, nil
	case KnowledgeBaseNone:
		if notebook != "" {
			return KnowledgeBase{}, invalidf("a Topic without a Knowledge base has no notebook: leave the notebook id out")
		}
		return KnowledgeBase{Kind: kind}, nil
	default:
		return KnowledgeBase{}, invalidf("the Knowledge base kind must be %q or %q, not %q",
			KnowledgeBaseNotebookLM, KnowledgeBaseNone, kb.Kind)
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
	notebook, _ := table["notebook"].(string)
	return &KnowledgeBase{Kind: kind, Notebook: notebook}
}

// planKnowledgeBase plans setting a Topic's Knowledge base; nothing when it
// is already set so.
func planKnowledgeBase(topicID string, kb KnowledgeBase) plan {
	return func(_ *replayed, view *topicView) (*change, error) {
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
		if current := knowledgeBaseOf(settings); current != nil && *current == kb {
			return nil, nil
		}
		return &change{Type: eventKnowledgeBaseSet, Data: kb, Items: []string{topicFile}}, nil
	}
}

// applyKnowledgeBaseSet replaces the [knowledge_base] table of topic.toml.
// The table belongs to its kind, so settings of a previous kind go with it.
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
	table := map[string]any{"kind": kb.Kind}
	if kb.Notebook != "" {
		table["notebook"] = kb.Notebook
	}
	settings.extra["knowledge_base"] = table
	data, err := encodeTopicSettings(settings)
	return data, err == nil, err
}

// Source is a document or web page a Topic learns from. Sources are kept in
// sources.jsonl, one per line, so each is its own item in the History and two
// machines adding Sources never conflict.
type Source struct {
	// ID is a slug of the title plus a random suffix, such as
	// "modern-operating-systems.k3f9a2", so it never collides across
	// machines.
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	// Path, Hash and Size describe a file Source: its absolute path when it
	// was added or last found, and its content, by which it is found again
	// in the Library after a move.
	Path string `json:"path,omitempty"`
	Hash string `json:"hash,omitempty"`
	Size int64  `json:"size_bytes,omitempty"`
	// URL is a web page Source's address.
	URL string `json:"url,omitempty"`
	// NotebookLMID is the Source's id in the Topic's NotebookLM notebook.
	NotebookLMID string `json:"notebooklm_id,omitempty"`
}

// knownSourceFields are the fields of Source; others in a line, written by a
// newer version, are kept as they are.
var knownSourceFields = []string{"id", "kind", "title", "path", "hash", "size_bytes", "url", "notebooklm_id"}

var sourceIDPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*\.[a-z0-9]{1,26}$`)

func sourceItem(id string) string { return sourcesFile + "#" + id }

// SourceSpec describes a Source to add: a file or a URL.
type SourceSpec struct {
	Topic string
	// File is the file's path, relative to the folder study started in.
	File string
	URL  string
	// Title defaults to the Library's title for the file, else one derived
	// from its name, or the URL.
	Title string
	// NotebookLMID is the Source's id in the Topic's NotebookLM notebook, if
	// the agent added it there already.
	NotebookLMID string
	DryRun       bool
}

// SourceChanges describes changes to a Source. Nil fields stay as they are.
type SourceChanges struct {
	Title *string
	// Path records where a file Source is now. The file there must have the
	// recorded content.
	Path *string
	// NotebookLMID sets the Source's id in the NotebookLM notebook; empty
	// removes it.
	NotebookLMID *string
	DryRun       bool
}

// SourceResult is the result of AddSource and UpdateSource.
type SourceResult struct {
	Topic  string `json:"topic"`
	Source Source `json:"source"`
	// Changed is false when the Source already had the requested values.
	Changed bool `json:"changed"`
	DryRun  bool `json:"dry_run,omitempty"`
}

// sourceUpdatedData is the payload of a source.updated Event: the fields that
// changed, with their new values.
type sourceUpdatedData struct {
	ID           string  `json:"id"`
	Title        *string `json:"title,omitempty"`
	Path         *string `json:"path,omitempty"`
	NotebookLMID *string `json:"notebooklm_id,omitempty"`
}

// AddSource registers a Source on a Topic. A file is hashed, never parsed, so
// it can be found again in the Library after a move. Adding a file or URL the
// Topic already has is an error that names the existing Source.
func (c *Core) AddSource(ctx context.Context, spec SourceSpec) (SourceResult, error) {
	if err := checkTopicID(spec.Topic); err != nil {
		return SourceResult{}, err
	}
	title, err := cleanText("title", spec.Title, maxTitleRunes)
	if err != nil {
		return SourceResult{}, err
	}
	notebookID, err := cleanRef("NotebookLM id", spec.NotebookLMID)
	if err != nil {
		return SourceResult{}, err
	}
	var src Source
	switch file, link := strings.TrimSpace(spec.File), strings.TrimSpace(spec.URL); {
	case file != "" && link != "":
		return SourceResult{}, invalidf("a Source is a file or a URL, not both")
	case file != "":
		path, hash, size, err := c.hashSourceFile(ctx, file)
		if err != nil {
			return SourceResult{}, err
		}
		if title == "" {
			title = c.libraryTitle(path)
		}
		src = Source{Kind: SourceFile, Title: title, Path: path, Hash: hash, Size: size}
	case link != "":
		u, err := cleanURL(link)
		if err != nil {
			return SourceResult{}, err
		}
		if title == "" {
			title = u
		}
		src = Source{Kind: SourceURL, Title: title, URL: u}
	default:
		return SourceResult{}, invalidf("give the Source's file or URL")
	}
	src.NotebookLMID = notebookID

	_, err = c.writeTopic(ctx, spec.Topic, func(_ *replayed, view *topicView) (*change, error) {
		sources, err := readSources(view)
		if err != nil {
			return nil, err
		}
		for _, other := range sources {
			if src.Kind == SourceFile && other.Hash == src.Hash || src.Kind == SourceURL && other.URL == src.URL {
				return nil, &Error{Code: CodeAlreadyExists, Message: fmt.Sprintf(
					"Topic %s already has this %s as Source %s", spec.Topic, src.Kind, other.ID)}
			}
		}
		slugFrom := src.Title
		if src.Kind == SourceURL && src.Title == src.URL {
			slugFrom = strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(src.URL, "https://"), "http://"), "www.")
		}
		src.ID = c.newSourceID(slugFrom, sources)
		return &change{Type: eventSourceAdded, Data: src, Items: []string{sourceItem(src.ID)}}, nil
	}, spec.DryRun)
	if err != nil {
		return SourceResult{}, err
	}
	return SourceResult{Topic: spec.Topic, Source: src, Changed: true, DryRun: spec.DryRun}, nil
}

// UpdateSource changes a Source's title or NotebookLM id, or records where a
// moved file is now. Asking for the values it already has records nothing.
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
	if changes.NotebookLMID != nil {
		id, err := cleanRef("NotebookLM id", *changes.NotebookLMID)
		if err != nil {
			return SourceResult{}, err
		}
		want.NotebookLMID = &id
	}
	var newHash string
	if changes.Path != nil {
		path, hash, _, err := c.hashSourceFile(ctx, *changes.Path)
		if err != nil {
			return SourceResult{}, err
		}
		want.Path, newHash = &path, hash
	}
	if want.Title == nil && want.Path == nil && want.NotebookLMID == nil {
		return SourceResult{}, invalidf("nothing to change: give a new title, path or NotebookLM id")
	}

	var src Source
	ev, err := c.writeTopic(ctx, topicID, func(_ *replayed, view *topicView) (*change, error) {
		line, exists, err := view.read(sourceItem(sourceID))
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, &Error{Code: CodeNotFound, Message: fmt.Sprintf(
				"Topic %s has no Source %s: study source list %s shows its Sources", topicID, sourceID, topicID)}
		}
		if src, _, err = decodeSourceLine(line); err != nil {
			return nil, err
		}
		diff := sourceUpdatedData{ID: sourceID}
		if want.Title != nil && *want.Title != src.Title {
			diff.Title, src.Title = want.Title, *want.Title
		}
		if want.NotebookLMID != nil && *want.NotebookLMID != src.NotebookLMID {
			diff.NotebookLMID, src.NotebookLMID = want.NotebookLMID, *want.NotebookLMID
		}
		if want.Path != nil {
			if src.Kind != SourceFile {
				return nil, invalidf("Source %s is a %s, and only a file Source has a path", sourceID, src.Kind)
			}
			if newHash != src.Hash {
				return nil, invalidf("%s is not Source %s: its content differs, so add it as a new Source", *want.Path, sourceID)
			}
			if *want.Path != src.Path {
				diff.Path, src.Path = want.Path, *want.Path
			}
		}
		if diff.Title == nil && diff.Path == nil && diff.NotebookLMID == nil {
			return nil, nil
		}
		return &change{Type: eventSourceUpdated, Data: diff, Items: []string{sourceItem(sourceID)}}, nil
	}, changes.DryRun)
	if err != nil {
		return SourceResult{}, err
	}
	return SourceResult{Topic: topicID, Source: src, Changed: ev != nil, DryRun: changes.DryRun}, nil
}

// SourceStatus is a Source as ListSources finds it on this computer.
type SourceStatus struct {
	Source
	// State is where a file Source is now: ok, moved, changed or missing.
	// It is empty for URLs, which are never fetched.
	State string `json:"state,omitempty"`
	// FoundAt is where a moved file is now, found in the Library by its
	// content. Record it with UpdateSource.
	FoundAt string `json:"found_at,omitempty"`
}

// SourceList is a Topic's Knowledge base and Sources.
type SourceList struct {
	Topic string `json:"topic"`
	// KnowledgeBase is absent until one is chosen.
	KnowledgeBase *KnowledgeBase `json:"knowledge_base,omitempty"`
	Sources       []SourceStatus `json:"sources"`
}

// ListSources lists a Topic's Sources and checks where each file is. A file
// whose size still matches counts as in place, without being hashed again; a
// file that is gone is searched for in the Library by its content.
func (c *Core) ListSources(ctx context.Context, topicID string) (SourceList, error) {
	home, topic, err := c.openTopicFolder(topicID)
	if err != nil {
		return SourceList{}, err
	}
	defer home.Close()
	defer topic.Close()
	list := SourceList{Topic: topicID, Sources: []SourceStatus{}}
	data, exists, err := readItem(topic, topicFile)
	if err != nil {
		return SourceList{}, err
	}
	if !exists {
		return SourceList{}, corruptf("%s of %s is missing", topicFile, topicID)
	}
	settings, err := parseTopicSettings(data, filepath.Join(c.home, topicID, topicFile))
	if err != nil {
		return SourceList{}, err
	}
	list.KnowledgeBase = knowledgeBaseOf(settings)
	sources, err := readSources(&topicView{root: topic})
	if err != nil {
		return SourceList{}, err
	}
	var ix *library.Index
	loaded := false
	lib := func() *library.Index {
		if !loaded {
			loaded = true
			if index, err := library.Load(filepath.Join(c.home, libraryIndex)); err == nil {
				ix = index
			}
		}
		return ix
	}
	for _, src := range sources {
		if err := ctx.Err(); err != nil {
			return SourceList{}, err
		}
		st := SourceStatus{Source: src}
		if src.Kind == SourceFile {
			st.State, st.FoundAt = locateSource(src, lib)
		}
		list.Sources = append(list.Sources, st)
	}
	return list, nil
}

// locateSource finds a file Source on this computer.
func locateSource(src Source, lib func() *library.Index) (state, foundAt string) {
	info, err := os.Stat(src.Path)
	if err == nil && info.Mode().IsRegular() && info.Size() == src.Size {
		return SourceOK, ""
	}
	if ix := lib(); ix != nil {
		for _, book := range ix.Books {
			if book.Size != src.Size || book.Path == src.Path {
				continue
			}
			if hash, _, err := hashFile(book.Path); err == nil && hash == src.Hash {
				return SourceMoved, book.Path
			}
		}
	}
	if err == nil {
		return SourceChanged, ""
	}
	return SourceMissing, ""
}

// readSources reads every Source in sources.jsonl, in file order.
func readSources(view *topicView) ([]Source, error) {
	data, exists, err := view.read(sourcesFile)
	if err != nil || !exists {
		return nil, err
	}
	lines, _, err := jsonlEntries(data)
	if err != nil {
		return nil, corruptf("%s is damaged: %v", sourcesFile, err)
	}
	sources := make([]Source, 0, len(lines))
	for _, line := range lines {
		src, _, err := decodeSourceLine(line)
		if err != nil {
			return nil, err
		}
		sources = append(sources, src)
	}
	return sources, nil
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

func applySourceAdded(ev event, item string, _ []byte, _ bool) ([]byte, bool, error) {
	var src Source
	if err := json.Unmarshal(ev.Data, &src); err != nil || src.ID == "" {
		return nil, false, corruptf("Event %s has an unreadable payload", ev.ID)
	}
	if item != sourceItem(src.ID) {
		return nil, false, corruptf("Event %s (%s) cannot edit %s", ev.ID, ev.Type, item)
	}
	line, err := encodeSourceLine(src, nil)
	return line, err == nil, err
}

func applySourceUpdated(ev event, item string, current []byte, exists bool) ([]byte, bool, error) {
	var d sourceUpdatedData
	if err := json.Unmarshal(ev.Data, &d); err != nil || d.ID == "" {
		return nil, false, corruptf("Event %s has an unreadable payload", ev.ID)
	}
	if item != sourceItem(d.ID) {
		return nil, false, corruptf("Event %s (%s) cannot edit %s", ev.ID, ev.Type, item)
	}
	if !exists {
		return nil, false, corruptf("Source %s is missing from %s", d.ID, sourcesFile)
	}
	src, extra, err := decodeSourceLine(current)
	if err != nil {
		return nil, false, err
	}
	if d.Title != nil {
		src.Title = *d.Title
	}
	if d.Path != nil {
		src.Path = *d.Path
	}
	if d.NotebookLMID != nil {
		src.NotebookLMID = *d.NotebookLMID
	}
	line, err := encodeSourceLine(src, extra)
	return line, err == nil, err
}

// newSourceID makes an id for a Source: a slug of its title (or address)
// and a random suffix, different from every id in sources.
func (c *Core) newSourceID(title string, sources []Source) string {
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
		taken := false
		for _, s := range sources {
			taken = taken || s.ID == id
		}
		if !taken && sourceIDPattern.MatchString(id) {
			return id
		}
	}
}

// hashSourceFile resolves a file Source's path against the folder study
// started in and hashes the file. Only its bytes are read: the core never
// parses a document.
func (c *Core) hashSourceFile(ctx context.Context, file string) (path, hash string, size int64, err error) {
	path = strings.TrimSpace(file)
	if path == "" {
		return "", "", 0, invalidf("give the Source's file")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(c.dir, path)
	}
	path = filepath.Clean(path)
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", "", 0, &Error{Code: CodeNotFound, Message: path + " does not exist"}
	}
	if err != nil {
		return "", "", 0, internalError("reading "+path, err)
	}
	if !info.Mode().IsRegular() {
		return "", "", 0, invalidf("%s is not a file", path)
	}
	if err := ctx.Err(); err != nil {
		return "", "", 0, err
	}
	hash, size, err = hashFile(path)
	if err != nil {
		return "", "", 0, internalError("reading "+path, err)
	}
	return path, hash, size, nil
}

// hashFile returns a file's content hash, in the form the History uses, and
// its size.
func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), n, nil
}

// libraryTitle is the Library's title for the file at path, or one derived
// from its name when the file is not in the Library.
func (c *Core) libraryTitle(path string) string {
	if ix, err := library.Load(filepath.Join(c.home, libraryIndex)); err == nil {
		want := resolvedPath(path)
		for _, book := range ix.Books {
			if book.Path == path || resolvedPath(book.Path) == want {
				return book.Title
			}
		}
	}
	return library.TitleOf(path)
}

func resolvedPath(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return path
}

// cleanURL checks a web page Source's address: an absolute http or https URL.
func cleanURL(raw string) (string, error) {
	raw, err := cleanText("URL", raw, maxURLRunes)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || strings.ContainsAny(raw, " \t") {
		return "", invalidf("%q is not a web address: give an absolute http or https URL", raw)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", invalidf("%q is not a web address: give an absolute http or https URL", raw)
	}
	u.Host = strings.ToLower(u.Host)
	return u.String(), nil
}

// cleanRef checks an id from another service, such as a NotebookLM notebook
// id: printable, without spaces.
func cleanRef(field, s string) (string, error) {
	s, err := cleanText(field, s, maxRefRunes)
	if err != nil {
		return "", err
	}
	if strings.ContainsFunc(s, unicode.IsSpace) {
		return "", invalidf("the %s contains a space", field)
	}
	return s, nil
}

// Evidence is an exact quote from a Source, with its location when known,
// cited by a Lesson.
type Evidence struct {
	ID     string `json:"id"`
	Lesson string `json:"lesson"`
	Source string `json:"source"`
	Quote  string `json:"quote"`
	// Location is where the quote is in the Source, such as "p. 42" or
	// "§3.2", when known; LocationFrom says how it is known.
	Location     string `json:"location,omitempty"`
	LocationFrom string `json:"location_from,omitempty"`
	// Recorded is when the Evidence was recorded, by the recording
	// computer's clock.
	Recorded time.Time `json:"recorded"`
}

// evidenceData is the payload of an evidence.recorded Event.
type evidenceData struct {
	ID           string `json:"id"`
	Lesson       string `json:"lesson"`
	Source       string `json:"source"`
	Quote        string `json:"quote"`
	Location     string `json:"location,omitempty"`
	LocationFrom string `json:"location_from,omitempty"`
}

// EvidenceSpec describes Evidence to record.
type EvidenceSpec struct {
	Topic string
	// Lesson is the id of the Lesson that cites the Evidence.
	Lesson string
	// Source is the id of the Source the quote comes from.
	Source string
	// Quote is the exact text, as the Source has it.
	Quote        string
	Location     string
	LocationFrom string
	DryRun       bool
}

// EvidenceResult is the result of RecordEvidence.
type EvidenceResult struct {
	Topic    string   `json:"topic"`
	Evidence Evidence `json:"evidence"`
	// Changed is false when the same Evidence was already recorded.
	Changed bool `json:"changed"`
	DryRun  bool `json:"dry_run,omitempty"`
}

// EvidenceList is the Evidence recorded in a Topic.
type EvidenceList struct {
	Topic string `json:"topic"`
	// Lesson is set when the list was asked for one Lesson.
	Lesson   string     `json:"lesson,omitempty"`
	Evidence []Evidence `json:"evidence"`
}

// RecordEvidence records an exact quote from one of the Topic's Sources that
// a Lesson cites. The core does not check the quote against the Source: it
// never parses documents. Recording the same Evidence twice records nothing.
func (c *Core) RecordEvidence(ctx context.Context, spec EvidenceSpec) (EvidenceResult, error) {
	if err := checkTopicID(spec.Topic); err != nil {
		return EvidenceResult{}, err
	}
	if err := checkLessonID(spec.Lesson); err != nil {
		return EvidenceResult{}, err
	}
	if !sourceIDPattern.MatchString(spec.Source) {
		return EvidenceResult{}, invalidf("%q is not a Source id: study source list shows them", spec.Source)
	}
	quote, err := cleanQuote(spec.Quote)
	if err != nil {
		return EvidenceResult{}, err
	}
	location, err := cleanText("location", spec.Location, maxPlaceRunes)
	if err != nil {
		return EvidenceResult{}, err
	}
	from := strings.TrimSpace(spec.LocationFrom)
	switch {
	case location == "" && from != "":
		return EvidenceResult{}, invalidf("location_from says where a location came from: give the location too")
	case location != "" && !validLocationFrom(from):
		return EvidenceResult{}, invalidf("say where the location came from: %s, %s, %s or %s",
			LocationFromSource, LocationFromKnowledgeBase, LocationFromLearner, LocationFromEstimate)
	}
	want := evidenceData{Lesson: spec.Lesson, Source: spec.Source, Quote: quote, Location: location, LocationFrom: from}

	var existing *Evidence
	ev, err := c.writeTopic(ctx, spec.Topic, func(s *replayed, _ *topicView) (*change, error) {
		k := s.knowledge()
		if !k.sources[spec.Source] {
			return nil, &Error{Code: CodeNotFound, Message: fmt.Sprintf(
				"Topic %s has no Source %s: add it with study source add first", spec.Topic, spec.Source)}
		}
		for i, e := range k.evidence {
			if e.Lesson == want.Lesson && e.Source == want.Source && e.Quote == want.Quote &&
				e.Location == want.Location && e.LocationFrom == want.LocationFrom {
				existing = &k.evidence[i]
				return nil, nil
			}
		}
		want.ID = c.newEvidenceID()
		return &change{Type: eventEvidenceRecorded, Data: want}, nil
	}, spec.DryRun)
	if err != nil {
		return EvidenceResult{}, err
	}
	result := EvidenceResult{Topic: spec.Topic, DryRun: spec.DryRun}
	switch {
	case existing != nil:
		result.Evidence = *existing
	case ev != nil:
		result.Evidence = evidenceOf(want, ev.Wall)
		result.Changed = true
	}
	return result, nil
}

// ListEvidence lists the Evidence recorded in a Topic, in the order it was
// recorded; for one Lesson when lesson is not empty.
func (c *Core) ListEvidence(ctx context.Context, topicID, lesson string) (EvidenceList, error) {
	if lesson != "" {
		if err := checkLessonID(lesson); err != nil {
			return EvidenceList{}, err
		}
	}
	home, topic, err := c.openTopicFolder(topicID)
	if err != nil {
		return EvidenceList{}, err
	}
	defer home.Close()
	defer topic.Close()
	if err := ctx.Err(); err != nil {
		return EvidenceList{}, err
	}
	h, err := readHistory(topic, topicID)
	if err != nil {
		return EvidenceList{}, err
	}
	list := EvidenceList{Topic: topicID, Lesson: lesson, Evidence: []Evidence{}}
	for _, e := range replayHistory(h).knowledge().evidence {
		if lesson == "" || e.Lesson == lesson {
			list.Evidence = append(list.Evidence, e)
		}
	}
	return list, nil
}

func evidenceOf(d evidenceData, recorded time.Time) Evidence {
	return Evidence{ID: d.ID, Lesson: d.Lesson, Source: d.Source, Quote: d.Quote,
		Location: d.Location, LocationFrom: d.LocationFrom, Recorded: recorded.UTC()}
}

// newEvidenceID makes a short random id for Evidence.
func (c *Core) newEvidenceID() string {
	id := strings.ToLower(c.newID())
	if len(id) > 10 {
		id = id[:10]
	}
	return id
}

func validLocationFrom(from string) bool {
	switch from {
	case LocationFromSource, LocationFromKnowledgeBase, LocationFromLearner, LocationFromEstimate:
		return true
	}
	return false
}

// checkLessonID checks a Lesson id. Lessons belong to the Syllabus (#25); the
// Knowledge seam only stores the id, so it checks its form.
func checkLessonID(id string) error {
	if strings.TrimSpace(id) == "" {
		return invalidf("name the Lesson that cites the Evidence")
	}
	if len(id) > 64 || !topicIDPattern.MatchString(id) {
		return invalidf("%q is not a valid Lesson id: use lowercase letters, digits and single hyphens, up to 64 characters", id)
	}
	return nil
}

// cleanQuote checks an exact quote. It may span lines, so newlines and tabs
// are kept; other control characters are refused.
func cleanQuote(s string) (string, error) {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
	if s == "" {
		return "", invalidf("give the exact quote, as the Source has it")
	}
	for _, r := range s {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return "", invalidf("the quote contains a control character")
		}
	}
	if utf8.RuneCountInString(s) > maxQuoteRunes {
		return "", invalidf("the quote is longer than %d characters: quote the passage that matters", maxQuoteRunes)
	}
	return s, nil
}

// knowledgeState is what replay knows about a Topic's Sources and Evidence.
type knowledgeState struct {
	sources  map[string]bool
	evidence []Evidence
}

func (s *replayed) knowledge() *knowledgeState {
	if s.know == nil {
		s.know = &knowledgeState{sources: map[string]bool{}}
	}
	return s.know
}

// lessonsWithoutEvidence returns the lessons, of those given, that cite no
// Evidence. Lessons without Evidence are marked in status, never blocked;
// the Syllabus (#25) supplies the Lessons.
func (s *replayed) lessonsWithoutEvidence(lessons []string) []string {
	cited := map[string]bool{}
	for _, e := range s.knowledge().evidence {
		cited[e.Lesson] = true
	}
	var out []string
	for _, l := range lessons {
		if !cited[l] {
			out = append(out, l)
		}
	}
	return out
}

func replaySourceAdded(s *replayed, ev event) error {
	if s.created.IsZero() {
		return fmt.Errorf("%w: the Topic's creation", errUnknownItem)
	}
	var src Source
	if err := json.Unmarshal(ev.Data, &src); err != nil || !sourceIDPattern.MatchString(src.ID) {
		return errors.New("its payload is not a Source")
	}
	s.knowledge().sources[src.ID] = true
	return nil
}

func replaySourceUpdated(s *replayed, ev event) error {
	var d sourceUpdatedData
	if err := json.Unmarshal(ev.Data, &d); err != nil || d.ID == "" {
		return errors.New("its payload names no Source")
	}
	if !s.knowledge().sources[d.ID] {
		return fmt.Errorf("%w: Source %s", errUnknownItem, d.ID)
	}
	return nil
}

func replayEvidenceRecorded(s *replayed, ev event) error {
	var d evidenceData
	if err := json.Unmarshal(ev.Data, &d); err != nil || d.ID == "" || d.Lesson == "" || d.Source == "" || d.Quote == "" {
		return errors.New("its payload is not Evidence")
	}
	k := s.knowledge()
	if !k.sources[d.Source] {
		return fmt.Errorf("%w: Source %s", errUnknownItem, d.Source)
	}
	k.evidence = append(k.evidence, evidenceOf(d, ev.Wall))
	return nil
}
