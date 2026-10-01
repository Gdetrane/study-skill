package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/mordor-forge/lamplight/v2/internal/checkpoint"
)

const eventAttemptRecorded = "attempt.recorded"

// Attempt outcomes.
const (
	OutcomePassed  = "passed"
	OutcomeFailed  = "failed"
	OutcomeErrored = "errored"
)

// DefaultCheckTimeout bounds each criterion of a Check.
const DefaultCheckTimeout = 30 * time.Minute

// maxOutputBytes is how much of a criterion's output a Check keeps: the end,
// where failures are reported.
const maxOutputBytes = 16 << 10

// Attempt is one run of a Lesson's Check against the learner's work as it
// stood at that moment. Its ID is the ID of the Event that recorded it.
type Attempt struct {
	ID     string `json:"id"`
	Lesson string `json:"lesson"`
	// CheckVersion is the version of the Check that ran.
	CheckVersion string `json:"check_version"`
	// Snapshot is the hash of practice/<lesson-id>/ when the Check ran.
	Snapshot string            `json:"snapshot"`
	Outcome  string            `json:"outcome"`
	Criteria []CriterionResult `json:"criteria"`
	// Reason explains an errored Attempt that no criterion explains.
	Reason string    `json:"reason,omitempty"`
	At     time.Time `json:"at"`
}

// CriterionResult is the outcome of one criterion in an Attempt.
type CriterionResult struct {
	ID       string `json:"id"`
	Outcome  string `json:"outcome"`
	ExitCode int    `json:"exit_code"`
	// Reason explains an errored criterion.
	Reason string `json:"reason,omitempty"`
	// Output is the end of what the command printed. It is shown to the
	// agent running the Check but never recorded in the History.
	Output string `json:"output,omitempty"`
}

// attemptRecordedData is the payload of an attempt.recorded Event.
type attemptRecordedData struct {
	Lesson       string            `json:"lesson"`
	CheckVersion string            `json:"check_version"`
	Snapshot     string            `json:"snapshot"`
	Outcome      string            `json:"outcome"`
	Criteria     []CriterionResult `json:"criteria"`
	Reason       string            `json:"reason,omitempty"`
}

// CheckOptions configures RunCheck.
type CheckOptions struct {
	// Timeout bounds each criterion. Zero means DefaultCheckTimeout.
	Timeout time.Duration
}

// RunCheck runs a Lesson's Check against the work in practice/<lesson-id>/
// and records the result as an Attempt. It runs commands the agent wrote, so
// it is reached only through the CLI, from the agent's own shell, where the
// agent's sandbox applies; the MCP server never runs a Check (ADR-0009).
//
// Each criterion's command runs in the practice folder with STUDY_TOPIC,
// STUDY_LESSON and STUDY_HELDOUT_DIR set; it passes when it exits with 0.
// The work is snapshotted before and after: if it changed while the Check
// ran, the Attempt is errored.
func (c *Core) RunCheck(ctx context.Context, topicID, lessonID string, opts CheckOptions) (Attempt, error) {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultCheckTimeout
	}
	s, dir, err := c.replayTopic(ctx, topicID)
	if err != nil {
		return Attempt{}, err
	}
	if _, err := requireLesson(s, topicID, lessonID); err != nil {
		return Attempt{}, err
	}
	home, topic, err := c.openTopicFolder(topicID)
	if err != nil {
		return Attempt{}, err
	}
	check, version, err := readCheck(topic, lessonID)
	topic.Close()
	home.Close()
	if err != nil {
		return Attempt{}, err
	}
	practice := "practice/" + lessonID
	work := filepath.Join(dir, filepath.FromSlash(practice))
	if info, err := os.Lstat(work); err != nil || !info.IsDir() {
		return Attempt{}, &Error{Code: CodeFailedPrecondition, Message: practice +
			"/ does not exist in " + topicID + ": the Lesson's exercise lives there"}
	}

	before, err := checkpoint.Snapshot(ctx, dir, practice)
	if err != nil {
		return Attempt{}, checkpointError(topicID, dir, err)
	}
	d := attemptRecordedData{Lesson: lessonID, CheckVersion: version, Snapshot: before, Outcome: OutcomePassed}
	for _, crit := range check {
		r := runCriterion(ctx, crit, work, timeout, []string{
			"STUDY_TOPIC=" + topicID,
			"STUDY_LESSON=" + lessonID,
			"STUDY_HELDOUT_DIR=" + filepath.Join(dir, ".heldout", lessonID),
		})
		switch {
		case r.Outcome == OutcomeErrored:
			d.Outcome = OutcomeErrored
		case r.Outcome == OutcomeFailed && d.Outcome == OutcomePassed:
			d.Outcome = OutcomeFailed
		}
		d.Criteria = append(d.Criteria, r)
	}
	if err := ctx.Err(); err != nil {
		return Attempt{}, err
	}
	after, err := checkpoint.Snapshot(ctx, dir, practice)
	if err != nil {
		return Attempt{}, checkpointError(topicID, dir, err)
	}
	if after != before {
		d.Outcome = OutcomeErrored
		d.Reason = "the work in " + practice + "/ changed while the Check ran: run it again"
	}

	recorded := d
	recorded.Criteria = make([]CriterionResult, len(d.Criteria))
	for i, r := range d.Criteria {
		r.Output = "" // shown, never recorded: it could reveal test data
		recorded.Criteria[i] = r
	}
	ev, err := c.writeTopic(ctx, topicID, func(*replayed, *topicView) (*change, error) {
		return &change{Type: eventAttemptRecorded, Data: recorded}, nil
	}, false)
	if err != nil {
		return Attempt{}, err
	}
	return Attempt{ID: ev.ID, Lesson: lessonID, CheckVersion: version, Snapshot: before, Outcome: d.Outcome,
		Criteria: d.Criteria, Reason: d.Reason, At: ev.Wall}, nil
}

// runCriterion runs one criterion's command. The command never inherits
// stdin, and its output is kept, bounded, for the agent to read.
func runCriterion(ctx context.Context, crit Criterion, dir string, timeout time.Duration, env []string) CriterionResult {
	r := CriterionResult{ID: crit.ID}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	name := crit.Run[0]
	if strings.Contains(name, "/") && !filepath.IsAbs(name) {
		name = filepath.Join(dir, name) // a script in the practice folder
	}
	cmd := exec.CommandContext(ctx, name, crit.Run[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out := &tailBuffer{max: maxOutputBytes}
	cmd.Stdout, cmd.Stderr = out, out
	err := cmd.Run()
	r.Output = out.String()
	var exit *exec.ExitError
	switch {
	case err == nil:
		r.Outcome = OutcomePassed
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		r.Outcome, r.ExitCode, r.Reason = OutcomeErrored, -1, fmt.Sprintf("it ran for longer than %s", timeout)
	case errors.As(err, &exit) && exit.ExitCode() >= 0:
		r.Outcome, r.ExitCode = OutcomeFailed, exit.ExitCode()
	case errors.As(err, &exit):
		r.Outcome, r.ExitCode, r.Reason = OutcomeErrored, -1, "it was stopped by a signal: "+exit.String()
	default:
		r.Outcome, r.ExitCode, r.Reason = OutcomeErrored, -1, "it could not start: "+err.Error()
	}
	return r
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf.Write(p)
	if extra := t.buf.Len() - t.max; extra > 0 {
		t.buf.Next(extra)
		t.truncated = true
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	s := strings.ToValidUTF8(t.buf.String(), "")
	if t.truncated {
		s = "…" + s
	}
	return s
}

func replayAttemptRecorded(s *replayed, ev event) error {
	var d attemptRecordedData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return fmt.Errorf("its payload is unreadable: %v", err)
	}
	l := s.study.lesson(d.Lesson)
	l.attempts = append(l.attempts, Attempt{ID: ev.ID, Lesson: d.Lesson, CheckVersion: d.CheckVersion,
		Snapshot: d.Snapshot, Outcome: d.Outcome, Criteria: d.Criteria, Reason: d.Reason, At: wallOf(ev)})
	s.recordVersion(checkItem(d.Lesson), d.CheckVersion, ev.ID)
	return nil
}

// CheckResults is a Lesson's Attempts and whether it can be completed now.
type CheckResults struct {
	Topic    string      `json:"topic"`
	Lesson   string      `json:"lesson"`
	Check    []Criterion `json:"check"`
	Attempts []Attempt   `json:"attempts"`
	// CanComplete reports whether the completion rule holds for the
	// current Check and work; Reason says why not.
	CanComplete bool   `json:"can_complete"`
	Reason      string `json:"reason,omitempty"`
	Done        bool   `json:"done"`
}

// CheckResultsOf returns a Lesson's Attempts, oldest first, and whether the
// completion rule holds for the current Check and work.
func (c *Core) CheckResultsOf(ctx context.Context, topicID, lessonID string) (CheckResults, error) {
	s, dir, err := c.replayTopic(ctx, topicID)
	if err != nil {
		return CheckResults{}, err
	}
	if _, err := requireLesson(s, topicID, lessonID); err != nil {
		return CheckResults{}, err
	}
	res := CheckResults{Topic: topicID, Lesson: lessonID, Check: []Criterion{}, Attempts: []Attempt{}}
	if ls := s.study.lessons[lessonID]; ls != nil {
		res.Attempts = append(res.Attempts, ls.attempts...)
		res.Done = ls.completed != nil
	}
	cur, err := c.currentWork(ctx, topicID, dir, lessonID)
	if err != nil {
		return CheckResults{}, err
	}
	res.Check = cur.check
	if _, err := completionRule(s, lessonID, cur); err != nil {
		res.Reason = err.Error()
	} else {
		res.CanComplete = !res.Done
		if res.Done {
			res.Reason = "Lesson " + lessonID + " is done"
		}
	}
	return res, nil
}

// work is a Lesson's Check and work as they are now.
type work struct {
	check        []Criterion
	checkVersion string
	snapshot     string
	// missing explains why the Check or the work cannot be read.
	missing error
}

// currentWork reads a Lesson's Check and snapshots its practice folder.
func (c *Core) currentWork(ctx context.Context, topicID, dir, lessonID string) (work, error) {
	var w work
	home, topic, err := c.openTopicFolder(topicID)
	if err != nil {
		return w, err
	}
	w.check, w.checkVersion, err = readCheck(topic, lessonID)
	topic.Close()
	home.Close()
	if CodeOf(err) == CodeFailedPrecondition || CodeOf(err) == CodeCorrupt {
		w.missing = err
		return w, nil
	}
	if err != nil {
		return w, err
	}
	practice := "practice/" + lessonID
	if _, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(practice))); errors.Is(err, fs.ErrNotExist) {
		w.missing = &Error{Code: CodeFailedPrecondition, Message: practice + "/ does not exist"}
		return w, nil
	}
	if w.snapshot, err = checkpoint.Snapshot(ctx, dir, practice); err != nil {
		return w, checkpointError(topicID, dir, err)
	}
	return w, nil
}

// completionRule finds the Attempt that allows completing a Lesson: for the
// current Check version, every criterion passed on an Attempt whose snapshot
// matches the current work. Changing the work or the Check after a pass
// means running the Check again.
func completionRule(s *replayed, lessonID string, w work) (Attempt, error) {
	if w.missing != nil {
		return Attempt{}, w.missing
	}
	ls := s.study.lessons[lessonID]
	if ls == nil || len(ls.attempts) == 0 {
		return Attempt{}, &Error{Code: CodeFailedPrecondition, Message: "the Check of " + lessonID +
			" has never run: run study check " + lessonID + " in your shell"}
	}
	for i := len(ls.attempts) - 1; i >= 0; i-- {
		a := ls.attempts[i]
		if a.Outcome == OutcomePassed && a.CheckVersion == w.checkVersion && a.Snapshot == w.snapshot {
			return a, nil
		}
	}
	last := ls.attempts[len(ls.attempts)-1]
	reason := "no Attempt passed"
	switch {
	case last.Outcome != OutcomePassed:
		reason = "the last Attempt " + last.Outcome
	case last.CheckVersion != w.checkVersion:
		reason = "the Check changed since it last passed"
	case last.Snapshot != w.snapshot:
		reason = "the work changed since the Check last passed"
	}
	return Attempt{}, &Error{Code: CodeFailedPrecondition, Message: fmt.Sprintf(
		"Lesson %s cannot be completed yet: %s; run study check %s in your shell on the current work", lessonID, reason, lessonID)}
}

// cleanTextBlock is cleanText for multi-line text: line breaks and tabs are
// allowed, other control characters are not.
func cleanTextBlock(field, s string, maxRunes int) (string, error) {
	s = strings.TrimSpace(s)
	for _, r := range s {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return "", invalidf("the %s contains a control character", field)
		}
	}
	if n := len([]rune(s)); n > maxRunes {
		return "", invalidf("the %s is longer than %d characters", field, maxRunes)
	}
	return s, nil
}
