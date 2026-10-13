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

From the first v2.0 release:

```bash
curl -fsSL https://raw.githubusercontent.com/mordor-forge/lamplight/main/install.sh | sh
```

The script downloads the latest release for your machine, checks it against the release's
checksums, puts `study` and `study-shelf` in `~/.local/bin` without sudo, and installs
completions for your shell. Run it again to upgrade. `STUDY_VERSION` and `STUDY_INSTALL_DIR` choose another
release or folder, set for `sh`: `curl ... | STUDY_VERSION=v2.0.0 sh`.

You can also download an archive, a `.deb` or an `.rpm` from the
[releases](https://github.com/mordor-forge/lamplight/releases), or build `study` with Go
1.26 or later:

```bash
go install github.com/mordor-forge/lamplight/v2/cmd/study@latest github.com/mordor-forge/lamplight/v2/cmd/study-shelf@latest
```

Then make Lamplight available to your agents:

```bash
study setup                # installs the skill and registers `study mcp` with Claude Code and Codex
study completion install   # shell completions, if you did not use the install script
study doctor               # checks that everything is in place
```

`study setup` also registers Shelf, the program that indexes and searches your books:
see [A Knowledge base](#a-knowledge-base).

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

## A Knowledge base

A Topic learns from Sources: your books, as PDFs, and web pages. A Knowledge base holds
their text and searches it, so that your agent finds the Passage a Lesson rests on and
quotes it word for word. That quote is Evidence. When the agent records it with the Passage
it is from, Lamplight checks it against that Passage, and what the Passage does not say is
not recorded as its words. A quote recorded without its Passage is kept as not checked,
and `study evidence list` says which is which. Without a Knowledge base the agent reads
the Sources itself, and none of its quotes are checked.

A Knowledge base is a plugin: a program on your computer that `study` starts whenever it
has something to ask it. Lamplight ships with one, Shelf, the `study-shelf` program
installed beside `study`. It needs no account, and keeps its index on your computer.
`study setup` registers it, and then a Topic names it:

```bash
study topic update linear-algebra --knowledge-base shelf
study source add linear-algebra --file ~/Books/strang.pdf
study source index linear-algebra --dry-run   # which files and addresses Shelf would read
study source index linear-algebra
study evidence search linear-algebra "why does elimination work?"
```

Or tell your agent which book to learn from. It adds the Source, runs `study source index`
in its own shell, and searches the book as it teaches. `study status` and `study doctor`
say which Sources are not indexed yet, with the command that indexes them.

Shelf searches by keyword as it is installed. Name an embedding model that you serve
yourself in `~/.config/lamplight/shelf.toml`, and it also finds a Passage by what a
question means; your books' text then goes to that model and nowhere else.
[docs/shelf.md](docs/shelf.md) says how, what Shelf reads, and where it keeps its index.
Another plugin can take Shelf's place:
[docs/knowledge-base-contract.md](docs/knowledge-base-contract.md) is the contract one
speaks, and `study knowledge-base add` registers it.

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
| `study knowledge-base …` | The Knowledge base plugins this computer has |
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
make build      # builds ./study and ./study-shelf
make test       # all tests, with the race detector
make lint       # gofmt and go vet
```

## Design

- [GLOSSARY.md](GLOSSARY.md): the words Lamplight uses, and what they mean.
- [docs/design/lamplight-v2.md](docs/design/lamplight-v2.md): the design.
- [docs/adr](docs/adr): the decisions behind it.
- [docs/shelf.md](docs/shelf.md): Shelf, the `study-shelf` program that indexes and searches
  a Topic's PDFs and web pages on your own computer.
- [docs/knowledge-base-contract.md](docs/knowledge-base-contract.md): the contract a
  Knowledge base plugin speaks, for whoever writes one.
- [AGENTS.md](AGENTS.md): how to work on the code.

## License

MIT: see [LICENSE](LICENSE). What `study` and `study-shelf` are built from, and the licence
of each part, is in [NOTICES](NOTICES/README.md), which every release carries.
