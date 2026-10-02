# Cards and Reviews

A **Card** is one thing to remember: a prompt and its expected answer. Lamplight schedules
each Card from its Reviews, so it comes back just before the learner would forget it.

## Writing Cards

- One fact per Card, with a prompt that asks and an answer that states.
- No lists, no answer hidden in the prompt, no trivia.
- At least one Card from the learner's own mistakes in the Lesson.
- Prompts and answers may span lines, for code.
- When a Card states something from a Source, cite its Evidence ids
  ([Knowledge](knowledge.md)).

Cards come from three places:

- `lesson_complete`: the Lesson's draft Cards, written when it is completed.
- `card_add` with a Lesson: a Card the learner wants to keep later, including from a
  Lesson that was skipped.
- `card_add` without a Lesson: an Explore Card, from a free question.

Fix a Card with `card_edit`. When the learner wants to stop seeing one, `card_suspend` it;
`card_delete` removes it for good.

## Reviews

1. Call `due_cards`. It sizes the list to the Session's Energy and to the daily cap on new
   Cards. Done when you have the list.
2. For each Card, ask its prompt and let the learner answer in their own words. Then show
   the expected answer and ask how well they recalled it:

   ```
   How well did you remember it?
   1. Again: I didn't
   2. Hard
   3. Good
   4. Easy
   ```

   A new Card is a draft until its first Review. Before rating it, ask whether to keep it,
   edit it or drop it.

   Record each answer with `review_record`, with a fresh `request` id for each Review, so
   a retry after an error records it once.
3. When the list ends or the learner wants to stop, say "That's all for now". Leave out how
   many Cards are due or left.

Done when every Card on the list was reviewed or the learner stopped.

Without an agent, the learner can review in the terminal with `study review`.

## Explore

In an Explore Session the learner asks whatever they like. Answer by the teaching rules;
when an answer is worth keeping, offer it as an Explore Card. When a question shows the
Syllabus should change, offer a Revision ([The Syllabus](syllabus.md#revisions)).
