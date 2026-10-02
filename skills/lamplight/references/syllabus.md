# The Syllabus

A Topic's Syllabus is its Milestones, in order, each a group of Lessons:

- A **Milestone** ends in an outcome the learner can state ("I can write and test a small
  CLI in Go"). Its priority is `must`, `if_time` (if time allows) or `after_deadline`, and
  it may have a target date (`YYYY-MM-DD`).
- A **Lesson** has an id (a slug that never changes; `explore` is reserved), a title, and an
  hour estimate aimed at one full-energy sitting. Forecasts need the estimates.

## Drafting the first Syllabus

1. Detail Milestone 1 Lesson by Lesson. Outline the later Milestones with their outcomes
   and priorities, and a few Lessons each; they fill in as the learner gets there.
2. Order Lessons so each one builds on the last. For a project, each Lesson advances the
   build; for challenges, difficulty rises Lesson by Lesson.
3. Pitch it at the learner's Level and around what the Assessment found
   ([Starting a Topic](starting-a-topic.md)).
4. Show the learner a short summary: each Milestone's outcome and its Lesson titles.
   Adjust it with them, then propose it (next section).

Done when the learner approved it and `syllabus` shows it.

## Revisions

Every change to the Syllabus, the first one included, is a Revision the learner approves:

1. The learner asks in plain words, or you suggest a change and they agree to look at it.
2. Call `revision_propose` with a summary in plain words and the whole Syllabus as it
   would be afterwards.
3. Show the learner `changes.text` as it is. Lamplight computes it from the two versions:
   it names Lessons by title and shows any renumbering.
4. Call `revision_apply`. When your client can ask the learner directly, Lamplight shows
   them the change and records their answer. Otherwise, ask them yourself, then call it
   with their words. When they say no, record it with `revision_decline` and their words.

Done when the Revision is applied or declined.

### What a Revision can change

- **Skipping**: mark a Lesson `skipped`; it keeps its place and number, and a later
  Revision can take the skip back. For each Lesson in `changes.skipped_in_progress`, go
  over what was already covered and offer to keep it as Cards with `card_add`.
- **Done and skipped Lessons** keep their title, hours and Milestone.
- **Hand edits**: when `status` flags `syllabus.toml` as edited outside Lamplight, propose
  with `from_file` to adopt the learner's edit as it stands.
- **After a weak Assessment** at the end of a Milestone, propose a Revision that helps,
  such as a review Lesson. Progress is never blocked.
- **Triage**: when a Forecast misses a deadline, see
  [Goal, Pace and Forecasts](planning.md#triage).
