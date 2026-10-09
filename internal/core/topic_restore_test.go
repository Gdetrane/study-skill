package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

// removedTopic is a Study home with one Topic, rust, removed from it.
func removedTopic(t *testing.T) (m *machine, removed TopicRemoval) {
	t.Helper()
	gitIdentity(t)
	m = newMachine(t, t.TempDir(), "id", t0)
	if _, err := m.CreateTopic(context.Background(), TopicSpec{Title: "Rust", Goal: "Write a CLI"}); err != nil {
		t.Fatal(err)
	}
	return m, m.remove(t, "rust")
}

func (m *machine) remove(t *testing.T, topicID string) TopicRemoval {
	t.Helper()
	removed, err := m.RemoveTopic(context.Background(), topicID, false)
	if err != nil {
		t.Fatal(err)
	}
	return removed
}

// dropRecord deletes the record of a removal: the folder stays, and only
// its name says what it was.
func dropRecord(t *testing.T, removed TopicRemoval) {
	t.Helper()
	if err := os.Remove(removed.MovedTo + ".json"); err != nil {
		t.Fatal(err)
	}
}

// Restoring a removed Topic moves its folder back under its id, unchanged:
// nothing inside it is written, the History least of all. The dry run
// reports the same and moves nothing.
func TestRestoreTopicBringsItBackUnchanged(t *testing.T) {
	ctx := context.Background()
	gitIdentity(t)
	home := t.TempDir()
	m := newMachine(t, home, "id", t0)
	if _, err := m.CreateTopic(ctx, TopicSpec{Title: "Rust", Goal: "Write a CLI"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "rust", "notes.md"), []byte("ownership\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Checkpoint(ctx, CheckpointSpec{Topic: "rust", Role: "learner"}); err != nil {
		t.Fatal(err)
	}
	before := treeHash(t, filepath.Join(home, "rust"))
	removed := m.remove(t, "rust")
	m.setClock(t0.Add(time.Hour))

	dry, err := m.RestoreTopic(ctx, TopicRestoreSpec{Topic: "rust", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	want := TopicRestore{Topic: "rust", Path: filepath.Join(home, "rust"), RestoredFrom: removed.MovedTo, Removed: t0, DryRun: true}
	if dry != want {
		t.Fatalf("dry run = %+v, want %+v", dry, want)
	}
	if exists(filepath.Join(home, "rust")) || !exists(removed.MovedTo) || !exists(removed.MovedTo+".json") {
		t.Fatal("the dry run moved the Topic")
	}

	got, err := m.RestoreTopic(ctx, TopicRestoreSpec{Topic: "rust"})
	if err != nil {
		t.Fatal(err)
	}
	want.DryRun = false
	if got != want {
		t.Fatalf("restore = %+v, the dry run said %+v", got, dry)
	}
	if after := treeHash(t, filepath.Join(home, "rust")); after != before {
		t.Error("the restored Topic is not what was removed: restoring wrote inside it")
	}
	if exists(removed.MovedTo) || exists(removed.MovedTo+".json") {
		t.Error("the removal's folder or record is still in .lamplight/removed")
	}
	if list, err := m.ListRemovedTopics(ctx); err != nil || len(list.Removed) != 0 {
		t.Errorf("removed Topics after the restore = %+v, %v", list, err)
	}
	status, err := m.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Topics) != 1 || status.Topics[0].Goal != "Write a CLI" || len(status.Topics[0].Flags) != 0 ||
		len(status.Problems) != 0 || status.ActiveTopic == nil || status.ActiveTopic.ID != "rust" {
		t.Fatalf("status after the restore = %+v", status)
	}
	// It is a Topic like any other again.
	goal := "Write a web server"
	if _, err := m.UpdateTopic(ctx, "rust", TopicChanges{Goal: &goal}); err != nil {
		t.Fatal(err)
	}
}

// By its id alone, the newest removal of a Topic is restored; --from names
// another. The list shows them newest first, each with the command that
// restores it.
func TestRestoreTopicTakesTheNewestRemovalUnlessTold(t *testing.T) {
	ctx := context.Background()
	m, first := removedTopic(t)
	if _, err := m.CreateTopic(ctx, TopicSpec{Title: "Rust", Goal: "Write a web server"}); err != nil {
		t.Fatal(err)
	}
	m.setClock(t0.Add(time.Hour))
	second := m.remove(t, "rust")
	if _, err := m.CreateTopic(ctx, TopicSpec{Title: "Go", Goal: "Write a scheduler"}); err != nil {
		t.Fatal(err)
	}
	m.setClock(t0.Add(2 * time.Hour))
	other := m.remove(t, "go")

	list, err := m.ListRemovedTopics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []RemovedTopic{
		{Topic: "go", Folder: "20261001-113000-go", Path: other.MovedTo, Removed: t0.Add(2 * time.Hour), Restore: other.Restore},
		{Topic: "rust", Folder: "20261001-103000-rust", Path: second.MovedTo, Removed: t0.Add(time.Hour), Restore: second.Restore},
		{Topic: "rust", Folder: "20261001-093000-rust", Path: first.MovedTo, Removed: t0, Restore: first.Restore},
	}
	if !reflect.DeepEqual(list.Removed, want) {
		t.Fatalf("removed Topics = %+v\nwant %+v", list.Removed, want)
	}

	got, err := m.RestoreTopic(ctx, TopicRestoreSpec{Topic: "rust"})
	if err != nil || got.RestoredFrom != second.MovedTo {
		t.Fatalf("restore by id = %+v, %v; want the newest removal, %s", got, err, second.MovedTo)
	}
	if goal := readSettings(t, filepath.Join(m.home, "rust")).Goal; goal != "Write a web server" {
		t.Errorf("the restored Topic's goal = %q", goal)
	}

	// Swapping them, as the refusal advises: the Topic in place is removed,
	// and the first removal is named by its folder.
	_, err = m.RestoreTopic(ctx, TopicRestoreSpec{Topic: "rust", From: filepath.Base(first.MovedTo)})
	if CodeOf(err) != CodeAlreadyExists || !strings.Contains(err.Error(), "study topic remove rust, then "+first.Restore) {
		t.Fatalf("restoring over a Topic = %v, want already_exists with the way to swap them", err)
	}
	m.setClock(t0.Add(3 * time.Hour))
	m.remove(t, "rust")
	got, err = m.RestoreTopic(ctx, TopicRestoreSpec{Topic: "rust", From: filepath.Base(first.MovedTo)})
	if err != nil || got.RestoredFrom != first.MovedTo || !got.Removed.Equal(t0) {
		t.Fatalf("restore --from = %+v, %v; want %s", got, err, first.MovedTo)
	}
	if goal := readSettings(t, filepath.Join(m.home, "rust")).Goal; goal != "Write a CLI" {
		t.Errorf("the restored Topic's goal = %q", goal)
	}
	if left := removedFolders(t, m.home); len(left) != 2 {
		t.Errorf("removals left = %v, want the web server's and go's", left)
	}
}

// Two removals within one second are told apart by their records, which
// hold the time more exactly than the folders' names.
func TestRestoreTopicOrdersRemovalsWithinOneSecond(t *testing.T) {
	ctx := context.Background()
	m, first := removedTopic(t)
	if _, err := m.CreateTopic(ctx, TopicSpec{Title: "Rust", Goal: "Write a web server"}); err != nil {
		t.Fatal(err)
	}
	m.setClock(t0.Add(300 * time.Millisecond))
	second := m.remove(t, "rust")
	if filepath.Dir(second.MovedTo) != filepath.Dir(first.MovedTo) || !strings.HasPrefix(filepath.Base(second.MovedTo), filepath.Base(first.MovedTo)+"-") {
		t.Fatalf("the second removal went to %s, want %s plus a suffix", second.MovedTo, first.MovedTo)
	}
	got, err := m.RestoreTopic(ctx, TopicRestoreSpec{Topic: "rust"})
	if err != nil || got.RestoredFrom != second.MovedTo {
		t.Fatalf("restore = %+v, %v; want the later removal, %s", got, err, second.MovedTo)
	}
}

func TestRestoreTopicRefusals(t *testing.T) {
	ctx := context.Background()
	m, removed := removedTopic(t)
	folder := filepath.Base(removed.MovedTo)
	if _, err := m.CreateTopic(ctx, TopicSpec{Title: "Go", Goal: "Write a scheduler"}); err != nil {
		t.Fatal(err)
	}
	goRemoved := m.remove(t, "go")

	for _, tc := range []struct {
		name string
		spec TopicRestoreSpec
		code ErrorCode
		says string
	}{
		{"a Topic never removed", TopicRestoreSpec{Topic: "haskell"}, CodeNotFound, "study topic restore --list"},
		{"no id", TopicRestoreSpec{}, CodeInvalidArgument, "name the Topic"},
		{"a path for an id", TopicRestoreSpec{Topic: "../rust"}, CodeInvalidArgument, "not a valid Topic id"},
		{"a path for a folder", TopicRestoreSpec{Topic: "rust", From: removed.MovedTo}, CodeInvalidArgument, "not the name of a removed Topic's folder"},
		{"a folder name that leads elsewhere", TopicRestoreSpec{Topic: "rust", From: "../../rust"}, CodeInvalidArgument, "not the name of a removed Topic's folder"},
		{"an id for a folder", TopicRestoreSpec{Topic: "rust", From: "rust"}, CodeInvalidArgument, "study topic restore --list"},
		{"a folder that is not there", TopicRestoreSpec{Topic: "rust", From: "20250101-000000-rust"}, CodeNotFound, "no removed Topic is kept in a folder named 20250101-000000-rust"},
		{"another Topic's folder", TopicRestoreSpec{Topic: "rust", From: filepath.Base(goRemoved.MovedTo)}, CodeInvalidArgument,
			"holds Topic go, not rust: to restore it, run " + goRemoved.Restore},
	} {
		for _, dryRun := range []bool{true, false} {
			tc.spec.DryRun = dryRun
			if _, err := m.RestoreTopic(ctx, tc.spec); CodeOf(err) != tc.code || !strings.Contains(err.Error(), tc.says) {
				t.Errorf("%s (dry run %v): %v, want %s saying %q", tc.name, dryRun, err, tc.code, tc.says)
			}
		}
	}

	// A restore refused before it starts waits for no lock, and leaves none
	// behind for an id that names nothing.
	if lock := filepath.Join(m.home, ".lamplight", "locks", "haskell.lock"); exists(lock) {
		t.Errorf("the refused restore of a Topic never removed left %s", lock)
	}

	// A write marker under the id, with no Topic to finish the write in,
	// would become the restored Topic's.
	leaveIntent(t, m.home, "rust")
	for _, dryRun := range []bool{true, false} {
		_, err := m.RestoreTopic(ctx, TopicRestoreSpec{Topic: "rust", DryRun: dryRun})
		if CodeOf(err) != CodeFailedPrecondition || !strings.Contains(err.Error(), "interrupted") ||
			!strings.Contains(err.Error(), filepath.Join(m.home, ".lamplight", "intents", "rust.json")) {
			t.Errorf("a stray write marker (dry run %v): %v", dryRun, err)
		}
	}
	if err := os.Remove(filepath.Join(m.home, ".lamplight", "intents", "rust.json")); err != nil {
		t.Fatal(err)
	}

	// Something that is not a Topic has the id.
	if err := os.WriteFile(filepath.Join(m.home, "rust"), []byte("notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, dryRun := range []bool{true, false} {
		_, err := m.RestoreTopic(ctx, TopicRestoreSpec{Topic: "rust", DryRun: dryRun})
		if CodeOf(err) != CodeAlreadyExists || !strings.Contains(err.Error(), "is not a Topic") || !strings.Contains(err.Error(), removed.Restore) {
			t.Errorf("a file under the id (dry run %v): %v", dryRun, err)
		}
	}
	if err := os.Remove(filepath.Join(m.home, "rust")); err != nil {
		t.Fatal(err)
	}

	// Every refusal left the removal as it was, and it still restores.
	if !exists(filepath.Join(removed.MovedTo, "topic.toml")) || !exists(removed.MovedTo+".json") {
		t.Fatal("a refused restore moved the removal")
	}
	if _, err := m.RestoreTopic(ctx, TopicRestoreSpec{Topic: "rust", From: folder}); err != nil {
		t.Fatal(err)
	}

	// A Study home that does not exist has no removed Topics.
	gone := newMachine(t, filepath.Join(t.TempDir(), "nowhere"), "id", t0)
	if _, err := gone.RestoreTopic(ctx, TopicRestoreSpec{Topic: "rust"}); CodeOf(err) != CodeNotFound {
		t.Errorf("without a Study home: %v", err)
	}
	if list, err := gone.ListRemovedTopics(ctx); err != nil || list.Removed == nil || len(list.Removed) != 0 {
		t.Errorf("removed Topics without a Study home = %+v, %v", list, err)
	}
}

// restoreRace is a removed Topic two machines share a Study home over: one
// restores it, the other does something else under its id meanwhile.
type restoreRace struct {
	home            string
	restorer, other *machine
	removed         TopicRemoval
	removedHash     string
}

func newRestoreRace(t *testing.T) restoreRace {
	t.Helper()
	m, removed := removedTopic(t)
	return restoreRace{home: m.home, restorer: m, other: newMachine(t, m.home, "o", t0), removed: removed,
		removedHash: treeHash(t, removed.MovedTo)}
}

// create makes another Topic under the removed one's id, and returns the
// hash of its folder.
func (r restoreRace) create(t *testing.T) string {
	t.Helper()
	if _, err := r.other.CreateTopic(context.Background(), TopicSpec{Title: "Rust", Goal: "A new start"}); err != nil {
		t.Errorf("CreateTopic: %v", err)
	}
	return treeHash(t, filepath.Join(r.home, "rust"))
}

// untouched checks that a refused restore left both Topics as they were: the
// one that took the id, and the removed one, in its folder with its record,
// never inside the other.
func (r restoreRace) untouched(t *testing.T, createdHash string) {
	t.Helper()
	if got := treeHash(t, filepath.Join(r.home, "rust")); got != createdHash {
		t.Error("the Topic created under the id was changed by the restore")
	}
	if goal := readSettings(t, filepath.Join(r.home, "rust")).Goal; goal != "A new start" {
		t.Errorf("the Topic under the id has goal %q", goal)
	}
	if !exists(r.removed.MovedTo+".json") || treeHash(t, r.removed.MovedTo) != r.removedHash {
		t.Error("the removed Topic or its record was moved or changed")
	}
	status, err := r.restorer.Status(context.Background())
	if err != nil || len(status.Topics) != 1 || len(status.Topics[0].Flags) != 0 || len(status.Problems) != 0 {
		t.Errorf("status = %+v, %v", status, err)
	}
}

// The reason study topic restore exists (issue #62): a Topic created under
// the id while the restore waits for the id's lock is left untouched, and
// the restore then refuses with advice. The mv it replaces would have put the
// removed Topic inside the new one.
func TestATopicCreatedWhileARestoreWaitsForTheLockIsLeftUntouched(t *testing.T) {
	ctx := context.Background()
	r := newRestoreRace(t)

	// Another process holds the lock of the id.
	root, err := os.OpenRoot(r.home)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	unlock, err := lockTopic(ctx, root, "rust")
	if err != nil {
		t.Fatal(err)
	}
	waiting := make(chan struct{})
	r.restorer.crash = atPoint(crashBeforeLock, func() { close(waiting) })
	restored := make(chan error, 1)
	go func() {
		_, err := r.restorer.RestoreTopic(ctx, TopicRestoreSpec{Topic: "rust"})
		restored <- err
	}()
	select {
	case <-waiting:
	case err := <-restored:
		unlock()
		t.Fatalf("the restore did not reach the lock: %v", err)
	}
	// It cannot go on until the lock is released, and by then the id is taken.
	createdHash := r.create(t)
	select {
	case err := <-restored:
		unlock()
		t.Fatalf("the restore did not wait for the lock: %v", err)
	default:
	}
	unlock()

	err = <-restored
	if CodeOf(err) != CodeAlreadyExists || !strings.Contains(err.Error(), "another Topic is named rust now") ||
		!strings.Contains(err.Error(), r.removed.Restore) {
		t.Fatalf("the restore after the lock = %v, want already_exists with advice", err)
	}
	r.untouched(t, createdHash)
}

// Creating a Topic takes no lock, so one can also appear after the restore
// checked the id, just before it moves the folder. The move is refused
// then, by the file system, and nothing is nested.
func TestATopicCreatedJustBeforeARestoreMovesIsLeftUntouched(t *testing.T) {
	ctx := context.Background()
	r := newRestoreRace(t)
	var createdHash string
	r.restorer.crash = atPoint(crashRestoreChecked, func() { createdHash = r.create(t) })

	_, err := r.restorer.RestoreTopic(ctx, TopicRestoreSpec{Topic: "rust"})
	if CodeOf(err) != CodeAlreadyExists || !strings.Contains(err.Error(), "appeared while") {
		t.Fatalf("the restore = %v, want already_exists", err)
	}
	r.untouched(t, createdHash)
}

// The other order: once the Topic is restored, creating one under its id is
// refused as for any Topic.
func TestCreatingATopicUnderARestoredIDIsRefused(t *testing.T) {
	ctx := context.Background()
	r := newRestoreRace(t)
	if _, err := r.restorer.RestoreTopic(ctx, TopicRestoreSpec{Topic: "rust"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.other.CreateTopic(ctx, TopicSpec{Title: "Rust", Goal: "A new start"}); CodeOf(err) != CodeAlreadyExists {
		t.Fatalf("CreateTopic over the restored Topic = %v", err)
	}
	if treeHash(t, filepath.Join(r.home, "rust")) != r.removedHash {
		t.Error("the restored Topic was changed")
	}
}

// Two restores of one removal: the lock lets one through, and the other
// finds nothing left to restore.
func TestOneRemovalIsRestoredOnce(t *testing.T) {
	ctx := context.Background()
	for _, from := range []string{"", "20261001-093000-rust"} {
		r := newRestoreRace(t)
		r.restorer.crash = atPoint(crashBeforeLock, func() {
			if _, err := r.other.RestoreTopic(ctx, TopicRestoreSpec{Topic: "rust"}); err != nil {
				t.Errorf("the first restore: %v", err)
			}
		})
		if _, err := r.restorer.RestoreTopic(ctx, TopicRestoreSpec{Topic: "rust", From: from}); CodeOf(err) != CodeNotFound {
			t.Errorf("the second restore (from %q) = %v, want not_found", from, err)
		}
		if treeHash(t, filepath.Join(r.home, "rust")) != r.removedHash || len(removedFolders(t, r.home)) != 0 {
			t.Errorf("after two restores (from %q) the Topic is not back whole, once", from)
		}
	}
}

// A write that opened the Topic, then waited while it was removed and
// restored, lands in it: it is the Topic it opened, under the id again.
func TestAWriteThatWaitedThroughARemovalAndARestoreLands(t *testing.T) {
	ctx := context.Background()
	r := newRemovalRace(t)
	r.writer.crash = atPoint(crashBeforeLock, func() {
		if _, err := r.remover.RemoveTopic(ctx, "rust", false); err != nil {
			t.Errorf("RemoveTopic: %v", err)
		}
		if _, err := r.remover.RestoreTopic(ctx, TopicRestoreSpec{Topic: "rust"}); err != nil {
			t.Errorf("RestoreTopic: %v", err)
		}
	})
	if err := raceWriters[0].write(r.writer); err != nil {
		t.Fatalf("the write: %v", err)
	}
	if !raceWriters[0].wrote(t, filepath.Join(r.home, "rust")) || len(removedFolders(t, r.home)) != 0 {
		t.Error("the write is not in the restored Topic")
	}
}

// A restore interrupted after the move is complete but for the record it
// left, which names no removed Topic any more.
func TestARestoreInterruptedAfterTheMoveIsComplete(t *testing.T) {
	ctx := context.Background()
	m, removed := removedTopic(t)
	m.crash = crashOnce(crashRestoreMoved)
	if _, err := m.RestoreTopic(ctx, TopicRestoreSpec{Topic: "rust"}); !errors.Is(err, errCrash) {
		t.Fatalf("RestoreTopic = %v, want the crash", err)
	}
	if !exists(removed.MovedTo+".json") || exists(removed.MovedTo) {
		t.Fatal("the crash should leave the record and no folder")
	}
	status, err := m.Status(ctx)
	if err != nil || len(status.Topics) != 1 || len(status.Topics[0].Flags) != 0 || len(status.Problems) != 0 {
		t.Fatalf("status after the crash = %+v, %v", status, err)
	}
	if list, err := m.ListRemovedTopics(ctx); err != nil || len(list.Removed) != 0 {
		t.Errorf("removed Topics after the crash = %+v, %v; want none", list, err)
	}
	if _, err := m.RestoreTopic(ctx, TopicRestoreSpec{Topic: "rust", From: filepath.Base(removed.MovedTo)}); CodeOf(err) != CodeNotFound {
		t.Errorf("restoring the removal again = %v, want not_found", err)
	}

	// The Topic is removed and restored again like any other; the leftover
	// record's name is not taken by the new removal.
	again := m.remove(t, "rust")
	if again.MovedTo == removed.MovedTo {
		t.Fatalf("the new removal took the name the leftover record holds: %s", again.MovedTo)
	}
	if _, err := m.RestoreTopic(ctx, TopicRestoreSpec{Topic: "rust"}); err != nil {
		t.Fatal(err)
	}
}

// A crash before the lock or before the move changed nothing: the restore
// can be run again.
func TestARestoreInterruptedBeforeTheMoveMovedNothing(t *testing.T) {
	ctx := context.Background()
	for _, point := range []string{crashBeforeLock, crashRestoreChecked} {
		m, removed := removedTopic(t)
		before := treeHash(t, removed.MovedTo)
		m.crash = crashOnce(point)
		if _, err := m.RestoreTopic(ctx, TopicRestoreSpec{Topic: "rust"}); !errors.Is(err, errCrash) {
			t.Fatalf("%s: RestoreTopic = %v, want the crash", point, err)
		}
		if exists(filepath.Join(m.home, "rust")) || treeHash(t, removed.MovedTo) != before || !exists(removed.MovedTo+".json") {
			t.Errorf("%s: the crash moved something", point)
		}
		if _, err := m.RestoreTopic(ctx, TopicRestoreSpec{Topic: "rust"}); err != nil {
			t.Errorf("%s: the restore after the crash: %v", point, err)
		}
	}
}

// A record written by a newer version of study is refused: this one cannot
// know what it says about the folder. Its format is read before anything
// else in it, so a record whose other fields changed shape, which this
// version cannot even decode, is refused as newer too, never taken for a
// damaged one whose folder's name would do instead.
func TestRestoreTopicRefusesARecordInANewerFormat(t *testing.T) {
	ctx := context.Background()
	for name, newer := range map[string]string{
		"the fields this version knows, and more": `{"format":2,"topic":"rust","removed":"2026-10-01T09:30:00Z","kept":["sources"]}` + "\n",
		"fields of another shape":                 `{"format":2,"topic":{"id":"rust"},"removed":1790847000}` + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			m, removed := removedTopic(t)
			record := removed.MovedTo + ".json"
			if err := os.WriteFile(record, []byte(newer), 0o644); err != nil {
				t.Fatal(err)
			}
			for _, spec := range []TopicRestoreSpec{
				{Topic: "rust"}, {Topic: "rust", DryRun: true}, {Topic: "rust", From: filepath.Base(removed.MovedTo)},
			} {
				_, err := m.RestoreTopic(ctx, spec)
				if CodeOf(err) != CodeNewerFormat || !strings.Contains(err.Error(), record) || !strings.Contains(err.Error(), "upgrade study") {
					t.Errorf("RestoreTopic(%+v) = %v, want newer_format naming the record", spec, err)
				}
			}
			// The list still shows it, saying what to do, with no id and no
			// command: the id its folder's name fits is not offered.
			list, err := m.ListRemovedTopics(ctx)
			if err != nil || len(list.Removed) != 1 {
				t.Fatalf("removed Topics = %+v, %v", list, err)
			}
			if got := list.Removed[0]; got.Topic != "" || got.Restore != "" || got.Folder != filepath.Base(removed.MovedTo) ||
				!strings.Contains(got.Note, "has format 2") || !strings.Contains(got.Note, "upgrade study") || strings.Contains(got.Note, "damaged") {
				t.Errorf("the entry of a newer record = %+v", got)
			}
			// Nothing was moved, and the record is as the newer version wrote it.
			left, err := os.ReadFile(record)
			if err != nil || string(left) != newer {
				t.Errorf("the newer record = %q, %v; want it left as it was", left, err)
			}
			if exists(filepath.Join(m.home, "rust")) || !exists(filepath.Join(removed.MovedTo, "topic.toml")) {
				t.Fatal("a removal with a newer record was moved")
			}

			// Another Topic's removal is not held up by it.
			if _, err := m.CreateTopic(ctx, TopicSpec{Title: "Go", Goal: "Write a scheduler"}); err != nil {
				t.Fatal(err)
			}
			m.remove(t, "go")
			if _, err := m.RestoreTopic(ctx, TopicRestoreSpec{Topic: "go"}); err != nil {
				t.Errorf("restoring another Topic: %v", err)
			}
			// Nor is a removal of the same Topic that is certainly newer than
			// its folder: the time in a record this version cannot read is
			// not trusted, but the folder's name bounds it to its second.
			if _, err := m.CreateTopic(ctx, TopicSpec{Title: "Rust", Goal: "Write a web server"}); err != nil {
				t.Fatal(err)
			}
			m.setClock(t0.Add(time.Hour))
			later := m.remove(t, "rust")
			got, err := m.RestoreTopic(ctx, TopicRestoreSpec{Topic: "rust"})
			if err != nil || got.RestoredFrom != later.MovedTo {
				t.Fatalf("restore by id with an older newer-format removal = %+v, %v; want %s", got, err, later.MovedTo)
			}
			if left, err := os.ReadFile(record); err != nil || string(left) != newer || !exists(removed.MovedTo) {
				t.Errorf("the newer record or its folder was touched: %q, %v", left, err)
			}
			// One in the same second as its folder may be older, so the newer
			// record is refused again.
			m.setClock(t0.Add(500 * time.Millisecond))
			m.remove(t, "rust")
			if _, err := m.RestoreTopic(ctx, TopicRestoreSpec{Topic: "rust"}); CodeOf(err) != CodeNewerFormat {
				t.Errorf("restore by id with a newer-format removal of the same second = %v, want newer_format", err)
			}
		})
	}
}

// A folder whose record is missing or cannot be used is restored by its id
// when its name fits one id only. A name that fits two is never guessed: the
// learner names the folder and the id.
func TestRestoreTopicReadsRemovalsWithoutAUsableRecord(t *testing.T) {
	ctx := context.Background()

	t.Run("a name that fits one id", func(t *testing.T) {
		m, removed := removedTopic(t)
		dropRecord(t, removed)
		list, err := m.ListRemovedTopics(ctx)
		want := []RemovedTopic{{Topic: "rust", Folder: "20261001-093000-rust", Path: removed.MovedTo, Removed: t0, Restore: removed.Restore}}
		if err != nil || !reflect.DeepEqual(list.Removed, want) {
			t.Fatalf("removed Topics = %+v, %v\nwant %+v", list.Removed, err, want)
		}
		if _, err := m.RestoreTopic(ctx, TopicRestoreSpec{Topic: "rust"}); err != nil {
			t.Fatal(err)
		}
	})

	// data-python removed, or data removed in a second that was taken: the
	// folder's name is the same.
	ambiguous := func(t *testing.T) (*machine, TopicRemoval) {
		t.Helper()
		gitIdentity(t)
		m := newMachine(t, t.TempDir(), "id", t0)
		if _, err := m.CreateTopic(ctx, TopicSpec{Title: "Data in Python", ID: "data-python", Goal: "Clean a dataset"}); err != nil {
			t.Fatal(err)
		}
		removed := m.remove(t, "data-python")
		dropRecord(t, removed)
		return m, removed
	}

	t.Run("a name that fits two ids", func(t *testing.T) {
		m, removed := ambiguous(t)
		folder := filepath.Base(removed.MovedTo)
		list, err := m.ListRemovedTopics(ctx)
		if err != nil || len(list.Removed) != 1 {
			t.Fatalf("removed Topics = %+v, %v", list, err)
		}
		if got := list.Removed[0]; got.Topic != "" || got.Restore != "" || got.Folder != folder || !got.Removed.Equal(t0) ||
			!strings.Contains(got.Note, "study topic restore <id> --from "+folder) ||
			!strings.Contains(got.Note, "its record is missing, and its name fits both data-python and data") {
			t.Errorf("the entry = %+v", got)
		}
		for _, id := range []string{"data-python", "data"} {
			_, err := m.RestoreTopic(ctx, TopicRestoreSpec{Topic: id})
			if CodeOf(err) != CodeNotFound || !strings.Contains(err.Error(), "study topic restore "+id+" --from "+folder) {
				t.Errorf("restoring %s by id alone = %v, want not_found naming the folder", id, err)
			}
		}
		if _, err := m.RestoreTopic(ctx, TopicRestoreSpec{Topic: "python", From: folder}); CodeOf(err) != CodeInvalidArgument ||
			!strings.Contains(err.Error(), "its name fits data-python or data") {
			t.Errorf("an id the name does not fit = %v", err)
		}
		if !exists(removed.MovedTo) {
			t.Fatal("a refused restore moved the folder")
		}
		got, err := m.RestoreTopic(ctx, TopicRestoreSpec{Topic: "data-python", From: folder})
		if err != nil || got.Path != filepath.Join(m.home, "data-python") {
			t.Fatalf("restore with the id and the folder named = %+v, %v", got, err)
		}
	})

	t.Run("newer than a removal with a record", func(t *testing.T) {
		m, old := ambiguous(t)
		if _, err := m.CreateTopic(ctx, TopicSpec{Title: "Data in Python", ID: "data-python", Goal: "Plot it"}); err != nil {
			t.Fatal(err)
		}
		m.setClock(t0.Add(-time.Hour))
		known := m.remove(t, "data-python")
		_, err := m.RestoreTopic(ctx, TopicRestoreSpec{Topic: "data-python"})
		if CodeOf(err) != CodeFailedPrecondition || !strings.Contains(err.Error(), filepath.Base(old.MovedTo)) ||
			!strings.Contains(err.Error(), filepath.Base(known.MovedTo)) || !strings.Contains(err.Error(), "--from") {
			t.Fatalf("restore by id = %v, want failed_precondition naming both folders", err)
		}
		// An older folder without a record does not come into it.
		m2, old2 := ambiguous(t)
		if _, err := m2.CreateTopic(ctx, TopicSpec{Title: "Data in Python", ID: "data-python", Goal: "Plot it"}); err != nil {
			t.Fatal(err)
		}
		m2.setClock(t0.Add(time.Hour))
		known2 := m2.remove(t, "data-python")
		got, err := m2.RestoreTopic(ctx, TopicRestoreSpec{Topic: "data-python"})
		if err != nil || got.RestoredFrom != known2.MovedTo || !exists(old2.MovedTo) {
			t.Fatalf("restore by id = %+v, %v; want the removal with a record", got, err)
		}
	})

	// Records that cannot be used, each put beside the removed folder: the
	// folder is then read as one without a record.
	unusable := map[string]string{
		"not JSON":             "rust\n",
		"no format":            `{"topic":"%s"}`,
		"no Topic":             `{"format":1}`,
		"another Topic's":      `{"format":1,"topic":"go"}`,
		"an id that is none":   `{"format":1,"topic":"../%s"}`,
		"a folder, not a file": "",
	}
	replaceRecord := func(t *testing.T, removed TopicRemoval, topicID, record string) {
		t.Helper()
		dropRecord(t, removed)
		var err error
		if record == "" {
			err = os.Mkdir(removed.MovedTo+".json", 0o755)
		} else {
			err = os.WriteFile(removed.MovedTo+".json", []byte(strings.ReplaceAll(record, "%s", topicID)), 0o644)
		}
		if err != nil {
			t.Fatal(err)
		}
	}

	t.Run("a record that cannot be used, a name that fits one id", func(t *testing.T) {
		for name, record := range unusable {
			m, removed := removedTopic(t)
			replaceRecord(t, removed, "rust", record)
			list, err := m.ListRemovedTopics(ctx)
			want := []RemovedTopic{{Topic: "rust", Folder: "20261001-093000-rust", Path: removed.MovedTo, Removed: t0, Restore: removed.Restore}}
			if err != nil || !reflect.DeepEqual(list.Removed, want) {
				t.Errorf("%s: removed Topics = %+v, %v\nwant %+v", name, list.Removed, err, want)
			}
			if _, err := m.RestoreTopic(ctx, TopicRestoreSpec{Topic: "go"}); CodeOf(err) != CodeNotFound {
				t.Errorf("%s: restore under the id the record names = %v, want not_found", name, err)
			}
			if _, err := m.RestoreTopic(ctx, TopicRestoreSpec{Topic: "rust"}); err != nil {
				t.Errorf("%s: restore by the id the name gives: %v", name, err)
			}
			if exists(removed.MovedTo + ".json") {
				t.Errorf("%s: the record that could not be used is left behind", name)
			}
		}
	})

	t.Run("a record that cannot be used, a name that fits two ids", func(t *testing.T) {
		for name, record := range unusable {
			m, removed := ambiguous(t)
			folder := filepath.Base(removed.MovedTo)
			if err := os.WriteFile(removed.MovedTo+".json", nil, 0o644); err != nil { // for replaceRecord to drop
				t.Fatal(err)
			}
			replaceRecord(t, removed, "data-python", record)
			list, err := m.ListRemovedTopics(ctx)
			if err != nil || len(list.Removed) != 1 || list.Removed[0].Topic != "" || list.Removed[0].Restore != "" ||
				!strings.Contains(list.Removed[0].Note, "study topic restore <id> --from "+folder) ||
				!strings.Contains(list.Removed[0].Note, removed.MovedTo+".json") {
				t.Errorf("%s: removed Topics = %+v, %v", name, list, err)
			}
			if _, err := m.RestoreTopic(ctx, TopicRestoreSpec{Topic: "data-python"}); CodeOf(err) != CodeNotFound ||
				!strings.Contains(err.Error(), "--from "+folder) {
				t.Errorf("%s: restore by id alone = %v, want not_found naming the folder", name, err)
			}
			if _, err := m.RestoreTopic(ctx, TopicRestoreSpec{Topic: "go", From: folder}); CodeOf(err) != CodeInvalidArgument {
				t.Errorf("%s: restore under an id the name does not fit = %v", name, err)
			}
			if _, err := m.RestoreTopic(ctx, TopicRestoreSpec{Topic: "data-python", From: folder}); err != nil {
				t.Errorf("%s: restore with the id and the folder named: %v", name, err)
			}
		}
	})

	// The record is what tells the two readings of a name apart.
	t.Run("a record that says which of two ids", func(t *testing.T) {
		m, removed := ambiguous(t)
		record := `{"format":1,"topic":"data","removed":"2026-10-01T09:30:00.25Z"}` + "\n"
		if err := os.WriteFile(removed.MovedTo+".json", []byte(record), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := m.RestoreTopic(ctx, TopicRestoreSpec{Topic: "data-python"}); CodeOf(err) != CodeNotFound {
			t.Errorf("restore under the other id the name fits = %v, want not_found", err)
		}
		got, err := m.RestoreTopic(ctx, TopicRestoreSpec{Topic: "data"})
		if err != nil || got.Path != filepath.Join(m.home, "data") || !got.Removed.Equal(t0.Add(250*time.Millisecond)) {
			t.Fatalf("restore under the id the record names = %+v, %v", got, err)
		}
	})
}

// A time read from a folder's name stands for the whole second. Of two
// removals of one id within a second, the later one's record lost, which is
// the newer cannot be told: the learner names one.
func TestRestoreTopicDoesNotOrderByAFoldersNameWithinItsSecond(t *testing.T) {
	ctx := context.Background()
	gitIdentity(t)
	m := newMachine(t, t.TempDir(), "id", t0.Add(900*time.Millisecond))
	if _, err := m.CreateTopic(ctx, TopicSpec{Title: "Rust", Goal: "Write a CLI"}); err != nil {
		t.Fatal(err)
	}
	first := m.remove(t, "rust")
	if _, err := m.CreateTopic(ctx, TopicSpec{Title: "Rust", Goal: "Write a web server"}); err != nil {
		t.Fatal(err)
	}
	m.setClock(t0.Add(950 * time.Millisecond))
	second := m.remove(t, "rust")

	if got, err := m.RestoreTopic(ctx, TopicRestoreSpec{Topic: "rust", DryRun: true}); err != nil || got.RestoredFrom != second.MovedTo {
		t.Fatalf("with both records = %+v, %v; want the later removal, %s", got, err, second.MovedTo)
	}
	dropRecord(t, second)
	for _, dryRun := range []bool{true, false} {
		_, err := m.RestoreTopic(ctx, TopicRestoreSpec{Topic: "rust", DryRun: dryRun})
		if CodeOf(err) != CodeFailedPrecondition || !strings.Contains(err.Error(), filepath.Base(first.MovedTo)+" does") ||
			!strings.Contains(err.Error(), filepath.Base(second.MovedTo)+" may hold a newer removal") || !strings.Contains(err.Error(), "--from") {
			t.Fatalf("the later removal's record lost (dry run %v) = %v, want failed_precondition naming both folders", dryRun, err)
		}
	}
	if !exists(first.MovedTo) || !exists(second.MovedTo) || exists(filepath.Join(m.home, "rust")) {
		t.Fatal("a refused restore moved a folder")
	}
	// Named, either restores.
	got, err := m.RestoreTopic(ctx, TopicRestoreSpec{Topic: "rust", From: filepath.Base(second.MovedTo)})
	if err != nil || readSettings(t, got.Path).Goal != "Write a web server" {
		t.Fatalf("restore --from the later removal = %+v, %v", got, err)
	}

	// A folder known by its name alone is no better placed: the earlier
	// removal's record lost, its name fits one id, and the second still
	// covers the later removal's time.
	m.remove(t, "rust")
	dropRecord(t, first)
	list, err := m.ListRemovedTopics(ctx)
	if err != nil || len(list.Removed) != 2 || list.Removed[1].Topic != "rust" || list.Removed[1].Folder != filepath.Base(first.MovedTo) {
		t.Fatalf("removed Topics = %+v, %v", list, err)
	}
	if _, err := m.RestoreTopic(ctx, TopicRestoreSpec{Topic: "rust"}); CodeOf(err) != CodeFailedPrecondition ||
		!strings.Contains(err.Error(), filepath.Base(first.MovedTo)+" may hold a newer removal") {
		t.Fatalf("an earlier removal known by its name, in the same second = %v, want failed_precondition", err)
	}
}

// Whatever is under the id that is not a Topic's own folder, a link to one
// included, cannot be moved out of the way with study topic remove: the
// advice is to move it away.
func TestRestoreTopicNeverAdvisesRemovingWhatIsNotATopic(t *testing.T) {
	ctx := context.Background()
	m, removed := removedTopic(t)
	if _, err := m.CreateTopic(ctx, TopicSpec{Title: "Go", Goal: "Write a scheduler"}); err != nil {
		t.Fatal(err)
	}
	under := filepath.Join(m.home, "rust")
	for name, put := range map[string]func() error{
		"a link to a Topic":         func() error { return os.Symlink(filepath.Join(m.home, "go"), under) },
		"a link to nothing":         func() error { return os.Symlink(filepath.Join(m.home, "nowhere"), under) },
		"an empty folder":           func() error { return os.Mkdir(under, 0o755) },
		"a file":                    func() error { return os.WriteFile(under, []byte("notes\n"), 0o644) },
		"a folder of the learner's": func() error { return os.MkdirAll(filepath.Join(under, "notes"), 0o755) },
	} {
		if err := put(); err != nil {
			t.Fatal(err)
		}
		for _, dryRun := range []bool{true, false} {
			_, err := m.RestoreTopic(ctx, TopicRestoreSpec{Topic: "rust", DryRun: dryRun})
			if CodeOf(err) != CodeAlreadyExists || !strings.Contains(err.Error(), under+" exists and is not a Topic") ||
				!strings.Contains(err.Error(), "Move it away, then run "+removed.Restore) || strings.Contains(err.Error(), "study topic remove") {
				t.Errorf("%s under the id (dry run %v): %v", name, dryRun, err)
			}
		}
		if !exists(filepath.Join(removed.MovedTo, "topic.toml")) {
			t.Fatalf("%s under the id: the removal was moved", name)
		}
		if err := os.RemoveAll(under); err != nil {
			t.Fatal(err)
		}
	}
	if !exists(filepath.Join(m.home, "go", "topic.toml")) {
		t.Fatal("the Topic a link pointed at was touched")
	}
}

// A folder in .lamplight/removed that is named like a removal and holds no
// Topic is not restored: under the id it would be a Topic to nothing, not
// even to study topic remove. The list shows it, saying so.
func TestRestoreTopicRefusesAFolderThatHoldsNoTopic(t *testing.T) {
	ctx := context.Background()
	gitIdentity(t)
	m := newMachine(t, t.TempDir(), "id", t0)
	hollow := filepath.Join(m.home, ".lamplight", removedDir, "20261001-103000-rust")
	if err := os.MkdirAll(filepath.Join(hollow, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, withRecord := range []bool{false, true} {
		if withRecord {
			record := `{"format":1,"topic":"rust","removed":"2026-10-01T10:30:00Z"}` + "\n"
			if err := os.WriteFile(hollow+".json", []byte(record), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		list, err := m.ListRemovedTopics(ctx)
		if err != nil || len(list.Removed) != 1 {
			t.Fatalf("removed Topics (record %v) = %+v, %v", withRecord, list, err)
		}
		if got := list.Removed[0]; got.Topic != "" || got.Restore != "" || got.Path != hollow || !strings.Contains(got.Note, "holds no Topic") {
			t.Errorf("the entry (record %v) = %+v", withRecord, got)
		}
		for _, spec := range []TopicRestoreSpec{
			{Topic: "rust"}, {Topic: "rust", DryRun: true}, {Topic: "rust", From: filepath.Base(hollow)},
		} {
			_, err := m.RestoreTopic(ctx, spec)
			if CodeOf(err) != CodeCorrupt || !strings.Contains(err.Error(), hollow+" holds no Topic") {
				t.Errorf("RestoreTopic(%+v) (record %v) = %v, want corrupt naming the folder", spec, withRecord, err)
			}
		}
		if exists(filepath.Join(m.home, "rust")) || !exists(filepath.Join(hollow, "notes")) {
			t.Fatalf("a folder that holds no Topic was moved (record %v)", withRecord)
		}
	}

	// It does not stand in the way of a removal of the id that is one, even
	// an older one.
	if _, err := m.CreateTopic(ctx, TopicSpec{Title: "Rust", Goal: "Write a CLI"}); err != nil {
		t.Fatal(err)
	}
	removed := m.remove(t, "rust")
	got, err := m.RestoreTopic(ctx, TopicRestoreSpec{Topic: "rust"})
	if err != nil || got.RestoredFrom != removed.MovedTo || !exists(hollow) {
		t.Fatalf("restore by id beside a folder that holds no Topic = %+v, %v", got, err)
	}

	// A Topic that lost its settings is damaged, not gone: it is restored,
	// and status says what is wrong with it, as before its removal.
	removed = m.remove(t, "rust")
	if err := os.Remove(filepath.Join(removed.MovedTo, "topic.toml")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.RestoreTopic(ctx, TopicRestoreSpec{Topic: "rust", From: filepath.Base(removed.MovedTo)}); err != nil {
		t.Fatalf("restoring a Topic without its settings: %v", err)
	}
	if status, err := m.Status(ctx); err != nil || len(status.Problems) != 1 || status.Problems[0].ID != "rust" {
		t.Errorf("status = %+v, %v; want the damaged Topic as a problem", status, err)
	}
}

// What is in .lamplight/removed and is not a removed Topic's folder is not
// listed, and never restored.
func TestRemovedTopicsListsOnlyRemovals(t *testing.T) {
	ctx := context.Background()
	m, removed := removedTopic(t)
	dir := filepath.Dir(removed.MovedTo)
	for _, name := range []string{"notes", "20261001-093000-Rust", "20261301-093000-rust", "20261001-0930-rust"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "20261001-093000-go"), []byte("a file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(removed.MovedTo, filepath.Join(dir, "20261001-093000-alias")); err != nil {
		t.Fatal(err)
	}
	list, err := m.ListRemovedTopics(ctx)
	if err != nil || len(list.Removed) != 1 || list.Removed[0].Path != removed.MovedTo {
		t.Fatalf("removed Topics = %+v, %v; want the one removal", list, err)
	}
	for _, spec := range []TopicRestoreSpec{{Topic: "go"}, {Topic: "alias"}, {Topic: "alias", From: "20261001-093000-alias"}} {
		if _, err := m.RestoreTopic(ctx, spec); CodeOf(err) != CodeNotFound {
			t.Errorf("RestoreTopic(%+v) = %v, want not_found", spec, err)
		}
	}
}

func TestReadRemovedName(t *testing.T) {
	long := strings.Repeat("a", 64)
	for _, tc := range []struct {
		name string
		ids  []string
	}{
		{"20261001-093000-rust", []string{"rust"}},
		{"20261001-093000-go-concurrency", []string{"go-concurrency"}},
		{"20261001-093000-abc234", []string{"abc234"}},                   // nothing before it: not a suffix
		{"20261001-093000-rust-abc234", []string{"rust-abc234", "rust"}}, // six characters of a random id
		{"20261001-093000-data-python", []string{"data-python", "data"}}, // so is a six-letter word
		{"20261001-093000-rust-abc189", []string{"rust-abc189"}},         // 1, 8 and 9 are in no random id
		{"20261001-093000-rust-abc2345", []string{"rust-abc2345"}},       // seven characters
		{"20261001-093000-" + long + "-abc234", []string{long}},          // too long for an id without the suffix
		{"20261001-093000-" + long + "a", nil},                           // too long for an id
		{"20261001-093000-", nil},
		{"20261001-093000", nil},
		{"20261001-093000-Rust", nil},
		{"20261001-093000-rust--x", nil},
		{"20261001-093000-rust.json", nil},
		{"20261301-093000-rust", nil}, // no thirteenth month
		{"rust", nil},
		{"../20261001-093000-rust", nil},
	} {
		when, ids, ok := readRemovedName(tc.name)
		if !reflect.DeepEqual(ids, tc.ids) || ok != (tc.ids != nil) || ok && !when.Equal(t0) {
			t.Errorf("readRemovedName(%q) = %v, %v, %v; want %v", tc.name, when, ids, ok, tc.ids)
		}
	}
}

// Moves the operating system refuses get advice, not an internal error.
func TestRestoreTopicExplainsARefusedMove(t *testing.T) {
	m, removed := removedTopic(t)
	root, err := os.OpenRoot(m.home)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, tc := range []struct {
		errno error
		code  ErrorCode
		says  string
	}{
		{syscall.EXDEV, CodeFailedPrecondition, "different file systems"},
		{syscall.EBUSY, CodeBusy, "in use or is a mount point"},
		{os.ErrNotExist, CodeNotFound, "restored or moved"},
		{syscall.ENOTEMPTY, CodeAlreadyExists, "appeared while"},
		{errors.New("no reason"), CodeInternal, "moving " + removed.MovedTo},
	} {
		err := m.restoreMoveError(root, "rust", filepath.Base(removed.MovedTo),
			&os.LinkError{Op: "rename", Old: "x", New: "rust", Err: tc.errno})
		if CodeOf(err) != tc.code || !strings.Contains(err.Error(), tc.says) || !errors.Is(err, tc.errno) {
			t.Errorf("%v: %v (%s)", tc.errno, err, CodeOf(err))
		}
	}
}
