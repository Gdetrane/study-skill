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
		{"a part of it, between marks", "It is (the pivot), as said.", "the pivot", true},

		// White space is white space, however much and of whichever kind,
		// and is there where the Passage has it and nowhere else.
		{"the Passage's line breaks", "Elimination produces\nan upper   triangular\r\n\tsystem.", "Elimination produces an upper triangular system.", true},
		{"line breaks in the quote", "Elimination produces an upper triangular system.", "Elimination\nproduces an\nupper triangular system.", true},
		{"a page break", "ends the page.\fThe next begins", "ends the page. The next begins", true},
		{"a no-break space", "a\U000000a0pivot is", "a pivot is", true},
		{"a space the Passage does not have", "Elimination producesan upper triangular system.", "Elimination produces an upper", false},
		{"a space the Passage has, left out", "Elimi nation produces an upper triangular system.", "Elimination produces", false},
		{"two words made one", "Let A be a symmetric matrix.", "Let A be asymmetric matrix", false},
		{"one word made two", "It is notable that", "It is not able that", false},
		{"a space moved in a command", "Clean up with: rm -rf /tmp/build", "rm -rf / tmp/build", false},
		{"two numbers made one", "in rows 1 2 and 3", "in rows 12 and 3", false},

		// A quote begins and ends where a word does.
		{"the end of a word", "The scheme is unstable for all n.", "stable for all n", false},
		{"the beginning of a word", "It is impossible to say.", "It is imposs", false},
		{"the end of a number", "after 1500 steps", "500 steps", false},
		{"one letter of a word", "the pivots", "e", false},
		{"a word of one letter", "let e be the base", "e", true},
		{"a mark alone", "the pivots, on the diagonal.", ".", false},
		// A mark written onto a letter is part of its word and of its
		// character: a vowel sign that no form of the text joins to its
		// letter, an accent that has no letter of its own.
		{"a word without its last vowel sign", "\U00000643\U0000062A\U00000627\U00000628\U0000064C \U0000062C\U0000062F\U0000064A\U0000062F", "\U00000643\U0000062A\U00000627\U00000628", false},
		{"the word with it", "\U00000643\U0000062A\U00000627\U00000628\U0000064C \U0000062C\U0000062F\U0000064A\U0000062F", "\U00000643\U0000062A\U00000627\U00000628\U0000064C", true},
		{"a letter without the mark over it", "the mean x\U00000304 of the sample", "the mean x", false},
		{"the letter with its mark", "the mean x\U00000304 of the sample", "the mean x\U00000304 of", true},
		{"a quote that begins at a mark", "the mean x\U00000304 of the sample", "\U00000304 of the sample", false},
		{"a consonant without its vowel sign, in a script without spaces", "\U00000E01\U00000E34\U00000E19\U00000E02\U00000E49\U00000E32\U00000E27", "\U00000E01", false},
		{"with it", "\U00000E01\U00000E34\U00000E19\U00000E02\U00000E49\U00000E32\U00000E27", "\U00000E01\U00000E34\U00000E19", true},

		// Letters in another form are the same letters, and nothing else is
		// made like anything else.
		{"an accent apart in the Passage", "the nai\U00000308ve cafe\U00000301 method", "the naïve café method", true},
		{"an accent apart in the quote", "the naïve café method", "the nai\U00000308ve cafe\U00000301 method", true},
		{"a ligature in the Passage", "the \U0000FB01rst \U0000FB02oor of the o\U0000FB03ce", "the first floor of the office", true},
		{"a ligature in the quote", "the first floor", "the \U0000FB01rst \U0000FB02oor", true},
		{"an ellipsis character", "and so on\U00002026 until", "and so on... until", true},
		// What the Passage writes as one character is quoted whole: three
		// stops stand for an ellipsis, and one of them does not.
		{"an ellipsis, up to its first stop", "The result\U00002026 Next comes", "The result.", false},
		{"an ellipsis, up to its second stop", "The result\U00002026 Next comes", "The result..", false},
		{"an ellipsis whole", "The result\U00002026 Next comes", "The result...", true},
		{"from inside an ellipsis", "wait\U00002026 for it", ".. for it", false},
		{"three stops that were typed are three characters", "The result... Next comes", "The result.", true},
		{"full-width letters are not plain ones", "\U0000FF21\U0000FF22\U0000FF23 is the key", "ABC is the key", false},
		{"a superscript is not its digit", "runs in O(2\U0000207F) time", "runs in O(2n) time", false},
		{"a power of ten is not a number", "about 10\U00002076 rows", "about 106 rows", false},
		{"a subscript is not a superscript", "the term x\U00002082 here", "the term x\U000000B2 here", false},
		{"an inverse is not a difference", "then A\U0000207B\U000000B9 exists", "then A-1 exists", false},

		// What is not shown is not compared, where the Passage holds it.
		{"a soft hyphen", "elimi\U000000adnation produces", "elimination produces", true},
		{"a zero-width space", "upper\U0000200btriangular system", "uppertriangular system", true},
		{"a control character", "upper\x01 triangular", "upper triangular", true},

		// A quotation mark of its kind, and a dash of any length.
		{"curly quotes for straight", "the \U0000201Cpivot\U0000201D is the row\U00002019s first entry", `the "pivot" is the row's first entry`, true},
		{"straight quotes for curly", `the "pivot" is the row's first entry`, "the \U0000201Cpivot\U0000201D is the row\U00002019s first entry", true},
		{"guillemets", "the \U000000ABpivot\U000000BB is", `the "pivot" is`, true},
		{"a single mark is not a double one", "with f'(x) = 0", `with f"(x) = 0`, false},
		{"nor in a shell", "echo '$HOME' prints", `echo "$HOME" prints`, false},
		{"a minus sign and an en dash", "x \U00002212 1 on pages 3\U000020135 \U00002014 as said", "x - 1 on pages 3-5 - as said", true},

		// A word cut at a line's end may be quoted whole.
		{"a word cut at a line's end, quoted whole", "Elimi-\nnation produces", "Elimination produces", true},
		{"a word cut at a line's end, quoted as it is", "Elimi-\nnation produces", "Elimi-\nnation produces", true},
		{"a word cut at a line's end, quoted with its hyphen", "Elimi-\nnation produces", "Elimi-nation produces", true},
		{"spaces around the line break", "Elimi- \n  nation produces", "Elimination produces", true},
		{"a Unicode hyphen at a line's end", "Elimi\U00002010\nnation produces", "Elimination produces", true},
		{"a compound cut at its hyphen", "a well-\nknown result", "a well-known result", true},
		{"two words cut, one of them a compound", "a well-\nknown elimi-\nnation step", "a well-known elimination step", true},
		{"the second half of a cut word alone", "Elimi-\nnation produces", "nation produces", false},
		{"the first half of a cut word alone", "after Elimi-\nnation produces", "after Elimi", false},
		{"the first half of a cut word, with its hyphen", "after Elimi-\nnation produces", "after Elimi-", false},
		{"the second half of a cut word, from its hyphen", "after Elimi-\nnation produces", "-\nnation produces", false},
		{"a hyphen that cuts nothing ends a quote", "the range 3- and no more", "the range 3-", true},
		{"a hyphen whose next line begins with no letter stays", "word-\n-word", "word-word", false},
		{"and is quoted with what follows it", "word-\n-word", "word- -word", true},
		{"a hyphen followed by a space and no line break stays", "Elimi- nation produces", "Elimination produces", false},
		{"a hyphen left hanging stays", "pre- and post-processing", "pre and post-processing", false},
		{"a hyphen after one letter stays", "choose n-\nk of them", "choose nk of them", false},
		{"a word cut after its capital and one letter", "Re-\nmember the pivots", "Remember the pivots", true},
		{"a hyphen after capitals stays", "the AB-\ntest ran", "the ABtest ran", false},
		{"a hyphen after one capital stays", "an X-\nray of it", "an Xray of it", false},
		{"a hyphen with nothing after it but a mark", "the pivot- , and", "the pivot , and", false},
		{"a hyphen inside a line stays", "a well-known result", "a wellknown result", false},
		{"a minus at a line's end stays", "a -\nb is the difference", "a b is the difference", false},
		{"a hyphen before a digit stays", "see x-\n1 below", "see x1 below", false},
		{"a longer dash at a line's end stays", "the word\U00002014\nand more", "the wordand more", false},

		// A script written without spaces: a line break may be left out,
		// and a quote begins and ends anywhere.
		{"a line break inside such a script", "\U00006F22\U00005B57\n\U0000306E\U00006587\U00007AE0", "\U00006F22\U00005B57\U0000306E\U00006587\U00007AE0", true},
		{"a part of such a text", "\U00006F22\U00005B57\U0000306E\U00006587\U00007AE0", "\U0000306E\U00006587", true},

		// Everything else is compared as it is.
		{"another letter case", "Elimination produces an upper triangular system.", "elimination produces", false},
		{"a paraphrase", "Elimination produces an upper triangular system.", "Elimination gives an upper triangular system.", false},
		{"a word left out", "Elimination produces an upper triangular system.", "Elimination produces an triangular system.", false},
		{"words in another order", "Elimination produces an upper triangular system.", "an upper triangular system produces", false},
		{"another punctuation mark", "the pivots, on the diagonal", "the pivots; on the diagonal", false},
		{"a mark left out", "the pivots, on the diagonal", "the pivots on the diagonal", false},
		{"two sentences joined with an ellipsis", "One thing. Another thing. A third.", "One thing. \U00002026 A third.", false},
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
		// The end of a Passage is white space: the last word of one and the
		// first of the next are two words.
		{"two words at the ends of two Passages made one", []string{one, two}, "an uppertriangular system.", false, -1},
		{"an article and a word made one across Passages", []string{"Let A be a", "symmetric matrix."}, "be asymmetric matrix.", false, -1},
		{"two numbers made one across Passages", []string{"in rows 1", "2 and 3"}, "rows 12 and", false, -1},
		{"the same with the space there", []string{"Let A be a", "symmetric matrix."}, "be a symmetric matrix.", true, -1},
		{"a script without spaces across Passages", []string{"\U00006F22\U00005B57", "\U0000306E\U00006587\U00007AE0"}, "\U00005B57\U0000306E", true, -1},
		{"a hyphen at the end of a Passage before a capital stays", []string{"the Gauss-", "Jordan method"}, "the GaussJordan method", false, -1},
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
	// From the beginning of one sentence to the end of another: a quote
	// begins and ends where a word does.
	from := strings.Index(text[1000:], "Sentence") + 1000
	to := strings.Index(text[3500:], "here.") + 3500 + len("here.")
	quote := text[from:to]
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
		texts[i] = strings.Repeat("a ", 16<<10)
	}
	quote := strings.TrimSpace(strings.Repeat("a ", maxQuoteRunes/2))
	began := time.Now()
	found, alone := quoteIn(quote, texts)
	// It is in each Passage alone, and so does not run through them all.
	if found || alone != 0 {
		t.Errorf("quoteIn = %v, %d; want the quote found in the first Passage alone", found, alone)
	}
	// A comparison at every place takes several times this, and the one
	// that is made a fraction of a second.
	if took := time.Since(began); took > 5*time.Second {
		t.Errorf("the check took %s", took)
	}
}
