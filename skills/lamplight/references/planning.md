# Goal, Pace and Forecasts

## Goal and Pace

The Goal is what the learner wants to be able to do at the end, with an optional deadline.
The Pace is how many hours a week they plan to spend, as dated periods. Set both with
`topic_update`, and change them whenever the learner's life changes.

`status` and `syllabus` give a Forecast for each Milestone; Instruction 10 says how to show
it.

## Triage

When a must-Milestone's Forecast ends after its deadline, `status` offers a Triage to
consider. Offer only the options it gives, as a numbered menu, and change nothing until the
learner chooses (Instruction 10):

- `raise_pace_to`: a Pace that finishes in time, in hours a week until the deadline; or
  `hours_today` when the deadline is today. Change it with `topic_update`.
- `move_lessons`: Lessons to move past the deadline, through a Revision.
- `trim_hours`: hours of Stretch goals to trim. Trimming means lowering the `hours` of the
  Lessons whose Stretch goals the learner drops, through a Revision.
- `suggest_deadline`: a date the Milestone can end by at the current Pace, offered when
  nothing else fits or the deadline has passed.

When `deadline_passed` is set, the deadline is already behind them, so a new date comes
first: offer `suggest_deadline`, or ask for a date of their own. A new date changes what
`deadline_from` names: the Goal's deadline (`goal`) with `topic_update`, a Milestone's
target date (`target`) through a Revision.

## Tasks

Tasks are steps toward the Goal that are not study, such as booking an exam. Add them with
`topic_update` (`add_tasks`), list them with `tasks`, and mark one done with `task_done`
when the learner says it is. `status` shows a Task while it is relevant; show its `by` date
as written.

## Topic states

A Topic is active, paused or finished, set with `topic_update` (`state`). Pause it when
the learner sets it aside, and finish it when the Goal is reached; Instruction 11 says how
each behaves.
