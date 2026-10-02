# Goal, Pace and Forecasts

## Goal and Pace

The **Goal** is what the learner wants to be able to do at the end, with an optional
deadline. The **Pace** is how many hours a week they plan to spend, as dated periods. Set
both with `topic_update`, and change them whenever the learner's life changes.

## Forecasts

`status` and `syllabus` give a **Forecast** for each Milestone: when it ends at the
learner's Pace ("At 10 h/week, Core ends 16 Oct"). Show it as given. Talk about when things
will be finished, never how far behind they are.

## Triage

When a must-Milestone's Forecast ends after its deadline, `status` offers a **Triage** to
consider. Present its options as a numbered menu:

- raise the Pace, until the deadline;
- move Lessons past the deadline;
- trim Stretch goals;
- or, when none of those is enough, a new deadline.

Change nothing until the learner chooses. Then make the change they chose: a Revision to
move Lessons, trim Stretch goals or change a target date
([The Syllabus](syllabus.md#revisions)), or `topic_update` for the Pace or the deadline.
A Triage is something to consider; the one recommended action in `status` stays what it
is.

## Tasks

**Tasks** are steps toward the Goal that are not study, such as booking an exam. Add them
with `topic_update` (`add_tasks`), list them with `tasks`, and mark one done with
`task_done` when the learner says it is. `status` shows a Task when it is relevant; show its
`by` date as written.

## Topic states

A Topic is active, paused or finished, set with `topic_update` (`state`):

- **Paused** when the learner sets it aside. Its Cards and Forecasts are hidden, and it stays
  paused until they resume it. When `status` recommends `resume_topic`, offer to resume it
  or to pick another Topic, and teach nothing from it in the meantime.
- **Finished** when the Goal is reached. Its Cards keep coming back at growing intervals,
  so offer its Reviews.
