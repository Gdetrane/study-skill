package core

import (
	"strings"
	"testing"
	"time"
)

// What "in its text" allows, and what it does not: each quote against the
// text of one Passage.
func TestAQuoteIsInAPassage(t *testing.T) {
	for _, tc := range []struct {
		name, text, quote string
		want              bool
	}{
		{"the same text", "Elimination produces an upper triangular system.", "Elimination produces an upper triangular system.", true},
		{"a part of it", "Elimination produces an upper triangular system.", "an upper triangular", true},

		// White space is not compared.
		{"the Passage's line breaks", "Elimination produces\nan upper   triangular\r\n\tsystem.", "Elimination produces an upper triangular system.", true},
		{"line breaks in the quote", "Elimination produces an upper triangular system.", "Elimination\nproduces an\nupper triangular system.", true},
		{"a space the extraction lost", "Elimination producesan upper triangular system.", "Elimination produces an upper", true},
		{"a space the extraction made up", "Elimi nation produces an upper triangular system.", "Elimination produces", true},
		{"a page break", "ends the page.\fThe next begins", "ends the page. The next begins", true},

		// Unicode written in another form is the same text.
		{"an accent apart in the Passage", "the nai\U00000308ve cafe\U00000301 method", "the naïve café method", true},
		{"an accent apart in the quote", "the naïve café method", "the nai\U00000308ve cafe\U00000301 method", true},
		{"a ligature in the Passage", "the ﬁrst ﬂoor of the oﬃce", "the first floor of the office", true},
		{"a ligature in the quote", "the first floor", "the ﬁrst ﬂoor", true},
		{"a no-break space", "a\U000000a0pivot is", "a pivot is", true},
		{"full-width letters", "ＡＢＣ is the key", "ABC is the key", true},
		{"an ellipsis character", "and so on… until", "and so on... until", true},

		// What is not shown is not compared.
		{"a soft hyphen", "elimi\U000000adnation produces", "elimination produces", true},
		{"a zero-width space", "upper\U0000200btriangular", "uppertriangular", true},
		{"a control character", "upper\x01 triangular", "upper triangular", true},

		// Quotation marks of any shape, and dashes of any length.
		{"curly quotes for straight", "the “pivot” is the row’s first entry", `the "pivot" is the row's first entry`, true},
		{"straight quotes for curly", `the "pivot" is the row's first entry`, "the “pivot” is the row’s first entry", true},
		{"guillemets", "the «pivot» is", `the "pivot" is`, true},
		{"a minus sign and an en dash", "x − 1 on pages 3–5 — as said", "x - 1 on pages 3-5 - as said", true},

		// A word cut at a line's end may be quoted whole.
		{"a word cut at a line's end, quoted whole", "Elimi-\nnation produces", "Elimination produces", true},
		{"a word cut at a line's end, quoted as it is", "Elimi-\nnation produces", "Elimi-\nnation produces", true},
		{"a word cut at a line's end, quoted with its hyphen", "Elimi-\nnation produces", "Elimi-nation produces", true},
		{"spaces around the line break", "Elimi- \n  nation produces", "Elimination produces", true},
		{"a Unicode hyphen at a line's end", "Elimi\U00002010\nnation produces", "Elimination produces", true},
		{"a compound cut at its hyphen", "a well-\nknown result", "a well-known result", true},
		{"two words cut, one of them a compound", "a well-\nknown elimi-\nnation step", "a well-known elimination step", true},
		{"a hyphen whose next line begins with no letter stays", "word-\n-word", "word-word", false},
		{"and is quoted with what follows it", "word-\n-word", "word--word", true},
		// A plugin that joined the lines left a space where the line broke:
		// which white space follows the hyphen is not compared either.
		{"a word cut where the lines were joined", "Elimi- nation produces", "Elimination produces", true},
		{"a hyphen left hanging, which is given away", "pre- and post-processing", "pre and post-processing", true},
		{"a hyphen with nothing after it but a mark", "the pivot- , and", "the pivot , and", false},
		{"a hyphen inside a line stays", "a well-known result", "a wellknown result", false},
		{"a minus at a line's end stays", "a -\nb is the difference", "a b is the difference", false},
		{"a hyphen before a digit stays", "see x-\n1 below", "see x1 below", false},
		{"a longer dash at a line's end stays", "the word—\nand more", "the wordand more", false},

		// Everything else is compared as it is.
		{"another letter case", "Elimination produces an upper triangular system.", "elimination produces", false},
		{"a paraphrase", "Elimination produces an upper triangular system.", "Elimination gives an upper triangular system.", false},
		{"a word left out", "Elimination produces an upper triangular system.", "Elimination produces an triangular system.", false},
		{"words in another order", "Elimination produces an upper triangular system.", "an upper triangular system produces", false},
		{"another punctuation mark", "the pivots, on the diagonal", "the pivots; on the diagonal", false},
		{"a mark left out", "the pivots, on the diagonal", "the pivots on the diagonal", false},
		{"two sentences joined with an ellipsis", "One thing. Another thing. A third.", "One thing. … A third.", false},
		{"a corrected spelling", "the triangluar system", "the triangular system", false},
		{"more than the Passage holds", "an upper triangular", "an upper triangular system", false},
		{"nothing to compare", "an upper triangular", " \U0000200b\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, _ := quoteIn(tc.quote, []string{tc.text}); got != tc.want {
				t.Errorf("quoteIn(%q, %q) = %v, want %v", tc.quote, tc.text, got, tc.want)
			}
		})
	}
}

// A quote that runs from one Passage into the next is checked against
// their texts read one after the other, and must begin in the first and end
// in the last.
func TestAQuoteRunsThroughPassages(t *testing.T) {
	one := "The pivots are on the diagonal. Elimination produces an upper"
	two := "triangular system. Back substitution"
	three := "then solves it, from the last equation up."
	for _, tc := range []struct {
		name  string
		texts []string
		quote string
		want  bool
		alone int
	}{
		{"from the first into the second", []string{one, two}, "Elimination produces an upper triangular system.", true, -1},
		{"through three", []string{one, two, three}, "an upper triangular system. Back substitution then solves it", true, -1},
		{"a word cut at the end of a Passage", []string{"Elimi-", "nation produces"}, "Elimination produces", true, -1},
		{"a word cut at the end of a Passage, with its hyphen", []string{"Elimi-\n", "\nnation produces"}, "Elimi-nation produces", true, -1},
		{"a compound cut at the end of a Passage", []string{"a well-", "known result"}, "a well-known result", true, -1},
		{"all in the first", []string{one, two}, "The pivots are on the diagonal.", false, 0},
		{"all in the second", []string{one, two}, "Back substitution", false, 1},
		{"through two of three, and not the third", []string{one, two, three}, "an upper triangular system.", false, -1},
		{"the Passages in the other order", []string{two, one}, "Elimination produces an upper triangular system.", false, -1},
		{"the end of one glued to the middle of the next", []string{one, two}, "an upper system. Back substitution", false, -1},
		{"in neither", []string{one, two}, "Elimination yields a triangle.", false, -1},
		// The one Passage is the last too: a hyphen at its end cuts nothing.
		{"a hyphen at the end of the only Passage", []string{"the pivot-"}, "the pivot", true, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, alone := quoteIn(tc.quote, tc.texts)
			if got != tc.want || alone != tc.alone {
				t.Errorf("quoteIn(%q, %q) = %v, %d; want %v, %d", tc.quote, tc.texts, got, alone, tc.want, tc.alone)
			}
		})
	}
}

// A long quote is compared in words of 64 characters: it is found whole,
// and not found with one character changed, wherever in the quote that is.
func TestALongQuoteIsComparedWhole(t *testing.T) {
	var b strings.Builder
	for i := range 400 {
		b.WriteString("Sentence ")
		b.WriteString(strings.Repeat("x", i%7+1))
		b.WriteString(" ends here. ")
	}
	text := b.String()
	quote := text[1000:3500]
	if found, _ := quoteIn(quote, []string{text}); !found {
		t.Fatal("a quote of 2,500 characters taken from the text is not found in it")
	}
	for _, at := range []int{0, 1, 62, 63, 64, 65, 127, 128, 129, 1000, len(quote) - 2, len(quote) - 1} {
		changed := quote[:at] + "#" + quote[at+1:]
		if found, _ := quoteIn(changed, []string{text}); found {
			t.Errorf("the quote with character %d changed is found in the text", at)
		}
	}
}

// What the check costs does not depend on what the texts hold: the longest
// quote against the most text a plugin can give, both of one letter, is
// done in a moment, where comparing at every place would take minutes.
func TestTheCheckIsBoundedByTheTextsLength(t *testing.T) {
	texts := make([]string, MaxEvidencePassages)
	for i := range texts {
		texts[i] = strings.Repeat("a", 32<<10)
	}
	quote := strings.Repeat("a", maxQuoteRunes)
	began := time.Now()
	found, alone := quoteIn(quote, texts)
	// It is in each Passage alone, and so does not run through them all.
	if found || alone != 0 {
		t.Errorf("quoteIn = %v, %d; want the quote found in the first Passage alone", found, alone)
	}
	if took := time.Since(began); took > 30*time.Second {
		t.Errorf("the check took %s", took)
	}
}
