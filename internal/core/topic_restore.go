package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Restoring a removed Topic (study topic restore): the folder RemoveTopic
// moved into .lamplight/removed goes back into the Study home under the
// Topic's id. Like removing, it is this computer's state and records no
// Event: nothing inside the Topic changes, and other computers and the
// Topic's git remote never knew it was gone.

const (
	// removedTimeLayout is how a removed Topic's folder name begins: the UTC
	// time of the removal, to the second.
	removedTimeLayout = "20060102-150405"
	// removalSuffixLen is the length of the suffix a removed Topic's folder
	// gets when its name is taken: that many characters of a random id.
	removalSuffixLen = 6
)

var (
	// removedFolderPattern is the name of a removed Topic's folder:
	// <time>-<id>, or <time>-<id>-<suffix>.
	removedFolderPattern = regexp.MustCompile(`^([0-9]{8}-[0-9]{6})-([a-z0-9]+(?:-[a-z0-9]+)*)$`)
	// removalSuffixPattern is what the suffix looks like: characters of
	// randomID's alphabet.
	removalSuffixPattern = regexp.MustCompile(fmt.Sprintf(`^[a-z2-7]{%d}$`, removalSuffixLen))
)

// removalRecord is the content of .lamplight/removed/<folder>.json, beside a
// removed Topic's folder: which Topic the folder was. Nothing else says it.
// A Topic does not hold its own id, which is its folder's name in the Study
// home, and the removed folder's name cannot always be read back into one:
// ids hold hyphens, and a random suffix follows the id when two removals
// share a second. RemoveTopic writes the record before it moves the folder,
// and RestoreTopic deletes it after moving the folder back, so a crash
// leaves at worst a record, or the temporary file of one, without a folder.
// Neither is a removal, and both are passed over.
type removalRecord struct {
	Format int    `json:"format"`
	Topic  string `json:"topic"`
	// Removed is when, in UTC, more exactly than the folder's name says it:
	// it orders two removals made within one second.
	Removed time.Time `json:"removed"`
}

func removalRecordPath(folder string) string {
	return filepath.Join(localDir, removedDir, folder+".json")
}

func writeRemovalRecord(home *os.Root, folder string, rec removalRecord) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return internalError("encoding the record of a removed Topic", err)
	}
	return writeFileAtomic(home, removalRecordPath(folder), append(data, '\n'))
}

// RemovedTopic is a folder in .lamplight/removed: a Topic removed from the
// Study home on this computer, kept whole.
type RemovedTopic struct {
	// Topic is the id the Topic had. It is empty when this version of study
	// cannot tell, or when the folder holds no Topic; Note then says why,
	// and what can be done.
	Topic string `json:"topic,omitempty"`
	// Folder is the name of its folder in .lamplight/removed, which
	// RestoreTopic takes as From, and Path where that folder is.
	Folder string `json:"folder"`
	Path   string `json:"path"`
	// Removed is when it was removed, in UTC: to the second when its record
	// is missing or cannot be used, since the folder's name says no more.
	Removed time.Time `json:"removed"`
	// Restore is the command that restores exactly this removal. It is
	// empty when Topic is.
	Restore string `json:"restore,omitempty"`
	Note    string `json:"note,omitempty"`
}

// RemovedTopicList is the Topics removed on this computer, newest first.
type RemovedTopicList struct {
	Removed []RemovedTopic `json:"removed"`
}

// TopicRestoreSpec names a removed Topic to restore.
type TopicRestoreSpec struct {
	// Topic is the id the Topic had, and gets back.
	Topic string
	// From names one removal by its folder in .lamplight/removed. Empty
	// takes the newest removal of Topic.
	From   string
	DryRun bool
}

// TopicRestore reports a removed Topic moved back into the Study home.
type TopicRestore struct {
	Topic string `json:"topic"`
	// Path is where the Topic is again: <Study home>/<topic>.
	Path string `json:"path"`
	// RestoredFrom is the folder in .lamplight/removed it was kept in.
	RestoredFrom string `json:"restored_from"`
	// Removed is when it had been removed, in UTC.
	Removed time.Time `json:"removed"`
	// Note says, in a dry run, that another study process holds the lock of
	// the id: the restore waits for it, and may find the id taken then.
	Note   string `json:"note,omitempty"`
	DryRun bool   `json:"dry_run,omitempty"`
}

// removedEntry is one folder of .lamplight/removed, as this version of study
// reads it.
type removedEntry struct {
	folder string
	// when is the time of the removal: from the folder's record, or else the
	// start of the second its name gives (byName).
	when   time.Time
	byName bool
	// topic is the Topic's id, when it is known: the record's, when it has
	// one that can be used, or else the one id the folder's name fits.
	topic string
	// ids are the ids the folder's name fits: one, or two when its last part
	// could be the suffix of a name that was taken.
	ids []string
	// newer is the format of a record a newer version of study wrote.
	newer int
	// noTopic is set when the folder holds no Topic, so it is not restored.
	noTopic bool
	// note says why the folder cannot be restored by its id alone, and what
	// the learner can do.
	note string
}

// latest is the latest the removal can have happened: a time read from a
// folder's name stands for the whole second.
func (r removedEntry) latest() time.Time {
	if r.byName {
		return r.when.Add(time.Second - time.Nanosecond)
	}
	return r.when
}

// ListRemovedTopics lists the Topics removed from the Study home on this
// computer, newest first. It reads only.
func (c *Core) ListRemovedTopics(ctx context.Context) (RemovedTopicList, error) {
	list := RemovedTopicList{Removed: []RemovedTopic{}}
	home, err := os.OpenRoot(c.home)
	if errors.Is(err, fs.ErrNotExist) {
		return list, nil
	}
	if err != nil {
		return list, internalError("opening the Study home "+c.home, err)
	}
	defer home.Close()
	entries, err := c.removedEntries(ctx, home)
	if err != nil {
		return list, err
	}
	for _, r := range entries {
		out := RemovedTopic{Folder: r.folder, Path: c.removedPath(r.folder), Removed: r.when, Note: r.note}
		if r.topic != "" && !r.noTopic {
			out.Topic, out.Restore = r.topic, restoreCommand(r.topic, r.folder)
		}
		list.Removed = append(list.Removed, out)
	}
	return list, nil
}

// RestoreTopic moves a removed Topic's folder back into the Study home under
// its id, on this computer only. It takes the lock of the Topic id, checks
// under it that nothing has the id, and renames the folder into place.
//
// The lock keeps it apart from everything else that takes it: a removal, a
// write, another restore, and study import, which holds the lock of the id
// it imports under from before it stages the Topic until it has moved it
// into place. Creating a Topic takes no lock, and there the rename does the
// work: unlike the mv a shell offers, it never puts the folder inside one
// that took its place, so of a create and a restore of one id the second to
// move is refused. (A rename can replace a folder that holds nothing; one
// that appears under the id after the check is no Topic, and nothing is
// lost.)
//
// Like RemoveTopic it records no Event (see there), is for the learner, and
// agents have no tool for it.
func (c *Core) RestoreTopic(ctx context.Context, spec TopicRestoreSpec) (TopicRestore, error) {
	out, err := c.restoreTopic(ctx, spec)
	if err != nil && ctx.Err() != nil {
		return TopicRestore{}, &Error{Code: CodeCanceled, Err: ctx.Err(),
			Message: "the restore was stopped before it moved the Topic, so nothing was restored"}
	}
	return out, err
}

func (c *Core) restoreTopic(ctx context.Context, spec TopicRestoreSpec) (TopicRestore, error) {
	if err := checkTopicID(spec.Topic); err != nil {
		return TopicRestore{}, err
	}
	if spec.From != "" {
		if _, _, ok := readRemovedName(spec.From); !ok {
			return TopicRestore{}, invalidf("%q is not the name of a removed Topic's folder: study topic restore --list shows them", spec.From)
		}
	}
	home, err := os.OpenRoot(c.home)
	if errors.Is(err, fs.ErrNotExist) {
		return TopicRestore{}, noSuchRemoval(spec.Topic)
	}
	if err != nil {
		return TopicRestore{}, internalError("opening the Study home "+c.home, err)
	}
	defer home.Close()

	// Planned once before the lock, so a restore that cannot succeed says so
	// at once, without waiting and without leaving a lock file for an id
	// that names nothing.
	r, err := c.planRestore(ctx, home, spec)
	if err != nil {
		return TopicRestore{}, err
	}
	if spec.DryRun {
		if wasInterrupted(home, spec.Topic) {
			return TopicRestore{}, c.strayIntent(spec.Topic)
		}
		out := c.restoreOf(spec.Topic, r)
		out.DryRun = true
		if lockHeld(home, spec.Topic) {
			// Nothing has the id, so the holder is an import under it, or
			// another restore: the real run waits for it.
			out.Note = "another study process holds the lock of " + spec.Topic + ", such as an import under that id: " +
				"the restore waits for it to finish, and refuses if the id is taken by then"
		}
		return out, nil
	}
	if err := ctx.Err(); err != nil {
		return TopicRestore{}, err
	}
	if err := c.crashAt(crashBeforeLock); err != nil {
		return TopicRestore{}, err
	}
	unlock, err := lockTopic(ctx, home, spec.Topic)
	if err != nil {
		return TopicRestore{}, err
	}
	defer unlock()

	// And again under the lock, which is the plan that counts: another
	// restore may have taken the removal, or a Topic the id, while this one
	// waited.
	if r, err = c.planRestore(ctx, home, spec); err != nil {
		return TopicRestore{}, err
	}
	// The lock is ours and no Topic has the id, so a marker left now belongs
	// to a write to a Topic that is gone.
	if hasIntent(home, spec.Topic) {
		return TopicRestore{}, c.strayIntent(spec.Topic)
	}
	if err := c.crashAt(crashRestoreChecked); err != nil {
		return TopicRestore{}, err
	}
	out := c.restoreOf(spec.Topic, r)
	from := filepath.Join(localDir, removedDir, r.folder)
	if err := home.Rename(from, spec.Topic); err != nil {
		return TopicRestore{}, c.restoreMoveError(home, spec.Topic, r.folder, err)
	}
	syncDir(home, ".")
	syncDir(home, filepath.Dir(from))
	if err := c.crashAt(crashRestoreMoved); err != nil {
		return TopicRestore{}, err
	}
	// The record last: a crash before this leaves a record without its
	// folder, which is passed over.
	if err := home.Remove(removalRecordPath(r.folder)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		c.log.Warn("could not delete the record of a restored Topic; it is ignored", "topic", spec.Topic,
			"record", filepath.Join(c.home, removalRecordPath(r.folder)), "err", err)
	}
	c.log.Info("restored a Topic to the Study home", "topic", spec.Topic, "restored_from", out.RestoredFrom)
	return out, nil
}

func (c *Core) removedPath(folder string) string {
	return filepath.Join(c.home, localDir, removedDir, folder)
}

func (c *Core) restoreOf(topicID string, r removedEntry) TopicRestore {
	return TopicRestore{Topic: topicID, Path: filepath.Join(c.home, topicID), RestoredFrom: c.removedPath(r.folder), Removed: r.when}
}

// planRestore finds the removal spec names and checks that nothing in the
// Study home has the Topic's id. The dry run and the restore share it.
func (c *Core) planRestore(ctx context.Context, home *os.Root, spec TopicRestoreSpec) (removedEntry, error) {
	entries, err := c.removedEntries(ctx, home)
	if err != nil {
		return removedEntry{}, err
	}
	r, err := c.pickRemoval(entries, spec.Topic, spec.From)
	if err != nil {
		return removedEntry{}, err
	}
	switch info, err := home.Lstat(spec.Topic); {
	case err == nil:
		return removedEntry{}, c.idTaken(home, spec.Topic, r.folder, info)
	case !errors.Is(err, fs.ErrNotExist):
		return removedEntry{}, internalError("checking "+filepath.Join(c.home, spec.Topic), err)
	}
	return r, nil
}

// pickRemoval chooses the removal to restore among entries, which are newest
// first: the one in the folder from, or else the newest of the Topic. It
// never guesses. A folder whose id is not known is restored only when the
// learner names it with from, under an id its name fits. And when another
// folder that may be the Topic's may also be newer than the newest removal
// known to be the Topic's, because its record is missing or cannot be used
// and its name gives the time only to the second, the learner chooses. A
// folder that holds no Topic is never restored.
func (c *Core) pickRemoval(entries []removedEntry, topicID, from string) (removedEntry, error) {
	if from != "" {
		i := slices.IndexFunc(entries, func(r removedEntry) bool { return r.folder == from })
		if i < 0 {
			return removedEntry{}, &Error{Code: CodeNotFound, Message: "no removed Topic is kept in a folder named " + from +
				" on this computer: study topic restore --list shows them"}
		}
		r := entries[i]
		switch {
		case r.noTopic:
			return removedEntry{}, c.holdsNoTopic(r.folder)
		case r.newer > 0:
			return removedEntry{}, newerFormat(filepath.Join(c.home, removalRecordPath(r.folder)), r.newer)
		case r.topic == topicID, r.topic == "" && slices.Contains(r.ids, topicID):
			return r, nil
		case r.topic != "":
			return removedEntry{}, invalidf("%s holds Topic %s, not %s: to restore it, run %s",
				from, r.topic, topicID, restoreCommand(r.topic, from))
		}
		return removedEntry{}, invalidf("%s cannot be Topic %s: its name fits %s", from, topicID, strings.Join(r.ids, " or "))
	}

	// The newest removal known to be the Topic's.
	known := slices.IndexFunc(entries, func(r removedEntry) bool { return r.topic == topicID && !r.noTopic })
	// The folders that may be the Topic's too and, when one is known, may be
	// newer than it; and those that would be the Topic's but hold none.
	var unsure, hollow []string
	for i, r := range entries {
		switch {
		case i == known, !slices.Contains(r.ids, topicID), r.topic != "" && r.topic != topicID:
		case r.noTopic:
			hollow = append(hollow, r.folder)
		case known >= 0 && !(r.byName && !r.latest().Before(entries[known].when)):
			// Older than the newest known, whatever it is.
		case r.newer > 0:
			return removedEntry{}, newerFormat(filepath.Join(c.home, removalRecordPath(r.folder)), r.newer)
		default:
			unsure = append(unsure, r.folder)
		}
	}
	switch {
	case known >= 0 && len(unsure) == 0:
		return entries[known], nil
	case known >= 0:
		return removedEntry{}, &Error{Code: CodeFailedPrecondition, Message: fmt.Sprintf(
			"%s may hold a newer removal of Topic %s than %s does: its record is missing or cannot be used, so study cannot tell "+
				"(study topic restore --list says more). Name the one to restore: study topic restore %s --from <folder>",
			strings.Join(unsure, " or "), topicID, entries[known].folder, topicID)}
	case len(unsure) > 0:
		return removedEntry{}, &Error{Code: CodeNotFound, Message: fmt.Sprintf(
			"no folder in .lamplight/removed is known to hold Topic %s, though %s may: its record is missing or cannot be used "+
				"(study topic restore --list says more). If it is Topic %s, run %s",
			topicID, strings.Join(unsure, " or "), topicID, restoreCommand(topicID, unsure[0]))}
	case len(hollow) > 0:
		return removedEntry{}, c.holdsNoTopic(hollow[0])
	}
	return removedEntry{}, noSuchRemoval(topicID)
}

func noSuchRemoval(topicID string) error {
	return &Error{Code: CodeNotFound, Message: "no Topic named " + topicID +
		" was removed on this computer: study topic restore --list shows the removed Topics"}
}

// holdsNoTopic explains a folder in .lamplight/removed that is named like a
// removal and has nothing of a Topic in it: moved back, it would take the id
// and be a Topic to nothing, not even to study topic remove.
func (c *Core) holdsNoTopic(folder string) error {
	return corruptf("%s holds no Topic (it has neither %s nor %s), so it was not restored: "+
		"if anything in it is yours, move it out, then delete the folder", c.removedPath(folder), topicFile, historyFile)
}

// idTaken explains that the id a removed Topic had belongs to something
// else in the Study home now, info. Only a real folder that is a Topic can
// be moved out of the way with study topic remove.
func (c *Core) idTaken(home *os.Root, topicID, folder string, info fs.FileInfo) error {
	if !info.IsDir() || !isTopic(home, topicID) {
		return &Error{Code: CodeAlreadyExists, Message: filepath.Join(c.home, topicID) +
			" exists and is not a Topic, so the removed Topic cannot have the id back; nothing was moved. " +
			"Move it away, then run " + restoreCommand(topicID, folder)}
	}
	return &Error{Code: CodeAlreadyExists, Message: "another Topic is named " + topicID +
		" now, so the removed one cannot have the id back; nothing was moved, and it stays in " + c.removedPath(folder) +
		". To swap them, run study topic remove " + topicID + ", then " + restoreCommand(topicID, folder)}
}

// strayIntent explains a write marker under an id no Topic has: restoring a
// Topic under the id would make the marker its own.
func (c *Core) strayIntent(topicID string) error {
	return &Error{Code: CodeFailedPrecondition, Message: "a write to a Topic named " + topicID +
		" was interrupted and never finished, and no Topic has that id now, so its marker would be taken for " +
		"the restored Topic's. If that Topic is gone for good, delete " + filepath.Join(c.home, intentPath(topicID)) +
		", then restore"}
}

// restoreMoveError explains a move back that the operating system refused.
func (c *Core) restoreMoveError(home *os.Root, topicID, folder string, err error) error {
	from, to := c.removedPath(folder), filepath.Join(c.home, topicID)
	switch {
	case errors.Is(err, syscall.EXDEV):
		return &Error{Code: CodeFailedPrecondition, Err: err, Message: "the Study home and its .lamplight folder are on " +
			"different file systems, so Topic " + topicID + " cannot be moved back: move " + from + " to " + to +
			" yourself, while nothing else is there"}
	case errors.Is(err, syscall.EBUSY):
		return &Error{Code: CodeBusy, Err: err, Message: from +
			" is in use or is a mount point: close the programs using it, or unmount it, and try again"}
	case errors.Is(err, fs.ErrNotExist):
		return &Error{Code: CodeNotFound, Err: err, Message: from +
			" is gone: the Topic was restored or moved while this restore was under way"}
	}
	// Creating a Topic takes no lock, so one can appear between the check
	// and the move. The move then fails, since a folder is never moved over
	// one that holds anything.
	if _, statErr := home.Lstat(topicID); statErr == nil || errors.Is(err, fs.ErrExist) {
		return &Error{Code: CodeAlreadyExists, Err: err, Message: to +
			" appeared while the removed Topic was being restored; nothing was moved, and it stays in " + from}
	}
	return internalError("moving "+from+" to "+to, err)
}

// removedEntries reads .lamplight/removed, newest removal first. Only folders
// named as RemoveTopic names them count: anything else there is not
// Lamplight's, and a record without its folder is what an interrupted
// removal or restore leaves.
func (c *Core) removedEntries(ctx context.Context, home *os.Root) ([]removedEntry, error) {
	entries, err := fs.ReadDir(home.FS(), path.Join(filepath.ToSlash(localDir), removedDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, internalError("listing "+c.removedPath(""), err)
	}
	var out []removedEntry
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !e.IsDir() {
			continue
		}
		if r, ok := c.readRemoved(home, e.Name()); ok {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].when.Equal(out[j].when) {
			return out[i].when.After(out[j].when)
		}
		return out[i].folder > out[j].folder
	})
	return out, nil
}

// readRemoved reads one folder of .lamplight/removed and its record. The
// folder's name bounds what the Topic's id can be, and the record says which
// it is, and when exactly. A folder whose record is missing or cannot be
// used keeps the time its name gives, and has a known id only when its name
// fits one.
func (c *Core) readRemoved(home *os.Root, folder string) (removedEntry, bool) {
	when, ids, ok := readRemovedName(folder)
	if !ok {
		return removedEntry{}, false
	}
	r := removedEntry{folder: folder, when: when, byName: true, ids: ids}
	recordPath := filepath.Join(c.home, removalRecordPath(folder))

	var rec removalRecord
	var unusable string
	switch data, err := home.ReadFile(removalRecordPath(folder)); {
	case errors.Is(err, fs.ErrNotExist):
		unusable = "its record is missing"
	case err != nil:
		unusable = "its record, " + recordPath + ", cannot be read (" + err.Error() + ")"
	case json.Unmarshal(data, &rec) != nil || rec.Format < 1:
		unusable = "its record, " + recordPath + ", is damaged"
	case rec.Format > FormatVersion:
		r.newer = rec.Format
		r.note = fmt.Sprintf("its record, %s, has format %d, but this version of study only understands format %d: "+
			"upgrade study to restore it", recordPath, rec.Format, FormatVersion)
	case !slices.Contains(ids, rec.Topic):
		unusable = "its record, " + recordPath + ", names a Topic its folder's name does not fit"
	default:
		r.topic = rec.Topic
		if !rec.Removed.IsZero() {
			r.when, r.byName = rec.Removed.UTC(), false
		}
	}
	switch {
	case unusable == "":
	case len(ids) == 1:
		r.topic = ids[0]
	default:
		r.note = unusable + ", and its name fits both " + strings.Join(ids, " and ") + ", so its Topic id is not known: " +
			"restore it with study topic restore <id> --from " + folder + ", giving the id it had"
	}
	if !isTopic(home, filepath.Join(localDir, removedDir, folder)) {
		r.noTopic = true
		r.note = "it holds no Topic (it has neither " + topicFile + " nor " + historyFile + "), so study does not restore it: " +
			"if anything in it is yours, move it out, then delete the folder"
	}
	return r, true
}

// readRemovedName reads the name of a removed Topic's folder: when the Topic
// was removed, and the ids the name fits. <time>-<id> and
// <time>-<id>-<suffix> look alike, so a name whose last part could be a
// suffix fits two ids: 20261001-093000-data-python is Topic data-python, or
// Topic data removed in a second another removal of it had taken.
func readRemovedName(name string) (when time.Time, ids []string, ok bool) {
	m := removedFolderPattern.FindStringSubmatch(name)
	if m == nil {
		return time.Time{}, nil, false
	}
	when, err := time.Parse(removedTimeLayout, m[1])
	if err != nil {
		return time.Time{}, nil, false
	}
	rest := m[2]
	if validateTopicID(rest) == nil {
		ids = append(ids, rest)
	}
	if i := strings.LastIndexByte(rest, '-'); i > 0 && removalSuffixPattern.MatchString(rest[i+1:]) && validateTopicID(rest[:i]) == nil {
		ids = append(ids, rest[:i])
	}
	return when, ids, len(ids) > 0
}
