# 0005. Plain Files, With History as the Record of Progress

## Status

Accepted

## Context

Study material has to outlive the tool, stay readable without it, produce reviewable git
diffs, and sync between machines through git. A first draft stored a Lesson's status in
three places (the Syllabus, the Lesson's header and History), so a crash halfway through
completing a Lesson left them disagreeing, and two machines rating the same Card produced
line-by-line merge conflicts. Learners may also edit content files by hand, so recovery must
never undo a valid edit. SQLite as the source of truth, with an export command, was also
considered.

## Decision

All state lives in plain files in the Topic's folder, split by what the file holds:

- **Content** is edited directly: the Syllabus structure (TOML), Lesson text (Markdown), and
  Card text (JSON Lines).
- **What happened** is the History (JSON Lines, append only). Lesson status, the Resume
  point and Card scheduling are not stored anywhere else; they are computed by replaying the
  History. go-fsrs gives the same result on every replay.

Content files are authoritative for text: a hand edit always wins, and recovery never
rewrites text from an old Event. The History is authoritative for status, scheduling and
approvals.

**Writes.** Every operation takes a per-Topic lock and leaves an intent marker in a local,
unsynced file in the Study home's `.lamplight/`. It writes its Event first, with everything
needed to apply it and, for each item it edits (one Card, one Lesson file, the Syllabus), a
hash of the item before and after. It then replaces content files atomically and clears the
marker. Because of the lock, at most one Event can be unapplied after a crash, and recovery
inspects only that one: if the item matches the `before` hash, apply the Event; if it matches
`after`, clear the marker; if it matches neither, keep the hand edit and log it; if the file
is missing or unparseable, stop with a clear error and keep the Event. For content that
approves or gates something (the Syllabus and each Lesson's Check), the version recorded by
the last Event is compared on load, and a mismatch is flagged in `status`. Operations are
idempotent: completing a Lesson twice changes nothing.

**Ordering and identity.** Each Event carries a unique ID and a time from a hybrid logical
clock: the later of the wall clock and the latest time already in the History, plus one
tick, so an Event written after another was read always sorts after it. Replay orders Events
by time, then by ID, never by their position in the file, and applies each ID once. An Event
that refers to an item not yet known is held, and reported in `status` if it never resolves.
Entity IDs cannot collide across machines: Card IDs are `<lesson-id>.<random suffix>`, and
Explore Cards use `explore.<random suffix>`; display numbers come from position. Each Event is
written as a single append ending in a newline; a last line without one comes from an
interrupted write, so replay ignores it and the next write truncates it, logging the
fragment, before appending.

Every file carries a `format` number. A binary refuses to write a file newer than it
understands. File schemas are Lamplight's own types, never a library's structs.

Indexes (the Library, forecasts across Topics) are derived data that can be deleted and
rebuilt at any time.

## Consequences

v2.0 supports using a Topic on one machine at a time, synced through git between sessions.
History files merge by union, which can leave lines in any order; because replay sorts
Events, a merge in either direction replays to the same state. Conflicting changes made on
two machines anyway (two edits of one Card, a delete and a Review, a Revision whose base no
longer matches) are flagged in `status`, never resolved automatically, and textual conflicts
in content files such as `cards.jsonl` are resolved by hand. The hybrid logical clock and
random Card IDs are part of the format from the start, because adding them later would mean
migrating learners' data.

Writes still take a per-Topic lock, because the CLI and the MCP server can run at the same
time. Hand edits to content files are allowed and validated on load; comments in files the
core rewrites are not preserved, and those files say so in a header.
