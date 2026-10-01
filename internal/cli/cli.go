// Package cli is the study command line: a thin adapter over the core.
//
// It follows the conventions in docs/cli.md: human output by default, a stable
// JSON envelope with --json (JSON on stdout only, diagnostics on stderr), and
// documented exit codes.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"runtime/debug"
	"strings"

	"github.com/charmbracelet/fang"
	"github.com/spf13/cobra"

	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/internal/mcpserver"
)

// Version is the study version. Release builds set it with -ldflags.
var Version = ""

// Exit codes. See docs/cli.md.
const (
	ExitOK    = 0
	ExitError = 1
	ExitUsage = 2
)

// Run executes the study command line with args (without the program name)
// and returns the process exit code. stdin is only read by study mcp.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, opts core.Options) int {
	a := &app{opts: opts, stdin: stdin, stdout: stdout, stderr: stderr}
	root := a.rootCommand()
	// Decide the output mode before parsing, so errors in earlier flags are
	// still reported as JSON when --json appears later on the command line.
	// This must follow rootCommand: registering the flag resets a.json.
	a.json = wantsJSON(args)
	if args == nil {
		args = []string{} // cobra reads os.Args when given nil
	}
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	err := fang.Execute(ctx, root,
		fang.WithVersion(version()),
		fang.WithErrorHandler(a.handleError),
	)
	switch {
	case err == nil:
		return ExitOK
	case isUsage(err):
		return ExitUsage
	default:
		return ExitError
	}
}

type app struct {
	opts   core.Options
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	json   bool
}

func (a *app) rootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "study",
		Short: "Lamplight: see where you are in your studies, and what to do next",
		Long: "Lamplight keeps your study Topics, Syllabus, Cards and History in plain files in your Study home.\n" +
			"Run study on its own to see where you are.",
		Args: noArgs,
		RunE: a.runStatus,
	}
	root.PersistentFlags().BoolVar(&a.json, "json", false, "print a JSON envelope on stdout (see docs/cli.md)")
	// Once parsing succeeds, the parsed flag decides the output mode: the
	// up-front scan in Run could mistake a flag value such as --title --json
	// for the flag.
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if f := cmd.Flags().Lookup("json"); f == nil || !f.Changed {
			a.json = false
		}
		return nil
	}
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError{err} })

	status := &cobra.Command{
		Use:   "status",
		Short: "Show where you are: the Active topic and every Topic",
		Args:  noArgs,
		RunE:  a.runStatus,
	}

	topic := &cobra.Command{
		Use:   "topic",
		Short: "Create and inspect Topics",
		Args:  noArgs,
		RunE:  showHelp,
	}
	var spec core.TopicSpec
	create := &cobra.Command{
		Use:   "create",
		Short: "Create a Topic in the Study home",
		Example: `  study topic create --title "Linear algebra"
  study topic create --title "C" --id c --goal "Write and debug small C programs"`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := core.Open(a.opts)
			if err != nil {
				return a.fail(err)
			}
			created, err := c.CreateTopic(cmd.Context(), spec)
			if err != nil {
				return a.fail(err)
			}
			if a.json {
				return a.writeJSON(envelope{OK: true, Data: created})
			}
			verb := "Created"
			if spec.DryRun {
				verb = "Would create"
			}
			_, err = fmt.Fprintf(a.stdout, "%s Topic %s (%s) in %s\n", verb, created.ID, created.Title, created.Path)
			return err
		},
	}
	create.Flags().StringVar(&spec.Title, "title", "", "what you are studying, for example \"Linear algebra\" (required)")
	create.Flags().StringVar(&spec.ID, "id", "", "folder name; derived from the title when omitted")
	create.Flags().StringVar(&spec.Goal, "goal", "", "what you want to be able to do at the end")
	create.Flags().BoolVar(&spec.DryRun, "dry-run", false, "show what would be created without writing anything")
	topic.AddCommand(create)

	serve := &cobra.Command{
		Use:   "mcp",
		Short: "Run the MCP server for agents over stdin and stdout",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := core.Open(a.opts)
			if err != nil {
				return a.fail(err)
			}
			return mcpserver.Serve(cmd.Context(), c, version(), a.stdin, a.stdout)
		},
	}

	root.AddCommand(status, topic, a.libraryCommand(), serve)
	return root
}

func (a *app) libraryCommand() *cobra.Command {
	lib := &cobra.Command{
		Use:   "library",
		Short: "Index and search your Library of books",
		Args:  noArgs,
		RunE:  showHelp,
	}
	build := &cobra.Command{
		Use:     "build <folder>",
		Short:   "Index the books in a folder, replacing the previous index",
		Example: "  study library build ~/Books",
		Args:    exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := core.Open(a.opts)
			if err != nil {
				return a.fail(err)
			}
			summary, err := c.BuildLibrary(cmd.Context(), args[0])
			if err != nil {
				return a.fail(err)
			}
			if a.json {
				return a.writeJSON(envelope{OK: true, Data: summary})
			}
			return writeLibrarySummary(a.stdout, summary)
		},
	}
	var limit int
	search := &cobra.Command{
		Use:   "search <query>",
		Short: "Find books in your Library",
		Example: `  study library search "linear algebra"
  study library search C --limit 5`,
		Args: minArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := core.Open(a.opts)
			if err != nil {
				return a.fail(err)
			}
			results, err := c.SearchLibrary(cmd.Context(), strings.Join(args, " "), limit)
			if err != nil {
				return a.fail(err)
			}
			if a.json {
				return a.writeJSON(envelope{OK: true, Data: map[string]any{"results": results}})
			}
			return writeSearchResults(a.stdout, results)
		},
	}
	search.Flags().IntVar(&limit, "limit", core.DefaultSearchLimit,
		fmt.Sprintf("most results to show, up to %d", core.MaxSearchLimit))
	lib.AddCommand(build, search)
	return lib
}

func (a *app) runStatus(cmd *cobra.Command, _ []string) error {
	c, err := core.Open(a.opts)
	if err != nil {
		return a.fail(err)
	}
	status, err := c.Status(cmd.Context())
	if err != nil {
		return a.fail(err)
	}
	if a.json {
		return a.writeJSON(envelope{OK: true, Data: status})
	}
	return writeStatus(a.stdout, status)
}

// envelope is the JSON shape of every --json result. See docs/cli.md.
type envelope struct {
	OK    bool       `json:"ok"`
	Data  any        `json:"data,omitempty"`
	Error *errorBody `json:"error,omitempty"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (a *app) writeJSON(v envelope) error {
	enc := json.NewEncoder(a.stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// fail reports err. With --json the error envelope goes to stdout and the
// returned error is marked as already reported, so fang prints nothing more.
func (a *app) fail(err error) error {
	if !a.json {
		if core.CodeOf(err) == core.CodeInvalidArgument {
			return usageError{err}
		}
		return err
	}
	if werr := a.writeJSON(envelope{Error: &errorBody{Code: string(core.CodeOf(err)), Message: err.Error()}}); werr != nil {
		return werr
	}
	if core.CodeOf(err) == core.CodeInvalidArgument {
		return reported{usageError{err}}
	}
	return reported{err}
}

// handleError prints errors that were not already reported as JSON.
func (a *app) handleError(w io.Writer, styles fang.Styles, err error) {
	var r reported
	if errors.As(err, &r) {
		return
	}
	if a.json {
		code := string(core.CodeInternal)
		if isUsage(err) {
			code = "usage"
		}
		_ = a.writeJSON(envelope{Error: &errorBody{Code: code, Message: err.Error()}})
		return
	}
	fang.DefaultErrorHandler(w, styles, err)
}

// reported marks an error whose JSON envelope was already written.
type reported struct{ err error }

func (r reported) Error() string { return r.err.Error() }
func (r reported) Unwrap() error { return r.err }

// usageError marks invalid arguments or flags, which exit with ExitUsage.
type usageError struct{ err error }

func (u usageError) Error() string { return u.err.Error() }
func (u usageError) Unwrap() error { return u.err }

func isUsage(err error) bool {
	var u usageError
	return errors.As(err, &u)
}

// showHelp prints help for command groups such as study topic. Unknown
// subcommands arrive as arguments and are rejected by noArgs first.
func showHelp(cmd *cobra.Command, _ []string) error { return cmd.Help() }

func noArgs(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return usageError{fmt.Errorf("unexpected argument %q for %q", args[0], cmd.CommandPath())}
	}
	return nil
}

func exactArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != n {
			return usageError{fmt.Errorf("%q takes %d argument(s), got %d", cmd.CommandPath(), n, len(args))}
		}
		return nil
	}
}

func minArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) < n {
			return usageError{fmt.Errorf("%q needs at least %d argument(s)", cmd.CommandPath(), n)}
		}
		return nil
	}
}

func wantsJSON(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if arg == "--json" || arg == "--json=true" {
			return true
		}
	}
	return false
}

var (
	releaseVersion = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)
	pseudoVersion  = regexp.MustCompile(`\d{14}-[0-9a-f]{12}$`)
)

// version returns the release version set at build time, or the module
// version for a tagged go install (release candidates included), and "dev"
// for anything else. Pseudo-versions are not shown: they derive from the v1
// skill's tags and would mislead.
func version() string {
	if Version != "" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		v := info.Main.Version
		if releaseVersion.MatchString(v) && !pseudoVersion.MatchString(v) {
			return v
		}
	}
	return "dev"
}
