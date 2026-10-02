package cli

import (
	"strings"

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

// WriteScores renders a criterion's scores as the check and results
// commands do.
func WriteScores(s *core.CriterionScores) string {
	var b strings.Builder
	writeScores(&b, s, "")
	return b.String()
}
