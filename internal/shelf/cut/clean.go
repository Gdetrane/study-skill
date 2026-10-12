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

// Words is how a Source writes its words: the ones it holds, and the pairs it
// joins with a hyphen. Clean asks it what a word that was divided at the end
// of a line is, since the division looks the same for "solu-tion", which is
// one word, and for "well-known", which is two and keeps its hyphen. A
// reader's mark says only that a line ended there.
type Words struct {
	// words are the Source's words, in lower case, and pairs two words it
	// writes with a hyphen between them, as "well-known".
	words, pairs map[string]struct{}
}

// WordsOf reads how a Source writes its words from its text as a reader
// extracted it, page by page or whole. A word that is divided at the end of
// a line there counts as neither of its halves' words.
func WordsOf(texts ...string) *Words {
	w := &Words{words: map[string]struct{}{}, pairs: map[string]struct{}{}}
	for _, text := range texts {
		runes := []rune(plain(text))
		// before is the word that ended just before a hyphen, when the
		// next word follows the hyphen at once.
		before := ""
		for i := 0; i < len(runes); {
			if !unicode.IsLetter(runes[i]) {
				if !(runes[i] == '-' && before != "" && i+1 < len(runes) && unicode.IsLetter(runes[i+1])) {
					before = ""
				}
				i++
				continue
			}
			start := i
			for i < len(runes) && unicode.IsLetter(runes[i]) {
				i++
			}
			word := strings.ToLower(string(runes[start:i]))
			// A half of a divided word is no word of the Source's.
			divided := start > 0 && isDivision(runes[start-1]) || i < len(runes) && isDivision(runes[i])
			if !divided {
				w.words[word] = struct{}{}
				if before != "" {
					w.pairs[before+"-"+word] = struct{}{}
				}
			}
			before = ""
			if !divided && i < len(runes) && runes[i] == '-' {
				before = word
			}
		}
	}
	return w
}

func isDivision(r rune) bool { return r == hyphenMark || r == noCharacter }

// plain is text with its letters as Clean writes them: a ligature as the
// letters it draws, and an accent in one form.
func plain(text string) string {
	var b strings.Builder
	for _, r := range strings.ToValidUTF8(text, " ") {
		if r >= firstLigature && r <= lastLigature {
			b.WriteString(ligatures[r-firstLigature])
			continue
		}
		b.WriteRune(r)
	}
	return norm.NFC.String(b.String())
}

// one reports whether two halves of a word that was divided at the end of a
// line are one word, to be joined, or two that keep their hyphen:
//
//   - one word, when the Source holds the joined word elsewhere;
//   - two, when it writes the pair with a hyphen elsewhere, or holds each
//     half as a word of its own: "well" and "known", but not "solu";
//   - one word otherwise, which is what most divided words are.
//
// Without Words, every divided word is joined.
func (w *Words) one(left, right string) bool {
	if w == nil {
		return true
	}
	if one, known := w.written(left, right); known {
		return one
	}
	_, isWord := w.words[strings.ToLower(left)]
	_, isWordToo := w.words[strings.ToLower(right)]
	return !(isWord && isWordToo)
}

// written reports what the Source itself shows two halves to be, when it
// shows it: one word, since it holds the joined word elsewhere, or a pair,
// since it writes the two with a hyphen elsewhere.
func (w *Words) written(left, right string) (one, known bool) {
	if w == nil {
		return false, false
	}
	left, right = strings.ToLower(left), strings.ToLower(right)
	if _, held := w.words[left+right]; held {
		return true, true
	}
	if _, hyphenated := w.pairs[left+"-"+right]; hyphenated {
		return false, true
	}
	return false, false
}

// Clean tidies text as a reader extracted it, without rewriting it. words is
// how the Source the text is from writes its words (WordsOf), and may be
// nil.
//
//   - a word divided at the end of a line is joined, where the reader marked
//     the division, or keeps its hyphen when the Source's own words say it
//     is a pair such as "well-known" (Words);
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
func Clean(text string, words *Words) string {
	text = strings.ToValidUTF8(text, " ")
	text = strings.NewReplacer("\r\n", "\n", "\r", "\n", "\f", "\n").Replace(text)
	runes := []rune(text)
	var b strings.Builder
	b.Grow(len(text))
	for i, r := range runes {
		switch {
		case isDivision(r):
			// A division when it stands inside a word, as between "solu"
			// and "tion"; anywhere else it is a character of a formula's
			// font. Before a small letter it is taken for a division
			// always. Before a capital it is one only when the Source's own
			// words show it: "non-Euclidean" and "NASA" are divided so, and
			// so are two symbols of a formula that happen to be letters.
			if i > 0 && i+1 < len(runes) && unicode.IsLetter(runes[i-1]) && unicode.IsLetter(runes[i+1]) {
				left, right := i, i+1
				for left > 0 && unicode.IsLetter(runes[left-1]) {
					left--
				}
				for right < len(runes) && unicode.IsLetter(runes[right]) {
					right++
				}
				before, after := plain(string(runes[left:i])), plain(string(runes[i+1:right]))
				one, known := words.written(before, after)
				if unicode.IsLower(runes[i+1]) {
					one, known = words.one(before, after), true
				}
				if known {
					if !one {
						b.WriteByte('-')
					}
					continue
				}
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
