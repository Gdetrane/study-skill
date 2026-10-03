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
			"without a Next step, what changed since the last Checkpoint. With --session and --focus, it records\n" +
			"the Focus you chose for that open Session instead of opening another.",
		Example: "  study session open c --energy half\n  study session open c --energy full --focus learn\n" +
			"  study session open c --session <id> --focus practice",
		Args: exactArgs(1),
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
			if open.Session != "" {
				return writeSessionFocused(a.out, res)
			}
			return writeSessionOpened(a.out, res, c.Now())
		},
	}
	openCmd.Flags().StringVar(&open.Energy, "energy", "", "your Energy: full, half or fumes")
	openCmd.Flags().StringVar(&open.Focus, "focus", "", "what the Session is for: learn, practice, reviews or explore")
	openCmd.Flags().StringVar(&open.Session, "session", "", "with --focus, the open Session to record the chosen Focus for")
	openCmd.Flags().BoolVar(&open.DryRun, "dry-run", false, "show the result without recording the Session")

	var closeSpec core.CloseSpec
	closeCmd := &cobra.Command{
		Use:   "close <topic> --next-step STEP",
		Short: "Close the Session with a Next step",
		Long: "Close the open Session with a Next step that starts with a verb and says what to act on, and the\n" +
			"context needed to take it. You see it first when you come back. --session gives a Session that was\n" +
			"left unclosed the note it never got. Closing saves the work with a Checkpoint.",
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
			if _, err := fmt.Fprintf(a.out, "%s the Session. Next step: %s\n", verb, styleAccent.Render(printable(res.NextStep.Step))); err != nil {
				return err
			}
			return writeTurnCheckpoint(a.out, res.Topic, res.TurnCheckpoint)
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
			"resumes from it. Reaching it saves the work with a Checkpoint.",
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

// suggestionWords are how human output names a suggestion.
var suggestionWords = map[string]string{
	core.SuggestPlan:        "plan the Syllabus",
	core.SuggestStop:        "stop here",
	core.SuggestResumeTopic: "resume the Topic, or pick another",
}

// writeSessionOpened shows where the learner stopped, what their Energy
// suggests, whether Cards are ready, and the Sessions left unclosed with
// what changed since the last Checkpoint.
func writeSessionOpened(w io.Writer, res core.SessionOpened, now time.Time) error {
	var b strings.Builder
	verb := "Opened"
	if res.DryRun {
		verb = "Would open"
	}
	fmt.Fprintf(&b, "%s a Session on %s.\n", verb, styleAccent.Render(res.Topic))
	if res.Paused {
		fmt.Fprintf(&b, "%s\n", styleWarn.Render(pausedText(res.Topic)))
	}
	if res.LongGap {
		fmt.Fprintf(&b, "%s\n", styleDim.Render("Welcome back: start with a short recap and a two-minute warm-up."))
	}
	labels := widest([]string{"Break point:", "Next step:", "Suggested:"}) + 2
	writeResume(&b, res.Resume, labels, now)
	if s := res.Suggested; s != nil {
		words := s.Suggest
		if w, ok := suggestionWords[words]; ok {
			words = w
		}
		fmt.Fprintf(&b, "%s%s %s\n", styleLabel.Render(pad("Suggested:", labels)), styleAccent.Render(words), styleDim.Render("("+s.Reason+")"))
	}
	writeCardsReady(&b, res.Cards, labels, now)
	if len(res.Unclosed) > 0 {
		b.WriteString("\n")
		for _, u := range res.Unclosed {
			fmt.Fprintf(&b, "%s\n", styleWarn.Render("The Session "+u.ID+" opened "+u.Opened.In(now.Location()).Format("2 Jan 15:04")+
				" ended without a Next step."))
		}
		writeWorkChanges(&b, res.Changes)
		fmt.Fprintf(&b, "  %s\n", styleDim.Render("Give each its Next step with: study session close "+res.Topic+
			" --session <id> --next-step \"...\""))
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// writeWorkChanges lists what changed since the last Checkpoint.
func writeWorkChanges(b *strings.Builder, ch *core.WorkChanges) {
	since := "since the last Checkpoint"
	if ch != nil && ch.Since == "" {
		since = "since the Topic was created (no Checkpoint yet)"
	}
	switch {
	case ch == nil:
	case ch.Error != "":
		fmt.Fprintf(b, "  %s\n", styleDim.Render(printable(ch.Error)))
	case len(ch.Files) == 0:
		fmt.Fprintf(b, "  %s\n", styleDim.Render("Nothing changed "+since+"."))
	default:
		fmt.Fprintf(b, "  Changed %s:\n", since)
		for _, f := range ch.Files {
			fmt.Fprintf(b, "    %s %s\n", pad(f.Change, 9), printable(f.Path))
		}
		if ch.More > 0 {
			fmt.Fprintf(b, "    %s\n", styleDim.Render(fmt.Sprintf("and %d more", ch.More)))
		}
	}
}

func writeBreakPointReached(w io.Writer, res core.BreakPointReached) error {
	verb := "Reached"
	switch {
	case res.DryRun:
		verb = "Would reach"
	case !res.Changed:
		verb = "Already at"
	}
	text := printable(res.BreakPoint.ID)
	if res.BreakPoint.Describe != "" {
		text += " (" + printable(res.BreakPoint.Describe) + ")"
	}
	if _, err := fmt.Fprintf(w, "%s Break point %s of %s. Next step: %s\n", verb, text, printable(res.Lesson),
		styleAccent.Render(printable(res.NextStep.Step))); err != nil {
		return err
	}
	return writeTurnCheckpoint(w, res.Topic, res.TurnCheckpoint)
}

// writeSessionFocused confirms the Focus recorded for an open Session.
func writeSessionFocused(w io.Writer, res core.SessionOpened) error {
	verb := "Recorded"
	if res.DryRun {
		verb = "Would record"
	}
	_, err := fmt.Fprintf(w, "%s the Focus %s for Session %s of %s.\n", verb, styleAccent.Render(res.Focus),
		printable(res.Session), styleAccent.Render(res.Topic))
	return err
}

// writeTurnCheckpoint says whether a stop saved the work, and how to save it
// when the Checkpoint could not be taken.
func writeTurnCheckpoint(w io.Writer, topic string, tc core.TurnCheckpoint) error {
	var err error
	switch {
	case tc.CheckpointError != "":
		_, err = fmt.Fprintf(w, "%s\n", styleWarn.Render("Not saved: "+printable(tc.CheckpointError)+
			". Once that is fixed, run: study checkpoint --topic "+topic+" --role "+tc.CheckpointRole))
	case tc.Checkpoint != nil && tc.Checkpoint.Committed:
		_, err = fmt.Fprintf(w, "%s\n", styleDim.Render("Saved."))
	}
	return err
}
