// Command study is Lamplight's command line and MCP server.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/mordor-forge/lamplight/v2/internal/cli"
	"github.com/mordor-forge/lamplight/v2/internal/core"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	code := cli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, core.Options{})
	stop()
	os.Exit(code)
}
