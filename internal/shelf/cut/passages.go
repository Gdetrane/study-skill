package cut

import (
	"strings"
	"unicode"
)

// Sizes of a Passage, in characters.
const (
	// DefaultSize is about how long a Passage is when nothing says
	// otherwise: long enough to hold a thought, short enough to quote from.
	DefaultSize = 1500
	// MinSize and MaxSize bound what a caller may ask for. At MaxSize the
	// longest Passage this package makes still fits the contract's limit on a
	// Passage's text, in any script.
	MinSize = 200
	MaxSize = 5000
	// loneNumber is how many bytes a stretch without a letter must hold to
	// be a Passage: fewer are a page number, with whatever is drawn around
	// it.
	loneNumber = 16
)

// Page is the text of one page of a Source, already cleaned (Clean). Number
// counts from 1. A Source that has no pages, a web page, is one Page whose
// Number is 0.
type Page struct {
	Number int
	Text   string
}

// Passage is one stretch of a Source's text. First and Last are the pages it
// is on, which are one page, and both are 0 for a Source that has none.
type Passage struct {
	Text        string
	First, Last int
}

// Longest is the most characters a Passage cut to size can hold: a third
// over the size, so that no short tail is left on its own.
func Longest(size int) int { return bound(size) + bound(size)/3 + 1 }

func bound(size int) int { return min(max(size, MinSize), MaxSize) }

// Passages cuts pages into Passages of about size characters, in the order
// of the text. Every word of every page is in exactly one Passage.
//
// A Passage keeps to one page, always, so that its location is one a reader
// can turn to. A sentence that runs over the end of a page is in two
// Passages, the last of the one page and the first of the next. Taking the
// rest of such a sentence into the Passage it begins in was tried, so that a
// quote of it could be checked against one Passage, and did not do that: a
// printed book begins each page with a running header, which then stood in
// the middle of the sentence, and a page that ends in a heading or a table
// row looked like an unfinished sentence too. Whoever checks a quote that
// runs from one Passage into the next reads the two together.
//
// Within a page a Passage ends where the text does, in this order of
// preference: between two paragraphs, at the end of a sentence, at the end
// of a line, between two words. A few characters without a letter among
// them, a page number alone on its page, are no Passage: there is nothing
// in them to search for or to quote. A table of numbers is one.
func Passages(pages []Page, size int) []Passage {
	size = bound(size)
	var out []Passage
	for _, page := range pages {
		for _, piece := range split([]rune(page.Text), size) {
			if len(piece) < loneNumber && !strings.ContainsFunc(piece, unicode.IsLetter) {
				continue
			}
			out = append(out, Passage{Text: piece, First: page.Number, Last: page.Number})
		}
	}
	return out
}

// split cuts the text of one page into pieces of about size characters, each
// with no white space around it.
func split(text []rune, size int) []string {
	var out []string
	for {
		text = trim(text)
		if len(text) == 0 {
			return out
		}
		// What is left fits in one piece, or would leave a tail too short to
		// stand on its own.
		if len(text) <= size+size/3 {
			return append(out, string(text))
		}
		at := breakAt(text, size)
		if piece := trim(text[:at]); len(piece) > 0 {
			out = append(out, string(piece))
		}
		text = text[at:]
	}
}

// breakAt finds where a piece of at most size characters ends in text, which
// is longer: the best place in its second half.
func breakAt(text []rune, size int) int {
	half := size / 2
	// Between two paragraphs, when that leaves a piece of some length.
	for i := size - 1; i >= size*3/5; i-- {
		if text[i] == '\n' && text[i-1] == '\n' {
			return i + 1
		}
	}
	for i := size - 1; i >= half; i-- {
		if end := sentenceEndsAt(text, i); end > 0 && end <= size {
			return end
		}
	}
	for i := size - 1; i >= half; i-- {
		if text[i] == '\n' {
			return i + 1
		}
	}
	for i := size - 1; i >= half; i-- {
		if unicode.IsSpace(text[i]) {
			return i + 1
		}
	}
	// One word, or a script written without spaces.
	return size
}

// stops end a sentence, and closers may follow one before the space.
const (
	stops   = ".!?…。！？"
	closers = "\"')]}”’»›"
)

// sentenceEndsAt reports where the sentence ends that has a stop at index i:
// after the stop and what closes a quotation or a bracket behind it, when
// white space or the end of the text follows. It returns 0 when no sentence
// ends there.
func sentenceEndsAt(text []rune, i int) int {
	if !strings.ContainsRune(stops, text[i]) {
		return 0
	}
	end := i + 1
	for end < len(text) && strings.ContainsRune(closers, text[end]) {
		end++
	}
	if end < len(text) && !unicode.IsSpace(text[end]) {
		return 0 // 3.14, or an address
	}
	return end
}

func trim(text []rune) []rune {
	for len(text) > 0 && unicode.IsSpace(text[0]) {
		text = text[1:]
	}
	for len(text) > 0 && unicode.IsSpace(text[len(text)-1]) {
		text = text[:len(text)-1]
	}
	return text
}
