package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mordor-forge/lamplight/v2/internal/core"
)

// lessonCommand is study lesson: one Lesson, where it sits in the Syllabus,
// and what its YAML header declares.
func (a *app) lessonCommand() *cobra.Command {
	var topic string
	cmd := &cobra.Command{
		Use:   "lesson <lesson>",
		Short: "Show a Lesson: its progress, its file, its Check and its Break points",
		Long: "Show one Lesson: its number, Milestone, status and Phase, its file, and what its YAML header declares:\n" +
			"the Check's criteria and the Break points. Nothing is run. For Attempts and grades, see study results.",
		Example: `  study lesson pointers --topic c`,
		Args:    exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := core.Open(a.opts)
			if err != nil {
				return a.fail(err)
			}
			if topic == "" {
				if topic, err = topicForReading(cmd, c, nil); err != nil {
					return a.fail(err)
				}
			}
			d, err := c.LessonOf(cmd.Context(), topic, args[0])
			if err != nil {
				return a.fail(err)
			}
			if a.json {
				return a.writeJSON(envelope{OK: true, Data: d})
			}
			return writeLessonDetail(a.out, d)
		},
	}
	cmd.Flags().StringVar(&topic, "topic", "", "the Topic's id; defaults to the Active topic")
	return cmd
}

// historyCommand is study history: what happened in a Topic, newest first.
func (a *app) historyCommand() *cobra.Command {
	var q core.HistoryQuery
	cmd := &cobra.Command{
		Use:   "history [topic]",
		Short: "Show what happened in a Topic, newest first",
		Long: "Show a Topic's most recent Events, newest first, each summarised in a sentence. Filter by an Event\n" +
			"type prefix, such as card. or attempt, or by a Lesson.",
		Example: `  study history c
  study history --lesson pointers --limit 20
  study history c --type card. --json`,
		Args: maxArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := core.Open(a.opts)
			if err != nil {
				return a.fail(err)
			}
			topic, err := topicForReading(cmd, c, args)
			if err != nil {
				return a.fail(err)
			}
			v, err := c.HistoryOf(cmd.Context(), topic, q)
			if err != nil {
				return a.fail(err)
			}
			if a.json {
				return a.writeJSON(envelope{OK: true, Data: v})
			}
			return writeHistory(a.out, v)
		},
	}
	cmd.Flags().IntVar(&q.Limit, "limit", core.DefaultHistoryLimit,
		fmt.Sprintf("most Events to show, up to %d", core.MaxHistoryLimit))
	cmd.Flags().StringVar(&q.Type, "type", "", "only Events whose type starts with this, such as card. or attempt")
	cmd.Flags().StringVar(&q.Lesson, "lesson", "", "only Events about this Lesson")
	return cmd
}

func writeLessonDetail(w io.Writer, d core.LessonDetail) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Lesson %s %s (%s) in %s\n", d.Number, styleAccent.Render(printable(d.Title)), d.ID, d.Topic)
	fmt.Fprintf(&b, "  Milestone %d: %s\n", d.Milestone.Number, printable(d.Milestone.Title))
	status := strings.ReplaceAll(d.Status, "_", " ")
	if d.Phase != "" {
		status += ", " + d.Phase
	}
	fmt.Fprintf(&b, "  Status: %s\n", status)
	fmt.Fprintf(&b, "  File: %s\n", printable(d.Path))
	if !d.HeaderReadable {
		fmt.Fprintf(&b, "%s\n", styleWarn.Render(sentence(printable(d.HeaderError))))
	}
	if len(d.Check) > 0 {
		fmt.Fprintln(&b, styleLabel.Render("Check:"))
		ids := make([]string, len(d.Check))
		for i, c := range d.Check {
			ids[i] = c.ID
		}
		width := widest(ids) + 2
		for _, c := range d.Check {
			what := c.Describe
			switch {
			case c.Kind == core.CriterionRubric:
				what = c.Rubric
			case what == "":
				what = strings.Join(c.Command, " ")
			}
			fmt.Fprintf(&b, "  %s%s: %s\n", styleLabel.Render(pad(c.ID, width)), c.Kind, printable(what))
		}
		switch {
		case d.CheckShown:
			fmt.Fprintln(&b, styleDim.Render("  This is the Check shown to the learner."))
		case d.ShownCheck != "":
			fmt.Fprintln(&b, styleWarn.Render("  The Check changed since it was shown to the learner: show it again."))
		}
	}
	if len(d.BreakPoints) > 0 {
		fmt.Fprintln(&b, styleLabel.Render("Break points:"))
		for _, p := range d.BreakPoints {
			fmt.Fprintf(&b, "  %s: %s\n", p.ID, printable(p.Describe))
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func writeHistory(w io.Writer, v core.HistoryView) error {
	var b strings.Builder
	if len(v.Entries) == 0 {
		fmt.Fprintf(&b, "Nothing has happened in %s that matches.\n", v.Topic)
	}
	for _, e := range v.Entries {
		about := ""
		if e.Lesson != "" {
			about = styleDim.Render(" (" + e.Lesson + ")")
		}
		fmt.Fprintf(&b, "%s  %s%s\n", styleDim.Render(e.At.Local().Format("2 Jan 2006 15:04")), printable(e.Summary), about)
	}
	if v.More {
		fmt.Fprintln(&b, styleDim.Render("Older Events match too: raise --limit to see them."))
	}
	_, err := io.WriteString(w, b.String())
	return err
}
