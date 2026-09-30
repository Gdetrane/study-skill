# Lamplight v2 design

Lamplight is an interactive tutor for one learner. v2 turns the v1 `study` skill into a Go
program that owns all study state, with a thin skill that teaches through it. Terms in
**bold** are defined in [CONTEXT.md](../../CONTEXT.md). Lasting decisions are recorded in
ADR-0004 to ADR-0009.

This version incorporates two design reviews (architecture, and the learner's journey) and
the maintainer's answers to the questions they raised.

## Principles

- The core owns state and rules; the agent owns judgment and teaching.
- Content lives in plain files; the History records what happened; everything else is
  computed or rebuildable.
- The learner always sees where they are, the Next step, and what "done" means.
- Nothing silently changes the learner's Syllabus: changes are proposed, then approved.
- Show forecasts and one next action, never counts of what is late. No streaks, no guilt.
- Agent input is untrusted: agent-written code runs only inside the agent's sandbox.
- The core has no dependency on any single agent, knowledge service or companion skill.

## Architecture

```
            learner                         agent (any harness)
               │                             │               │
        CLI (study …)               lamplight skill     MCP (study mcp)
               │                   (teaching only)           │
               └──────────────┬──────────────────────────────┘
                              ▼
                     Lamplight core (Go)
   topic · syllabus · lesson · history (event log) · card (go-fsrs) · check
   workbench · forecast · checkpoint (git) · library · knowledge seam
                              │
             plain files in the Study home + rebuildable indexes
```

- **Core**: one Go module of deep modules with small interfaces. The adapters are thin.
- **Adapters**: the CLI and the MCP server expose the same domain operations, except that
  Checks run only through the CLI (ADR-0009). An HTTP API for the dashboard comes in a later
  point release.
- **MCP server instructions**: the rules that must never drift from the binary (write
  tools name their Topic, never edit state files, record a Next step, run Checks through the
  CLI, never show overdue counts) are sent as the server's instructions, so even an outdated
  skill gets them.
- **Skill**: one skill, `lamplight`, which only teaches (see "The skill" below). It writes
  teaching material directly but never edits Lamplight's state files.
- **Knowledge**: a per-Topic seam that returns Evidence (ADR-0007).

## The Study home on disk

```
~/study/                            Study home (STUDY_HOME); not a git repository
  learner.md                        Learner profile
  .lamplight/                       most recent Topic, Library index, caches (rebuildable)
  .templates/<name>/                optional learner-provided Workbench starters
  llm-data-engineering/             a Topic: its own git repository
    topic.toml                      Goal, Pace periods, Level, Approach, Workbench,
                                    Knowledge base, Sources, Tasks
    syllabus.toml                   Milestones (priority, target date) and Lessons
                                    (id, title, hour estimate), in order
    lessons/<lesson-id>.md          Lesson text; YAML header holds the Check and Break points
    cards.jsonl                     Card content, sorted by ID
    history.jsonl                   Events, append only
    learner.md                      optional per-Topic additions to the Learner profile
    notes/                          Session notes, Assessments, research briefs
    teacher/                        Teacher's notes: answer keys, expected scores
    practice/<lesson-id>/           exercise work on the Workbench
    .heldout/<lesson-id>/           Held-out data: synthetic or public only
    .gitattributes                  history.jsonl merges by union
```

- Global config: `$XDG_CONFIG_HOME/lamplight/config.toml`; environment variables such as
  `STUDY_HOME` override it.
- **IDs** are short slugs that never change (`pii-in-cli-logs`, or `lesson-01` for imported
  v1 Lessons). Display numbers ("Lesson 2.3") come from position, so a Revision never
  renames files. Card IDs are `<lesson-id>.<n>`.
- **Status is never stored.** Lesson status, Topic status (active, paused, finished), the
  Resume point, completed Tasks and Card scheduling are computed by replaying the History
  (ADR-0005). go-fsrs is deterministic, so every replay gives the same schedule.
- **Writes** take a per-Topic lock, write the Event first with everything needed to apply
  it, then update content files atomically. On load, the core replays any Event whose effect
  is missing. Every operation is idempotent.
- **Formats**: every file has a `format` number, and a binary refuses to write a file newer
  than it understands. File schemas are Lamplight's own types, never go-fsrs structs. Files
  the core rewrites say in a header comment that comments are not preserved.
- **Sync**: a Topic can be synced between machines with git. History files merge by union,
  and a partly written last line is ignored until it is complete.

## Domain behaviour

### Creating a Topic

Topic creation can stop and resume at any step, because a first session that runs for
hours before any learning happens is exactly what v1 produced.

1. Energy check. At fumes, suggest coming back later.
2. Brainstorm the Goal (deadline optional), Pace periods, Approach and Workbench kind.
3. Add Sources: files from the Library or URLs. Choose the Knowledge base: `notebooklm` or
   `none`.
4. Assessment, time-boxed to about 15 minutes. Areas not reached are marked "confirm during
   Lessons". The result is saved in `notes/` and sets the Level.
5. The agent drafts the Syllabus: Milestone 1 in detail, later Milestones as outlines with
   their outcomes and priorities. The learner approves a short summary.
6. The agent sets up the Workbench with the ecosystem's own tools (`go mod init`,
   `cargo new`, `uv init`, `npm create`, or a folder for written work). It asks before
   installing any toolchain.
7. Session 1 ends at a Break point of Lesson 1, or with a written Next step.

### Syllabus, Forecasts and Revisions

- Syllabus → Milestones → Lessons. Each Milestone has an outcome, a priority (must, if time
  allows, after the deadline) and an optional target date. Each Lesson has an hour estimate
  and cites Evidence where available.
- The **Forecast** projects when each Milestone ends at the current Pace ("at 10 h/week,
  Core ends Oct 16"). If a must-Milestone misses its deadline, the agent offers a **Triage**
  Revision: trim Stretch goals, move Lessons past the deadline, or raise the Pace. A
  Pace change produces a new Forecast.
- **Revisions**: the learner asks in plain words; the agent proposes a before/after change
  that names Lessons by title and shows any renumbering; the core applies it after approval.
  Done and skipped Lessons are never rewritten. Skipping a Lesson in progress offers Cards
  for what was already covered.
- **Approvals are tamper-evident, not tamper-proof.** A Revision stores its exact change and
  the Syllabus version it was based on. Where the client supports MCP elicitation, or on a
  terminal, the core asks the learner directly; the Event records how approval was given.
  `status` flags Syllabus edits made outside Lamplight.
- An **Assessment** at the end of a Milestone never blocks progress; weak results lead to a
  proposed Revision (for example a review Lesson).

### Sessions

1. `status` comes first. It shows the Active topic and why it was chosen, the Resume point
   and its Next step word for word, one recommended action, Cards sized to the Energy
   (never the total due), Forecasts, and any relevant Tasks.
2. Energy check (full, half, fumes) suggests a Focus, and the learner chooses:
   - **Learn**: start the next Lesson.
   - **Practice**: continue the current exercise from the last Break point.
   - **Reviews**: due Cards only, capped by Energy.
   - **Explore**: free questions; useful answers can become Cards or a Revision proposal.
   With nothing due at fumes, the offer is "write tomorrow's first step".
3. The Learner profile and the Topic's additions are read at the start of every Session.
4. A Lesson moves through its Phases: teaching → practicing → feedback. The Check's criteria
   are shown before practicing starts. A Checkpoint is taken at every turn switch, with
   `[agent]` or `[learner]` authorship.
5. Reaching a Break point, or ending a Session, records a Next step (starting with a verb)
   and free-text context. If the learner simply closes the terminal, the next Session sees
   the unclosed Session, shows what changed since the last Checkpoint, and asks for the
   missing note.
6. Completing a Lesson is one idempotent operation: mark it done, save its draft Cards,
   record the Event, take a Checkpoint.
7. After a long gap: a short recap of where the Topic stands and a two-minute warm-up, never
   the size of the backlog.

### Checks

- A Check is a list of criteria, each of one kind:
  - **run**: a command that can be repeated (tests, a build, a script);
  - **held-out**: an evaluation on Held-out data; the first run counts, later runs are
    recorded as "not counted";
  - **rubric**: an item graded by the agent, shown before the work starts; the learner
    checks themselves first.
- Checks run only through `study check <lesson>` in the agent's own shell (ADR-0009).
  Commands are argument lists, not shell strings. The core passes `STUDY_LESSON` and
  `STUDY_HELDOUT_DIR`, and reads scores from a JSON results file, so scores (not only pass or
  fail) are recorded.
- Per-Lesson criteria allow Lessons with special needs (a GPU, a container) and project
  Topics where one codebase grows across Lessons.
- Written work can be submitted as typed final answers or a photo of paper work.

### Cards and Reviews

- Cards are single concepts with a prompt and an expected answer, written from a Lesson or
  an Explore Session, linked to Evidence where possible.
- **Drafts**: a new Card is a draft until its first Review, where the learner keeps, edits or
  drops it. A daily cap limits how many new Cards appear.
- The skill's card-writing rules: one fact per Card, no lists, no answer in the prompt, no
  trivia, at least one Card from the learner's own mistakes.
- Cards can be added, edited, suspended and deleted; `study review` has a key to flag one.
- Reviews work with the agent (conversational recall) or without it (`study review` in the
  terminal). Paused Topics hide their Cards; finished Topics keep reviewing at growing
  intervals.

### Level, Goal, Pace and Tasks

- The Level is set at the Assessment and can be changed by the learner at any time; an
  override holds until the next Assessment.
- v2.0 records every signal a future Level suggestion needs: Checks passed on the first try,
  hints requested, feedback rounds, the gap between dev and Held-out scores, and Review
  results. Suggestions come later, once there is data to tune them.
- Tasks are non-study steps toward the Goal. They appear in `status` when relevant and are
  marked done through the core.

## Knowledge

- **v2.0** (ADR-0007): Knowledge base kind `notebooklm` or `none`. Evidence is an exact quote
  plus a location when known, with where the location came from. Sources are files (by path
  plus content hash) or URLs. The NotebookLM login is checked when a Session opens; Lessons
  without Evidence are marked, never blocked.
- **Later**: Knowledge base plugins are MCP servers implementing Lamplight's fixed contract
  (add a Source, search for Evidence, list Sources). The first is a generic local RAG
  plugin: layout-aware conversion (Docling), hybrid keyword and embedding search, reranking.

## Library

The Python catalog is ported to Go. One normalizer is shared by indexing and searching,
topics match as whole words (fixing the "C" and "algorithms" ranking bugs), and results are
typed. The index is rebuildable data in the Study home; ADR-0003's scanning rules still
apply. Search results carry an absolute path. Conversion leaves the Library.

## Interfaces

### MCP tools

Every write names its Topic. Tools are named after things that happen in the domain.

- **Read**: `status`, `syllabus`, `lesson`, `due_cards`, `history`, `check_results`,
  `library_search`.
- **Topics**: `topic_create`, `topic_update` (Goal, Pace, Level, Approach, Knowledge base,
  Tasks, pause, finish), `task_done`, `assessment_record`, `source_add`, `evidence_record`.
- **Syllabus**: `revision_propose`, `revision_apply`.
- **Sessions**: `session_open`, `session_close`, `phase_set`, `break_point_reached`,
  `checkpoint`, `hint_record`, `rubric_record`, `lesson_complete`.
- **Cards**: `card_add`, `card_edit`, `card_suspend`, `card_delete`, `review_record`
  (including keep, edit or drop for drafts).

Opening a Session on a Topic also makes it the most recent Topic; there is no separate
switch tool.

### CLI

- The same operations, plus `study check`, following the `cli-creator` conventions: nouns
  then verbs, `--json` everywhere (JSON on stdout only, diagnostics on stderr), documented
  success and error shapes, exit 0 on empty results, `--dry-run` on writes, bounded
  `--limit`.
- `study` alone prints `status`. `study review` runs terminal Reviews. `study doctor --json`
  works even when setup is broken.
- Human output via cobra, Charm's fang and lipgloss; colour turns off with `NO_COLOR` or when
  piped. `study completion install` writes bash, zsh and fish completions; packages ship them
  system-wide.

### Wording

Glossary terms are used everywhere, except in learner-facing text for other users, where
"test set", "saved" and "citation" replace Held-out data, Checkpoint and Evidence. A learner
asking to "review my code" means feedback, not Reviews.

### Logging

`log/slog` everywhere; `charmbracelet/log` for terminal output; JSON lines under
`$XDG_STATE_HOME/lamplight/`; level from `--log-level` or `STUDY_LOG`. In MCP mode nothing is
logged to stdout.

## Security

ADR-0009: Checks run only inside the agent's sandbox. The core treats agent input as
untrusted: Topic files are accessed through Go's `os.Root` (Go 1.24+), IDs are validated,
git runs with hooks disabled, and child processes never inherit the MCP server's stdin.
Sources and caches stay local, except what the learner sends to NotebookLM.

## Checkpoints

Each Checkpoint is a git commit made by the core. It skips empty commits, refuses to commit
during a merge, a rebase or on a detached HEAD, retries when an editor holds git's lock
(`GIT_OPTIONAL_LOCKS=0`), and warns before committing large files. The default `.gitignore`
covers data and model artefacts (Parquet, DuckDB, GGUF, safetensors, PyTorch checkpoints).

## Distribution and setup

ADR-0008. `study` ships for Linux and macOS through a Homebrew cask tap, the AUR, deb and
rpm packages, and `go install`. `study setup` installs the `lamplight` skill and registers
the MCP server at user scope for Claude Code and Codex, never overwrites a folder it did not
create, and can be reversed exactly with `--remove`. Claude Code can instead use the
marketplace plugin, which includes a session-start hook that prints `status` when the agent
starts inside the Study home. Other agents use `npx skills add` and a documented MCP snippet.

## Migration from v1

v1 stays installed as the `study` skill and keeps working. The LLM data engineering Topic
stays on v1 until after 13 October. Until the learner switches, v2's skill is installed only
for testing, against a separate Study home, so "let's study" keeps reaching v1.

1. `study import <v1-dir> [--dry-run]` copies the workspace, git history included, into the
   Study home and leaves the original untouched. It keeps v1 folder and Lesson names as IDs
   (`lesson-01`), so paths quoted in Lesson text and in `.gitignore` keep working. It moves
   `lessons/plan.md` to `notes/v1-plan.md`, converts `.study-config.json` into `topic.toml`,
   and maps `sources` and `notebooklm` to the Knowledge base. It records one `imported` Event
   plus the Lesson completions it can prove. v1's lesson-level cards are dropped.
   `--dry-run` lists everything that will be dropped.
2. An adoption Session works through a checklist: Goal and deadline, Pace periods, Syllabus
   from `notes/v1-plan.md` (the three tiers become three Milestone priorities), a Check for
   each open Lesson, the Knowledge base, Cards for completed Lessons, and the Next step from
   v1's `pending_action` and `context`. The learner approves the result as a Revision.
3. Acceptance test: `~/study-workspaces/c` and `~/study-workspaces/llm-data-engineering`
   import and resume exactly where they stopped. Automated tests use sanitised copies,
   because the real workspaces contain work-related content.

## The skill

`skills/lamplight/` holds the teaching method, with separate references for: brainstorming
and Assessment, drafting a Syllabus, the lesson loop and feedback, writing Cards, and Reviews.
It carries v1's teaching material over explicitly:

- teaching rules, including "never write the learner's implementation";
- the Lesson template: Concept, Key points, Reference example (don't copy), Common pitfalls,
  Exercise, Break points, Check, Stretch goals;
- numbered menus after a Lesson and after feedback;
- guidance for writing at each Level;
- the time budget question at the start of a Session;
- KaTeX for maths and physics;
- optional companions (visual explainers, domain packs, live-docs tools) suggested when
  installed, never required.

## Repository

The repository is renamed to Lamplight; v1 is tagged and kept on a `v1` branch; v2 is
written on main.

```
cmd/study/                  entry point
internal/…                  core modules, and the CLI and MCP adapters
skills/lamplight/           the skill and its references (embedded in the binary)
plugin/                     Claude Code plugin manifest and hooks
.claude-plugin/             marketplace.json
docs/adr/, docs/design/, CONTEXT.md
web/                        dashboard, in a later point release
```

`templates/`, `scripts/fsrs` and `scripts/catalog` are removed.

## Testing

- Tests go through each module's interface. Core tests run in process with a fixed clock
  and a temporary Study home.
- Replay: the same History always yields the same state and Card schedule.
- Crash injection: interrupt every write between the Event and the content update, then
  check that replay repairs it.
- Concurrency: the CLI and the MCP server writing to one Topic at once.
- Sync: two machines' History files merged by union replay to one consistent state.
- The MCP server through the Go SDK's in-memory transport; the CLI by comparing `--json`
  output with saved expected files.
- `study setup` against fake `claude` and `codex` executables.
- One end-to-end test drives the real binary through a whole Lesson.
- The import against sanitised copies of the two v1 workspaces.

## Scope

**v2.0**: the core (Topics, Syllabus with Forecasts, Triage and Revisions, Lessons with
Break points and Next steps, Checks, Cards with drafts and Reviews, History with replay,
Checkpoints, Assessment, Goal, Pace and Tasks, recording Level signals); the Library in Go;
the CLI with terminal Reviews; the MCP server; the `lamplight` skill; the Claude Code plugin
and `study setup` for Claude Code and Codex; Linux and macOS packages; `study import`.

**Later**: the dashboard (Vue 3, TypeScript, shadcn-vue; home network with a login, or
Tailscale; read-mostly first), Knowledge base plugins and the generic RAG plugin, Level
suggestions, Windows packages, the Agent Plugins 1.0 manifest, setup for more agents, reading
tables of contents from PDFs, retrieval from page images, the Journal, the FSRS optimizer,
publishing to the MCP Registry.

## Known risks

- NotebookLM access is unofficial and can break without notice.
- Approvals are tamper-evident only: an agent with a shell can still edit files.
- Agents that read both `~/.agents/skills` and `~/.claude/skills` may list the skill twice.
- The session-start `status` is automatic only where the agent supports hooks.
- Comments in files the core rewrites are lost.
