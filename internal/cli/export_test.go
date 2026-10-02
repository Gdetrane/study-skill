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
