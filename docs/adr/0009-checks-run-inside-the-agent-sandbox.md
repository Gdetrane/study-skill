# 0009. Checks Run Inside the Agent's Sandbox

## Status

Accepted

## Context

A Check runs code that the agent itself wrote: tests, `conftest.py`, Makefiles, package
scripts. The MCP server runs outside the agent's sandbox, so a Check started through MCP
would execute agent-written code with the learner's full permissions and network access,
bypassing the harness's approval prompts. Pinning the command string does not help, because
it does not pin what the command runs.

git can also run programs named by repository configuration: hooks, clean, smudge and process
filters, fsmonitor, textconv and external diff drivers, and signing programs. `.git/config`
lives inside the Topic, where the agent can edit it, so disabling hooks alone does not stop a
Checkpoint's `git add` from running agent-controlled code; a design review reproduced this
with a clean filter.

## Decision

Checks run only through the CLI (`study check <lesson>`), which the agent calls from its own
shell, so the harness's sandbox and approval prompts apply. The CLI records the result in
the History. The MCP server only reads Check results and records rubric grades; it has no
tool that runs learner or agent code.

The core never runs a program that repository configuration names. Checkpoints and snapshot
hashes use low-level git commands that cannot invoke one: `hash-object -w --no-filters`,
`update-index --cacheinfo`, `write-tree`, `commit-tree --no-gpg-sign` and `update-ref`, run
with `GIT_CONFIG_GLOBAL=/dev/null`, `GIT_CONFIG_NOSYSTEM=1`, `core.hooksPath=/dev/null` and
`core.fsmonitor=false`. Diffs use `--no-textconv --no-ext-diff`. The commit author is read from
the learner's git configuration beforehand and passed explicitly. The guarantee is that the
core executes nothing the configuration names, not that the configuration is trusted.

The core also treats agent input as untrusted: Topic files are accessed through `os.Root` so
symlinks cannot escape the Topic, IDs are validated, and child processes never inherit the
MCP server's stdin.

Considered and rejected: running Checkpoints through the sandboxed CLI as well. It is
simpler, but `checkpoint` would stop being an operation both adapters share. Revisit it only
if the low-level route proves leaky.

## Consequences

Agents without a shell cannot run Checks; every harness Lamplight targets has one. Long
Checks, such as cold builds or evaluations that call a model, are not limited by an MCP tool
timeout. The learner can always run a Check themselves with the same command. Checkpoints
store raw bytes, so line-ending conversion and Git LFS are not applied, which is acceptable
for study Topics.
