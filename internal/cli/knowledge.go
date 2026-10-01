package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mordor-forge/lamplight/v2/internal/core"
)

// maxStdinQuote bounds a quote read from stdin with --quote -.
const maxStdinQuote = 1 << 20

func (a *app) sourceCommand() *cobra.Command {
	group := &cobra.Command{
		Use:   "source",
		Short: "Add and list the documents and web pages a Topic learns from",
		Args:  noArgs,
		RunE:  a.groupHelp,
	}

	var spec core.SourceSpec
	add := &cobra.Command{
		Use:   "add <topic>",
		Short: "Add a file or a web page as a Source of a Topic",
		Long: "Add a file or a web page as a Source of a Topic. A file is hashed, never parsed, so it can be\n" +
			"found again in the Library if it moves.",
		Example: `  study source add c --file ~/Books/kernighan_ritchie.pdf
  study source add c --url https://go.dev/blog/context --title "Go Concurrency Patterns: Context"`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := core.Open(a.opts)
			if err != nil {
				return a.fail(err)
			}
			spec.Topic = args[0]
			res, err := c.AddSource(cmd.Context(), spec)
			if err != nil {
				return a.fail(err)
			}
			if a.json {
				return a.writeJSON(envelope{OK: true, Data: res})
			}
			return writeSourceResult(a.out, res, "add")
		},
	}
	add.Flags().StringVar(&spec.File, "file", "", "the file, such as a PDF from your Library")
	add.Flags().StringVar(&spec.URL, "url", "", "the web page's address")
	add.Flags().StringVar(&spec.Title, "title", "", "the Source's title; derived from the file or URL when omitted")
	add.Flags().StringVar(&spec.NotebookLMID, "notebooklm-id", "", "the Source's id in the Topic's NotebookLM notebook")
	add.Flags().BoolVar(&spec.DryRun, "dry-run", false, "show the Source that would be added without recording it")

	var changes core.SourceChanges
	var title, path, notebookID string
	update := &cobra.Command{
		Use:   "update <topic> <source>",
		Short: "Change a Source's title or NotebookLM id, or record where its file is now",
		Example: `  study source update c kernighan-ritchie.k3f9a2 --notebooklm-id 7b1e
  study source update c kernighan-ritchie.k3f9a2 --path ~/Books/C/kr.pdf`,
		Args: exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("title") {
				changes.Title = &title
			}
			if cmd.Flags().Changed("path") {
				changes.Path = &path
			}
			if cmd.Flags().Changed("notebooklm-id") {
				changes.NotebookLMID = &notebookID
			}
			c, err := core.Open(a.opts)
			if err != nil {
				return a.fail(err)
			}
			res, err := c.UpdateSource(cmd.Context(), args[0], args[1], changes)
			if err != nil {
				return a.fail(err)
			}
			if a.json {
				return a.writeJSON(envelope{OK: true, Data: res})
			}
			return writeSourceResult(a.out, res, "update")
		},
	}
	update.Flags().StringVar(&title, "title", "", "the new title")
	update.Flags().StringVar(&path, "path", "", "where the file is now; it must hold the same content")
	update.Flags().StringVar(&notebookID, "notebooklm-id", "", "the Source's id in the NotebookLM notebook; empty removes it")
	update.Flags().BoolVar(&changes.DryRun, "dry-run", false, "show the result without recording anything")

	list := &cobra.Command{
		Use:   "list <topic>",
		Short: "List a Topic's Knowledge base and Sources, and check where each file is",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := core.Open(a.opts)
			if err != nil {
				return a.fail(err)
			}
			res, err := c.ListSources(cmd.Context(), args[0])
			if err != nil {
				return a.fail(err)
			}
			if a.json {
				return a.writeJSON(envelope{OK: true, Data: res})
			}
			return writeSourceList(a.out, res)
		},
	}
	group.AddCommand(add, update, list)
	return group
}

func (a *app) evidenceCommand() *cobra.Command {
	group := &cobra.Command{
		Use:   "evidence",
		Short: "Record and list the quotes from Sources that Lessons cite",
		Args:  noArgs,
		RunE:  a.groupHelp,
	}

	var spec core.EvidenceSpec
	record := &cobra.Command{
		Use:   "record <topic>",
		Short: "Record an exact quote from a Source that a Lesson cites",
		Long: "Record an exact quote from one of the Topic's Sources that a Lesson cites, with its location\n" +
			"when known and where the location came from. Pass --quote - to read the quote from stdin.",
		Example: `  study evidence record c --lesson pointers --source kernighan-ritchie.k3f9a2 \
    --quote "A pointer is a variable that contains the address of a variable." \
    --location "p. 93" --location-from source`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if spec.Quote == "-" {
				data, err := io.ReadAll(io.LimitReader(a.stdin, maxStdinQuote))
				if err != nil {
					return a.fail(err)
				}
				spec.Quote = string(data)
			}
			c, err := core.Open(a.opts)
			if err != nil {
				return a.fail(err)
			}
			spec.Topic = args[0]
			res, err := c.RecordEvidence(cmd.Context(), spec)
			if err != nil {
				return a.fail(err)
			}
			if a.json {
				return a.writeJSON(envelope{OK: true, Data: res})
			}
			return writeEvidenceResult(a.out, res)
		},
	}
	record.Flags().StringVar(&spec.Lesson, "lesson", "", "the id of the Lesson that cites it (required)")
	record.Flags().StringVar(&spec.Source, "source", "", "the id of the Source it comes from (required)")
	record.Flags().StringVar(&spec.Quote, "quote", "", "the exact quote, as the Source has it, or - to read it from stdin (required)")
	record.Flags().StringVar(&spec.Location, "location", "", "where the quote is, such as \"p. 93\" or \"§5.1\"")
	record.Flags().StringVar(&spec.LocationFrom, "location-from", "",
		"where the location came from: source, knowledge_base, learner or estimate")
	_ = record.RegisterFlagCompletionFunc("location-from", cobra.FixedCompletions([]string{
		core.LocationFromSource, core.LocationFromKnowledgeBase, core.LocationFromLearner, core.LocationFromEstimate,
	}, cobra.ShellCompDirectiveNoFileComp))
	record.Flags().BoolVar(&spec.DryRun, "dry-run", false, "show the Evidence that would be recorded without recording it")

	var lesson string
	list := &cobra.Command{
		Use:   "list <topic>",
		Short: "List the Evidence recorded in a Topic",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := core.Open(a.opts)
			if err != nil {
				return a.fail(err)
			}
			res, err := c.ListEvidence(cmd.Context(), args[0], lesson)
			if err != nil {
				return a.fail(err)
			}
			if a.json {
				return a.writeJSON(envelope{OK: true, Data: res})
			}
			return writeEvidenceList(a.out, res)
		},
	}
	list.Flags().StringVar(&lesson, "lesson", "", "only the Evidence one Lesson cites")
	group.AddCommand(record, list)
	return group
}

func describeKnowledgeBase(kb *core.KnowledgeBase) string {
	switch {
	case kb == nil:
		return "not chosen yet, so none"
	case kb.Notebook != "":
		return kb.Kind + " (notebook " + kb.Notebook + ")"
	default:
		return kb.Kind
	}
}

func writeSourceResult(w io.Writer, r core.SourceResult, verb string) error {
	s := r.Source
	switch {
	case !r.Changed:
		fmt.Fprintf(w, "Source %s already has those values: nothing changed\n", s.ID)
	case verb == "add" && r.DryRun:
		fmt.Fprintf(w, "Would add Source %s to %s: %s\n", styleAccent.Render(s.ID), r.Topic, s.Title)
	case verb == "add":
		fmt.Fprintf(w, "Added Source %s to %s: %s\n", styleAccent.Render(s.ID), r.Topic, s.Title)
	case r.DryRun:
		fmt.Fprintf(w, "Would update Source %s in %s: %s\n", styleAccent.Render(s.ID), r.Topic, s.Title)
	default:
		fmt.Fprintf(w, "Updated Source %s in %s: %s\n", styleAccent.Render(s.ID), r.Topic, s.Title)
	}
	writeSourceDetails(w, core.SourceStatus{Source: s})
	return nil
}

func writeSourceDetails(w io.Writer, s core.SourceStatus) {
	switch s.Kind {
	case core.SourceFile:
		fmt.Fprintf(w, "  %s\n", styleDim.Render(s.Path))
	case core.SourceURL:
		fmt.Fprintf(w, "  %s\n", styleDim.Render(s.URL))
	}
	switch s.State {
	case core.SourceMoved:
		fmt.Fprintf(w, "  %s the file moved to %s; record it with study source update --path\n",
			styleWarn.Render("!"), s.FoundAt)
	case core.SourceChanged:
		fmt.Fprintf(w, "  %s the file at this path is no longer the one added\n", styleWarn.Render("!"))
	case core.SourceMissing:
		fmt.Fprintf(w, "  %s the file is gone, and the Library has no copy of it\n", styleWarn.Render("!"))
	}
	if s.NotebookLMID != "" {
		fmt.Fprintf(w, "  NotebookLM: %s\n", s.NotebookLMID)
	}
}

func writeSourceList(w io.Writer, l core.SourceList) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", styleLabel.Render("Knowledge base:"), describeKnowledgeBase(l.KnowledgeBase))
	if len(l.Sources) == 0 {
		fmt.Fprintf(&b, "\nNo Sources yet. Add one with:\n  %s\n", styleAccent.Render("study source add "+l.Topic+" --file <path>"))
		_, err := io.WriteString(w, b.String())
		return err
	}
	fmt.Fprintf(&b, "\n%s\n", styleLabel.Render("Sources:"))
	for _, s := range l.Sources {
		fmt.Fprintf(&b, "%s  %s\n", styleAccent.Render(s.ID), s.Title)
		writeSourceDetails(&b, s)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func writeEvidenceResult(w io.Writer, r core.EvidenceResult) error {
	e := r.Evidence
	switch {
	case !r.Changed:
		fmt.Fprintf(w, "Evidence %s was already recorded: nothing changed\n", e.ID)
	case r.DryRun:
		fmt.Fprintf(w, "Would record Evidence for Lesson %s from Source %s\n", e.Lesson, e.Source)
	default:
		fmt.Fprintf(w, "Recorded Evidence %s for Lesson %s from Source %s\n", styleAccent.Render(e.ID), e.Lesson, e.Source)
	}
	writeQuote(w, e)
	return nil
}

func writeEvidenceList(w io.Writer, l core.EvidenceList) error {
	if len(l.Evidence) == 0 {
		_, err := fmt.Fprintln(w, "No Evidence recorded yet.")
		return err
	}
	var b strings.Builder
	for i, e := range l.Evidence {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s  Lesson %s, Source %s\n", styleAccent.Render(e.ID), e.Lesson, e.Source)
		writeQuote(&b, e)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func writeQuote(w io.Writer, e core.Evidence) {
	for _, line := range strings.Split(e.Quote, "\n") {
		fmt.Fprintf(w, "  > %s\n", line)
	}
	if e.Location != "" {
		fmt.Fprintf(w, "  %s\n", styleDim.Render(e.Location+" (location from "+strings.ReplaceAll(e.LocationFrom, "_", " ")+")"))
	}
}
