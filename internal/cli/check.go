package cli

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/spf13/cobra"

	"github.com/mordor-forge/lamplight/v2/internal/core"
)

// checkCommand is study check: the only way to run a Lesson's Check. It runs
// commands the agent wrote, so the agent runs it from its own shell, where
// its sandbox applies; the MCP server never runs a Check (ADR-0009).
func (a *app) checkCommand() *cobra.Command {
	var topic string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "check <lesson>",
		Short: "Run a Lesson's Check on the current work and record the Attempt",
		Long: "Run each run and held_out criterion of a Lesson's Check, from the YAML header of lessons/<lesson>.md, in\n" +
			"the Lesson's practice folder, and record the Attempt in the Topic's History. A run criterion passes when its\n" +
			"command exits with 0 and its results file, if any, does not say passed: false. Held-out results are\n" +
			"diagnostic and never decide the outcome; rubric items are graded with study rubric grade. Progress goes to\n" +
			"stderr. The exit code is 0 whenever the Attempt was recorded; its outcome is in the output.",
		Example: `  study check pointers --topic c
  study check pointers --json        # inside the Topic's folder`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := core.Open(a.opts)
			if err != nil {
				return a.fail(err)
			}
			if topic == "" {
				if topic, err = topicFromFolder(cmd, c); err != nil {
					return a.fail(err)
				}
			}
			attempt, err := c.RunCheck(cmd.Context(), topic, args[0], core.CheckOptions{
				Timeout: timeout, Progress: checkProgress(a.stderr, a.json),
			})
			if err != nil {
				return a.fail(err)
			}
			if a.json {
				return a.writeJSON(envelope{OK: true, Data: attempt})
			}
			return writeAttempt(a.out, attempt)
		},
	}
	cmd.Flags().StringVar(&topic, "topic", "", "the Topic's id; defaults to the Topic whose folder you are in")
	cmd.Flags().DurationVar(&timeout, "timeout", core.DefaultCheckTimeout, "longest each criterion may run")
	return cmd
}

// checkProgress reports a running Check on stderr: each criterion as it
// starts, and every so often while a long one runs. With --json only the
// reports of long runs are written, so a quick Check keeps stderr empty.
func checkProgress(w io.Writer, quiet bool) func(core.CheckProgress) {
	return func(p core.CheckProgress) {
		switch {
		case p.State == core.ProgressStarted && !quiet:
			fmt.Fprintf(w, "Running %s (%d of %d)…\n", printable(p.Criterion), p.Index, p.Total)
		case p.State == core.ProgressRunning:
			fmt.Fprintf(w, "  still running %s (%s)\n", printable(p.Criterion), p.Elapsed.Round(time.Second))
		}
	}
}

// topicFromFolder names the Topic whose folder the command runs in. Writes
// never fall back to the most recent Topic: outside a Topic's folder, the
// Topic must be named.
func topicFromFolder(cmd *cobra.Command, c *core.Core) (string, error) {
	status, err := c.Status(cmd.Context())
	if err != nil {
		return "", err
	}
	if t := status.ActiveTopic; t != nil && t.ChosenBy == core.ChosenByFolder {
		return t.ID, nil
	}
	return "", usageError{fmt.Errorf("name the Topic with --topic, or run the command inside the Topic's folder")}
}

func writeAttempt(w io.Writer, at core.Attempt) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Check of %s: %s\n", styleAccent.Render(at.Lesson), outcomeStyle(at.Outcome).Render(at.Outcome))
	ids := make([]string, len(at.Criteria))
	for i, c := range at.Criteria {
		ids[i] = c.ID
	}
	width := widest(ids) + 2
	for _, c := range at.Criteria {
		line := c.Outcome
		switch {
		case c.Reason != "":
			line += ": " + printable(c.Reason)
		case c.Outcome == core.OutcomeFailed && c.ExitCode != 0:
			line += fmt.Sprintf(" (exit %d)", c.ExitCode)
		}
		if c.Kind == core.CriterionHeldOut {
			line = "held_out, " + line
			switch {
			case c.Counted != nil && *c.Counted:
				line += ", counted"
			case c.Counted != nil:
				line += ", not counted: " + c.NotCounted
			}
		}
		fmt.Fprintf(&b, "  %s%s\n", styleLabel.Render(pad(c.ID, width)), line)
		writeScores(&b, c.Results, "    ")
		if c.Outcome != core.OutcomePassed && strings.TrimSpace(c.Output) != "" {
			for _, l := range strings.Split(strings.TrimRight(c.Output, "\n"), "\n") {
				fmt.Fprintf(&b, "    %s\n", styleDim.Render(printable(l)))
			}
		}
	}
	if at.Reason != "" {
		fmt.Fprintf(&b, "%s\n", styleWarn.Render(sentence(printable(at.Reason))))
	}
	fmt.Fprintf(&b, "%s\n", styleDim.Render("Recorded as Attempt "+at.ID+"."))
	_, err := io.WriteString(w, b.String())
	return err
}

func outcomeStyle(outcome string) lipgloss.Style {
	switch outcome {
	case core.OutcomeFailed:
		return styleWarn
	case core.OutcomeErrored:
		return styleFail
	}
	return styleOK
}

// writeScores shows what a results file reported: the score, the metrics in
// name order and the summary.
func writeScores(b *strings.Builder, s *core.CriterionScores, indent string) {
	if s == nil {
		return
	}
	var parts []string
	if s.Score != nil {
		max := 1.0
		if s.Max != nil {
			max = *s.Max
		}
		parts = append(parts, "score "+number(*s.Score)+" of "+number(max))
	}
	names := make([]string, 0, len(s.Metrics))
	for name := range s.Metrics {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		parts = append(parts, name+" "+number(s.Metrics[name]))
	}
	if len(parts) > 0 {
		fmt.Fprintf(b, "%s%s\n", indent, strings.Join(parts, ", "))
	}
	for _, l := range strings.Split(s.Summary, "\n") {
		if strings.TrimSpace(l) != "" {
			fmt.Fprintf(b, "%s%s\n", indent, printable(l))
		}
	}
}

func number(f float64) string { return strconv.FormatFloat(f, 'g', 6, 64) }

// rubricCommand is study rubric: grading a Lesson's rubric items.
func (a *app) rubricCommand() *cobra.Command {
	rubric := &cobra.Command{
		Use:   "rubric",
		Short: "Grade the rubric items of a Lesson's Check",
		Args:  noArgs,
		RunE:  a.groupHelp,
	}
	var topic, grade, note string
	var lookedAt []string
	var dryRun bool
	gradeCmd := &cobra.Command{
		Use:   "grade <lesson> <criterion>",
		Short: "Grade a rubric item for the Check shown to the learner and the work as it is now",
		Long: "Grade a rubric item met, partly or not_met, after the learner has checked their work against it. Completion\n" +
			"needs every rubric item graded for the current Check and work. --looked-at names files of the work in the\n" +
			"Lesson's practice folder, such as typed final answers or a photo of paper work; only their paths and\n" +
			"content hashes are recorded.",
		Example: `  study rubric grade pointers names --grade met --note "Every name says what it does"
  study rubric grade proofs written --grade partly --looked-at page1.jpg --topic maths`,
		Args: exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := core.Open(a.opts)
			if err != nil {
				return a.fail(err)
			}
			if topic == "" {
				if topic, err = topicFromFolder(cmd, c); err != nil {
					return a.fail(err)
				}
			}
			r, err := c.RecordRubricGrade(cmd.Context(), topic, core.RubricSpec{Lesson: args[0], Criterion: args[1],
				Grade: grade, Note: note, LookedAt: lookedAt, DryRun: dryRun})
			if err != nil {
				return a.fail(err)
			}
			if a.json {
				return a.writeJSON(envelope{OK: true, Data: r})
			}
			return writeRubricGraded(a.out, r)
		},
	}
	gradeCmd.Flags().StringVar(&topic, "topic", "", "the Topic's id; defaults to the Topic whose folder you are in")
	gradeCmd.Flags().StringVar(&grade, "grade", "", "met, partly or not_met")
	gradeCmd.Flags().StringVar(&note, "note", "", "what the grade is based on, for the learner")
	gradeCmd.Flags().StringArrayVar(&lookedAt, "looked-at", nil, "a file of the work the grade looked at (repeatable)")
	gradeCmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the grade without recording it")
	rubric.AddCommand(gradeCmd)
	return rubric
}

func writeRubricGraded(w io.Writer, r core.RubricGraded) error {
	verb := "Graded"
	switch {
	case !r.Changed:
		verb = "Already graded"
	case r.DryRun:
		verb = "Would grade"
	}
	_, err := fmt.Fprintf(w, "%s rubric item %s of %s: %s\n", verb, styleAccent.Render(r.Criterion), r.Lesson, r.Grade.Grade)
	if err == nil && r.Grade.Note != "" {
		for _, l := range strings.Split(r.Grade.Note, "\n") {
			fmt.Fprintf(w, "  %s\n", printable(l))
		}
	}
	return err
}

// resultsCommand is study results: a Lesson's Check, Attempts, rubric
// grades and held_out results, and whether it can be completed now.
func (a *app) resultsCommand() *cobra.Command {
	var topic string
	cmd := &cobra.Command{
		Use:   "results <lesson>",
		Short: "Show a Lesson's Check results and whether it can be completed",
		Long: "Show a Lesson's Check, its Attempts, the grade of each rubric item, each held_out criterion's counted\n" +
			"measurement, and whether the Lesson can be completed now. Nothing is run.",
		Example: `  study results pointers --topic c`,
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
			r, err := c.CheckResultsOf(cmd.Context(), topic, args[0])
			if err != nil {
				return a.fail(err)
			}
			if a.json {
				return a.writeJSON(envelope{OK: true, Data: r})
			}
			return writeCheckResults(a.out, r)
		},
	}
	cmd.Flags().StringVar(&topic, "topic", "", "the Topic's id; defaults to the Active topic")
	return cmd
}

func writeCheckResults(w io.Writer, r core.CheckResults) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Check of %s in %s\n", styleAccent.Render(r.Lesson), r.Topic)
	ids := make([]string, len(r.Check))
	for i, c := range r.Check {
		ids[i] = c.ID
	}
	width := widest(ids) + 2
	for _, c := range r.Check {
		what := c.Describe
		if c.Kind == core.CriterionRubric {
			what = c.Rubric
		}
		fmt.Fprintf(&b, "  %s%s: %s\n", styleLabel.Render(pad(c.ID, width)), c.Kind, printable(what))
	}
	if len(r.Attempts) > 0 {
		last := r.Attempts[len(r.Attempts)-1]
		fmt.Fprintf(&b, "Last Attempt: %s (%s)\n", outcomeStyle(last.Outcome).Render(last.Outcome), last.ID)
	} else {
		fmt.Fprintln(&b, "No Attempt yet.")
	}
	for _, g := range r.Rubric {
		switch {
		case g.Grade == nil:
			fmt.Fprintf(&b, "Rubric %s: not graded\n", g.Criterion)
		case g.Current:
			fmt.Fprintf(&b, "Rubric %s: %s\n", g.Criterion, g.Grade.Grade)
		default:
			fmt.Fprintf(&b, "Rubric %s: %s, for earlier work or an earlier Check: grade it again\n", g.Criterion, g.Grade.Grade)
		}
	}
	for _, h := range r.HeldOut {
		switch {
		case h.Counted == nil:
			fmt.Fprintf(&b, "Held-out %s: not measured yet\n", h.Criterion)
		default:
			fmt.Fprintf(&b, "Held-out %s, counted measurement (%s):\n", h.Criterion, h.CountedAttempt)
			writeScores(&b, h.Counted, "  ")
		}
		if h.Latest != nil {
			fmt.Fprintf(&b, "Held-out %s, latest run, not counted: %s (%s):\n", h.Criterion, h.LatestNotCounted, h.LatestAttempt)
			writeScores(&b, h.Latest, "  ")
		}
	}
	switch {
	case r.Done:
		fmt.Fprintln(&b, styleOK.Render("The Lesson is done."))
	case r.CanComplete:
		fmt.Fprintln(&b, styleOK.Render("The Lesson can be completed."))
	default:
		fmt.Fprintln(&b, styleWarn.Render(sentence(printable(r.Reason))))
	}
	if r.Next != nil {
		fmt.Fprintln(&b, sentence(r.Next.Text))
	}
	_, err := io.WriteString(w, b.String())
	return err
}
