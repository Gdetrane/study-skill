// Package core owns Lamplight's study state and rules.
//
// The CLI and the MCP server are thin adapters over this package: every domain
// operation lives here, and tests exercise it in process with a fixed clock and
// a temporary Study home. Terms follow CONTEXT.md.
package core

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// FormatVersion is the format number written to every file the core owns.
// A binary refuses files with a newer format than it understands.
const FormatVersion = 1

// Options configures a Core. Zero values select the production defaults.
type Options struct {
	// Getenv reads environment variables such as STUDY_HOME, XDG_CONFIG_HOME
	// and HOME. Defaults to os.Getenv.
	Getenv func(string) string
	// Dir is the folder the learner or agent started in. It decides the
	// Active topic. Defaults to the process working directory.
	Dir string
	// Now returns the current time. Defaults to time.Now.
	Now func() time.Time
	// NewID returns a new unique Event ID. Defaults to a random ID.
	NewID func() string
}

// Core is the Lamplight core for one Study home.
type Core struct {
	home  string
	dir   string
	now   func() time.Time
	newID func() string
}

// Open resolves the Study home and returns a Core for it. The Study home is
// created on the first write, not by Open.
func Open(opts Options) (*Core, error) {
	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	home, err := resolveHome(getenv)
	if err != nil {
		return nil, err
	}
	dir := opts.Dir
	if dir == "" {
		if dir, err = os.Getwd(); err != nil {
			return nil, internalError("reading the working directory", err)
		}
	}
	if dir, err = filepath.Abs(dir); err != nil {
		return nil, internalError("resolving the working directory", err)
	}
	c := &Core{home: home, dir: dir, now: opts.Now, newID: opts.NewID}
	if c.now == nil {
		c.now = time.Now
	}
	if c.newID == nil {
		c.newID = randomID
	}
	return c, nil
}

// Home returns the absolute path of the Study home.
func (c *Core) Home() string { return c.home }

// config is the global configuration file, config.toml.
type config struct {
	Format    int    `toml:"format"`
	StudyHome string `toml:"study_home"`
}

// resolveHome picks the Study home: STUDY_HOME, then config.toml, then ~/study.
func resolveHome(getenv func(string) string) (string, error) {
	userHome := getenv("HOME")
	if userHome == "" {
		var err error
		if userHome, err = os.UserHomeDir(); err != nil {
			return "", internalError("finding your home folder", err)
		}
	}
	home := getenv("STUDY_HOME")
	if home == "" {
		cfg, err := loadConfig(configPath(getenv, userHome))
		if err != nil {
			return "", err
		}
		home = cfg.StudyHome
	}
	if home == "" {
		home = filepath.Join(userHome, "study")
	}
	if home == "~" {
		home = userHome
	} else if rest, ok := strings.CutPrefix(home, "~/"); ok {
		home = filepath.Join(userHome, rest)
	}
	abs, err := filepath.Abs(home)
	if err != nil {
		return "", internalError("resolving the Study home", err)
	}
	return abs, nil
}

func configPath(getenv func(string) string, userHome string) string {
	base := getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(userHome, ".config")
	}
	return filepath.Join(base, "lamplight", "config.toml")
}

func loadConfig(path string) (config, error) {
	var cfg config
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, internalError("reading "+path, err)
	}
	if _, err := toml.Decode(string(data), &cfg); err != nil {
		return cfg, invalidf("%s is not valid TOML: %v", path, err)
	}
	if cfg.Format > FormatVersion {
		return cfg, newerFormat(path, cfg.Format)
	}
	return cfg, nil
}

var idEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

func randomID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // crypto/rand.Read never returns an error.
	return strings.ToLower(idEncoding.EncodeToString(b))
}

// ErrorCode classifies errors so the adapters can report them consistently.
type ErrorCode string

// Error codes reported by the CLI and the MCP server.
const (
	CodeInvalidArgument ErrorCode = "invalid_argument"
	CodeAlreadyExists   ErrorCode = "already_exists"
	CodeNotFound        ErrorCode = "not_found"
	CodeNewerFormat     ErrorCode = "newer_format"
	CodeInternal        ErrorCode = "internal"
)

// Error is a domain error with a stable code.
type Error struct {
	Code    ErrorCode
	Message string
	Err     error
}

func (e *Error) Error() string { return e.Message }

// Unwrap returns the underlying cause, if any.
func (e *Error) Unwrap() error { return e.Err }

// CodeOf returns the code of err, or CodeInternal for errors the core did not
// classify.
func CodeOf(err error) ErrorCode {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return CodeInternal
}

func invalidf(format string, args ...any) error {
	return &Error{Code: CodeInvalidArgument, Message: fmt.Sprintf(format, args...)}
}

func internalError(doing string, err error) error {
	return &Error{Code: CodeInternal, Message: fmt.Sprintf("%s: %v", doing, err), Err: err}
}

func newerFormat(path string, format int) error {
	return &Error{
		Code: CodeNewerFormat,
		Message: fmt.Sprintf("%s has format %d, but this version of study only understands format %d: upgrade study",
			path, format, FormatVersion),
	}
}
