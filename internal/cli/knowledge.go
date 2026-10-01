package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"

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
			"found again on any computer, inside the Topic or in the Library.",
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
		Short: "Change a Source's title or NotebookLM id, or say where its file is on this computer",
		Long: "Change a Source's title or NotebookLM id, which the History records, or say where its file is on\n" +
			"this computer with --path, which is remembered here only.",
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
	update.Flags().StringVar(&path, "path", "", "where the file is on this computer; it must hold the same content")
	update.Flags().StringVar(&notebookID, "notebooklm-id", "", "the Source's id in the NotebookLM notebook; empty removes it")
	update.Flags().BoolVar(&changes.DryRun, "dry-run", false, "show the result without recording anything")

	list := &cobra.Command{
		Use:   "list <topic>",
		Short: "List a Topic's Knowledge base and Sources, and find each file on this computer",
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
			return writeEvidenceResult(a.out, res, "record")
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

	var retractDryRun bool
	retract := &cobra.Command{
		Use:   "retract <topic> <evidence>",
		Short: "Take back Evidence recorded by mistake",
		Long: "Take back Evidence recorded by mistake. The retraction is recorded in the History, never\n" +
			"deleted; study evidence list --all still shows the Evidence.",
		Example: "  study evidence retract c k3f9a2b7qd",
		Args:    exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := core.Open(a.opts)
			if err != nil {
				return a.fail(err)
			}
			res, err := c.RetractEvidence(cmd.Context(), args[0], args[1], retractDryRun)
			if err != nil {
				return a.fail(err)
			}
			if a.json {
				return a.writeJSON(envelope{OK: true, Data: res})
			}
			return writeEvidenceResult(a.out, res, "retract")
		},
	}
	retract.Flags().BoolVar(&retractDryRun, "dry-run", false, "show the Evidence that would be retracted without recording anything")

	var query core.EvidenceQuery
	list := &cobra.Command{
		Use:   "list <topic>",
		Short: "List the Evidence recorded in a Topic",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := core.Open(a.opts)
			if err != nil {
				return a.fail(err)
			}
			query.Topic = args[0]
			res, err := c.ListEvidence(cmd.Context(), query)
			if err != nil {
				return a.fail(err)
			}
			if a.json {
				return a.writeJSON(envelope{OK: true, Data: res})
			}
			return writeEvidenceList(a.out, res)
		},
	}
	list.Flags().StringVar(&query.Lesson, "lesson", "", "only the Evidence one Lesson cites")
	list.Flags().BoolVar(&query.All, "all", false, "include retracted Evidence")
	group.AddCommand(record, retract, list)
	return group
}

func describeKnowledgeBase(kb *core.KnowledgeBase) string {
	switch {
	case kb == nil:
		return "not chosen yet, so none"
	case kb.Notebook != "":
		return kb.Kind + " (notebook " + printable(kb.Notebook) + ")"
	default:
		return kb.Kind
	}
}

// printable quotes text that came from the file system, such as a path, if
// it holds characters that a terminal would interpret.
func printable(s string) string {
	if strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) }) {
		return strconv.QuoteToASCII(s)
	}
	return s
}

func writeSourceResult(w io.Writer, r core.SourceResult, verb string) error {
	s := r.Source
	id := styleAccent.Render(s.ID)
	switch {
	case !r.Changed:
		fmt.Fprintf(w, "Source %s already has those values: nothing changed\n", s.ID)
	case verb == "add" && r.DryRun:
		fmt.Fprintf(w, "Would add Source %s to %s: %s\n", id, r.Topic, printable(s.Title))
	case verb == "add":
		fmt.Fprintf(w, "Added Source %s to %s: %s\n", id, r.Topic, printable(s.Title))
	case r.DryRun:
		fmt.Fprintf(w, "Would update Source %s in %s: %s\n", id, r.Topic, printable(s.Title))
	default:
		fmt.Fprintf(w, "Updated Source %s in %s: %s\n", id, r.Topic, printable(s.Title))
	}
	writeSourceDetails(w, core.SourceStatus{Source: s, Path: r.Path})
	return nil
}

func writeSourceDetails(w io.Writer, s core.SourceStatus) {
	switch {
	case s.Kind == core.SourceURL:
		fmt.Fprintf(w, "  %s\n", styleDim.Render(printable(s.URL)))
	case s.Path != "":
		fmt.Fprintf(w, "  %s\n", styleDim.Render(printable(s.Path)))
	case s.TopicPath != "":
		fmt.Fprintf(w, "  %s\n", styleDim.Render(printable(s.TopicPath)+" in the Topic"))
	case s.FileName != "":
		fmt.Fprintf(w, "  %s\n", styleDim.Render(printable(s.FileName)))
	}
	switch s.State {
	case core.SourceChanged:
		fmt.Fprintf(w, "  %s the file here is no longer the one added\n", styleWarn.Render("!"))
	case core.SourceMissing:
		fmt.Fprintf(w, "  %s not found on this computer: say where it is with study source update --path,\n"+
			"    or add its folder to the Library with study library build\n", styleWarn.Render("!"))
	case core.SourceUntracked:
		fmt.Fprintf(w, "  %s added to %s by hand, so not a Source yet: add it with study source add\n",
			styleWarn.Render("!"), "sources.jsonl")
	}
	if s.NotebookLMID != "" {
		note := ""
		if s.NotebookLMStale {
			note = styleWarn.Render(" (from another notebook)")
		}
		fmt.Fprintf(w, "  NotebookLM: %s%s\n", printable(s.NotebookLMID), note)
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
		fmt.Fprintf(&b, "%s  %s\n", styleAccent.Render(s.ID), printable(s.Title))
		writeSourceDetails(&b, s)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func writeEvidenceResult(w io.Writer, r core.EvidenceResult, verb string) error {
	e := r.Evidence
	switch {
	case !r.Changed && verb == "retract":
		fmt.Fprintf(w, "Evidence %s was already retracted: nothing changed\n", e.ID)
	case !r.Changed:
		fmt.Fprintf(w, "Evidence %s was already recorded: nothing changed\n", e.ID)
	case verb == "retract" && r.DryRun:
		fmt.Fprintf(w, "Would retract Evidence %s for Lesson %s\n", styleAccent.Render(e.ID), e.Lesson)
	case verb == "retract":
		fmt.Fprintf(w, "Retracted Evidence %s for Lesson %s\n", styleAccent.Render(e.ID), e.Lesson)
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
		retracted := ""
		if e.Retracted {
			retracted = styleWarn.Render(" (retracted)")
		}
		fmt.Fprintf(&b, "%s  Lesson %s, Source %s%s\n", styleAccent.Render(e.ID), e.Lesson, e.Source, retracted)
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
