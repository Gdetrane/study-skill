package core

import (
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Whether a quote is in the text of its Passages (ADR-0012).
//
// Evidence is an exact quote, and "checked against its Passage" has to mean
// that: whoever reads it relies on the words being the Source's own. A
// Passage is the Source's text as a Knowledge base plugin extracted it, with
// the line breaks of a page, a word cut at a line's end, ligatures, and
// whatever dashes and quotation marks the file holds. Whoever quotes it
// copies the words, and a keyboard or an editor changes a few of those
// bytes on the way. So a quote is in a text when its characters are there,
// in order and with nothing between them, once both are read like this, and
// no more loosely:
//
//   - Letters with accents are written in one form (NFC), a ligature is the
//     letters it draws, and an ellipsis is three stops. Nothing else is
//     made like anything else: a superscript is not its digit, so 2ⁿ is not
//     2n, and a full-width letter is not the plain one.
//   - White space is white space, however much of it and of whichever kind:
//     a line break in the Passage is a space in the quote. But it is there
//     where the Passage has it, and nowhere else: "a symmetric matrix" is
//     not "asymmetric matrix", and "rm -rf /tmp" is not "rm -rf / tmp".
//   - A quote begins and ends where a word of the Passage does: "stable" is
//     not in "unstable", and "500 steps" is not in "1500 steps". Nor does it
//     begin or end between a letter and a mark written onto it, in any
//     script: a vowel sign is part of its word, and of its character.
//   - A single quotation mark of any shape is a single one, and a double a
//     double: ’ for ' and “ for ". The two are not each other: f' is not f".
//   - A dash of any length is a dash: — for -, and a minus sign too.
//   - A character that is not shown is not compared, where the Passage holds
//     one: a soft hyphen, a zero-width space. A quote may not hold one at
//     all, beyond the few a script needs (cleanQuote).
//   - A word cut at a line's end may be quoted whole: where two letters at
//     least, the last of them small, are followed by a hyphen, a line break
//     and a small letter, the hyphen and the break may be left out, or the
//     break alone. "Re-" and "member" at a sentence's start are "Remember";
//     "n-" and "k" keep their hyphen, as does "AB-" before anything. Any
//     other hyphen stays: "pre- and post-" keeps its two.
//   - The end of a Passage is white space with a line break in it, when the
//     quote goes on in the next: a quote has white space there, as between
//     any two words, unless a word was cut there or the script is written
//     without spaces. "an upper" and "triangular" are not "uppertriangular".
//   - Where a script is written without spaces between its words, a line
//     break between two of its characters may be left out.
//
// Everything else is compared as it is: every letter and its case, every
// digit, every other mark, and their order. A paraphrase, a word left out,
// a part of a word, two sentences joined with an ellipsis, and a correction
// of the Source's spelling are all not in the text.
//
// A quote can run from one Passage into the next: a plugin may keep every
// Passage on one page, and a sentence does not end with the page. So a quote
// is checked against the texts of a few Passages read one after the other,
// in the order given, and must then begin in the first and end in the last:
// each Passage named is one the quote is in.

// MaxEvidencePassages is the most Passages one piece of Evidence quotes
// from: a quote that runs through more is two quotes.
const MaxEvidencePassages = 5

// The quotation marks that are one mark to a quote, by kind.
const (
	singleMarks = "'‘’‚‛‹›＇"
	doubleMarks = "\"“”„‟«»〝〞〟＂"
)

// The ligatures of the Latin alphabet, from U+FB00: ff, fi, fl, ffi, ffl,
// long st, st.
var quoteLigatures = [...]string{"ff", "fi", "fl", "ffi", "ffl", "st", "st"}

// compared is one character of a text as a quote is compared with it.
type compared struct {
	r rune
	// space says that it stands for a run of white space, and breaks that
	// the run held a line break, or the end of a Passage.
	space, breaks bool
	// hyphen says that it was written as a hyphen, and not as a longer dash.
	hyphen bool
	// glued says that it was written as one character with the one before
	// it, which this reading spells out: the i of a ligature for fi, the
	// second stop of an ellipsis. No quote begins or ends between the two.
	glued bool
}

// unspaced reports whether a character is of a script that is written
// without spaces between its words.
func unspaced(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Thai, unicode.Lao, unicode.Khmer, unicode.Myanmar)
}

// wordy reports whether a character is part of a word that white space or a
// punctuation mark ends: a letter, a digit, or a mark written onto one, of a
// script that divides its words.
func wordy(r rune) bool {
	return (unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r)) && !unspaced(r)
}

// joined reports whether two characters that stand side by side in a
// Passage belong together, so that no quote begins or ends between them:
// two characters of one word, or a mark and what it is written onto, which
// are one character to whoever reads them, in a script with spaces or
// without. A text's end is 0, and joined to nothing.
func joined(first, second rune) bool {
	if first == 0 || second == 0 {
		return false
	}
	return unicode.IsMark(second) || wordy(first) && wordy(second)
}

// isLineBreak reports whether a white space character ends a line.
func isLineBreak(r rune) bool {
	switch r {
	case '\n', '\r', '\f', '\v', '\u0085', ' ', ' ':
		return true
	}
	return false
}

// foldText reads a text as a quote is compared: see the rules above. A run
// of white space is one character, and none stands at its ends.
func foldText(text string) []compared {
	var out []compared
	add := func(c compared) { out = append(out, c) }
	for _, r := range norm.NFC.String(text) {
		switch {
		case unicode.IsSpace(r):
			if n := len(out); n > 0 && out[n-1].space {
				out[n-1].breaks = out[n-1].breaks || isLineBreak(r)
			} else if n > 0 {
				add(compared{r: ' ', space: true, breaks: isLineBreak(r)})
			}
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r), isTag(r):
			// Not shown, and not compared. The tag block is named apart:
			// some of it is no format character, being no character yet,
			// and a Passage is handed on without any of it (withoutTags).
		case r >= 'ﬀ' && r <= 'ﬆ':
			for i, letter := range quoteLigatures[r-'ﬀ'] {
				add(compared{r: letter, glued: i > 0})
			}
		case r == '…':
			add(compared{r: '.'})
			add(compared{r: '.', glued: true})
			add(compared{r: '.', glued: true})
		case containsRune(singleMarks, r):
			add(compared{r: '\''})
		case containsRune(doubleMarks, r):
			add(compared{r: '"'})
		case r == '⁻' || r == '₋':
			// A minus that is written above or below the line is itself.
			add(compared{r: r})
		case unicode.Is(unicode.Dash, r):
			add(compared{r: '-', hyphen: r == '-' || r == '‐'})
		default:
			add(compared{r: r})
		}
	}
	if n := len(out); n > 0 && out[n-1].space {
		out = out[:n-1]
	}
	return out
}

func containsRune(set string, r rune) bool {
	for _, s := range set {
		if s == r {
			return true
		}
	}
	return false
}

// foldQuote is a quote as it is compared.
func foldQuote(quote string) []rune {
	folded := foldText(quote)
	out := make([]rune, len(folded))
	for i, c := range folded {
		out[i] = c.r
	}
	return out
}

// passageText is the text of some Passages, read one after the other, as a
// quote is compared with it.
type passageText struct {
	runes []rune
	// optional says of each character that a quote may leave it out: the
	// hyphen and the line break of a word cut at a line's end, and a line
	// break inside a script written without spaces. The end of a Passage is
	// a line break, and no more optional than another.
	optional []bool
	// glued says of each character that it is one character of the Passage
	// with the one before it (compared.glued), and cut that it is the hyphen
	// of a word cut at a line's end: no quote begins or ends at such a
	// hyphen, which is inside its word, though a quote may hold it.
	glued, cut []bool
	// starts is where each Passage's text begins in runes.
	starts []int
}

// foldPassages reads the texts of some Passages one after the other.
func foldPassages(texts []string) passageText {
	var all []compared
	var t passageText
	for _, text := range texts {
		folded := foldText(text)
		// The end of a Passage is a line break, when another follows and
		// either holds anything.
		if len(all) > 0 && len(folded) > 0 {
			all = append(all, compared{r: ' ', space: true, breaks: true})
		}
		t.starts = append(t.starts, len(all))
		all = append(all, folded...)
	}
	t.runes, t.optional = make([]rune, len(all)), make([]bool, len(all))
	t.glued, t.cut = make([]bool, len(all)), make([]bool, len(all))
	small := func(i int) bool { return i >= 0 && i < len(all) && unicode.IsLower(all[i].r) }
	letter := func(i int) bool { return i >= 0 && i < len(all) && unicode.IsLetter(all[i].r) }
	for i, c := range all {
		t.runes[i], t.glued[i] = c.r, c.glued
		switch {
		case c.space && c.breaks && i > 0 && i+1 < len(all) && unspaced(all[i-1].r) && unspaced(all[i+1].r):
			t.optional[i] = true
		case c.hyphen && small(i-1) && letter(i-2) && i+1 < len(all) && all[i+1].space && all[i+1].breaks && small(i+2):
			// A word cut at a line's end: the hyphen may be left out, and
			// the line break after it.
			t.optional[i], t.optional[i+1], t.cut[i] = true, true, true
		}
	}
	// The break after a cut hyphen was marked above, and the cases that
	// follow it in the loop leave what is marked as it is.
	return t
}

// holds reports whether the quote is in the text: its characters, in order,
// with nothing between them but characters a quote may leave out, beginning
// and ending where a word does. With more Passages than one, the quote must
// begin in the first and end in the last.
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
	// before and after are, for each character, the nearest one on that
	// side that a quote cannot leave out, or 0 at the text's ends: what a
	// quote that begins or ends here has for a neighbour.
	n := len(t.runes)
	before, after := make([]rune, n), make([]rune, n)
	var last rune
	for j := 0; j < n; j++ {
		before[j] = last
		if !t.optional[j] {
			last = t.runes[j]
		}
	}
	last = 0
	for j := n - 1; j >= 0; j-- {
		after[j] = last
		if !t.optional[j] {
			last = t.runes[j]
		}
	}
	firstEnd, lastStart := n, 0
	if len(t.starts) > 1 {
		firstEnd, lastStart = t.starts[1], t.starts[len(t.starts)-1]
	}
	state := make([]uint64, words)
	end, endBit := (m-1)/64, uint64(1)<<((m-1)%64)
	for j, r := range t.runes {
		mask := masks[r]
		// A quote begins only in the first Passage, and never inside a word
		// or a character: not at the hyphen a word was cut with, and not at
		// the later part of what the Passage writes as one character.
		carry := uint64(0)
		if j < firstEnd && !t.cut[j] && !t.glued[j] && !joined(before[j], quote[0]) {
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
		// And ends only in the last, and never inside a word or a character.
		inside := t.cut[j] || j+1 < n && t.glued[j+1]
		if ended && j >= lastStart && !inside && !joined(quote[m-1], after[j]) {
			return true
		}
	}
	return false
}

// quotable reports whether a quote holds anything to check: a letter or a
// digit. A mark alone is in every text.
func quotable(quote []rune) bool {
	for _, r := range quote {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
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
	if !quotable(folded) {
		return false, -1
	}
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
