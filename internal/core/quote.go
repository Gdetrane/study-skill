package core

import (
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Whether a quote is in the text of its Passages (ADR-0012).
//
// Evidence is an exact quote, and a Passage is the Source's own text as a
// Knowledge base plugin extracted it: with the line breaks of a page, a word
// cut at a line's end, ligatures, and whatever dashes and quotation marks the
// file holds. Whoever quotes it writes the same words and seldom the same
// bytes. So a quote is in a text when its characters are there, in order and
// with nothing between them, once both are read like this:
//
//   - Unicode written in another form is the same text: both are brought to
//     NFKC, so an accent composed or apart, a ligature and its letters, a
//     no-break space and a space are each one thing.
//   - White space is not compared, wherever it is and wherever it is not: a
//     line break in the Passage, two spaces for one, and a space the
//     extraction lost or made up all pass. Nor is a character that is not
//     shown: a control character, a soft hyphen, a zero-width space.
//   - A quotation mark of any shape is a quotation mark, and a dash of any
//     length a dash (Unicode's Quotation_Mark and Dash): ’ for ' and — for -
//     are how a file and a keyboard write the same thing.
//   - A word cut at a line's end may be quoted whole: a hyphen that follows
//     a letter, with white space after it and then a letter, may be left
//     out. The white space is a line break as a file has it, and a space
//     once a plugin has joined the lines: which it is does not matter here
//     either. So may a hyphen at the end of a Passage whose word goes on in
//     the next. A hyphen that is left hanging on purpose, as in "pre- and
//     post-", may be left out with it: that much is given away.
//
// Everything else is compared as it is: every letter and its case, every
// digit, every other mark, and their order. A paraphrase, a word left out,
// two sentences joined with an ellipsis, and a correction of the Source's
// spelling are all not in the text.
//
// A quote can run from one Passage into the next: a plugin may keep every
// Passage on one page, and a sentence does not end with the page. So a quote
// is checked against the texts of a few Passages read one after the other,
// in the order given, and must then begin in the first and end in the last:
// each Passage named is one the quote is in.

// MaxEvidencePassages is the most Passages one piece of Evidence quotes
// from: a quote that runs through more is two quotes.
const MaxEvidencePassages = 5

// foldRune is a character as a quote is compared: nothing, for white space
// and for what is not shown; one quotation mark and one dash for all of
// theirs; and otherwise itself.
func foldRune(r rune) (rune, bool) {
	switch {
	case unicode.IsSpace(r), unicode.IsControl(r), unicode.Is(unicode.Cf, r):
		return 0, false
	case unicode.Is(unicode.Quotation_Mark, r):
		return '"', true
	case unicode.Is(unicode.Dash, r):
		return '-', true
	}
	return r, true
}

// foldQuote is a quote as it is compared.
func foldQuote(quote string) []rune {
	var out []rune
	for _, r := range norm.NFKC.String(quote) {
		if folded, kept := foldRune(r); kept {
			out = append(out, folded)
		}
	}
	return out
}

// passageText is the text of some Passages, read one after the other, as a
// quote is compared with it.
type passageText struct {
	runes []rune
	// optional says of each character that a quote may leave it out: the
	// hyphen of a word cut at a line's end.
	optional []bool
	// starts is where each Passage's text begins in runes.
	starts []int
}

// foldPassages reads the texts of some Passages one after the other.
func foldPassages(texts []string) passageText {
	raw := make([][]rune, len(texts))
	for i, text := range texts {
		raw[i] = []rune(norm.NFKC.String(text))
	}
	var t passageText
	for n, text := range raw {
		t.starts = append(t.starts, len(t.runes))
		for i, r := range text {
			folded, kept := foldRune(r)
			if !kept {
				continue
			}
			t.runes = append(t.runes, folded)
			t.optional = append(t.optional, cutsAWord(text, i, raw[n+1:]))
		}
	}
	return t
}

// cutsAWord reports whether the character at i of a Passage's text is a
// hyphen that cuts a word at a line's end: it follows a letter, white space
// follows it, and what follows that is a letter. The end of a Passage is
// white space too, when a Passage follows: after are the texts of the
// Passages that follow, in which the word then goes on.
//
// Only a hyphen is one: a longer dash at a line's end is punctuation, and a
// soft hyphen is not shown, so it is never compared at all.
func cutsAWord(text []rune, i int, after [][]rune) bool {
	if r := text[i]; r != '-' && r != '\U00002010' {
		return false
	}
	if i == 0 || !unicode.IsLetter(text[i-1]) {
		return false
	}
	// The next character that is compared, in this Passage or in a later
	// one, with whether white space, or the end of a Passage, is before it.
	apart := false
	for n, rest := range append([][]rune{text[i+1:]}, after...) {
		apart = apart || n > 0
		for _, r := range rest {
			if _, kept := foldRune(r); kept {
				return apart && unicode.IsLetter(r)
			}
			apart = apart || unicode.IsSpace(r)
		}
	}
	return false
}

// holds reports whether the quote is in the text: its characters, in order,
// with nothing between them but characters a quote may leave out. With more
// Passages than one, the quote must begin in the first and end in the last.
//
// It reads the text once, and keeps for every character which beginnings of
// the quote end there, as one bit each (the Shift-And of Baeza-Yates and
// Gonnet): what it costs is the text's length times the quote's, divided by
// 64, whatever the two hold. A character that may be left out keeps the
// beginnings that ended before it as well as the ones it ends.
func (t passageText) holds(quote []rune) bool {
	m := len(quote)
	if m == 0 {
		return false
	}
	words := (m + 63) / 64
	// masks says of each character at which places of the quote it is.
	masks := map[rune][]uint64{}
	for i, r := range quote {
		if masks[r] == nil {
			masks[r] = make([]uint64, words)
		}
		masks[r][i/64] |= 1 << (i % 64)
	}
	first, last := len(t.runes), 0
	if len(t.starts) > 1 {
		first, last = t.starts[1], t.starts[len(t.starts)-1]
	}
	state := make([]uint64, words)
	end, endBit := (m-1)/64, uint64(1)<<((m-1)%64)
	for j, r := range t.runes {
		mask := masks[r]
		// A quote begins only in the first Passage.
		carry := uint64(0)
		if j < first {
			carry = 1
		}
		ended := false
		for w := range words {
			had := state[w]
			next := had<<1 | carry
			carry = had >> 63
			if mask == nil {
				next = 0
			} else {
				next &= mask[w]
			}
			// The whole quote ends here when this character is its last: a
			// quote that ended earlier, and is only kept below, does not.
			ended = ended || w == end && next&endBit != 0
			if t.optional[j] {
				next |= had
			}
			state[w] = next
		}
		// And ends only in the last.
		if ended && j >= last {
			return true
		}
	}
	return false
}

// quoteIn reports whether a quote is in the texts of some Passages read one
// after the other, and when it is not, whether it is in one of them alone:
// alone is then that Passage's place among them, and -1 otherwise.
func quoteIn(quote string, texts []string) (found bool, alone int) {
	folded := foldQuote(quote)
	if foldPassages(texts).holds(folded) {
		return true, -1
	}
	if len(texts) > 1 {
		for i, text := range texts {
			if foldPassages([]string{text}).holds(folded) {
				return false, i
			}
		}
	}
	return false, -1
}
