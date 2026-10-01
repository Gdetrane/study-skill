# 0008. Distribution and Agent Setup

## Status

Accepted

## Context

Lamplight has to reach users of several agent harnesses, each with its own plugin format and
configuration files. The skill's instructions and the MCP tools must never drift apart. v1
is installed at `~/.agents/skills/study` and must keep working while v2 is adopted.

## Decision

The contract is that `study` is on the PATH. For v2.0, GoReleaser publishes Linux and macOS
builds as a Homebrew cask in our own tap, an AUR `-bin` package, and deb and rpm packages;
`go install` also works. Packages ship shell completions and man pages.

The skill is named `lamplight` and is embedded in the binary. `study setup`:

- installs the skill into `~/.agents/skills/lamplight`, linked from
  `~/.claude/skills/lamplight`, and never overwrites a folder it did not create;
- registers the MCP server at user scope with each agent's own command (`claude mcp add
  --scope user`, `codex mcp add`), using the stable absolute path of `study`, because GUI
  editors do not inherit the shell's PATH;
- records everything it wrote, so `--remove` is exact and `--check` finds stale installs.

`study mcp` refreshes a stale copy of the skill when it starts. The repository also hosts a
Claude Code marketplace whose plugin uses a `command` source (`study claude-plugin-path`),
so the plugin always matches the installed binary. The plugin and `study setup` are mutually
exclusive for Claude Code: `setup` skips anything the plugin already provides.

Claude Code and Codex are fully supported in v2.0. Other agents install the skill with
`npx skills add` and follow a documented MCP configuration snippet.

## Consequences

This deliberately differs from Railway and flyctl, which write every configuration file
directly: each agent's own command owns its format, so agent upgrades break us less. Windows
packages, the Agent Plugins 1.0 manifest, setup for further agents, and publishing to the
official MCP Registry come after v2.0.
