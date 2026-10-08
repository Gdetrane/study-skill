# 0011. Lamplight Does Not Use NotebookLM

## Status

Accepted. Replaces the v2.0 part of ADR-0007, which made `notebooklm` a Knowledge base kind;
the per-Topic seam and Evidence of ADR-0007 stand.

## Context

ADR-0007 gave v2.0 two Knowledge base kinds. With `notebooklm`, the agent queried a
NotebookLM notebook through a community MCP server, and the core recorded the notebook and
each Source's id in it. With `none`, the agent reads the Sources itself.

NotebookLM has no consumer API. The community MCP server reaches it through undocumented
endpoints, signed in with the session cookies of the learner's browser. That is unofficial
access, which Google neither documents nor supports and which may not be allowed by its
terms, so the risk of it falls on the learner's Google account. Lamplight should not
recommend that route, and should not wire it into its data.

A second, lesser point: its citations carried no page numbers, so Evidence from it never
had a location the learner could open the book at.

v2.0 has not been released, so no release carries the kind or its fields.

## Decision

Lamplight does not use NotebookLM, and nothing in it names a way to reach it.

- These are removed: the Knowledge base kind `notebooklm` and the notebook's id in
  `topic.toml`; a Source's ids in a notebook (`notebooklm_id`, `notebooklm_notebook`, and
  `notebooklm_stale` in `study source list`); the flags `--notebook` and `--notebooklm-id`,
  and the same inputs of the MCP tools; the NotebookLM login check when a Session opens.
- The seam of ADR-0007 stands. A Topic has at most one Knowledge base, kept in the
  `[knowledge_base]` table of `topic.toml` and set by a `knowledge_base.set` Event. Evidence
  is an exact quote, with a location when one is known and where the location came from,
  `knowledge_base` among them. Until another kind exists, `none` is the only one.
- `study import` no longer maps v1's `notebooklm` setting, or a source's ids in a notebook.
  It lists each under `dropped` with this reason, imports the Sources themselves, and
  chooses no Knowledge base.
- Topics written by earlier builds of v2 still load. There is no migration and no new Event
  type, because no release carried these payloads:
  - A kind in `topic.toml` that this version does not know, this one or one a newer version
    adds, is shown as recorded and treated as `none`. It cannot be chosen, and choosing
    `none` replaces its table.
  - NotebookLM ids in `source.added` and `source.updated` Events are ignored when the
    History is replayed, so a `source.updated` that changed only such an id changes nothing.
  - The same fields in a line of `sources.jsonl` are kept as a newer version's fields are:
    they stay in the line and are never shown.
  - A write an earlier build left interrupted is finished by this version. A Source's line
    is written as the Event recorded it, those fields included, when the Source in the
    Event's payload is the version the Event recorded; the next change to that Source then
    raises no conflict. It is not that version when the line also held a field the earlier
    build did not know, such as one added by hand. Recovery then writes the fields this
    version knows over the line, and if the Event changed or removed only an id in a
    notebook, the next change to that Source is flagged as a conflict, which the learner
    dismisses; nothing is lost. A move from one notebook to another leaves the table with
    the kind alone, at a version of its own, so the next change to `topic.toml` raises no
    conflict.

## Consequences

With `none`, the agent has no way to search a Topic's Sources: it reads them itself, which
is slow in a long book. So v2.0 is not released until Lamplight has a Knowledge base that
needs no account. That makes the "Later" part of ADR-0007, Knowledge base plugins with a
local one first, part of v2.0. Its own ADR decides it.

A learner who used NotebookLM with v1 keeps their notebook, which Lamplight never touched,
and has the same files and web pages as Sources after the import. v1, on `main`, is
unchanged.

Ignoring fields of an Event type without renaming the type is an exception to the rule that
a payload never changes shape within a format. It holds only because v2.0 was never
released: after the release, a changed payload is a new type.
