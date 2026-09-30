# 0009. Checks Run Inside the Agent's Sandbox

## Status

Accepted

## Context

A Check runs code that the agent itself wrote: tests, `conftest.py`, Makefiles, package
scripts. The MCP server runs outside the agent's sandbox, so a Check started through MCP
would execute agent-written code with the learner's full permissions and network access,
bypassing the harness's approval prompts. Pinning the command string does not help, because
it does not pin what the command runs.

## Decision

Checks run only through the CLI (`study check <lesson>`), which the agent calls from its own
shell, so the harness's sandbox and approval prompts apply. The CLI records the result in
the History. The MCP server only reads Check results and records rubric grades; it has no
tool that runs learner or agent code.

The core also treats agent input as untrusted: Topic files are accessed through `os.Root`
so symlinks cannot escape the Topic, IDs are validated, git runs with hooks disabled, and
child processes never inherit the MCP server's stdin.

## Consequences

Agents without a shell cannot run Checks; every harness Lamplight targets has one. Long
Checks, such as cold builds or evaluations that call a model, are not limited by an MCP tool
timeout. The learner can always run a Check themselves with the same command.
