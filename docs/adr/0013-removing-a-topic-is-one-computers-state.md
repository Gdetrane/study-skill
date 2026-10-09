# 0013. Removing a Topic Is One Computer's State

## Status

Accepted

## Context

`study topic remove` moves a Topic's folder out of the Study home, into `.lamplight/removed`,
and deletes nothing. It printed a shell command that moved the folder back. That command
checked for the Topic id and then moved, in two steps, so a Topic created under the id in
between ended up with the removed one inside it.

Two rules pull against each other here:

- Every write is an Event in the Topic's History, so that every computer that has the Topic
  agrees on what happened to it.
- Removing a Topic happens on one computer. The Topic's git remote and the learner's other
  computers keep their copies, unchanged. An Event saying "removed" would reach them through
  the History and be false there.

A removed folder also has to be told apart from others, and nothing inside a Topic holds its
id: the id is the folder's name in the Study home. The removed folder's name, the time and
the id, cannot always be read back, because ids contain hyphens and a suffix is added when
two removals share a second.

## Decision

Removing and restoring a Topic record no Event. They move a folder on one computer and
change nothing inside the Topic, so they are that computer's state, like where a Source's
file is. This is not a general exemption: anything that changes what a Topic holds records
an Event.

`study topic restore` puts a removed Topic back.

- `study topic restore <topic>` takes the newest removal of that id. `--from <folder>` picks
  another by its folder's name, and `--list` shows what can be restored.
- It takes the lock of the Topic id, refuses while anything in the Study home has the id,
  and moves the folder with a rename. The lock keeps it apart from writes, removals, other
  restores and `study import`. Creating a Topic takes no lock; there the rename does the
  work, since it refuses a folder that holds anything.
- A record beside each removed folder, `<folder>.json` with a `format` number, says which
  Topic it was and when. It is written before the folder moves out and deleted after the
  folder moves back, so a crash leaves at worst a record without a folder, which is ignored.
- A folder whose record is missing or cannot be used has only its name. It is restored by
  id alone only when the name fits one id and is certainly the Topic's newest removal;
  otherwise the learner names the folder. A folder that holds no Topic is never restored.
- Both commands are for the learner: CLI-only, with no MCP tool.

Considered and rejected:

- **Recording `topic.removed` and `topic.restored`.** Other computers would replay a removal
  that never happened to their copy.
- **Reading the id from the folder's name alone.** It fits two ids in ordinary cases, such
  as `data-python` and `data` with a suffix.
- **Making `study topic create` take the lock too.** The rename already refuses a folder
  that holds anything, and tests race the two in both orders.

## Consequences

- A removed Topic can be restored only on the computer it was removed on. Another computer
  gets it back by cloning, as before.
- `.lamplight/removed` holds one more kind of file. A leftover record is harmless and is not
  cleaned up.
- The `restore` field of a removal's result keeps its name and holds the new command, so a
  script that ran it through a shell still works.
- With Go 1.26.0 to 1.26.4 the rename replaces an empty folder under the id. Nothing is
  lost, and the docs say "a folder that holds anything".
