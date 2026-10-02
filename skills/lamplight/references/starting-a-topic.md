# Starting a Topic

A new Topic is set up over one or more Sessions, a step at a time: each step records what
it decided, so the learner can stop after any of them and pick up later. The first Session
ends inside Lesson 1, not after hours of planning.

Done when Session 1 ends at a Break point of Lesson 1, or with a written Next step.

## Steps

1. **Energy.** At fumes, suggest coming back when they have more to give, and note the
   subject they want to learn. Done when they have the energy to start.
2. **Brainstorm**, one question at a time, each a short numbered menu where the answer is a
   choice:
   - the **Goal**: what they want to be able to do at the end, and a deadline if there is
     one;
   - the **Pace**: hours a week, as periods if it will change ("10 hours a week until
     December, then 3");
   - the **Approach**: standalone concepts, one project built step by step, or a run of
     challenges;
   - the **Workbench**: code as the subject, code as a tool, or written work.

   Create the Topic with `topic_create` (title and Goal), then record the deadline and the
   Pace with `topic_update`. Write the Approach and the Workbench kind in
   `notes/brainstorm.md` until the tools take them (see the pending note below).
   Done when the Topic exists with its Goal and Pace.
3. **Sources.** Find books in their Library with `library_search` and web pages they trust,
   add them with `source_add`, and choose the Knowledge base
   ([Knowledge](knowledge.md)). Done when the Sources are added, or the learner chose to
   start without any.
4. **Assessment**, time-boxed to about 15 minutes. Ask short questions across the Goal's
   areas, from easy to hard, and move on as soon as an area is clear. Mark areas you didn't
   reach as "confirm during Lessons". Save the result in `notes/assessment-<date>.md`: what
   they know, what they don't yet, what to confirm, and the Level it suggests (beginner,
   intermediate, advanced or expert). Tell the learner the Level and let them change it.
   Done when the notes are saved and the learner agreed the Level.
5. **The first Syllabus**: draft and approve it ([The Syllabus](syllabus.md)). Done when
   the learner approved it.
6. **The Workbench**: set it up with the ecosystem's own tools (`go mod init`,
   `cargo new`, `uv init`, `npm create`), or a folder for written work. Ask before
   installing any toolchain. Done when an empty exercise can run.
7. **Lesson 1**: start it ([The lesson loop](lesson-loop.md)) and end the Session at one of
   its Break points.

<!-- pending #31 -->
## Assessment and Level (pending #31)

Once the Assessment tools exist:

- Record each Assessment with `assessment_record`: the placement Assessment here, and the
  one at the end of each Milestone. Weak results lead to a proposed Revision, such as a
  review Lesson, never to a block.
- The Assessment sets the Level. The learner can change it with `topic_update` (`level`);
  their choice holds until the next Assessment.
- Record the Approach with `topic_update` (`approach`) instead of in notes.
<!-- /pending #31 -->
