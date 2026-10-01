package cli

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/internal/library"
)

// writeStatus renders the status for people. Styling arrives with #24.
func writeStatus(w io.Writer, s core.Status) error {
	if len(s.Topics) == 0 {
		if _, err := fmt.Fprintf(w, "Study home: %s\n\nNo Topics yet. Start one with:\n  study topic create --title \"Linear algebra\"\n", s.StudyHome); err != nil {
			return err
		}
		return writeProblems(w, s.Problems)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "Study home:\t%s\n", s.StudyHome)
	if s.ActiveTopic != nil {
		fmt.Fprintf(tw, "Active topic:\t%s (%s): %s\n", s.ActiveTopic.ID, s.ActiveTopic.Title, s.ActiveTopic.Reason)
	} else {
		fmt.Fprintf(tw, "Active topic:\tnone yet: start inside a Topic's folder, or name a Topic\n")
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(w, "\nTopics:")
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, t := range s.Topics {
		fmt.Fprintf(tw, "  %s\t%s\n", t.ID, t.Title)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	return writeProblems(w, s.Problems)
}

func writeProblems(w io.Writer, problems []core.TopicProblem) error {
	if len(problems) == 0 {
		return nil
	}
	fmt.Fprintln(w, "\nTopics that could not be read:")
	for _, p := range problems {
		fmt.Fprintf(w, "  %s: %s\n", p.ID, p.Message)
	}
	return nil
}

func writeLibrarySummary(w io.Writer, s core.LibrarySummary) error {
	fmt.Fprintf(w, "Indexed %d books from %s\n", s.Books, s.Root)
	if n := len(s.Skipped); n > 0 {
		fmt.Fprintf(w, "Skipped %d entries that could not be read:\n", n)
		for _, path := range s.Skipped {
			fmt.Fprintf(w, "  %s\n", path)
		}
	}
	return nil
}

func writeSearchResults(w io.Writer, results []library.Result) error {
	if len(results) == 0 {
		_, err := fmt.Fprintln(w, "No books match.")
		return err
	}
	for _, r := range results {
		fmt.Fprintf(w, "%s\n  %s · %s\n  %s\n", r.Title, r.Category, r.Format, r.Path)
	}
	return nil
}
