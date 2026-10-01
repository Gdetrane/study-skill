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

// readItem returns an item's content and whether it exists.
func readItem(topic *os.Root, item string) ([]byte, bool, error) {
	file, key, err := parseItem(item)
	if err != nil {
		return nil, false, err
	}
	data, exists, err := readFile(topic, file)
	if err != nil || key == "" || !exists {
		return data, exists, err
	}
	codec, _ := codecFor(file)
	content, exists, err := codec.get(data, key)
	if err != nil {
		return nil, false, corruptf("%s is damaged: %v", file, err)
	}
	return content, exists, nil
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
// A file that merges by union can end up with two lines for one id, when
// the entity was edited on two machines. Such a file stays usable: the line
// with the smallest canonical form is read, so every machine reads the same
// entity whatever order the merge left the lines in, and the next write of
// the entity leaves a single line. Status flags the repeat.
//
// New entities are appended rather than inserted in order: a union merge
// keeps both machines' changes apart only when they touch different parts of
// the file, and two machines appending touch only its end.
var jsonlCodec = fileCodec{get: jsonlGet, put: jsonlPut}

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

// jsonlRepeats returns the ids that appear on more than one line with
// different content, in file order.
func jsonlRepeats(file []byte) []string {
	lines, ids, err := jsonlEntries(file)
	if err != nil {
		return nil
	}
	first := map[string]string{}
	var repeats []string
	for i, id := range ids {
		var canonical bytes.Buffer
		if json.Compact(&canonical, lines[i]) != nil {
			continue
		}
		seen, ok := first[id]
		switch {
		case !ok:
			first[id] = canonical.String()
		case seen != canonical.String() && !slices.Contains(repeats, id):
			repeats = append(repeats, id)
		}
	}
	return repeats
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

// jsonlMatching returns the line for key whose canonical form has the
// version hash, when a union merge left several lines for it.
func jsonlMatching(file []byte, key, hash string) ([]byte, bool) {
	lines, ids, err := jsonlEntries(file)
	if err != nil {
		return nil, false
	}
	for i, id := range ids {
		if id != key {
			continue
		}
		var canonical bytes.Buffer
		if json.Compact(&canonical, lines[i]) == nil && contentHash(canonical.Bytes(), true) == hash {
			return canonical.Bytes(), true
		}
	}
	return nil, false
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
