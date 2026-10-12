// Command study-shelf is Shelf, the Knowledge base plugin that ships with
// Lamplight (ADR-0012): an MCP server over its standard input and output
// that indexes a Topic's PDFs and web pages and searches them.
//
// The learner does not run it. They register it once,
//
//	study knowledge-base add shelf -- study-shelf
//
// and study starts it when a Topic that names it needs it, in the world
// docs/knowledge-base-contract.md describes: a small environment of study's
// making, the registry's folder to work in, which Shelf never writes to, and
// the folder LAMPLIGHT_PLUGIN_DATA names to keep its index in. docs/shelf.md
// is the learner's page.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
	"github.com/mordor-forge/lamplight/v2/internal/shelf"
)

const usage = `study-shelf is Shelf, the Knowledge base plugin that ships with Lamplight.

It is started by study, not by hand. Register it once:

  study knowledge-base add shelf -- study-shelf
  study knowledge-base check shelf

A Topic that names the plugin "shelf" as its Knowledge base then has its
PDFs and web pages indexed by study source index, to be searched when the
agent looks for Evidence.

Usage:
  study-shelf            speak the Knowledge base plugin contract over
                         standard input and output, as study expects
  study-shelf --stdio    the same, said outright
  study-shelf --version  print the version
  study-shelf --help     print this

Options, given where Shelf is registered:
  --allow-loopback       also fetch web pages from this computer itself
                         (localhost, 127.0.0.1). Shelf fetches none from
                         there otherwise: whatever listens on this computer
                         trusts whoever reaches it, and the agent chooses
                         which addresses are Sources. For a learner who
                         serves the pages they study from to themselves:
                           study knowledge-base add shelf --replace -- study-shelf --allow-loopback
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	// Whatever is not an MCP message goes to standard error: standard
	// output is study's to read, and a line of anything else ends the
	// session.
	flags := flag.NewFlagSet(shelf.Name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	version := flags.Bool("version", false, "")
	// Over its standard streams is the one way Shelf speaks, so --stdio
	// changes nothing. It is taken because a registration that says how a
	// plugin is to be reached reads better, and still holds on the day Shelf
	// can be reached another way.
	_ = flags.Bool("stdio", false, "")
	// Given where Shelf is registered, which is the learner's to do and
	// outside the Study home: nothing the agent writes turns it on.
	loopback := flags.Bool("allow-loopback", false, "")
	switch err := flags.Parse(args); {
	case errors.Is(err, flag.ErrHelp):
		fmt.Fprint(stdout, usage)
		return 0
	case err != nil:
		fmt.Fprintf(stderr, "%s: %v\n\n%s", shelf.Name, err, usage)
		return 2
	case flags.NArg() > 0:
		fmt.Fprintf(stderr, "%s: it takes no arguments, and was given %q\n\n%s", shelf.Name, flags.Arg(0), usage)
		return 2
	case *version:
		fmt.Fprintf(stdout, "%s version %s\n", shelf.Name, shelf.VersionString())
		return 0
	}

	folder := os.Getenv(plugincontract.DataFolderEnv)
	if folder == "" {
		fmt.Fprintf(stderr, "%s: %s is not set, so there is no folder to keep the index in. study sets it when it starts a "+
			"registered plugin: register Shelf with\n  study knowledge-base add shelf -- study-shelf\n", shelf.Name, plugincontract.DataFolderEnv)
		return 1
	}
	settings := shelf.DefaultSettings()
	settings.Fetch.Loopback = *loopback
	plugin := shelf.New(shelf.Options{Folder: folder, Settings: settings})

	// study asks a plugin to exit by closing its standard input: Run
	// returns then. A signal ends it too.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	err := plugin.Server().Run(ctx, &mcp.StdioTransport{})
	stop()
	// Whatever was being indexed is stopped, and nothing is waited for
	// longer than study waits for a plugin, which after a glance is a
	// quarter of a second: the index is whole wherever the work was
	// (docs/shelf.md), so the program may end under it.
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		plugin.Close()
	}()
	select {
	case <-closed:
	case <-time.After(shelf.StopsWithin + 50*time.Millisecond):
	}
	if err != nil && ctx.Err() == nil && !errors.Is(err, io.EOF) {
		// Last on standard error, where study looks for the reason.
		fmt.Fprintf(stderr, "%s: the session ended: %v\n", shelf.Name, err)
	}
	return 0
}
