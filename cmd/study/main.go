// Command study is Lamplight's command line and MCP server.
package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/mordor-forge/lamplight/v2/internal/cli"
	"github.com/mordor-forge/lamplight/v2/internal/core"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr, core.Options{})
	stop()
	os.Exit(code)
}
