package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mordor-forge/lamplight/v2/internal/core"
)

func (a *app) importCommand() *cobra.Command {
	var spec core.ImportSpec
	cmd := &cobra.Command{
		Use:   "import <v1-workspace>",
		Short: "Import a v1 study workspace as a new Topic",
		Long: "Import a workspace of the v1 study skill as a new Topic, with its git history. The workspace\n" +
			"itself is left untouched. Run it with --dry-run first: it lists what will be copied and converted,\n" +
			"which Lessons are proven done and by what, and everything that will be dropped. If a Lesson shown\n" +
			"done is not, keep it open with --not-done. Then adopt the Topic with your agent.",
		Example: `  study import ~/study-workspaces/go-concurrency --dry-run
  study import ~/study-workspaces/go-concurrency --topic go --not-done lesson-03`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := core.Open(a.opts)
			if err != nil {
				return a.fail(err)
			}
			spec.Dir = args[0]
			res, err := c.ImportV1(cmd.Context(), spec)
			if err != nil {
				return a.fail(err)
			}
			if a.json {
				return a.writeJSON(envelope{OK: true, Data: res})
			}
			return writeImport(a.out, res)
		},
	}
	cmd.Flags().StringVar(&spec.ID, "topic", "", "the new Topic's id; defaults to the workspace folder's name")
	cmd.Flags().StringSliceVar(&spec.NotDone, "not-done", nil,
		"keep this Lesson open even if v1's records prove it done, by its id (lesson-03); repeatable")
	cmd.Flags().BoolVar(&spec.DryRun, "dry-run", false, "list what the import will do, without writing anything")
	return cmd
}

// writeImport renders an import's report: what it converted, moved, proved
// done and dropped.
func writeImport(w io.Writer, r core.TopicImport) error {
	var b strings.Builder
	verb := "Imported"
	if r.DryRun {
		verb = "Would import"
	}
	fmt.Fprintf(&b, "%s %s as Topic %s (%s)\n", verb, printable(r.From), styleAccent.Render(r.Topic.ID), printable(r.Topic.Title))
	fmt.Fprintf(&b, "%s %d files, %s, git history included\n", styleLabel.Render("Copy:"), r.Copy.Files, humanSize(r.Copy.Bytes))
	section := func(title string) { fmt.Fprintf(&b, "\n%s\n", styleLabel.Render(title)) }
	if len(r.Converted) > 0 {
		section("Converted:")
		for _, n := range r.Converted {
			fmt.Fprintf(&b, "  %s: %s\n", printable(n.What), printable(n.Detail))
		}
	}
	if len(r.Moved) > 0 {
		section("Moved:")
		for _, m := range r.Moved {
			fmt.Fprintf(&b, "  %s → %s\n", printable(m.From), printable(m.To))
		}
	}
	if len(r.Completed) > 0 {
		section("Done (proven):")
		for _, l := range r.Completed {
			fmt.Fprintf(&b, "  %s  %s %s\n", l.Lesson, printable(l.Title), styleDim.Render("("+printable(l.Proof)+")"))
		}
	}
	if len(r.Open) > 0 {
		section("Open:")
		for _, l := range r.Open {
			status := ""
			if l.V1Status != "" {
				status = " " + styleDim.Render("(v1: "+printable(l.V1Status)+")")
			}
			fmt.Fprintf(&b, "  %s  %s%s\n", l.Lesson, printable(l.Title), status)
		}
	}
	if len(r.Sources) > 0 || r.KnowledgeBase != nil {
		section("Knowledge:")
		if kb := r.KnowledgeBase; kb != nil {
			fmt.Fprintf(&b, "  Knowledge base: %s %s\n", kb.Kind, printable(kb.Notebook))
		}
		for _, s := range r.Sources {
			where := s.URL
			if s.TopicPath != "" {
				where = s.TopicPath
			} else if s.FileName != "" {
				where = s.FileName
			}
			fmt.Fprintf(&b, "  %s  %s\n", printable(s.Title), styleDim.Render(printable(where)))
		}
	}
	if n := r.NextStep; n != nil {
		section("Where v1 stopped:")
		if n.PendingAction != "" {
			fmt.Fprintf(&b, "  %s\n", printable(n.PendingAction))
		}
		if n.Context != "" {
			fmt.Fprintf(&b, "  %s\n", styleDim.Render(printable(n.Context)))
		}
	}
	if len(r.Dropped) > 0 {
		section(styleWarn.Render("Dropped:"))
		for _, n := range r.Dropped {
			fmt.Fprintf(&b, "  %s: %s\n", printable(n.What), printable(n.Detail))
		}
	}
	switch {
	case r.DryRun:
		b.WriteString("\nNothing was written. Run the command again without --dry-run to import.\n")
	case r.CheckpointError != "":
		fmt.Fprintf(&b, "\n%s %s\n", styleWarn.Render("The import is not saved in git yet:"), printable(r.CheckpointError))
	default:
		b.WriteString("\nNext: open a Session on it with your agent, and adopt it together.\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}
