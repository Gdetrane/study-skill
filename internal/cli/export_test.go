package cli

import (
	"strings"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/core"
)

// SystemCompletionRoots lets tests replace the share folders where packages
// install completions, so results do not depend on the machine.
var SystemCompletionRoots = &systemCompletionRoots

// SetBeforeRCReplace installs a hook that runs just before an rc file is
// replaced, and returns a function that removes it.
func SetBeforeRCReplace(hook func(path string)) func() {
	beforeRCReplace = hook
	return func() { beforeRCReplace = nil }
}

// RenderLessonDetail renders a Lesson as study lesson does.
func RenderLessonDetail(d core.LessonDetail) string {
	var b strings.Builder
	_ = writeLessonDetail(&b, d)
	return b.String()
}

// RenderHistory renders a History view as study history does.
func RenderHistory(v core.HistoryView) string {
	var b strings.Builder
	_ = writeHistory(&b, v)
	return b.String()
}

// SetExecutable makes study see path as its own binary, as study setup
// registers it, and returns a function that restores the real one.
func SetExecutable(path string) func() {
	old := executable
	executable = func() (string, error) { return path, nil }
	return func() { executable = old }
}

// SetBeforeSkillWrite installs a hook that runs before study setup writes
// each skill file, and returns a function that removes it.
func SetBeforeSkillWrite(hook func(rel string)) func() {
	beforeSkillWrite = hook
	return func() { beforeSkillWrite = nil }
}

// SetBeforeShelfRegister installs a hook that runs once study setup has
// decided to register Shelf and before it does, and returns a function that
// removes it: where a registration of the learner's can reach the registry
// between setup's look and its write.
func SetBeforeShelfRegister(hook func()) func() {
	beforeShelfRegister = hook
	return func() { beforeShelfRegister = nil }
}

// RenderDiagnosis renders Findings as study doctor prints them for people.
// The command line's own writer drops escape sequences when it does not
// write to a terminal: this is what a terminal would be sent.
func RenderDiagnosis(d core.Diagnosis) string {
	var b strings.Builder
	_ = writeDiagnosis(&b, d)
	return b.String()
}

// RenderSetupShelf renders what study setup prints for people when Shelf's
// registration came to this, as a terminal would be sent it.
func RenderSetupShelf(status, program, note string) string {
	res := newSetupResult(false, false)
	res.Study, res.Shelf = "/usr/bin/study", &shelfResult{Name: shelfName, Status: status, Program: program, Note: note}
	var b strings.Builder
	_ = writeSetupResult(&b, res, false)
	return b.String()
}

// SetAgentTimeouts shortens how long agent commands may run, and returns a
// function that restores the real limits.
func SetAgentTimeouts(read, write time.Duration) func() {
	oldRead, oldWrite := agentReadTimeout, agentWriteTimeout
	agentReadTimeout, agentWriteTimeout = read, write
	return func() { agentReadTimeout, agentWriteTimeout = oldRead, oldWrite }
}

// WriteScores renders a criterion's scores as the check and results
// commands do.
func WriteScores(s *core.CriterionScores) string {
	var b strings.Builder
	writeScores(&b, s, "")
	return b.String()
}
