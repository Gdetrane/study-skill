# The study command line

`study` is Lamplight's command line for learners and for agents with a shell. Agents
without a shell use the same operations through `study mcp`. This page is the contract
that scripts and agents can rely on. Terms follow [CONTEXT.md](../CONTEXT.md).

## Commands

| Command | What it does |
|---|---|
| `study` | Same as `study status`. |
| `study status` | Shows the Study home, the Active topic and why it was chosen, every Topic, and any Topic that could not be read (`problems`). A broken Topic never stops the others from being listed. |
| `study topic create --title T [--id ID] [--goal G] [--dry-run]` | Creates a Topic folder with its settings, History and git repository. `--dry-run` validates and shows the result without writing. |
| `study topic update <topic> [--title T] [--goal G] [--dry-run]` | Changes a Topic's title or goal; flags left out stay as they are, and `--goal ""` removes the goal. The result is `{"topic": ..., "changed": bool}`: asking for the values the Topic already has changes nothing and records nothing. |
| `study library build <folder>` | Indexes the books in a folder (relative to where you run it) and replaces the Library index in the Study home. |
| `study library search <query> [--limit N]` | Ranks the books in the Library against the query. `--limit` defaults to 10 and is capped at 100; no matches is a success with an empty list. |
| `study checkpoint --topic ID --role agent\|learner [-m MESSAGE] [--dry-run]` | Saves the Topic's work as a git commit at a turn switch. Skips when nothing changed, refuses during a merge or rebase, and lists large files it saved. Never runs programs named in the Topic's git configuration. |
| `study mcp` | Runs the MCP server over stdin and stdout. |

The Study home is `STUDY_HOME` if set, otherwise `study_home` in
`$XDG_CONFIG_HOME/lamplight/config.toml`, otherwise `~/study`. It must be an absolute path
or start with `~/`, so every folder finds the same Study home.

## JSON output

Every command accepts `--json`. With it, `study` prints exactly one JSON document on
stdout and nothing else; diagnostics, if any, go to stderr. `--json` is honoured even
when an earlier argument is invalid. Help (`--help`), `--version`, `study mcp` (which
speaks MCP on stdout) and the generated completion and man-page commands print text.

Success:

```json
{
  "ok": true,
  "data": { "...": "the command's result" }
}
```

Failure:

```json
{
  "ok": false,
  "error": {
    "code": "already_exists",
    "message": "a Topic named c already exists"
  }
}
```

Error codes:

| Code | Meaning |
|---|---|
| `usage` | Unknown flag, unexpected argument, or similar command-line mistake. |
| `invalid_argument` | The request was understood but its values are invalid, such as an empty title. |
| `already_exists` | The thing to create exists already. |
| `not_found` | The thing named does not exist. |
| `newer_format` | A file was written by a newer version of `study`; upgrade to read it. |
| `corrupt` | A file Lamplight reads is damaged or missing, for example invalid TOML after a hand edit, or a Topic's git repository is missing, was replaced during a Checkpoint, or leads outside the Topic. Fix or restore it; the message names it. |
| `failed_precondition` | The request is valid but the Topic is not ready for it, for example a git merge is in progress or git has no identity. The message says what to do. |
| `busy` | Another program is using the Topic: another `study` process writing to it for too long, an editor using its git repository, or files that kept changing while they were being saved. Try again shortly; the message says what to do if it persists. |
| `internal` | Anything else, such as a file that cannot be read. |

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success, including empty results. |
| 1 | The command failed (`already_exists`, `not_found`, `newer_format`, `corrupt`, `failed_precondition`, `busy`, `internal`). |
| 2 | Usage error (`usage`, `invalid_argument`). |

## Flags in status

Each Topic in `study status --json` may carry `flags`: things replaying its History found
that need the learner's attention. Flags are reported, never resolved automatically.

```json
{ "kind": "conflict", "message": "...", "item": "topic.toml", "events": ["...", "..."] }
```

| Kind | Meaning |
|---|---|
| `held_event` | An Event could not be applied: it refers to something the History doesn't contain (yet), or this version of `study` doesn't know its type. |
| `conflict` | Two Events changed one item from the same version, or two different Events share an ID: usually changes made on two machines without syncing in between. |
| `edited_outside` | Content that approves or gates progress differs from the version its last Event recorded. |
| `interrupted_write` | A write to the Topic was interrupted; the next write to it finishes the job. |

`item` and `events` are present when the flag concerns a particular item or Events.

## Writes

Commands that write accept `--dry-run`, which validates the request and reports what would
change without writing anything.

Every write to a Topic takes the Topic's lock, so the CLI and the MCP server can run at
the same time. It records its Event in the History before it changes any content, so a
write interrupted by a crash is finished by the next write. Lock and intent-marker files
live in the Study home's `.lamplight/` folder, are local to the machine, and are never
synced.
