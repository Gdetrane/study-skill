package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"time"
)

const historyFile = "history.jsonl"

// clockTick is the smallest step of the hybrid logical clock. Event times
// are kept to the microsecond, so they read well in the History and survive
// a JSON round trip unchanged.
const clockTick = time.Microsecond

// event is one entry in a Topic's History. It carries everything needed to
// apply it, so recovery can finish an interrupted write from the History
// alone (ADR-0005).
//
// TODO(#26): Card IDs are <lesson-id>.<random suffix>, and Explore Cards use
// explore.<random suffix>, so two machines adding Cards never collide.
type event struct {
	Format int    `json:"format"`
	ID     string `json:"id"`
	// Time comes from a hybrid logical clock (see nextEventTime), so an
	// Event written after another was read always sorts after it.
	Time time.Time `json:"time"`
	Type string    `json:"type"`
	// Data is the Event's full payload: what an applier needs to rewrite
	// the items the Event edits.
	Data json.RawMessage `json:"data,omitempty"`
	// Items are the content files the Event edits, each with its hash
	// before and after.
	Items []itemChange `json:"items,omitempty"`

	// raw is the Event's line in the History, used to tell a repeated
	// Event from two different Events that share an ID.
	raw []byte
}

// itemChange records how an Event changes one item: one content file today
// (topic.toml, and later a Lesson file or the Syllabus), or one Card in
// cards.jsonl once Cards exist (#26). A hash is empty when the item does not
// exist, so an Event that creates an item has no Before and one that
// deletes it has no After.
type itemChange struct {
	Item   string `json:"item"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

// contentHash identifies one version of an item. It is empty for an item
// that does not exist.
func contentHash(data []byte, exists bool) string {
	if !exists {
		return ""
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// nextEventTime is the hybrid logical clock: the wall clock, unless the
// History already holds an Event at or after it (another machine's clock was
// ahead, or Events came quickly), in which case one tick after that Event.
func nextEventTime(wall, latest time.Time) time.Time {
	wall = wall.UTC().Truncate(clockTick)
	if wall.After(latest) {
		return wall
	}
	return latest.Add(clockTick)
}

// appendEvent writes ev as one line at the end of the Topic's History. A last
// line without a newline is left by an interrupted write: it is truncated
// first, and the fragment is logged.
func (c *Core) appendEvent(topic *os.Root, topicID string, ev event) error {
	line, err := json.Marshal(ev)
	if err != nil {
		return internalError("encoding an Event", err)
	}
	f, err := topic.OpenFile(historyFile, os.O_RDWR|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return internalError("opening the History of "+topicID, err)
	}
	if err := c.dropFragment(f, topicID); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return internalError("writing the History of "+topicID, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return internalError("syncing the History of "+topicID, err)
	}
	if err := f.Close(); err != nil {
		return internalError("closing the History of "+topicID, err)
	}
	return nil
}

// dropFragment truncates the History after its last newline.
func (c *Core) dropFragment(f *os.File, topicID string) error {
	info, err := f.Stat()
	if err != nil {
		return internalError("reading the History of "+topicID, err)
	}
	size := info.Size()
	if size == 0 {
		return nil
	}
	last := make([]byte, 1)
	if _, err := f.ReadAt(last, size-1); err != nil {
		return internalError("reading the History of "+topicID, err)
	}
	if last[0] == '\n' {
		return nil
	}
	data := make([]byte, size)
	if _, err := f.ReadAt(data, 0); err != nil {
		return internalError("reading the History of "+topicID, err)
	}
	keep := int64(bytes.LastIndexByte(data, '\n') + 1)
	if err := f.Truncate(keep); err != nil {
		return internalError("repairing the History of "+topicID, err)
	}
	c.log.Warn("dropped the unfinished last line of a History, left by an interrupted write",
		"topic", topicID, "fragment", string(data[keep:]))
	return nil
}

// readEvents returns the Topic's Events ordered by time, then ID, never by
// their position in the file. A last line without a newline is an interrupted
// write and is ignored.
func readEvents(topic *os.Root, topicID string) ([]event, error) {
	data, err := topic.ReadFile(historyFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, internalError("reading the History of "+topicID, err)
	}
	if i := bytes.LastIndexByte(data, '\n'); i < len(data)-1 {
		data = data[:i+1]
	}
	var events []event
	for n, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev event
		if err := json.Unmarshal(line, &ev); err != nil {
			return nil, corruptf("line %d of the History of %s is not valid JSON: %v", n+1, topicID, err)
		}
		if ev.Format > FormatVersion {
			return nil, newerFormat(fmt.Sprintf("the History of %s", topicID), ev.Format)
		}
		if ev.ID == "" || ev.Type == "" || ev.Time.IsZero() {
			return nil, corruptf("line %d of the History of %s is not an Event: it lacks an id, a type or a time", n+1, topicID)
		}
		ev.raw = line
		events = append(events, ev)
	}
	sort.SliceStable(events, func(i, j int) bool {
		if !events[i].Time.Equal(events[j].Time) {
			return events[i].Time.Before(events[j].Time)
		}
		if events[i].ID != events[j].ID {
			return events[i].ID < events[j].ID
		}
		// The same ID twice: order the copies by content, so the result
		// never depends on the order lines were merged in.
		return bytes.Compare(events[i].raw, events[j].raw) < 0
	})
	return events, nil
}
