package cli

// SystemCompletionRoots lets tests replace the share folders where packages
// install completions, so results do not depend on the machine.
var SystemCompletionRoots = &systemCompletionRoots

// SetBeforeRCReplace installs a hook that runs just before an rc file is
// replaced, and returns a function that removes it.
func SetBeforeRCReplace(hook func(path string)) func() {
	beforeRCReplace = hook
	return func() { beforeRCReplace = nil }
}
