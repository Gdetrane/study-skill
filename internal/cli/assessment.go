package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mordor-forge/lamplight/v2/internal/core"
)

// maxStdinAssessment bounds an Assessment read from stdin.
const maxStdinAssessment = 1 << 20

func (a *app) assessmentCommand() *cobra.Command {
	group := &cobra.Command{
		Use:   "assessment",
		Short: "Record a Topic's Assessments and list them",
		Args:  noArgs,
		RunE:  a.groupHelp,
	}
	var file string
	var dryRun bool
	record := &cobra.Command{
		Use:   "record <topic>",
		Short: "Record an Assessment, read from a JSON file",
		Long: "Record a placement Assessment, run when a Topic is created, or the Assessment that ends a Milestone.\n" +
			"The file holds JSON in the shape the assessment_record tool takes: kind, milestone, items (area,\n" +
			"question, outcome: correct, partly, incorrect or not_reached), summary, minutes, time_box, level and\n" +
			"notes, and request, an id that makes a retry record nothing. With a level, the Assessment sets the\n" +
			"Topic's Level. Without a request id, the same Assessment as the latest records nothing unless the\n" +
			"Level changed since. - reads the JSON from stdin.",
		Example: `  study assessment record go --file notes/placement.json
  study assessment record go --file - < assessment.json`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := core.Open(a.opts)
			if err != nil {
				return a.fail(err)
			}
			var spec core.AssessmentSpec
			switch file {
			case "":
				return a.fail(usageError{fmt.Errorf("give the Assessment with --file, or --file - for stdin")})
			case "-":
				data, err := io.ReadAll(io.LimitReader(a.stdin, maxStdinAssessment+1))
				if err != nil {
					return a.fail(err)
				}
				spec, err = core.ParseAssessment(data, "stdin")
				if err != nil {
					return a.fail(err)
				}
			default:
				if spec, err = c.ReadAssessmentFile(file); err != nil {
					return a.fail(err)
				}
			}
			spec.DryRun = dryRun
			r, err := c.RecordAssessment(cmd.Context(), args[0], spec)
			if err != nil {
				return a.fail(err)
			}
			if a.json {
				return a.writeJSON(envelope{OK: true, Data: r})
			}
			return writeAssessmentRecorded(a.out, r)
		},
	}
	record.Flags().StringVar(&file, "file", "", "the Assessment as JSON; - reads stdin")
	record.Flags().BoolVar(&dryRun, "dry-run", false, "show the Assessment without recording it")

	list := &cobra.Command{
		Use:     "list [topic]",
		Short:   "List a Topic's Assessments and its Level",
		Example: "  study assessment list go",
		Args:    maxArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := core.Open(a.opts)
			if err != nil {
				return a.fail(err)
			}
			topic, err := topicForReading(cmd, c, args)
			if err != nil {
				return a.fail(err)
			}
			l, err := c.ListAssessments(cmd.Context(), topic)
			if err != nil {
				return a.fail(err)
			}
			if a.json {
				return a.writeJSON(envelope{OK: true, Data: l})
			}
			return writeAssessments(a.out, l)
		},
	}
	group.AddCommand(record, list)
	return group
}

func (a *app) hintCommand() *cobra.Command {
	group := &cobra.Command{
		Use:   "hint",
		Short: "Record the hints given while the learner practises",
		Args:  noArgs,
		RunE:  a.groupHelp,
	}
	var spec core.HintSpec
	var topic string
	record := &cobra.Command{
		Use:   "record <lesson>",
		Short: "Record a hint given for a Lesson",
		Long: "Record a hint given while the learner studies a Lesson: a nudge (a question or pointer), an\n" +
			"explanation of a concept again, or a step of the way to a solution. Hints are a signal for adapting\n" +
			"how the Topic is taught. --request makes a retry record nothing.",
		Example: `  study hint record pointers --kind nudge --requested-by learner --note "Asked what *p reads"
  study hint record pointers --kind step --requested-by agent --topic go`,
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
			spec.Lesson = args[0]
			r, err := c.RecordHint(cmd.Context(), topic, spec)
			if err != nil {
				return a.fail(err)
			}
			if a.json {
				return a.writeJSON(envelope{OK: true, Data: r})
			}
			return writeHintRecorded(a.out, r)
		},
	}
	record.Flags().StringVar(&topic, "topic", "", "the Topic's id; defaults to the Topic whose folder you are in")
	record.Flags().StringVar(&spec.Kind, "kind", core.HintNudge, "nudge, explanation or step")
	record.Flags().StringVar(&spec.RequestedBy, "requested-by", "", "learner, when the learner asked, or agent, when it was offered")
	_ = record.RegisterFlagCompletionFunc("requested-by", cobra.FixedCompletions(
		[]string{core.HintByLearner, core.HintByAgent}, cobra.ShellCompDirectiveNoFileComp))
	_ = record.RegisterFlagCompletionFunc("kind", cobra.FixedCompletions(
		[]string{core.HintNudge, core.HintExplanation, core.HintStep}, cobra.ShellCompDirectiveNoFileComp))
	record.Flags().StringVar(&spec.Note, "note", "", "what the hint was about, in a few words")
	record.Flags().StringVar(&spec.Request, "request", "", "an id for this hint, so a retry records nothing")
	record.Flags().BoolVar(&spec.DryRun, "dry-run", false, "show the hint without recording it")
	group.AddCommand(record)
	return group
}

// signalsCommand is an interface for agents with a shell: the signals are
// for adapting how a Topic is taught, never shown to the learner as counts
// or scores, so the command is hidden and answers in JSON only.
func (a *app) signalsCommand() *cobra.Command {
	return &cobra.Command{
		Use:    "signals [topic]",
		Short:  "For agents: a Topic's learning signals, in JSON",
		Hidden: true,
		Args:   maxArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !a.json {
				_, err := fmt.Fprintln(a.out, "study signals is for agents, which read it with --json to adapt how they teach.")
				return err
			}
			c, err := core.Open(a.opts)
			if err != nil {
				return a.fail(err)
			}
			topic, err := topicForReading(cmd, c, args)
			if err != nil {
				return a.fail(err)
			}
			s, err := c.SignalsOf(cmd.Context(), topic)
			if err != nil {
				return a.fail(err)
			}
			return a.writeJSON(envelope{OK: true, Data: s})
		},
	}
}

// describeLevel says what a Level is and where it came from.
func describeLevel(l *core.LevelInfo) string {
	switch l.Source {
	case core.LevelFromAssessment:
		return l.Level + styleDim.Render(" (set by an Assessment)")
	case core.LevelFromImport:
		return l.Level + styleDim.Render(" (v1's estimate)")
	default:
		return l.Level + styleDim.Render(" (your choice)")
	}
}

func writeAssessmentRecorded(w io.Writer, r core.AssessmentRecorded) error {
	var b strings.Builder
	as := r.Assessment
	what := "placement Assessment"
	if as.Kind == core.AssessmentMilestone {
		what = "Assessment of Milestone " + printable(as.Milestone)
	}
	switch {
	case !r.Changed:
		fmt.Fprintf(&b, "This %s was already recorded.\n", what)
	case r.DryRun:
		fmt.Fprintf(&b, "Would record the %s.\n", what)
	default:
		fmt.Fprintf(&b, "Recorded the %s.\n", what)
	}
	writeAssessmentBody(&b, as)
	if r.Level != nil {
		fmt.Fprintf(&b, "  %s%s\n", styleLabel.Render("Level: "), describeLevel(r.Level))
	}
	if r.Next != nil {
		fmt.Fprintf(&b, "  %s%s\n", styleLabel.Render("Next: "), printable(r.Next.Text))
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// writeAssessmentBody shows what an Assessment found: its summary, the
// areas to work on and those to confirm during Lessons.
func writeAssessmentBody(b *strings.Builder, as core.Assessment) {
	for _, line := range strings.Split(as.Summary, "\n") {
		fmt.Fprintf(b, "  %s\n", printable(line))
	}
	if len(as.Weak) > 0 {
		fmt.Fprintf(b, "  %s%s\n", styleLabel.Render("To work on: "), printableList(as.Weak))
	}
	if len(as.Confirm) > 0 {
		fmt.Fprintf(b, "  %s%s\n", styleLabel.Render("To confirm during Lessons: "), printableList(as.Confirm))
	}
	if as.Notes != nil {
		fmt.Fprintf(b, "  %s%s\n", styleLabel.Render("Notes: "), styleDim.Render(printable(as.Notes.Path)))
	}
}

func printableList(list []string) string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = printable(s)
	}
	return strings.Join(out, ", ")
}

func writeAssessments(w io.Writer, l core.AssessmentList) error {
	var b strings.Builder
	if l.Level != nil {
		fmt.Fprintf(&b, "%s%s\n", styleLabel.Render("Level: "), describeLevel(l.Level))
	}
	if len(l.Assessments) == 0 {
		fmt.Fprintf(&b, "No Assessments yet in %s.\n", printable(l.Topic))
	}
	for _, as := range l.Assessments {
		what := "Placement"
		if as.Kind == core.AssessmentMilestone {
			what = "Milestone " + printable(as.Milestone)
		}
		level := ""
		if as.Level != "" {
			level = styleDim.Render(", Level " + as.Level)
		}
		fmt.Fprintf(&b, "\n%s %s%s\n", styleAccent.Render(what), styleDim.Render(as.At.Format("2 Jan 2006")), level)
		writeAssessmentBody(&b, as)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func writeHintRecorded(w io.Writer, r core.HintRecorded) error {
	h := r.Hint
	who := "offered unasked"
	if h.RequestedBy == core.HintByLearner {
		who = "asked for by the learner"
	}
	var err error
	switch {
	case !r.Changed:
		_, err = fmt.Fprintf(w, "This hint for %s was already recorded.\n", printable(h.Lesson))
	case r.DryRun:
		_, err = fmt.Fprintf(w, "Would record a %s hint for %s, %s.\n", h.Kind, printable(h.Lesson), who)
	default:
		_, err = fmt.Fprintf(w, "Recorded a %s hint for %s, %s.\n", h.Kind, printable(h.Lesson), who)
	}
	return err
}
