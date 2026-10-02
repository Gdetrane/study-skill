# The Syllabus

A Topic's Syllabus is its Milestones, in order, each a group of Lessons:

- A Milestone ends in an outcome the learner can state ("I can write and test a small CLI
  in Go"). Its priority is `must`, `if_time` (if time allows) or `after_deadline`, and it
  may have a target date (`YYYY-MM-DD`).
- A Lesson has an id (a slug that never changes; `explore` is reserved for flashcards from
  free questions), a title, and an hour estimate aimed at one full-energy sitting.
  Forecasts need the estimates.

## Drafting the first Syllabus

1. Detail Milestone 1 Lesson by Lesson. Outline the later Milestones with their outcomes,
   priorities and a few Lessons each; they fill in as the learner gets there.
2. Order Lessons so each builds on the last.
3. Pitch it at the learner's Level and around what the Assessment found. Shape it by the
   Approach: for `concepts`, each Lesson stands on its own; for `project`, each Lesson
   advances the build; for `challenges`, difficulty rises Lesson by Lesson.
4. Show the learner a short summary, each Milestone's outcome and its Lesson titles, and
   adjust it with them. Then propose it as a Revision (below).

Done when the learner approved it and `syllabus` shows it.

## Revisions

Every change to the Syllabus, the first one included, is a Revision:

1. Call `revision_propose` with a summary in plain words and the whole Syllabus as it would
   be afterwards.
2. Show the learner `changes.text` as it is.
3. Apply it with `revision_apply`, or record a no with `revision_decline`, as Instruction 8
   says.

Done when the Revision is applied or declined. The tool descriptions cover skipping
Lessons, the Lessons a Revision can't rewrite, and adopting the learner's own edits with
`from_file`. When a Forecast misses a deadline, see
[Goal, Pace and Forecasts](planning.md#triage).

## The end of a Milestone

When `lesson_complete` returns `next` with the code `assess_milestone`, or `status`
recommends `assess`, the Milestone it names is finished: run its Assessment, as in
[Starting a Topic](starting-a-topic.md) step 5, over the Milestone's outcome. Record it
with `assessment_record`, kind `milestone` and the Milestone's id. Give `level` only when
the learner agreed a new Level; it replaces any they chose since the last Assessment.
When `next` says `propose_revision`, propose a Revision for the weak areas, such as a
review Lesson. Weak results never block the next Milestone. When the learner stops before
the Assessment, the Next step names the next Lesson's first step: `status` keeps
recommending `assess` on its own, so offer it first at the next Session.

Done when `assessment_record` succeeded, and any Revision it prompted is applied or
declined.
