# Agent Guide

## Project Overview

This branch holds Lamplight v2: a Go program, `study`, whose core owns all study state
(Topics, the Syllabus, Cards, the History) in plain files inside the learner's Study home.
A command line and an MCP server are thin adapters over the core, and a small `lamplight`
skill teaches agents to use them. v1, the Markdown study skill, stays on `main` until v2
is released.

Read these before changing behaviour:

- `CONTEXT.md`: the glossary. Use its terms in code, comments and docs.
- `docs/design/lamplight-v2.md`: the design.
- `docs/adr/`: the decisions behind it (0004 onwards; 0001 and 0002 are superseded).
- `docs/cli.md`: the command-line contract that scripts and agents rely on.

## Development Commands

Use the root `Makefile` as the stable entrypoint:

```bash
make test         # go test -race ./...
make lint         # gofmt check and go vet
make coverage     # tests with a coverage profile
make build        # build ./study
```

Narrow checks:

```bash
go test ./internal/core -run TestCreateTopic
go test ./internal/cli -run TestJSONOutput
go test ./internal/cli -update      # rewrite CLI golden files, then review the diff
```

## Code Conventions

- Go 1.25 or later; the module is `github.com/mordor-forge/lamplight/v2`.
- Domain logic lives in `internal/core`. `internal/cli` and `internal/mcpserver` only
  parse input, call the core and render results.
- Core errors are `*core.Error` with a documented code (`invalid_argument`, `corrupt`,
  …); adapters map codes to exit codes and MCP tool errors.
- Files in a Topic are read and written through `os.Root`; git runs through
  `gitCommand`, never with the caller's `GIT_*` environment.
- Every file Lamplight owns carries a `format` number, and newer formats are refused.
- Tests go through each package's public interface, with a fixed clock, injected IDs and
  a temporary Study home. CLI output is checked with golden files under `testdata/`.
- Use conventional commit prefixes such as `fix:`, `feat:`, `docs:`, `test:` and `ci:`.
- Do not commit built binaries, Study homes or agent assessment reports.

## Important Paths

- `cmd/study`: the entry point.
- `internal/core`: Topics, status, the History and the Library, behind one `Core`.
- `internal/library`: building and searching the Library index.
- `internal/cli`: the `study` command line (cobra and fang).
- `internal/mcpserver`: the MCP server, its tools and its instructions for agents.

## Patterns

- New operation: add it to the core with tests, then expose it in the CLI (with `--json`,
  and `--dry-run` for writes) and the MCP server, and document it in `docs/cli.md`.
- New file or Event format: bump nothing silently; add a `format` field and a test that
  newer formats are refused.
- Design changes: update `CONTEXT.md`, the design and a new ADR together.
