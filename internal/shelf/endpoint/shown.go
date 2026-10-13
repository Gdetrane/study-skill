package endpoint

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
)

// shown is text as it will be shown: on one line, without what a terminal
// would act on and without what shows nothing.
//
// What shows nothing is what study leaves out of a plugin's words too
// (plugincontract.ShowsNothing). So a key or a name that a server repeats
// with such a character inside it is whole again here, where it is looked
// for, and not only later, where study shows it.
func shown(text string) string {
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t' || r == '\r' || r == plugincontract.BrailleBlank:
			// The blank that is no white space is one where study shows
			// the text: what follows "Bearer" behind it follows it there.
			return ' '
		case !unicode.IsPrint(r) || unicode.Is(unicode.Bidi_Control, r) || plugincontract.ShowsNothing(r):
			return -1
		}
		return r
	}, strings.ToValidUTF8(text, ""))), " ")
}

// withoutFold is text with with in place of part, wherever part stands and
// in whatever letter case.
func withoutFold(text, part, with string) string {
	in, err := regexp.Compile("(?i)" + regexp.QuoteMeta(part))
	if err != nil {
		// Never, for text that is shown: as it is written, then.
		return strings.ReplaceAll(text, part, with)
	}
	return in.ReplaceAllLiteralString(text, with)
}
