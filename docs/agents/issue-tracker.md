# Issue tracker: GitHub

Specs and tickets live in **mordor-forge/study-skill**. Always pass `--repo mordor-forge/study-skill` to `gh`;
`origin` is a personal fork, while `upstream` is the project tracker.

## Operations

- Read: `gh issue view NUMBER --repo mordor-forge/study-skill --comments`.
- List: `gh issue list --repo mordor-forge/study-skill --state open --json number,title,labels,assignees`.
- Publish: `gh issue create --repo mordor-forge/study-skill --title TITLE --body-file BODY.md --label ready-for-agent`.
- Comment: `gh issue comment NUMBER --repo mordor-forge/study-skill --body-file BODY.md`.
- Label: `gh issue edit NUMBER --repo mordor-forge/study-skill --add-label LABEL` (or `--remove-label`).
- Close: `gh issue close NUMBER --repo mordor-forge/study-skill` after its acceptance criteria and integration are verified.

Draft specs and one-file-per-ticket drafts may live under `.scratch/<feature>/`
while being reviewed. They are unpublished drafts, not a second tracker. Record
GitHub URLs in them once published and use GitHub for subsequent status.

## Dependencies and close-out

Publish blockers first. Link tickets to the spec using GitHub sub-issues, and
use native issue dependencies for blocking edges. Fetch a blocker's database ID
with `gh api repos/mordor-forge/study-skill/issues/NUMBER --jq .id`, then add it with
`gh api --method POST repos/mordor-forge/study-skill/issues/CHILD/dependencies/blocked_by -F issue_id=ID`.
If the repository cannot support a native relationship, record `Parent: #N` and
`Blocked by: #N` in the ticket body. A ticket is ready when all its blockers are
closed and it is unassigned.

A pull request should reference the spec and tickets it implements. Close work
when the agreed integration branch has passed review and checks; GitHub closing
keywords may wait for the default branch, so confirm issue state explicitly.
Publishing issues does not grant permission to commit, push, merge, deploy, or
close unfinished work. Keep those actions within the user's authorization.

## Pull requests as a triage surface

**PRs as a request surface: no.**

Lamplight v2 work targets `v2`; `main` remains v1 until the release. Select the
PR base deliberately rather than relying on GitHub's default branch.

## Wayfinding operations

Used by `wayfinder`. The map is one issue; its tickets are that issue's sub-issues.

- Labels: `wayfinder:map` on the map, and `wayfinder:research`, `wayfinder:prototype`, `wayfinder:grilling` or `wayfinder:task` on each ticket. Create any the tracker lacks with `gh label create NAME --repo mordor-forge/study-skill`.
- Map: `gh issue create --repo mordor-forge/study-skill --title TITLE --body-file BODY.md --label wayfinder:map`.
- Child ticket: create it with its `wayfinder:<type>` label, then link it with `gh api --method POST repos/mordor-forge/study-skill/issues/MAP/sub_issues -F sub_issue_id=ID`, where `ID` is the child's database ID, fetched as for a blocker.
- Blocking: native issue dependencies, as in "Dependencies and close-out".
- Frontier: the map's open sub-issues (`gh api repos/mordor-forge/study-skill/issues/MAP/sub_issues`) that have no assignee and no open blocker (`issue_dependencies_summary.blocked_by` is 0). The first in map order wins.
- Claim: `gh issue edit NUMBER --repo mordor-forge/study-skill --add-assignee @me`, as the session's first write.
- Resolve: comment the answer, close the ticket, then add its gist and link to the map's Decisions-so-far.
