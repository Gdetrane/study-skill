# Lamplight

> **Work in progress.** This is the `v2` branch, where Lamplight is being built. The
> current study skill (v1) lives on [`main`](https://github.com/mordor-forge/study-skill/tree/main)
> until Lamplight is released.

Lamplight is a study companion for learning with an AI agent. It always knows where you
are in a subject and what to do next, so you can stop at any point and pick up again
later, from any agent.

- **`study`**, one Go program, keeps your Topics, Syllabus, review Cards and History as
  plain files in your Study home. Each Topic is a git repository you own.
- **A command line** for you and for agents with a shell, with JSON output for scripts.
- **An MCP server** (`study mcp`) for agents such as Claude Code and Codex.
- **A `lamplight` skill** that teaches agents how to tutor with it.

## Status

Available on this branch so far:

```bash
study                                     # where you are: the Active topic and every Topic
study topic create --title "Linear algebra"
study mcp                                 # the MCP server, over stdin and stdout
```

See [docs/cli.md](docs/cli.md) for the full command-line contract, and the
[v2.0 milestone](https://github.com/mordor-forge/study-skill/milestone/1) for what is
still to come.

## Build

Lamplight needs Go 1.25 or later, and git.

```bash
make build      # builds ./study
make test
make lint
```

## Design

- [CONTEXT.md](CONTEXT.md): the words Lamplight uses, and what they mean.
- [docs/design/lamplight-v2.md](docs/design/lamplight-v2.md): the design.
- [docs/adr](docs/adr): the decisions behind it.

## License

MIT
