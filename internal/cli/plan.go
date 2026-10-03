package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mordor-forge/lamplight/v2/internal/core"
)

// The plan of a Topic on the command line: Tasks, the Pace, Forecasts and
// Triage, and paused or finished Topics.

func (a *app) taskCommand() *cobra.Command {
	task := &cobra.Command{
		Use:   "task",
		Short: "Add, finish and list a Topic's Tasks: steps toward the Goal that are not study",
		Args:  noArgs,
		RunE:  a.groupHelp,
	}

	var spec core.TaskSpec
	var addDryRun bool
	add := &cobra.Command{
		Use:   "add <topic> <title…>",
		Short: "Add a Task, such as booking the exam",
		Example: `  study task add c "Book the exam" --by 2026-11-01
  study task add c "Ask for feedback on the project" --after core`,
		Args: minArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := core.Open(a.opts)
			if err != nil {
				return a.fail(err)
			}
			spec.Title = strings.Join(args[1:], " ")
			res, err := c.UpdateTopic(cmd.Context(), args[0], core.TopicChanges{AddTasks: []core.TaskSpec{spec}, DryRun: addDryRun})
			if err != nil {
				return a.fail(err)
			}
			if a.json {
				return a.writeJSON(envelope{OK: true, Data: res})
			}
			verb := "Added"
			switch {
			case !res.Changed:
				verb = "Already there:"
			case addDryRun:
				verb = "Would add"
			}
			for _, t := range res.AddedTasks {
				if t.ID == "" {
					fmt.Fprintf(a.out, "%s Task %s %s\n", verb, taskLine(t), styleDim.Render("(a new id)"))
					continue
				}
				fmt.Fprintf(a.out, "%s Task %s %s\n", verb, styleAccent.Render(t.ID), taskLine(t))
			}
			return nil
		},
	}
	add.Flags().StringVar(&spec.By, "by", "", "a date you want it done by, YYYY-MM-DD")
	add.Flags().StringVar(&spec.After, "after", "", "a Milestone id: show the Task once that Milestone is done")
	add.Flags().BoolVar(&addDryRun, "dry-run", false, "show the Task without adding it")

	var undo, doneDryRun bool
	done := &cobra.Command{
		Use:     "done <topic> <task>",
		Short:   "Mark a Task done, or not done with --undo",
		Example: "  study task done c book-the-exam.k3f9a2",
		Args:    exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := core.Open(a.opts)
			if err != nil {
				return a.fail(err)
			}
			res, err := c.MarkTask(cmd.Context(), args[0], args[1], !undo, doneDryRun)
			if err != nil {
				return a.fail(err)
			}
			if a.json {
				return a.writeJSON(envelope{OK: true, Data: res})
			}
			state := "done"
			if undo {
				state = "not done"
			}
			switch {
			case !res.Changed:
				fmt.Fprintf(a.out, "Task %s was already %s.\n", res.Task.ID, state)
			case doneDryRun:
				fmt.Fprintf(a.out, "Would mark Task %s %s.\n", res.Task.ID, state)
			default:
				fmt.Fprintf(a.out, "Task %s is %s: %s\n", styleAccent.Render(res.Task.ID), state, printable(res.Task.Title))
			}
			return nil
		},
	}
	done.Flags().BoolVar(&undo, "undo", false, "mark the Task not done")
	done.Flags().BoolVar(&doneDryRun, "dry-run", false, "show the result without recording it")

	var removeDryRun bool
	remove := &cobra.Command{
		Use:     "remove <topic> <task…>",
		Short:   "Remove Tasks that no longer matter",
		Example: "  study task remove c book-the-exam.k3f9a2",
		Args:    minArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := core.Open(a.opts)
			if err != nil {
				return a.fail(err)
			}
			res, err := c.UpdateTopic(cmd.Context(), args[0], core.TopicChanges{RemoveTasks: args[1:], DryRun: removeDryRun})
			if err != nil {
				return a.fail(err)
			}
			if a.json {
				return a.writeJSON(envelope{OK: true, Data: res})
			}
			switch {
			case !res.Changed:
				fmt.Fprintln(a.out, "Nothing to remove: those Tasks are not there.")
			case removeDryRun:
				fmt.Fprintf(a.out, "Would remove %s.\n", strings.Join(args[1:], ", "))
			default:
				fmt.Fprintf(a.out, "Removed %s.\n", strings.Join(args[1:], ", "))
			}
			return nil
		},
	}
	remove.Flags().BoolVar(&removeDryRun, "dry-run", false, "show the result without removing anything")

	var all bool
	list := &cobra.Command{
		Use:   "list <topic>",
		Short: "List a Topic's open Tasks, or all with --all",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := core.Open(a.opts)
			if err != nil {
				return a.fail(err)
			}
			res, err := c.ListTasks(cmd.Context(), args[0], all)
			if err != nil {
				return a.fail(err)
			}
			if a.json {
				return a.writeJSON(envelope{OK: true, Data: res})
			}
			var b strings.Builder
			if len(res.Tasks) == 0 {
				b.WriteString("No Tasks.\n")
			}
			for _, t := range res.Tasks {
				mark := " "
				if t.Done {
					mark = styleOK.Render("✓")
				}
				fmt.Fprintf(&b, "%s %s %s\n", mark, styleAccent.Render(t.ID), taskLine(t))
			}
			for _, p := range res.Problems {
				fmt.Fprintf(&b, "%s\n", styleWarn.Render(printable(p)))
			}
			_, err = io.WriteString(a.out, b.String())
			return err
		},
	}
	list.Flags().BoolVar(&all, "all", false, "include the Tasks already done")

	task.AddCommand(add, done, remove, list)
	return task
}

// taskLine is a Task's title with its date and Milestone, if any.
func taskLine(t core.Task) string {
	line := printable(t.Title)
	var details []string
	if t.By != "" {
		details = append(details, "by "+t.By)
	}
	if t.After != "" {
		details = append(details, "after Milestone "+t.After)
	}
	if len(details) > 0 {
		line += styleDim.Render(" (" + strings.Join(details, ", ") + ")")
	}
	if t.Note != "" {
		line += styleDim.Render(": " + t.Note)
	}
	return line
}

// parsePace reads --pace values: HOURS for the first period, from now on,
// and HOURS@YYYY-MM-DD for a period starting that day.
func parsePace(values []string) ([]core.PacePeriod, error) {
	periods := make([]core.PacePeriod, 0, len(values))
	for _, v := range values {
		hours, from, _ := strings.Cut(strings.TrimSpace(v), "@")
		h, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(hours), "h"), 64)
		if err != nil {
			return nil, usageError{fmt.Errorf("--pace %q: give hours a week, such as 10, or 3@2026-11-16 for a period "+
				"starting that day", v)}
		}
		periods = append(periods, core.PacePeriod{From: strings.TrimSpace(from), HoursPerWeek: h})
	}
	return periods, nil
}

// pausedText says that a paused Topic's Cards wait.
func pausedText(topic string) string {
	return fmt.Sprintf("Topic %s is paused: its Cards wait until you resume it with study topic update %s --state active.",
		topic, topic)
}

// milestoneForecast is a Milestone's Forecast, if there is one.
func milestoneForecast(f *core.Forecast, id string) *core.MilestoneForecast {
	if f == nil {
		return nil
	}
	for i := range f.Milestones {
		if f.Milestones[i].Milestone == id {
			return &f.Milestones[i]
		}
	}
	return nil
}

// setPaceText suggests setting a Pace for a Topic.
func setPaceText(topic string) string {
	return "Set a Pace to see when each Milestone ends: study topic update " + topic + " --pace 10"
}

// writeForecastNotes writes what a Forecast asks of the learner: a Pace to
// set, or a Triage to choose from.
func writeForecastNotes(b *strings.Builder, topic string, f *core.Forecast) {
	if f == nil {
		return
	}
	if f.NeedsPace {
		fmt.Fprintf(b, "\n%s\n", styleDim.Render(setPaceText(topic)))
	}
	if t := f.Triage; t != nil {
		fmt.Fprintf(b, "\n%s\n  %s\n", styleWarn.Render("To consider:"), printable(t.Text))
	}
}

// writePlan writes a Topic's plan in status: its state, the Goal's
// deadline, the Pace, the Forecast and the relevant Tasks.
func writePlan(b *strings.Builder, t core.Topic, labels int) {
	switch t.State {
	case core.TopicPaused:
		fmt.Fprintf(b, "%s%s\n", styleLabel.Render(pad("State:", labels)),
			styleDim.Render("paused: its Cards wait until you resume it with study topic update "+t.ID+" --state active"))
	case core.TopicFinished:
		fmt.Fprintf(b, "%s%s\n", styleLabel.Render(pad("State:", labels)), styleDim.Render("finished: its Cards keep coming back"))
	}
	if t.Deadline != "" {
		fmt.Fprintf(b, "%s%s\n", styleLabel.Render(pad("Deadline:", labels)), t.Deadline)
	}
	if f := t.Forecast; f != nil {
		if f.Pace != "" {
			fmt.Fprintf(b, "%s%s\n", styleLabel.Render(pad("Pace:", labels)), f.Pace)
		}
		var lines []string
		for _, m := range f.Milestones {
			if !m.Done {
				lines = append(lines, printable(m.Text))
			}
		}
		if f.NeedsPace {
			lines = append(lines, styleDim.Render(setPaceText(t.ID)))
		}
		for i, line := range lines {
			label := ""
			if i == 0 {
				label = "Forecast:"
			}
			fmt.Fprintf(b, "%s%s\n", styleLabel.Render(pad(label, labels)), line)
		}
		if f.Triage != nil {
			fmt.Fprintf(b, "%s%s\n", styleLabel.Render(pad("To consider:", labels)), printable(f.Triage.Text))
		}
	}
	if len(t.Tasks) > 0 {
		fmt.Fprintf(b, "%s\n", styleLabel.Render("Tasks:"))
		for _, task := range t.Tasks {
			fmt.Fprintf(b, "  %s %s\n", styleAccent.Render(task.ID), taskLine(task))
		}
	}
	// Each problem names its file: topic.toml or tasks.jsonl.
	for _, p := range t.SettingsProblems {
		fmt.Fprintf(b, "%s\n", styleWarn.Render(printable(p)))
	}
}

// writePlanSettings writes the plan settings an update shows.
func writePlanSettings(w io.Writer, t core.Topic) {
	if t.State != "" && t.State != core.TopicActive {
		fmt.Fprintf(w, "  State: %s\n", t.State)
	}
	if t.Deadline != "" {
		fmt.Fprintf(w, "  Deadline: %s\n", t.Deadline)
	}
	if t.Forecast != nil && t.Forecast.Pace != "" {
		fmt.Fprintf(w, "  Pace: %s\n", t.Forecast.Pace)
	} else if len(t.Pace) > 0 {
		var parts []string
		for _, p := range t.Pace {
			part := strconv.FormatFloat(p.HoursPerWeek, 'f', -1, 64) + " h/week"
			if p.From != "" {
				part += " from " + p.From
			}
			parts = append(parts, part)
		}
		fmt.Fprintf(w, "  Pace: %s\n", strings.Join(parts, ", then "))
	}
	if t.NewCardsPerDay != core.NewCardsPerDay {
		fmt.Fprintf(w, "  New Cards a day: %d\n", t.NewCardsPerDay)
	}
	if f := t.Forecast; f != nil {
		for _, m := range f.Milestones {
			if !m.Done {
				fmt.Fprintf(w, "  %s\n", printable(m.Text))
			}
		}
		if f.Triage != nil {
			fmt.Fprintf(w, "  %s %s\n", styleLabel.Render("To consider:"), printable(f.Triage.Text))
		}
	}
}
