package cli_test

import (
	"os"
	"regexp"
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
	// The page is roff: asked for JSON, it says so in the JSON envelope.
	r = run(t, t.TempDir(), "man", "--json")
	if r.code != cli.ExitUsage || !strings.Contains(r.stdout, `"code": "usage"`) || strings.Contains(r.stdout, ".TH ") {
		t.Errorf("study man --json: exit %d, stdout %.200q, stderr %q", r.code, r.stdout, r.stderr)
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
	for _, problem := range undocumented(string(data), cli.CommandTree()) {
		t.Error(problem)
	}
}

// The check itself must catch a command hidden behind a longer one and a
// flag documented only for another command.
func TestDocsCheckCatchesGaps(t *testing.T) {
	data, err := os.ReadFile("../../docs/cli.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(data)
	for _, tc := range []struct {
		name string
		edit func(string) string
		want string
	}{
		{"study check, left only inside study checkpoint", func(doc string) string {
			return regexp.MustCompile("study check([ `])").ReplaceAllString(doc, "study cheque$1")
		}, `never mentions "study check"`},
		{"--dry-run left out of the topic remove row", func(doc string) string {
			return strings.Replace(doc, "| `study topic remove <topic> [--dry-run]` |", "| `study topic remove <topic>` |", 1)
		}, "study topic remove's flag --dry-run"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			edited := tc.edit(doc)
			if edited == doc {
				t.Fatal("the edit changed nothing: docs/cli.md moved on, so update this test")
			}
			if problems := undocumented(edited, cli.CommandTree()); !strings.Contains(strings.Join(problems, "\n"), tc.want) {
				t.Errorf("the check missed it; it reported %q", problems)
			}
		})
	}
}

// undocumented lists what the command tree has and the doc does not
// describe. A command must appear followed by a space or a backtick, so
// study checkpoint does not count for study check. Each flag must appear,
// as a whole word, where its own command is described: a table row whose
// first cell names the command, or a section whose heading does. The root's
// flags are global, documented once for every command.
func undocumented(doc string, root *cobra.Command) []string {
	var problems []string
	lines := strings.Split(doc, "\n")
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Hidden || c.Name() == "help" {
			return
		}
		path := c.CommandPath()
		named := regexp.MustCompile(regexp.QuoteMeta(path) + "[ `]")
		own := doc
		if c.HasParent() {
			if !named.MatchString(doc) {
				problems = append(problems, "docs/cli.md never mentions \""+path+"\"")
			}
			own = ownText(lines, named)
		}
		visit := func(f *pflag.Flag) {
			if f.Hidden || f.Name == "help" {
				return
			}
			// The name must end there: --topic is not documented by --topic-id.
			if !regexp.MustCompile(`--` + regexp.QuoteMeta(f.Name) + `(?:[^\w-]|$)`).MatchString(own) {
				problems = append(problems, "docs/cli.md does not describe "+path+"'s flag --"+f.Name+
					" in its row of the table or a section named after it")
			}
		}
		c.LocalNonPersistentFlags().VisitAll(visit)
		c.PersistentFlags().VisitAll(visit)
		for _, s := range c.Commands() {
			walk(s)
		}
	}
	walk(root)
	return problems
}

// ownText is the text describing one command: the table rows whose first
// cell names it, and the sections whose heading does.
func ownText(lines []string, named *regexp.Regexp) string {
	var b strings.Builder
	inSection := false
	for _, line := range lines {
		if strings.HasPrefix(line, "#") {
			inSection = named.MatchString(line + " ")
		}
		if inSection || strings.HasPrefix(line, "|") && named.MatchString(firstCell(line)+" ") {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// firstCell is a table row's first cell; `\|` inside it is not a border.
func firstCell(row string) string {
	row = strings.TrimPrefix(row, "|")
	for i := 0; i < len(row); i++ {
		if row[i] == '\\' {
			i++
			continue
		}
		if row[i] == '|' {
			return row[:i]
		}
	}
	return row
}
