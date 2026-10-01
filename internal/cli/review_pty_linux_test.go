//go:build linux

package cli_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/x/term"

	"github.com/mordor-forge/lamplight/v2/internal/cli"
)

// TestReviewReadsSingleKeysFromATerminal runs study review with a terminal
// on stdin: keys arrive one at a time without Enter, Enter is a carriage
// return, and Ctrl-C stops the session.
func TestReviewReadsSingleKeysFromATerminal(t *testing.T) {
	master, slave := openPTY(t)
	// The terminal starts raw, so every key is delivered at once whatever
	// mode study review is in when it is typed.
	state, err := term.MakeRaw(slave.Fd())
	if err != nil {
		t.Skipf("cannot make the pseudo-terminal raw: %v", err)
	}
	t.Cleanup(func() { _ = term.Restore(slave.Fd(), state) })

	home := withTopic(t)
	addCard(t, home, "What does a pivot column hold?", "A leading 1")
	addCard(t, home, "What is a free variable?", "One whose column has no pivot")
	addCard(t, home, "Who invented matrices?", "Many people")
	// Card 1: Enter, keep, good. Card 2: space, drop. Card 3: Ctrl-C.
	if _, err := master.Write([]byte("\rk3 d\x03")); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := cli.Run(context.Background(), []string{"review", "linear-algebra"}, slave, &stdout, &stderr, options(home, home))
	if code != cli.ExitOK {
		t.Fatalf("exit %d, stderr %s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"Good. Next in", "Dropped.", "Stopped. 1 Review recorded, 1 new Card dropped."} {
		if !strings.Contains(out, want) {
			t.Errorf("the session lacks %q:\n%s", want, out)
		}
	}
	if cards := listCards(t, home); len(cards) != 2 || cards[0].Draft || !cards[1].Draft {
		t.Errorf("after the session: %+v, want the first Card reviewed and the third still a draft", cards)
	}
}
