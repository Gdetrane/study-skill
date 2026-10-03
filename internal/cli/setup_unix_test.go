//go:build unix

package cli_test

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/cli"
)

func skipAsRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root writes to read-only folders")
	}
}

func chmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestSetupChangesNothingWhenItCannotRecord(t *testing.T) {
	skipAsRoot(t)
	h := newSetupHome(t, "claude", "codex")
	state := h.path(".local", "state", "lamplight")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(state, 0o755) })
	refused := func(when string) {
		t.Helper()
		chmod(t, state, 0o555)
		before, calls := tree256(t, h.home), len(h.calls(t))
		r := h.run(t, "setup", "--json")
		code, _ := failure(t, r)
		if after := tree256(t, h.home); code != "internal" || !maps.Equal(after, before) {
			t.Errorf("%s: setup with a read-only state folder: %s, left %v", when, code, leftovers(before, after))
		}
		if !strings.Contains(r.stdout, "cannot write there") || !strings.Contains(r.stdout, "it changed nothing") {
			t.Errorf("%s: the error does not say setup changed nothing:\n%s", when, r.stdout)
		}
		if len(h.calls(t)) != calls {
			t.Errorf("%s: setup ran agent commands it could not record: %v", when, h.calls(t)[calls:])
		}
		chmod(t, state, 0o755)
	}

	refused("before any setup")
	before := tree256(t, h.home)
	decodeData(t, h.run(t, "setup", "--json"))
	decodeData(t, h.run(t, "setup", "--remove", "--json"))
	// Again with the lock file an earlier setup left, which opens fine.
	refused("after an earlier setup")
	decodeData(t, h.run(t, "setup", "--json"))
	decodeData(t, h.run(t, "setup", "--remove", "--json"))
	if left := leftovers(before, tree256(t, h.home)); left != nil {
		t.Errorf("--remove left %v", left)
	}
}

func TestSetupRecordsEachStepSoRemoveUndoesAPartialRun(t *testing.T) {
	skipAsRoot(t)
	// Write number failAt cannot be written, as on a full disk: the first
	// (after setup created the folders) or a later one.
	for _, failAt := range []int{1, 3} {
		t.Run(fmt.Sprintf("write %d fails", failAt), func(t *testing.T) {
			h := newSetupHome(t, "codex")
			before := tree256(t, h.home)
			failing, n := true, 0
			readOnly := func(mode os.FileMode) {
				for _, dir := range []string{h.skill(), h.skill("references")} {
					if exists(dir) {
						chmod(t, dir, mode)
					}
				}
			}
			t.Cleanup(cli.SetBeforeSkillWrite(func(string) {
				if n++; failing && n == failAt {
					readOnly(0o555)
				}
			}))
			t.Cleanup(func() {
				for _, dir := range []string{h.skill(), h.skill("references")} {
					_ = os.Chmod(dir, 0o755)
				}
			})

			code, data := failure(t, h.run(t, "setup", "--json"))
			written := stringList(data["skill"].(map[string]any)["written"])
			if code != "internal" || len(written) != failAt-1 || len(data["agents"].([]any)) != 0 {
				t.Fatalf("setup that failed part-way: %s, %v", code, data)
			}
			readOnly(0o755)
			files := h.record(t)["skill"].(map[string]any)["files"].(map[string]any)
			if got := slices.Sorted(maps.Keys(files)); !slices.Equal(got, written) {
				t.Errorf("the record holds %v, but setup wrote %v", got, written)
			}
			decodeData(t, h.run(t, "setup", "--remove", "--json"))
			if left := leftovers(before, tree256(t, h.home)); left != nil {
				t.Errorf("--remove after a partial setup left %v", left)
			}

			// And setup recovers from a partial run.
			n = 0
			failure(t, h.run(t, "setup", "--json"))
			readOnly(0o755)
			failing = false
			got := decodeData(t, h.run(t, "setup", "--json"))
			if s := got["skill"].(map[string]any); s["status"] != "updated" || len(s["kept"].([]any)) != 0 {
				t.Errorf("setup after a partial run: %v", s)
			}
			if r := h.run(t, "setup", "--check", "--json"); r.code != cli.ExitOK {
				t.Errorf("--check after recovering: %s", r.stdout)
			}
			decodeData(t, h.run(t, "setup", "--remove", "--json"))
			if left := leftovers(before, tree256(t, h.home)); left != nil {
				t.Errorf("--remove after recovering left %v", left)
			}
		})
	}
}

func TestAgentCommandsAreStoppedInTime(t *testing.T) {
	h := newSetupHome(t, "codex")
	decodeData(t, h.run(t, "setup", "--json"))
	t.Cleanup(cli.SetAgentTimeouts(300*time.Millisecond, 300*time.Millisecond))
	// From now on codex starts a child that sleeps, and waits for it.
	writeFile(t, h.path("codex-hang"), "")
	for _, args := range [][]string{{"setup", "--check", "--json"}, {"doctor", "--json"}} {
		start := time.Now()
		r := h.run(t, args...)
		if took := time.Since(start); took > 1500*time.Millisecond {
			t.Errorf("%v took %v with a codex that hangs", args, took)
		}
		if !strings.Contains(r.stdout, "did not finish within 300ms") {
			t.Errorf("%v does not say codex hung:\n%s", args, r.stdout)
		}
		data, err := os.ReadFile(h.path("sleeper.pid"))
		if err != nil {
			t.Fatal(err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			t.Fatal(err)
		}
		if !gone(pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Errorf("%v left codex's child %d running", args, pid)
		}
		_ = os.Remove(h.path("sleeper.pid"))
	}
}

// gone reports whether pid has exited, waiting briefly: a killed process
// lingers until its new parent reaps it.
func gone(pid int) bool {
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		if stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
			if i := strings.LastIndexByte(string(stat), ')'); i > 0 && strings.HasPrefix(string(stat[i+1:]), " Z") {
				return true
			}
		}
	}
	return false
}
