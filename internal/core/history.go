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
// alone (ADR-0005). One line of history.jsonl looks like this:
//
//	{"format":1,"id":"k3…","time":"2026-10-01T09:30:00.000001Z",
//	 "wall":"2026-10-01T09:30:00Z","type":"topic.updated","data":{"title":"C"},
//	 "items":[{"item":"topic.toml","before":"sha256:…","after":"sha256:…"}]}
//
// Versioning: format is the History's format; a binary keeps reading a
// History with Events newer than it understands, but refuses to write to
// it. An Event type's payload never changes shape within a format: a new
// shape is a new type name, so an old payload is never reinterpreted.
type event struct {
	Format int    `json:"format"`
	ID     string `json:"id"`
	// Time comes from a hybrid logical clock (see nextEventTime), so an
	// Event written after another was read always sorts after it. It orders
	// Events and nothing else.
	Time time.Time `json:"time"`
	// Wall is the writer's own clock when the Event was written. Domain
	// logic that needs real time, such as Card scheduling, uses it.
	Wall time.Time `json:"wall"`
	Type string    `json:"type"`
	// Data is the Event's full payload: what an applier needs to rewrite
	// the items the Event edits.
	Data json.RawMessage `json:"data,omitempty"`
	// Items are the items the Event edits (see items.go), each with its
	// hash before and after.
	Items []itemChange `json:"items,omitempty"`

	// raw is the Event's line in the History, used to tell a repeated
	// Event from two different Events that share an ID.
	raw []byte
}

// itemChange records how an Event changes one item. A hash is empty when the
// item does not exist, so an Event that creates an item has no Before and
// one that deletes it has no After.
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

// historyLog is a Topic's History as read from disk.
type historyLog struct {
	// events are the Events this binary understands, ordered by time, then
	// ID, then content, never by their position in the file.
	events []event
	// newer are Events written by a newer version of study. They are held,
	// and the Topic can be read but not written.
	newer []event
	// damaged are lines that are not Events, such as a line cut off by an
	// interrupted write that a union merge moved into the middle of the
	// file. They are skipped and flagged, never deleted.
	damaged []damagedLine
}

type damagedLine struct {
	line   int
	text   []byte
	reason string
}

// errNewerEvent marks an Event written by a newer version of study.
var errNewerEvent = errors.New("written by a newer version of study")

// parseEvent reads one line of the History.
func parseEvent(line []byte) (event, error) {
	if !json.Valid(line) {
		return event{}, errors.New("it is not valid JSON")
	}
	// The format number is read on its own first: a newer format may change
	// the type of any other field, and its Events must still be recognised
	// as newer, never as damaged lines this binary could write past.
	var head struct {
		Format int `json:"format"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return event{}, fmt.Errorf("it is not an Event: %v", err)
	}
	if head.Format > FormatVersion {
		ev := event{Format: head.Format, raw: line}
		var id struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(line, &id) == nil {
			ev.ID = id.ID
		}
		var at struct {
			Time time.Time `json:"time"`
		}
		if json.Unmarshal(line, &at) == nil {
			ev.Time = at.Time
		}
		return ev, errNewerEvent
	}
	var ev event
	if err := json.Unmarshal(line, &ev); err != nil {
		return ev, fmt.Errorf("it is not an Event: %v", err)
	}
	if ev.Format < 1 {
		return ev, errors.New("it has no format number")
	}
	if ev.ID == "" || ev.Type == "" || ev.Time.IsZero() || ev.Wall.IsZero() {
		return ev, errors.New("it lacks an id, a type, a time or a wall time")
	}
	ev.raw = line
	return ev, nil
}

// readHistory reads a Topic's History. A last line without a newline is
// either a complete Event whose newline is missing, which is kept, or an
// interrupted append, which is ignored; repairHistoryTail fixes either.
func readHistory(topic *os.Root, topicID string) (historyLog, error) {
	var h historyLog
	data, err := topic.ReadFile(historyFile)
	if errors.Is(err, fs.ErrNotExist) {
		return h, nil
	}
	if err != nil {
		return h, internalError("reading the History of "+topicID, err)
	}
	body, tail := splitTail(data)
	lines := bytes.Split(body, []byte{'\n'})
	if len(tail) > 0 {
		if _, err := parseEvent(tail); err == nil || errors.Is(err, errNewerEvent) {
			lines = append(lines, tail)
		}
	}
	for n, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		ev, err := parseEvent(line)
		switch {
		case errors.Is(err, errNewerEvent):
			h.newer = append(h.newer, ev)
		case err != nil:
			h.damaged = append(h.damaged, damagedLine{line: n + 1, text: line, reason: err.Error()})
		default:
			h.events = append(h.events, ev)
		}
	}
	sort.SliceStable(h.events, func(i, j int) bool {
		a, b := h.events[i], h.events[j]
		if !a.Time.Equal(b.Time) {
			return a.Time.Before(b.Time)
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		// The same ID twice: order the copies by content, so the result
		// never depends on the order lines were merged in.
		return bytes.Compare(a.raw, b.raw) < 0
	})
	return h, nil
}

// splitTail splits data after its last newline.
func splitTail(data []byte) (body, tail []byte) {
	i := bytes.LastIndexByte(data, '\n')
	return data[:i+1], data[i+1:]
}

// appendEvent writes ev as one line at the end of the Topic's History, after
// repairing a last line left without its newline.
func (c *Core) appendEvent(topic *os.Root, topicID string, ev event) error {
	line, err := json.Marshal(ev)
	if err != nil {
		return internalError("encoding an Event", err)
	}
	f, err := topic.OpenFile(historyFile, os.O_RDWR|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return internalError("opening the History of "+topicID, err)
	}
	if err := c.repairTail(f, topicID); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return internalError("writing the History of "+topicID, err)
	}
	if err := syncFile(f); err != nil {
		_ = f.Close()
		return internalError("syncing the History of "+topicID, err)
	}
	if err := f.Close(); err != nil {
		return internalError("closing the History of "+topicID, err)
	}
	return nil
}

// repairHistoryTail repairs the last line of a Topic's History if it lacks
// its newline. The caller holds the Topic lock.
func (c *Core) repairHistoryTail(topic *os.Root, topicID string) error {
	f, err := topic.OpenFile(historyFile, os.O_RDWR|os.O_APPEND, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return internalError("opening the History of "+topicID, err)
	}
	if err := c.repairTail(f, topicID); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return internalError("closing the History of "+topicID, err)
	}
	return nil
}

// repairTail fixes a last line without a newline, in f opened for appending.
// A complete Event only lost its newline, perhaps to an editor, so the
// newline is added back. Anything else is an append interrupted part way:
// it is truncated, and the fragment is logged.
func (c *Core) repairTail(f *os.File, topicID string) error {
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
	body, tail := splitTail(data)
	if _, err := parseEvent(tail); err == nil || errors.Is(err, errNewerEvent) {
		if _, err := f.Write([]byte{'\n'}); err != nil {
			return internalError("repairing the History of "+topicID, err)
		}
		if err := syncFile(f); err != nil {
			return internalError("syncing the History of "+topicID, err)
		}
		c.log.Info("added the missing newline after the last Event of a History", "topic", topicID)
		return nil
	}
	if err := f.Truncate(int64(len(body))); err != nil {
		return internalError("repairing the History of "+topicID, err)
	}
	if err := syncFile(f); err != nil {
		return internalError("syncing the History of "+topicID, err)
	}
	c.log.Warn("dropped the unfinished last line of a History, left by an interrupted write",
		"topic", topicID, "fragment", string(tail))
	return nil
}

// historyTailPending reports whether the History's last line lacks its
// newline, which the next write or Checkpoint repairs.
func historyTailPending(topic *os.Root) bool {
	info, err := topic.Stat(historyFile)
	if err != nil || info.Size() == 0 {
		return false
	}
	f, err := topic.Open(historyFile)
	if err != nil {
		return false
	}
	defer f.Close()
	last := make([]byte, 1)
	if _, err := f.ReadAt(last, info.Size()-1); err != nil {
		return false
	}
	return last[0] != '\n'
}

// syncFile flushes a file to disk; tests replace it to check that writes
// are synced.
var syncFile = (*os.File).Sync
