package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/mordor-forge/lamplight/v2/internal/core"
)

// syllabusCommand is study syllabus: a Topic's Syllabus with each Lesson's
// progress and the Revisions waiting for the learner.
func (a *app) syllabusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "syllabus [topic]",
		Short: "Show a Topic's Syllabus, its progress and any Revision waiting for you",
		Example: `  study syllabus c
  study syllabus --json       # the Active topic`,
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
			v, err := c.SyllabusOf(cmd.Context(), topic)
			if err != nil {
				return a.fail(err)
			}
			if a.json {
				return a.writeJSON(envelope{OK: true, Data: v})
			}
			return writeSyllabus(a.out, v)
		},
	}
}

// topicForReading names the Topic a read is about: the one given, or the
// Active topic.
func topicForReading(cmd *cobra.Command, c *core.Core, args []string) (string, error) {
	if len(args) > 0 {
		return args[0], nil
	}
	status, err := c.Status(cmd.Context())
	if err != nil {
		return "", err
	}
	if status.ActiveTopic == nil {
		return "", usageError{errors.New("there is no Active topic: name the Topic")}
	}
	return status.ActiveTopic.ID, nil
}

// revisionCommand is study revision: proposing a change to a Syllabus, and
// recording the learner's answer.
func (a *app) revisionCommand() *cobra.Command {
	group := &cobra.Command{
		Use:   "revision",
		Short: "Propose changes to a Syllabus, and approve or decline them",
		Args:  noArgs,
		RunE:  a.groupHelp,
	}

	var spec core.RevisionSpec
	var file string
	propose := &cobra.Command{
		Use:   "propose <topic>",
		Short: "Propose a change to a Topic's Syllabus; nothing changes until it is approved",
		Long: "Propose a change to a Topic's Syllabus, the first Syllabus included, from a file holding the whole\n" +
			"Syllabus as it would be afterwards (TOML like syllabus.toml, or JSON). With --from-file, propose\n" +
			"syllabus.toml as you edited it by hand, so the edit is approved instead of overwritten.",
		Example: `  study revision propose c --summary "Add a Lesson on maps" --syllabus next.toml
  study revision propose c --summary "Keep my edit" --from-file`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := core.Open(a.opts)
			if err != nil {
				return a.fail(err)
			}
			switch {
			case spec.FromFile && file != "":
				return a.fail(usageError{errors.New("give either --syllabus or --from-file, not both")})
			case !spec.FromFile && file == "":
				return a.fail(usageError{errors.New("give the proposed Syllabus with --syllabus <file>, or adopt syllabus.toml with --from-file")})
			case file != "":
				if spec.Syllabus, err = c.ReadSyllabusFile(file); err != nil {
					return a.fail(err)
				}
			}
			p, err := c.ProposeRevision(cmd.Context(), args[0], spec)
			if err != nil {
				return a.fail(err)
			}
			if a.json {
				return a.writeJSON(envelope{OK: true, Data: p})
			}
			return writeProposal(a.out, p)
		},
	}
	propose.Flags().StringVar(&spec.Summary, "summary", "", "what changes and why, in plain words (required)")
	propose.Flags().StringVar(&file, "syllabus", "", "a file holding the whole Syllabus as it would be after the Revision")
	propose.Flags().BoolVar(&spec.FromFile, "from-file", false, "propose syllabus.toml as it is, to keep a hand edit")
	propose.Flags().BoolVar(&spec.DryRun, "dry-run", false, "show the change without recording the proposal")

	group.AddCommand(propose, a.answerCommand(false), a.answerCommand(true))
	return group
}

// answerCommand is study revision apply or study revision decline. On a
// terminal, with no --learner-said, study asks the learner directly and
// records that; with --learner-said, the agent relays the learner's words
// from the conversation. A Revision already applied, or already declined,
// is reported as such on every path.
func (a *app) answerCommand(declining bool) *cobra.Command {
	var said string
	var dryRun bool
	use, short := "apply <topic> <revision>", "Apply a proposed Revision once the learner approves it"
	example := `  study revision apply c id042                 # asks you on this terminal
  study revision apply c id042 --learner-said "Yes, add it"`
	if declining {
		use, short = "decline <topic> <revision>", "Record that the learner said no to a proposed Revision"
		example = `  study revision decline c id042
  study revision decline c id042 --learner-said "No, keep the order"`
	}
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Long: short + ".\nOn a terminal, study shows the change and asks you directly. An agent relaying your answer from the\n" +
			"conversation passes your own words with --learner-said.",
		Example: example,
		Args:    exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := core.Open(a.opts)
			if err != nil {
				return a.fail(err)
			}
			ctx, topic, revision := cmd.Context(), args[0], args[1]
			answer := core.Approval{Via: core.ViaChat, LearnerSaid: said}
			decline := declining
			if said == "" && !dryRun {
				// Settled already: report the answer recorded, as with
				// --learner-said, and ask nothing.
				if declining {
					if r, err := c.DeclineRevision(ctx, topic, revision, core.Approval{}, true); err != nil {
						return a.fail(err)
					} else if !r.Changed {
						r.DryRun = false
						return a.answered(r)
					}
				} else if r, err := c.ApplyRevision(ctx, topic, revision, core.Approval{}, true); err == nil && !r.Changed {
					r.DryRun = false
					return a.answered(r)
				}
				// Ask only about a Revision the answer can settle: one that
				// can still be applied, or declined.
				var p core.RevisionProposal
				if declining {
					p, err = c.ProposedRevision(ctx, topic, revision)
				} else {
					p, err = c.Revision(ctx, topic, revision)
				}
				if err != nil {
					return a.fail(err)
				}
				var decided bool
				if answer, decline, decided, err = a.askOnTerminal(p, declining); err != nil {
					return a.fail(err)
				}
				if !decided {
					_, err := fmt.Fprintf(a.out, "Nothing recorded: Revision %s is still waiting for you.\n", revision)
					return err
				}
			}
			if decline {
				r, err := c.DeclineRevision(ctx, topic, revision, answer, dryRun)
				if err != nil {
					return a.fail(err)
				}
				return a.answered(r)
			}
			r, err := c.ApplyRevision(ctx, topic, revision, answer, dryRun)
			if err != nil {
				return a.fail(err)
			}
			return a.answered(r)
		},
	}
	cmd.Flags().StringVar(&said, "learner-said", "", "the learner's answer in the conversation, in their own words")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "check the Revision could be answered without recording anything")
	return cmd
}

// answered reports the result of applying or declining a Revision.
func (a *app) answered(r any) error {
	if a.json {
		return a.writeJSON(envelope{OK: true, Data: r})
	}
	var msg string
	switch r := r.(type) {
	case core.RevisionDeclined:
		switch {
		case r.DryRun:
			msg = "Would record that the learner declined Revision " + r.Revision
		case !r.Changed:
			msg = "The learner had already declined Revision " + r.Revision
		default:
			msg = "Recorded that the learner declined Revision " + r.Revision + "; the Syllabus is unchanged"
		}
	case core.RevisionApplied:
		switch {
		case r.DryRun:
			msg = "Would apply Revision " + r.Revision
		case !r.Changed:
			msg = "Revision " + r.Revision + " was already applied"
		default:
			msg = "Applied Revision " + r.Revision + " to the Syllabus of " + r.Topic
		}
	}
	_, err := fmt.Fprintln(a.out, msg+".")
	return err
}

// askOnTerminal shows the learner the change and asks them, on the terminal
// study runs in. It returns their answer, whether they declined, and whether
// they decided at all. Without a terminal, the learner's words must come
// from the conversation.
func (a *app) askOnTerminal(p core.RevisionProposal, declining bool) (core.Approval, bool, bool, error) {
	f, ok := a.stdin.(*os.File)
	if a.json || !ok || !term.IsTerminal(f.Fd()) {
		return core.Approval{}, false, false, usageError{errors.New(
			"run the command in a terminal so study can ask the learner, or pass their own words with --learner-said")}
	}
	question := p.Question()
	in := bufio.NewReader(f)
	fmt.Fprintf(a.stderr, "%s\n\n", question)
	prompt := "Apply it? [y]es, [n]o, or Enter to decide later: "
	if declining {
		prompt = "Decline it? [y]es, or Enter to decide later: "
	}
	fmt.Fprint(a.stderr, prompt)
	reply, err := in.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return core.Approval{}, false, false, err
	}
	reply = strings.ToLower(strings.TrimSpace(reply))
	decline := false
	switch {
	case reply == "y" || reply == "yes":
		decline = declining
	case !declining && (reply == "n" || reply == "no"):
		decline = true
	default:
		return core.Approval{}, false, false, nil
	}
	fmt.Fprint(a.stderr, "Anything to add? (Enter to skip): ")
	comment, err := in.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return core.Approval{}, false, false, err
	}
	return core.Approval{Via: core.ViaTerminal, LearnerSaid: strings.TrimSpace(comment), Shown: question}, decline, true, nil
}

func writeSyllabus(w io.Writer, v core.SyllabusView) error {
	var b strings.Builder
	if len(v.Milestones) == 0 {
		fmt.Fprintf(&b, "%s has no Syllabus yet.\n", styleAccent.Render(v.Topic))
	} else {
		fmt.Fprintf(&b, "%s %s\n", styleLabel.Render("Syllabus of"), styleAccent.Render(v.Topic))
	}
	var titles []string
	for _, m := range v.Milestones {
		for _, l := range m.Lessons {
			titles = append(titles, l.Title)
		}
	}
	width := widest(titles) + 2
	for _, m := range v.Milestones {
		details := []string{priorityWords(m.Priority)}
		if m.Target != "" {
			details = append(details, "by "+m.Target)
		}
		fmt.Fprintf(&b, "\n%s %s %s\n", styleLabel.Render(fmt.Sprint(m.Number)), styleLabel.Render(m.Title),
			styleDim.Render("("+strings.Join(details, ", ")+")"))
		if m.Outcome != "" {
			fmt.Fprintf(&b, "  %s\n", styleDim.Render(m.Outcome))
		}
		if mf := milestoneForecast(v.Forecast, m.ID); mf != nil && !mf.Done {
			fmt.Fprintf(&b, "  %s\n", styleAccent.Render(mf.Text))
		}
		for _, l := range m.Lessons {
			status := strings.ReplaceAll(l.Status, "_", " ")
			if l.Phase != "" && l.Status == core.LessonInProgress {
				status += ", " + l.Phase
			}
			hours := ""
			if l.Hours != 0 {
				hours = fmt.Sprintf("  %g h", l.Hours)
			}
			fmt.Fprintf(&b, "  %s %s%s%s\n", pad(l.Number, 5), pad(l.Title, width), styleStatus(l.Status).Render(status), styleDim.Render(hours))
		}
	}
	writeForecastNotes(&b, v.Topic, v.Forecast)
	if len(v.Proposals) > 0 {
		fmt.Fprintf(&b, "\n%s\n", styleWarn.Render("Waiting for the learner:"))
		for _, p := range v.Proposals {
			fmt.Fprintf(&b, "  Revision %s: %s\n", styleAccent.Render(p.Revision), p.Summary)
			if p.Stale {
				fmt.Fprintf(&b, "    %s\n", styleDim.Render("the Syllabus changed since: propose it again"))
				continue
			}
			for _, line := range strings.Split(p.Changes.Text, "\n") {
				fmt.Fprintf(&b, "    %s\n", line)
			}
		}
	}
	if v.EditedOutside {
		msg := "syllabus.toml was changed outside Lamplight: keep the edit with study revision propose " + v.Topic + " --from-file"
		if v.FileError != "" {
			msg = "syllabus.toml was changed outside Lamplight and cannot be adopted as it is: " + v.FileError
		}
		fmt.Fprintf(&b, "\n%s\n", styleWarn.Render(msg))
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func styleStatus(status string) interface{ Render(...string) string } {
	switch status {
	case core.LessonDone:
		return styleOK
	case core.LessonInProgress:
		return styleAccent
	}
	return styleDim
}

func priorityWords(p string) string {
	switch p {
	case core.PriorityIfTime:
		return "if time allows"
	case core.PriorityAfterDeadline:
		return "after the deadline"
	}
	return "must"
}

func writeProposal(w io.Writer, p core.RevisionProposal) error {
	verb := "Proposed"
	if p.DryRun {
		verb = "Would propose"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s Revision %s for %s: %s\n\n%s\n", verb, styleAccent.Render(p.Revision), p.Topic, p.Summary, p.Changes.Text)
	if !p.DryRun {
		fmt.Fprintf(&b, "\n%s\n", styleDim.Render("Nothing changes until the learner approves it: study revision apply "+p.Topic+" "+p.Revision))
	}
	_, err := io.WriteString(w, b.String())
	return err
}
