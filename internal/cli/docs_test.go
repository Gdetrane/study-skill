package cli_test

import (
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/mordor-forge/lamplight/v2/internal/cli"
)

// TestManWritesToItsOutput: study man prints the man page to the writer it
// is given, so packaging scripts and tests capture it, rather than straight
// to the process's stdout as fang's own man command did.
func TestManWritesToItsOutput(t *testing.T) {
	r := run(t, t.TempDir(), "man")
	if r.code != cli.ExitOK || !strings.Contains(r.stdout, ".TH ") || !strings.Contains(r.stdout, "study") {
		t.Errorf("study man: exit %d, stdout %.200q, stderr %q", r.code, r.stdout, r.stderr)
	}
}

// TestDocsDescribeEveryCommand keeps docs/cli.md, the contract scripts and
// agents rely on, in step with the command tree: every visible command and
// every visible flag of it must be documented.
func TestDocsDescribeEveryCommand(t *testing.T) {
	data, err := os.ReadFile("../../docs/cli.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(data)
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Hidden || c.Name() == "help" {
			return
		}
		path := c.CommandPath()
		if c.HasParent() && !strings.Contains(doc, path) {
			t.Errorf("docs/cli.md never mentions %q", path)
		}
		visit := func(f *pflag.Flag) {
			if !f.Hidden && f.Name != "help" && !strings.Contains(doc, "--"+f.Name) {
				t.Errorf("docs/cli.md never mentions %s's flag --%s", path, f.Name)
			}
		}
		c.LocalNonPersistentFlags().VisitAll(visit)
		c.PersistentFlags().VisitAll(visit)
		for _, s := range c.Commands() {
			walk(s)
		}
	}
	walk(cli.CommandTree())
}
