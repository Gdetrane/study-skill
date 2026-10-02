# Starting a Topic

A new Topic is set up a step at a time, over one or more Sessions. Each step records what
it decided, so the learner can stop after any of them; when `status` recommends `plan`,
pick up at the first step not done.

Done when Session 1 ends at a Break point of Lesson 1, or with a Next step.

## Steps

1. Energy. Ask how much energy they have and how long they have today.
2. The Topic. Ask what they want to learn, create the Topic with `topic_create` (a title,
   and the Goal if they already know it), and open the Session on it with `session_open`
   and the Energy from step 1. At fumes, write what they said in `notes/brainstorm.md`,
   write tomorrow's first step together, and stop as in SKILL.md step 4.
   Done when the Session is open.
3. Brainstorm, one question at a time, with a numbered menu wherever the answer is a
   choice:
   - the Goal: what they want to be able to do at the end, and a deadline if there is one;
   - the Pace: hours a week, as periods if it will change ("10 hours a week until December,
     then 3");
   - the Approach: standalone concepts (`concepts`), one project built step by step
     (`project`), or a run of challenges (`challenges`);
   - the Workbench: code as the subject, code as a tool, or written work.

   Record the Goal, the deadline, the Pace and the Approach with `topic_update`, and write
   the Workbench kind in `notes/brainstorm.md`.
   Done when the Goal, the Pace and the Approach are recorded.
4. Sources: add them and choose the Knowledge base ([Knowledge](knowledge.md)).
   Done when the Sources are added, or the learner chose to start without any.
5. The placement Assessment, time-boxed to about 15 minutes. Ask short questions across
   the Goal's areas, from easy to hard, moving on as soon as an area is clear; the areas you
   didn't reach are `not_reached`, to confirm during Lessons. Save your notes in
   `notes/assessment-<date>.md`: what they know, what they don't yet, what to confirm, and
   the Level it suggests. Agree the Level with the learner, then record the Assessment with
   `assessment_record`: kind `placement`, one item per question, the notes file, and the
   agreed Level as `level`, which becomes the Topic's Level.
   Done when `assessment_record` succeeded.
6. The first Syllabus: draft it and get it approved ([The Syllabus](syllabus.md)).
7. The Workbench: set it up with the ecosystem's own tools (`go mod init`, `cargo new`,
   `uv init`, `npm create`), or a folder for written work. Ask before installing any
   toolchain. Done when an empty exercise can run.
8. Lesson 1: start it ([The lesson loop](lesson-loop.md)).

Then stop as in SKILL.md step 4, at one of Lesson 1's Break points.
