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
	// AboutTheCall says that the error is about what one call asked, and
	// not about the plugin: the plugin answered and refused the call with
	// the contract's error, or study did not send the call because it is
	// not one the contract allows. The plugin works as before, and the next
	// call is worth making. Every other error of a call says the plugin did
	// not answer, or not as the contract has it.
	AboutTheCall bool
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

// clean makes text a plugin wrote safe to put in a message and on a
// terminal: one line, cut to max bytes, with nothing a terminal would
// interpret. A plugin is not trusted with the learner's terminal.
//
// It reads the text once and stops where the cleaned text is full, so what
// it costs is what it keeps: a plugin's message can be as long as a message
// may be, and none of that is copied.
func clean(s string, max int) string {
	var b strings.Builder
	space, wasInvalid := false, false
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		invalid := r == utf8.RuneError && size <= 1
		s = s[size:]
		// Bytes that are no UTF-8 become one replacement character, however
		// many of them follow one another.
		if invalid && wasInvalid {
			continue
		}
		wasInvalid = invalid
		if !invalid && unicode.IsSpace(r) {
			space = b.Len() > 0
			continue
		}
		piece := string(r)
		if !invalid && (unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r)) {
			piece = fmt.Sprintf("\\u%04x", r)
		}
		need := len(piece)
		if space {
			need++
		}
		if b.Len()+need > max {
			b.WriteString("…")
			return b.String()
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteString(piece)
	}
	return b.String()
}

// cleanEnd is clean for text whose end matters more than its start, such as
// what a program wrote before it failed: the last max bytes of the cleaned
// text are kept. The text is short already: study keeps only the end of a
// plugin's standard error.
func cleanEnd(s string, max int) string {
	whole := clean(s, 8*len(s)+8)
	if len(whole) <= max {
		return whole
	}
	start := len(whole) - max
	for start < len(whole) && !utf8.RuneStart(whole[start]) {
		start++
	}
	// Not in the middle of an escape either.
	if i := strings.IndexByte(whole[start:], ' '); i >= 0 && i < 16 {
		start += i + 1
	}
	return "…" + whole[start:]
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
	return clean(without(libraryPrefix.ReplaceAllString(err.Error(), ""), address), 400)
}

// without takes the parts of a registered address that may be a key out of
// a text. The address whole is left as study itself shows it: its scheme
// and host.
func without(text, address string) string {
	if address == "" {
		return text
	}
	text = strings.ReplaceAll(text, address, Address(address)+"…")
	for _, secret := range secrets(address) {
		text = strings.ReplaceAll(text, secret, "…")
	}
	return text
}
