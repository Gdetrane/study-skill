package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// A change is one write to a Topic: an Event of type Type with payload Data
// that edits Items, the content files named relative to the Topic.
type change struct {
	Type  string
	Data  any
	Items []string
}

// A plan decides the change to make, given the Topic's replayed History and
// its folder. It returns nil when there is nothing to do, which keeps
// operations idempotent: completing a Lesson twice changes nothing.
type plan func(s *replayed, topic *os.Root) (*change, error)

// Points where a test can interrupt a write, as a crash would. See Core.crash.
const (
	crashAfterIntent = "after-intent" // the marker is written, the Event is not
	crashAfterEvent  = "after-event"  // the Event is written, no content is
	crashAfterItem   = "after-item"   // after each item is replaced
	crashBeforeClear = "before-clear" // all content is replaced, the marker remains
)

// writeTopic records one change in a Topic, so that a crash at any point
// leaves something recovery can finish (ADR-0005):
//
//  1. take the Topic's lock, so the CLI and the MCP server never interleave;
//  2. finish any interrupted write, then replay the History and plan;
//  3. leave an intent marker naming the Event;
//  4. append the Event, with each item's hash before and after;
//  5. replace each content file atomically;
//  6. clear the marker.
//
// It returns the Event written, or nil when the plan had nothing to do.
func (c *Core) writeTopic(ctx context.Context, topicID string, p plan) (*event, error) {
	if err := validateTopicID(topicID); err != nil {
		return nil, err
	}
	home, err := c.openHome()
	if err != nil {
		return nil, err
	}
	defer home.Close()
	topic, err := openTopic(home, topicID)
	if err != nil {
		return nil, err
	}
	defer topic.Close()
	unlock, err := lockTopic(ctx, home, topicID)
	if err != nil {
		return nil, err
	}
	defer unlock()

	if err := c.recoverTopic(home, topic, topicID); err != nil {
		return nil, err
	}
	events, err := readEvents(topic, topicID)
	if err != nil {
		return nil, err
	}
	s := replay(events)
	ch, err := p(s, topic)
	if err != nil || ch == nil {
		return nil, err
	}
	ev, contents, err := c.prepareEvent(topic, *ch, nextEventTime(c.now(), s.latest))
	if err != nil {
		return nil, err
	}

	if err := writeIntent(home, intent{Format: FormatVersion, Topic: topicID, Event: ev.ID, Type: ev.Type}); err != nil {
		return nil, err
	}
	if err := c.crashAt(crashAfterIntent); err != nil {
		return nil, err
	}
	if err := c.appendEvent(topic, topicID, ev); err != nil {
		return nil, err
	}
	if err := c.crashAt(crashAfterEvent); err != nil {
		return nil, err
	}
	for i, it := range ev.Items {
		if err := writeItem(topic, it.Item, contents[i]); err != nil {
			return nil, err
		}
		if err := c.crashAt(crashAfterItem); err != nil {
			return nil, err
		}
	}
	if err := c.crashAt(crashBeforeClear); err != nil {
		return nil, err
	}
	if err := clearIntent(home, topicID); err != nil {
		return nil, err
	}
	// The most recent Topic is local convenience state: failing to record
	// it must not turn a successful write into an error.
	_ = c.setRecentTopic(home, topicID)
	return &ev, nil
}

// itemContent is an item's content after an Event, or its absence.
type itemContent struct {
	data   []byte
	exists bool
}

// prepareEvent builds the Event for ch at time at: it runs the Event's
// applier on each item's current content and records both versions.
func (c *Core) prepareEvent(topic *os.Root, ch change, at time.Time) (event, []itemContent, error) {
	kind, ok := eventKinds[ch.Type]
	if !ok {
		return event{}, nil, internalError("recording an Event", fmt.Errorf("unknown Event type %q", ch.Type))
	}
	data, err := json.Marshal(ch.Data)
	if err != nil {
		return event{}, nil, internalError("encoding an Event", err)
	}
	ev := event{Format: FormatVersion, ID: c.newID(), Time: at, Type: ch.Type, Data: data}
	contents := make([]itemContent, 0, len(ch.Items))
	for _, item := range ch.Items {
		current, exists, err := readItem(topic, item)
		if err != nil {
			return event{}, nil, err
		}
		next, keep, err := kind.apply(ev, item, current, exists)
		if err != nil {
			return event{}, nil, err
		}
		ev.Items = append(ev.Items, itemChange{Item: item, Before: contentHash(current, exists), After: contentHash(next, keep)})
		contents = append(contents, itemContent{data: next, exists: keep})
	}
	return ev, contents, nil
}

// recoverTopic finishes a write that was interrupted, if the Topic's intent
// marker says there is one. Because of the lock, at most one Event can be
// unapplied, and only that Event is inspected. For each item it edits:
//
//   - matching the hash after the Event: that part of the write finished;
//   - matching the hash before: the write never reached it, so apply it;
//   - missing or unreadable: stop with an error and keep the Event and the
//     marker, so nothing is lost;
//   - anything else: the learner edited it since, so keep the edit and log it.
func (c *Core) recoverTopic(home, topic *os.Root, topicID string) error {
	m, ok, err := readIntent(home, topicID)
	if err != nil || !ok {
		return err
	}
	events, err := readEvents(topic, topicID)
	if err != nil {
		return err
	}
	var ev *event
	for i := range events {
		if events[i].ID == m.Event {
			ev = &events[i]
			break
		}
	}
	if ev == nil {
		c.log.Info("an interrupted write never reached the History, so there is nothing to finish",
			"topic", topicID, "event", m.Event, "type", m.Type)
		return clearIntent(home, topicID)
	}
	kind, ok := eventKinds[ev.Type]
	if !ok {
		return corruptf("Topic %s has an interrupted %s write that this version of study cannot finish: upgrade study", topicID, ev.Type)
	}
	for _, it := range ev.Items {
		current, exists, err := readItem(topic, it.Item)
		if err != nil {
			return err
		}
		switch hash := contentHash(current, exists); {
		case hash == it.After:
			continue
		case hash == it.Before:
			next, keep, err := kind.apply(*ev, it.Item, current, exists)
			if err != nil {
				return err
			}
			if contentHash(next, keep) != it.After {
				return corruptf("Event %s in the History of %s does not produce the version of %s it recorded, "+
					"so the interrupted write cannot be finished", ev.ID, topicID, it.Item)
			}
			if err := writeItem(topic, it.Item, itemContent{data: next, exists: keep}); err != nil {
				return err
			}
			c.log.Info("finished an interrupted write", "topic", topicID, "event", ev.ID, "item", it.Item)
		case !exists:
			return corruptf("%s in Topic %s is missing, so an interrupted write (Event %s) cannot be finished: "+
				"restore it, for example from the last Checkpoint, and try again", it.Item, topicID, ev.ID)
		default:
			if err := validateItem(it.Item, current); err != nil {
				return corruptf("%s in Topic %s cannot be read (%v), so an interrupted write (Event %s) cannot be finished: "+
					"fix or restore it and try again", it.Item, topicID, err, ev.ID)
			}
			c.log.Warn("kept a hand edit made after an interrupted write", "topic", topicID, "event", ev.ID, "item", it.Item)
		}
	}
	return clearIntent(home, topicID)
}

func (c *Core) crashAt(point string) error {
	if c.crash == nil {
		return nil
	}
	return c.crash(point)
}

// isTopic reports whether a folder in the Study home is a Topic: it has
// settings or a History. A Topic whose topic.toml is missing is damaged,
// not gone.
func isTopic(home *os.Root, name string) bool {
	for _, file := range []string{topicFile, historyFile} {
		_, err := home.Lstat(filepath.Join(name, file))
		// A folder that cannot be read (such as permission denied) counts as
		// a Topic, so it is reported as unreadable instead of disappearing.
		if err == nil || !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
			return true
		}
	}
	return false
}

func noSuchTopic(topicID string) error {
	return &Error{Code: CodeNotFound, Message: "there is no Topic named " + topicID + ": run study status to list your Topics"}
}

// openTopic opens an existing Topic's folder.
func openTopic(home *os.Root, topicID string) (*os.Root, error) {
	if !isTopic(home, topicID) {
		return nil, noSuchTopic(topicID)
	}
	topic, err := home.OpenRoot(topicID)
	if err != nil {
		return nil, internalError("opening Topic "+topicID, err)
	}
	return topic, nil
}

// readItem returns an item's content and whether it exists. Items are
// regular files inside the Topic: a symbolic link is refused, so an agent
// cannot point Lamplight at a file elsewhere.
func readItem(topic *os.Root, item string) ([]byte, bool, error) {
	info, err := topic.Lstat(item)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, internalError("reading "+item, err)
	}
	if !info.Mode().IsRegular() {
		return nil, false, corruptf("%s is not a regular file: Lamplight does not follow links inside a Topic", item)
	}
	data, err := topic.ReadFile(item)
	if err != nil {
		return nil, false, internalError("reading "+item, err)
	}
	return data, true, nil
}

// writeItem replaces an item atomically, or removes it.
func writeItem(topic *os.Root, item string, content itemContent) error {
	if !content.exists {
		if err := topic.Remove(item); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return internalError("removing "+item, err)
		}
		syncDir(topic, filepath.Dir(item))
		return nil
	}
	if dir := filepath.Dir(item); dir != "." {
		if err := topic.MkdirAll(dir, 0o755); err != nil {
			return internalError("creating "+dir, err)
		}
	}
	return writeFileAtomic(topic, item, content.data)
}

// validateItem checks that an item's content can be read, so recovery can
// tell a hand edit from a damaged file.
func validateItem(item string, data []byte) error {
	switch item {
	case topicFile:
		_, err := parseTopicSettings(data, item)
		return err
	}
	return nil
}

// intent is the marker a write leaves in the Study home's .lamplight/intents
// while it runs. It is local to the machine and never synced. If it is still
// there when the next write starts, the write was interrupted and recovery
// finishes it.
type intent struct {
	Format int    `json:"format"`
	Topic  string `json:"topic"`
	Event  string `json:"event"`
	Type   string `json:"type"`
}

func intentPath(topicID string) string { return filepath.Join(localDir, "intents", topicID+".json") }

func writeIntent(home *os.Root, m intent) error {
	if err := home.MkdirAll(filepath.Dir(intentPath(m.Topic)), 0o755); err != nil {
		return internalError("creating "+filepath.Dir(intentPath(m.Topic)), err)
	}
	data, err := json.Marshal(m)
	if err != nil {
		return internalError("encoding an intent marker", err)
	}
	return writeFileAtomic(home, intentPath(m.Topic), append(data, '\n'))
}

func readIntent(home *os.Root, topicID string) (intent, bool, error) {
	path := intentPath(topicID)
	data, err := home.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return intent{}, false, nil
	}
	if err != nil {
		return intent{}, false, internalError("reading "+path, err)
	}
	var m intent
	if err := json.Unmarshal(data, &m); err != nil || m.Event == "" {
		return intent{}, false, corruptf("the intent marker %s is damaged, so an interrupted write to Topic %s cannot be checked: "+
			"look at the end of the Topic's History, then delete the marker", path, topicID)
	}
	if m.Format > FormatVersion {
		return intent{}, false, newerFormat(path, m.Format)
	}
	return m, true, nil
}

func clearIntent(home *os.Root, topicID string) error {
	if err := home.Remove(intentPath(topicID)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return internalError("clearing the intent marker of "+topicID, err)
	}
	syncDir(home, filepath.Dir(intentPath(topicID)))
	return nil
}

// hasIntent reports whether a write to the Topic was interrupted.
func hasIntent(home *os.Root, topicID string) bool {
	_, err := home.Lstat(intentPath(topicID))
	return err == nil
}
