# Lamplight

An interactive tutor that helps one person learn a subject through a planned syllabus,
hands-on exercises and spaced repetition.

## Language

### Learning

**Topic**:
One subject a person is learning, with its own folder of lessons, exercises, notes and
review material. A Topic is active, paused or finished; a paused Topic hides its due Cards
and Forecasts.
_Avoid_: Workspace, course, subject

**Removed Topic**:
A Topic the learner moved out of the Study home on one computer. It is kept whole there
until it is restored; the learner's other computers and the Topic's git remote keep theirs.
_Avoid_: Deleted, archived, trashed

**Goal**:
What the learner wants to be able to do at the end of a Topic, optionally with a deadline.
_Avoid_: End goal, objective

**Pace**:
How many hours per week the learner plans to spend on a Topic, as dated periods (for
example ten hours a week until a deadline, then three).
_Avoid_: Budget, schedule

**Approach**:
How a Topic's Lessons relate to each other: `concepts` (standalone concepts), `project` (one
project built step by step) or `challenges` (a run of challenges). Chosen with the learner
when the Topic is created.
_Avoid_: Style, track

**Level**:
How advanced the teaching is for a Topic: beginner, intermediate, advanced or expert.
_Avoid_: Difficulty

**Assessment**:
A short, time-boxed quiz that finds what the learner already knows, run before drafting a
Syllabus and again at the end of each Milestone.
_Avoid_: Calibration, placement test

**Syllabus**:
The ordered program of Milestones and Lessons for one Topic, drafted with the learner and
extended over time.
_Avoid_: Plan, curriculum, study program

**Revision**:
A proposed change to the not-yet-done part of a Syllabus, applied only after the learner
approves it.
_Avoid_: Edit, update, replan

**Triage**:
A Revision offered when a Forecast misses a deadline: trim Stretch goals, move Lessons past
the deadline, or raise the Pace.
_Avoid_: Catch-up plan

**Milestone**:
A finish line within a Syllabus: a group of Lessons that ends in an outcome the learner can
state. Its priority is must, if time allows, or after the deadline.
_Avoid_: Unit, chapter, module, tier

**Forecast**:
When a Milestone will be finished at the current Pace. Shown instead of any count of what
is late.
_Avoid_: Behind, overdue count

**Lesson**:
One unit of teaching within a Milestone: an explanation, an exercise, and a Check, aimed at
one full-energy sitting. A Lesson can be skipped through a Revision.

**Break point**:
A named step written into a Lesson where a Session can stop and a later one can resume.

**Stretch goal**:
Optional extra work in a Lesson that never blocks it from being done.
_Avoid_: Bonus, extra credit

**Phase**:
How far the current Lesson has got: teaching, practicing or feedback.
_Avoid_: Reviewing (that word belongs to Cards), step, stage

**Card**:
A single concept to remember, stated as a prompt with an expected answer, written from a
Lesson or an Explore Session. A new Card is a draft until the learner keeps, edits or drops
it at its first Review.
_Avoid_: Flashcard, review item, lesson card

### Exercises

**Workbench**:
Where a Topic's exercises are done: code as the subject, code as a tool, or written work.
_Avoid_: Template, practice folder, scaffold

**Check**:
The criteria a Lesson's exercise is measured against: repeatable runs, rubric items, and
evaluations on Held-out data. A Lesson is done when the runs pass on the current work and
every rubric item is graded for it; any grade counts, `not_met` included, because grading
records that the learner's work was checked, not that it is right. Held-out results are
recorded but don't decide it, so a Check needs at least one run or rubric item.
_Avoid_: Grading, validation

**Attempt**:
One run of a Lesson's Check against the learner's work as it stood at that moment, with its
outcome: passed, failed or errored.
_Avoid_: Try, submission, run

**Held-out data**:
Test data kept out of the learner's sight while they work; only the first run that produces
results, on the Check shown to the learner and in an Attempt that did not error, counts as
its measurement. It is always synthetic or public, never personal data.
_Avoid_: Hidden tests, secret data

**Hint**:
Help the agent gives while the learner practises a Lesson: a nudge, an explanation of a
concept again, or a step of the way to a solution, asked for by the learner or offered by
the agent unasked. Each one is recorded, as a signal for adapting how the Topic is taught.
_Avoid_: Help request, clue

### Sessions

**Session**:
One sitting, from the energy check to the break.

**Energy**:
How much the learner has to give at the start of a Session: full, half or fumes. It decides
what the Session does.
_Avoid_: Battery, mood

**Focus**:
What a Session is for: Learn, Practice, Reviews or Explore. Suggested from Energy, chosen by
the learner.
_Avoid_: Mode

**Next step**:
The concrete action, starting with a verb, recorded whenever a Session stops and shown first
when the learner comes back.
_Avoid_: Continue, pending action

**Checkpoint**:
A saved snapshot of a Topic, taken whenever the turn passes between the agent and the
learner, so the learner's own work can be seen on its own.
_Avoid_: Save, commit (that is how a Checkpoint is stored, not what it is)

**Import**:
Turning a workspace of the v1 study skill into a new Topic: a copy with its history, v1's
names kept, the Lessons v1's records prove done marked done, and a report of what was
converted and dropped. The original is left untouched.
_Avoid_: Migration (that is the whole move from v1, of which the Import is one step)

**Adoption Session**:
The first Session on an imported Topic, which works through the import's report with the
learner and gives the Topic what v1 lacked: Goal and deadline, Pace, a Syllabus approved as
a Revision, Checks, Cards and a Next step.
_Avoid_: Onboarding, conversion

### Records

**History**:
The learner's record of what happened in a Topic, as a sequence of Events. Progress and
Card scheduling are read from it.
_Avoid_: Log, activity log, journal (reserved for learner-written reflections)

**Event**:
One entry in the History, such as a Session starting, a Lesson finishing or the Syllabus
changing.

**Flag**:
Something in a Topic that needs the learner's attention, found while replaying its History,
such as one Card changed on two machines. Reported in `status`, never resolved
automatically; once the learner has looked at it, some kinds can be dismissed.
_Avoid_: Warning, alert (and do not confuse with a command-line option)

**Review**:
One rating of one Card by the learner; the Event that drives spaced repetition.
_Avoid_: Rating, recall check

**Log**:
Application diagnostics: errors, warnings and debug output. Never the learner's activity.

### People and notes

**Learner profile**:
How the learner likes to be taught, read at the start of every Session, with optional
additions per Topic.
_Avoid_: Persona, preferences

**Teacher's notes**:
What the agent keeps for itself while teaching a Topic, such as answer keys and expected
scores. Clearly labelled so the learner can choose not to look.
_Avoid_: Agent notes, hidden notes

**Task**:
A step toward a Topic's Goal that is not study, such as booking a meeting or submitting an
application.
_Avoid_: Errand, todo

### Materials

**Source**:
A document or web page a Topic learns from.

**Knowledge base**:
Where a Topic's Sources are held and searched for Evidence. A Topic has at most one; without
one, the agent reads the Sources and records Evidence itself.
_Avoid_: Backend, RAG

**Knowledge base plugin**:
A program or service the learner runs that indexes a Topic's Sources and searches them,
speaking Lamplight's contract over MCP. It is registered on each machine under a name, and a
Topic names the one it uses.
_Avoid_: Backend, RAG, extension, adapter

**Shelf**:
The Knowledge base plugin that ships with Lamplight, as the `study-shelf` program.

**Recipe**:
A named set of Shelf's settings for one embedding model.
_Avoid_: Preset, profile

**Passage**:
A stretch of a Source's own text that a search of the Knowledge base returns, with its
location when known. Evidence quotes from a Passage.
_Avoid_: Chunk, snippet, hit, result

**Library**:
The learner's whole collection of documents across all Topics.
_Avoid_: Catalog (that is only the index behind the Library)

**Evidence**:
An exact quote from a Source, with its location when known, cited by a Lesson.
_Avoid_: Answer, snippet, chunk

### Places

**Study home**:
The one folder that holds every Topic. The learner can start their agent there or inside
any Topic.
_Avoid_: Workspace root, study directory

**Active topic**:
The Topic the agent is working on: the one whose folder it started in, otherwise the one
worked on most recently, otherwise the only Topic.

**Resume point**:
Where the learner stopped within a Topic: the Lesson, its Phase, the last Break point
reached, and the Next step.
