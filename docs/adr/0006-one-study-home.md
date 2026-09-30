# 0006. One Study Home

## Status

Accepted

## Context

v1 workspaces could live anywhere, and learners lost track of where a topic was and which
directory to start the agent in. Agent sandboxes only allow the agent's own edits under the
directory it was started in. Editors such as Cursor, VS Code and Kiro start the agent in
whatever folder is open, and language tooling expects a Topic's folder to be the project
root. Spaced repetition also needs a view across all topics ("what is due today").

## Decision

All Topics live in one Study home, `~/study` by default and overridable with `STUDY_HOME`.
The learner can start the agent in the Study home or inside any Topic. The core works out
the Active topic from the starting folder, falling back to the most recent Topic, and says
which one it chose and why. The Active topic is only a default for reading: every write
names its Topic explicitly, so two agent sessions on different Topics cannot redirect each
other.

Considered and rejected: topics anywhere, tracked by a registry. Stale paths and sandbox
limits make it fragile.

## Consequences

Topics outside the Study home are not supported; `study import` copies v1 workspaces in and
leaves the originals untouched. Each Topic is its own git repository, so it can be synced or
published on its own. The session-start hook only runs inside the Study home, so study
status never appears in unrelated projects.
