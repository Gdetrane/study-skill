package core

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Finding a plugin reads the registry and looks at every registered
// program, on whatever drive each is. A drive that does not answer holds
// that look for as long as it likes: the command does not wait with it. It
// has the time a plugin has to start, and it ends when the command is
// stopped.
func TestFindingAPluginDoesNotOutlastTheCommand(t *testing.T) {
	open := func(start time.Duration) *Core {
		home := t.TempDir()
		c, err := Open(Options{
			Getenv: func(key string) string {
				return map[string]string{"STUDY_HOME": filepath.Join(home, "study"), "HOME": home}[key]
			},
			Dir:     home,
			Plugins: PluginOptions{StartTimeout: start},
		})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	// The look is held until the test is over.
	release := make(chan struct{})
	hold := func() { <-release }
	beforePluginLookup.Store(&hold)
	t.Cleanup(func() {
		close(release)
		beforePluginLookup.Store(nil)
	})

	began := time.Now()
	_, err := open(200*time.Millisecond).CheckPlugin(context.Background(), "kb")
	if CodeOf(err) != CodeFailedPrecondition ||
		!strings.Contains(err.Error(), "study could not read the Knowledge base plugin registry within 200ms") {
		t.Errorf("CheckPlugin = %v; want failed_precondition, with how long study waited", err)
	}
	if took := time.Since(began); took > 5*time.Second {
		t.Errorf("CheckPlugin took %s over a registry that does not answer, with 200ms to start a plugin", took)
	}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	began = time.Now()
	_, err = open(time.Minute).CheckPlugin(ctx, "kb")
	if CodeOf(err) != CodeCanceled {
		t.Errorf("CheckPlugin, stopped = %v; want canceled", err)
	}
	if took := time.Since(began); took > 5*time.Second {
		t.Errorf("CheckPlugin took %s to end once the command was stopped", took)
	}
}
