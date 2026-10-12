package checkpoint_test

import (
	"slices"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/checkpoint"
)

// git takes a relative XDG_CONFIG_HOME or HOME as it is, from the folder it
// runs in, where study ignores one. git is not given it, so both find the
// learner's ignore file in the same place, the default under the home
// folder, and neither takes a file in the Topic, where the agent writes, for
// it: such a file hides nothing from a Checkpoint, nor from the snapshot of
// the work an Attempt is recorded on.
func TestGitIsNotGivenARelativeFolder(t *testing.T) {
	for _, tc := range []struct {
		name, variable, planted string
	}{
		{"XDG_CONFIG_HOME", "XDG_CONFIG_HOME", "rel/git/ignore"},
		{"HOME", "HOME", "rel/.config/git/ignore"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", "")
			t.Setenv(tc.variable, "rel")
			// The learner's own ignore file, where the default is.
			write(t, home, ".config/git/ignore", "*.tmp\n")

			dir := newRepo(t)
			write(t, dir, tc.planted, "answer.txt\n")
			write(t, dir, practice+"/answer.txt", "41\n")
			write(t, dir, practice+"/scratch.tmp", "x\n")
			before := work(t, dir)
			write(t, dir, practice+"/answer.txt", "42\n")
			if w := work(t, dir); w.Hash == before.Hash {
				t.Error("the snapshot of the work does not see a file the planted ignore file names")
			}

			take(t, dir, checkpoint.Learner, "")
			got := paths(files(t, dir, "HEAD"))
			want := []string{practice + "/answer.txt", tc.planted}
			if tc.variable == "HOME" {
				// A relative HOME is no home folder, so there is no
				// ignore file of the learner's to find.
				want = append(want, practice+"/scratch.tmp")
			}
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("the Checkpoint holds %v, want %v", got, want)
			}
		})
	}
}
