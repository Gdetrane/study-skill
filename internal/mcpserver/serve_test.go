package mcpserver

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/core"
)

// startedWriter reports its first Write, then passes it on.
type startedWriter struct {
	*io.PipeWriter
	once    sync.Once
	started chan struct{}
}

func (w *startedWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	return w.PipeWriter.Write(p)
}

// A client that stops reading leaves the response write blocked; cancelling
// must still make Serve return.
func TestServeReturnsWhenCancelledWithAWriteBlocked(t *testing.T) {
	defer func(d time.Duration) { shutdownGrace = d }(shutdownGrace)
	shutdownGrace = 100 * time.Millisecond

	home := t.TempDir()
	c, err := core.Open(core.Options{
		Getenv: func(key string) string { return map[string]string{"STUDY_HOME": home, "HOME": home}[key] },
		Dir:    home,
	})
	if err != nil {
		t.Fatal(err)
	}
	inR, inW := io.Pipe()
	_, outW := io.Pipe() // never read
	out := &startedWriter{PipeWriter: outW, started: make(chan struct{})}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, c, "test", inR, out, slog.New(slog.DiscardHandler)) }()

	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18",` +
		`"capabilities":{},"clientInfo":{"name":"test","version":"1"}}}` + "\n"
	go func() { _, _ = io.Copy(inW, strings.NewReader(initialize)) }()

	select {
	case <-out.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the server never started writing its response")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after cancellation while a write was blocked")
	}
}
