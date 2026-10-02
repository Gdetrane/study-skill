# Knowledge

## Sources

A **Source** is a document or web page the Topic learns from. Find books in the learner's
Library with `library_search` (it returns absolute paths), and add files or URLs with
`source_add`. List them with `sources`.

## The Knowledge base

A Topic has at most one Knowledge base, chosen with `topic_update`:

- `notebooklm`: the Sources live in a NotebookLM notebook, and you search them through the
  NotebookLM MCP server when it is installed. At the start of a Session, make one cheap
  call to it; when it fails, tell the learner they may need to log in to NotebookLM again.
- `none`: read the Sources yourself.

When the NotebookLM server is not installed, read the Sources yourself as with `none`.

## Evidence

**Evidence** is an exact quote from a Source that a Lesson relies on; the learner hears "a
citation". Record it with `evidence_record` as you write each Lesson:

- Quote the Source's own words, never a summary or NotebookLM's answer.
- Give the location when you know it, and where it came from in `location_from`:
  `source` (you read it in the Source), `knowledge_base` (the citation as NotebookLM gave
  it; its citations carry no page numbers), `learner` (the learner told you) or
  `estimate`.
- Take back a mistaken quote with `evidence_retract`.

Lessons that cite no Evidence show in `status` as `lessons_without_evidence`. Teaching goes
on; add the Evidence when you can.
