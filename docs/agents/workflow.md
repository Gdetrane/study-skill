# Working with Matt's engineering skills

Use the smallest workflow that fits the task. Routine fixes can go directly to
implementation and affected checks; the spec/ticket flow is for work that benefits
from a reusable plan across sessions.

For project design: `grill-with-docs` records settled terms and decisions, then
`to-spec` and `to-tickets` produce reviewable work in the configured issue tracker.
Start with one ticket through `implement`; use `implement-spec` only when the
approved ticket graph has useful parallel work and the user has authorized its
branch, commit and integration actions.

For close-out: use Matt's `code-review` against an explicit base, `pr` for the PR
body, and invoke `retro` after a difficult session to improve the environment.
Mechanical recurring mistakes belong in existing checks; judgement belongs in
review guidance. Confirm a guardrail is missing or broken before adding one.

In Claude Code, use qualified commands such as
`/mattpocock-skills:code-review`, `/mattpocock-skills:retro` and
`/mattpocock-skills:grill-with-docs` to select Matt's plugin. In Codex, explicitly
select the corresponding installed skill (for example `$code-review`).
User-invoked phases stay under the user's control. The `pr` skill is also
available to the model when it writes a PR body; `.github/pull_request_template.md`
provides the same structure to human authors.

The PR evidence must demonstrate the affected behavior. Use screenshots or a
recorded interaction for visual changes and executed checks for logic/integration
changes. Record what was and was not verified. A build alone does not verify a UI.

For the tutoring product, use Lamplight's own skill and domain model to manage
learning state; engineering `grilling` is for project decisions.
