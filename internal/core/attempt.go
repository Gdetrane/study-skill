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
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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

// CriterionResult is the outcome of one run or held-out criterion in an
// Attempt.
type CriterionResult struct {
	ID string `json:"id"`
	// Kind is run or held-out; empty in Attempts recorded before held-out
	// criteria existed, which were all runs.
	Kind     string `json:"kind,omitempty"`
	Outcome  string `json:"outcome"`
	ExitCode int    `json:"exit_code"`
	// Reason explains an errored criterion.
	Reason string `json:"reason,omitempty"`
	// Results is what the criterion wrote to its results file, if anything.
	Results *CriterionScores `json:"results,omitempty"`
	// Counted, for a held-out criterion that produced results, says whether
	// this run is its counted measurement: the first run of that criterion
	// in the Lesson to produce results. Later runs are not counted.
	Counted *bool `json:"counted,omitempty"`
	// Output is the end of what a run criterion's command printed. It is
	// shown to the agent running the Check but never recorded in the
	// History, and never kept for held-out criteria, so Held-out data
	// cannot leak through it.
	Output string `json:"output,omitempty"`
}

// CriterionScores is what a criterion's command reported in its results
// file: whether it passed, a score out of a maximum, named metrics and a
// short summary. Every field is optional.
type CriterionScores struct {
	Passed  *bool              `json:"passed,omitempty"`
	Score   *float64           `json:"score,omitempty"`
	Max     *float64           `json:"max,omitempty"`
	Metrics map[string]float64 `json:"metrics,omitempty"`
	Summary string             `json:"summary,omitempty"`
}

// kind is the criterion's kind, run for Attempts recorded before kinds.
func (r CriterionResult) kind() string {
	if r.Kind == "" {
		return CriterionRun
	}
	return r.Kind
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
	// Progress, if set, is told when each criterion starts and ends, and
	// every ProgressEvery while one runs. It is never called concurrently.
	Progress func(CheckProgress)
	// ProgressEvery is how often a running criterion is reported. Zero
	// means DefaultProgressEvery.
	ProgressEvery time.Duration
}

// DefaultProgressEvery is how often a long criterion is reported as running.
const DefaultProgressEvery = 15 * time.Second

// Progress states of a criterion.
const (
	ProgressStarted  = "started"
	ProgressRunning  = "running"
	ProgressFinished = "finished"
)

// CheckProgress reports a criterion of a running Check.
type CheckProgress struct {
	Criterion string
	Kind      string
	// Index counts criteria from 1, out of Total that run.
	Index, Total int
	State        string
	// Elapsed is how long the criterion has run.
	Elapsed time.Duration
	// Outcome is set when the criterion finished.
	Outcome string
}

// RunCheck runs a Lesson's Check against the work in practice/<lesson-id>/
// and records the result as an Attempt. It runs commands the agent wrote, so
// it is reached only through the CLI, from the agent's own shell, where the
// agent's sandbox applies; the MCP server never runs a Check (ADR-0009).
//
// Each run and held-out criterion's command runs in the practice folder, in
// a process group of its own, with STUDY_TOPIC, STUDY_LESSON and
// STUDY_RESULTS set, and STUDY_HELDOUT_DIR for held-out criteria only. A
// command may write a results file (see CriterionScores) to STUDY_RESULTS;
// held-out criteria must. A run criterion passes when its command exits
// with 0, leaves nothing running and does not report passed: false. The
// Attempt's outcome comes from the run criteria alone: held-out results are
// diagnostic, and rubric items are graded with RecordRubricGrade. The work
// is snapshotted before and after (see checkpoint.SnapshotWork): work the
// snapshot cannot fully see, or that changed while the Check ran, makes the
// Attempt errored. When ctx is cancelled, as when study receives SIGTERM,
// every process the Check started is stopped and nothing is recorded.
func (c *Core) RunCheck(ctx context.Context, topicID, lessonID string, opts CheckOptions) (Attempt, error) {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultCheckTimeout
	}
	s, dir, err := c.replayTopic(ctx, topicID)
	if err != nil {
		return Attempt{}, err
	}
	if _, err := requireStudiedLesson(s, topicID, lessonID); err != nil {
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
	var runnable []Criterion
	for _, crit := range check {
		if crit.Kind != CriterionRubric {
			runnable = append(runnable, crit)
		}
	}
	if len(runnable) == 0 {
		return Attempt{}, &Error{Code: CodeFailedPrecondition, Message: "the Check of " + lessonID +
			" has only rubric items, so there is nothing to run: grade each one with rubric_record"}
	}
	practice := practiceFolder(lessonID)
	workDir := filepath.Join(dir, filepath.FromSlash(practice))
	if info, err := os.Lstat(workDir); err != nil || !info.IsDir() {
		return Attempt{}, &Error{Code: CodeFailedPrecondition, Message: practice +
			"/ does not exist in " + topicID + ": the Lesson's exercise lives there"}
	}

	d := attemptRecordedData{Lesson: lessonID, CheckVersion: version, Outcome: OutcomePassed}
	before, err := checkpoint.SnapshotWork(ctx, dir, practice)
	switch {
	case blindSpot(err):
		// Nothing runs on work the snapshot cannot see whole.
		d.Outcome, d.Reason = OutcomeErrored, blindSpotReason(practice, err)
		return c.recordAttempt(ctx, topicID, d, nil)
	case err != nil:
		return Attempt{}, checkpointError(topicID, dir, err)
	}
	d.Snapshot = before.Hash
	heldOutDir := filepath.Join(dir, heldOutFolder(lessonID))
	every := opts.ProgressEvery
	if every <= 0 {
		every = DefaultProgressEvery
	}
	for i, crit := range runnable {
		if ctx.Err() != nil {
			break
		}
		env := []string{"STUDY_TOPIC=" + topicID, "STUDY_LESSON=" + lessonID}
		var r CriterionResult
		if crit.Kind == CriterionHeldOut {
			env = append(env, "STUDY_HELDOUT_DIR="+heldOutDir)
		}
		report := progressReporter(opts.Progress, crit, i+1, len(runnable), every)
		if info, err := os.Stat(heldOutDir); crit.Kind == CriterionHeldOut && (err != nil || !info.IsDir()) {
			r = CriterionResult{ID: crit.ID, Kind: crit.Kind, Outcome: OutcomeErrored, ExitCode: -1,
				Reason: "there is no Held-out data in " + heldOutFolder(lessonID) + "/: write it there before practicing starts"}
		} else {
			stop := report.start()
			r = runCriterion(ctx, crit, workDir, timeout, env)
			stop()
		}
		report.finish(r.Outcome)
		if crit.Kind == CriterionRun {
			switch {
			case r.Outcome == OutcomeErrored:
				d.Outcome = OutcomeErrored
			case r.Outcome == OutcomeFailed && d.Outcome == OutcomePassed:
				d.Outcome = OutcomeFailed
			}
		}
		d.Criteria = append(d.Criteria, r)
	}
	if ctx.Err() != nil {
		return Attempt{}, &Error{Code: CodeCanceled, Err: ctx.Err(), Message: "the Check of " + lessonID +
			" was stopped before it finished, and every program it started was stopped too; no Attempt was recorded"}
	}
	after, err := checkpoint.SnapshotWork(ctx, dir, practice)
	switch {
	case blindSpot(err):
		d.Outcome, d.Reason = OutcomeErrored, "while the Check ran, "+blindSpotReason(practice, err)
	case err != nil:
		return Attempt{}, checkpointError(topicID, dir, err)
	case after.Hash != before.Hash:
		d.Outcome = OutcomeErrored
		d.Reason = fmt.Sprintf("the Check changed %s in %s/ while it ran; if a Check writes files, such as "+
			"build outputs, list them in %s/.gitignore, then run it again", strings.Join(after.Changed(before), ", "),
			practice, practice)
	}
	return c.recordAttempt(ctx, topicID, d, d.Criteria)
}

// recordAttempt records an Attempt and returns it with the output of each
// run criterion, which is shown but never recorded: it could reveal test
// data. Whether each held-out result is the counted measurement is decided
// under the Topic's lock, from the History as it stands.
func (c *Core) recordAttempt(ctx context.Context, topicID string, d attemptRecordedData, shown []CriterionResult) (Attempt, error) {
	d.Criteria = make([]CriterionResult, len(shown))
	for i, r := range shown {
		r.Output = ""
		d.Criteria[i] = r
	}
	ev, err := c.writeTopic(ctx, topicID, func(s *replayed, _ *topicView) (*change, error) {
		ls := s.study.lessons[d.Lesson]
		for i := range d.Criteria {
			r := &d.Criteria[i]
			if r.kind() != CriterionHeldOut || r.Results == nil || r.Outcome == OutcomeErrored {
				r.Counted = nil
				continue
			}
			counted := ls == nil || ls.heldOut[r.ID] == ""
			r.Counted = &counted
		}
		return &change{Type: eventAttemptRecorded, Data: d}, nil
	}, false)
	if err != nil {
		return Attempt{}, err
	}
	criteria := make([]CriterionResult, len(shown))
	for i, r := range shown {
		r.Counted = d.Criteria[i].Counted
		if r.kind() == CriterionHeldOut {
			r.Output = ""
		}
		criteria[i] = r
	}
	return Attempt{ID: ev.ID, Lesson: d.Lesson, CheckVersion: d.CheckVersion, Snapshot: d.Snapshot, Outcome: d.Outcome,
		Criteria: criteria, Reason: d.Reason, At: ev.Wall}, nil
}

func practiceFolder(lessonID string) string { return "practice/" + lessonID }

// heldOutFolder is where a Lesson's Held-out data lives in its Topic. It is
// committed with the Topic, so every machine measures on the same data, and
// it is synthetic or public, never personal data. Lamplight never shows its
// contents, or the raw output of a held-out command.
func heldOutFolder(lessonID string) string { return ".heldout/" + lessonID }

// progress reports one criterion of a running Check to CheckOptions.Progress.
type progress struct {
	report       func(CheckProgress)
	crit         Criterion
	index, total int
	every        time.Duration
	started      time.Time
}

func progressReporter(report func(CheckProgress), crit Criterion, index, total int, every time.Duration) *progress {
	return &progress{report: report, crit: crit, index: index, total: total, every: every}
}

func (p *progress) send(state, outcome string) {
	if p.report == nil {
		return
	}
	var elapsed time.Duration
	if !p.started.IsZero() {
		elapsed = time.Since(p.started)
	}
	p.report(CheckProgress{Criterion: p.crit.ID, Kind: p.crit.Kind, Index: p.index, Total: p.total,
		State: state, Elapsed: elapsed, Outcome: outcome})
}

// start reports the criterion started, then reports it running at every
// tick until the returned stop is called; stop waits for the last report.
func (p *progress) start() (stop func()) {
	p.started = time.Now()
	p.send(ProgressStarted, "")
	if p.report == nil {
		return func() {}
	}
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(p.every)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				p.send(ProgressRunning, "")
			}
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}

func (p *progress) finish(outcome string) { p.send(ProgressFinished, outcome) }

// blindSpot reports whether err is work a snapshot cannot see whole.
func blindSpot(err error) bool {
	for _, e := range []error{checkpoint.ErrHiddenEntries, checkpoint.ErrNestedRepository,
		checkpoint.ErrLinkOutside, checkpoint.ErrAllIgnored} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// blindSpotReason explains work a snapshot cannot see whole, and what to do.
func blindSpotReason(practice string, err error) string {
	switch {
	case errors.Is(err, checkpoint.ErrHiddenEntries):
		return fmt.Sprintf("%v; clear the flag with git update-index --no-skip-worktree --no-assume-unchanged", err)
	case errors.Is(err, checkpoint.ErrNestedRepository):
		return fmt.Sprintf("%v; a Check can only judge work whose every file the Topic's own repository sees, "+
			"so move the other repository out of %s/", err, practice)
	case errors.Is(err, checkpoint.ErrLinkOutside):
		return fmt.Sprintf("%v; copy what it points to into %s/ instead", err, practice)
	default:
		return fmt.Sprintf("every file in %s/ is ignored, so there is no work to judge; check its .gitignore", practice)
	}
}

// checkGrace is how long a Check's programs get to stop after SIGTERM before
// they are killed, and how long study waits for output from programs a
// finished criterion left running.
const checkGrace = 2 * time.Second

// runCriterion runs one criterion's command in a process group of its own.
// The command never inherits stdin, and its output is kept, bounded, for the
// agent to read. When it exits, anything left running in its group is
// killed, and the criterion is errored: a Check must finish what it starts.
// The command may write a results file to STUDY_RESULTS, in a folder of its
// own outside the practice folder, so writing it never changes the work.
func runCriterion(ctx context.Context, crit Criterion, dir string, timeout time.Duration, env []string) CriterionResult {
	r := CriterionResult{ID: crit.ID, Kind: crit.Kind}
	resultsDir, err := os.MkdirTemp("", "study-results-")
	if err != nil {
		r.Outcome, r.ExitCode, r.Reason = OutcomeErrored, -1, "study could not make a folder for its results file: "+err.Error()
		return r
	}
	defer os.RemoveAll(resultsDir)
	resultsPath := filepath.Join(resultsDir, "results.json")

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	name := crit.Run[0]
	if strings.Contains(name, "/") && !filepath.IsAbs(name) {
		name = filepath.Join(dir, name) // a script in the practice folder
	}
	cmd := exec.CommandContext(ctx, name, crit.Run[1:]...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), env...), "STUDY_RESULTS="+resultsPath)
	ownProcessGroup(cmd)
	out := &tailBuffer{max: maxOutputBytes}
	cmd.Stdout, cmd.Stderr = out, out
	err = cmd.Run()
	leftover := cmd.Process != nil && stopGroup(cmd.Process.Pid)
	r.Output = out.String()
	var exit *exec.ExitError
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		r.Outcome, r.ExitCode, r.Reason = OutcomeErrored, -1, fmt.Sprintf("it ran for longer than %s", timeout)
		return r
	case ctx.Err() != nil:
		r.Outcome, r.ExitCode, r.Reason = OutcomeErrored, -1, "it was stopped"
		return r
	case leftover || errors.Is(err, exec.ErrWaitDelay):
		r.Outcome, r.ExitCode, r.Reason = OutcomeErrored, -1,
			"it left programs running in the background, which were stopped; a Check must finish everything it starts"
		return r
	case err == nil:
		r.Outcome = OutcomePassed
	case errors.As(err, &exit) && exit.ExitCode() >= 0:
		r.Outcome, r.ExitCode = OutcomeFailed, exit.ExitCode()
	case errors.As(err, &exit):
		r.Outcome, r.ExitCode, r.Reason = OutcomeErrored, -1, "it was stopped by a signal: "+exit.String()
		return r
	default:
		r.Outcome, r.ExitCode, r.Reason = OutcomeErrored, -1, "it could not start: "+err.Error()
		return r
	}

	scores, present, problem := readResults(resultsPath)
	switch {
	case problem != "":
		r.Outcome, r.Reason = OutcomeErrored, "its results file is not valid: "+problem
	case !present && crit.Kind == CriterionHeldOut:
		r.Outcome, r.Reason = OutcomeErrored, "it wrote no results file: a held-out command writes its scores to "+
			"the file named by STUDY_RESULTS"
	case present:
		r.Results = scores
		if scores.Passed != nil && !*scores.Passed && r.Outcome == OutcomePassed {
			r.Outcome = OutcomeFailed
		}
	}
	return r
}

// Limits of a results file.
const (
	maxResultsBytes        = 64 << 10
	maxMetrics             = 20
	maxResultsSummaryRunes = 1000
	resultsFormatMax       = 1
)

var metricNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,39}$`)

// resultsFile is the JSON a criterion's command may write to STUDY_RESULTS:
//
//	{"format": 1, "passed": true, "score": 17, "max": 20,
//	 "metrics": {"precision": 0.91}, "summary": "17 of 20 cases"}
//
// Every field is optional; other fields are ignored and never recorded.
type resultsFile struct {
	Format  *int               `json:"format"`
	Passed  *bool              `json:"passed"`
	Score   *float64           `json:"score"`
	Max     *float64           `json:"max"`
	Metrics map[string]float64 `json:"metrics"`
	Summary *string            `json:"summary"`
}

// readResults reads and checks a results file. present is false when the
// command wrote none; problem explains an invalid one.
func readResults(path string) (scores *CriterionScores, present bool, problem string) {
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, false, ""
	case err != nil:
		return nil, true, err.Error()
	case !info.Mode().IsRegular():
		return nil, true, "it is not a regular file"
	case info.Size() > maxResultsBytes:
		return nil, true, fmt.Sprintf("it is larger than %d KiB", maxResultsBytes>>10)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, true, err.Error()
	}
	var f resultsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, true, "it is not a JSON object with the documented fields: " + err.Error()
	}
	switch {
	case f.Format != nil && *f.Format > resultsFormatMax:
		return nil, true, fmt.Sprintf("it has format %d, but this version of study understands format %d: upgrade study",
			*f.Format, resultsFormatMax)
	case f.Format != nil && *f.Format < 1:
		return nil, true, "its format must be 1"
	}
	s := &CriterionScores{Passed: f.Passed, Score: f.Score, Max: f.Max}
	if s.Score != nil {
		if s.Max == nil {
			one := 1.0
			s.Max = &one
		}
		if *s.Score < 0 {
			return nil, true, "its score is negative"
		}
	}
	if s.Max != nil {
		if *s.Max <= 0 {
			return nil, true, "its max must be more than 0"
		}
		if s.Score != nil && *s.Score > *s.Max {
			return nil, true, fmt.Sprintf("its score %g is more than its max %g", *s.Score, *s.Max)
		}
	}
	if len(f.Metrics) > maxMetrics {
		return nil, true, fmt.Sprintf("it has more than %d metrics", maxMetrics)
	}
	for name := range f.Metrics {
		if !metricNamePattern.MatchString(name) {
			return nil, true, fmt.Sprintf("the metric name %q is not a short name of lowercase letters, digits, "+
				"underscores, dots and hyphens", name)
		}
	}
	if len(f.Metrics) > 0 {
		s.Metrics = f.Metrics
	}
	if f.Summary != nil {
		summary, err := cleanTextBlock("summary", *f.Summary, maxResultsSummaryRunes)
		if err != nil {
			return nil, true, err.Error()
		}
		s.Summary = summary
	}
	return s, true, ""
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
	// The counted measurement of a held-out criterion is the first run, in
	// replay order, that produced results; an errored run never uses it up.
	// A run that claimed to be counted when it was recorded but is not, as
	// when two machines each measured first, is flagged.
	for i := range d.Criteria {
		r := &d.Criteria[i]
		if r.kind() != CriterionHeldOut || r.Results == nil || r.Outcome == OutcomeErrored {
			r.Counted = nil
			continue
		}
		claimed := r.Counted != nil && *r.Counted
		counted := l.heldOut[r.ID] == ""
		if counted {
			if l.heldOut == nil {
				l.heldOut = map[string]string{}
			}
			l.heldOut[r.ID] = ev.ID
		} else if claimed {
			s.flag(newFlag(FlagConflict, checkItem(d.Lesson), []string{l.heldOut[r.ID], ev.ID}, r.ID,
				fmt.Sprintf("held-out criterion %s of Lesson %s was measured first twice, by Attempts %s and %s, "+
					"probably on two machines: the first counts, and the other is recorded as not counted",
					r.ID, d.Lesson, l.heldOut[r.ID], ev.ID)))
		}
		r.Counted = &counted
	}
	l.attempts = append(l.attempts, Attempt{ID: ev.ID, Lesson: d.Lesson, CheckVersion: d.CheckVersion,
		Snapshot: d.Snapshot, Outcome: d.Outcome, Criteria: d.Criteria, Reason: d.Reason, At: wallOf(ev)})
	return nil
}

// CheckResults is a Lesson's Attempts and whether it can be completed now.
type CheckResults struct {
	Topic    string      `json:"topic"`
	Lesson   string      `json:"lesson"`
	Check    []Criterion `json:"check"`
	Attempts []Attempt   `json:"attempts"`
	// Rubric is each rubric item's latest grade, if any, in the order of
	// the Check.
	Rubric []RubricStatus `json:"rubric"`
	// HeldOut is each held-out criterion's counted measurement and latest
	// results, in the order of the Check. They never decide completion.
	HeldOut []HeldOutStatus `json:"held_out"`
	// CanComplete reports whether the completion rule holds for the
	// current Check and work; Reason says why not.
	CanComplete bool   `json:"can_complete"`
	Reason      string `json:"reason,omitempty"`
	Done        bool   `json:"done"`
	// Next says what to do after a failed Attempt during feedback: give
	// feedback, then go back to practicing with a Next step naming the fix.
	Next string `json:"next,omitempty"`
}

// RubricStatus is a rubric item and its latest grade.
type RubricStatus struct {
	Criterion string       `json:"criterion"`
	Rubric    string       `json:"rubric"`
	Grade     *RubricGrade `json:"grade,omitempty"`
	// Current reports whether the grade is for the Check shown to the
	// learner and the work as it is now, which completion needs.
	Current bool `json:"current"`
}

// HeldOutStatus is a held-out criterion's counted measurement, which stays
// as it was, and its latest results.
type HeldOutStatus struct {
	Criterion string `json:"criterion"`
	// Counted is the counted measurement: the first run that produced
	// results, and the Attempt that recorded it.
	Counted        *CriterionScores `json:"counted,omitempty"`
	CountedAttempt string           `json:"counted_attempt,omitempty"`
	// Latest is the latest run's results, when it is not the counted one;
	// it is recorded as not counted.
	Latest        *CriterionScores `json:"latest,omitempty"`
	LatestAttempt string           `json:"latest_attempt,omitempty"`
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
	res := CheckResults{Topic: topicID, Lesson: lessonID, Check: []Criterion{}, Attempts: []Attempt{},
		Rubric: []RubricStatus{}, HeldOut: []HeldOutStatus{}}
	ls := s.study.lessons[lessonID]
	if ls != nil {
		res.Attempts = append(res.Attempts, ls.attempts...)
		res.Done = ls.completed != nil
	}
	cur, err := c.currentWork(ctx, topicID, dir, lessonID)
	if err != nil {
		return CheckResults{}, err
	}
	res.Check = cur.check
	for _, crit := range cur.check {
		switch crit.Kind {
		case CriterionRubric:
			st := RubricStatus{Criterion: crit.ID, Rubric: crit.Rubric}
			if ls != nil && ls.grades[crit.ID] != nil {
				g := *ls.grades[crit.ID]
				st.Grade = &g
				st.Current = g.CheckVersion == ls.shownCheck && g.CheckVersion == cur.checkVersion && g.Snapshot == cur.snapshot
			}
			res.Rubric = append(res.Rubric, st)
		case CriterionHeldOut:
			res.HeldOut = append(res.HeldOut, heldOutStatus(ls, crit.ID))
		}
	}
	if _, err := completionRule(s, lessonID, cur); err != nil {
		res.Reason = err.Error()
	} else {
		res.CanComplete = !res.Done
		if res.Done {
			res.Reason = "Lesson " + lessonID + " is done"
		}
	}
	if ls != nil && !res.Done && failedDuringFeedback(ls) {
		res.Next = "give the learner feedback on the failed Attempt, then move the Lesson back to practicing with " +
			"phase_set and a Next step that names the fix"
	}
	return res, nil
}

// heldOutStatus finds a held-out criterion's counted measurement and its
// latest results in a Lesson's Attempts.
func heldOutStatus(ls *lessonState, criterion string) HeldOutStatus {
	st := HeldOutStatus{Criterion: criterion}
	if ls == nil {
		return st
	}
	for _, a := range ls.attempts {
		for _, r := range a.Criteria {
			if r.ID != criterion || r.kind() != CriterionHeldOut || r.Results == nil || r.Outcome == OutcomeErrored {
				continue
			}
			if r.Counted != nil && *r.Counted {
				st.Counted, st.CountedAttempt = r.Results, a.ID
				st.Latest, st.LatestAttempt = nil, ""
			} else {
				st.Latest, st.LatestAttempt = r.Results, a.ID
			}
		}
	}
	return st
}

// failedDuringFeedback reports whether a Lesson is in feedback and its
// latest Attempt failed: it goes back to practicing, with a Next step that
// names the fix.
func failedDuringFeedback(ls *lessonState) bool {
	return ls.phase == PhaseFeedback && len(ls.attempts) > 0 && ls.attempts[len(ls.attempts)-1].Outcome == OutcomeFailed
}

// work is a Lesson's Check and work as they are now.
type work struct {
	check        []Criterion
	checkVersion string
	snapshot     string
	// missing explains why the Check or the work cannot be judged.
	missing error
}

// currentWork reads a Lesson's Check and snapshots its practice folder,
// writing nothing, not even git objects.
func (c *Core) currentWork(ctx context.Context, topicID, dir, lessonID string) (work, error) {
	w := work{check: []Criterion{}}
	home, topic, err := c.openTopicFolder(topicID)
	if err != nil {
		return w, err
	}
	check, version, err := readCheck(topic, lessonID)
	topic.Close()
	home.Close()
	if CodeOf(err) == CodeFailedPrecondition || CodeOf(err) == CodeCorrupt {
		w.missing = err
		return w, nil
	}
	if err != nil {
		return w, err
	}
	w.check, w.checkVersion = check, version
	practice := practiceFolder(lessonID)
	if _, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(practice))); errors.Is(err, fs.ErrNotExist) {
		w.missing = &Error{Code: CodeFailedPrecondition, Message: practice + "/ does not exist"}
		return w, nil
	}
	snap, err := checkpoint.SnapshotWork(ctx, dir, practice)
	if blindSpot(err) {
		w.missing = &Error{Code: CodeFailedPrecondition, Message: blindSpotReason(practice, err)}
		return w, nil
	}
	if err != nil {
		return w, checkpointError(topicID, dir, err)
	}
	w.snapshot = snap.Hash
	return w, nil
}

// completion is what the completion rule relied on: the passing Attempt, if
// the Check has run criteria, and the rubric grades, if it has rubric items.
type completion struct {
	attempt Attempt
	grades  []string
}

// completionRule finds what allows completing a Lesson: for the Check
// version shown to the learner when practicing last started, which must
// still be the current one, every run criterion passed on an Attempt of that
// Check whose snapshot matches the current work, and every rubric item has a
// grade for that Check and that work. Held-out criteria never decide it.
// Changing the work or the Check after a pass means running the Check, and
// grading, again, and a changed Check must be shown again through phase_set
// practicing.
func completionRule(s *replayed, lessonID string, w work) (completion, error) {
	var done completion
	if w.missing != nil {
		return done, w.missing
	}
	cannot := func(reason string) error {
		return &Error{Code: CodeFailedPrecondition, Message: fmt.Sprintf("Lesson %s cannot be completed yet: %s", lessonID, reason)}
	}
	ls := s.study.lessons[lessonID]
	if ls == nil || ls.shownCheck == "" {
		return done, cannot("its Check was never shown to the learner: move it to practicing with phase_set first")
	}
	if w.checkVersion != ls.shownCheck {
		return done, cannot("the Check changed since it was shown to the learner when practicing started; " +
			"show the learner the new Check with phase_set practicing, then run it again")
	}
	var runs, rubric []Criterion
	for _, crit := range w.check {
		switch crit.Kind {
		case CriterionRun:
			runs = append(runs, crit)
		case CriterionRubric:
			rubric = append(rubric, crit)
		}
	}
	if len(runs) > 0 {
		a, err := passingAttempt(ls, lessonID, w, cannot)
		if err != nil {
			return done, err
		}
		done.attempt = a
	}
	for _, crit := range rubric {
		g := ls.grades[crit.ID]
		switch {
		case g == nil:
			return done, cannot("rubric item " + crit.ID + " has no grade: grade it with rubric_record, after the " +
				"learner checks their work against it")
		case g.CheckVersion != ls.shownCheck:
			return done, cannot("rubric item " + crit.ID + " was graded for another version of the Check: grade it again")
		case g.Snapshot != w.snapshot:
			return done, cannot("the work changed since rubric item " + crit.ID + " was graded: grade it again")
		}
		done.grades = append(done.grades, g.Event)
	}
	return done, nil
}

// passingAttempt finds the latest Attempt of the shown Check, on the current
// work, whose run criteria all passed: an Attempt's outcome comes from its
// run criteria alone.
func passingAttempt(ls *lessonState, lessonID string, w work, cannot func(string) error) (Attempt, error) {
	if len(ls.attempts) == 0 {
		return Attempt{}, cannot("its Check has never run: run study check " + lessonID + " in your shell")
	}
	for i := len(ls.attempts) - 1; i >= 0; i-- {
		a := ls.attempts[i]
		if a.Outcome == OutcomePassed && a.CheckVersion == ls.shownCheck && a.Snapshot == w.snapshot {
			return a, nil
		}
	}
	last := ls.attempts[len(ls.attempts)-1]
	reason := "no Attempt passed"
	switch {
	case last.Outcome != OutcomePassed:
		reason = "the last Attempt " + last.Outcome
	case last.CheckVersion != ls.shownCheck:
		reason = "the Check that last passed is not the one shown to the learner"
	case last.Snapshot != w.snapshot:
		reason = "the work changed since the Check last passed"
	}
	return Attempt{}, cannot(reason + "; run study check " + lessonID + " in your shell on the current work")
}

// cleanTextBlock is cleanText for multi-line text: line breaks and tabs are
// allowed, other control characters are not.
func cleanTextBlock(field, s string, maxRunes int) (string, error) {
	if !utf8.ValidString(s) {
		return "", invalidf("the %s is not valid UTF-8 text", field)
	}
	s = strings.TrimSpace(s)
	for _, r := range s {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return "", invalidf("the %s contains a control character", field)
		}
		if isBidiControl(r) {
			return "", invalidf("the %s contains a bidirectional control character (U+%04X), which can make text "+
				"display differently from what it says", field, r)
		}
	}
	if n := len([]rune(s)); n > maxRunes {
		return "", invalidf("the %s is longer than %d characters", field, maxRunes)
	}
	return s, nil
}
