# Knowledge

## Sources

A Source is a document or web page the Topic learns from. Find books in the learner's
Library with `library_search`, add files or web pages with `source_add`, and list them with
`sources`.

## The Knowledge base

A Topic has at most one Knowledge base: where its Sources are held and searched for
Evidence. This version has one kind, `none`: you read the Sources yourself, files at the
path `sources` gives and web pages at their URL. A Topic without a Knowledge base yet
behaves the same, and so does one whose kind this version does not know, which `sources`
shows as it is recorded.

## Evidence

Evidence is the exact quote from a Source that a Lesson relies on; the learner hears "a
citation". Record it with `evidence_record` while you write each Lesson, as its description
says, and take back a mistake with `evidence_retract`. Lessons without Evidence show in
`status` as `lessons_without_evidence`; teaching goes on, and you add the Evidence when you
can.
