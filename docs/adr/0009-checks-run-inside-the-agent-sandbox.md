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
`update-index --index-info`, `write-tree`, `commit-tree --no-gpg-sign` and `update-ref`, run
with no inherited `GIT_*` variables, `GIT_CONFIG_GLOBAL=/dev/null`, `GIT_CONFIG_NOSYSTEM=1`,
`core.hooksPath=/dev/null` and `core.fsmonitor=false`. Diffs use `--no-textconv --no-ext-diff`.
The commit author and the learner's global ignore file are read from the learner's git
configuration beforehand and passed explicitly. The guarantee is that the core executes
nothing the configuration names, not that the configuration is trusted.

Building it showed that this list alone is not enough, and closed three more routes:

- Hooks defined in configuration (`hook.<name>.command` with `hook.<name>.event`) still run
  with git 2.55 despite `core.hooksPath=/dev/null`, so every githooks(5) event, and every hook
  the configuration names, is also disabled with `hook.<…>.enabled=false`.
- A repository-local `core.worktree` can point git at files outside the Topic, so `GIT_DIR`
  and `GIT_WORK_TREE` are set explicitly, and a `.git` that is a file, a symlink or a linked
  worktree is refused.
- In a partial clone, fetching a missing object runs the promisor remote's `uploadpack`
  program, so `GIT_NO_LAZY_FETCH=1` and `protocol.allow=never` are set.

`internal/checkpoint` holds the complete list. Its security test arms every known route
(filters, signing, fsmonitor, diff drivers, textconv, hooks in each form, pager, editor,
`core.sshCommand`, `core.worktree`, a promisor remote), takes Checkpoints and Snapshots, checks
that nothing ran, and then checks that plain git does run each one, so it cannot pass
vacuously.

The agent can also change the Topic while a Checkpoint runs, and a review showed it could
redirect one: while the Checkpoint waited for the index lock, the agent swapped `.git` for a
symbolic link to another of the learner's repositories, and the core committed the Topic's
files onto that repository's branch. Checkpoints now pin the repository:

- The Topic folder and its `.git` are opened once and held open. The index lock, the
  temporary index and the replacement of the learner's index all go through the open
  folders, never through their names.
- On Linux, git works on the open folders too: it inherits them as descriptors and gets
  `GIT_DIR=/proc/self/fd/3` and `GIT_WORK_TREE=/proc/self/fd/4`, running from `/`, outside
  the work tree, where git keeps that `GIT_DIR` as given instead of resolving it to a name.
  A test swaps `.git` just before the branch moves and checks the commit still lands in the
  Topic.
- Before each step in which git writes (staging, and moving the branch) the names are
  checked again: the Topic folder and `.git` must still be the open folders, and `.git` must
  have no `commondir` file and no symbolic link for `HEAD`, the refs and logs folders on the
  way to the branch, `objects` or a folder in it. Afterwards the branch is read back through
  the open `.git`. Any difference stops the Checkpoint with nothing committed, and the
  learner's index is put back.

What remains is a race of a few milliseconds between the last check and git's own writes.
On Linux, the agent would have to turn a folder inside `.git`, such as `refs/heads`, into a
symbolic link in that window. On other systems, where git receives names, swapping `.git`
itself in that window is enough; the read-back then reports the Checkpoint as failed, but the
other repository's branch has already moved. Closing the window completely would mean moving
the branch without git, or keeping `.git` out of the agent's reach.

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
