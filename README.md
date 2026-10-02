# Lamplight

> Until v2.0 is released, this is the `v2` branch, and
> [`main`](https://github.com/mordor-forge/lamplight/tree/main) still holds v1, the
> Markdown study skill.

Lamplight is a study companion for learning with an AI agent. It always knows where you
are in a subject and what to do next, so you can stop at any point and pick up again later,
from any agent, on any of your computers.

- **`study`**, one Go program, keeps your Topics, Syllabus, Checks, review Cards and History
  as plain files in your Study home. Each Topic is a git repository you own.
- **An MCP server** (`study mcp`) lets agents such as Claude Code and Codex teach with it.
- **The `lamplight` skill**, built into `study`, teaches the agent how to tutor: it explains,
  you practise, your own work is checked, and nothing is ever written for you.
- **A command line** for you and for agents with a shell, with JSON output for scripts.

No streaks, no overdue counts: Lamplight shows where you are, one next action, and
Forecasts when you have a deadline.

## Install

Lamplight runs on Linux and macOS and needs git.

From the first v2.0 release, install a package:

```bash
brew install --cask mordor-forge/tap/lamplight   # macOS and Linux, Homebrew
yay -S lamplight-bin                            # Arch Linux (AUR)
```

or download a `.deb`, `.rpm` or archive from the
[releases](https://github.com/mordor-forge/lamplight/releases). With Go 1.26 or later:

```bash
go install github.com/mordor-forge/lamplight/v2/cmd/study@latest
```

Then make Lamplight available to your agents and your shell:

```bash
study setup                # installs the skill and registers `study mcp` with Claude Code and Codex
study completion install   # shell completions for bash, zsh or fish
study doctor               # checks that everything is in place
```

`study setup --check` finds a stale install, and `study setup --remove` undoes exactly what
setup did. Claude Code users can install the plugin instead; other agents can run
`study mcp` directly. See [Setting up agents](docs/cli.md#setting-up-agents).

(The repository is being renamed from `study-skill` to `lamplight`; the addresses above are
the new ones.)

## Quick start

Open your agent in any folder and say what you want to learn:

> Let's study linear algebra. I want to solve systems by hand before my exam in December.

The agent creates a Topic, asks how much time and energy you have, drafts a Syllabus with
you, and starts the first Lesson. Next time, it opens with where you stopped.

Your Study home is `~/study` unless `STUDY_HOME` or `study_home` in
`~/.config/lamplight/config.toml` says otherwise:

```
~/study/
  linear-algebra/            a Topic: its own git repository
    topic.toml               Goal, Pace, Level, Knowledge base
    syllabus.toml            Milestones and Lessons, changed only by approved Revisions
    lessons/                 Lesson notes, each with its Check
    practice/                your work
    cards.jsonl              review Cards
    history.jsonl            everything that happened
```

## Commands

Most of the time you talk to your agent. The command line covers the same ground:

| Command | What it does |
|---|---|
| `study` | Where you are: the Active topic, the Next step and one recommended action |
| `study topic create\|update\|remove` | Start a Topic, change it, or move it out of the Study home |
| `study session open\|close` | Start a Session, or stop with a Next step |
| `study syllabus`, `study revision …` | See the Syllabus; propose, approve or decline a change |
| `study check <lesson>` | Run a Lesson's Check on your work (in your own shell) |
| `study review` | Review your Cards in the terminal |
| `study card …`, `study task …` | Manage Cards and Tasks |
| `study source …`, `study evidence …`, `study library …` | Sources, citations, and your book Library |
| `study history`, `study lesson <lesson>` | What happened, and one Lesson's details |
| `study import <v1-workspace>` | Bring a v1 workspace over |
| `study setup`, `study doctor`, `study completion …` | Installation and checks |
| `study mcp` | The MCP server for agents |

[docs/cli.md](docs/cli.md) is the full contract: every command, its `--json` output, error
codes and exit codes.

## Coming from v1

v1 workspaces keep working with v1 until you move them. To bring one over:

```bash
study import ~/path/to/workspace --dry-run   # shows what will be copied, converted and left out
study import ~/path/to/workspace
```

The workspace is copied with its whole git history and never changed. Lessons count as done
only when v1's records prove it; `--not-done lesson-NN` keeps one open. Then ask your agent
to adopt the imported Topic: together you set the Goal and Pace and turn v1's plan into a
Syllabus. See [Importing a v1 workspace](docs/cli.md#importing-a-v1-workspace).

## Build from source

```bash
make build      # builds ./study
make test       # all tests, with the race detector
make lint       # gofmt and go vet
```

## Design

- [CONTEXT.md](CONTEXT.md): the words Lamplight uses, and what they mean.
- [docs/design/lamplight-v2.md](docs/design/lamplight-v2.md): the design.
- [docs/adr](docs/adr): the decisions behind it.
- [AGENTS.md](AGENTS.md): how to work on the code.

## License

MIT
