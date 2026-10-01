package cli

import (
	"charm.land/lipgloss/v2"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/internal/library"
)

// Styles for human output. They are always rendered; the writer they go
// through (app.out) drops them when stdout is not a terminal or NO_COLOR is
// set, so piped output and golden files stay plain.
var (
	styleLabel  = lipgloss.NewStyle().Bold(true)
	styleDim    = lipgloss.NewStyle().Faint(true)
	styleAccent = lipgloss.NewStyle().Foreground(lipgloss.Yellow).Bold(true)
	styleOK     = lipgloss.NewStyle().Foreground(lipgloss.Green)
	styleWarn   = lipgloss.NewStyle().Foreground(lipgloss.Yellow)
	styleFail   = lipgloss.NewStyle().Foreground(lipgloss.Red).Bold(true)
)

// pad right-pads s to width columns. Padding is computed on the plain text,
// before styling, so columns line up with or without colour.
func pad(s string, width int) string {
	if n := lipgloss.Width(s); n < width {
		return s + strings.Repeat(" ", width-n)
	}
	return s
}

// writeFlags lists what replaying each Topic's History found that needs the
// learner's attention.
func writeFlags(w io.Writer, topics []core.Topic) {
	header := false
	for _, t := range topics {
		for _, f := range t.Flags {
			if !header {
				fmt.Fprintf(w, "\n%s\n", styleWarn.Render("Needs attention:"))
				header = true
			}
			fmt.Fprintf(w, "  %s: %s (flag %s)\n", styleLabel.Render(t.ID), printable(f.Message), f.ID)
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
		fmt.Fprintf(&b, "Topic %s already has those settings: nothing changed\n", t.ID)
	case dryRun:
		fmt.Fprintf(&b, "Would update Topic %s (%s)\n", styleAccent.Render(t.ID), t.Title)
	default:
		fmt.Fprintf(&b, "Updated Topic %s (%s)\n", styleAccent.Render(t.ID), t.Title)
	}
	if t.Goal != "" {
		fmt.Fprintf(&b, "  Goal: %s\n", t.Goal)
	}
	if t.KnowledgeBase != nil {
		fmt.Fprintf(&b, "  Knowledge base: %s\n", describeKnowledgeBase(t.KnowledgeBase))
	}
	writeFlags(&b, []core.Topic{t})
	_, err := io.WriteString(w, b.String())
	return err
}

func widest(items []string) int {
	w := 0
	for _, s := range items {
		w = max(w, lipgloss.Width(s))
	}
	return w
}

// writeStatus renders the status for people.
func writeStatus(w io.Writer, s core.Status, now time.Time) error {
	var b strings.Builder
	if len(s.Topics) == 0 {
		fmt.Fprintf(&b, "%s %s\n\nNo Topics yet. Start one with:\n  %s\n",
			styleLabel.Render("Study home:"), s.StudyHome, styleAccent.Render(`study topic create --title "Linear algebra"`))
		writeProblems(&b, s.Problems)
		_, err := io.WriteString(w, b.String())
		return err
	}
	names := []string{"Study home:", "Active topic:"}
	if s.LearnerProfile != "" {
		names = append(names, "Learner profile:")
	}
	labels := widest(names) + 2
	fmt.Fprintf(&b, "%s%s\n", styleLabel.Render(pad("Study home:", labels)), s.StudyHome)
	if t := s.ActiveTopic; t != nil {
		fmt.Fprintf(&b, "%s%s (%s): %s\n", styleLabel.Render(pad("Active topic:", labels)),
			styleAccent.Render(t.ID), t.Title, styleDim.Render(t.Reason))
		for _, topic := range s.Topics {
			if topic.ID == t.ID {
				writeActiveTopic(&b, topic, s.Recommended, labels, now)
			}
		}
	} else {
		fmt.Fprintf(&b, "%s%s\n", styleLabel.Render(pad("Active topic:", labels)),
			styleDim.Render("none yet: start inside a Topic's folder, or name a Topic"))
	}
	if s.LearnerProfile != "" {
		fmt.Fprintf(&b, "%s%s\n", styleLabel.Render(pad("Learner profile:", labels)), s.LearnerProfile)
	}
	fmt.Fprintf(&b, "\n%s\n", styleLabel.Render("Topics:"))
	ids := make([]string, len(s.Topics))
	for i, t := range s.Topics {
		ids[i] = t.ID
	}
	width := widest(ids) + 2
	for _, t := range s.Topics {
		id := pad(t.ID, width)
		if s.ActiveTopic != nil && t.ID == s.ActiveTopic.ID {
			id = styleAccent.Render(id)
		}
		kb := ""
		if t.KnowledgeBase != nil {
			kb = styleDim.Render("  Knowledge base: " + describeKnowledgeBase(t.KnowledgeBase))
		}
		fmt.Fprintf(&b, "  %s%s%s\n", id, t.Title, kb)
	}
	writeLessonsWithoutEvidence(&b, s.Topics)
	writeFlags(&b, s.Topics)
	writeProblems(&b, s.Problems)
	_, err := io.WriteString(w, b.String())
	return err
}

// writeActiveTopic shows the Active topic's Resume point, the one action
// recommended next, whether Cards are ready (never how many), and the
// Topic's additions to the Learner profile.
func writeActiveTopic(b *strings.Builder, t core.Topic, rec *core.Recommendation, labels int, now time.Time) {
	if t.Resume != nil {
		writeResume(b, *t.Resume, labels)
	}
	if rec != nil && rec.Action != core.ActionNextStep {
		fmt.Fprintf(b, "%s%s\n", styleLabel.Render(pad("Do next:", labels)), styleAccent.Render(rec.Text))
	}
	if c := t.Cards; c != nil {
		switch {
		case c.Ready:
			fmt.Fprintf(b, "%s%s\n", styleLabel.Render(pad("Cards:", labels)), "ready to review")
		case c.NextDue != nil:
			fmt.Fprintf(b, "%s%s\n", styleLabel.Render(pad("Cards:", labels)),
				styleDim.Render("next due "+c.NextDue.In(now.Location()).Format("2 Jan 2006")))
		}
	}
	if t.LearnerAdditions != "" {
		fmt.Fprintf(b, "%s%s\n", styleLabel.Render(pad("Additions:", labels)), styleDim.Render(t.LearnerAdditions))
	}
}

// writeResume shows where the learner stopped: the Lesson and its Phase,
// the last Break point reached, then the Next step word for word.
func writeResume(b *strings.Builder, r core.ResumePoint, labels int) {
	switch {
	case r.Lesson != "":
		where := fmt.Sprintf("%s (%s)", styleAccent.Render(r.Lesson), r.LessonTitle)
		if r.Phase != "" {
			where += ", " + r.Phase
		}
		fmt.Fprintf(b, "%s%s\n", styleLabel.Render(pad("Lesson:", labels)), where)
	case r.SyllabusDone:
		fmt.Fprintf(b, "%s%s\n", styleLabel.Render(pad("Lesson:", labels)), "every Lesson in the Syllabus is done")
	}
	if p := r.BreakPoint; p != nil {
		text := p.ID
		if p.Describe != "" {
			text += styleDim.Render(": " + printable(p.Describe))
		}
		fmt.Fprintf(b, "%s%s\n", styleLabel.Render(pad("Break point:", labels)), text)
	}
	if r.NextStep != nil {
		fmt.Fprintf(b, "%s%s\n", styleLabel.Render(pad("Next step:", labels)), styleAccent.Render(r.NextStep.Step))
		if r.NextStep.Context != "" {
			fmt.Fprintf(b, "%s%s\n", pad("", labels), styleDim.Render(strings.ReplaceAll(r.NextStep.Context, "\n", "\n"+pad("", labels))))
		}
	}
	if r.OpenSession != nil {
		fmt.Fprintf(b, "%s%s\n", pad("", labels), styleDim.Render("A Session opened "+
			r.OpenSession.Opened.Format("2 Jan 15:04")+" is still open."))
	}
}

func writeProblems(b *strings.Builder, problems []core.TopicProblem) {
	if len(problems) == 0 {
		return
	}
	fmt.Fprintf(b, "\n%s\n", styleWarn.Render("Topics that could not be read:"))
	for _, p := range problems {
		fmt.Fprintf(b, "  %s: %s\n", styleLabel.Render(p.ID), p.Message)
	}
}

func writeTopicCreated(w io.Writer, t core.Topic, dryRun bool) error {
	verb := "Created"
	if dryRun {
		verb = "Would create"
	}
	_, err := fmt.Fprintf(w, "%s Topic %s (%s) in %s\n", verb, styleAccent.Render(t.ID), t.Title, styleDim.Render(t.Path))
	return err
}

func writeLibrarySummary(w io.Writer, s core.LibrarySummary) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Indexed %s books from %s\n", styleAccent.Render(fmt.Sprint(s.Books)), s.Root)
	if n := len(s.Skipped); n > 0 {
		fmt.Fprintf(&b, "%s\n", styleWarn.Render(fmt.Sprintf("Skipped %d entries that could not be read:", n)))
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
		fmt.Fprintf(&b, "%s\n  %s\n  %s\n", styleLabel.Render(r.Title),
			styleDim.Render(r.Category+" · "+r.Format), r.Path)
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
		fmt.Fprintf(w, "Checkpoint %s saved in %s.\n", short(res.Commit), styleAccent.Render(res.Topic))
	}
	if len(res.LargeFiles) == 0 {
		return nil
	}
	fmt.Fprintf(w, "\n%s\n", styleWarn.Render("Large files in this Checkpoint (consider adding them to .gitignore):"))
	paths := make([]string, len(res.LargeFiles))
	for i, f := range res.LargeFiles {
		paths[i] = f.Path
	}
	width := widest(paths) + 2
	for _, f := range res.LargeFiles {
		fmt.Fprintf(w, "  %s%s\n", pad(f.Path, width), humanSize(f.Size))
	}
	return nil
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

// writeDiagnosis renders study doctor's Findings, one per line, with the fix
// under each warning or failure.
func writeDiagnosis(w io.Writer, d core.Diagnosis) error {
	var b strings.Builder
	names := make([]string, len(d.Findings))
	for i, f := range d.Findings {
		names[i] = f.Name
	}
	width := widest(names) + 2
	var warnings, failures int
	for _, f := range d.Findings {
		mark := styleOK.Render("✓")
		switch f.Status {
		case core.FindingWarn:
			mark, warnings = styleWarn.Render("!"), warnings+1
		case core.FindingFail:
			mark, failures = styleFail.Render("✗"), failures+1
		}
		fmt.Fprintf(&b, "%s %s%s\n", mark, styleLabel.Render(pad(f.Name, width)), f.Message)
		if f.Fix != "" {
			fmt.Fprintf(&b, "  %s%s %s\n", strings.Repeat(" ", width), styleDim.Render("fix:"), f.Fix)
		}
	}
	b.WriteString("\n")
	switch {
	case failures > 0:
		b.WriteString(styleFail.Render(fmt.Sprintf("Fix the %d %s marked ✗ before studying.",
			failures, plural(failures, "failure", "failures"))))
	case warnings > 0:
		b.WriteString(styleWarn.Render(fmt.Sprintf("Ready to study, with %d %s.", warnings, plural(warnings, "warning", "warnings"))))
	default:
		b.WriteString(styleOK.Render("Everything looks good."))
	}
	b.WriteString("\n")
	_, err := io.WriteString(w, b.String())
	return err
}

func writeInstallResult(w io.Writer, r installResult) error {
	var b strings.Builder
	if r.ProvidedBy != "" {
		fmt.Fprintf(&b, "A package already provides %s completions at %s, so study installed nothing.\n",
			styleAccent.Render(r.Shell), r.ProvidedBy)
		fmt.Fprintf(&b, "%s\n", styleDim.Render("Pass --force to install your own."))
		_, err := io.WriteString(w, b.String())
		return err
	}
	install, add := "Installed", "Added"
	if r.DryRun {
		install, add = "Would install", "Would add"
	}
	fmt.Fprintf(&b, "%s %s completions in %s\n", install, styleAccent.Render(r.Shell), r.File)
	if r.RCLine != "" && len(r.Manual) == 0 {
		fmt.Fprintf(&b, "%s this line to %s:\n  %s\n", add, r.RCFile, styleDim.Render(r.RCLine))
	}
	writeManual(&b, r.Manual)
	if !r.DryRun {
		fmt.Fprintf(&b, "%s\n", styleDim.Render(sentence(r.Note)))
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func writeUninstallResult(w io.Writer, r uninstallResult) error {
	var b strings.Builder
	if len(r.Shells) == 0 {
		fmt.Fprintf(&b, "Nothing to remove: %s.\n", r.Note)
	}
	verb := "Removed"
	if r.DryRun {
		verb = "Would remove"
	}
	for _, path := range r.Removed {
		fmt.Fprintf(&b, "%s %s\n", verb, path)
	}
	for _, path := range r.Kept {
		fmt.Fprintf(&b, "Kept %s: it changed since study installed it\n", path)
	}
	for _, path := range r.AlreadyGone {
		fmt.Fprintf(&b, "%s was already gone\n", path)
	}
	for _, rc := range r.RCLines {
		switch rc.Status {
		case rcRemoved:
			fmt.Fprintf(&b, "%s this line from %s:\n  %s\n", verb, rc.File, styleDim.Render(rc.Line))
		case rcAlreadyGone:
			fmt.Fprintf(&b, "The line study added to %s was already gone\n", rc.File)
		}
	}
	writeManual(&b, r.Manual)
	_, err := io.WriteString(w, b.String())
	return err
}

// writeManual lists what is left for the learner to do by hand.
func writeManual(b *strings.Builder, steps []string) {
	if len(steps) == 0 {
		return
	}
	fmt.Fprintf(b, "%s\n", styleWarn.Render("Left for you to do:"))
	for _, step := range steps {
		fmt.Fprintf(b, "  - %s\n", strings.ReplaceAll(step, "\n", "\n    "))
	}
}

// sentence capitalises the first letter of s and ends it with a full stop.
func sentence(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:] + "."
}

// writeLessonsWithoutEvidence lists the Lessons that cite no Evidence yet.
// They are a reminder, never a block.
func writeLessonsWithoutEvidence(w io.Writer, topics []core.Topic) {
	header := false
	for _, t := range topics {
		if len(t.LessonsWithoutEvidence) == 0 {
			continue
		}
		if !header {
			fmt.Fprintf(w, "\n%s\n", styleLabel.Render("Lessons without Evidence:"))
			header = true
		}
		fmt.Fprintf(w, "  %s: %s\n", styleLabel.Render(t.ID), strings.Join(t.LessonsWithoutEvidence, ", "))
	}
}
