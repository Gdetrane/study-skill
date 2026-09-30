# 0004. Go Core With CLI and MCP Adapters

## Status

Accepted. Supersedes ADR-0001 and ADR-0002.

## Context

v1 kept its workflow and state rules in Markdown that the agent executed, with a Go FSRS
binary and a Python catalog as helpers. An architecture review found that the workspace
state had no owner (five documents restated it and disagreed), that the hand-written FSRS
deviated from the reference formulas, and that users could not remember the skill's
subcommands. The skill also depended on third-party skills that could go out of date.

## Decision

Build Lamplight as one Go binary, `study`, whose core owns all state and rules. The core is
reached through adapters: a CLI for people and shell-capable agents, an MCP server for any
agent, and later an HTTP API for the dashboard. The `lamplight` skill only teaches and calls
the core; it has no subcommands. Rules that must never drift from the binary are also sent
as the MCP server's instructions. Spaced repetition uses go-fsrs. The core contains no
Python; plugins may be written in any language.

Considered and rejected:

- Keep a Markdown-only skill: nothing would own the state, which is the root of v1's bugs.
- A Python core: a single static binary is easier to install for every harness.
- A Claude Code plugin only: ties the tool to one harness.

## Consequences

Rules that v1 left to Markdown (completing a Lesson, scheduling Cards, forecasting a
Milestone) become tested code. Every harness gets the same behaviour through MCP, and the
skill stays portable. The binary must be installed before the skill can work, so packaging
and `study setup` carry that cost (see ADR-0008).
