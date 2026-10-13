# The study command line

`study` is Lamplight's command line for learners and for agents with a shell. Agents
without a shell use the same operations through `study mcp`. This page is the contract
that scripts and agents can rely on. Terms follow [GLOSSARY.md](../GLOSSARY.md).

## Commands

| Command | What it does |
|---|---|
| `study` | Same as `study status`. |
| `study status` | Shows the Study home, the Active topic and why it was chosen, the one action recommended for it (`recommended`), the Learner profile (`learner_profile`), every Topic, and any Topic that could not be read (`problems`). A broken Topic never stops the others from being listed. Each Topic can carry `flags`, its Resume point (`resume`), whether its Cards are ready (`cards`, never a count), its additions to the Learner profile (`learner_additions`) and `lessons_without_evidence`: Lessons started or done that cite no Evidence yet, once the Topic has Sources (a reminder, never a block). A Topic that names a Knowledge base plugin carries `indexing`: which of its Sources the plugin has indexed on this computer, and the one command to run when some are not; the plugin has two seconds to say, and is never waited for (see [What is indexed](#what-is-indexed)). See "Where the learner stopped" below. It also carries its plan (see [Goal, Pace and Forecasts](#goal-pace-and-forecasts)): `state`, `deadline`, `pace`, `new_cards_per_day`, the `forecast` and the open `tasks` that matter now; human output shows them under the Active topic. |
| `study session open <topic> [--energy E] [--focus F] [--session ID] [--dry-run]` | Opens a Session and shows where you stopped. With an Energy and no Focus chosen yet, it suggests one (or to plan, or to stop); it lists every Session that ended without a Next step, and what changed since the last Checkpoint. With `--session` and `--focus`, it records the Focus chosen for that open Session instead of opening another. See "Opening a Session" below. |
| `study session close <topic> --next-step S [--context C] [--session ID] [--dry-run]` | Closes the Session with a Next step that starts with a verb (see "Next steps" below), and saves the work with a Checkpoint. `--session` gives a Session left unclosed the note it never got. |
| `study session break-point <topic> <lesson> <break-point> --next-step S [--context C] [--dry-run]` | Records a Break point the Lesson's header declares, with a Next step, and saves the work with a Checkpoint. The Session stays open. Reaching the same Break point with the same Next step again changes nothing. |
| `study topic create --title T [--id ID] [--goal G] [--dry-run]` | Creates a Topic folder with its settings, History and git repository. `--dry-run` validates and shows the result without writing. |
| `study topic update <topic> [--title T] [--goal G] [--knowledge-base K] [--deadline D] [--pace H[@FROM] ... \| --clear-pace] [--new-cards-per-day N] [--level L] [--approach A] [--state S] [--dry-run]` | Changes a Topic's title, goal, Knowledge base (`none`, or the name of a Knowledge base plugin, which need not be registered on this computer; see [Sources and Evidence](#sources-and-evidence)), the Goal's deadline, the Pace, the daily cap on new Cards, the Level (the learner's choice, which holds until the next Assessment), the Approach (`concepts`, `project` or `challenges`; see [Assessments, Level and learning signals](#assessments-level-and-learning-signals)), or its state (`active`, `paused` or `finished`); see [Goal, Pace and Forecasts](#goal-pace-and-forecasts). Flags left out stay as they are; `--goal ""` and `--deadline ""` remove them. `--pace` replaces the Pace: `--pace 10` is 10 hours a week from now on, and a further `--pace 3@2026-11-16` starts a period of 3 hours a week that day. Settings in `topic.toml` that this version does not know are kept. The result is `{"topic": ..., "changed": bool, "note"?: text}`: asking for the values the Topic already has changes nothing and records nothing, and `note` is there when the update names a Knowledge base plugin this computer cannot use, with the command that puts it right. Each kind of change is its own Event; if a later one fails, the error says which were recorded. A dry run shows the Topic as it would be, Forecast included. |
| `study assessment record <topic> --file F [--dry-run]` | Records an Assessment read from a JSON file (`--file -` reads stdin) holding one object in the shape the `assessment_record` tool takes; anything after it is refused. With a `level`, it sets the Topic's Level. A retry records nothing (see below). A dry run shows the Assessment without an id and the Level the Topic would have. See [Assessments, Level and learning signals](#assessments-level-and-learning-signals). |
| `study assessment list [topic]` | Lists a Topic's Assessments, newest first in the text and in `--json`, and its Level. |
| `study hint record <lesson> --requested-by learner\|agent [--topic ID] [--kind K] [--note N] [--request ID] [--dry-run]` | Records a hint given for a Lesson: `nudge`, `explanation` or `step`, asked for by the `learner` or offered unasked by the `agent`. Without `--topic`, it uses the Topic whose folder it runs in. |
| `study signals [topic] --json` | For agents: a Topic's learning signals, in JSON. Hidden from help, and without `--json` it only says it is for agents: the signals are never shown to the learner. Reads only. |
| `study task add <topic> <title...> [--by D] [--after M] [--dry-run]` | Adds a Task, a step toward the Goal that is not study, to `tasks.jsonl`. `--by` is a date the learner wants it done by, shown as is; `--after` names a Milestone of the Syllabus, and the Task is shown once that Milestone is done. Adding a Task with the title of one already there returns that Task, done or not. The result lists the Tasks with their ids in `added_tasks`; a dry run gives a new Task no id yet. |
| `study task done <topic> <task> [--undo] [--dry-run]` | Marks a Task done, or not done with `--undo`; marking it as it already is changes nothing. |
| `study task remove <topic> <task>... [--dry-run]` | Removes Tasks that no longer matter. A Task removed already changes nothing; one that never existed is `not_found`. |
| `study task list <topic> [--all]` | Lists the open Tasks, or all with `--all`, and the lines of `tasks.jsonl` that are not Tasks (`problems`). |
| `study topic dismiss-flag <topic> <flag-id> [--dry-run]` | Dismisses one of the Topic's flags, by the id `status` shows, once the learner has looked at it. It records the decision in the History and never changes content. The result is `{"topic": ..., "flag": {...}, "changed": bool}`; dismissing a flag twice changes nothing. Only `held_event`, `conflict`, `damaged_line`, `clock_ahead` and `card_flagged` flags can be dismissed (see below). |
| `study topic remove <topic> [--dry-run]` | Moves the Topic's folder, whole (git history included), out of the Study home into `.lamplight/removed/<YYYYMMDD-HHMMSS>-<topic>`, the time in UTC, and records nothing in the History. Nothing is deleted: the result, `{"topic": ..., "moved_to": path, "restore": command}`, carries the exact command that restores this removal, `study topic restore <topic> --from <folder>`, where the folder is the last part of `moved_to`; a later removal of the same id does not change what it restores. A record beside the folder, `<folder>.json`, says which Topic it was. The removal is local to this computer: the Topic's git remote and other computers' copies are untouched. It waits for a write in progress (a dry run says so in `note`, without waiting), refuses (`failed_precondition`) while an interrupted write waits to be finished, and fails with `not_found` if the Topic is removed or replaced while it waits; every other write that was waiting then fails the same way and writes nothing. Stopped while it waits, it reports `canceled` and has moved nothing. It is for the learner, such as before importing a v1 workspace again with `--not-done`; agents have no tool for it. |
| `study topic restore <topic> [--from FOLDER] [--dry-run]`, `study topic restore --list` | Moves a removed Topic's folder back into the Study home under its id: the newest removal of that Topic on this computer, or with `--from` the one kept in that folder of `.lamplight/removed` (its name, as `--list` and the removal's `moved_to` give it; an empty `--from` is a usage error, never "the newest"). The result is `{"topic", "path", "restored_from", "removed"}`. Nothing inside the Topic is written and nothing is recorded in the History: like the removal, it is this computer's state. It takes the lock of the Topic id, then refuses (`already_exists`) while anything in the Study home has that id, and moves nothing: a Topic created or imported under the id while the restore waits is left untouched, and the removed one is never put inside it. A dry run does not wait: when another study process holds the lock, such as an import under the id, it says so in `note`. It is `not_found` when no such removal is kept, `invalid_argument` when `--from` names a removal of another Topic, `corrupt` when the folder holds no Topic (neither `topic.toml` nor `history.jsonl`), `failed_precondition` when a write marker under the id belongs to a Topic that is gone, and `canceled` when it is stopped while it waits for the lock. `--list` (no Topic, no other flag) lists the folders in `.lamplight/removed` that are named as removals, newest first, as `{"removed": [{"topic"?, "folder", "path", "removed", "restore"?, "note"?}]}`, `restore` being the command for that one. The record beside a folder says which Topic it was, and when more exactly than the folder's name does; it is used only when its Topic and its time fit that name, which fixes the second. A folder whose record is missing or cannot be used has only its name: the time to the second, and an id when the name fits one (`<time>-<id>` and `<time>-<id>-<suffix>` look alike, so some names fit two). Nothing is guessed. When the name fits two ids, `topic` and `restore` are left out, `note` says why, and the folder is restored by naming both, `study topic restore <id> --from <folder>`. When such a folder may be a newer removal of the Topic than the newest one known, restoring by id alone is `failed_precondition` and names the folders to choose from. A folder that holds no Topic is listed the same way, without `topic` and `restore` and with a `note`, and is never restored. A record in a newer format is refused (`newer_format`) before anything else in it is read, and its folder is not judged by where this version keeps a Topic's files: the `note` says to upgrade study, never to delete. For the learner, like the removal; agents have no tool for it. |
| `study import <v1-workspace> [--topic ID] [--not-done LESSON]... [--dry-run]` | Imports a v1 study workspace as a new Topic, with its history, leaving the original untouched. `--not-done` keeps a Lesson open that v1's records prove done. See [Importing a v1 workspace](#importing-a-v1-workspace). |
| `study library build <folder>` | Indexes the books in a folder (relative to where you run it) and replaces the Library index in the Study home. |
| `study library search <query> [--limit N]` | Ranks the books in the Library against the query. `--limit` defaults to 10 and is capped at 100; no matches is a success with an empty list. |
| `study source add <topic> (--file PATH \| --url URL) [--title T] [--dry-run]` | Adds a file or a web page as a Source of the Topic. A file is hashed, never parsed. Adding a file or URL the Topic already has is `already_exists`, naming the Source. |
| `study source update <topic> <source> [--title T] [--path P] [--dry-run]` | Changes a Source's title, which the History records, or says where its file is on this computer (`--path`, remembered locally only); the file at `--path` must hold the same content. |
| `study source list <topic>` | Lists the Topic's Knowledge base and Sources, and finds each file on this computer. When the Topic names a Knowledge base plugin, each Source has an `index` (`indexed`, `outdated`, `indexing`, `failed`, `not_indexed` or `unknown`) and the list an `indexing` with the one command to run next; the plugin has two seconds to say, and is never waited for. See [What is indexed](#what-is-indexed). |
| `study source index <topic> [<source>...] [--again] [--dry-run]` | Asks the Topic's Knowledge base plugin to index the Topic's Sources that are not indexed, or the ones named, one after the other, and follows each to its end; progress goes to stderr. A file is handed over by its path and content hash, a web page by its address. `--again` asks for Sources that are indexed already too, and has a web page fetched again. `--dry-run` starts the plugin, asks what it has indexed and lists what would be asked of it, without asking. The result is `{"topic", "plugin", "collection", "sources": [{"id", "title", "path"? \| "url"?, "state", "progress"?, "reason"?, "asked"?, "skipped"?}]}`. A Source that fails is not an error of the command: its `state` is `failed`, with the plugin's `reason`, and the exit code is 0. Stopped, it tells the plugin to stop and reports `canceled`; run again, it carries on. It records no Event. It is the one thing that indexes, and agents have no tool for it: they run it in their own shell. See [study source index](#study-source-index). |
| `study evidence search <topic> <query> [--limit N] [--source S]...` | Asks the Topic's Knowledge base plugin for the Passages of the Topic's Sources that answer the query, best first: each with its `id`, its `source` and `title`, its `text` (the Source's own, as the plugin extracted it), its `location` when the plugin knows it, and its `score` for this query. Only the Sources the plugin holds indexed on this computer, as the Topic records them, are searched: the result lists the others in `left_out`, says so in `note`, and gives the one command to run next for them in `action`. `--limit` defaults to 5 and is capped at 50; `--source` searches only that Source, and can be repeated. An answer holds at most 256 KiB of Passages' text. It is `failed_precondition` when the Topic names no plugin, whatever Sources it has, and when the plugin cannot be had or does not answer within 45 seconds. A Topic that names a plugin and has no Sources, or none indexed, is a success with no Passages. It reads the plugin's index and never a Source, and records nothing. The MCP tool is `evidence_search`. See [Searching a Topic's Sources](#searching-a-topics-sources). |
| `study evidence record <topic> --lesson L --source S --quote Q [--passage P]... [--location LOC --location-from F] [--dry-run]` | Records an exact quote from a Source that a Lesson cites. `--quote -` reads the quote from stdin. With `--passage`, the id of the Passage the quote is from as `study evidence search` gave it, the quote is checked: it is refused (`invalid_argument`) when it is not in the Passage's text, and its location is the Passage's, so `--location` is then refused too. Repeat `--passage`, in order, for a quote that runs from one Passage into the next: each names one Passage, and one given without an id is a usage error. Without `--passage` nothing is checked. Recording the same Evidence twice changes nothing. A dry run asks the plugin and checks the quote, as the real run does. See [Evidence checked against its Passage](#evidence-checked-against-its-passage). |
| `study evidence retract <topic> <evidence> [--dry-run]` | Takes back Evidence recorded by mistake. The retraction is recorded, never deleted; retracting twice changes nothing. |
| `study evidence list <topic> [--lesson L] [--all]` | Lists the Evidence recorded in the Topic, or only what one Lesson cites, each with whether its quote was checked against the Passages it is from (`checked`, and then `passages` and `plugin`). Retracted Evidence is listed only with `--all`. |
| `study knowledge-base add <name> [--allow-study-home-arguments] [--replace] [--dry-run] (--url URL \| -- <command> [args...])` | Registers a Knowledge base plugin on this computer under a name, by the command that starts it, given after `--`, or by the http or https URL it answers at. The command's program is found on `PATH`, or at the path given, and registered by its absolute path. A program inside the Study home, or reached through it, is `invalid_argument`; so is an argument that names something there, unless `--allow-study-home-arguments` is given, and one that names a file relative to the folder `study` is started in. A program, or an argument that is not allowed so, named by a path `study` cannot follow to its end is `failed_precondition`: it is never taken for one outside the Study home. Registering a plugin as it is registered already changes nothing (`changed: false`); a name registered as something else is `already_exists` unless `--replace` is given. What it registers is the learner's, and never carries the mark of a registration `study setup` made (`by_setup`). It records no Event, and agents have no tool for it. See [Knowledge base plugins](#knowledge-base-plugins). |
| `study knowledge-base list` | Lists the Knowledge base plugins registered on this computer, by name, and where the registry is. A plugin `study` does not use as it is registered carries a `problem`, and the one `study setup` registered says so (`by_setup`). |
| `study knowledge-base remove <name> [--dry-run]` | Removes a Knowledge base plugin from this computer's registry, leaving its program or service as it is; a name that is not registered is `not_found`. It records no Event, and agents have no tool for it. |
| `study knowledge-base check <name>` | Starts the Knowledge base plugin registered under the name, or reaches it at its URL, asks which version of Lamplight's contract it speaks and what it can do, has it test what it depends on, and stops it again. The result is `{"name", "command"?, "address"?, "contract": {"version", "sources", "search", "locations"}, "server": {"name", "version"?}, "data_folder"?, "findings": [{"name", "ok", "message"}]}`. A name that is not registered is `not_found`; a plugin that cannot be started, does not start, does not answer in time, is no MCP server or does not speak the contract is `failed_precondition`, with what to do; one that speaks a newer contract is `newer_format`. A finding that is not all right is no failure: the plugin answered, and said what it lacks. Nothing is left running. It indexes and searches nothing, records no Event, and agents have no tool for it. See [Knowledge base plugins](#knowledge-base-plugins). |
| `study syllabus [topic]` | Shows a Topic's Syllabus (the Active topic's when none is named): Milestones and Lessons with their display numbers and status, the Revisions waiting for the learner with their change, and a hand edit of `syllabus.toml` (see [The Syllabus](#the-syllabus)). |
| `study revision propose <topic> --summary S (--syllabus FILE \| --from-file) [--dry-run]` | Proposes a change to the Syllabus, the first one included. `--syllabus` names a file holding the whole Syllabus as it would be afterwards, as TOML like `syllabus.toml` or as JSON; `--from-file` proposes `syllabus.toml` as edited by hand. Nothing changes until the learner approves. |
| `study revision apply <topic> <revision> [--learner-said S] [--dry-run]` | Applies a proposed Revision once the learner approves. On a terminal, study shows the change and asks the learner directly; an agent relaying their answer from the conversation passes their words with `--learner-said`. Answering no records a decline. |
| `study revision decline <topic> <revision> [--learner-said S] [--dry-run]` | Records that the learner said no. The Syllabus is unchanged, and the Revision can no longer be applied. The result is `{"topic", "revision", "decision", "changed"}`; `apply` returns `{"topic", "revision", "syllabus", "approval", "changed"}`. Answering again changes nothing and reports the answer recorded the first time, on a terminal too. |
| `study checkpoint --topic ID --role agent\|learner [-m\|--message MESSAGE] [--dry-run]` | Saves the Topic's work as a git commit at a turn switch. Skips when nothing changed, refuses during a merge or rebase, and lists large files it saved. Never runs programs named in the Topic's git configuration. It waits for a write in progress and finishes an interrupted one first; `--dry-run` refuses (`failed_precondition`) while one is pending. |
| `study check <lesson> [--topic ID] [--timeout D]` | Runs a Lesson's Check on the current work and records the Attempt (see "Checks" below). Without `--topic`, it uses the Topic whose folder it runs in. |
| `study rubric grade <lesson> <criterion> --grade G [...]` | Grades a rubric item of a Lesson's Check (see "Checks"). |
| `study results <lesson> [--topic ID]` | Shows a Lesson's Check results and whether it can be completed (see "Checks"). |
| `study lesson <lesson> [--topic ID]` | Shows one Lesson: its number, Milestone, status and Phase, its file, and what its YAML header declares (the Check's criteria with their commands, and the Break points), with the current Check version, whether it is the one shown to the learner (`check_shown`), and whether the header can be read (`header_readable`, `header_error`). Runs nothing and never shows Held-out data. Without `--topic`, it uses the Active topic. The MCP tool is `lesson`. |
| `study history [topic] [--limit N] [--type T] [--lesson L]` | Shows a Topic's most recent Events, newest first, each as `{id, type, at, clock_behind?, summary, lesson?, item?, held?}` with `more` set when older ones match too. `--limit` defaults to 10 and is capped at 100; `--type` keeps the Events whose type starts with T (such as `card.` or `attempt`; `evidence` matches `evidence.recorded`, `evidence.retracted` and `evidence_passages.recorded`, and `evidence.` leaves the last out), `--lesson` those about one Lesson, its Card Events and Reviews included. Each Event appears once, as replay applied it. Entries are ordered by the History's clock (`time`) but show the writer's wall clock (`at`), so an Event from a machine whose clock was behind can look older than the one before it: `clock_behind` marks it ("clock behind" in the text). `held` marks an Event replay did not apply (`status` flags it), and Events written by a newer version of study are not shown. Entries never carry an Event's full payload: summaries are built from the kind of Event and ids only, so no Held-out results, notes, quotes, titles or Next steps. It is for understanding what happened, never a tally for the learner. The MCP tool is `history`. |
| `study review [topic] [--energy E] [--limit N]` | Reviews the due Cards in the terminal, without an agent (see "Cards and Reviews" below). Without a Topic, it reviews the Active topic. Interactive only: with `--json` it is a usage error. |
| `study card list <topic> [--lesson L]` | Lists the Topic's Cards in the order they were written, with their display numbers and state. `--lesson explore` lists the Explore Cards. |
| `study card due <topic> [--energy E] [--limit N]` | Lists the Cards to review now, sized to the Energy, never saying how many more are due. A paused Topic lists none, and its result says `"paused": true`. |
| `study card add <topic> --prompt P --answer A [--lesson L] [--evidence E,...] [--dry-run]` | Adds a draft Card from a Lesson, or without `--lesson` an Explore Card, citing Evidence by id. Adding the same Card again changes nothing. |
| `study card edit <topic> <card> [--prompt P] [--answer A] [--evidence E,... \| --clear-evidence] [--dry-run]` | Changes a Card's prompt, answer or Evidence, keeping its schedule; settles a flag on the Card. |
| `study card suspend <topic> <card> [--undo] [--dry-run]` | Stops offering a Card for Review, or with `--undo` offers it again. |
| `study card delete <topic> <card> [--dry-run]` | Deletes a Card for good; deleting it again changes nothing. |
| `study card flag <topic> <card> [--note N] [--dry-run]` | Flags a Card as wrong or unclear, so it shows in `status` until fixed. |
| `study card review <topic> <card> --rating R [--draft keep\|edit\|drop] [--prompt P --answer A] [--request ID] [--dry-run]` | Records one Review, for scripts; at a draft's first Review `--draft` is required. A retry with the same `--request`, or repeating a draft's first decision, returns the Review already recorded (`changed: false`). A dry run shows the Card after the Review. |
| `study doctor` | Diagnoses the setup and says how to fix what it finds: the Knowledge base plugins this computer has, what each found when it tested what it depends on, and which Sources they have not indexed, among it. It works even when nothing else does. Exits 1 when a Finding failed. See [study doctor](#study-doctor). |
| `study completion install [--shell S] [--dir D] [--yes] [--force] [--dry-run]` | Installs completions for bash, zsh or fish (default: from `$SHELL`) for your user. |
| `study completion uninstall [--shell S] [--dry-run]` | Removes what `install` added, for every shell or only `--shell`. |
| `study completion bash`, `study completion zsh`, `study completion fish`, `study completion powershell` `[--no-descriptions]` | Prints a completion script, for packagers. `--no-descriptions` leaves out the help text shown next to each completion. |
| `study man` | Prints study's man page in roff, for packagers to install as `study.1` (hidden from help). The page's date is the one `SOURCE_DATE_EPOCH` names (seconds since 1970, UTC) when it is set, so a package built twice from one source holds the same page, and today's otherwise. It has no JSON form: with `--json` it is a usage error. |
| `study setup [--agent claude\|codex\|all] [--dry-run] [--force]` | Installs the `lamplight` skill and registers `study mcp` with Claude Code and Codex. When `study-shelf` is installed, it also registers it as the Knowledge base plugin `shelf`, with a mark in the registry that says the registration is setup's, and leaves a plugin the learner registered under that name as it is. A `study` or a `study-shelf` in a temporary folder is refused unless `--force` is given. See [Setting up agents](#setting-up-agents). |
| `study setup --check [--agent A]` | Reports what is missing or stale, changing nothing; exits 1 (`unhealthy`) when setup has something to do. |
| `study setup --remove [--agent A] [--dry-run]` | Undoes exactly what `study setup` did. Of the Knowledge base plugins it removes the one that carries setup's mark, and only without `--agent`. |
| `study claude-plugin-path` | Writes the Claude Code plugin and prints its folder; the plugin marketplace runs it. |
| `study mcp` | Runs the MCP server over stdin and stdout. On start it refreshes a skill copy that `study setup` installed, when nobody changed it. |

The Study home is `STUDY_HOME` if set, otherwise `study_home` in
`$XDG_CONFIG_HOME/lamplight/config.toml`, otherwise `~/study`. It must be an absolute path
or start with `~/`, so every folder finds the same Study home.

`XDG_CONFIG_HOME` counts only as an absolute path, and so do the other folders named
below, `XDG_DATA_HOME`, `XDG_STATE_HOME` and `XDG_CACHE_HOME`. A relative one is ignored, as
the XDG Base Directory Specification says, and its default under the home folder is used
(`~/.config`, `~/.local/share`, `~/.local/state`, `~/.cache`): the folder would otherwise be
wherever `study` is started, inside a Topic too, and a file there would choose the Study
home. `study doctor` warns about it and says how to fix it. A relative `HOME` is not used
either, and has no default: a command that needs the home folder fails and says so, and so
does one given a path that starts with `~` (`invalid_argument`), which is never taken for a
folder called `~` where `study` is started. A command that needs no home folder, such as
`study completion install` when every folder it writes to is given as an absolute path,
works whatever `HOME` is. git is
started without the ones that are relative, so it looks for your configuration where `study`
does. The Knowledge base plugin registry is stricter: it is not read at all from a
configuration folder that is not an absolute path (see
[Knowledge base plugins](#knowledge-base-plugins)).

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
A result can hold text `study` did not write, such as a Passage's, which is a Knowledge base
plugin's. In a string, every character a terminal acts on is written as a JSON escape: the
control characters, DEL and the C1 controls among them, and the characters that change
the direction text is shown in. The document says the same text, and printed on a
terminal it is only text.

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
| `newer_format` | A file was written by a newer version of `study`, or a Knowledge base plugin speaks a newer version of the contract than this `study` knows; upgrade `study`. |
| `corrupt` | A file Lamplight reads is damaged or missing, for example invalid TOML after a hand edit, or a Topic's git repository is missing, was replaced during a Checkpoint, or leads outside the Topic. Fix or restore it; the message names it. |
| `failed_precondition` | The request is valid but the Topic is not ready for it, for example a git merge is in progress or git has no identity; or the Knowledge base plugin registry is where study does not read it, such as inside the Study home; or a Knowledge base plugin cannot be started, does not start, does not answer in time, does not speak the contract, or failed at what it was asked. The message says what to do. |
| `canceled` | The command was stopped, by SIGTERM or Ctrl-C, before it finished; what it would have recorded was not recorded. A command reports it wherever it was stopped: while it waited for another `study` process to finish writing to the Topic, too. `study check` stops every program the Check started, and a Knowledge base plugin that was started is stopped with everything it started. A few commands do not report it, by design. `study review`, once its session has begun, takes a stop as it takes `q`: the Reviews made so far are kept and it exits with 0 (see [Cards and Reviews](#cards-and-reviews)). `study doctor` and `study setup --check` report a diagnosis whatever happens, so what a stop kept them from examining is a Finding in it, and can make it `unhealthy`. `study mcp` prints no result of its own and exits with 1 when it is stopped, and the session-start hook of the Claude Code plugin never fails. |
| `busy` | Another program is using the Topic: another `study` process writing to it for too long, an editor using its git repository, or files that kept changing while they were being saved. Also another `study` process changing the Knowledge base plugin registry for too long. Try again shortly; the message says what to do if it persists. |
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
Syllabus yet), `adopt` (imported from v1 and not adopted yet: see
[Importing a v1 workspace](#importing-a-v1-workspace)), `learn`, `practice` or `feedback` (the current Lesson's Phase), `reviews`
(every Lesson done, Cards ready) or `explore` (every Lesson done); or, whatever the Resume
point says, `resume_topic` (the Topic is paused: resume it or pick another Topic), and for a
finished Topic `reviews` (Cards ready) or `stop` (no Card ready: nothing to study on it now).
Before the Next step comes `assess`, with `milestone` (`{number, id, title}`): every Lesson
of that Milestone is done or skipped, at least one done, and its end-of-Milestone Assessment
is the next thing to do. It names the Milestone finished most recently, on an active Topic,
and only while no milestone Assessment for it was recorded after its last Lesson was
completed and the learner has not deliberately moved on since by starting a Lesson of
another Milestone (`phase_set`). Stopping does not end it: a Next step recorded with an
Assessment due names the next Lesson's first step (or, when no Lesson follows, taking that
Assessment), the Resume point still shows it word for
word, and `assess` outranks it until the Assessment is recorded; then the Next step leads
again. A completion merged in from another machine for a Lesson already done does not bring
it back. A Topic carries the same Milestone as `assessment_due`. It is a next action, never
something late. The text of `study status` asks a learner without an agent to ask theirs
for it, or to record one with `study assessment record`.
The words match the Focuses and the suggestions of `session_open` wherever they mean the same
thing, and the list is fixed, so skills can rely on it; `text` is English prose a skill may
rephrase. An active Topic's recommendation always agrees with its Resume point. A Triage is
never the recommended action: it is something to consider (see
[Goal, Pace and Forecasts](#goal-pace-and-forecasts)). Each Topic's `cards` is
`{"ready": true}`, `{"ready": false, "next_due": "..."}`, or `{"ready": false, "paused": true}`
while the Topic is paused: whether Reviews are possible now, under the Topic's own daily cap
on new Cards, never how many Cards are due. Under a cap of 0, drafts alone are never ready and
give no `next_due`. `learner_profile` is the Study home's `learner.md` and a
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
  (`learn`, `practice`, `reviews`, `explore`), `plan` (no Syllabus yet: plan it together),
  `stop` (fumes with nothing due: write tomorrow's first step and end here; or a finished
  Topic with no Card ready), `resume_topic` (the Topic is paused), `assess` (at full or
  half Energy, when `status` would recommend `assess`: offer the Milestone's Assessment
  first) or `adopt` (an imported Topic without a Syllabus: adopt it before planning). At
  fumes an Assessment waits for another day, and the suggestion is what it would be without
  one: `reviews` when Cards are ready, otherwise `stop`; `reason` is English
  prose a skill may rephrase. A paused Topic is never suggested for study, and a finished one
  only for its Reviews. A suggestion is never recorded.
- `paused`, when the Topic is paused. The Session opens anyway; the Topic stays paused until
  the learner resumes it (see [Goal, Pace and Forecasts](#goal-pace-and-forecasts)).
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
  `cards.jsonl`, `sources.jsonl`, `tasks.jsonl`) are left out. Listing them writes nothing to `.git` and
  runs nothing the repository names; when git cannot list them, such as during a merge,
  `error` says why and the Session opens anyway.

Once the learner chooses a Focus, record it on the open Session: `session_open` (or `study
session open`) with `session` (the open Session's id) and `focus` records a `session.focused`
Event and opens nothing new. Naming a closed or unknown Session is refused, and the same Focus
again records nothing.

A `phase.set` Event whose Phase is not `teaching`, `practicing` or `feedback`, or whose Lesson
is not a valid id, as a hand edit or another version could write, is held and flagged at
replay; it never reaches the Resume point.

**Stopping saves the work.** `session_close` and `break_point_reached` take a Checkpoint of the
turn they end, as `phase_set` does at a turn switch: the learner's while a Lesson is practicing,
the agent's otherwise. The result has `checkpoint`, or `checkpoint_error` and `checkpoint_role`
when it could not be taken: the stop is recorded anyway, and the Checkpoint stays owed, so
`checkpoint` with that role, or the next write that takes Checkpoints, saves it. A Checkpoint
still owed from earlier, such as a completion's, is taken instead and covers the stop. The
Checkpoint's own `checkpoint.taken` Event names the commit, so it is written after it: right
after a stop, `history.jsonl` is the only changed file, and the next Checkpoint saves it.

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
```

Tasks live in `tasks.jsonl`, one per line, so two machines adding Tasks merge without a
conflict (the file merges by union, like the History):

```json
{"format":1,"id":"book-the-exam.k3f9a2","title":"Book the exam","by":"2026-11-01","after":"core"}
```

`by` is a date shown as is, never counted as late; `after` names a Milestone, and the Task is
shown once that Milestone is done, or with a note if a Revision removed it. The learner may
add lines by hand with any id short of 100 bytes without `#` or control characters; such a
Task can be marked done and removed like the others. A line that is not a Task is named in
`settings_problems` and kept as it is; the other Tasks still work.

Whether a Topic is `active`, `paused` or `finished`, and which Tasks are done, come from its
History, never from a file. A paused Topic offers no Cards and has no Forecasts, and stays
paused until the learner resumes it (`--state active`); `status` recommends `resume_topic`,
and opening a Session on it works and says `"paused": true`. A finished Topic has no
Forecasts and keeps offering its Cards: `status` recommends `reviews` when they are ready and
`stop` otherwise. A Topic made
paused on one machine and finished on another, from the same state, is flagged as a
`conflict`; marking a Task done on one machine and not done on another is not: the last mark
wins.

A setting that cannot be read, such as a hand-edited Pace with negative hours or without
`hours_per_week`, is left out and named in `settings_problems`; the rest of the Topic stands,
and setting it again, or removing it (`--deadline ""`, `--clear-pace`), fixes it. A
`topic.toml` that still holds git's conflict markers after a merge is `corrupt`, with the
fix: keep one side of each block and delete the marker lines.

The **Forecast** says when each Milestone ends at the Pace, never how far behind anything
is. It is in `status` (`forecast`) and in `study syllabus` and `syllabus`:

On 1 Oct 2026, with 20 hours left in Core, of which "Structs" is 6, and a target of 10 Oct:

```json
{
  "pace": "10 h/week, then 3 h/week from 16 Nov 2026",
  "milestones": [
    { "milestone": "core", "title": "Core", "priority": "must",
      "deadline": "2026-10-10", "deadline_from": "target",
      "remaining_hours": 20, "ends": "2026-10-14", "after_deadline": true,
      "text": "At 10 h/week, Core ends 14 Oct 2026; its target is 10 Oct 2026." }
  ],
  "triage": { "milestone": "core", "deadline": "2026-10-10", "deadline_from": "target",
    "ends": "2026-10-14", "raise_pace_to": 14, "move_lessons": ["structs"], "trim_hours": 5.8,
    "suggest_deadline": "2026-10-14",
    "text": "To finish Core by 10 Oct 2026: raise the Pace to about 14 h/week until then, move \"Structs\" past the deadline, or trim about 5.8 h of Stretch goals." }
}
```

(At 10 h/week, 20 h take 14 days, so Core ends 14 Oct. The ten days to 10 Oct fit about 14.3
of the 20 hours, so about 5.8 h do not; 20 h in ten days is 14 h/week.)

- Work is the hour estimates of the Lessons neither done nor skipped, a Lesson in progress
  in full, taken through the Syllabus in order. Any estimate counts as at least a minute. A
  Milestone with a Lesson that has no estimate is not given a date, and nor are the ones
  after it: `unestimated` names the Lessons that need one. Without a Pace, `needs_pace` is
  `true` and no dates are given. `remaining_hours` is rounded to a tenth of an hour.
- Days are calendar days on this computer's clock, starting today, counted as dates so a
  daylight saving change never adds or loses one. Each day adds its Pace period's hours a
  week divided by seven; the count is exact, in minutes. A period of 0 h is shown as a break.
  A Forecast looks ten years ahead at most.
- A Milestone's `deadline` is its target date (`deadline_from: target`), or the Goal's
  deadline for a `must` Milestone without one (`deadline_from: goal`). `after_deadline` is
  set when it is forecast to end later.
- A **Triage** is offered for the first `must` Milestone forecast to end after its
  deadline. It is something to consider, never the one next action, and it offers only what
  can still finish the Milestone in time:
  - the Pace that does (`raise_pace_to`: hours a week from today to the deadline, rounded up
    to a half hour; "set" rather than "raise" when it is not above today's Pace), left out
    above 168 h a week; or, when the deadline is today, the hours to work today
    (`hours_today`, up to 24);
  - the fewest Lessons not started that fit it in time if moved past the deadline
    (`move_lessons`: optional Milestones' Lessons before it first, then its own from the
    last), left out when that is not enough or would move all its own work;
  - the hours of Stretch goals to trim (`trim_hours`), left out when that is more than half
    the Milestone's own work, since Stretch goals are optional extras.

  When none of them can, the text says so and `suggest_deadline` offers the date it is
  forecast to end. Once the deadline has passed, `deadline_passed` asks for a new date first.
  Nothing changes until the learner chooses: a Revision to move Lessons, trim Stretch goals
  or change a target date, or `study topic update` for the Pace or the Goal's deadline.

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

A Lesson's Check is written in the YAML header of `lessons/<lesson-id>.md`. Each criterion
has an id and exactly one of `run`, `rubric` or `held_out` (at most 30 criteria):

```yaml
---
check:
  - id: tests
    describe: The tests pass
    run: [go, test, ./...]              # a command that can be repeated
  - id: names
    rubric: Every function name says what it does   # graded by the agent
  - id: accuracy
    describe: Accuracy on the test set
    held_out: [python3, eval.py]        # evaluated on the Held-out data
---
```

The Check's version is the hash of its criteria, kinds included, so editing a rubric item's
text changes it too. A Check needs at least one run criterion or rubric item: one with only
`held_out` criteria is refused as `corrupt`, since held-out results never decide whether a
Lesson is done.

`study check <lesson>` runs each `run` and `held_out` criterion's command, an argument list
rather than a shell string, in `practice/<lesson-id>/`, run criteria first, with no
standard input and these variables set. Any other `STUDY_` variable in `study`'s own
environment is removed first:

| Variable | Set for | Meaning |
|---|---|---|
| `STUDY_TOPIC`, `STUDY_LESSON` | every command | the Topic's and the Lesson's ids |
| `STUDY_RESULTS` | every command | where the command may write its results file, in a folder of its own outside the practice folder |
| `STUDY_HELDOUT_DIR` | `held_out` commands only | the Lesson's Held-out data, `.heldout/<lesson-id>/` in the Topic |

A Check with only rubric items has nothing to run (`failed_precondition`): grade its items
instead. Progress goes to stderr: each criterion as it starts, and every 15 seconds while a
long one runs; with `--json`, only the latter, so a quick Check keeps stderr empty.

- **Outcomes.** A run criterion passes when its command exits with 0 and its results file,
  if any, does not say `"passed": false`; it fails when the command exits with another
  status or the results say so; and it errors when the command cannot start, is stopped by
  a signal, runs longer than `--timeout` (default 30 minutes), leaves programs running in
  the background, or writes a results file that is not valid. A `held_out` criterion
  follows the same rules but must write a results file with a `score`: without one, or
  without Held-out data, it is errored. **The Attempt's outcome comes from its run criteria
  alone**: held-out results are diagnostic and never change it. A Check without run
  criteria takes the outcome from its held-out results, so its Attempt passes only when
  every one passed; completion still never depends on them.
- **Processes.** Each command runs in a process group of its own. On a timeout, or when
  `study` receives SIGTERM or Ctrl-C, the whole group gets SIGTERM, then SIGKILL after
  two seconds; whatever a command leaves running when it exits is killed too. A Check
  stopped by SIGTERM records nothing and reports `canceled`. A program that leaves the
  group, with `setsid` say, escapes this and can outlive the Check; containing it is the
  sandbox's job (ADR-0009).
- **The work.** The practice folder is snapshotted before the run, after the run criteria
  and after each `held_out` command, writing nothing to `.git`. The snapshot covers every
  file that is not ignored and every ignore rule that applies inside the folder (each
  `.gitignore` on the way and inside it, `.git/info/exclude`, the global excludes file),
  so build outputs can be ignored, but ignoring a file after a pass changes the snapshot.
  If a run criterion changed the work, the Attempt is errored and the changed files are
  named; list files a Check writes in the folder's `.gitignore`. A `held_out` command must
  not write into the work either: if it does, that criterion is errored, its results are
  not kept and its run never counts, and the Attempt stands. The Attempt is also errored,
  without running anything, when the snapshot would have a blind spot: files the index
  marks skip-worktree or assume-unchanged, another git repository in the folder, a
  symbolic link leading outside it, or a folder whose files are all ignored.

**Results files.** A command may write a JSON object of at most 64 KiB to `STUDY_RESULTS`.
Every field is optional, except `score` for a `held_out` criterion; other fields are ignored
and never recorded, and an invalid file makes the criterion errored:

```json
{"format": 1, "passed": true, "score": 17, "max": 20,
 "metrics": {"precision": 0.91, "recall": 0.88}, "summary": "17 of 20 cases"}
```

`format` is 1 (a newer one asks to upgrade `study`); `score` is at least 0 and at most
`max`, which defaults to 1 and must be more than 0; `metrics` holds at most 20 numbers,
each named with 1 to 40 lowercase letters, digits, `_`, `.` and `-`, starting with a letter
or a digit; `summary` is text of at most 1,000 characters for the learner. The file must be
a regular file, not a link or a FIFO; it is opened without following links and read up to
the limit only. A reason that quotes the file, such as a bad metric name or a JSON error,
quotes at most 80 characters of it.

`study check --json` prints the Attempt with the end of each run criterion's output. The
History records the Attempt without any output, which could reveal test data, and a
`held_out` criterion's output is never kept at all. `not_counted` says why a held-out run is
not the counted measurement:

```json
{
  "id": "...", "lesson": "pointers", "check_version": "sha256:...", "snapshot": "sha256:...",
  "outcome": "failed", "at": "...",
  "criteria": [
    { "id": "tests", "kind": "run", "outcome": "failed", "exit_code": 1, "output": "the end of what it printed" },
    { "id": "accuracy", "kind": "held_out", "outcome": "passed", "exit_code": 0, "counted": false,
      "not_counted": "an earlier run is the counted measurement",
      "results": { "score": 0.87, "max": 1, "summary": "87 of 100 cases" } }
  ]
}
```

The exit code is 0 whenever the Attempt was recorded, whatever its outcome. Checks run
only through the command line, from the agent's own shell, so the agent's sandbox applies
(ADR-0009); the MCP server reads Attempts (`check_results`) but never runs a Check.

**Held-out data** lives in `.heldout/<lesson-id>/`, which is committed with the Topic so
every machine measures on the same data: the default `.gitignore` ends with `!/.heldout/`
and `!.heldout/**`, so the folder and its files are committed whatever their format, even
when a line above, such as `.*`, would leave the folder out. It is synthetic or public, never
personal data. Lamplight never shows its contents, the files' names (they are left out of
the changes an unclosed Session lists and of a Checkpoint's `large_files`) or a `held_out`
command's output, only its results.

The **counted** measurement of a `held_out` criterion is its first run that produces
results, on the Check shown to the learner when practicing last started, in an Attempt that
is not errored. Every other run is recorded as not counted, with the reason: the Check was
not shown yet, this version of it is not the one shown, the Attempt errored, or an earlier
run counted. So editing the Check never makes a fresh first run, and an errored run never
uses the count up. A criterion is known by its id, so renaming a `held_out` criterion does
start a new count: agents must never rename or re-create one to get a fresh first run. Two
machines that both measured first are flagged.

**Rubric items** are graded by the agent, after the learner checks their own work against
them, with `study rubric grade` or the MCP `rubric_record` tool: `met`, `partly` or
`not_met`, with an optional note. A grade is for the Check shown to the learner and the
work as it is now, and the practice folder must hold work: at least one file that is not
ignored. **Written work** is files in the practice folder: typed final answers in a text
file, or a photo of paper work. A grade names the files it looked at, relative to the
practice folder or to the Topic; each must be a file the work's snapshot sees, so never an
ignored file or a link, and it is opened inside the practice folder, so nothing leads out
of it. Only their paths and content hashes are recorded.

| Command | What it does |
|---|---|
| `study rubric grade <lesson> <criterion> --grade G [--note N] [--looked-at FILE]... [--topic ID] [--dry-run]` | Grades a rubric item. Without `--topic`, it uses the Topic whose folder it runs in. The same grade, note and files again record nothing. |
| `study results <lesson> [--topic ID]` | Shows the Check, the Attempts, each rubric item's grade (and whether it is for the current Check and work), each `held_out` criterion's counted measurement and latest run, whether the Lesson can be completed, and what to do next. Runs nothing. Without `--topic`, it uses the Active topic. |

With `--json`, each criterion of the Check has its `id`, `kind` (`run`, `rubric` or
`held_out`), `describe`, and either `command` (for `run` and `held_out`) or `rubric`.

**Completion.** A Lesson can be completed when, for the Check shown to the learner when
practicing last started (through `phase_set`), which must still be the current one, every
run criterion passed on an Attempt of the current work, and every rubric item has a grade
for that Check and that work, whose files it looked at are unchanged. Every rubric item
needs a grade, but which grade does not matter: `not_met` counts as graded. Held-out
results never decide it. A Check edited afterwards is flagged in `status` and must be shown
again; changing the work after a pass means running the Check, and grading, again.
`lesson_complete` records the Attempt and the grades it relied on, and a done Lesson is
never reopened. When the completion finishes its Milestone of an active Topic (every
other Lesson of it is done or skipped), the result has `next`, with the Milestone; calling
it again returns the same `next` while that Milestone's Assessment is still due:

```json
"next": {"code": "assess_milestone", "text": "Milestone 1 “Basics” is finished: assess it together with the learner, then record it with assessment_record", "milestone": {"number": 1, "id": "basics", "title": "Basics"}}
```

**After a failed Attempt**, one where a run criterion of the Check shown to the learner
failed, the agent gives feedback, then moves the Lesson to practicing with `phase_set` and
a Next step that names the fix, whatever Phases it goes through on the way: without one,
`phase_set` refuses with `invalid_argument`. Once that Next step is recorded, none is asked
for again until another Attempt fails. `study results` and `check_results` say so in
`next`, with a `code` for programs and a `text` for people:

```json
"next": {"code": "name_the_fix", "text": "give the learner feedback on the failed Attempt, then move the Lesson to practicing with phase_set and a Next step that names the fix"}
```

## Assessments, Level and learning signals

An **Assessment** is a short quiz, kept to its time box (15 minutes unless set): the
placement Assessment when a Topic is created, before its Syllabus, and one at the end of
each Milestone. It is recorded by an `assessment.recorded` Event:

```json
{
  "kind": "placement",
  "items": [
    {"area": "variables", "question": "What does x := 1 do?", "outcome": "correct"},
    {"area": "pointers", "outcome": "incorrect", "note": "confused * and &"},
    {"area": "goroutines", "outcome": "not_reached"}
  ],
  "summary": "Knows variables; pointers need work",
  "minutes": 13,
  "time_box": 15,
  "level": "beginner",
  "notes": "notes/placement.md",
  "request": "placement-go"
}
```

- `kind` is `placement` or `milestone`; a `milestone` Assessment names its `milestone`,
  which must be in the Syllabus.
- Each item has an `area`, an optional `question` and `note`, and an `outcome`: `correct`,
  `partly`, `incorrect`, or `not_reached` when time ran out before it.
- `time_box` is from 1 to 240 minutes, 15 when left out. `minutes`, how long it took, is
  optional: left out it is not known, which is not the same as 0. It is never more than the
  time box.
- The result lists the areas to work on (`weak`: `incorrect` or `partly`) and those to
  `confirm` during Lessons (`not_reached`). Weak results never block anything. After a
  `milestone` Assessment with weak areas, the result carries
  `"next": {"code": "propose_revision", "text": ...}`: the agent proposes a Revision for
  them, such as a review Lesson.
- `notes` names the file in the Topic's `notes/` folder where the agent saved the
  Assessment, with forward slashes: a regular file, not a link; only its path and hash are
  recorded. A name with a backslash is refused.
- `request` is an optional id the client chooses: a retry with the same id records
  nothing, and the same id with a different Assessment is refused. Without one, the same
  Assessment as the latest records nothing, unless the learner changed the Level since; a
  retake after that is the next Assessment, and sets the Level again.
- Replay checks each `assessment.recorded`, `level.set` and `hint.recorded` Event as a
  write would; one that fails, from a hand edit or another version, is held and flagged.

The **Level** (`beginner`, `intermediate`, `advanced` or `expert`) lives in `topic.toml` as
`level`. An Assessment with a `level` sets it; the learner can change it at any time with
`study topic update --level` (`level.set`), and that choice holds until the next Assessment
sets one. Each Topic in `status` carries `level: {level, source, assessment?, at?}`, where
`source` is `assessment`, `learner` or `import` (taken by `study import` from a v1 workspace's
difficulty: v1's estimate, until an Assessment or the learner sets the Level); a Level edited
into `topic.toml` by hand is the learner's, without `at`, and one that is not a Level is
reported in `settings_problems`.

The Level changed on two machines is flagged as a conflict that can be dismissed: "the
Level was changed on two machines", naming both Events. That covers two choices made from
the same version, and also a later Assessment on one machine that kept the Level
`topic.toml` already held there while the learner chose another on the other: after the
merge, `topic.toml` keeps the learner's choice although the History's last writer is the
Assessment. `status` then reports the Level the file holds, with the Event that set it,
never as a hand edit. Choosing a Level with `study topic update --level`, even the one the
file holds, records it and clears the flag; dismissing the flag keeps the file's Level.

The **Approach** is how a Topic's Lessons relate: `concepts` (standalone concepts, each
with its own exercises), `project` (one project built step by step, Lesson by Lesson) or
`challenges` (a run of challenges of growing difficulty). The agent chooses it with the
learner when creating the Topic, and it lives in `topic.toml` as `approach`, set by
`study topic update --approach` (`approach.set`); `status` shows it, and one that is not an
Approach is reported in `settings_problems`.

A **hint** (`hint.recorded`) is help given while the learner practises a Lesson that is not
done or skipped: a `nudge` (a question or a pointer), an `explanation` of a concept again,
or a `step` of the way to a solution. `requested_by` says who asked: the `learner`, or the
`agent`, which offered it unasked. Every call records a hint, except a retry with the same
`--request`, which returns the first hint recorded with it.

**Learning signals** are what a future Level suggestion will use; v2.0 records them and
makes no suggestion yet. `study signals --json` (and the `signals` tool) derives them from
the History. They are for the agent to adapt how it teaches, never shown to the learner as
counts or scores, so `study signals` is hidden from help and answers in JSON only.

| Signal | Derived from |
|---|---|
| `first_try` | The first Attempt measured on the Check shown to the learner passed. An Attempt is measured when it ran run criteria, was not errored, and used the Check shown; an agent's try before showing the Check is not. A Check without run criteria, with only rubric items or `held_out` criteria, has no `first_try`. |
| `attempts` | The Attempts measured. |
| `feedback_rounds` | The times the Lesson went to feedback. |
| `hints` | The hints recorded, by kind; `hints_requested` counts those the learner asked for and `hints_offered` those the agent offered unasked. |
| `held_out` | For each `held_out` criterion, its counted score against the run criteria's mean score in the same Attempt (each a results score over its max, or 1 for passed and 0 for failed), and the `gap`: dev minus Held-out, positive when the work did better on the learner's own tests. |
| `reviews` | Review ratings: per Lesson, and for the Topic with Explore Cards. |

`totals` sums them: Lessons passed on the first try out of those measured, feedback rounds,
hints and the hints the learner asked for. `assessments` lists the Topic's Assessments,
oldest first.

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

## Importing a v1 workspace

`study import <v1-workspace>` turns a workspace of the v1 study skill (`.study-config.json`,
`lessons/`, `practice/`, `notes/`, `.fsrs/`, in a repository) into a new Topic. Run it with
`--dry-run` first: the report lists the size of the copy, everything that will be converted
and moved, the Lessons proven done with their proof, those left open, and everything left
out, and nothing is written. The workspace itself is only read, never changed, and its
history is read through the same hardened calls as Checkpoints, which run no program its
configuration names.

The import copies the workspace, `.git` included, so every commit is kept, and copies every
file, git-ignored or not, since ignored files can be the learner's data. The Topic's id is
the workspace folder's name unless `--topic` gives another, and v1's names become ids:
Lesson 1 is `lesson-01`, as v1 named its practice folder, so paths quoted in Lesson text and
in `.gitignore` keep working. The workspace is recognised by its real path, links resolved.

| v1 | v2 |
|---|---|
| `topic`, `end_goal` | the Topic's title and Goal |
| `difficulty_override`, else `difficulty` | the Level, with `source: import` |
| `approach` (`concept`, `project`, `challenge`) | the Approach (`concepts`, `project`, `challenges`) |
| `lessons[]`, numbered | Lessons `lesson-NN`; a file under `lessons/` moves to `lessons/lesson-NN.md` |
| a Lesson `completed` with proof | done, through the import's Event, with no Attempt |
| `lessons/plan.md` (v1's project approach only) | `notes/v1-plan.md`, for the adoption Session |
| `.study-config.json` | `topic.toml`; the original is kept as `notes/v1-config.json` |
| `sources[]` (paths, or objects with `path`, `url`, `title`) | Sources: a file inside the workspace by its path there, one outside by its content, URLs as they are |
| `notebooklm`, and a source's NotebookLM keys (`notebook_id`, `source_id` and their other spellings) | nothing: Lamplight v2 does not use NotebookLM ([ADR-0011](adr/0011-lamplight-does-not-use-notebooklm.md)). Each is listed under `dropped`, the Source itself is imported, and the import chooses no Knowledge base |
| `session_state` (`pending_action`, `context`, `phase`) | `v1_next_step`, for the adoption Session's Next step |
| `syllabus.toml`, `cards.jsonl`, `sources.jsonl`, `tasks.jsonl` at the top | `notes/v1-<name>`, so they are not taken for Lamplight's own |
| `.gitignore`, `.gitattributes` | merged: the learner's lines first, Lamplight's last, so they win |

**Moves** are planned before anything is copied, and the dry run and the import share the
plan. A Lesson's file moves only if it is a regular file under `lessons/`, where v1 writes
them; anything else stays where it is and is not the Lesson's file (nothing under `.git`
ever moves). A file two Lessons share moves with the first. A move whose target exists, or
whose folder is a file, does not happen; the file stays where it is, with a note.

**Proof.** A Lesson v1 calls `completed` (or `complete`, `done`) counts as done only when its
Lesson file is under `lessons/` and v1's own records show it. v1's Lesson Completion Contract
(`references/workspace-lifecycle.md` in v1) adds an FSRS card with id `lesson-NN`, then
commits `[agent] complete lesson NN`. A commit proves a Lesson only when its whole subject is
that (`completed` is accepted, and `lesson 1` never matches `lesson 10` or `lesson 4.5`), and
no newer `Revert "..."` of it exists; otherwise the card does. The report shows each proof,
the commit's hash and subject or the card, and a Lesson without one stays open, saying why.
`--not-done lesson-NN`, repeatable, keeps a Lesson open whatever its proof: check the dry run
first, since a Topic imported already must be removed (`study topic remove <topic>`) before
importing it again.

**The git files.** In a `.gitignore` and a `.gitattributes`, the last matching line wins, so
the learner's lines come first and Lamplight's last. The `.gitignore` gets Lamplight's
default lines it lacks, then `!/topic.toml`, `!/history.jsonl`, `!/syllabus.toml`,
`!/cards.jsonl`, `!/sources.jsonl`, `!/tasks.jsonl`, `!/.gitattributes` and `!/.gitignore`,
so a v1 line such as `*.jsonl` cannot leave the state out of Checkpoints. The
`.gitattributes` ends with Lamplight's union merges, and resets `filter`, `text`, `eol`,
`ident` and `working-tree-encoding` on the state files, so a v1 line such as
`*.jsonl filter=lfs merge=lfs -text` leaves them alone. The `topic.imported` Event records
the merged `.gitattributes`.

**Left out of the copy**, each listed under `dropped` with its reason: links leading outside
the workspace, directly or through another link (links inside it are copied as links);
entries that are neither files, folders nor links (FIFOs, sockets); v1's lesson-level cards
(`.fsrs/`, kept in the history); and folders the language's tools rebuild, unless the
history tracks them: `node_modules`, `.venv`, `venv` and any folder holding `pyvenv.cfg`,
`__pycache__`, `.pytest_cache`, `.mypy_cache`, `.ruff_cache`, `.tox`, and `target` next to a
`Cargo.toml` or `pom.xml`.

**Dropped from the config**, each with its reason: templates, the mode, calibration rounds
(`next_calibration_at_lesson`, `difficulty_override_at_lesson`), progress counters, the
review queue, the catalog path, companions' settings, the NotebookLM notebook and each
imported source's ids in it, Energy and time budget, v1's creation date, each Lesson's
`metrics`, keys the importer does not know (they stay in `notes/v1-config.json`), and
Lesson entries it cannot read. Lessons are read one at a time: a `num` written as `"1"` or
`1.0` is read as 1, and an entry without a whole number, or that is not an object, is
dropped alone.

**Refused:** a folder without `.study-config.json`; one inside the Study home, or holding it;
one that is a Lamplight Topic already; a config version newer than 3, or more than 500
Lessons or 200 sources; a config that is not a regular file or is over 1 MiB; a linked
worktree or submodule (`.git` is a file); a repository that borrows objects
(`.git/objects/info/alternates`, `failed_precondition`; run `git repack -a -d` in it, then
delete that file); one where git is writing (`index.lock`, `HEAD.lock`, `packed-refs.lock`
or a lock under `refs/`: `busy`, naming the file to delete if no git command is running); a
`.gitignore` or `.gitattributes` that is a folder; a file named like Lamplight's whose
`notes/v1-<name>` is taken; a `--not-done` Lesson the config does not list; an id that is
taken (`already_exists`); and a workspace imported already with the same config and HEAD
(`already_exists`, naming the Topic). An import stopped by SIGTERM or Ctrl-C reports
`canceled`, and leaves nothing.

Imports run one at a time, under a lock in `.lamplight/locks/`, so two imports of the same
workspace cannot both succeed. The Topic is assembled under `.lamplight/tmp` and moved into
place only when complete, so an interrupted import leaves no Topic, and importing again
starts over; each import first removes staging folders over an hour old whose Topic no
process holds the lock of. It records `topic.created`, then the settings (`level.set` with
`source: import`, `approach.set`), one `source.added` per Source, and
one `topic.imported` Event, which writes the `.gitattributes` and whose payload is the
report: where it came from (`from`, `head`, `config_version`, `config_hash`), where the plan
and the config now are (`plan`, `config`), the Lessons kept open with `--not-done`
(`not_done`), what was converted, moved and dropped, the Lessons proven done with their
proof, the open ones, and where v1 stopped. A Checkpoint then saves the import; without an
identity for commits the result carries `checkpoint_error`, and the next Checkpoint saves it.

An imported Topic shows `imported` in `status`: `from`, `at`, `adopted`, `plan` (absent when
v1 wrote none), `config`, `v1_next_step`, and the report the adoption works from:
`completed` with their proofs, `open` and `dropped`, each cut to its first 100 entries, with
`more` counting what was cut (the `topic.imported` Event holds everything). It carries the
recommendation `adopt` until it has a Syllabus. The adoption, with the agent, is a
checklist: the Goal and deadline, the Pace, the Syllabus from `notes/v1-plan.md` (v1's three
tiers become the priorities `must`, `if_time` and `after_deadline`), or, without a plan,
from v1's lesson list and the learner's notes, approved as a Revision that keeps every
Lesson proven done, Checks for the open Lessons, the Sources, Cards for the completed
Lessons, and a Next step from where v1 stopped.

## Writes

Commands that write accept `--dry-run`, which validates the request and reports what would
change without writing anything.

Every write to a Topic takes the Topic's lock, so the CLI and the MCP server can run at
the same time. Once it holds the lock, it checks that the Topic's folder is still the one it
opened; if the Topic was removed or replaced meanwhile, it fails with `not_found` and
writes nothing. `study check` and `study checkpoint` open the folder once, before the Check
runs or the commit is made, so a Topic removed or replaced while its Check ran gets no
Attempt, and a Checkpoint never commits in a folder that another program put in the
Topic's place. A write records its Event in the History before it changes any content, so one
interrupted by a crash is finished by the next write or Checkpoint. A dry run reports
what the real run would do after finishing such a write, and writes nothing. Lock and
intent-marker files live in the Study home's `.lamplight/` folder, which holds this
computer's local state (removed Topics too). Lamplight never syncs it; if a file-sync tool
syncs the Study home, exclude `.lamplight/` from it. Topics sync through git.

`study topic remove` and `study topic restore` are the writes that record no Event: they
move a Topic's folder on one computer and change nothing inside it, so there is nothing for
the Topic's History, which every computer shares, to say. A restore and an import under the
same id are kept apart by the lock of that id, which `study import` holds from before it
copies the workspace until its Topic is in place. Creating a Topic takes no lock; it and a
restore of the same id cannot both succeed, because each moves a complete folder into
place, and the move is refused when a folder that holds anything is there.

`study knowledge-base add` and `study knowledge-base remove` record no Event either, and
write nothing in the Study home: the registry they change is this computer's and no
Topic's (see [Knowledge base plugins](#knowledge-base-plugins)). Nor does
`study knowledge-base check`, which writes nothing in the Study home at all: starting a
plugin that is a command makes its folder, `lamplight/knowledge-base-plugins/<name>` in the
learner's state folder, when it is not there, and that is this computer's state too.

`study source index` records no Event and writes nothing in the Topic: whether a Source is
indexed is the plugin's to say on this computer, and its index is in that same folder (see
[study source index](#study-source-index)). Naming a Topic's plugin is a write like any
other, with an Event of its own type, `knowledge_base_plugin.set`.

`study evidence search` records no Event and writes nothing either. Evidence recorded with
the Passage it quotes is a write like any other, with an Event of its own type,
`evidence_passages.recorded`; the plugin is asked for the Passage before the Topic's lock
is taken, so no write waits for a plugin (see
[Evidence checked against its Passage](#evidence-checked-against-its-passage)).

## Sources and Evidence

The Knowledge seam ([ADR-0007](adr/0007-knowledge-bases-return-evidence.md)) records where
a Topic's material comes from. `study` never parses a document. Without a Knowledge base the
agent reads the Sources itself and records the quotes it relied on; a Topic can instead name
a Knowledge base plugin, which indexes its Sources and searches them, and against whose
Passages a quote is checked
([ADR-0012](adr/0012-knowledge-base-plugins-speak-one-contract.md)).

- **Knowledge base**: `none`, or a Knowledge base plugin, which the Topic names. It is
  stored in the `[knowledge_base]` table of `topic.toml` and shown on the Topic in `status`.

  ```toml
  [knowledge_base]
    kind = "plugin"
    plugin = "shelf"
  ```

  - `study topic update <topic> --knowledge-base <name>` names a plugin, and
    `--knowledge-base none` goes back to none. A name follows the rule of the registry:
    lowercase letters, digits and single hyphens, up to 64 characters, and neither `none`
    nor `plugin`. Anything else is `invalid_argument`: a Topic holds a name and never a
    command line or an address, so nothing in a Topic, which syncs between computers and
    which an agent can write, decides which program `study` starts. What the name stands
    for is each computer's to say, in its registry (see
    [Knowledge base plugins](#knowledge-base-plugins)).
  - **A name this computer does not have is recorded all the same**: another of your
    computers may have the plugin. The result then carries a `note` that says so, with the
    command that registers it, and the Topic works here as one without a Knowledge base.
  - A name is recorded by a `knowledge_base_plugin.set` Event, whose payload is the name
    (`{"plugin": "shelf"}`), and `none` by a `knowledge_base.set` Event, whose payload is the
    kind (`{"kind": "none"}`), as it always was. Naming the plugin a Topic already names, or
    choosing `none` for a Topic that has it, writes nothing, so the table is left as it is,
    with any keys this version does not know. Any other choice writes the table anew, with
    the kind, and the name when there is one, and nothing else.
  - A Topic without a Knowledge base behaves as `none`. So does a Topic whose `topic.toml`
    holds a kind this version does not know, one a newer version added or one an earlier
    build of v2 had: the kind is shown as recorded, human output says it is treated as
    `none`, and `--knowledge-base none` replaces it. `topic.toml` can be edited by hand, so
    a plugin's name is whatever is written there: one no plugin can have is shown made safe
    to print, at most 64 characters of it, and names no plugin.
- **Sources**: the History records which Sources exist and what they are;
  `sources.jsonl` is the readable copy Lamplight writes, one line per Source. Edit Sources
  with `study source`, not by hand: a line added by hand is listed as `untracked` and is not
  a Source until `study source add` records it. A Source's id is a slug of its title plus a
  random suffix, such as `strang-linear-algebra.k3f9a2`. Each line holds only what is the
  same on every computer:

  ```json
  {"id":"strang-linear-algebra.k3f9a2","kind":"file","title":"Linear Algebra","file_name":"strang.pdf",
   "hash":"sha256:…","size_bytes":15}
  {"id":"notes-paper.p2x7q1","kind":"file","title":"Paper","topic_path":"notes/paper.pdf","hash":"sha256:…","size_bytes":9}
  {"id":"go-dev-blog-context.m4k8s3","kind":"url","title":"Go Concurrency Patterns: Context","url":"https://go.dev/blog/context"}
  ```

  A file inside the Topic is kept by its `topic_path`, so every clone has it; a file
  outside, by its original `file_name`, `hash` and `size_bytes`. Fields in a line that this
  version does not know, written by a newer version or by an earlier build, are kept as
  they are and not shown. URLs are normalised: the scheme and host are lowercased, a default
  port is dropped, an international host stays readable (Unicode, NFC), and addresses with
  a user name or password are refused.
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
  `learner`, `estimate`, or `knowledge_base` (the location of the Passage the quote is
  from, as the Knowledge base plugin gave it). The last is what Evidence recorded with its
  Passage gets, and nobody gives it: `--location-from knowledge_base` without a `--passage`
  is `invalid_argument`, since a location that says it is a Passage's comes with the
  Passage that was checked. Evidence lives in the History only, and Evidence
  whose Source has not arrived from another machine yet is held until it does. Retracted
  Evidence no longer counts for its Lesson. Lessons without Evidence are marked, never
  blocked.

  ```json
  { "id": "k3f9a2b7qd", "lesson": "elimination", "source": "strang-linear-algebra.k3f9a2",
    "quote": "Elimination produces an upper triangular system.", "location": "p. 46",
    "location_from": "source", "checked": false, "recorded": "2026-10-01T09:30:00Z" }
  ```

  `checked` says whether the quote was checked when it was recorded. Evidence recorded
  with the Passage it quotes is checked against that Passage's text, and carries the
  Passages and the plugin that gave them; Evidence recorded without one is not checked
  (see [Evidence checked against its Passage](#evidence-checked-against-its-passage)).
- **Two machines**: Sources and Evidence added on two machines merge: each Source is its own
  item in the History, and `history.jsonl` and `sources.jsonl` both merge by union in git.
  One Source edited on both machines is flagged as a `conflict`, and the union merge may
  leave two lines for it in `sources.jsonl`; the History's version is used, and the next
  change to the Source leaves one line. Under the one-machine-at-a-time contract, adding the
  same file or URL, or the same Evidence, on both machines before syncing gives two of them;
  that is not flagged.

### What is indexed

For a Topic that names a Knowledge base plugin, `study status` and `study source list`, and
the `status` and `sources` tools, ask the plugin which of the Topic's Sources it has
indexed. So does the session-start hook of the Claude Code plugin, which prints
`study status`. Whether a Source is indexed is the plugin's to say, on this computer: it is
no Event, it is in no file of the Topic, and it is asked each time.

`status` asks about the Topics that are active, and gives a Topic that is paused or
finished no `indexing`: such a Topic has nothing to do next. `study source list` asks about
the Topic it lists, whatever its state.

`source list` gives each Source an `index`, and the list an `indexing`:

```json
{
  "topic": "linear-algebra",
  "knowledge_base": {"kind": "plugin", "plugin": "shelf"},
  "indexing": {"plugin": "shelf", "action": "study source index linear-algebra"},
  "sources": [
    {"id": "strang-linear-algebra.k3f9a2", "kind": "file", "title": "Linear Algebra", "path": "/home/ada/Books/strang.pdf",
     "state": "ok", "index": {"state": "indexed"}},
    {"id": "axler-linear-algebra-done-right.p2x7q1", "kind": "file", "title": "Linear Algebra Done Right",
     "path": "/home/ada/Books/axler.pdf", "state": "ok", "index": {"state": "indexing", "progress": 0.4}},
    {"id": "scanned-notes.t8w3n5", "kind": "file", "title": "Notes", "path": "/home/ada/Books/notes.pdf", "state": "ok",
     "index": {"state": "failed", "reason": "the file has no text layer"}},
    {"id": "go-dev-blog-context.m4k8s3", "kind": "url", "title": "Go Concurrency Patterns: Context",
     "url": "https://go.dev/blog/context", "index": {"state": "not_indexed"}}
  ]
}
```

`status` gives each such Topic the same `indexing`, with the Sources' states in it, since
`status` lists no Sources otherwise:
`"indexing": {"plugin", "problem"?, "action"?, "sources"?: [{"id", "title", "state", "progress"?, "reason"?, "retry"?}]}`.
`sources` is left out for a Topic without Sources.

| `state` | Means |
|---|---|
| `indexed` | The plugin holds the Source's content, and a search finds it. For a file, the hash of what the plugin holds is the Source's. |
| `outdated` | The plugin holds the Source's content, and a search finds it, as with `indexed`. But the plugin would index it otherwise now, because its own settings changed since: `reason` says what indexing it again would add, in the plugin's words, such as Passages that have no vectors of the embedding model that was set since. `study source index` asks for it again, without `--again`, and it is that command's to do next. |
| `indexing` | The plugin is indexing it now; `progress` is how much is done, from 0 to 1, when the plugin can tell. A service says so to whoever asks while it works. A plugin that is a command indexes only while the `study source index` that started it runs. Another `study` starts a copy of its own meanwhile, which says `indexing` when it can tell that the work is going on, and otherwise shows the Source as it was before. |
| `failed` | The plugin's last indexing of it failed; `reason` is why, in the plugin's words: a file without a text layer, a page that cannot be fetched, a file that was another when the plugin read it than when `study` did. It stays so, on every run of `study`, until the plugin is asked to index the Source again. |
| `not_indexed` | The plugin does not hold the Source's content. `reason` is there when the plugin does not index Sources of this kind; when the Source's file is not on this computer, is no longer the one added or cannot be read, so that there is nothing to hand the plugin; and when a plugin that breaks the contract holds other content for the Source than the Topic records. |
| `unknown` | The plugin could not say: `indexing.problem` says why. |

- **`indexing.action` is the one command to run next.** It is
  `study source index <topic>` when a Source is `not_indexed`, or `outdated`, and the
  command would hand it to the plugin: the plugin indexes Sources of its kind, and a file is on this computer,
  is the one that was added, and can be read. It is the command that puts a `problem`
  right. It is absent when there is nothing to do, and for a folder whose name is no Topic
  id, which no command can name. Human output names it under the Sources, and `status`
  under "Knowledge base plugins", with the Sources that are not indexed, by title. An
  action that puts a `problem` right can hold a part in angle brackets, `<command>` or
  `<name>`, which is for the learner to fill in: such a command is theirs to run.
- **A file that is missing, is another now, or cannot be read is not one to index.**
  `study source index` skips it, so it is not the action for it: the Source is
  `not_indexed` with the `reason`, and in `status` the action is then
  `study source list <topic>`, which says what to do about each such file. In
  `study source list` itself there is no action for it beyond what the Source's own lines
  say. The same holds for a Source that `failed` and whose file is such a one, and for one
  that is `outdated`: it keeps the plugin's `reason`, stays found as it is, and is brought
  up to date once its file is back.
- **A Source that `failed` is shown, and is no next action.** The plugin tried, and said
  why: trying again helps only once what the `reason` names is put right, and a scan
  without text would otherwise be the one thing to do for ever. `status` names it with its
  reason. Its `retry` is the command that has it tried again,
  `study source index <topic> <source>`, which `study source list` prints under it; it is
  left out when trying again would come to nothing as things are, because the Source's
  file is missing, has changed or cannot be read. Whoever runs
  `study source index <topic>` has every failed Source tried again with the rest.
- **Nothing waits for a plugin.** Reading the registry, starting the plugin or reaching
  it, and its answers have **2 seconds, all of it together**, against the 30 seconds a
  plugin has to start when it is asked to index. A plugin that takes longer is stopped,
  and its Sources are `unknown`. Stopping a command that does not exit when its input is
  closed takes half a second more at most: after a quarter of a second its process group
  gets `SIGTERM`, and after another it is killed. A service that does not answer when it
  is told the session is over is waited for three quarters of a second, and no longer.
  A plugin that missed the 2 seconds is given nothing more: a command is killed at once,
  and a service is not waited for. Several plugins are asked side by side,
  each once, however many Topics name it. A Topic without Sources has nothing to ask, so
  its plugin is not started. **Opening a Session asks no plugin anything**, and neither
  does a command that only has to know which Topic it is about, such as `study check`,
  `study review` and `study syllabus`.
- **The Topics that name one plugin share its 2 seconds**, and are asked one after the
  other: the Active topic first, then the others in the order `status` lists them. A plugin
  that answered about one Topic works, so a Topic the time did not reach says that, and its
  action is `study source list <topic>`, which asks about that Topic alone with all of the
  time.
- **For this a plugin is asked what it has indexed, and nothing else**: its `status` tool,
  once it said which contract it speaks. It is started for that and stopped again before
  the command, or the tool, answers: nothing is kept running in between, in `study mcp`
  either. A search, and Evidence recorded with its Passage, ask it more, and have longer
  (see [Searching a Topic's Sources](#searching-a-topics-sources)).
- **`indexing.problem`** says why the plugin could not say, and every Source is then
  `unknown`. The Topic works as one without a Knowledge base meanwhile: Sessions open,
  teaching goes on, and Evidence is recorded from the Sources as the agent reads them.

  | The problem | `action` |
  |---|---|
  | `topic.toml` records the kind `plugin` without a name a plugin can have. | `study topic update <topic> --knowledge-base <name>` |
  | No plugin is registered under the name on this computer. | `study knowledge-base add <name> -- <command>` |
  | The registry cannot be read, or the plugin is registered and `study` does not use it, because its program is inside the Study home. | `study knowledge-base list` |
  | The plugin cannot be started, exits, does not answer within the 2 seconds, is no MCP server, does not speak the contract, or speaks a newer one. | `study knowledge-base check <name>` |

  `study knowledge-base check` gives the plugin the 30 seconds, so a plugin that is only
  slow to start passes it, and `study source index`, which waits as long, then says what
  is indexed.
- **What a plugin wrote is cut short here**: a `reason` to 300 bytes, a `problem` to 600,
  the `…` that says so included, each on one line and with nothing a terminal would
  interpret. `study source index` gives a `reason` whole, up to the 2,000 bytes `study`
  keeps of one.
- **The collection**: a plugin keeps collections apart and knows nothing else of Topics.
  The name `study` gives it for a Topic is the Topic's id, a hyphen, and 32 characters of a
  hash of where the Study home really is and of the Topic's id, such as
  `linear-algebra-mfrggzdfmztwq2lknnwg23tpobyxe43u`. An id longer than 31 characters is cut
  to fit the 64 a name may have, and a folder whose name is no Topic id is called `topic`
  there. A plugin's folder is one for the plugin on a computer, and a service may serve
  several, so the Topic's id alone would have two Study homes that each hold a
  `linear-algebra` share one collection. The path is not the agent's to choose, and every
  name ends with the hash, so no two Topics of one Study home, or of two, share a name.
  **Moving the Study home, or renaming it, changes the name: the Sources are then indexed
  again**, and what the plugin holds under the old name stays where it is, unused. The
  same goes for a Study home that `study` sees under another path, as inside a sandbox that
  shows it elsewhere: what is indexed there is not what `study` asks about outside it. A
  Topic that is removed, and another made later under its id, share a name: nothing
  removes a collection, so the plugin still holds the removed Topic's Sources there, under
  their own ids.

### study source index

`study source index <topic>` asks the Topic's Knowledge base plugin to index the Topic's
Sources that are not indexed. It is **the one thing that asks a plugin to index**. Adding a
Source indexes nothing, with `study source add` or the `source_add` tool, and no MCP tool
indexes: a plugin that indexes reads the files it is handed and fetches the addresses, and
`study mcp` runs outside the agent's sandbox, so a tool that indexed would have a plugin
read any file the agent named as a Source and hand its text back through a search. An
agent runs this command in its own shell, where its sandbox and approval prompts apply, as
it runs `study check`; the learner can always run it themselves.

```json
{
  "topic": "linear-algebra",
  "plugin": "shelf",
  "collection": "linear-algebra-mfrggzdfmztwq2lknnwg23tpobyxe43u",
  "sources": [
    {"id": "strang-linear-algebra.k3f9a2", "title": "Linear Algebra", "path": "/home/ada/Books/strang.pdf", "state": "indexed"},
    {"id": "axler-linear-algebra-done-right.p2x7q1", "title": "Linear Algebra Done Right", "path": "/home/ada/Books/axler.pdf",
     "state": "indexed", "asked": true},
    {"id": "scanned-notes.t8w3n5", "title": "Notes", "path": "/home/ada/Books/notes.pdf", "state": "failed",
     "reason": "the file has no text layer", "asked": true},
    {"id": "lecture-notes.b6c2d9", "title": "Lecture notes", "state": "not_indexed",
     "skipped": "its file is not on this computer: say where it is with study source update linear-algebra lecture-notes.b6c2d9 --path <file>"}
  ]
}
```

- **What a plugin is handed is shown**: each Source comes with the real `path` of its file
  on this computer, or its `url`, and human output prints it under the title. A plugin reads
  the files it is handed, with your permissions when you run the command yourself, and an
  agent can add any file it names as a Source: `--dry-run` shows which files those are
  before anything reads them.

- **Which Sources**: all of the Topic's, or the ones named after the Topic, in the order
  they were added. A Source that is `indexed` is left alone. Every other is asked for, one
  after the other: `asked` says so. One that is `indexing` already, in a service, is
  followed with the rest.
- **A file** is handed to the plugin by its path on this computer and its content hash.
  The path is the file's real one, with every symbolic link followed: a Source can be a
  link, a plugin reads what it is handed a moment later, and handed the link it would read
  wherever the link leads by then.
  **A Source is its content**: only the file the Topic records is handed over, and `study`
  reads it once more, whole, just before it asks for it, to be sure. A file that is not on
  this computer, cannot be read, or has other content now than was added is not asked for:
  `skipped` says why and what to do, and what the plugin indexed for it before stays as it
  is. The plugin reads the file itself a moment later, and **keeps nothing of a file whose
  bytes are not the hash it was handed**: the Source is then `failed`, with the plugin's
  reason. That is what keeps a file inside the Study home, which the agent can exchange for
  a link to any file of yours once `study` has read it, from being indexed as something
  else than was shown.
- **A web page** is handed over by its address. The plugin fetches it, from where the
  plugin runs.
- **A kind the plugin does not index**, a web page for a plugin that indexes files, is
  `skipped`, and is no reason for `indexing.action` to name this command.
- `--again` asks for the Sources that are indexed too. A web page is then fetched again.
  A file whose content is indexed is left as it is by the plugin, as the contract says, so
  for a file it asks and changes nothing.
- `--dry-run` starts the plugin, asks what it has indexed, and reports each Source as it is
  now, with `asked` for the ones the run would ask for and `skipped` for the ones it would
  not. It asks the plugin to index nothing. A command plugin's folder is made when it is
  not there, as `study knowledge-base check` makes it, and is checked as below.
- **Progress** goes to stderr: each Source as the plugin is asked for it, and every 15
  seconds while one is indexed, with how far the plugin says it is. With `--json` only
  the second kind is written, so a quick run keeps stderr empty. `study` asks the plugin
  how far it is often at first and then once a second. **Indexing a Source has no time
  limit**: a book takes as long as it takes, and each of the plugin's answers has the 30
  seconds every call has.
- **What goes wrong with a Source is not an error of the command.** A file without a text
  layer, a page that cannot be fetched, a Source the plugin refuses: the Source is
  `failed`, with the plugin's `reason`, the others are indexed, and the exit code is 0.
  Look at each Source's `state`. A plugin that does not answer, or answers with what is
  not the contract's, is another matter: it would do the same for the next Source, so the
  command ends there, with `failed_precondition`.
- **Stopped**, by Ctrl-C, `SIGTERM` or `SIGHUP`, it tells the plugin to stop indexing the
  Source it was indexing, with the plugin's `cancel` tool, and waits up to 10 seconds for
  the plugin to say that it has: so no work goes on behind a command that has exited. It
  says so on stderr at once (`Stopping: telling the plugin to stop indexing …`), since
  nothing else is printed until the plugin has answered, and a second Ctrl-C does not
  hurry it. A plugin that is a command is stopped then, and its indexing with it, whether
  or not it answered. A service is told, and when it cannot be told, or had not yet
  answered the request to index, the message says that it may still be indexing. The
  command reports `canceled`, and its message what came of the stop. **What is indexed
  stays indexed, and running the command again carries on** with what is not. A `study`
  that is killed (`SIGKILL`), or ends on `SIGQUIT` or on a closed terminal (`SIGPIPE`),
  stops nothing: a plugin exits by itself when its input closes, as the contract asks, and
  one that does not is left running.
- **Inside a sandbox.** A command plugin keeps its index in
  `lamplight/knowledge-base-plugins/<name>` in your state folder (`$XDG_STATE_HOME`, or
  `~/.local/state`), which is outside the Study home on purpose (see
  [study knowledge-base check](#study-knowledge-base-check)). An agent's sandbox that lets
  `study` write in the Study home alone refuses it. `study` makes sure of the folder before
  it starts the plugin, by making and removing an empty file in it, and fails with
  `failed_precondition` when the folder cannot be made or written in. The message gives
  both ways out: **allow that folder in the sandbox, or run the command in a shell of
  your own.** Listing Sources and `status` need no such folder to be written in, and a
  plugin reached by URL has none.
- **It records nothing in the Topic**: no Event, and no file. What is indexed is this
  computer's.

| It ends with | When |
|---|---|
| exit 0 | The plugin was asked for every Source there was to ask for, and each was followed to its end, whatever that was: `indexed`, `failed` or `not_indexed`. Also when there was nothing to ask for, and for a dry run. |
| `failed_precondition`, exit 1 | The Topic names no plugin; the plugin is not registered on this computer; it cannot be started, is no MCP server or does not speak the contract; its folder cannot be made or written in; or it stopped answering while it indexed. The message says which, and what to do. |
| `newer_format`, exit 1 | The plugin speaks a newer version of the contract: upgrade `study`. |
| `not_found`, exit 1 | There is no such Topic, or the Topic has no such Source. |
| `canceled`, exit 1 | The command was stopped. |
| `invalid_argument`, exit 2 | What was given as the Topic is not the id of one, or what was given as a Source is not the id of one. |

### Searching a Topic's Sources

`study evidence search <topic> <query>`, and the `evidence_search` tool, ask the Topic's
Knowledge base plugin for the Passages of the Topic's Sources that answer a query, best
first. A Passage is a stretch of a Source's own text, as the plugin extracted it: never a
summary, and never a generated answer. Evidence is quoted from it.

```json
{
  "topic": "linear-algebra",
  "query": "why does elimination produce a triangular system?",
  "plugin": "shelf",
  "passages": [
    {"id": "7f3a9c12e5b04d68", "source": "strang-linear-algebra.k3f9a2", "title": "Linear Algebra",
     "text": "Elimination produces an upper triangular system.", "location": "p. 46", "score": 0.83},
    {"id": "2b9d4e71a0c3f586", "source": "strang-linear-algebra.k3f9a2", "title": "Linear Algebra",
     "text": "The pivots are on the diagonal of the triangle after elimination.", "location": "pp. 46–47", "score": 0.41}
  ],
  "searched": ["strang-linear-algebra.k3f9a2"],
  "left_out": [{"id": "axler-linear-algebra-done-right.p2x7q1", "title": "Linear Algebra Done Right", "state": "not_indexed"}],
  "note": "1 of the 2 Sources was not searched: Knowledge base plugin shelf does not hold it indexed on this computer as the Topic records it",
  "action": "study source index linear-algebra"
}
```

- **Only what the Topic records is returned.** A search is limited to the Sources the Topic
  has, by their ids: a Topic that was removed and another made later under its id share a
  collection, and the removed Topic's Sources are still in it. And a Source is searched
  only when the plugin says, by the `hash` in its `status`, that what a search finds of it
  is the Source's own content: a plugin that breaks the contract may hold another file's
  text under the Source's id, and says so through that hash. That is every Source the
  plugin says is `indexed`, and also one that is being indexed anew, or whose last
  indexing failed, while its earlier content is still indexed: the contract has a plugin
  give that content's hash, and find it. So the plugin is asked what it holds first
  (`status`), and then to search the Sources that passed (`search`, with their ids).
  `searched` lists them. A plugin that answers with a Passage of a Source it was not asked
  for breaks the contract: the whole answer is refused, with `failed_precondition`.
- **What was left out is said, as `study source list` says it**: `left_out` lists each
  Source that was not searched, with its `state` (`not_indexed`, `indexing` or `failed`),
  the `reason` when there is one, and for a Source that failed the command that has it
  tried again, in `retry`, when trying again can help. `note` says it in a sentence. And
  `action` is the one command to run next, by the rule `status` follows: `study source
  index <topic>` when that would hand the plugin a Source it has not tried; `study source
  list <topic>` when a Source's file is missing, changed or unreadable, which that listing
  explains; and nothing when all that is left out failed, since indexing it again as it
  is would fail again. A Source that is being indexed is found once that is done.
- **Nothing to search is no error, for a Topic that names a plugin.** One without Sources,
  and one of whose Sources none is indexed, give no Passages and a `note`, with exit code
  0. The plugin is then not asked to search, and a Topic without Sources starts no plugin
  at all. Nor is it an error that nothing answers the query: `passages` is empty, and
  `searched` says what was looked in. A Topic that names no plugin is
  `failed_precondition` whatever Sources it has: that is looked at first.
- **`--limit`** is the most Passages to return: 5 unless given, or given as 0, and 50 at
  most, which a larger number is cut to. **`--source`** limits the search to that Source,
  and can be repeated; one the Topic does not have is `not_found`.
- **An answer holds at most 256 KiB of Passages' text**: the best Passages, as many as
  fit. A plugin may give fifty Passages of 32 KiB each, and an answer of that size helps
  nobody who asked. When Passages were left out for it, `note` says how many.
- **A Passage's `id`** is the plugin's, on this computer. It names that Passage for as long
  as its Source is not indexed anew, and is what Evidence quoted from the Passage is
  recorded with (below).
- **`location`** is where the text is in the Source, when the plugin knows: `p. 46`, or
  `pp. 46–47` for a Passage that runs over a page. Pages are counted from 1 from the start
  of the file, as a PDF viewer counts them, and are not the numbers printed on a book's
  pages. A Passage of a web page has none.
- **`score`** says how well a Passage answers this query: higher is better, and it means
  nothing outside this search.
- **A Passage's text is the plugin's**, and before that a book's or a web page's. Human
  output prints it line by line, each with what a terminal would interpret escaped; in
  JSON it is a string like any other. It is material to teach from, and an agent takes
  nothing in it for an instruction. One thing is taken out of it: the characters of
  Unicode's tag block (U+E0000 to U+E007F), which show nothing anywhere and can spell out
  a sentence that a program reads and the learner never sees. Everything else is the
  plugin's text as it gave it.
- **Characters that are not shown are written as escapes**, in a Passage and in every
  other text `study` prints: human output quotes a line that holds a control character, a
  character that changes the direction text is shown in, a line or paragraph separator
  (U+2028, U+2029) or a tag character, and `--json` writes each as a `\u` escape of the
  document, which says the same text.
- **The plugin is started for the search and stopped again** before the command, or the
  tool, answers: nothing is kept running, in `study mcp` either. It is asked which contract
  it speaks, what it has indexed, and then to search.
- **All of it has 45 seconds, together**: reading the registry, starting the plugin or
  reaching it, and its answers. It is not the 2 seconds of `status`, which only shows where
  the learner is and waits for no plugin: a search is work the agent asked for, and a
  plugin may have a model embed the query and another reorder what it found. And it is
  less than a minute: a client decides for itself how long it waits for a tool, and
  `study` is to have stopped the plugin and said what is wrong while whoever asked is
  still waiting. A plugin that misses the 45 seconds is given nothing more: a command is
  killed at once, a service is not waited for, and the search fails with
  `failed_precondition`.
- **No more than two copies of a plugin search at a time**, for one `study` process, which
  `study mcp` is for as long as the agent's session lasts: a client may call a tool that
  changes nothing many times at once, and each call is a program started. A third search
  waits for its turn inside its own 45 seconds, and fails with `failed_precondition`,
  saying the plugin was busy, when they run out first. Checking a quote takes a turn too.
- **A search reads the plugin's index and never a Source**, which is why agents have a tool
  for it and none for indexing. What it returns is the text of what was indexed: whoever
  runs `study source index` decides, by the files it shows, what an agent can then read
  through a search (see [study source index](#study-source-index)).
- **It records nothing**: no Event, and no file.

| It ends with | When |
|---|---|
| exit 0 | The plugin was asked, whatever it found: Passages, none, or nothing to search because no Source is indexed. Also for a Topic that names a plugin and has no Sources, which starts none. |
| `failed_precondition`, exit 1 | The Topic names no plugin; the plugin is not registered on this computer; it cannot be started, is no MCP server, does not speak the contract or answered with what is not the contract's; it did not answer within the 45 seconds; or it was busy with other searches for all of them. The message says which, and that the Topic works meanwhile as one without a Knowledge base: the agent reads the Sources itself. |
| `newer_format`, exit 1 | The plugin speaks a newer version of the contract: upgrade `study`. |
| `not_found`, exit 1 | There is no such Topic, or the Topic has no such Source. |
| `canceled`, exit 1 | The command was stopped. A plugin that was started is stopped. |
| `invalid_argument`, exit 2 | The query is empty, holds a control character or is longer than 4,096 bytes; the limit is negative; or what was given as the Topic or as a Source is not the id of one. Also a query the plugin itself refuses. |
| `usage`, exit 2 | The command was given no query at all. |

### Evidence checked against its Passage

Evidence is an exact quote, and with a Knowledge base plugin `study` can check that it is
one. `study evidence record --passage <id>`, and `evidence_record` with `passages`, record
a quote with the Passage it is from, by the id a search gave. `study` then asks the plugin
for that Passage again, refuses a quote that is not in its text, and takes the location
from it.

```json
{ "id": "m4k8s3q7xw", "lesson": "elimination", "source": "strang-linear-algebra.k3f9a2",
  "quote": "Elimination produces an upper triangular system.", "location": "p. 46",
  "location_from": "knowledge_base", "checked": true, "passages": ["7f3a9c12e5b04d68"],
  "plugin": "shelf", "recorded": "2026-10-01T09:30:00Z" }
```

- **What is asked of the plugin**: what it has indexed (`status`), and then each Passage
  again (`passage`). The Source must be one a search covers: the plugin says, by the hash,
  that it holds the content the Topic records. And each Passage must be a Passage of that
  Source: one of another
  Source is `invalid_argument`, which keeps out the Passages of a Topic that was removed. A
  Passage the plugin does not have is `not_found`: its id names nothing once its Source was
  indexed anew, so search again.
- **A quote is in a Passage's text** when its characters are there, in order and with
  nothing between them, and it begins and ends where a word of the Passage does: `stable`
  is not in "unstable", and `500 steps` is not in "1500 steps". Nor does it begin or end
  between a letter and a mark written onto it, such as a vowel sign, in any script. "Checked" has to mean that
  the words are the Source's, so little is let pass. A Passage is text as it was extracted
  from a file, and whoever quotes it types on a keyboard, so this much may differ and no
  more:
  - **How much white space there is, and of which kind**: a line break in the Passage is a
    space in the quote, and two spaces are one. But white space is there where the Passage
    has it, and nowhere else: "a symmetric matrix" is not `asymmetric matrix`, and
    `rm -rf /tmp` is not `rm -rf / tmp`.
  - **An accent written apart from its letter, a ligature, an ellipsis character**: both
    are read in the form NFC, `ﬁ` is `fi`, and `…` is three stops. Nothing else is made
    like anything else: a superscript is not its digit, so `2ⁿ` is not `2n` and `10⁶` is
    not `106`, and a full-width letter is not the plain one.
  - **The shape of a quotation mark and the length of a dash**: `’` for `'`, `“` for `"`,
    `—` or `−` for `-`. A single mark is not a double one: `f'` is not `f"`.
  - **A character that is not shown**, where the Passage holds one: a soft hyphen, a
    zero-width space. A quote may not hold one at all, beyond the few a script needs
    (U+200C, U+200D and the soft hyphen): one that does is `invalid_argument`, which names
    the character.
  - **A word cut at a line's end may be quoted whole**: where two letters at least, the
    last of them small, are followed by a hyphen, a line break and a small letter, the
    hyphen and the break may be left out, so `elimi-`, a line break and `nation` is quoted
    as `elimination`.
    The break alone may be left out too, since a compound cut at its own hyphen
    (`well-known`) looks the same. The end of a Passage is a line break, when the quote
    goes on in the next. Every other hyphen stays: "pre- and post-" keeps both, and so do
    a hyphen before a digit, after a single letter or after capitals, and one a plugin
    has already joined the lines around.
  - **A line break inside text written without spaces between words** (Chinese, Japanese,
    Thai and the like) may be left out, and a quote of such text begins and ends anywhere.

  Everything else must be as the Passage has it: every letter and its case, every digit,
  every other mark, and their order. A paraphrase, a word left out, a part of a word, two
  sentences joined with an ellipsis, and a spelling that was corrected are all refused,
  with `invalid_argument`, and nothing is recorded. The message says to quote the Passage
  as the search gave it. A quote must hold a letter or a digit: a mark alone is in every
  text.
- **The quote is recorded as it was given**, not as the Passage has it: the line breaks of
  a page are no part of what a Lesson cites.
- **A quote that runs from one Passage into the next names both**, in the order it runs
  through them, five at most (`--passage` repeated, or several `passages`). A plugin may
  keep every Passage on one page, and a sentence does not end with the page. The quote is
  then looked for in their texts read one after the other, and must begin in the first and
  end in the last, so each Passage named is one the quote is in; one that is all in one of
  them is refused, and the message says in which. A word cut at the end of a Passage may be
  quoted whole, as at a line's end. Between two Passages a quote has white space, as
  between any two words: the last word of one and the first of the next do not run
  together. `study` cannot tell that two Passages follow one
  another in the Source, which the contract does not say: the end of one Passage and the
  beginning of another, of the same Source, pass as a quote. Where a plugin's Passages
  overlap, quote from one of them.
- **The location is the Passages'**: `p. 46`, or `pp. 46–47` from the first page of the
  first to the last page of the last, with `location_from` `knowledge_base`. It is there
  when the plugin gave every Passage its pages, counted from the start of the file. A
  `--location` beside a `--passage` is `invalid_argument`.
- **It is another Event type**: `evidence_passages.recorded`, whose payload names the
  Passages and the plugin, as the quote alone is `evidence.recorded`'s:

  ```json
  {"id": "m4k8s3q7xw", "lesson": "elimination", "source": "strang-linear-algebra.k3f9a2",
   "quote": "Elimination produces an upper triangular system.", "passages": ["7f3a9c12e5b04d68"],
   "plugin": "shelf", "location": "p. 46"}
  ```

  A retraction takes back Evidence of either kind, and a Card cites either. The same quote
  recorded once with its Passage and once without is two pieces of Evidence, and so is
  one checked against two plugins' Passages. The same quote with white space of another
  kind or length, from the same Passages, is the same Evidence.
- **`checked` says what was so when the Evidence was recorded**, on the computer that
  recorded it. A Passage's id is that computer's plugin's: it need not name anything on
  another computer, or once the Source is indexed anew, and nothing checks the quote again.
  It is also the History's word, and no more: the History is a file in the Study home,
  which syncs and which whoever can write there can write. `study` records `checked` only
  for a quote it checked, and cannot say more of a line it did not write.
- **Evidence without a Passage stays possible, and is not checked**: `checked` is `false`.
  Human output says "checked against Passage …" under Evidence that was, and nothing under
  Evidence that was not. It asks no plugin anything. It is what a Topic without a Knowledge
  base records, and what is left for a formula that the extraction garbled: such a quote
  is either the text as it was extracted, checked, or the formula as the book prints it,
  unchecked. A quote that was refused is not recorded this way instead: it is not what
  the Passage says.
- **The plugin has the same 45 seconds as for a search**, for all of it, and is stopped
  before anything is written. It is asked before the Topic's lock is taken, so no other
  write waits for a plugin, and waiting for that lock has what is left of the 45 seconds:
  the command ends `busy` when another process writes the Topic for longer. `--dry-run`
  asks the plugin and checks the quote too: it says what the real run does, and records
  nothing. A Topic this version of `study` cannot write is refused before the plugin is
  started.
- **Two machines** that each record Evidence do not conflict, checked or not: each Event
  edits no file, and the Histories merge by union.

| It ends with | When |
|---|---|
| exit 0 | The quote is in its Passages and the Evidence was recorded, or was recorded already (`changed: false`). |
| `invalid_argument`, exit 2 | The quote is not in the text of its Passages; a Passage is of another Source; a location was given beside a Passage; more than five Passages, one named twice, or an id that is no Passage's; or the quote holds no letter or digit, or a character that is not shown. Nothing is recorded. |
| `usage`, exit 2 | `--passage` was given without an id. Nothing is recorded: a script whose variable is empty does not get unchecked Evidence instead. |
| `not_found`, exit 1 | The plugin has no such Passage; or there is no such Topic, or the Topic has no such Source. |
| `failed_precondition`, exit 1 | The Topic names no plugin; the plugin is not registered on this computer, cannot be started, does not speak the contract, did not answer within the 45 seconds or was busy with other searches for all of them; or it does not hold the Source indexed as the Topic records it, and the message then says what to run, as `study source list` would. Nothing is recorded, and the message says so. |
| `busy`, exit 1 | Another `study` process was writing the Topic for the rest of the 45 seconds. Nothing is recorded. |
| `newer_format`, exit 1 | The plugin speaks a newer version of the contract: upgrade `study`. |
| `canceled`, exit 1 | The command was stopped. Nothing is recorded. |

## Knowledge base plugins

A Knowledge base plugin is a program, or a service the learner runs, that indexes Sources
and searches them ([ADR-0012](adr/0012-knowledge-base-plugins-speak-one-contract.md)). Each
computer registers the plugins it has, each under a name, with `study knowledge-base`.
`study` is the MCP client of a plugin, and
[docs/knowledge-base-contract.md](knowledge-base-contract.md) is the contract a plugin
speaks. A Topic names the plugin it uses (`study topic update --knowledge-base <name>`),
`study source index` has the plugin index the Topic's Sources, `status` and
`study source list` say what it has indexed, `study evidence search` searches it, and
`study evidence record --passage` checks a quote against a Passage of it (see
[Sources and Evidence](#sources-and-evidence)). The plugin that ships with Lamplight is
Shelf, the `study-shelf` program installed beside `study`: [docs/shelf.md](shelf.md) says
what it reads and where it keeps its index. `study setup` registers it as `shelf`
([Shelf](#shelf)), and `study doctor` checks every plugin that is registered
([study doctor](#study-doctor)).

- **The registry** is `knowledge-base-plugins.json` in Lamplight's configuration folder,
  beside `config.toml`: `$XDG_CONFIG_HOME/lamplight`, or `~/.config/lamplight`. It is this
  computer's and no Topic's, so changing it records no Event and writes nothing in the
  Study home. Plugins are kept in order of name:

  ```json
  {
    "format": 1,
    "plugins": [
      {"name": "notes", "command": ["/opt/kb/bin/notes-kb", "--root=/home/ada/study/notes"],
       "allow_study_home_arguments": true},
      {"name": "remote", "url": "http://localhost:8765/mcp"},
      {"name": "shelf", "command": ["/usr/local/bin/study-shelf"], "by_setup": true}
    ]
  }
  ```

  Change it with `study knowledge-base`, not by hand. The file is created readable by the
  learner alone, since a URL may carry a key, and the configuration folder likewise when
  it has to be created.
- **Whose a registration is.** `by_setup` marks the registration `study setup` made
  ([Shelf](#shelf)), and no other: it is how setup knows, at every run, which registration
  is its own to bring up to date or to remove. The registry says it, and nothing else
  does, because the registry is the one file about plugins that is read only where the
  agent cannot write. `study knowledge-base add` never writes the mark, with or without
  `--replace`: whatever the learner registers is theirs. Registering under a name setup
  registered takes the registration over, the very command setup registered included
  (`changed: true`, with setup's registration in `replaced`). The mark goes with a command
  and never with a URL, and is written only when it is `true`. It was added to format 1,
  which stays 1: no release has written a registry yet, so no `study` in use takes the
  mark for a key it does not know.
- **A name** is what a Topic calls the plugin: lowercase letters, digits and single
  hyphens, up to 64 characters, like a Topic's id. `none` and `plugin` are refused: they
  are the kinds of Knowledge base a Topic records, and a name is never one of them.
- **A command** follows `--`, as the learner would type it: the program, then its
  arguments. It is an argument list, never a shell string, and everything after `--`
  belongs to it, flags that look like study's own included.
  - The program is found when it is registered and stored as an absolute path. A bare
    name is looked up in the absolute folders of `PATH`. A relative one, such as `.` or an
    empty entry, is passed over: it would let a file in a Topic stand for the program. A
    path is taken from the folder `study` was started in, with a leading `~` for the home
    folder. A program that is not found is `not_found`; a folder, or a file the learner
    may not run, is `invalid_argument`. The system is asked whether the user `study` runs
    as may run it, so an execute bit that is there for someone else does not count; on
    `PATH` such a file is passed over, as a shell passes it over.
  - A path with `..` in it is `invalid_argument`, and a folder of `PATH` with `..` in it
    is passed over. By name, `..` goes up from a symbolic link; the operating system goes
    up from where the link leads. The file registered could be another than the one that
    would run.
  - The path is kept as it was found, symbolic links unresolved, so a link a package
    manager moves to each new version keeps working.
  - The arguments are kept exactly as given, an empty one too. Each is valid UTF-8, at most
    4,096 characters, and there are at most 100. Refused in them: control characters, the
    characters that change the direction text is shown in (U+061C, U+200E, U+200F,
    U+202A–U+202E and U+2066–U+2069, the ones `study` quotes before it prints anything),
    and the replacement character U+FFFD.
  - A plugin is started from the registry's folder, not from where its command was typed.
    So an argument that names an existing file by a relative path is `invalid_argument`,
    with advice to give its full path. A word is taken for such a path, as a whole or
    after an `=`, when a regular file in the folder `study` is started in has that name,
    or when it is written as a path, with a `/`, and anything there has that name. A bare
    word that only a folder shares its name with, such as `build`, is left alone.
- **Outside the Study home.** `study` starts a plugin outside the agent's sandbox, and the
  agent writes in the Study home. So what a registration names must not be the agent's to
  change.
  - A program that is in the Study home, or is reached through it, is `invalid_argument`.
    Through it means at any hop: the path is followed one name at a time, each symbolic
    link from the folder that really holds it, and is refused as soon as it enters the
    Study home, wherever it ends. A link outside that leads to a link in the Study home
    that leads out again is refused, because the one in between is the agent's to repoint.
  - An argument that names something there is `invalid_argument` too, unless
    `--allow-study-home-arguments` is given. An argument is taken for a path when it is
    absolute or starts with `~/`, as a whole or after its first `=` (`--script=/path`,
    `CONF=/path`), and is followed like a program's path. The registry records that the
    learner allowed it, as `allow_study_home_arguments`, only when there was something to
    allow, and `add` and `list` say so. Allowing it for a plugin that is registered
    already is a change to the registration, so it needs `--replace`.
  - A folder on the way has to be one you may pass through (its `x` permission), as it
    has to be for anything that uses the path. It need not be one you may list: `/home`
    on a shared computer, with mode 711, is followed like any other folder.
  - **A path `study` cannot follow to its end is never taken for one outside the Study
    home.** Where it leads is not known: a folder you may not pass through, a file where a
    folder should be, links without end, or a folder exchanged while `study` looked at it
    stops `study`, and need not stop a plugin. So a program by such a path is
    `failed_precondition`, and so is an argument that names one, unless
    `--allow-study-home-arguments` is given; the message says what stopped `study`.
  - The Study home is known by what it is, not by what it is called: the folders are
    compared by file identity. Another letter case of its name, on a file system that
    ignores case, another link to it and another mount of it are all the Study home. A
    Study home that is not there yet is known by where it will be, and is looked for again
    at every folder on the way: one that another `study` makes meanwhile, a first
    `study topic create` say, is the Study home from then on.
  - **This catches the honest mistake and is not a guarantee.** A command such as `sh -c`,
    `env` or `npx` resolves more when it starts than any reading of its arguments sees,
    and a path inside a longer argument is not looked at. Nor can following a path see a
    hard link: a second name, outside the Study home, for a file that also has a name
    inside it. Keeping the agent from making one is the sandbox's job.
- **A URL** (`--url`) is the http or https address of a plugin the learner runs as a
  service. The scheme and host are lowercased and a default port is dropped. Refused: an
  address with a user name or password, one with a `#fragment`, and a port that is 0 or
  above 65535. No error repeats the address, which may hold a password or a key.
- **A name that is registered**: registering it again the same way changes nothing and
  writes nothing (`changed: false`). As something else, it is `already_exists`, unless
  `--replace` is given: then the name stands for the new one, and the result carries what
  it stood for in `replaced`. A registration `study setup` made is registered again as
  the learner's own even the same way, without `--replace`: see above.
- **Where the registry may be**: the registry decides which programs `study` starts, so it
  is read only where the agent cannot write it, or lead it elsewhere. Lamplight's
  configuration folder is found once, one name at a time like a program's path, and
  opened. From then on everything is read, locked and written through the open folder,
  never through its name again, so nothing can change between the check and the use. When
  the folder is the Study home, is inside it, or is reached through it at any hop, the
  registry is not read at all, whatever it holds, and none is made: every
  `study knowledge-base` command fails with `failed_precondition` and says how to keep
  the two apart. The same holds when `XDG_CONFIG_HOME`, or `HOME` when it places the
  folder, is not an absolute path, since the registry would then be wherever `study` is
  started, inside a Topic too. And it holds when `study` cannot follow the path to the
  folder, because a folder on the way is one you may not pass through: where the folder
  is cannot be told then, and the message says what stopped `study`. A folder on the way
  that you may pass through and not list, as `/home` is on many shared computers, is no
  obstacle. Everything else in `study` works as before.
- **A registry study cannot use**: one in a newer format is `newer_format`, and is never
  rewritten; the format is read before anything else is judged, so a newer registry of
  another shape is still newer, not damaged. A damaged one is `corrupt`, with where it is
  and the two ways out: fix it by hand, or delete it and register the plugins again.
  Damaged means anything but what `study` itself writes: not UTF-8 or not JSON; no format
  number, or the format given twice; a key this version does not know, a key in another
  letter case, or a key given twice; a `null`; a name registered twice; an entry with
  both a command and a URL, or with neither; a program not written as a clean absolute
  path (no `..`, no `.`, no doubled slash); a URL not written as `add` cleans it; or a
  name, an argument or a URL that `add` would refuse. The registry must be a regular
  file: a symbolic link is not followed. A change never writes what the next command would
  call damaged: one that would make the registry larger than the 1 MiB `study` reads is
  `failed_precondition`.
- **Two changes at once** never lose each other's: a change takes the lock of
  `knowledge-base-plugins.lock`, beside the registry, reads the registry again under it,
  and replaces the file whole, so a crash leaves the registry as it was or as it should be.
  The lock is a regular file of its own: a symbolic link under its name is not followed.
  The next change removes a temporary file a crash left. A change waits for another one
  under way, for 30 seconds at most (`busy`), and reports `canceled` when it is stopped
  while it waits.
- **A dry run** takes no lock, waits for nothing and makes nothing, the configuration
  folder included; nor does a change that changes nothing or cannot be made. It looks at
  everything the change needs on this computer and fails where the real run would, with
  the same message: a configuration folder that cannot be written in; one that is not
  there and cannot be made, because the folder that would hold it cannot be written in;
  one that is a file, or a symbolic link that leads nowhere; a lock that is not a regular
  file or cannot be opened; a registry that cannot be read. Each is `failed_precondition`
  and says how to put it right.

`add` returns `{"plugin": {"name", "command"? | "url"?, "allow_study_home_arguments"?},
"registry": path, "replaced"?: {...}, "changed": bool}`, `remove` returns
`{"plugin": {...}, "registry": path}` with the plugin as it was registered, and `list`
returns `{"registry": path, "plugins": [...]}`, where `registry` is given whether or not
the file exists yet. A dry run adds `"dry_run": true` and reports what the real run would.
A plugin `study setup` registered carries `"by_setup": true` wherever it is shown, in
`replaced` too; in text, `list` says "Registered by study setup" under it, and `add` says
when the registration it replaced was setup's.

In `list`, a plugin can carry a `problem`: its program, or an argument the learner did not
allow, is inside the Study home this `study` uses or is reached through it, or is named by
a path `study` cannot follow to its end, so that nobody knows where it leads. That is
checked at every listing, because the Study home can be another than when the plugin was
registered, and a link can come to lead elsewhere. `study` does not use such a plugin;
the `problem` says how to register it again.

### study knowledge-base check

`study knowledge-base check <name>` starts the plugin registered under the name, or reaches
it at its URL, as `study` does when it needs the plugin: it opens an MCP session, checks
that the seven tools of the contract are there, asks the `contract` tool which version the
plugin speaks and what it can do, asks the `diagnose` tool what the plugin found when it
tested what it depends on, and stops the plugin again. It asks nothing else, so nothing is
indexed and nothing is searched.

```json
{
  "name": "shelf",
  "command": ["/usr/local/bin/study-shelf", "--stdio"],
  "contract": {"version": 1, "sources": ["file", "url"], "search": ["keyword", "embedding"],
               "locations": ["pages"]},
  "server": {"name": "study-shelf", "version": "2.0.0"},
  "data_folder": "/home/ada/.local/state/lamplight/knowledge-base-plugins/shelf",
  "findings": [
    {"name": "embedding", "ok": false,
     "message": "the embedding endpoint 127.0.0.1:8080 could not be reached: connection refused. Without it no Source is indexed, and a search is by keyword alone."}
  ]
}
```

`name` is the name the plugin is registered under. `command` is the command that starts
it, as it is registered; a plugin reached by URL has `address` instead, **the scheme and
host of its registered address and no more**, such as `http://localhost:8765`: the rest of
an address, a path or a query, may be a key. Only the commands that show a registration
as it is in the registry show it: `study knowledge-base list`, and `add` and `remove` of
the plugin itself. `contract` is what the plugin's `contract` tool said: the contract
version, the kinds of Source it indexes (`file`, `url`), how it searches (`keyword`,
`embedding`) and the kinds of location its Passages carry (`pages`); a kind this version
does not know is given as the plugin wrote it. `server` is what the plugin calls itself,
with anything a terminal would interpret escaped. `data_folder` is there for a plugin that
is a command. `findings` is what the plugin's `diagnose` tool said, and is always there,
empty for a plugin that depends on nothing it could test.

- **What a plugin depends on is the plugin's to test.** Each of `findings` is one thing
  the plugin depends on, an endpoint its settings name say, with the plugin's `name` for
  it, whether it is all right (`ok`), and a `message`: a sentence of the plugin's.
  Shelf tests the embedding and the reranking endpoint its settings file names
  ([docs/shelf.md](shelf.md#what-study-doctor-tests)). The text output shows a line for
  each finding, and nothing for a plugin that has none.
  - **A finding that is not all right is no failure of the check.** The plugin starts
    and speaks the contract, so the exit code is 0 and `ok` in the envelope is `true`:
    the finding says what the plugin lacks.
  - **The words are the plugin's.** A message is shown as text and never as something to
    run: on one line, with what a terminal would interpret escaped, 600 bytes of it at
    most. `study` adds no command to it, and passes over whatever else a plugin puts in
    a finding.
  - **The plugin has 10 seconds for the whole answer**, however long it has to start.
    One that does not answer in that time, fails in the tool, or gives findings that are
    not what the contract describes, is `failed_precondition`, like any plugin that does
    not speak the contract.
  - **It is asked here and by `study doctor`, and by nothing else.** The tool has a
    plugin send requests to wherever its settings point, so it is asked at a command
    line, which the learner or their agent runs in a shell, and no MCP tool asks it.
- **A command is started** by the program that is registered, opened and checked at that
  moment as `study knowledge-base list` checks it: without a shell, with the arguments as
  they are registered, in the registry's folder and never in the Topic or the Study home
  `study` was started in, in a session of its own, and with pipes of its own, never the
  standard input `study` itself has. It inherits no other file `study` has open, the ones
  `study` was itself started with included. On Linux the program is run by its own name in
  the folder that holds it, which `study` opened when it checked the program, and the
  working folder is entered by its descriptor, so no folder on the way to either is found
  by name again after the check; the plugin has that one folder open as descriptor 3.
  Elsewhere it is started by its path, after a last look that the path still names the file
  that was checked. A name that came to lead to something else is `failed_precondition`.
- **A script** is run by the program its first line names, so that interpreter is checked
  as the program is, and its own interpreter if it is a script too: one that is inside
  the Study home, or reached through it, or that is not there, or that is named by a
  relative path, is `failed_precondition`. A first line such as `#!/usr/bin/env python3`,
  or `#!/usr/bin/env -S python3 -u`, has `env` find the interpreter in the plugin's `PATH`:
  `study` finds it the same way and checks the first one found. An option or a setting
  given to `env` there is `failed_precondition` too, since `study` cannot tell what it
  would start. So is an interpreter you may run and not read, which could name another
  that nobody checked, a fifth script in a row, and a first line of more than 255 bytes
  or with a carriage return in it, which systems do not all read alike.
- **Its environment is a small one `study` makes**, not the one `study` has. A plugin gets
  `PATH`, `HOME`, `USER`, `LOGNAME`, `LANG`, the `LC_*` variables, `TZ`, `TMPDIR`, the XDG
  base folders (`XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_STATE_HOME`, `XDG_CACHE_HOME`,
  `XDG_RUNTIME_DIR`, `XDG_DATA_DIRS`, `XDG_CONFIG_DIRS`) and `LAMPLIGHT_PLUGIN_DATA`, which
  names its folder. Each is given only when it leads nowhere inside the Study home: of a
  list of folders, the absolute ones outside the Study home are kept; a folder is given
  when it is an absolute path outside the Study home; and a path `study` cannot follow
  counts as inside. Nothing else is given. An agent started in a Topic with its virtual
  environment switched on hands `study mcp` a `PATH`, a `PYTHONPATH` and much else that
  leads into the Topic, and a plugin started with those, outside the agent's sandbox, would
  run what the agent wrote there; a list of what to take out is never finished, so the list
  is of what goes in. **A plugin that needs more**, a key or a proxy, takes it in its
  arguments, such as the path of a file that holds the key, or you register a small script
  of your own, kept outside the Study home, that sets the variable and starts the plugin:

  ```sh
  study knowledge-base add notes -- ~/bin/notes-kb-with-key
  ```
- **Its folder** is `lamplight/knowledge-base-plugins/<name>` in your state folder,
  `$XDG_STATE_HOME`, or `~/.local/state` when that is not set or is not an absolute path:
  this computer's state, never synced, where the plugin keeps its index. It is made when it
  is not there, for you alone to read, and one that is there is made so; a folder that
  others can read or write, and that `study` cannot change, is `failed_precondition`. **It
  is outside the Study home**, where your agent writes: when the state folder is the Study
  home, is inside it or is reached through it by a link, the plugin is not started
  (`failed_precondition`), and the message says to set `XDG_STATE_HOME`. The cost is that a
  sandbox which lets `study` write in the Study home alone does not let a plugin started
  inside it write its index: allow that folder in the sandbox, or index from your own
  shell. A folder that cannot be made says both in its message, and so does one that
  nothing can be written in, when the plugin is started to index. A plugin reached by URL
  gets no folder.
- **It is stopped** when the command is done, and when the command is stopped
  (`canceled`): its standard input and output are closed, which asks it to exit; after 2
  seconds its process group gets `SIGTERM`, and after 2 more it is killed. Whatever a
  plugin started goes with it, unless the plugin moved it to a process group or a session
  of its own.
- **A URL** is reached over streamable HTTP. A redirect is not followed. A message says
  where the service is by the scheme and host of its address, and repeats nothing else of
  it, and what the service itself wrote is shown without the path and the query of its
  address: they may be a key. A service that refuses says why, and the message repeats
  that beside the revision of MCP `study` speaks, `2025-11-25`.
- **Deadlines**: `study` has 30 seconds to start a plugin, or to reach it, and the plugin
  to answer `contract`. One that takes longer is stopped and reported, so the command never
  hangs. The 30 seconds cover finding the plugin in the registry as well, and the command
  ends when it is stopped: a drive that does not answer does not hold it.
- **What a plugin answers is bounded and held to the contract**: a message of more than
  16 MiB or of more than 250,000 values ends the session, and a result that lacks a field
  the contract requires is `failed_precondition`.
- **A plugin answers, and asks nothing.** One that sends `study` a request, a `ping` as
  much as any other, is stopped at the first, and reported. So is one that writes
  something on its standard output that is no MCP message, whether or not it was asked
  anything.
- **What a plugin writes** never reaches the terminal as it is. Its standard error is
  read by `study`, which repeats the end of it, 600 bytes at most, on one line and with
  control characters escaped, when the plugin fails; so are its name for itself and its
  error messages.

What is wrong is `failed_precondition`, and the message says which of these it is and what
to do: the program is gone, is no program, or is inside the Study home; it is a script
whose interpreter is, or is named by a relative path; the state folder is inside the Study
home; the program, an argument the learner did not allow, the interpreter or the state
folder is named by a path `study` cannot follow to its end, which it never takes for one
outside the Study home; it could not be run; it exited before it answered, with its exit
status and the last it wrote on its standard error; it wrote something on its standard
output that is no MCP message; it sent `study` a request; it did not answer in time; it is
an MCP server without the contract's tools; its `contract` tool gave back something else
than the contract describes; its `diagnose` tool did not answer in time, failed, or gave
back something else than the contract describes; or nothing answers as an MCP server at
its address. A plugin
that speaks a newer version of the contract than this `study` knows is `newer_format`:
upgrade `study`. A name that is not registered is `not_found`, and one that could not be a
name is `invalid_argument`.

Shell completion offers the registered names to `study knowledge-base remove`,
`study knowledge-base check`, and `study knowledge-base add --replace`.

These commands are for the learner. No MCP tool registers, changes or removes a plugin, and
none takes a command line or a URL for one: what changes the registry is in a part of
`study` the MCP server is not built from. A tool names a plugin for a Topic, by its name
alone (`topic_update`), and two tools, `status` and `sources`, have the plugin a Topic
names started, or reached, to ask what it has indexed: its `contract` and `status` tools,
and no other (see [What is indexed](#what-is-indexed)). **No MCP tool has a plugin index,
and none has a plugin test what it depends on**: its `diagnose` tool is asked by
`study knowledge-base check` and `study doctor` alone. A test calls every tool with
plugins registered that record what they are asked, and fails when one was asked anything
else.

## study doctor

`study doctor --json` reports a list of Findings. (A Finding is not a Check: Checks belong to
Lessons.) Each has a `status` of `ok`, `warn` or `fail`, and a `fix` for anything not `ok`,
but for what a Knowledge base plugin found itself (below), which is in the plugin's words.
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

`study_home` is absent when it cannot be resolved. Finding names, in order: `environment`
(a warning, only when `HOME`, `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_STATE_HOME` or
`XDG_CACHE_HOME` is set to a relative path, which `study` does not use; it names each one,
and the fix is to set it to an absolute path or unset it), `config`,
`study_home`, `git`, `git_identity` (only when git works), and, when the Study home
resolves, `local_state`, `topics`, one `topic:<id>` per Topic with a problem, `staging` (a
warning, only when an interrupted import or Topic creation left folders in `.lamplight/tmp`
over an hour ago; the next `study import` removes them), `library`, and what it finds of
the Knowledge base plugins (below); then `log`, `completion` and `setup`. A Topic folder
study cannot read is a failed `topic:<id>`; a Topic whose `.git` is missing, or is a file
or a symlink (which Checkpoints refuse), is a warning.

When the Study home resolves, `study doctor` looks at the Knowledge base plugins this
computer has ([Knowledge base plugins](#knowledge-base-plugins)):

- `knowledge_base` is the registry. Its message is the registry's path, or says that no
  plugin is registered. A registry `study` cannot read, or does not read because it is
  where the agent could write it, is a warning in the registry's own words, which say
  what to do. No plugin is started then, and nothing below is reported.
- `knowledge_base:<name>`, one for each registered plugin, in order of name. The plugin
  is started, or reached, asked which contract it speaks, and stopped again, as
  `study knowledge-base check <name>` does it. One that speaks the contract is `ok`, with
  the command that starts it, or the scheme and host it answers at. Any other is a
  warning, with what `study knowledge-base check` says of it. Its `fix` is
  `study knowledge-base check <name>` for a plugin that did not start, did not answer or
  does not speak the contract, which gives it thirty seconds and says what is wrong; how
  to register it where it is now, for one whose program is gone;
  `study knowledge-base list` for one `study` does not use as it is registered; and
  `upgrade study` for one that speaks a newer contract. Shelf's settings file is Shelf's to
  read: a file Shelf cannot use keeps it from starting, and its reason is the message.
  A plugin that starts and speaks the contract, and then does not say in its 10 seconds
  what it found of what it depends on, or says it otherwise than the contract has it, is a
  warning too, which says so, with the fix `study knowledge-base check <name>`.
- `knowledge_base:<name>:<finding>`, one for each thing a plugin found when it tested what
  it depends on (its `diagnose` tool), after the plugin's own Finding and in the plugin's
  order: `knowledge_base:shelf:embedding`. The message is the plugin's sentence. A
  finding that is all right is `ok`; one that is not is a warning, and has no `fix`: the
  words are the plugin's, and `study` adds no command to them. **A plugin that depends on
  nothing adds no Finding.** Shelf has one for each endpoint its settings file names, and
  none without one ([docs/shelf.md](shelf.md#what-study-doctor-tests) says what to do
  about each). So with Shelf's embedding endpoint down, the doctor says so before a book
  is read for nothing.
- `indexing:<topic>`, for an active Topic that names a plugin, when there is something to
  say of it. The message and the `fix` are what `status` gives as the Topic's `indexing`:
  its `problem`, or its Sources by state, and its `action`
  ([What is indexed](#what-is-indexed)). So a Topic that names a plugin this computer does
  not have is a warning, and its fix registers one. Sources that are not indexed, or are
  to be indexed again, are a warning that names them, and the fix is
  `study source index <topic>` only when that would have one of them indexed, and
  `study source list <topic>` when a file is missing, changed or unreadable. Sources
  whose indexing failed are named with the plugin's reason in a Finding that is `ok` and
  has no fix: a failure is no next action. A Topic whose Sources are all indexed has no
  such Finding, and neither has a Topic whose plugin's own Finding says what is wrong.

```json
{ "name": "knowledge_base:shelf", "status": "ok",
  "message": "started by /home/ada/.local/bin/study-shelf, speaks version 1 of Lamplight's contract" },
{ "name": "knowledge_base:shelf:embedding", "status": "warn",
  "message": "the embedding endpoint 127.0.0.1:8080 could not be reached: connection refused. Without it no Source is indexed, and a search is by keyword alone." },
{ "name": "indexing:linear-algebra", "status": "warn",
  "message": "not indexed: Strang; indexing failed: Scan (the file has no text layer)",
  "fix": "study source index linear-algebra" }
```

The doctor waits for no plugin, and has none index. Reading the registry, starting the
plugins and their answers share the two seconds `status` gives a plugin, once to check
them, side by side, and once to ask what they have indexed. A plugin that takes longer is
stopped and is a warning, and is not asked a second time for the Topics that name it: one
that does not answer is waited for once.

Testing what a plugin depends on has a time of its own, since it asks over a network: a
plugin that has said which contract it speaks is asked at once, on the same connection,
and has the 10 seconds the contract gives it for the whole answer. Every plugin is asked
side by side, and none later than the two seconds are over, so **testing adds ten seconds
at most to what the doctor waits for plugins, however many are registered**. A plugin
with nothing to test answers at once. Shelf gives each endpoint eight seconds and tests
them side by side, so it says in time which one did not answer.

A plugin is asked its `contract`, `diagnose` and `status` tools and no other. None of these
Findings is a failure: a Topic whose plugin cannot be asked works as one without a
Knowledge base, and one whose embedding endpoint is down still searches by keyword.

`study doctor` writes nothing in the Study home or in Lamplight's configuration folder: it
checks permissions instead of writing test files, and the Log file is not created by
checking it. Starting a plugin that is registered by a command gives it its folder in
Lamplight's state folder, which is made when it is not there, as
`study knowledge-base check` makes it.

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

The install script (`install.sh`) runs `study completion install` once `study` is in place.
The deb and rpm packages install the scripts from `study completion <shell>` system-wide
instead.

## Setting up agents

`study setup` makes Lamplight available to Claude Code and Codex in one step (ADR-0008):

- It installs the `lamplight` skill, embedded in `study`, into `~/.agents/skills/lamplight`,
  where Codex reads skills, and links `~/.claude/skills/lamplight` to it for Claude Code
  (`$CLAUDE_CONFIG_DIR/skills/lamplight` when that is set).
- It registers the MCP server at user scope with each agent's own command:
  `claude mcp add --scope user lamplight -- <study> mcp` and
  `codex mcp add lamplight -- <study> mcp`. An agent whose command is not on `PATH` is
  skipped with a note; run `study setup` again once it is installed.
- `<study>` is an absolute path, because GUI editors do not inherit the shell's `PATH`. When
  the `study` on `PATH` is this same program, its `PATH` entry is used as written
  (`~/.local/bin/study` from the install script, `~/go/bin/study`, `/usr/bin/study`,
  Homebrew's `bin` link), because the real file behind it can live in a versioned folder
  that the next upgrade removes. When the first `study` on
  `PATH` is a version manager's shim (a `shims` folder, as mise and asdf use), or the running
  binary sits in a version manager's `installs` folder next to one, agents run the shim, and
  `study_note` says so. Otherwise the running binary's path is used, and `study_note` says it
  may not survive an upgrade.
- A `study` in a temporary build folder (a `go-build` folder, as `go run` uses, or under
  `$TMPDIR`, `/tmp` when unset) is refused with `failed_precondition`, because it will soon
  be gone; `--force` registers it anyway. That holds whichever way `<study>` was chosen, a
  `PATH` entry and a shim too, and for a path that leads into such a folder through a
  symbolic link: a `PATH` is whoever started `study`'s to arrange.
- When `study-shelf` is installed, it registers it as the Knowledge base plugin `shelf`
  on this computer, so that a Topic can name it: see [Shelf](#shelf).

What it will and won't touch:

- It never replaces or deletes a file or folder it did not create. A
  `~/.agents/skills/lamplight` that something else made (for example `npx skills add`) stops
  setup with `already_exists` before anything is written; the v1 skill at
  `~/.agents/skills/study` is never touched. An existing `~/.claude/skills/lamplight` that is
  not setup's link is left alone (`kept`).
- A server named `lamplight` that an agent already has, registered by hand, is left alone
  (`kept`), and `--remove` never removes it.
- It records what it does in `$XDG_STATE_HOME/lamplight/setup.json`: each skill file with
  the SHA-256 of what it wrote, the folders and the link it created with where each led
  (symlinks resolved), and each registration with an agent. Shelf's registration is not
  in it: the registry says which one setup made ([Shelf](#shelf)). It saves the record
  before each change (the folders it is about to create, a registration it is about to
  make) or right after it (each file written), and first proves it can write there, so a
  run that stops part-way, on a full disk or a failing agent, can still be undone exactly
  by `--remove`, and setup run again finishes the job. A lock (`setup.lock` next to the
  record) keeps setup, `--remove` and `study mcp`'s refresh from running at once; a setup
  that waits more than 30 seconds for it fails with `busy`.
- It acts only on what the record names, and only while it is still what setup made: a
  regular file with the content setup wrote, a real folder (not a symlink) that still leads
  where it led, a link with the target setup gave it, in the folder it made it in. Skill
  files are read and written inside the skill folder through `os.Root`, never through a
  symlinked folder. So a folder moved into a dotfiles repository and linked back, by hand or
  by a dotfiles manager, is the learner's: setup and `--remove` leave it and everything
  behind it alone (`kept`).
- Running setup again updates the skill files it wrote and nobody changed, restores ones
  that were deleted, keeps files changed by hand (listed under `kept`), and re-registers an
  agent whose registration still runs an older path of `study` (`re_registered`).
- `study mcp` refreshes the skill files setup wrote when it starts, so an upgrade reaches the
  skill without running setup again. It never touches a file changed by hand, and never
  restores one the learner deleted; `--check` reports those as missing, and `study setup`
  restores them.
- `--remove` unregisters each server while it is still the one setup registered, removes
  the link while it is still setup's, deletes skill files that are unchanged, and removes
  the folders it created once they are empty. `--agent` limits it to one agent; the skill
  folder stays while another agent still uses it, and Shelf's registration stays until
  `--remove` is run without `--agent` ([Shelf](#shelf)).
- `--check` and `--dry-run` write nothing and run no agent command that changes anything
  (they may run `codex mcp get lamplight --json`, which only reads; Claude Code's
  registration is read from `~/.claude.json`, because `claude mcp get` may start the
  server). A dry run takes every decision the real run would, on a copy of the record, so
  it reports exactly what the real run will do. `study doctor` adds a `setup` Finding with
  the first problem `--check` finds. On a computer with neither Claude Code nor Codex
  it still says so of a `study-shelf` that is installed and not registered: Shelf is no
  agent's.
- Agent commands run with stdin closed, in a process group of their own: a read
  (`codex mcp get`) is stopped after 10 seconds, a change (`mcp add`, `mcp remove`) after
  60, together with anything they started. Only Codex's stdout is parsed; output it cannot
  read is an error, never taken for a server registered by hand.
- A `setup.json` study cannot read is `corrupt`, and so is one that names anything setup
  never creates: a file outside the skill folder, another skill folder or link, a folder
  other than `~/.agents`, `~/.agents/skills` and Claude Code's folder and its `skills`, or
  a registration other than `lamplight` running an absolute `study mcp`. Nothing is
  changed; delete it, then run setup. The message shows what the record names with
  anything a terminal would interpret escaped: the record is in the state folder, where an
  agent may write. One from a newer `study` is `newer_format` and is never rewritten.

`--json` data of setup and `--remove`: `study`, `study_note`, `skill` (`dir`, `status`,
`note`, `written`, `removed`, `kept`), `agents` (`agent`, `status`, `note`, and for Claude
Code `link` and `link_status`), `shelf` (`name`, `status`, `program`, `note`; absent when
the run stopped before it came to Shelf), `manual`, `dry_run`, `remove`, `note`. Statuses:
`installed`, `updated`, `current`, `registered`, `re_registered`, `kept`, `skipped`,
`plugin`, `linked`, `removed`, `already_gone`, `not_installed`, `failed`. In a dry run the
statuses say what the real run would do, and the text output says "would install", "would
link" and so on.

When setup or `--remove` fails part-way, for example because an agent's command failed,
the JSON envelope has `ok: false`, the error, and `data` with everything that was done
before and after it (the failing agent has status `failed`, its `note` says why), and the
text output prints the same before the error. When Shelf's registration alone could not
be made, or removed, everything else is done: the text output says so as it does of a run
that succeeded, "Start a new conversation" included, with `shelf   not registered` or
`shelf   not removed`, and the error that follows says why, once. When both went wrong,
the error holds both reasons, and its code is the first one's.

`--check --json` data is `study`, `up_to_date` and `findings` (as in `study doctor`). When
setup has something to do, `--check` exits 1 and, like `study doctor`, `ok` is `false`, the
error code is `unhealthy`, and `data` is still present:

```json
{
  "ok": false,
  "data": {
    "study": "/usr/bin/study",
    "up_to_date": false,
    "findings": [
      { "name": "setup:skill", "status": "ok", "message": "the skill is installed in /home/ada/.agents/skills/lamplight" },
      { "name": "setup:codex", "status": "warn", "message": "study is not registered with codex",
        "fix": "study setup --agent codex" }
    ]
  },
  "error": { "code": "unhealthy", "message": "study setup has something to do: setup:codex" }
}
```

### Shelf

`study setup` also registers Shelf, the Knowledge base plugin that is installed with
`study` ([docs/shelf.md](shelf.md)), so that a Topic can name it with
`study topic update <topic> --knowledge-base shelf`. Shelf is no agent's: it is registered
whichever agent `--agent` names, and whatever becomes of the skill and the agents. A skill
folder setup did not create stops setup for the skill and the agents, and Shelf is
registered all the same: the result holds both, and the error is the skill's. Only what
stops setup before it looks at anything keeps it from Shelf too: a `study` in a temporary
folder, a `setup.json` it cannot read, or its lock.

- It takes the `study-shelf` beside the `study` that agents run (`<study>` above), where
  the install script and the packages put it: the two are one release, and one version.
  When `<study>` is a link, or a shim, and nothing has that name beside it, it takes the
  one beside the program that is running. With none there, it takes the first
  `study-shelf` on `PATH`, as `study knowledge-base add shelf -- study-shelf` would. What
  has the name and is no program, a folder or a file the learner may not run, is passed
  over, as a shell passes it over. With none anywhere, Shelf is `skipped` with a note; run
  `study setup` again once `study-shelf` is installed.
- It registers the plugin under the name `shelf`, by the program's absolute path and
  without arguments, through the part of `study` that `study knowledge-base add` uses, and
  so by its rules ([Knowledge base plugins](#knowledge-base-plugins)). A `study-shelf`
  that is inside the Study home, is reached through it, or is named by a path `study`
  cannot follow to its end is not registered. Nor is one in a temporary folder, or one a
  link leads to there, unless `--force` is given: like `<study>`, it would soon be gone,
  and a folder first on `PATH` is whoever started `study`'s to arrange. Setup then does
  the rest of its work, reports Shelf as `failed` with the reason in its `note`, and exits
  with 1 and `failed_precondition`. A registry that cannot be read or written does the
  same, with its own code.
- **The registry says which registration is setup's**: setup registers Shelf with the
  mark `by_setup` ([Knowledge base plugins](#knowledge-base-plugins)), and replaces or
  removes a plugin named `shelf` only when it carries the mark. A `study-shelf` that has
  moved is then registered where it is now (`re_registered`). Nothing in `setup.json` says
  whose a registration is, and nothing there about Shelf is believed: the record is in
  the state folder, where an agent may be able to write, and the registry is read only
  where it cannot.
- **A plugin the learner registered as `shelf` is left exactly as it is** (`kept`),
  whatever it runs: another program, `study-shelf` with arguments, a URL, or the very
  `study-shelf` setup would register. It has no mark, and that alone makes it the
  learner's. The note says what it runs, and of a URL only the scheme and host, since the
  rest may be a key. Setup looks for it before anything else, so it is `kept` whichever
  `study-shelf` is installed, none and one setup would refuse included. The registry
  decides it again under its own lock, so a plugin the learner registers while setup runs
  is not replaced either, the same command included.
- **Registering `shelf` yourself makes it yours.** `study knowledge-base add shelf`, with
  the command setup registered or with `--replace` and another, takes the registration
  over and drops the mark; setup and `--remove` then leave it alone. A learner who removes
  `shelf` with `study knowledge-base remove` has it back at the next `study setup`, as an
  agent's registration that was removed by hand comes back: a registered plugin does
  nothing until a Topic names it, so there is nothing to opt out of, and setup has no
  record of what the learner once removed.
- `--remove` takes the registration with setup's mark out of the registry, and no other
  (`removed`, or `not_installed` when there is none). It does so also when nothing else of
  setup's is left to undo. Shelf is no agent's, so `--remove --agent A` leaves it
  registered (`kept`, with a note), the last agent's too: a learner who hands Claude Code
  over to the plugin keeps the Knowledge base their Topics name. Shelf's index stays where
  it is ([docs/shelf.md](shelf.md#where-the-index-is)).
- **When `study-shelf` is gone** from beside `study` and from `PATH`, the registration
  setup made is still in the registry. While its program is where it was, as after
  `study` alone moved, Shelf is `current`. When the program is gone, setup reports Shelf
  as `skipped`, and the note says that the registration remains, what it runs, and the
  two ways out: install `study-shelf` again and run `study setup`, or run
  `study setup --remove`, which takes the registration away.
- `--dry-run` says what would be registered and registers nothing. It is the registry's
  own dry run, which looks at everything the registration needs, so it refuses what the
  run would refuse. `--check` adds a `setup:shelf` Finding. It is a warning, with the fix
  `study setup`, when `study-shelf` is installed and not registered, and when the
  registration setup made runs it from where it no longer is; a warning when that
  registration runs a program that is gone and no `study-shelf` is installed, as for an
  agent that runs a `study` that is gone; and a warning with the reason when `study-shelf`
  cannot be registered. It is `ok` when Shelf is registered, when the plugin named `shelf`
  is the learner's own, and when `study-shelf` is not installed and the registry holds no
  registration of setup's. `study doctor` does not repeat in its `setup` Finding what it
  says of the registry or of the plugin itself: a registry it cannot read, and a
  registration whose program is gone, are reported once, as `knowledge_base` and
  `knowledge_base:shelf`.
- Setup starts no plugin, and does not read Shelf's settings file.
  `study knowledge-base check shelf` and `study doctor` start Shelf, and say why when it
  does not start.

```json
"shelf": { "name": "shelf", "status": "registered", "program": "/home/ada/.local/bin/study-shelf",
           "note": "a Topic uses it once it names it: study topic update <topic> --knowledge-base shelf" }
```

### The Claude Code plugin

Instead of `study setup`, Claude Code can install Lamplight from the marketplace in the
Lamplight repository:

```bash
claude plugin marketplace add mordor-forge/lamplight
claude plugin install lamplight@lamplight
```

The marketplace's plugin uses a `command` source, `study claude-plugin-path`, which writes the
plugin from the installed `study` into `$XDG_CACHE_HOME/lamplight/claude-plugin/` and prints
its folder; Claude Code runs it at install and once per session, so the plugin always
matches `study`. The plugin holds the `lamplight` skill, the `study mcp` server by `study`'s
absolute path, and a session-start hook (`study claude-hook session-start`) that prints
`study status` when a session starts inside the Study home, and nothing anywhere else.
`study` must be on the `PATH` of the shell Claude Code runs the command in.

The plugin and `study setup` never both register for Claude Code: `study setup` leaves
Claude Code to an enabled plugin, and undoes what an earlier setup did for it;
`study claude-plugin-path` refuses (`failed_precondition`) while `study setup` provides
Lamplight to Claude Code, so installing the plugin then fails until you run
`study setup --remove --agent claude`.

The plugin registers no Knowledge base plugin. With the plugin alone, Shelf is registered
by `study knowledge-base add shelf -- study-shelf`, or by `study setup`, which leaves
Claude Code to the plugin and also installs the skill where other agents read it.
`study setup --remove --agent claude` leaves Shelf registered.

Setup counts the plugin as enabled when `enabledPlugins` has `lamplight@lamplight` set to
`true` (a plugin of the same name from another marketplace does not count) in Claude Code's
user settings (`~/.claude/settings.json`, or under `$CLAUDE_CONFIG_DIR`), or in the settings
of the project setup runs in: the nearest folder above the working folder, short of `HOME`,
with a `.claude` folder, where `settings.local.json` overrides `settings.json`. A user-scope
registration reaches every project, so setup leaves Claude Code to the plugin when it is
enabled in either place. Setup cannot see other projects' settings, nor managed settings:
enable the plugin there and run `study setup --remove --agent claude` yourself.

The plugin's folder is named after a hash of its content, and is never replaced or
removed: another session, or another version of `study` during an upgrade, may have just
printed it for Claude Code to copy. Concurrent runs of the same `study` all print the same
folder. Folders of earlier versions stay in `$XDG_CACHE_HOME/lamplight/claude-plugin/`,
which can be deleted at any time; only temporary folders left by interrupted runs are
removed, after ten minutes.

### Other agents

Any agent that speaks MCP can run the server. Point it at `study mcp`, by `study`'s absolute
path when the agent is a GUI application:

```json
{
  "mcpServers": {
    "lamplight": { "command": "/usr/bin/study", "args": ["mcp"] }
  }
}
```

Install the skill for your user with `npx skills add -g mordor-forge/lamplight`, or copy
`skills/lamplight/` from the repository into the agent's skills folder. (The
`mordor-forge/lamplight` repository names here and in the plugin assume the repository
rename planned in issue #18.)

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
