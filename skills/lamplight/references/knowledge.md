# Knowledge

## Sources

A Source is a document or web page the Topic learns from. Find books in the learner's
Library with `library_search`, add files or web pages with `source_add`, and list them with
`sources`.

## The Knowledge base

A Topic has at most one Knowledge base: where its Sources are held and searched for
Evidence. It is one of two kinds:

- `none`: you read the Sources yourself, files at the path `sources` gives and web pages at
  their URL. A Topic without a Knowledge base yet behaves the same, and so does one whose
  kind this version does not know, which `sources` shows as it is recorded.
- `plugin`: the Topic names a Knowledge base plugin, a program or a service the learner
  registered on their computer under that name, which indexes the Sources. Name one with
  `topic_update` when the learner asks for it, by the name `study knowledge-base list`
  shows. Registering, changing and removing a plugin are the learner's to do
  (Instruction 14): when none is registered, say so and leave the Topic as it is.

## Indexing

With a plugin, `status` and `sources` say whether each Source is `indexed`, `indexing`,
`failed`, `not_indexed` or `unknown`, and `indexing` gives the one `action` to take. No tool
indexes, and adding a Source indexes nothing.

- The `action` is `study source index <topic>`: run it in your own shell, as you run
  `study check <lesson-id>`, and tell the learner what it printed. A book takes minutes. When
  there is teaching to do, offer the learner to run it in a terminal of their own instead,
  and go on: a Lesson never waits for it. Stopping the command is safe, and running it again
  carries on.
- The command says the plugin's folder cannot be made or written in: your shell is
  sandboxed, and the folder is outside the Study home. Tell the learner the two ways out the
  message gives: allow that folder in your sandbox, or run the command in a shell of their
  own. Do not look for another way to index.
- `indexing` has a `problem`: the plugin is missing on this computer, does not start or
  did not answer in time, and the Topic works as one without a Knowledge base. Tell the
  learner the problem and its `action` once, then read the Sources yourself.
- A Source `failed`: tell the learner the reason the plugin gave, such as a scan without
  text, and read that Source yourself. It is no `action`, and stays `failed` until it is
  indexed again: run `study source index <topic> <source>` for it only once what the reason
  names has been put right.
- A Source the command skipped has a file that is missing or has changed since it was
  added: the output says which, and what to do.

## Evidence

Evidence is the exact quote from a Source that a Lesson relies on; the learner hears "a
citation". Record it with `evidence_record` while you write each Lesson, as its description
says, and take back a mistake with `evidence_retract`. Lessons without Evidence show in
`status` as `lessons_without_evidence`; teaching goes on, and you add the Evidence when you
can.
