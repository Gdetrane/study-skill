package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"charm.land/log/v2"
	"github.com/charmbracelet/colorprofile"
)

const logFileName = "study.log"

// logs is the Log for one run of study: application diagnostics, never the
// learner's activity. Records go to stderr through charmbracelet/log and, as
// JSON lines, to study.log in the state folder.
type logs struct {
	logger *slog.Logger
	file   *lazyFile
	// level applies to the file; stderr shows warnings and errors unless a
	// level was chosen explicitly.
	level slog.Level
	// badEnv is STUDY_LOG when it does not name a level.
	badEnv string
}

// newLogs configures the Log. flagLevel is --log-level, if given; STUDY_LOG
// is the fallback. In MCP mode stderr only shows warnings and errors, and
// nothing is ever written to stdout.
func newLogs(getenv func(string) string, stderr io.Writer, flagLevel string, flagSet, mcpMode bool) (*logs, error) {
	l := &logs{level: slog.LevelInfo}
	explicit := false
	switch {
	case flagSet:
		if err := l.level.UnmarshalText([]byte(flagLevel)); err != nil {
			return nil, usageError{fmt.Errorf("--log-level must be debug, info, warn or error, not %q", flagLevel)}
		}
		explicit = true
	case getenv("STUDY_LOG") != "":
		if err := l.level.UnmarshalText([]byte(getenv("STUDY_LOG"))); err != nil {
			l.level, l.badEnv = slog.LevelInfo, getenv("STUDY_LOG")
		} else {
			explicit = true
		}
	}
	stderrLevel := slog.LevelWarn
	if explicit {
		stderrLevel = l.level
	}
	if mcpMode && stderrLevel < slog.LevelWarn {
		stderrLevel = slog.LevelWarn
	}

	terminal := log.NewWithOptions(stderr, log.Options{Level: log.Level(stderrLevel), Prefix: "study"})
	terminal.SetColorProfile(colorprofile.Detect(stderr, environ(getenv)))
	handlers := fanout{terminal}
	if dir, err := stateDir(getenv); err == nil {
		l.file = &lazyFile{path: filepath.Join(dir, logFileName)}
		handlers = append(handlers, slog.NewJSONHandler(l.file, &slog.HandlerOptions{Level: l.level}))
	}
	l.logger = slog.New(handlers)
	if l.badEnv != "" {
		l.logger.Warn("STUDY_LOG is not a level; using info", "STUDY_LOG", l.badEnv)
	}
	return l, nil
}

// path returns the log file, or "" when no state folder could be found.
func (l *logs) path() string {
	if l == nil || l.file == nil {
		return ""
	}
	return l.file.path
}

// probe opens the log file, so study doctor can report whether logging works.
func (l *logs) probe() error {
	if l == nil || l.file == nil {
		return errors.New("no state folder: set HOME or XDG_STATE_HOME")
	}
	return l.file.open()
}

func (l *logs) close() {
	if l != nil && l.file != nil {
		l.file.close()
	}
}

// stateDir is Lamplight's state folder: $XDG_STATE_HOME/lamplight, or
// ~/.local/state/lamplight. It holds the Log and the completion record.
func stateDir(getenv func(string) string) (string, error) {
	if dir := getenv("XDG_STATE_HOME"); filepath.IsAbs(dir) {
		return filepath.Join(dir, "lamplight"), nil
	}
	home := getenv("HOME")
	if home == "" {
		var err error
		if home, err = os.UserHomeDir(); err != nil {
			return "", err
		}
	}
	return filepath.Join(home, ".local", "state", "lamplight"), nil
}

// environ returns the variables that decide colours, read through getenv so
// tests control them.
func environ(getenv func(string) string) []string {
	var env []string
	for _, key := range []string{"TERM", "COLORTERM", "NO_COLOR", "CLICOLOR", "CLICOLOR_FORCE", "TTY_FORCE"} {
		if v := getenv(key); v != "" {
			env = append(env, key+"="+v)
		}
	}
	return env
}

// lazyFile creates the log file on the first record, so runs that log
// nothing leave nothing behind. A file that cannot be opened drops records:
// logging must never break a command.
type lazyFile struct {
	path string
	mu   sync.Mutex
	f    *os.File
	err  error
}

func (l *lazyFile) Write(p []byte) (int, error) {
	if err := l.open(); err != nil {
		return len(p), nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Write(p)
}

func (l *lazyFile) open() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil || l.err != nil {
		return l.err
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		l.err = err
		return err
	}
	l.f, l.err = os.OpenFile(l.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	return l.err
}

func (l *lazyFile) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		_ = l.f.Close()
		l.f = nil
	}
}

// fanout sends each record to every handler that wants it.
type fanout []slog.Handler

func (f fanout) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range f {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (f fanout) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, h := range f {
		if h.Enabled(ctx, r.Level) {
			errs = append(errs, h.Handle(ctx, r.Clone()))
		}
	}
	return errors.Join(errs...)
}

func (f fanout) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make(fanout, len(f))
	for i, h := range f {
		out[i] = h.WithAttrs(attrs)
	}
	return out
}

func (f fanout) WithGroup(name string) slog.Handler {
	out := make(fanout, len(f))
	for i, h := range f {
		out[i] = h.WithGroup(name)
	}
	return out
}

// levelName is the lower-case name of a level, as --log-level accepts it.
func levelName(level slog.Level) string { return strings.ToLower(level.String()) }
