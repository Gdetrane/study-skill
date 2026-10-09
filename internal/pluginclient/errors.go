package pluginclient

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
)

// Codes of this package's errors. They are among the codes docs/cli.md
// documents, under the names the core gives them.
const (
	CodeInvalidArgument    = "invalid_argument"
	CodeNotFound           = "not_found"
	CodeNewerFormat        = "newer_format"
	CodeFailedPrecondition = "failed_precondition"
	CodeCanceled           = "canceled"
	CodeInternal           = "internal"
)

// Error is an error of starting, reaching or asking a Knowledge base plugin,
// with one of the documented codes. Its message is safe to print: whatever
// in it came from the plugin was cleaned.
type Error struct {
	Code    string
	Message string
	Err     error
}

func (e *Error) Error() string { return e.Message }

// Unwrap returns the underlying cause, if any.
func (e *Error) Unwrap() error { return e.Err }

// ErrorCode is the error's code. The core's CodeOf reads it, so the adapters
// report it like any other.
func (e *Error) ErrorCode() string { return e.Code }

func errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// StudyCode is the code study reports a plugin's refusal under, given the
// code the plugin refused with. A plugin that cannot do something, or that
// failed, is the learner's setup to put right and no bug in study: it is
// failed_precondition, never internal. A code the contract does not have is
// read as the plugin's internal.
func StudyCode(pluginCode string) string {
	switch pluginCode {
	case plugincontract.CodeInvalidArgument:
		return CodeInvalidArgument
	case plugincontract.CodeNotFound:
		return CodeNotFound
	default:
		return CodeFailedPrecondition
	}
}

// toolError is the error for a call the plugin refused or failed.
func toolError(name, tool string, body plugincontract.ErrorBody) *Error {
	said := clean(body.Message, plugincontract.MaxTextBytes)
	if said == "" {
		said = "it gave no reason"
	}
	code := StudyCode(body.Code)
	switch body.Code {
	case plugincontract.CodeInvalidArgument:
		return errorf(code, "Knowledge base plugin %s does not take what its %s tool was given: %s", name, tool, said)
	case plugincontract.CodeNotFound:
		return errorf(code, "Knowledge base plugin %s does not have what its %s tool was asked for: %s", name, tool, said)
	case plugincontract.CodeUnsupported:
		return errorf(code, "Knowledge base plugin %s cannot do what its %s tool was asked: %s", name, tool, said)
	default:
		return errorf(code, "Knowledge base plugin %s failed in its %s tool: %s. The plugin's own messages may say more; "+
			"study knowledge-base check %s shows whether it still starts", name, tool, said, name)
	}
}

// clean makes text a plugin wrote safe to put in a message and on a
// terminal: one line, cut to max bytes, with nothing a terminal would
// interpret. A plugin is not trusted with the learner's terminal.
func clean(s string, max int) string {
	s = strings.Join(strings.Fields(strings.ToValidUTF8(s, string(utf8.RuneError))), " ")
	cut := len(s) > max
	if cut {
		s = plugincontract.Cut(s, max)
	}
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			fmt.Fprintf(&b, "\\u%04x", r)
			continue
		}
		b.WriteRune(r)
	}
	if cut {
		b.WriteString("…")
	}
	return b.String()
}

// libraryPrefix is what the MCP library puts in front of an error: which
// request it was sending. The message says that in its own words.
var libraryPrefix = regexp.MustCompile(`^(?:(?:calling|sending) "[^"]*": )+`)

// reason is an error's text without what must not be repeated: the address
// of a plugin reached by URL, which may hold a key. net/http puts the
// address in front of every error it returns.
func reason(err error, address string) string {
	var withURL *url.Error
	for errors.As(err, &withURL) {
		err = withURL.Err
	}
	text := libraryPrefix.ReplaceAllString(err.Error(), "")
	if address != "" {
		text = strings.ReplaceAll(text, address, "its address")
		// And the parts of it a key could be in, should a message hold one
		// without the rest.
		if u, perr := url.Parse(address); perr == nil {
			for _, part := range []string{u.RequestURI(), u.RawQuery} {
				if len(part) >= 8 {
					text = strings.ReplaceAll(text, part, "…")
				}
			}
		}
	}
	return clean(text, 400)
}
