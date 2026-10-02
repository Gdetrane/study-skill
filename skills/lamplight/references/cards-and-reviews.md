# Cards and Reviews

A Card is one thing to remember: a prompt and its expected answer; the learner calls them
flashcards. Lamplight schedules each Card from its Reviews, so it comes back just before the
learner would forget it.

## Writing Cards

Write Cards as Instruction 9 and the `card_add` and `lesson_complete` descriptions say.
Draw them from three places:

- the Lesson being completed: its draft Cards go with `lesson_complete`;
- a Lesson the learner wants to keep more from later, even one that was skipped:
  `card_add` with the Lesson;
- free questions in an Explore Session: `card_add` without a Lesson.

Fix a Card with `card_edit`. When the learner wants to stop seeing one, `card_suspend` it;
`card_delete` removes it for good.

## Reviews

1. Call `due_cards` for the list.
2. For each Card, ask its prompt and let the learner answer in their own words. Show the
   expected answer, then ask how well they remembered it:

   ```
   How well did you remember it?
   1. Again: I didn't
   2. Hard
   3. Good
   4. Easy
   ```

   At a new Card's first Review, ask first whether to keep it, edit it or drop it. Record
   each answer with `review_record` as its description says.
3. When the list ends or the learner wants to stop, say "That's all for now".

Done when every Card on the list was reviewed, or the learner stopped. Without an agent,
the learner can review in the terminal with `study review`.

## Explore

In an Explore Session the learner asks whatever they like. Answer by the teaching rules;
when an answer is worth keeping, offer it as a flashcard. When a question shows the
Syllabus should change, offer a Revision ([The Syllabus](syllabus.md#revisions)).
