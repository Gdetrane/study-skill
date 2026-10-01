package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mordor-forge/lamplight/v2/internal/core"
)

// sessionCommand is study session: opening and closing Sessions and
// reaching Break points, the same operations as the MCP tools.
func (a *app) sessionCommand() *cobra.Command {
	group := &cobra.Command{
		Use:   "session",
		Short: "Open and close Sessions, and record the Break points reached",
		Args:  noArgs,
		RunE:  a.groupHelp,
	}

	var open core.SessionSpec
	openCmd := &cobra.Command{
		Use:   "open <topic>",
		Short: "Open a Session and show where you stopped",
		Long: "Open a Session on a Topic, which also makes it the most recent Topic. It shows where you stopped,\n" +
			"suggests a Focus from your Energy when you have not chosen one, and, when the last Session ended\n" +
			"without a Next step, what changed since the last Checkpoint.",
		Example: "  study session open c --energy half\n  study session open c --energy full --focus learn",
		Args:    exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := core.Open(a.opts)
			if err != nil {
				return a.fail(err)
			}
			res, err := c.OpenSession(cmd.Context(), args[0], open)
			if err != nil {
				return a.fail(err)
			}
			if a.json {
				return a.writeJSON(envelope{OK: true, Data: res})
			}
			return writeSessionOpened(a.out, res, c.Now())
		},
	}
	openCmd.Flags().StringVar(&open.Energy, "energy", "", "your Energy: full, half or fumes")
	openCmd.Flags().StringVar(&open.Focus, "focus", "", "what the Session is for: learn, practice, reviews or explore")
	openCmd.Flags().BoolVar(&open.DryRun, "dry-run", false, "show the result without recording the Session")

	var closeSpec core.CloseSpec
	closeCmd := &cobra.Command{
		Use:   "close <topic> --next-step STEP",
		Short: "Close the Session with a Next step",
		Long: "Close the open Session with a Next step that starts with a verb and says what to act on, and the\n" +
			"context needed to take it. You see it first when you come back. --session gives a Session that was\n" +
			"left unclosed the note it never got.",
		Example: "  study session close c --next-step \"Fix the off-by-one in parse.go\" --context \"Line 42\"",
		Args:    exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := core.Open(a.opts)
			if err != nil {
				return a.fail(err)
			}
			res, err := c.CloseSession(cmd.Context(), args[0], closeSpec)
			if err != nil {
				return a.fail(err)
			}
			if a.json {
				return a.writeJSON(envelope{OK: true, Data: res})
			}
			verb := "Closed"
			if res.DryRun {
				verb = "Would close"
			}
			_, err = fmt.Fprintf(a.out, "%s the Session. Next step: %s\n", verb, styleAccent.Render(printable(res.NextStep.Step)))
			return err
		},
	}
	closeCmd.Flags().StringVar(&closeSpec.NextStep, "next-step", "", "the next action, starting with a verb (required)")
	closeCmd.Flags().StringVar(&closeSpec.Context, "context", "", "what is needed to take the Next step")
	closeCmd.Flags().StringVar(&closeSpec.Session, "session", "", "the Session to close; the latest when left out")
	closeCmd.Flags().BoolVar(&closeSpec.DryRun, "dry-run", false, "show the result without closing the Session")

	var point core.BreakPointSpec
	pointCmd := &cobra.Command{
		Use:   "break-point <topic> <lesson> <break-point> --next-step STEP",
		Short: "Record a Break point reached, with a Next step",
		Long: "Record that you reached one of the Break points a Lesson declares under break_points: in its YAML\n" +
			"header, with a Next step that starts with a verb. The Session can stop there, and the next one\n" +
			"resumes from it.",
		Example: "  study session break-point c pointers arrays --next-step \"Write the swap function\"",
		Args:    exactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := core.Open(a.opts)
			if err != nil {
				return a.fail(err)
			}
			point.Lesson, point.BreakPoint = args[1], args[2]
			res, err := c.ReachBreakPoint(cmd.Context(), args[0], point)
			if err != nil {
				return a.fail(err)
			}
			if a.json {
				return a.writeJSON(envelope{OK: true, Data: res})
			}
			return writeBreakPointReached(a.out, res)
		},
	}
	pointCmd.Flags().StringVar(&point.NextStep, "next-step", "", "the next action, starting with a verb (required)")
	pointCmd.Flags().StringVar(&point.Context, "context", "", "what is needed to take the Next step")
	pointCmd.Flags().BoolVar(&point.DryRun, "dry-run", false, "show the result without recording it")

	group.AddCommand(openCmd, closeCmd, pointCmd)
	return group
}

// writeSessionOpened shows where the learner stopped, the Focus their Energy
// suggests, whether Cards are ready, and a Session left unclosed with what
// changed since the last Checkpoint.
func writeSessionOpened(w io.Writer, res core.SessionOpened, now time.Time) error {
	var b strings.Builder
	verb := "Opened"
	if res.DryRun {
		verb = "Would open"
	}
	fmt.Fprintf(&b, "%s a Session on %s.\n", verb, styleAccent.Render(res.Topic))
	if res.LongGap {
		fmt.Fprintf(&b, "%s\n", styleDim.Render("Welcome back: start with a short recap and a two-minute warm-up."))
	}
	labels := widest([]string{"Break point:", "Next step:", "Suggested:"}) + 2
	writeResume(&b, res.Resume, labels)
	if s := res.Suggested; s != nil {
		focus := s.Focus
		if focus == "" {
			focus = "stop here"
		}
		fmt.Fprintf(&b, "%s%s %s\n", styleLabel.Render(pad("Suggested:", labels)), styleAccent.Render(focus), styleDim.Render("("+s.Reason+")"))
	}
	if c := res.Cards; c != nil {
		switch {
		case c.Ready:
			fmt.Fprintf(&b, "%s%s\n", styleLabel.Render(pad("Cards:", labels)), "ready to review")
		case c.NextDue != nil:
			fmt.Fprintf(&b, "%s%s\n", styleLabel.Render(pad("Cards:", labels)),
				styleDim.Render("next due "+c.NextDue.In(now.Location()).Format("2 Jan 2006")))
		}
	}
	if u := res.Unclosed; u != nil {
		fmt.Fprintf(&b, "\n%s\n", styleWarn.Render("The Session opened "+u.Opened.In(now.Location()).Format("2 Jan 15:04")+
			" ended without a Next step."))
		switch {
		case u.ChangesError != "":
			fmt.Fprintf(&b, "  %s\n", styleDim.Render(u.ChangesError))
		case len(u.Changes) == 0:
			fmt.Fprintf(&b, "  %s\n", styleDim.Render("Nothing changed since the last Checkpoint."))
		default:
			fmt.Fprintf(&b, "  %s\n", "Changed since the last Checkpoint:")
			for _, f := range u.Changes {
				fmt.Fprintf(&b, "    %s %s\n", pad(f.Change, 9), printable(f.Path))
			}
			if u.MoreChanges > 0 {
				fmt.Fprintf(&b, "    %s\n", styleDim.Render(fmt.Sprintf("and %d more", u.MoreChanges)))
			}
		}
		fmt.Fprintf(&b, "  %s\n", styleDim.Render("Give it its Next step with: study session close "+res.Topic+
			" --session "+u.ID+" --next-step \"...\""))
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func writeBreakPointReached(w io.Writer, res core.BreakPointReached) error {
	verb := "Reached"
	switch {
	case res.DryRun:
		verb = "Would reach"
	case !res.Changed:
		verb = "Already at"
	}
	text := res.BreakPoint.ID
	if res.BreakPoint.Describe != "" {
		text += " (" + printable(res.BreakPoint.Describe) + ")"
	}
	_, err := fmt.Fprintf(w, "%s Break point %s of %s. Next step: %s\n", verb, text, res.Lesson,
		styleAccent.Render(printable(res.NextStep.Step)))
	return err
}
