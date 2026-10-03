# The lesson loop

Each Lesson moves through three Phases: teaching, practicing and feedback. Teaching and
feedback are your turn, practicing is the learner's, and every move saves the turn that
ended (Instruction 5). Start at the step for the Resume point's Phase: teaching at 1,
practicing at 3, feedback at 4.

## Steps

1. Teach. Call `phase_set` with `teaching`, and write `lessons/<lesson-id>.md` from the
   template below, at the learner's Level and in their words. Research the concept first
   with the companions installed ([Companions](companions.md)), and record Evidence from
   the Topic's Sources ([Knowledge](knowledge.md)). Write the Check's scripts as the
   conventions below say, and the answers in Teacher's notes. Present the Lesson, then ask:

   ```
   What would you like to do?
   1. Read the Lesson and start the exercise
   2. Ask about the concept first
   3. Take a break
   ```

   Done when the learner is ready to practice.
2. Show the Check. Walk the learner through its criteria in plain words: what will be run,
   and what you will look at; when it has a `held_out` criterion, that there is also a test
   set they won't see. Then call `phase_set` with `practicing`.
   Done when practicing is recorded.
3. Practice. The learner works in `practice/<lesson-id>/`. Answer questions with hints that
   point to concepts, and record each hint ([Hints](#hints)). When they reach a Break point,
   record it with `break_point_reached`.
   Done when the learner hands their work back.
4. Feedback. Call `phase_set` with `feedback`. When the Check has `run` or `held_out`
   criteria, run it in your own shell:

   ```
   study check <lesson-id> --topic <topic-id>
   ```

   A Check with only rubric items has nothing to run: go straight to grading. Read
   `check_results`, grade each rubric item with `rubric_record` as Instruction 7 says, and
   give feedback by the teaching rules. Then ask:

   ```
   What's next?
   1. I'll revise my work
   2. Questions about the feedback
   3. Run the Check again
   4. Complete the Lesson
   5. Take a break
   ```

   Leave out 4 until `check_results` says the Lesson can be completed, keeping the other
   numbers as they are. After a failed Attempt, follow `next` in `check_results`.
   Done when the learner chose what's next.
5. Complete. Call `lesson_complete` with the Lesson's draft Cards
   ([Cards and Reviews](cards-and-reviews.md#writing-cards)), then offer the next Lesson,
   Reviews, or a break.
   Done when `lesson_complete` succeeded and the learner chose what comes next.

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
    held_out: [sh, -c, 'sh "$STUDY_HELDOUT_DIR/eval.sh"']
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

- Break points are the places a Session can stop inside the Lesson, in order, each a step
  the learner can finish in one sitting.
- Stretch goals are optional extras that never hold the Lesson back.
- Keep the YAML header plain, and quote any value that contains a colon.

## Check scripts

- Tests and other `run` scripts live in the practice folder, written before practicing
  starts, so the learner can read and run them too. List the files a run writes, such as
  build output, in the practice folder's `.gitignore`.
- A command may report its scores as JSON in the file `$STUDY_RESULTS` names. Every field
  is optional, but a `held_out` command must write the file, with a `score`:

  ```json
  {"format": 1, "passed": true, "score": 17, "max": 20,
   "metrics": {"recall": 0.88}, "summary": "17 of 20 cases"}
  ```

  A run criterion passes when its command exits with 0 and the file, if any, does not say
  `"passed": false`.
- Changing a Check after showing it means showing it again: `phase_set` with `practicing`.

## Written work

For maths, essays or anything on paper, the learner puts typed final answers in a text file
in the practice folder, or a photo of their paper work. Grade it with rubric items, naming
the files you looked at.

## Held-out data

Held-out data measures the work on data the learner never sees while they work; they hear
"the test set". Instruction 7 gives the rules, and where the data goes. Beyond them:

- Keep the evaluation script with the data, and call it through `$STUDY_HELDOUT_DIR`, as in
  the template, so the practice folder holds nothing of it.
- Present a result as how the work did on the test set: a measurement for later, never a
  gate.

## Writing at each Level

- Beginner: longer explanations with analogies; one concept per exercise; two or three
  small reference examples; explicit requirements; a starter file with comments in the
  practice folder.
- Intermediate: balanced notes and practice; exercises that combine two or three concepts;
  one short reference example; requirements stated as outcomes.
- Advanced: short notes that build on earlier Lessons; exercises that need independent
  research, or that point at a Source ("implement the algorithm in chapter 7"); open-ended
  requirements.
- Expert: a challenge with no notes; constraints on performance or design; requirements
  that leave design choices to the learner. When they are stuck, offer to step back a Level
  for this concept rather than hint.

`status` gives the Topic's Level and Approach. When the learner wants a different Level,
record their choice with `topic_update` (`level`); it holds until the next Assessment.

Before writing a Lesson, read `signals` to adapt it: the depth of the notes, how much
scaffolding, how soon to offer a hint. They are for you alone (Instruction 12): when the
learner asks how they are doing, answer in words.

## Hints

Record every hint you give with `hint_record`, in the Lesson being studied:

- `requested_by`: `learner` when they asked for help, `agent` when you offered it unasked;
- `kind`: `nudge` for a question or a pointer, `explanation` for explaining a concept
  again, `step` for showing part of the way to a solution.

Hints are how the next Lessons are pitched, so a hint you don't record teaches Lamplight
the wrong Level.
