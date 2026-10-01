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
| `study topic update <topic> [--title T] [--goal G] [--knowledge-base K [--notebook ID]] [--dry-run]` | Changes a Topic's title, goal or Knowledge base (`notebooklm` with the notebook's id, or `none`; see [Sources and Evidence](#sources-and-evidence)); flags left out stay as they are, and `--goal ""` removes the goal. Settings in `topic.toml` that this version does not know are kept. The result is `{"topic": ..., "changed": bool}`: asking for the values the Topic already has changes nothing and records nothing. |
| `study topic dismiss-flag <topic> <flag-id> [--dry-run]` | Dismisses one of the Topic's flags, by the id `status` shows, once the learner has looked at it. It records the decision in the History and never changes content. The result is `{"topic": ..., "flag": {...}, "changed": bool}`; dismissing a flag twice changes nothing. Only `held_event`, `conflict`, `damaged_line` and `clock_ahead` flags can be dismissed (see below). |
| `study library build <folder>` | Indexes the books in a folder (relative to where you run it) and replaces the Library index in the Study home. |
| `study library search <query> [--limit N]` | Ranks the books in the Library against the query. `--limit` defaults to 10 and is capped at 100; no matches is a success with an empty list. |
| `study source add <topic> (--file PATH \| --url URL) [--title T] [--notebooklm-id ID] [--dry-run]` | Adds a file or a web page as a Source of the Topic. A file is hashed, never parsed. Adding a file or URL the Topic already has is `already_exists`, naming the Source. |
| `study source update <topic> <source> [--title T] [--path P] [--notebooklm-id ID] [--dry-run]` | Changes a Source's title or NotebookLM id (`""` removes it), or records where a moved file is now; the file at `--path` must hold the same content. |
| `study source list <topic>` | Lists the Topic's Knowledge base and Sources, and where each file is on this computer. |
| `study evidence record <topic> --lesson L --source S --quote Q [--location LOC --location-from F] [--dry-run]` | Records an exact quote from a Source that a Lesson cites. `--quote -` reads the quote from stdin. Recording the same Evidence twice changes nothing. |
| `study evidence list <topic> [--lesson L]` | Lists the Evidence recorded in the Topic, or only what one Lesson cites. |
| `study checkpoint --topic ID --role agent\|learner [-m MESSAGE] [--dry-run]` | Saves the Topic's work as a git commit at a turn switch. Skips when nothing changed, refuses during a merge or rebase, and lists large files it saved. Never runs programs named in the Topic's git configuration. It waits for a write in progress and finishes an interrupted one first; `--dry-run` refuses (`failed_precondition`) while one is pending. |
| `study check <lesson> [--topic ID] [--timeout D]` | Runs a Lesson's Check on the current work and records the Attempt (see "Checks" below). Without `--topic`, it uses the Topic whose folder it runs in. |
| `study doctor` | Diagnoses the setup and says how to fix what it finds. It works even when nothing else does. Exits 1 when a Finding failed. |
| `study completion install [--shell S] [--dir D] [--yes] [--force] [--dry-run]` | Installs completions for bash, zsh or fish (default: from `$SHELL`) for your user. |
| `study completion uninstall [--shell S] [--dry-run]` | Removes what `install` added, for every shell or only `--shell`. |
| `study completion bash\|zsh\|fish\|powershell` | Prints a completion script, for packagers. |
| `study mcp` | Runs the MCP server over stdin and stdout. |

The Study home is `STUDY_HOME` if set, otherwise `study_home` in
`$XDG_CONFIG_HOME/lamplight/config.toml`, otherwise `~/study`. It must be an absolute path
or start with `~/`, so every folder finds the same Study home.

Global flags: `--json` (below) and `--log-level` (see [The Log](#the-log)).

Human output is styled when stdout is a terminal. Styles are dropped when it is not, and
colours are dropped with `NO_COLOR=1`; `CLICOLOR_FORCE=1` keeps them in a pipe.

## JSON output

Every command accepts `--json`. With it, `study` prints exactly one JSON document on
stdout and nothing else; diagnostics, if any, go to stderr. `--json` is honoured even
when an earlier argument is invalid. Help (`--help`), `--version`, `study mcp` (which
speaks MCP on stdout) and the completion-script and man-page commands print text; a command
group run without a subcommand, such as `study topic --json`, is a `usage` error. With
`--json`, `study` never writes to the terminal or waits for it, even when stdout is one.

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
| `canceled` | The command was stopped, by SIGTERM or Ctrl-C, before it finished; what it would have recorded was not recorded. `study check` stops every program the Check started. |
| `busy` | Another program is using the Topic: another `study` process writing to it for too long, an editor using its git repository, or files that kept changing while they were being saved. Try again shortly; the message says what to do if it persists. |
| `unhealthy` | `study doctor` only: a Finding failed. `data` still holds the full diagnosis. |
| `internal` | Anything else, such as a file that cannot be read. |

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success, including empty results, and `study doctor` with warnings only. |
| 1 | The command failed (`already_exists`, `not_found`, `newer_format`, `corrupt`, `failed_precondition`, `busy`, `canceled`, `unhealthy`, `internal`). |
| 2 | Usage error (`usage`, `invalid_argument`). |

## Flags in status

Each Topic in `study status --json` may carry `flags`: things replaying its History found
that need the learner's attention. Flags are reported, never resolved automatically.

```json
{ "id": "3f9c2a71b0", "kind": "conflict", "message": "...", "item": "topic.toml", "events": ["...", "..."] }
```

| Kind | Meaning | Goes away |
|---|---|---|
| `held_event` | An Event could not be applied: it refers to something the History doesn't contain (yet), or a newer version of `study` wrote it. While a Topic holds Events from a newer version, it can be read but not changed (`newer_format`). | when the Event becomes applicable, or when dismissed |
| `conflict` | Two Events changed one item from the same version, or two different Events share an ID: usually changes made on two machines without syncing in between. | when dismissed, after checking the item by hand |
| `damaged_line` | A line of `history.jsonl` is not an Event, such as a line cut off by an interrupted write that a merge moved into the middle of the file. It is skipped, never deleted. | when the line is fixed or deleted by hand, or when dismissed |
| `clock_ahead` | The History holds an Event dated more than a day after this computer's clock, so some machine's clock was wrong. New Events still sort after it. | when the clock catches up, or when dismissed |
| `edited_outside` | Content that approves or gates progress differs from the version its last Event recorded. | when Lamplight next records that item, or the content is restored |
| `interrupted_write` | A write to the Topic was interrupted; the next write to it finishes the job. | with the next write or Checkpoint |

`id` is stable across runs and machines, so a dismissal recorded on one machine applies on
every other. `item` and `events` are present when the flag concerns a particular item or
Events.

## Where the learner stopped

Once a Topic has a Syllabus or a Session, it carries `resume` in `study status --json`, and
human output shows it under the Active topic:

```json
{
  "lesson": "pointers", "lesson_title": "Pointers", "phase": "practicing",
  "next_step": { "step": "Fix the off-by-one in parse.c", "context": "...", "lesson": "pointers", "at": "..." },
  "open_session": { "id": "...", "opened": "..." }
}
```

`lesson` is the first Lesson in Syllabus order that is not done; `syllabus_done` is `true`
instead once every Lesson is. `next_step` is the latest Next step recorded, word for word.
`open_session` is a Session not closed yet: in progress, or ended without a Next step.

## Checks

A Lesson's Check is written in the YAML header of `lessons/<lesson-id>.md`:

```yaml
---
check:
  - id: tests
    describe: The tests pass
    run: [go, test, ./...]
---
```

`study check <lesson>` runs each criterion's command, an argument list rather than a shell
string, in `practice/<lesson-id>/`, with `STUDY_TOPIC`, `STUDY_LESSON` and
`STUDY_HELDOUT_DIR` set and no standard input.

- **Outcomes.** A criterion passes when its command exits with 0, fails when it exits with
  another status, and errors when it cannot start, is stopped by a signal, runs longer
  than `--timeout` (default 30 minutes), or leaves programs running in the background.
- **Processes.** Each command runs in a process group of its own. On a timeout, or when
  `study` receives SIGTERM or Ctrl-C, the whole group gets SIGTERM, then SIGKILL after
  two seconds; whatever a command leaves running when it exits is killed too. A Check
  stopped by SIGTERM records nothing and reports `canceled`.
- **The work.** The practice folder is snapshotted before and after the run, writing
  nothing to `.git`. The snapshot covers every file that is not ignored and every ignore
  rule that applies inside the folder (each `.gitignore` on the way and inside it,
  `.git/info/exclude`, the global excludes file), so build outputs can be ignored, but
  ignoring a file after a pass changes the snapshot. If the work changed during the run,
  the Attempt is errored and the changed files are named; list files a Check writes in the
  folder's `.gitignore`. The Attempt is also errored, without running anything, when the
  snapshot would have a blind spot: files the index marks skip-worktree or
  assume-unchanged, another git repository in the folder, a symbolic link leading outside
  it, or a folder whose files are all ignored.

`study check --json` prints the Attempt with the end of each criterion's output. The
History records the Attempt without any output, which could reveal test data:

```json
{
  "id": "...", "lesson": "pointers", "check_version": "sha256:...", "snapshot": "sha256:...",
  "outcome": "failed", "at": "...",
  "criteria": [{ "id": "tests", "outcome": "failed", "exit_code": 1, "output": "the end of what it printed" }]
}
```

The exit code is 0 whenever the Attempt was recorded, whatever its outcome. Checks run
only through the command line, from the agent's own shell, so the agent's sandbox applies
(ADR-0009); the MCP server reads Attempts (`check_results`) but never runs a Check.

A Lesson can be completed only on an Attempt that passed with the Check shown to the
learner when practicing last started (through `phase_set`) and with the work as it is now.
A Check edited afterwards is flagged in `status` and must be shown again.

Rubric and held-out criteria, and per-criterion results files, arrive in a later version;
until then a Check with one is reported as `corrupt`.

## Writes

Commands that write accept `--dry-run`, which validates the request and reports what would
change without writing anything.

Every write to a Topic takes the Topic's lock, so the CLI and the MCP server can run at
the same time. It records its Event in the History before it changes any content, so a
write interrupted by a crash is finished by the next write or Checkpoint. A dry run reports
what the real run would do after finishing such a write, and writes nothing. Lock and
intent-marker files live in the Study home's `.lamplight/` folder, are local to the
machine, and are never synced.

## Sources and Evidence

The Knowledge seam ([ADR-0007](adr/0007-knowledge-bases-return-evidence.md)) records where
a Topic's material comes from. `study` never parses a document and never calls a knowledge
service: the agent searches the Knowledge base itself, such as a NotebookLM notebook through
the NotebookLM MCP server, or reads the Sources when there is none, and records the quotes
it relied on.

- **Knowledge base**: `notebooklm`, with the notebook's id, or `none`. It is stored in the
  `[knowledge_base]` table of `topic.toml` and shown on the Topic in `status`. A Topic
  without one behaves as `none`.
- **Sources** are kept in `sources.jsonl`, one per line, each a separate item in the
  History, so Sources added on two machines never conflict. A Source's id is a slug of its
  title plus a random suffix, such as `strang-linear-algebra.k3f9a2`. A file Source records
  its absolute path, `hash` (`sha256:…`) and `size_bytes`; a URL Source its `url`. Either may
  carry the Source's `notebooklm_id`.
- **Where files are**: `source list` gives each file Source a `state`: `ok` (a file of the
  recorded size is at its path), `moved` (gone from its path, but a file with the same
  content is in the Library at `found_at`; record it with `source update --path`),
  `changed` (the file at the path is no longer the one added), or `missing`. Moved files
  are found through the Library index, so rebuild it with `study library build` after
  reorganising your books.
- **Evidence** is an exact quote, cited by a Lesson, with an optional `location` and
  `location_from`: `source` (read in the Source itself, such as a printed page number),
  `knowledge_base` (such as a NotebookLM citation), `learner`, or `estimate`. Evidence
  lives in the History only. Lessons without Evidence are marked, never blocked.

```json
{ "id": "k3f9a2b7qd", "lesson": "elimination", "source": "strang-linear-algebra.k3f9a2",
  "quote": "Elimination produces an upper triangular system.", "location": "p. 46",
  "location_from": "source", "recorded": "2026-10-01T09:30:00Z" }
```

## study doctor

`study doctor --json` reports a list of Findings. (A Finding is not a Check: Checks belong to
Lessons.) Each has a `status` of `ok`, `warn` or `fail`, and a `fix` for anything not `ok`.
Only a failure makes the setup unhealthy; then `ok` is `false`, the error code is
`unhealthy`, the exit code is 1, and `data` is still present:

```json
{
  "ok": false,
  "data": {
    "study_home": "/home/ada/study",
    "healthy": false,
    "findings": [
      { "name": "git", "status": "fail", "message": "git is not installed, or not on PATH",
        "fix": "install git 2.28 or newer" },
      { "name": "topic:physics", "status": "warn",
        "message": "physics is not a git repository, so Checkpoints cannot be saved",
        "fix": "git -C /home/ada/study/physics init --initial-branch=main" }
    ]
  },
  "error": { "code": "unhealthy", "message": "1 finding failed: git" }
}
```

`study_home` is absent when it cannot be resolved. Finding names, in order: `config`,
`study_home`, `git`, `git_identity` (only when git works), and, when the Study home
resolves, `local_state`, `topics`, one `topic:<id>` per Topic with a problem, and `library`;
then `log` and `completion`. A Topic folder study cannot read is a failed `topic:<id>`; a
Topic whose `.git` is missing, or is a file or a symlink (which Checkpoints refuse), is a
warning.

`study doctor` writes nothing: it checks permissions instead of writing test files, and the
Log file is not created by checking it.

## Completions

`study completion install` writes completions for one shell, for your user only, and
records what it did in `$XDG_STATE_HOME/lamplight/completions.json` so `uninstall` can undo
exactly that:

- fish: `$XDG_CONFIG_HOME/fish/completions/study.fish`. No configuration changes.
- bash: `completions/study` in the first folder of `$BASH_COMPLETION_USER_DIR` (a
  `:`-separated list), else `$XDG_DATA_HOME/bash-completion/completions/study`. Loaded by the
  bash-completion package; no configuration changes.
- zsh: `_study` in a writable folder already on `$fpath`. `study` cannot read zsh's `fpath`,
  so it uses `--dir` if given, else the first existing, writable folder in an exported
  `FPATH`, Oh My Zsh's completion cache (`$ZSH_CACHE_DIR/completions`, when `$ZSH` is set),
  or Homebrew's `$HOMEBREW_PREFIX/share/zsh/site-functions`. When none works, `study`
  installs to `$XDG_DATA_HOME/lamplight/completions/_study` and adds one line to
  `${ZDOTDIR:-~}/.zshrc` that sources it, but only with `--yes` or after asking at a
  terminal. Without consent it stops with a `usage` error and writes nothing.

What `study` will and won't touch:

- It never replaces a completion file it did not write: `install` stops with
  `already_exists` unless you pass `--force`. When a package already provides completions
  for the shell, `install` reports it in `provided_by` and installs nothing, again unless
  `--force`.
- The record keeps a SHA-256 of each script. `uninstall` deletes a script only while it is
  unchanged, and lists changed ones under `kept`.
- A `.zshrc` that is a symlink (stow, chezmoi) is edited where it points, so the link stays.
  One that is read-only or has other hard links is never rewritten: `install` still installs
  the script and lists the line to add by hand under `manual`, and `uninstall` lists the line
  to remove.
- `uninstall` finds its line even after an editor changed line endings. A line you edited is
  left alone, reported as `not_removed` with the line to remove by hand, and kept in the
  record so a later `uninstall` checks again. `.zshrc` is replaced atomically; if another
  program keeps changing it meanwhile, `uninstall` stops with `busy`.
- Installing to a different place first undoes the previous install. A failed install takes
  back what it wrote.
- `uninstall` removes a `.zshrc` that `install` created, once it is empty again.
- A damaged record is `corrupt` (delete it and reinstall); a record from a newer `study` is
  `newer_format` and is never rewritten.

`uninstall` reports `removed`, `kept` and `already_gone` scripts, `rc_lines` with a
`status` of `removed`, `already_gone` or `not_removed`, and any `manual` steps.

Packages install the scripts from `study completion <shell>` system-wide instead.

## The Log

The Log is application diagnostics, never the learner's activity. Records go to two places:

- JSON lines in `$XDG_STATE_HOME/lamplight/study.log` (default
  `~/.local/state/lamplight/study.log`), at the chosen level. The file is created on the
  first record.
- stderr, formatted for people. It shows warnings and errors, or the chosen level when one
  is chosen explicitly.

The level comes from `--log-level`, else `STUDY_LOG`, else `info`: one of `debug`, `info`,
`warn`, `error`, in any case. Anything else, including slog's offsets such as `error+8`, is
invalid: an invalid `--log-level` is a usage error; an invalid `STUDY_LOG` is ignored with a
warning. In `study mcp`, nothing but MCP messages is ever written to stdout,
and stderr shows only warnings and errors whatever the level.
