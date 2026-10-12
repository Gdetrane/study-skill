package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
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
	add.Flags().BoolVar(&spec.DryRun, "dry-run", false, "show the Source that would be added without recording it")

	var changes core.SourceChanges
	var title, path string
	update := &cobra.Command{
		Use:   "update <topic> <source>",
		Short: "Change a Source's title, or say where its file is on this computer",
		Long: "Change a Source's title, which the History records, or say where its file is on this computer\n" +
			"with --path, which is remembered here only.",
		Example: `  study source update c kernighan-ritchie.k3f9a2 --title "The C Programming Language"
  study source update c kernighan-ritchie.k3f9a2 --path ~/Books/C/kr.pdf`,
		Args: exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("title") {
				changes.Title = &title
			}
			if cmd.Flags().Changed("path") {
				changes.Path = &path
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
	update.Flags().BoolVar(&changes.DryRun, "dry-run", false, "show the result without recording anything")

	list := &cobra.Command{
		Use:   "list <topic>",
		Short: "List a Topic's Knowledge base and Sources, and find each file on this computer",
		Long: "List a Topic's Knowledge base and Sources, and find each file on this computer. When the\n" +
			"Topic names a Knowledge base plugin, the plugin is asked which Sources it has indexed. It\n" +
			"has a couple of seconds to say: a plugin that is missing, slow or broken leaves that not\n" +
			"known, and the list says which command shows what is wrong.",
		Args: exactArgs(1),
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
	group.AddCommand(add, update, list, a.sourceIndexCommand())
	return group
}

// sourceIndexCommand is study source index: the one thing that asks a
// Knowledge base plugin to index. A plugin that indexes reads the files it
// is handed and fetches the addresses, so the agent runs this from its own
// shell, where its sandbox applies, or the learner runs it; the MCP server,
// which runs outside that sandbox, has no tool for it (ADR-0012).
func (a *app) sourceIndexCommand() *cobra.Command {
	var spec core.IndexSpec
	cmd := &cobra.Command{
		Use:   "index <topic> [<source>...]",
		Short: "Have the Topic's Knowledge base plugin index the Sources it has not indexed",
		Long: "Ask the Knowledge base plugin a Topic names to index the Topic's Sources that are not\n" +
			"indexed, one after the other, and follow each until the plugin has indexed it or says why\n" +
			"it could not. Name Sources to index only those. A file is handed over by its path on this\n" +
			"computer and its content hash, a web page by its address.\n\n" +
			"A Source is its content: a file that is missing, or has other content now than was added,\n" +
			"is skipped, and the output says what to do about it. What goes wrong with a Source is not\n" +
			"an error of the command: the Source is failed, with the plugin's reason, and the exit code\n" +
			"is 0. Progress goes to stderr.\n\n" +
			"Stopped with Ctrl-C, it tells the plugin to stop and stops the plugin's program. What was\n" +
			"indexed stays, and running it again carries on. Nothing is recorded in the Topic: what is\n" +
			"indexed is this computer's, and the plugin's to say.\n\n" +
			"A plugin keeps its index in a folder of its own in your state folder, outside the Study\n" +
			"home. A sandbox that lets study write in the Study home alone refuses that folder: allow\n" +
			"it in the sandbox, or run this command in a shell of your own. Agents have no tool for\n" +
			"indexing, and run this command like any other.",
		Example: `  study source index linear-algebra
  study source index linear-algebra --dry-run
  study source index c go-dev-blog-context.m4k8s3 --again`,
		Args:              minArgs(1),
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := core.Open(a.opts)
			if err != nil {
				return a.fail(err)
			}
			spec.Topic, spec.Sources = args[0], args[1:]
			spec.Progress = indexProgress(a.stderr, a.json)
			res, err := c.IndexSources(cmd.Context(), spec)
			if err != nil {
				return a.fail(err)
			}
			if a.json {
				return a.writeJSON(envelope{OK: true, Data: res})
			}
			return writeIndexResult(a.out, res)
		},
	}
	cmd.Flags().BoolVar(&spec.Again, "again", false,
		"ask for Sources that are indexed already too: a web page is fetched again")
	cmd.Flags().BoolVar(&spec.DryRun, "dry-run", false,
		"start the plugin and list what it would be asked to index, without asking")
	return cmd
}

// indexProgress reports a running study source index on stderr: each Source
// as the plugin is asked for it, and every so often while a long one is
// indexed, with how far the plugin says it is. With --json only the reports
// of long ones are written, so a quick run keeps stderr empty.
func indexProgress(w io.Writer, quiet bool) func(core.IndexProgress) {
	return func(p core.IndexProgress) {
		switch {
		case p.State == core.ProgressStarted && !quiet:
			fmt.Fprintf(w, "Indexing %s (%d of %d)…\n", printable(p.Title), p.Number, p.Total)
		case p.State == core.ProgressRunning:
			done := ""
			if p.Progress != nil {
				done = fmt.Sprintf("%d%%, ", int(*p.Progress*100))
			}
			fmt.Fprintf(w, "  still indexing %s (%s%s)\n", printable(p.Title), done, p.Elapsed.Round(time.Second))
		case p.State == core.ProgressStopping:
			// At once, and with --json too: nothing else is printed until
			// the plugin has answered, which it has seconds to do.
			fmt.Fprintf(w, "Stopping: telling the plugin to stop indexing %s…\n", printable(p.Title))
		}
	}
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

// describeKnowledgeBase shows the Knowledge base as topic.toml records it: a
// kind, and with the kind plugin the plugin's name. A kind this version does
// not know is treated as none, and says so. Whether this computer has the
// plugin is for the Topic's indexing to say (writeIndexing).
func describeKnowledgeBase(kb *core.KnowledgeBase) string {
	switch {
	case kb == nil:
		return "not chosen yet, so none"
	case kb.Kind == core.KnowledgeBaseNone:
		return kb.Kind
	case kb.Kind == core.KnowledgeBasePlugin && kb.Plugin == "":
		return "plugin, without a name"
	case kb.Kind == core.KnowledgeBasePlugin:
		return "plugin " + printable(kb.Plugin)
	default:
		return printable(kb.Kind) + " (a kind this version of study does not know, so treated as none)"
	}
}

// describeIndex says what a Topic's Knowledge base plugin says of a Source,
// in the learner's words, and whether it is something to look at. A reason
// is the plugin's, or names a path: it goes through printable.
func describeIndex(x core.SourceIndex) (text string, attention bool) {
	reason := ""
	if x.Reason != "" {
		reason = ": " + printable(x.Reason)
	}
	switch x.State {
	case core.IndexIndexed:
		return "indexed", false
	case core.IndexIndexing:
		if x.Progress != nil {
			return fmt.Sprintf("being indexed (%d%%)", int(*x.Progress*100)), false
		}
		return "being indexed", false
	case core.IndexFailed:
		return "indexing failed" + reason, true
	case core.IndexNotIndexed:
		return "not indexed" + reason, true
	}
	return "", false
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
}

func writeSourceList(w io.Writer, l core.SourceList) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", styleLabel.Render("Knowledge base:"), describeKnowledgeBase(l.KnowledgeBase))
	problem := l.Indexing != nil && l.Indexing.Problem != ""
	if problem {
		// What keeps the plugin from saying what it has indexed, once, and
		// not with every Source.
		fmt.Fprintf(&b, "  %s %s\n    Meanwhile this Topic works as one without a Knowledge base. Run:\n      %s\n",
			styleWarn.Render("!"), printable(l.Indexing.Problem), styleAccent.Render(printable(l.Indexing.Action)))
	}
	if len(l.Sources) == 0 {
		fmt.Fprintf(&b, "\nNo Sources yet. Add one with:\n  %s\n", styleAccent.Render("study source add "+l.Topic+" --file <path>"))
		_, err := io.WriteString(w, b.String())
		return err
	}
	fmt.Fprintf(&b, "\n%s\n", styleLabel.Render("Sources:"))
	for _, s := range l.Sources {
		fmt.Fprintf(&b, "%s  %s\n", styleAccent.Render(printable(s.ID)), printable(s.Title))
		writeSourceDetails(&b, s)
		if s.Indexed == nil {
			continue
		}
		index := *s.Indexed
		if index.State == core.IndexNotIndexed && (s.State == core.SourceMissing || s.State == core.SourceChanged) {
			// The line above says so already. A Source that failed keeps
			// its reason, which is the plugin's.
			index.Reason = ""
		}
		switch text, attention := describeIndex(index); {
		case text == "":
		case attention:
			fmt.Fprintf(&b, "  %s %s\n", styleWarn.Render("!"), text)
			if index.Retry != "" {
				// A failure is no next action: it is tried again by whoever
				// put right what the reason names, and only where trying
				// again would hand the Source to the plugin.
				fmt.Fprintf(&b, "    To have it tried again, run: %s\n", styleAccent.Render(printable(index.Retry)))
			}
		default:
			fmt.Fprintf(&b, "  %s\n", styleDim.Render(text))
		}
	}
	if l.Indexing != nil && !problem && l.Indexing.Action != "" {
		fmt.Fprintf(&b, "\nTo have Knowledge base plugin %s index what is not indexed, run:\n  %s\n", printable(l.Indexing.Plugin),
			styleAccent.Render(printable(l.Indexing.Action)))
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// writeIndexResult shows what study source index did, or in a dry run what
// it would do: each Source that was considered, with what the plugin says
// of it now.
func writeIndexResult(w io.Writer, r core.IndexResult) error {
	var b strings.Builder
	asked := 0
	for _, s := range r.Sources {
		if s.Asked {
			asked++
		}
	}
	plugin, topic := styleAccent.Render(printable(r.Plugin)), r.Topic
	sources := fmt.Sprintf("%d %s", asked, plural(asked, "Source", "Sources"))
	switch {
	case len(r.Sources) == 0:
		fmt.Fprintf(&b, "%s has no Sources to index yet. Add one with:\n  %s\n", topic,
			styleAccent.Render("study source add "+topic+" --file <path>"))
	case r.DryRun && asked == 0:
		fmt.Fprintf(&b, "Knowledge base plugin %s would be asked to index nothing of %s:\n", plugin, topic)
	case r.DryRun:
		fmt.Fprintf(&b, "Knowledge base plugin %s would be asked to index %s of %s:\n", plugin, sources, topic)
	case asked == 0:
		fmt.Fprintf(&b, "Knowledge base plugin %s was asked to index nothing of %s:\n", plugin, topic)
	default:
		fmt.Fprintf(&b, "Knowledge base plugin %s was asked to index %s of %s:\n", plugin, sources, topic)
	}
	ids := make([]string, len(r.Sources))
	for i, s := range r.Sources {
		ids[i] = printable(s.ID)
	}
	width := widest(ids) + 2
	for i, s := range r.Sources {
		text, attention := describeIndex(s.SourceIndex)
		switch {
		case s.Asked && r.DryRun && s.State == core.IndexIndexed:
			text, attention = "indexed, and would be asked for again", false
		case s.Asked && r.DryRun:
			text, attention = text+", and would be indexed", false
		case s.Skipped != "":
			text, attention = text+"; skipped: "+printable(s.Skipped), true
		case !s.Asked && s.State == core.IndexIndexed:
			text = "indexed already"
		}
		mark := " "
		if attention {
			mark = styleWarn.Render("!")
		}
		fmt.Fprintf(&b, "%s %s%s\n", mark, styleAccent.Render(pad(ids[i], width)), printable(s.Title))
		// What the plugin is handed, and reads: whoever runs this sees which
		// files and addresses those are.
		if where := s.Path + s.URL; where != "" {
			fmt.Fprintf(&b, "    %s\n", styleDim.Render(printable(where)))
		}
		fmt.Fprintf(&b, "    %s\n", text)
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
