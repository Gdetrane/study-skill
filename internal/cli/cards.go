package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mordor-forge/lamplight/v2/internal/core"
)

// cardCommand is study card: the scriptable Card operations, the same as the
// MCP tools. Terminal Reviews are study review.
func (a *app) cardCommand() *cobra.Command {
	group := &cobra.Command{
		Use:   "card",
		Short: "List, add, edit, suspend and delete a Topic's Cards",
		Args:  noArgs,
		RunE:  a.groupHelp,
	}
	result := func(cmd *cobra.Command, res core.CardChange, err error, verb string) error {
		if err != nil {
			return a.fail(err)
		}
		if a.json {
			return a.writeJSON(envelope{OK: true, Data: res})
		}
		return writeCardChange(a.out, res, verb)
	}

	var lesson string
	list := &cobra.Command{
		Use:     "list <topic>",
		Short:   "List a Topic's Cards in the order they were written",
		Example: "  study card list c\n  study card list c --lesson pointers",
		Args:    exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := core.Open(a.opts)
			if err != nil {
				return a.fail(err)
			}
			res, err := c.ListCards(cmd.Context(), args[0], core.CardQuery{Lesson: lesson})
			if err != nil {
				return a.fail(err)
			}
			if a.json {
				return a.writeJSON(envelope{OK: true, Data: res})
			}
			return writeCardList(a.out, res.Cards, c.Now())
		},
	}
	list.Flags().StringVar(&lesson, "lesson", "", "only the Cards of this Lesson; explore for Explore Cards")

	var due core.DueQuery
	dueCmd := &cobra.Command{
		Use:   "due <topic>",
		Short: "List the Cards to review now, sized to your Energy",
		Long: "List the Cards to review now: those due, then drafts waiting for their first Review, as many as\n" +
			"today's cap on new Cards allows. Without --limit, the list is sized to --energy, or to the open\n" +
			"Session's Energy. It never says how many more Cards are due.",
		Example: "  study card due c --energy half",
		Args:    exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := core.Open(a.opts)
			if err != nil {
				return a.fail(err)
			}
			res, err := c.DueCardsOf(cmd.Context(), args[0], due)
			if err != nil {
				return a.fail(err)
			}
			if a.json {
				return a.writeJSON(envelope{OK: true, Data: res})
			}
			if len(res.Cards) == 0 {
				_, err := fmt.Fprintln(a.out, "Nothing to review now.")
				return err
			}
			return writeCardList(a.out, res.Cards, c.Now())
		},
	}
	dueCmd.Flags().IntVar(&due.Limit, "limit", 0, fmt.Sprintf("most Cards to list, up to %d", core.MaxDueLimit))
	dueCmd.Flags().StringVar(&due.Energy, "energy", "", "full, half or fumes; sizes the list")
	energyCompletion(dueCmd)

	var spec core.CardSpec
	add := &cobra.Command{
		Use:   "add <topic>",
		Short: "Add a draft Card, from a Lesson or as an Explore Card",
		Example: `  study card add c --lesson pointers --prompt "What does *p give for int *p = &x?" --answer "The value of x"
  study card add c --prompt "Which header declares malloc?" --answer "stdlib.h"`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := core.Open(a.opts)
			if err != nil {
				return a.fail(err)
			}
			res, err := c.AddCard(cmd.Context(), args[0], spec)
			return result(cmd, res, err, "add")
		},
	}
	add.Flags().StringVar(&spec.Lesson, "lesson", "", "the Lesson the Card comes from; leave out for an Explore Card")
	add.Flags().StringVar(&spec.Prompt, "prompt", "", "the question, without its answer")
	add.Flags().StringVar(&spec.Answer, "answer", "", "the expected answer")
	add.Flags().BoolVar(&spec.DryRun, "dry-run", false, "show the Card that would be added without recording it")

	var edit core.CardEdit
	editCmd := &cobra.Command{
		Use:     "edit <topic> <card>",
		Short:   "Change a Card's prompt or answer, keeping its schedule",
		Example: `  study card edit c pointers.k3x9a2bq --answer "The value stored in x"`,
		Args:    exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := core.Open(a.opts)
			if err != nil {
				return a.fail(err)
			}
			edit.Card = args[1]
			res, err := c.EditCard(cmd.Context(), args[0], edit)
			return result(cmd, res, err, "edit")
		},
	}
	editCmd.Flags().StringVar(&edit.Prompt, "prompt", "", "the new prompt")
	editCmd.Flags().StringVar(&edit.Answer, "answer", "", "the new answer")
	editCmd.Flags().BoolVar(&edit.DryRun, "dry-run", false, "show the Card as it would be without recording it")

	var undo, suspendDryRun bool
	suspend := &cobra.Command{
		Use:     "suspend <topic> <card>",
		Short:   "Stop offering a Card for Review, or offer it again with --undo",
		Example: "  study card suspend c pointers.k3x9a2bq\n  study card suspend c pointers.k3x9a2bq --undo",
		Args:    exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := core.Open(a.opts)
			if err != nil {
				return a.fail(err)
			}
			res, err := c.SuspendCard(cmd.Context(), args[0], args[1], !undo, suspendDryRun)
			verb := "suspend"
			if undo {
				verb = "unsuspend"
			}
			return result(cmd, res, err, verb)
		},
	}
	suspend.Flags().BoolVar(&undo, "undo", false, "offer the Card for Review again")
	suspend.Flags().BoolVar(&suspendDryRun, "dry-run", false, "show the result without recording it")

	var deleteDryRun bool
	deleteCmd := &cobra.Command{
		Use:     "delete <topic> <card>",
		Short:   "Delete a Card for good",
		Example: "  study card delete c pointers.k3x9a2bq",
		Args:    exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := core.Open(a.opts)
			if err != nil {
				return a.fail(err)
			}
			res, err := c.DeleteCard(cmd.Context(), args[0], args[1], deleteDryRun)
			return result(cmd, res, err, "delete")
		},
	}
	deleteCmd.Flags().BoolVar(&deleteDryRun, "dry-run", false, "show the Card that would be deleted without recording it")

	var note string
	var flagDryRun bool
	flag := &cobra.Command{
		Use:     "flag <topic> <card>",
		Short:   "Flag a Card as wrong or unclear, so it gets fixed",
		Example: `  study card flag c pointers.k3x9a2bq --note "the answer is ambiguous"`,
		Args:    exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := core.Open(a.opts)
			if err != nil {
				return a.fail(err)
			}
			res, err := c.FlagCard(cmd.Context(), args[0], args[1], note, flagDryRun)
			return result(cmd, res, err, "flag")
		},
	}
	flag.Flags().StringVar(&note, "note", "", "what is wrong with the Card")
	flag.Flags().BoolVar(&flagDryRun, "dry-run", false, "show the result without recording it")

	var review core.ReviewSpec
	reviewCmd := &cobra.Command{
		Use:   "review <topic> <card>",
		Short: "Record one Review of a Card, for scripts; study review is the terminal session",
		Example: `  study card review c pointers.k3x9a2bq --rating good
  study card review c pointers.k3x9a2bq --draft keep --rating hard`,
		Args: exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := core.Open(a.opts)
			if err != nil {
				return a.fail(err)
			}
			review.Card = args[1]
			res, err := c.RecordReview(cmd.Context(), args[0], review)
			if err != nil {
				return a.fail(err)
			}
			if a.json {
				return a.writeJSON(envelope{OK: true, Data: res})
			}
			return writeReviewResult(a.out, res)
		},
	}
	reviewCmd.Flags().StringVar(&review.Rating, "rating", "", "again, hard, good or easy")
	reviewCmd.Flags().StringVar(&review.Draft, "draft", "", "at a draft's first Review: keep, edit or drop")
	reviewCmd.Flags().StringVar(&review.Prompt, "prompt", "", "the new prompt, with --draft edit")
	reviewCmd.Flags().StringVar(&review.Answer, "answer", "", "the new answer, with --draft edit")
	reviewCmd.Flags().BoolVar(&review.DryRun, "dry-run", false, "show the result without recording it")
	_ = reviewCmd.RegisterFlagCompletionFunc("rating", cobra.FixedCompletions(
		[]string{core.RatingAgain, core.RatingHard, core.RatingGood, core.RatingEasy}, cobra.ShellCompDirectiveNoFileComp))
	_ = reviewCmd.RegisterFlagCompletionFunc("draft", cobra.FixedCompletions(
		[]string{core.DraftKeep, core.DraftEdit, core.DraftDrop}, cobra.ShellCompDirectiveNoFileComp))

	group.AddCommand(list, dueCmd, add, editCmd, suspend, deleteCmd, flag, reviewCmd)
	return group
}

func energyCompletion(cmd *cobra.Command) {
	_ = cmd.RegisterFlagCompletionFunc("energy", cobra.FixedCompletions(
		[]string{core.EnergyFull, core.EnergyHalf, core.EnergyFumes}, cobra.ShellCompDirectiveNoFileComp))
}

// cardLabel names a Card for people: "Card 4 (pointers)".
func cardLabel(card core.Card) string {
	from := card.Lesson
	if from == "" {
		from = "explore"
	}
	return fmt.Sprintf("Card %d (%s)", card.Number, from)
}

// cardState describes a Card's state in a few words.
func cardState(card core.Card, now time.Time) string {
	var parts []string
	switch {
	case card.Draft:
		parts = append(parts, "draft")
	case !card.Due.After(now):
		parts = append(parts, "due")
	default:
		parts = append(parts, "due "+card.Due.In(now.Location()).Format("2 Jan 2006"))
	}
	if card.Suspended {
		parts = append(parts, "suspended")
	}
	if card.Flagged {
		parts = append(parts, "flagged")
	}
	return strings.Join(parts, ", ")
}

func writeCardList(w io.Writer, cards []core.Card, now time.Time) error {
	if len(cards) == 0 {
		_, err := fmt.Fprintln(w, "No Cards yet.")
		return err
	}
	var b strings.Builder
	for _, card := range cards {
		fmt.Fprintf(&b, "%s %s  %s\n", styleLabel.Render(cardLabel(card)), styleDim.Render(card.ID),
			styleDim.Render(cardState(card, now)))
		fmt.Fprintf(&b, "  %s\n  %s %s\n", card.Prompt, styleDim.Render("→"), card.Answer)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func writeCardChange(w io.Writer, res core.CardChange, verb string) error {
	past := map[string]string{"add": "Added", "edit": "Edited", "suspend": "Suspended", "unsuspend": "Unsuspended",
		"delete": "Deleted", "flag": "Flagged"}[verb]
	label := styleAccent.Render(cardLabel(res.Card))
	var err error
	switch {
	case res.DryRun && res.Changed:
		_, err = fmt.Fprintf(w, "Would %s %s, %s\n", verb, label, res.Card.ID)
	case !res.Changed:
		_, err = fmt.Fprintf(w, "%s %s was already as asked: nothing changed\n", label, res.Card.ID)
	default:
		_, err = fmt.Fprintf(w, "%s %s, %s\n", past, label, res.Card.ID)
	}
	return err
}

func writeReviewResult(w io.Writer, res core.ReviewResult) error {
	var err error
	switch {
	case res.Dropped && res.DryRun:
		_, err = fmt.Fprintf(w, "Would drop %s\n", res.Card.ID)
	case res.Dropped:
		_, err = fmt.Fprintf(w, "Dropped %s\n", res.Card.ID)
	case res.DryRun:
		_, err = fmt.Fprintf(w, "Would record %s for %s\n", res.Rating, res.Card.ID)
	default:
		_, err = fmt.Fprintf(w, "Recorded %s for %s; next due %s\n", res.Rating, res.Card.ID,
			res.Card.Due.Format("2 Jan 2006"))
	}
	return err
}
