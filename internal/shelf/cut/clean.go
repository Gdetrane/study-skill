// Package cut turns the text Shelf read from a Source into Passages: it
// tidies what a reader extracted, and cuts each page into stretches of about
// the size a search should return.
//
// A Passage is the Source's own text (ADR-0012): nothing here writes a word
// the Source does not hold. Clean drops what is no text at all, and Passages
// only decides where one stretch ends and the next begins.
package cut

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Characters Clean knows by their number, since none of them shows.
const (
	// hyphenMark is what PDFium writes where a word was divided at the end
	// of a line, in place of the hyphen and the line break. Older versions
	// wrote noCharacter there.
	hyphenMark  = 0x0002
	noCharacter = 0xFFFE
	lastOfPlane = 0xFFFF
	softHyphen  = 0x00AD
	byteOrder   = 0xFEFF
	// The ligatures of the Latin alphabet: ff, fi, fl, ffi, ffl, long st, st.
	firstLigature = 0xFB00
	lastLigature  = 0xFB06
)

// ligatures are the letters a typeface draws as one glyph, and that a file
// may hold as that glyph's own character, from firstLigature on. A search
// for "find" must find a word that was set with the glyph for "fi".
var ligatures = [...]string{"ff", "fi", "fl", "ffi", "ffl", "st", "st"}

// Clean tidies text as a reader extracted it, without rewriting it:
//
//   - a word divided at the end of a line is joined, where the reader marked
//     the division;
//   - a ligature's character becomes the letters it draws;
//   - characters that are no text are dropped: control characters, which a
//     font without a table of its characters produces for a formula, soft
//     hyphens and invalid UTF-8. Each run of them leaves a space, so the
//     words around it stay apart;
//   - letters with accents are written in one form (NFC), so that the same
//     word is the same bytes wherever it came from;
//   - line endings are line feeds, no line ends in white space, and at most
//     one empty line stands between two paragraphs.
//
// Private-use characters are kept: a symbol font's glyphs are the Source's
// own, even where nothing can show them.
func Clean(text string) string {
	text = strings.ToValidUTF8(text, " ")
	text = strings.NewReplacer("\r\n", "\n", "\r", "\n", "\f", "\n").Replace(text)
	runes := []rune(text)
	var b strings.Builder
	b.Grow(len(text))
	for i, r := range runes {
		switch {
		case r == hyphenMark || r == noCharacter:
			// Joined when it stands inside a word, as between "solu" and
			// "tion"; anywhere else it is a character of a formula's font.
			if i > 0 && i+1 < len(runes) && unicode.IsLetter(runes[i-1]) && unicode.IsLower(runes[i+1]) {
				continue
			}
			b.WriteByte(' ')
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r == softHyphen || r == byteOrder:
			// Neither shows anything.
		case unicode.IsControl(r) || r == lastOfPlane || r == unicode.ReplacementChar:
			b.WriteByte(' ')
		case r >= firstLigature && r <= lastLigature:
			b.WriteString(ligatures[r-firstLigature])
		default:
			b.WriteRune(r)
		}
	}
	lines := strings.Split(norm.NFC.String(b.String()), "\n")
	out := lines[:0]
	blank := true // so that the text does not begin with an empty line
	for _, line := range lines {
		line = strings.TrimRightFunc(line, unicode.IsSpace)
		if line == "" {
			if blank {
				continue
			}
			blank = true
		} else {
			blank = false
		}
		out = append(out, line)
	}
	return strings.TrimRightFunc(strings.Join(out, "\n"), unicode.IsSpace)
}
