# Knowledge

## Sources

A Source is a document or web page the Topic learns from. Find books in the learner's
Library with `library_search`, add files or web pages with `source_add`, and list them with
`sources`.

## The Knowledge base

A Topic has at most one Knowledge base, chosen with `topic_update`:

- `notebooklm`: the Sources live in a NotebookLM notebook, searched through the NotebookLM
  MCP server when it is installed in your agent;
- `none`: you read the Sources yourself.

When the NotebookLM server is not installed, read the Sources yourself, as with `none`.

## Evidence

Evidence is the exact quote from a Source that a Lesson relies on; the learner hears "a
citation". Record it with `evidence_record` while you write each Lesson, as its description
says, and take back a mistake with `evidence_retract`. Lessons without Evidence show in
`status` as `lessons_without_evidence`; teaching goes on, and you add the Evidence when you
can.
