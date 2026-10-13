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
	// Late says that the plugin did not answer within the time study gave it
	// for what it was asked, and that study gave it up: a command was
	// stopped. Whoever would start the plugin again in the same run, to ask
	// it something else, would only wait for it a second time.
	Late bool
	// advice is the end of Message that says what to run or to look at,
	// breaks what of an answer is not the contract's, said alone, and broke
	// what was read in place of an answer when the connection broke under a
	// call, said alone too. They are for the one caller in this package that
	// says an error of a call in words of its own (Connection.untested).
	advice, breaks, broke string
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
// Nor is it trusted to say to an agent what the learner cannot see: a reason
// and a notice reach an agent through the tools as they reach a terminal.
// So the two kinds of character that are not shown are told apart:
//
//   - What a terminal would interpret is written out as an escape: a control
//     character, and a mark that turns the direction of text. The learner
//     and the agent then read the same of it.
//   - What shows nothing is left out (plugincontract.ShowsNothing): the tag
//     block, the variation selectors, the zero-width characters and the
//     other code points every display ignores. A sentence can be spelled in
//     them, and an escape written for each would still be that sentence, for
//     a program to read back. Leaving one out makes no space, and joins no
//     two words that white space stood between.
//
// White space of any kind and length is one space, and so is the braille
// blank, which shows as one (brailleBlank).
//
// Whoever looks for something in a plugin's words, to take it out, looks
// in them as they read without what shows nothing (readAs), and not as
// clean was handed them: what was found apart would be whole again here.
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
		if !invalid && (unicode.IsSpace(r) || r == brailleBlank) {
			space = b.Len() > 0
			continue
		}
		piece := string(r)
		switch {
		case invalid:
		case unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r):
			piece = fmt.Sprintf("\\u%04x", r)
		case plugincontract.ShowsNothing(r):
			// As if it had not been written: white space before it is
			// still to come, and none is made of it.
			continue
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

// brailleBlank is the braille pattern with no dot raised
// (plugincontract.BrailleBlank). A run of it would pad one line of a
// plugin's words into what looks like a second line on a terminal, with
// whatever the plugin chose to write there. clean takes it for white space.
const brailleBlank = plugincontract.BrailleBlank

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
