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
	// carryMost is how much of the next page a Passage may take to finish
	// the sentence its page ends in.
	carryMost = 400
)

// Page is the text of one page of a Source, already cleaned (Clean). Number
// counts from 1. A Source that has no pages, a web page, is one Page whose
// Number is 0.
type Page struct {
	Number int
	Text   string
}

// Passage is one stretch of a Source's text. First and Last are the pages it
// is on, and both are 0 for a Source that has none.
type Passage struct {
	Text        string
	First, Last int
}

// Longest is the most characters a Passage cut to size can hold: a third
// over the size, so that no short tail is left on its own, and the end of a
// sentence carried over from the next page.
func Longest(size int) int { return bound(size) + bound(size)/3 + 1 + carryMost }

func bound(size int) int { return min(max(size, MinSize), MaxSize) }

// Passages cuts pages into Passages of about size characters, in the order
// of the text. Every word of every page is in exactly one Passage.
//
// A Passage keeps to one page, so that its location is one a reader can turn
// to. The one exception is a sentence that runs over the end of a page: the
// last Passage of that page takes the rest of the sentence from the next
// one, and is on both. Evidence quotes a sentence, and a quote can only be
// checked against a Passage that holds all of it.
//
// Within a page a Passage ends where the text does, in this order of
// preference: between two paragraphs, at the end of a sentence, at the end
// of a line, between two words.
func Passages(pages []Page, size int) []Passage {
	size = bound(size)
	var out []Passage
	skip := 0 // how many characters of this page the page before it took
	for i, page := range pages {
		text := []rune(page.Text)
		text, skip = text[min(skip, len(text)):], 0
		first := len(out)
		for _, piece := range split(text, size) {
			out = append(out, Passage{Text: piece, First: page.Number, Last: page.Number})
		}
		if len(out) == first || page.Number == 0 || i+1 >= len(pages) || pages[i+1].Number != page.Number+1 {
			continue
		}
		// The page ends in the middle of a sentence when its last line of
		// words does not end one. A line without a letter, a page number at
		// the foot of the page, says nothing either way.
		last := &out[len(out)-1]
		if endsSentence(last.Text) {
			continue
		}
		next := []rune(pages[i+1].Text)
		if n := sentenceEnd(next, carryMost); n > 0 {
			if rest := strings.TrimSpace(string(next[:n])); rest != "" {
				last.Text += "\n" + rest
				last.Last = page.Number + 1
				skip = n
			}
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

// sentenceEnd returns where the first sentence of text ends, when that is
// within most characters, and 0 otherwise.
func sentenceEnd(text []rune, most int) int {
	for i := 0; i < len(text) && i < most; i++ {
		if end := sentenceEndsAt(text, i); end > 0 && end <= most {
			return end
		}
	}
	return 0
}

// endsSentence reports whether text ends where a sentence does. Lines
// without a letter at the end of it are passed over.
func endsSentence(text string) bool {
	lines := strings.Split(text, "\n")
	for len(lines) > 1 && !strings.ContainsFunc(lines[len(lines)-1], unicode.IsLetter) {
		lines = lines[:len(lines)-1]
	}
	last := []rune(strings.TrimRight(strings.TrimSpace(lines[len(lines)-1]), closers))
	// A colon or a semicolon ends what the page had to say as well: what
	// follows is a list or a formula, not the rest of a sentence.
	return len(last) == 0 || strings.ContainsRune(stops+":;", last[len(last)-1])
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
