# Adopting a Topic from v1

A Topic the learner imported from the v1 study skill with `study import <v1-workspace>`
carries their work, their git history, the Lessons v1's records prove done, and where v1
stopped. It has no Syllabus yet: `status` recommends `adopt` until the adoption gives it
one. Work through the steps in one Session or several; each records what it decided, so
pick up at the first step not done.

The import's report is `imported` on the Topic in `status`: the Lessons proven done with
the proof of each, the open ones, and what was dropped, with `plan` and `config` saying
where v1's plan and config are kept. `notes/v1-plan.md` is v1's plan, when v1 wrote one
(only its project approach did), and `notes/v1-config.json` v1's config. Lessons keep their
v1 ids (`lesson-01`), so their practice folders and the paths in their text still work.

Done when the Syllabus is approved and the learner has a Next step.

## Steps

1. Tell the learner what came over: the Lessons proven done, each with its proof (v1's
   completion commit or its card), the open ones, and what was dropped (v1's flashcards
   among them, to be written again in step 7). Ask whether the Lessons shown done are the
   ones they finished. If one is not, fix it now, before anything else, since a Revision
   cannot drop a done Lesson: the Topic is a copy and the v1 workspace is untouched, so ask
   the learner to run `study topic remove <topic>` (it moves the Topic out of the way into
   `.lamplight/removed`, deleting nothing), then to import again, keeping that Lesson
   open: `study import <v1-workspace> --not-done <lesson-id>`, once per Lesson. Then start
   again here. Done when the learner agrees with the report.
2. The Goal and its deadline. v1's goal became the Topic's Goal; confirm it, ask whether
   there is a deadline, and record changes with `topic_update`. The Level came from v1's
   difficulty (`source` is `import`): confirm it the same way, or let the first Assessment
   set it. Done when the Goal is confirmed.
3. The Pace: hours a week, as periods if it will change. Record it with `topic_update`.
   Done when the Pace is recorded.
4. The Syllabus. From `notes/v1-plan.md`, when there is one: its first tier becomes `must`,
   the second `if_time`, the third `after_deadline`. Without one, start from v1's lesson
   list in `notes/v1-config.json` and the learner's notes in `notes/`, and ask the learner
   which Lessons matter before the deadline. Keep every Lesson the import proved done, with
   its v1 id and title; a Revision that leaves one out is refused. Give each open Lesson an
   hour estimate. Propose it with `revision_propose` and get it approved as
   [The Syllabus](syllabus.md) describes. Done when the Revision is applied and `status` no
   longer recommends `adopt`.
5. Checks for the open Lessons. Each open Lesson's file is `lessons/<lesson-id>.md`; add
   its Check to the YAML header as [The lesson loop](lesson-loop.md) describes, starting
   with the Lesson in progress. Done when the current Lesson has a Check.
6. Sources. Check what `sources` lists, and add what is missing as
   [Knowledge](knowledge.md) describes. Done when the learner agrees the Sources are
   complete.
7. Cards for the completed Lessons. Go over each one with the learner and write a few
   Cards from it with `card_add`, as [Cards and Reviews](cards-and-reviews.md) describes.
   Done when each completed Lesson has Cards, or the learner chose to skip it.
8. The Next step. The import kept where v1 stopped (`v1_next_step` in `status`). Turn it
   into a Next step that starts with a verb, and stop as in SKILL.md step 4; the learner
   carries on from there next time.
