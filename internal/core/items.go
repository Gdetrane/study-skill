package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// Items are what an Event edits, each recorded with its hash before and
// after. An item is named relative to its Topic, always with forward
// slashes whatever the operating system:
//
//   - "<path>" is a whole file, such as "topic.toml" or "syllabus.toml";
//   - "<path>#<key>" is one entity inside a file, such as one Card,
//     "cards.jsonl#<card-id>", or a Lesson's Check, "lessons/<id>.md#check".
//
// An entity's hash covers only that entity, in a canonical form its file's
// codec defines, so two machines adding different Cards to one file never
// look like a conflict, and reformatting a file does not look like an edit.

// A fileCodec reads and writes the entities inside one kind of file.
type fileCodec struct {
	// get returns the canonical content of the entity with key in file, and
	// whether it exists. An unreadable file is an error.
	get func(file []byte, key string) ([]byte, bool, error)
	// put returns file with the entity replaced by content, added when it
	// is new, or removed when exists is false.
	put func(file []byte, key string, content []byte, exists bool) ([]byte, error)
	// index returns, for each key, every distinct canonical content the
	// file holds for it, in file order: more than one when a union merge
	// left several lines. Nil for files that hold one copy of each entity.
	index func(file []byte) (map[string][][]byte, error)
	// historyText is set for files whose text the History holds, such as
	// sources.jsonl: the History's version is the entity, and a line it
	// never recorded is an edit made outside Lamplight. Otherwise the file
	// is authoritative for text (ADR-0005), and a hand edit wins.
	historyText bool
}

// itemVersions is what the History recorded for an item, which decides
// among the lines a merge leaves for it.
type itemVersions struct {
	// latest is the hash the last Event that changed the item recorded,
	// "" when that Event removed it; recorded is whether any did.
	latest   string
	recorded bool
	// known maps every hash any Event recorded for the item, before or
	// after, to the position of the last Event that did.
	known map[string]int
}

// resolveEntity picks an entity's content among the distinct contents a
// file holds for it. One content is the entity, whatever it is. When a
// union merge left several, each is the version the History recorded last,
// an older version an Event recorded (debris of the merge), or a version the
// History never recorded (an edit made outside Lamplight). Then:
//
//   - where the file is authoritative for text, one edit made outside
//     Lamplight wins over the recorded versions, as a hand edit does
//     without a merge (ADR-0005). Several are a conflict, and the smallest
//     canonical one is read, the same on every machine;
//   - where the History holds the text, the recorded version wins, and any
//     other edit is a conflict;
//   - otherwise the version recorded last is read; when the last Event
//     removed the entity, debris does not bring it back. Without it, the
//     most recently recorded older version is read.
//
// unknown counts the contents the History never recorded, which decides
// whether status flags the entity (see conflicting).
func resolveEntity(contents [][]byte, v itemVersions, historyText bool) (content []byte, exists bool, unknown [][]byte) {
	switch len(contents) {
	case 0:
		return nil, false, nil
	case 1:
		return contents[0], true, nil
	}
	var latest, debris []byte
	debrisAt := -1
	for _, c := range contents {
		hash := contentHash(c, true)
		at, isKnown := v.known[hash]
		switch {
		case v.recorded && hash == v.latest:
			latest = c
		case isKnown:
			if at > debrisAt || (at == debrisAt && bytes.Compare(c, debris) < 0) {
				debris, debrisAt = c, at
			}
		default:
			unknown = append(unknown, c)
		}
	}
	smallest := func(cs [][]byte) []byte {
		best := cs[0]
		for _, c := range cs[1:] {
			if bytes.Compare(c, best) < 0 {
				best = c
			}
		}
		return best
	}
	switch {
	case !historyText && len(unknown) > 0:
		return smallest(unknown), true, unknown
	case latest != nil:
		return latest, true, unknown
	case v.recorded && v.latest == "":
		return nil, false, unknown
	case len(unknown) > 0:
		return smallest(unknown), true, unknown
	}
	return debris, true, unknown
}

// conflicting reports whether the contents resolveEntity found for an
// entity are a conflict for the learner to settle: several edits made
// outside Lamplight, or where the History holds the text, any.
func conflicting(unknown [][]byte, historyText bool) bool {
	if historyText {
		return len(unknown) > 0
	}
	return len(unknown) > 1
}

// fileCodecs maps file patterns (path.Match syntax, slash-separated) to the
// codec of their entities.
var fileCodecs = []codecEntry{
	{pattern: cardsFile, codec: jsonlCodec},
	{pattern: "lessons/*.md", codec: lessonCodec},
}

type codecEntry struct {
	pattern string
	codec   fileCodec
}

func codecFor(file string) (fileCodec, bool) {
	for _, e := range fileCodecs {
		if ok, _ := path.Match(e.pattern, file); ok {
			return e.codec, true
		}
	}
	return fileCodec{}, false
}

// parseItem splits an item name into its file and, for an entity, its key.
// Names come from the History, which anyone can edit, so they are checked:
// a file is a clean relative path that stays inside the Topic and never
// inside .git, and an entity's file must have a codec.
func parseItem(item string) (file, key string, err error) {
	file, key, entity := strings.Cut(item, "#")
	if !fs.ValidPath(file) || file == "." || strings.Contains(file, `\`) {
		return "", "", corruptf("%q is not a valid item name", item)
	}
	for _, elem := range strings.Split(file, "/") {
		if elem == ".git" {
			return "", "", corruptf("%q is not a valid item name: Lamplight never edits .git", item)
		}
	}
	if entity {
		if key == "" || strings.ContainsAny(key, "#\n") {
			return "", "", corruptf("%q is not a valid item name", item)
		}
		if _, ok := codecFor(file); !ok {
			return "", "", corruptf("%q names an entity of %s, which has no entities", item, file)
		}
	}
	return file, key, nil
}

// readFile returns a file's content and whether it exists. Files are
// regular files inside the Topic: a symbolic link is refused, so an agent
// cannot point Lamplight at a file elsewhere.
func readFile(topic *os.Root, file string) ([]byte, bool, error) {
	name := filepath.FromSlash(file)
	info, err := topic.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, internalError("reading "+file, err)
	}
	if !info.Mode().IsRegular() {
		return nil, false, corruptf("%s is not a regular file: Lamplight does not follow links inside a Topic", file)
	}
	data, err := topic.ReadFile(name)
	if err != nil {
		return nil, false, internalError("reading "+file, err)
	}
	return data, true, nil
}

// readItem returns an item's content and whether it exists, without the
// History: among several lines for an entity, the smallest canonical one.
// Readers that have replayed the History use readItemWith or a topicView.
func readItem(topic *os.Root, item string) ([]byte, bool, error) {
	return readItemWith(topic, item, itemVersions{})
}

// readItemWith returns an item's content and whether it exists, resolving
// the lines a merge leaves for an entity with what the History recorded.
func readItemWith(topic *os.Root, item string, v itemVersions) ([]byte, bool, error) {
	file, key, err := parseItem(item)
	if err != nil {
		return nil, false, err
	}
	data, exists, err := readFile(topic, file)
	if err != nil || key == "" || !exists {
		return data, exists, err
	}
	codec, _ := codecFor(file)
	if codec.index == nil {
		content, exists, err := codec.get(data, key)
		if err != nil {
			return nil, false, corruptf("%s is damaged: %v", file, err)
		}
		return content, exists, nil
	}
	idx, err := codec.index(data)
	if err != nil {
		return nil, false, corruptf("%s is damaged: %v", file, err)
	}
	content, exists, _ := resolveEntity(idx[key], v, codec.historyText)
	return content, exists, nil
}

// entityConflicts flags the entities of files with an index for which a
// merge left contents that conflict (see conflicting): two edits made
// outside Lamplight on two machines, or, where the History holds the text,
// an edit it never recorded.
func entityConflicts(topic *os.Root, s *replayed) []Flag {
	var flags []Flag
	for _, e := range fileCodecs {
		if e.codec.index == nil || strings.ContainsAny(e.pattern, "*?[") {
			continue
		}
		data, exists, err := readFile(topic, e.pattern)
		if err != nil || !exists {
			continue
		}
		idx, err := e.codec.index(data)
		if err != nil {
			continue
		}
		keys := make([]string, 0, len(idx))
		for key, contents := range idx {
			if len(contents) > 1 {
				keys = append(keys, key)
			}
		}
		slices.Sort(keys)
		for _, key := range keys {
			item := e.pattern + "#" + key
			_, _, unknown := resolveEntity(idx[key], s.itemVersions(item), e.codec.historyText)
			if !conflicting(unknown, e.codec.historyText) {
				continue
			}
			detail := make([]string, len(unknown))
			for i, c := range unknown {
				detail[i] = string(c)
			}
			slices.Sort(detail)
			msg := fmt.Sprintf("%s holds %d versions of %s that the History never recorded, probably edited by hand "+
				"on two machines: Lamplight reads the same one on every machine; keep the right line by hand",
				e.pattern, len(unknown), key)
			if e.codec.historyText {
				msg = fmt.Sprintf("%s holds a version of %s that the History never recorded, probably edited outside "+
					"Lamplight: Lamplight uses the History's version, and the next change to it leaves one line",
					e.pattern, key)
			}
			flags = append(flags, newFlag(FlagConflict, item, nil, strings.Join(detail, "\n"), msg))
		}
	}
	return flags
}

// writeItem replaces an item atomically, or removes it. An entity is written
// by replacing its whole file atomically.
func writeItem(topic *os.Root, item string, content itemContent) error {
	file, key, err := parseItem(item)
	if err != nil {
		return err
	}
	name := filepath.FromSlash(file)
	if key != "" {
		current, exists, err := readFile(topic, file)
		if err != nil {
			return err
		}
		if !exists && !content.exists {
			return nil
		}
		codec, _ := codecFor(file)
		next, err := codec.put(current, key, content.data, content.exists)
		if err != nil {
			if CodeOf(err) == CodeCorrupt {
				return corruptf("%s is damaged: %v", file, err)
			}
			return err
		}
		content = itemContent{data: next, exists: true}
	}
	if !content.exists {
		if err := topic.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return internalError("removing "+file, err)
		}
		syncDir(topic, filepath.Dir(name))
		return nil
	}
	if dir := filepath.Dir(name); dir != "." {
		if err := topic.MkdirAll(dir, 0o755); err != nil {
			return internalError("creating "+dir, err)
		}
	}
	return writeFileAtomic(topic, name, content.data)
}

// validateItem checks that an item's content can be read, so recovery can
// tell a hand edit from a damaged file. Entities were already parsed by
// their codec when they were read.
func validateItem(item string, data []byte) error {
	switch item {
	case topicFile:
		_, err := parseTopicSettings(data, item)
		return err
	case syllabusFile:
		_, err := parseSyllabusFile(data, item)
		return err
	}
	return nil
}

// jsonlCodec stores entities as JSON objects, one per line, each with a
// string "id" that is its key. An entity's canonical form is its compact
// JSON, so reformatting a line changes nothing. Lines Lamplight does not
// touch are kept byte for byte, and new entities are appended.
//
// A file that merges by union can end up with several lines for one id:
// a stale copy beside an edit, or two machines' edits. Such a file stays
// usable: resolveEntity picks one with what the History recorded, the same
// on every machine whatever order the merge left the lines in, status flags
// a real conflict, and the next write of the entity leaves a single line.
//
// New entities are appended rather than inserted in order: a union merge
// keeps both machines' changes apart only when they touch different parts of
// the file, and two machines appending touch only its end.
var jsonlCodec = fileCodec{get: jsonlGet, put: jsonlPut, index: jsonlIndex}

// jsonlHistoryCodec is jsonlCodec for a file whose text the History holds,
// such as sources.jsonl.
var jsonlHistoryCodec = fileCodec{get: jsonlGet, put: jsonlPut, index: jsonlIndex, historyText: true}

// jsonlTolerantCodec is jsonlCodec for a file the learner is invited to edit
// by hand, such as tasks.jsonl: a line that is not a JSON object with an id
// is skipped when reading and kept as it is when writing, so one bad line
// never makes the others unusable. Readers report such lines.
var jsonlTolerantCodec = fileCodec{get: jsonlTolerantGet, put: jsonlTolerantPut, index: jsonlTolerantIndex}

// jsonlLine is one non-blank line of a JSONL file, with its id; the id is
// empty for a line that is not a JSON object with one.
type jsonlLine struct {
	text []byte
	id   string
}

func jsonlLines(file []byte) []jsonlLine {
	var out []jsonlLine
	for _, line := range bytes.Split(file, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var head struct {
			ID string `json:"id"`
		}
		id := ""
		if json.Unmarshal(line, &head) == nil {
			id = head.ID
		}
		out = append(out, jsonlLine{text: line, id: id})
	}
	return out
}

func jsonlTolerantIndex(file []byte) (map[string][][]byte, error) {
	idx := map[string][][]byte{}
	for _, l := range jsonlLines(file) {
		if l.id == "" {
			continue
		}
		var canonical bytes.Buffer
		if json.Compact(&canonical, l.text) != nil {
			continue
		}
		if !slices.ContainsFunc(idx[l.id], func(c []byte) bool { return bytes.Equal(c, canonical.Bytes()) }) {
			idx[l.id] = append(idx[l.id], canonical.Bytes())
		}
	}
	return idx, nil
}

func jsonlTolerantGet(file []byte, key string) ([]byte, bool, error) {
	idx, _ := jsonlTolerantIndex(file)
	var entry []byte
	for _, c := range idx[key] {
		if entry == nil || bytes.Compare(c, entry) < 0 {
			entry = c
		}
	}
	return entry, entry != nil, nil
}

func jsonlTolerantPut(file []byte, key string, content []byte, exists bool) ([]byte, error) {
	if exists {
		var compact bytes.Buffer
		if err := json.Compact(&compact, content); err != nil {
			return nil, internalError("writing an entry", err)
		}
		content = compact.Bytes()
	}
	var out bytes.Buffer
	found := false
	for _, l := range jsonlLines(file) {
		line := l.text
		if l.id == key {
			if found || !exists {
				found = true
				continue
			}
			found = true
			line = content
		}
		out.Write(line)
		out.WriteByte('\n')
	}
	if exists && !found {
		out.Write(content)
		out.WriteByte('\n')
	}
	return out.Bytes(), nil
}

// jsonlIndex maps each id to the distinct canonical contents of its lines,
// in file order.
func jsonlIndex(file []byte) (map[string][][]byte, error) {
	lines, ids, err := jsonlEntries(file)
	if err != nil {
		return nil, err
	}
	idx := make(map[string][][]byte, len(ids))
	for i, id := range ids {
		var canonical bytes.Buffer
		if err := json.Compact(&canonical, lines[i]); err != nil {
			return nil, corruptf("the entry %s is not valid JSON: %v", id, err)
		}
		if !slices.ContainsFunc(idx[id], func(c []byte) bool { return bytes.Equal(c, canonical.Bytes()) }) {
			idx[id] = append(idx[id], canonical.Bytes())
		}
	}
	return idx, nil
}

// jsonlEntries splits a JSONL file into lines and their ids. An id can
// repeat: a union merge of two machines' edits of one entry keeps both
// lines. Status flags the repeat (see jsonlRepeats).
func jsonlEntries(file []byte) (lines [][]byte, ids []string, err error) {
	for n, line := range bytes.Split(file, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var head struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(line, &head); err != nil || head.ID == "" {
			return nil, nil, corruptf("line %d is not a JSON object with an id", n+1)
		}
		lines, ids = append(lines, line), append(ids, head.ID)
	}
	return lines, ids, nil
}

func jsonlGet(file []byte, key string) ([]byte, bool, error) {
	lines, ids, err := jsonlEntries(file)
	if err != nil {
		return nil, false, err
	}
	var entry []byte
	found := false
	for i, id := range ids {
		if id != key {
			continue
		}
		var canonical bytes.Buffer
		if err := json.Compact(&canonical, lines[i]); err != nil {
			return nil, false, corruptf("the entry %s is not valid JSON: %v", key, err)
		}
		if !found || bytes.Compare(canonical.Bytes(), entry) < 0 {
			entry, found = canonical.Bytes(), true
		}
	}
	return entry, found, nil
}

func jsonlPut(file []byte, key string, content []byte, exists bool) ([]byte, error) {
	lines, ids, err := jsonlEntries(file)
	if err != nil {
		return nil, err
	}
	if exists {
		var head struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(content, &head); err != nil || head.ID != key {
			return nil, internalError("writing an entry", fmt.Errorf("the entry is not a JSON object with the id %s", key))
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, content); err != nil {
			return nil, internalError("writing an entry", err)
		}
		content = compact.Bytes()
	}
	// Every line of the entry is replaced by the one written: writing an
	// entry settles a repeat, which only an explicit change does.
	var out bytes.Buffer
	found := false
	for i, line := range lines {
		if ids[i] == key {
			if found || !exists {
				// Later copies of the entity go: one line remains.
				found = true
				continue
			}
			found = true
			line = content
		}
		out.Write(line)
		out.WriteByte('\n')
	}
	if exists && !found {
		out.Write(content)
		out.WriteByte('\n')
	}
	return out.Bytes(), nil
}
