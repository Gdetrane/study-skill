# The study command line

`study` is Lamplight's command line for learners and for agents with a shell. Agents
without a shell use the same operations through `study mcp`. This page is the contract
that scripts and agents can rely on. Terms follow [CONTEXT.md](../CONTEXT.md).

## Commands

| Command | What it does |
|---|---|
| `study` | Same as `study status`. |
| `study status` | Shows the Study home, the Active topic and why it was chosen, the one action recommended for it (`recommended`), the Learner profile (`learner_profile`), every Topic, and any Topic that could not be read (`problems`). A broken Topic never stops the others from being listed. Each Topic can carry `flags`, its Resume point (`resume`), whether its Cards are ready (`cards`, never a count), its additions to the Learner profile (`learner_additions`) and `lessons_without_evidence`: Lessons started or done that cite no Evidence yet, once the Topic has Sources or a NotebookLM Knowledge base (a reminder, never a block). See "Where the learner stopped" below. It also carries its plan (see [Goal, Pace and Forecasts](#goal-pace-and-forecasts)): `state`, `deadline`, `pace`, `new_cards_per_day`, the `forecast` and the open `tasks` that matter now; human output shows them under the Active topic. |
| `study session open <topic> [--energy E] [--focus F] [--dry-run]` | Opens a Session and shows where you stopped. With an Energy and no Focus chosen yet, it suggests one (or to plan, or to stop); it lists every Session that ended without a Next step, and what changed since the last Checkpoint. See "Opening a Session" below. |
| `study session close <topic> --next-step S [--context C] [--session ID] [--dry-run]` | Closes the Session with a Next step that starts with a verb (see "Next steps" below). `--session` gives a Session left unclosed the note it never got. |
| `study session break-point <topic> <lesson> <break-point> --next-step S [--context C] [--dry-run]` | Records a Break point the Lesson's header declares, with a Next step. The Session stays open. Reaching the same Break point with the same Next step again changes nothing. |
| `study topic create --title T [--id ID] [--goal G] [--dry-run]` | Creates a Topic folder with its settings, History and git repository. `--dry-run` validates and shows the result without writing. |
| `study topic update <topic> [--title T] [--goal G] [--knowledge-base K [--notebook ID]] [--deadline D] [--pace H[@FROM] ... \| --clear-pace] [--new-cards-per-day N] [--state S] [--dry-run]` | Changes a Topic's title, goal, Knowledge base (`notebooklm` with the notebook's id, or `none`; see [Sources and Evidence](#sources-and-evidence)), the Goal's deadline, the Pace, the daily cap on new Cards, or its state (`active`, `paused` or `finished`); see [Goal, Pace and Forecasts](#goal-pace-and-forecasts). Flags left out stay as they are; `--goal ""` and `--deadline ""` remove them. `--pace` replaces the Pace: `--pace 10` is 10 hours a week from now on, and a further `--pace 3@2026-11-16` starts a period of 3 hours a week that day. Settings in `topic.toml` that this version does not know are kept. The result is `{"topic": ..., "changed": bool}`: asking for the values the Topic already has changes nothing and records nothing. Each kind of change is its own Event; if a later one fails, the error says which were recorded. A dry run shows the Topic as it would be, Forecast included. |
| `study task add <topic> <title...> [--by D] [--after M] [--dry-run]` | Adds a Task, a step toward the Goal that is not study. `--by` is a date the learner wants it done by, shown as is; `--after` names a Milestone, and the Task is shown once that Milestone is done. Adding a Task with the title of one already there returns that Task. The result lists the Tasks with their ids in `added_tasks`. |
| `study task done <topic> <task> [--undo] [--dry-run]` | Marks a Task done, or not done with `--undo`; marking it as it already is changes nothing. |
| `study task remove <topic> <task>... [--dry-run]` | Removes Tasks that no longer matter. |
| `study task list <topic> [--all]` | Lists the open Tasks, or all with `--all`. |
| `study topic dismiss-flag <topic> <flag-id> [--dry-run]` | Dismisses one of the Topic's flags, by the id `status` shows, once the learner has looked at it. It records the decision in the History and never changes content. The result is `{"topic": ..., "flag": {...}, "changed": bool}`; dismissing a flag twice changes nothing. Only `held_event`, `conflict`, `damaged_line`, `clock_ahead` and `card_flagged` flags can be dismissed (see below). |
| `study library build <folder>` | Indexes the books in a folder (relative to where you run it) and replaces the Library index in the Study home. |
| `study library search <query> [--limit N]` | Ranks the books in the Library against the query. `--limit` defaults to 10 and is capped at 100; no matches is a success with an empty list. |
| `study source add <topic> (--file PATH \| --url URL) [--title T] [--notebooklm-id ID] [--dry-run]` | Adds a file or a web page as a Source of the Topic. A file is hashed, never parsed. Adding a file or URL the Topic already has is `already_exists`, naming the Source. |
| `study source update <topic> <source> [--title T] [--path P] [--notebooklm-id ID] [--dry-run]` | Changes a Source's title or NotebookLM id (`""` removes it), which the History records, or says where its file is on this computer (`--path`, remembered locally only); the file at `--path` must hold the same content. |
| `study source list <topic>` | Lists the Topic's Knowledge base and Sources, and finds each file on this computer. |
| `study evidence record <topic> --lesson L --source S --quote Q [--location LOC --location-from F] [--dry-run]` | Records an exact quote from a Source that a Lesson cites. `--quote -` reads the quote from stdin. Recording the same Evidence twice changes nothing. |
| `study evidence retract <topic> <evidence> [--dry-run]` | Takes back Evidence recorded by mistake. The retraction is recorded, never deleted; retracting twice changes nothing. |
| `study evidence list <topic> [--lesson L] [--all]` | Lists the Evidence recorded in the Topic, or only what one Lesson cites. Retracted Evidence is listed only with `--all`. |
| `study syllabus [topic]` | Shows a Topic's Syllabus (the Active topic's when none is named): Milestones and Lessons with their display numbers and status, the Revisions waiting for the learner with their change, and a hand edit of `syllabus.toml` (see [The Syllabus](#the-syllabus)). |
| `study revision propose <topic> --summary S (--syllabus FILE \| --from-file) [--dry-run]` | Proposes a change to the Syllabus, the first one included. `--syllabus` names a file holding the whole Syllabus as it would be afterwards, as TOML like `syllabus.toml` or as JSON; `--from-file` proposes `syllabus.toml` as edited by hand. Nothing changes until the learner approves. |
| `study revision apply <topic> <revision> [--learner-said S] [--dry-run]` | Applies a proposed Revision once the learner approves. On a terminal, study shows the change and asks the learner directly; an agent relaying their answer from the conversation passes their words with `--learner-said`. Answering no records a decline. |
| `study revision decline <topic> <revision> [--learner-said S] [--dry-run]` | Records that the learner said no. The Syllabus is unchanged, and the Revision can no longer be applied. The result is `{"topic", "revision", "decision", "changed"}`; `apply` returns `{"topic", "revision", "syllabus", "approval", "changed"}`. Answering again changes nothing and reports the answer recorded the first time, on a terminal too. |
| `study checkpoint --topic ID --role agent\|learner [-m MESSAGE] [--dry-run]` | Saves the Topic's work as a git commit at a turn switch. Skips when nothing changed, refuses during a merge or rebase, and lists large files it saved. Never runs programs named in the Topic's git configuration. It waits for a write in progress and finishes an interrupted one first; `--dry-run` refuses (`failed_precondition`) while one is pending. |
| `study check <lesson> [--topic ID] [--timeout D]` | Runs a Lesson's Check on the current work and records the Attempt (see "Checks" below). Without `--topic`, it uses the Topic whose folder it runs in. |
| `study review [topic] [--energy E] [--limit N]` | Reviews the due Cards in the terminal, without an agent (see "Cards and Reviews" below). Without a Topic, it reviews the Active topic. Interactive only: with `--json` it is a usage error. |
| `study card list <topic> [--lesson L]` | Lists the Topic's Cards in the order they were written, with their display numbers and state. `--lesson explore` lists the Explore Cards. |
| `study card due <topic> [--energy E] [--limit N]` | Lists the Cards to review now, sized to the Energy, never saying how many more are due. A paused Topic lists none, and its result says `"paused": true`. |
| `study card add <topic> --prompt P --answer A [--lesson L] [--evidence E,...] [--dry-run]` | Adds a draft Card from a Lesson, or without `--lesson` an Explore Card, citing Evidence by id. Adding the same Card again changes nothing. |
| `study card edit <topic> <card> [--prompt P] [--answer A] [--evidence E,... \| --clear-evidence] [--dry-run]` | Changes a Card's prompt, answer or Evidence, keeping its schedule; settles a flag on the Card. |
| `study card suspend <topic> <card> [--undo] [--dry-run]` | Stops offering a Card for Review, or with `--undo` offers it again. |
| `study card delete <topic> <card> [--dry-run]` | Deletes a Card for good; deleting it again changes nothing. |
| `study card flag <topic> <card> [--note N] [--dry-run]` | Flags a Card as wrong or unclear, so it shows in `status` until fixed. |
| `study card review <topic> <card> --rating R [--draft keep\|edit\|drop] [--prompt P --answer A] [--request ID] [--dry-run]` | Records one Review, for scripts; at a draft's first Review `--draft` is required. A retry with the same `--request`, or repeating a draft's first decision, returns the Review already recorded (`changed: false`). A dry run shows the Card after the Review. |
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
| `conflict` | Two Events changed one item from the same version, or two different Events share an ID: usually changes made on two machines without syncing in between. Also a Next step recorded for a Lesson the other machine completed, skipped or removed, and one Session closed on two machines with different notes. | when dismissed, after checking the item by hand |
| `damaged_line` | A line of `history.jsonl` is not an Event, such as a line cut off by an interrupted write that a merge moved into the middle of the file. It is skipped, never deleted. | when the line is fixed or deleted by hand, or when dismissed |
| `clock_ahead` | The History holds an Event dated more than a day after this computer's clock, so some machine's clock was wrong. New Events still sort after it. | when the clock catches up, or when dismissed |
| `edited_outside` | Content that approves or gates progress differs from the version its last Event recorded. | when Lamplight next records that item, or the content is restored |
| `interrupted_write` | A write to the Topic was interrupted; the next write to it finishes the job. | with the next write or Checkpoint |
| `card_flagged` | The learner flagged a Card during a Review as wrong or unclear, with their note if any. | when the Card is edited or deleted, or when dismissed |
| `lesson_header` | The current Lesson's YAML header cannot be read, so its Break points are unknown. | when the header is fixed |

`id` is stable across runs and machines, so a dismissal recorded on one machine applies on
every other. `item` and `events` are present when the flag concerns a particular item or
Events.

## Where the learner stopped

Once a Topic has a Syllabus or a Session, it carries `resume` in `study status --json`, and
human output shows it under the Active topic:

```json
{
  "lesson": "pointers", "lesson_title": "Pointers", "phase": "practicing",
  "break_point": { "id": "swap", "describe": "swap() works on two ints" },
  "next_step": { "step": "Fix the off-by-one in parse.c", "context": "...", "lesson": "pointers", "at": "..." },
  "open_session": { "id": "...", "opened": "..." }
}
```

`lesson` is the first Lesson in Syllabus order that is neither done nor skipped;
`syllabus_done` is `true` instead once every Lesson is. `break_point` is the last Break point
reached in that Lesson, with its description from the Lesson's header. `next_step` is the
latest Next step recorded, word for word, unless its Lesson is done, skipped or no longer in
the Syllabus. `open_session` is the newest Session not closed yet: in progress, or ended
without a Next step.

`status` also recommends one action for the Active topic, never counting anything:

```json
"recommended": { "topic": "c", "action": "next_step", "text": "Fix the off-by-one in parse.c" }
```

`action` is one of `next_step` (the text is the Next step word for word), `plan` (no
Syllabus yet), `learn`, `practice` or `feedback` (the current Lesson's Phase), `reviews`
(every Lesson done, Cards ready) or `explore` (every Lesson done). The words match the
Focuses and the suggestions of `session_open` wherever they mean the same thing, and the
list is fixed, so skills can rely on it; `text` is English prose a skill may rephrase. The
recommendation always agrees with the Resume point. Each Topic's `cards` is
`{"ready": true}`, or `{"ready": false, "next_due": "..."}`: whether Reviews are possible
now, never how many Cards are due. `learner_profile` is the Study home's `learner.md` and a
Topic's `learner_additions` its own `learner.md`, when they exist as regular files (a
symbolic link is not followed); agents read both before teaching.

### Break points

A Lesson declares its Break points, in order, in its YAML header next to the Check:

```yaml
---
check:
  - id: tests
    run: [go, test, ./...]
break_points:
  - id: swap
    describe: swap() works on two ints
  - id: arrays
    describe: The same for arrays
---
```

Ids are slugs, unique within the Lesson. The Check and the Break points are read apart, so
a wrong type or an invalid value in `break_points` never makes the Check unreadable; a YAML
syntax error breaks the whole header, as it would any header. When the current Lesson's
header cannot be read, `status` flags it (`lesson_header`) instead of hiding its Break
points. `break_point_reached` (or `study session break-point`) records one, with a Next
step, in the current Lesson only: the Resume point's Lesson, Break point and Next step
always belong together. The next Session resumes from it. A Lesson removed by a Revision
loses its Break point, even if a later Revision adds it back.

### Next steps

A Next step is an action that starts with a verb and says what to act on: "Fix the
off-by-one in parse.go". `session_close`, `break_point_reached` and a `phase_set` that
gives one check it, without a language model, and refuse with `invalid_argument` a Next
step that:

- after any opening punctuation or symbols (quotes, `¿`, `**`, a backtick), does not start
  with a letter;
- is too short to say what to do: fewer than two words, or, in scripts written without
  spaces between words (Chinese, Japanese, Thai and the like), fewer than four characters;
- starts with a word that introduces a description rather than an action ("The parser is
  half done", "I'm on Lesson 3", "Done with the parser"); the first word is its leading run
  of letters, so contractions count by their first part. This word list is English; steps
  in other languages pass on the other rules;
- contains an invisible formatting character, such as a zero-width space.

All text Lamplight records also refuses control characters and bidirectional embedding,
override and isolate controls (U+202A–U+202E, U+2066–U+2069).

### Opening a Session

`session_open` (or `study session open`) returns, besides the Resume point:

- `suggested`, when an Energy is given and no Focus chosen yet: `suggest` is a Focus to offer
  (`learn`, `practice`, `reviews`, `explore`), `plan` (no Syllabus yet: plan it together)
  or `stop` (fumes with nothing due: write tomorrow's first step and end here); `reason` is
  English prose a skill may rephrase. A suggestion is never recorded.
- `cards`, as in `status`.
- `long_gap`, when the Topic was last worked on, by any Event, more than a week ago: start
  with a short recap and a warm-up, never with the size of a backlog.
- `unclosed`, every Session that ended without a Next step, newest first, each with its id
  and when it opened; a merge can leave one from each machine. Give each its note with
  `session_close` naming it.
- `changes`, when a Session was left unclosed: what changed since the last Checkpoint
  (`since`; absent before the first Checkpoint, when every file counts as added). `files`
  lists at most 50, each `added`, `modified` or `deleted`, and `more` counts the rest.
  Lamplight's own state files (`topic.toml`, `syllabus.toml`, `history.jsonl`,
  `cards.jsonl`, `sources.jsonl`) are left out. Listing them writes nothing to `.git` and
  runs nothing the repository names; when git cannot list them, such as during a merge,
  `error` says why and the Session opens anyway.

A note given late to an older Session never replaces a Next step recorded in a newer one. A
Next step that a merge brings in for a Lesson already done, skipped or removed never leads
the Resume point, and is flagged as a `conflict`; so is one Session closed on two machines
with different notes (the first note counts).

## Goal, Pace and Forecasts

A Topic's plan lives in `topic.toml`, next to its title and Goal, where the learner can read
and edit it; keys Lamplight does not know are kept:

```toml
deadline = "2026-12-01"      # the Goal's deadline, optional
new_cards_per_day = 10       # the daily cap on new Cards

[[pace]]                     # hours a week, in dated periods
hours_per_week = 10.0        # the first period may leave out from: from now on

[[pace]]
from = "2026-11-16"
hours_per_week = 3.0

[[tasks]]
id = "book-the-exam.k3f9a2"
title = "Book the exam"
by = "2026-11-01"            # optional, shown as is
after = "core"               # optional: shown once Milestone core is done
```

Whether a Topic is `active`, `paused` or `finished`, and which Tasks are done, come from its
History, never from a file. A paused Topic offers no Cards and has no Forecasts; a finished
one has no Forecasts and keeps offering its Cards. A setting that cannot be read, such as a
hand-edited Pace with negative hours, is left out and named in `settings_problems`; the rest
of the Topic stands.

The **Forecast** says when each Milestone ends at the Pace, never how far behind anything
is. It is in `status` (`forecast`) and in `study syllabus` and `syllabus`:

```json
{
  "pace": "10 h/week, then 3 h/week from 16 Nov 2026",
  "milestones": [
    { "milestone": "core", "title": "Core", "priority": "must", "deadline": "2026-10-16",
      "remaining_hours": 12, "ends": "2026-10-23",
      "after_deadline": true, "text": "At your Pace, Core ends 23 Oct 2026; its target is 16 Oct 2026." }
  ],
  "triage": { "milestone": "core", "deadline": "2026-10-16", "ends": "2026-10-23",
    "trim_hours": 2.9, "raise_pace_to": 14, "move_lessons": ["structs"],
    "text": "To finish Core by 16 Oct 2026: raise the Pace to about 14 h/week until then, move \"Structs\" past the deadline, or trim about 2.9 h of Stretch goals." }
}
```

- Work is the hour estimates of the Lessons neither done nor skipped, a Lesson in progress
  in full, taken through the Syllabus in order. A Milestone with a Lesson that has no
  estimate is not given a date, and nor are the ones after it: `unestimated` names the
  Lessons that need one. Without a Pace, `needs_pace` is `true` and no dates are given.
- Days are calendar days on this computer's clock, starting today. Each day adds its Pace
  period's hours a week divided by seven; the count is exact, in minutes.
- A Milestone's `deadline` is its target date, or the Goal's deadline for a `must`
  Milestone without one. `after_deadline` is set when it is forecast to end later.
- A **Triage** is offered for the first `must` Milestone forecast to end after its
  deadline: the Pace that finishes it in time (`raise_pace_to`, hours a week from today to
  the deadline, rounded up to a half hour), Lessons not started that fit it in time if
  moved past the deadline (optional Milestones' Lessons before it first, then its own from
  the last; left out when moving them all would not be enough), or hours of Stretch goals to
  trim (`trim_hours`). Nothing changes until the learner chooses: a Revision for the first
  two, a new Pace for the last. Once the deadline has passed, `deadline_passed` asks for a
  new date first.

## The Syllabus

`syllabus.toml` holds Milestones, each with an `outcome`, a `priority` (`must`, `if_time` or
`after_deadline`) and an optional `target` date, written as text (`"2026-12-01"`; a native
TOML date written by hand is read too), and Lessons with an `id`, a `title`, `hours` and
`skipped = true` for a skipped Lesson. Display numbers ("Lesson 2.3") come from position.
Lamplight rewrites the file only from approved Revisions, and keeps settings it does not
know at every level.

`study syllabus --json` returns:

```json
{
  "topic": "c",
  "milestones": [
    { "number": 1, "id": "basics", "title": "Basics", "priority": "must", "target": "2026-12-01",
      "lessons": [ { "number": "1.1", "id": "pointers", "title": "Pointers", "hours": 2,
                     "status": "in_progress", "phase": "practicing", "evidence": 1 } ] }
  ],
  "proposals": [ { "revision": "...", "summary": "...", "changes": { ... }, "stale": false } ],
  "edited_outside": true,
  "file_error": "syllabus.toml of c: Lesson 2.1 (maps): the title is empty"
}
```

A Lesson's `status` is `not_started`, `in_progress`, `done` or `skipped`. A proposal's
`changes` names Lessons by title: each change with its `kind`, `renumbered`
(`{lesson, title, from, to}`), `skipped_in_progress` (Lessons skipped while in progress: go
over what was already covered with the learner) and `text`, the whole change in plain
words. A `stale` proposal was based on a Syllabus that has changed since; propose it again.

| Change `kind` | Meaning |
|---|---|
| `first_syllabus` | The Topic's first Syllabus, followed by each Milestone and Lesson it adds. |
| `file_adopted` | The Revision adopts `syllabus.toml` as edited by hand (`--from-file`). |
| `milestone_added`, `milestone_removed` | A Milestone added or removed. |
| `milestone_changed` | A Milestone's title, outcome, priority, target date or position changed. |
| `lesson_added`, `lesson_removed` | A Lesson added or removed. |
| `lesson_renamed`, `lesson_moved`, `lesson_hours` | A Lesson's title, Milestone or hour estimate changed. |
| `lesson_skipped`, `lesson_unskipped` | A Lesson skipped, or its skip taken back. |
| `lessons_reordered` | Lessons that stay in a Milestone change places within it; `milestone` names it. |
| `settings_changed` | Only settings Lamplight does not know changed. |

Rules a Revision follows:

- Done and skipped Lessons keep their title, hours and Milestone, and are never removed; a
  done Lesson cannot be skipped. A Revision can take a skip back. Skipped Lessons keep
  their number, are left out of the Resume point, and cannot be studied.
- A Revision must change something; adopting the file as it is (`--from-file`) is the
  only Revision that may change nothing.
- A Revision is based on the Syllabus version the History recorded. While `syllabus.toml`
  differs from it (flagged `edited_outside`, with what keeps the edit from being adopted),
  the only Revision allowed adopts the file as it is: `--from-file`. A write interrupted
  before it rewrote `syllabus.toml` is no edit: the next write finishes it.
- Before asking the learner directly, study checks that the Revision can be applied as it
  stands, so an answer is never asked for and then thrown away.
- Approval records how the learner answered (`via`): `terminal` or `elicitation` when
  Lamplight asked them directly, with the question it showed (`shown`); `chat` when an agent
  relays their words (`learner_said`, required). What the learner adds when asked directly
  is kept on one line, without control characters, cut at 500 characters; it never makes
  their answer fail. Approvals are tamper-evident, not tamper-proof.
- A declined Revision is never applied. Changes made on two machines without syncing are
  flagged, never resolved: two Revisions (or a Revision and an adopted hand edit) approved
  from one Syllabus version, a Revision applied on one and declined on the other, and a
  Lesson completed on one and removed, skipped or rewritten on the other, in either order.
  One Revision approved on both machines is not a conflict.

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

## Cards and Reviews

A Card is one fact: a prompt and its expected answer, one line of `cards.jsonl`, in the order
Cards were written. Prompts and answers may span lines and hold tabs, for code; other control
characters are refused. A Card may cite Evidence by id (`--evidence`, `evidence` in MCP); the
ids must be recorded in the Topic's History and not retracted. Its ID is
`<lesson-id>.<random suffix>`, or `explore.<random suffix>` for an Explore Card written
without a Lesson, which is why no Lesson may be called `explore`. Its display number
("Card 4") comes from its position among the Topic's Cards. Whether a Card is a draft,
suspended, flagged or due is replayed from the History, never stored in the file.

A new Card is a draft until its first Review, where the learner keeps, edits or drops it.
Each day, counted on this computer's clock, at most 10 drafts are decided (the Topic's
`new_cards_per_day` changes that), so new Cards never pile up: `study card due` and
`due_cards` offer the Cards due first, earliest first, then as many drafts as are left of
the day's cap. A paused Topic offers no Cards until it is active again; a finished Topic
keeps offering them, at the growing intervals FSRS gives. Without `--limit`, the list is sized to the Energy, given with `--energy` or
taken from the open Session: 20 Cards at full, 10 at half, 3 at fumes, and 10 without one.
Suspended Cards are never offered. Neither command, nor `study review`, ever says how many
more Cards are due.

Scheduling replays every Review through FSRS-6 (go-fsrs v4, with fuzz and short-term steps
off), using the time each Review was really made, never earlier than the Card's previous
Review, so the same History always gives the same schedule on every machine. Every Review,
the first included, schedules the Card in days: Lamplight works in sessions, so a Card due
"in 10 minutes" would only come back next time anyway.

Reviews are safe to retry. A Review given a request id records nothing when the same id comes
again, and returns what was recorded; repeating a draft's first decision does the same. Over
MCP, `review_record` requires a request id, since a client may retry a call it saw fail. A
Review of a Card deleted on another machine, a delete that had not seen a Review made
elsewhere, and a draft decided on two machines are flagged in `status`.

`study review` shows each Card's prompt and asks the learner to recall the answer. Enter
shows it; the learner may type their answer first, and it is shown beside the real one to
compare. A line holding just `f` flags the Card as wrong or unclear, `s` skips it and `q`
stops. The learner then rates their recall with a single key: `1` again, `2` hard, `3` good,
`4` easy. At a new Card's first Review, `k` keeps it, `e` edits it (an empty line keeps the
prompt or the answer; the edit is shown and saved only after `y`) and `d` drops it, after
asking `y/N`. Before each question, whatever was typed ahead is discarded, so a key only ever
answers a question already on screen; arrow keys and other escape sequences are ignored.
Ctrl-C, SIGINT, SIGTERM or SIGHUP stop the session like `q`: the terminal is restored, every
Review made so far is kept, and `study` exits with 0 after "Stopped.". In a terminal each
key is one keystroke; otherwise each answer is read from one line of standard input, so a
script can drive it.

### Lines a merge leaves

`cards.jsonl` and `sources.jsonl` merge by union, so syncing can leave several lines for one
Card or Source. Every reader and every write picks among them by one rule, using what the
History recorded:

- a line at the version the History recorded last is the entity;
- lines at versions an earlier Event recorded are debris of the merge, and are ignored;
- a line at a version the History never recorded is an edit made outside Lamplight. In
  `cards.jsonl`, which is authoritative for text, one such edit wins, as a hand edit does
  without a merge, and several are a conflict in `status`. In `sources.jsonl`, whose text the
  History holds, the recorded version wins and any such edit is a conflict.

The order of the lines never matters, so every machine reads the same entity, and the next
change to it leaves a single line.

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

- **Knowledge base**: `notebooklm`, with the notebook's id, or `none`; `--notebook` alone
  means `notebooklm`. It is stored in the `[knowledge_base]` table of `topic.toml`, whose
  other keys stay while the kind stays, and shown on the Topic in `status`. A Topic without
  one behaves as `none`.
- **Sources**: the History records which Sources exist and what they are;
  `sources.jsonl` is the readable copy Lamplight writes, one line per Source. Edit Sources
  with `study source`, not by hand: a line added by hand is listed as `untracked` and is not
  a Source until `study source add` records it. A Source's id is a slug of its title plus a
  random suffix, such as `strang-linear-algebra.k3f9a2`. Each line holds only what is the
  same on every computer:

  ```json
  {"id":"strang-linear-algebra.k3f9a2","kind":"file","title":"Linear Algebra","file_name":"strang.pdf",
   "hash":"sha256:…","size_bytes":15,"notebooklm_id":"7b1e","notebooklm_notebook":"nb-42"}
  {"id":"notes-paper.p2x7q1","kind":"file","title":"Paper","topic_path":"notes/paper.pdf","hash":"sha256:…","size_bytes":9}
  {"id":"go-dev-blog-context.m4k8s3","kind":"url","title":"Go Concurrency Patterns: Context","url":"https://go.dev/blog/context"}
  ```

  A file inside the Topic is kept by its `topic_path`, so every clone has it; a file
  outside, by its original `file_name`, `hash` and `size_bytes`. A `notebooklm_id` belongs to
  the `notebooklm_notebook` it was recorded for; after the Topic moves to another notebook,
  `source list` marks it `notebooklm_stale`. URLs are normalised: the scheme and host are
  lowercased, a default port is dropped, an international host stays readable (Unicode,
  NFC), and addresses with a user name or password are refused.
- **Where files are** differs from one computer to the next, so it is local state, never
  synced: `.lamplight/sources/<topic>.json` in the Study home.

  ```json
  {"format": 1, "files": {"strang-linear-algebra.k3f9a2": {"path": "/home/ada/Books/strang.pdf",
   "size_bytes": 15, "mtime": "2026-10-01T09:30:00Z"}}}
  ```

  `source list` finds each file Source inside the Topic, where it was last found, or in the
  Library by its content (rebuild the Library with `study library build` after reorganising
  your books), and updates this file as it goes, recording nothing in the History. A file is
  hashed again only when its size or modification time changed. Each file Source gets a
  `state`: `ok`, `changed` (the file where it was last found has other content now, and the
  recorded content is nowhere in the Library) or `missing` (say where it is with
  `source update --path`). Files are opened without blocking and must be regular files, so a
  FIFO or a device is refused, and hashing stops when the command is cancelled.
- **Evidence** is an exact quote, cited by a Lesson, with an optional `location` and
  `location_from`: `source` (read in the Source itself, such as a printed page number),
  `knowledge_base` (a citation as the Knowledge base gave it; NotebookLM citations carry no
  page numbers), `learner`, or `estimate`. Evidence lives in the History only, and Evidence
  whose Source has not arrived from another machine yet is held until it does. Retracted
  Evidence no longer counts for its Lesson. Lessons without Evidence are marked, never
  blocked.

  ```json
  { "id": "k3f9a2b7qd", "lesson": "elimination", "source": "strang-linear-algebra.k3f9a2",
    "quote": "Elimination produces an upper triangular system.", "location": "p. 46",
    "location_from": "source", "recorded": "2026-10-01T09:30:00Z" }
  ```
- **Two machines**: Sources and Evidence added on two machines merge: each Source is its own
  item in the History, and `history.jsonl` and `sources.jsonl` both merge by union in git.
  One Source edited on both machines is flagged as a `conflict`, and the union merge may
  leave two lines for it in `sources.jsonl`; the History's version is used, and the next
  change to the Source leaves one line. Under the one-machine-at-a-time contract, adding the
  same file or URL, or the same Evidence, on both machines before syncing gives two of them;
  that is not flagged.

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
