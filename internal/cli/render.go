package cli

import (
	"fmt"
	"io"
	"strings"
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
	writeFlags(w, s.Topics)
	return writeProblems(w, s.Problems)
}

// writeFlags lists what replaying each Topic's History found that needs the
// learner's attention.
func writeFlags(w io.Writer, topics []core.Topic) {
	header := false
	for _, t := range topics {
		for _, f := range t.Flags {
			if !header {
				fmt.Fprintln(w, "\nNeeds attention:")
				header = true
			}
			fmt.Fprintf(w, "  %s: %s (flag %s)\n", t.ID, f.Message, f.ID)
		}
	}
}

func writeFlagDismissal(w io.Writer, d core.FlagDismissal) error {
	var err error
	switch {
	case !d.Changed:
		_, err = fmt.Fprintf(w, "Flag %s in %s was already dismissed\n", d.Flag.ID, d.Topic)
	case d.DryRun:
		_, err = fmt.Fprintf(w, "Would dismiss flag %s in %s: %s\n", d.Flag.ID, d.Topic, d.Flag.Message)
	default:
		_, err = fmt.Fprintf(w, "Dismissed flag %s in %s: %s\n", d.Flag.ID, d.Topic, d.Flag.Message)
	}
	return err
}

func writeTopicUpdate(w io.Writer, u core.TopicUpdate, dryRun bool) error {
	var b strings.Builder
	t := u.Topic
	switch {
	case !u.Changed:
		fmt.Fprintf(&b, "Topic %s already has that title and goal: nothing changed\n", t.ID)
	case dryRun:
		fmt.Fprintf(&b, "Would update Topic %s (%s)\n", t.ID, t.Title)
	default:
		fmt.Fprintf(&b, "Updated Topic %s (%s)\n", t.ID, t.Title)
	}
	if t.Goal != "" {
		fmt.Fprintf(&b, "  Goal: %s\n", t.Goal)
	}
	writeFlags(&b, []core.Topic{t})
	_, err := io.WriteString(w, b.String())
	return err
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
	var b strings.Builder
	fmt.Fprintf(&b, "Indexed %d books from %s\n", s.Books, s.Root)
	if n := len(s.Skipped); n > 0 {
		fmt.Fprintf(&b, "Skipped %d entries that could not be read:\n", n)
		for _, path := range s.Skipped {
			fmt.Fprintf(&b, "  %s\n", path)
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func writeSearchResults(w io.Writer, results []library.Result) error {
	if len(results) == 0 {
		_, err := fmt.Fprintln(w, "No books match.")
		return err
	}
	var b strings.Builder
	for _, r := range results {
		fmt.Fprintf(&b, "%s\n  %s · %s\n  %s\n", r.Title, r.Category, r.Format, r.Path)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func writeCheckpoint(w io.Writer, res core.CheckpointResult) error {
	switch {
	case res.DryRun && res.Committed:
		fmt.Fprintf(w, "Would checkpoint %s.\n", res.Topic)
	case !res.Committed && res.Commit == "":
		fmt.Fprintf(w, "Nothing to checkpoint in %s yet.\n", res.Topic)
	case !res.Committed:
		fmt.Fprintf(w, "Nothing changed in %s since Checkpoint %s.\n", res.Topic, short(res.Commit))
	default:
		fmt.Fprintf(w, "Checkpoint %s saved in %s.\n", short(res.Commit), res.Topic)
	}
	if len(res.LargeFiles) == 0 {
		return nil
	}
	fmt.Fprintln(w, "\nLarge files in this Checkpoint (consider adding them to .gitignore):")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, f := range res.LargeFiles {
		fmt.Fprintf(tw, "  %s\t%s\n", f.Path, humanSize(f.Size))
	}
	return tw.Flush()
}

func short(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}

// humanSize formats a size in bytes for people: "12.5 MiB".
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
