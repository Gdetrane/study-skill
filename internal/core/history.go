package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"time"
)

const historyFile = "history.jsonl"

// Event types recorded in the History.
const (
	eventTopicCreated = "topic.created"
)

// event is one entry in a Topic's History. This is the minimal shape for the
// tracer bullet; the History engine (#21) adds the hybrid logical clock, the
// intent marker and the before and after hashes from ADR-0005.
type event struct {
	Format int             `json:"format"`
	ID     string          `json:"id"`
	Time   time.Time       `json:"time"`
	Type   string          `json:"type"`
	Data   json.RawMessage `json:"data,omitempty"`
}

// appendEvent writes ev as one line at the end of the Topic's History.
func appendEvent(topic *os.Root, ev event) error {
	line, err := json.Marshal(ev)
	if err != nil {
		return internalError("encoding an Event", err)
	}
	f, err := topic.OpenFile(historyFile, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return internalError("opening the History", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return internalError("writing the History", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return internalError("syncing the History", err)
	}
	if err := f.Close(); err != nil {
		return internalError("closing the History", err)
	}
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
			return nil, invalidf("line %d of the History of %s is not valid JSON: %v", n+1, topicID, err)
		}
		if ev.Format > FormatVersion {
			return nil, newerFormat(fmt.Sprintf("the History of %s", topicID), ev.Format)
		}
		events = append(events, ev)
	}
	sort.SliceStable(events, func(i, j int) bool {
		if !events[i].Time.Equal(events[j].Time) {
			return events[i].Time.Before(events[j].Time)
		}
		return events[i].ID < events[j].ID
	})
	return events, nil
}
