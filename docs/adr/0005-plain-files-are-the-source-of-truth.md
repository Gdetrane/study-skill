# 0005. Plain Files, With History as the Record of Progress

## Status

Accepted

## Context

Study material has to outlive the tool, stay readable without it, produce reviewable git
diffs, and sync between machines through git. A first draft stored a Lesson's status in
three places (the Syllabus, the Lesson's header and History), so a crash halfway through
completing a Lesson left them disagreeing, and two machines rating the same Card produced
line-by-line merge conflicts. SQLite as the source of truth, with an export command, was
also considered.

## Decision

All state lives in plain files in the Topic's folder, split by what the file holds:

- **Content** is edited directly: the Syllabus structure (TOML), Lesson text (Markdown), and
  Card text (JSON Lines).
- **What happened** is the History (JSON Lines, append only). Lesson status, the Resume
  point and Card scheduling are not stored anywhere else; they are computed by replaying the
  History. go-fsrs gives the same result on every replay.

Every operation writes its Event first, with everything needed to apply it, and then
updates content files. On load, the core replays any Event whose effect is missing, so a
crash never leaves a half-done operation. Operations are idempotent: completing a Lesson
twice changes nothing, and Card IDs derive from the Lesson (`<lesson-id>.<n>`).

Every file carries a `format` number. A binary refuses to write a file newer than it
understands. File schemas are Lamplight's own types, never a library's structs.

Indexes (the Library, forecasts across Topics) are derived data that can be deleted and
rebuilt at any time.

## Consequences

Syncing a Topic between machines is a git pull: History files merge by union and replay to
the same state. Writes still take a per-Topic lock, because the CLI and the MCP server can
run at the same time. Hand edits to content files are allowed and validated on load;
comments in files the core rewrites are not preserved, and those files say so in a header.
