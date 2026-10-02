# The lesson loop

Each Lesson moves through three **Phases**: teaching, practicing, feedback. Teaching and
feedback are your turn; practicing is the learner's. `phase_set` records each move and
saves the work of the turn that just ended (a Checkpoint). When you hand over outside a
`phase_set`, save with `checkpoint`.

## Steps

1. **Teach.** Call `phase_set` with `teaching`. Write `lessons/<lesson-id>.md` from the
   template below, at the learner's Level. Research the concept first with the companions
   installed ([Companions](companions.md)), and record Evidence for what the Lesson
   teaches from its Sources ([Knowledge](knowledge.md)). Write the Check's scripts, the
   Held-out data if the Check has any, and answer keys in Teacher's notes. Present the
   Lesson, then ask:

   ```
   What would you like to do?
   1. Read the Lesson and start the exercise
   2. Ask about the concept first
   3. Take a break
   ```

   Done when the learner is ready to practise.
2. **Show the Check.** Walk the learner through its criteria in plain words: what will be
   run, what you will look at, and that there is a test set they won't see. Then call
   `phase_set` with `practicing`. Done when the Check was shown and practicing recorded.
3. **Practice.** The learner works in `practice/<lesson-id>/`. Answer questions with hints
   that point to concepts. When they reach a Break point, record it with
   `break_point_reached`. Done when the learner hands their work back.
4. **Feedback.** Call `phase_set` with `feedback`, which saves their work. Run the Check in
   your own shell:

   ```
   study check <lesson-id> --topic <topic-id>
   ```

   Then read `check_results`. Ask the learner to check their work against each rubric item
   themselves, then grade it with `rubric_record`. Give feedback by the teaching rules, and
   ask:

   ```
   What's next?
   1. I'll revise my work
   2. Questions about the feedback
   3. Run the Check again
   4. Complete the Lesson
   5. Take a break
   ```

   Offer 4 only when `check_results` says the Lesson can be completed. After a failed
   Attempt, give feedback, then go back to practicing with `phase_set` and a Next step
   that names the fix (`check_results` says so in `next`).
   Done when the learner chose what's next.
5. **Complete.** Call `lesson_complete` with the Lesson's draft Cards
   ([Cards and Reviews](cards-and-reviews.md#writing-cards)). Then offer the next Lesson,
   Reviews, or a break.

## The Lesson template

```markdown
---
check:
  - id: tests
    describe: The tests pass
    run: [go, test, ./...]
  - id: names
    rubric: Every function name says what it does
  - id: accuracy
    describe: Accuracy on the test set
    held_out: [python3, eval.py]
break_points:
  - id: swap
    describe: swap() works on two ints
---
# <Lesson title>

## Concept
## Key points
## Reference example (don't copy)
## Common pitfalls
## Exercise
### What to build
### Requirements
### Where to work: practice/<lesson-id>/
## Break points
## Check
## Stretch goals
```

- **The Check** is the `check:` list. Each criterion has an `id` and one of `run` (a
  command as an argument list), `rubric` (what you will grade) or `held_out` (a command
  that evaluates on the test set). Include at least one `run` or `rubric` criterion.
  Commands run in the practice folder and may write scores to the file named by
  `STUDY_RESULTS`. List files a run writes, such as build output, in the practice folder's
  `.gitignore`, so the work stays the same while the Check runs.
- **Break points** are the places a Session can stop inside the Lesson, in order, each a
  step the learner can finish in one go.
- **Stretch goals** are optional extras that never hold the Lesson back.
- Keep the YAML header plain: quote any value that contains a colon.
- To change a Check after showing it, show the new one and call `phase_set` with
  `practicing` again.

## Written work

For maths, essays or anything on paper, the learner puts typed final answers in a text file
in the practice folder, or a photo of their paper work. Grade it with rubric items, and
name the files you looked at in `looked_at`.

## Held-out data

Some Lessons are measured on data the learner never sees while they work, such as a test
set for a model. Lamplight calls it Held-out data; the learner hears "the test set".

- Write it into `.heldout/<lesson-id>/` before practicing starts. Use synthetic or public
  data only.
- Its `held_out` command reads the data from `STUDY_HELDOUT_DIR` and writes a results file
  with a `score`.
- Show the learner only the scores and summaries, keeping the data itself and the command's
  output to yourself.
- Only the first run on the Check shown to the learner counts. Keep each `held_out`
  criterion's id as it is, because a new id starts a new count.
- Held-out results are a measurement for later, never a gate: present them as how the work
  did on the test set.

## Writing at each Level

- **Beginner**: longer explanations with analogies; one concept per exercise; two or three
  small reference examples; explicit requirements; a starter file with comments in the
  practice folder.
- **Intermediate**: balanced notes and practice; exercises that combine two or three
  concepts; one short reference example; requirements stated as outcomes.
- **Advanced**: short notes that assume earlier Lessons; exercises that need independent
  research, or that point at a Source ("implement the algorithm in chapter 7"); open-ended
  requirements.
- **Expert**: a challenge with no notes; constraints on performance or design; requirements
  that leave design choices to the learner. When they are stuck, offer to step back a Level
  for this concept rather than hint.

Read the Level from the Assessment notes and the Learner profile; the learner can change
it at any time.

<!-- pending #31 -->
## Hints and Level signals (pending #31)

Once the tools exist, read the Level from the Topic, and record each hint the learner asks
for with `hint_record`. Lamplight records the other signals a future Level suggestion needs
(first-try passes, feedback rounds, Review results) from the tools you already call.
<!-- /pending #31 -->
