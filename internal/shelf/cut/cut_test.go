package cut_test

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/cut"
)

func TestCleanTidiesWhatAReaderExtracted(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"a word divided at the end of a line is joined":   {"the solu\x02tion of the recurrence", "the solution of the recurrence"},
		"the older mark for a divided word is joined too": {"a tech\uFFFEnique", "a technique"},
		"the mark is a formula's character elsewhere":     {"A[1 .. n\x02, we sort", "A[1 .. n , we sort"},
		"and before a capital letter":                     {"see\x02Table 2", "see Table 2"},
		"a ligature becomes its letters":                  {"the \uFB01nal e\uFB00ect of a\uFB04uent \uFB02ow in an o\uFB03ce", "the final effect of affluent flow in an office"},
		"control characters leave a space":                {"T .n/ D\x05 2\x00x", "T .n/ D  2 x"},
		"a soft hyphen shows nothing":                     {"exam\u00ADple", "example"},
		"a byte order mark shows nothing":                 {"\uFEFFThe page", "The page"},
		"line endings are line feeds":                     {"one\r\ntwo\rthree", "one\ntwo\nthree"},
		"no line ends in white space":                     {"one  \ntwo\t\n", "one\ntwo"},
		"one empty line between paragraphs":               {"\n\none\n\n\n\ntwo\n\n", "one\n\ntwo"},
		"accents are composed":                            {"cafe\u0301 Mo\u0308bius", "caf\u00E9 M\u00F6bius"},
		"invalid UTF-8 leaves a space":                    {"caf\xE9 au lait", "caf  au lait"},
		"private-use characters are kept":                 {"a \uE000 b", "a \uE000 b"},
		"indentation is kept":                             {"func f() {\n    return\n}", "func f() {\n    return\n}"},
		"and so is what needs no tidying":                 {"Elimination produces an upper triangular system.", "Elimination produces an upper triangular system."},
	} {
		if got := cut.Clean(tc.in); got != tc.want {
			t.Errorf("%s: Clean(%q) = %q, want %q", name, tc.in, got, tc.want)
		}
	}
}

// Clean never writes a word the text does not hold: cleaned twice, a text is
// what it was cleaned once.
func TestCleanChangesNothingTheSecondTime(t *testing.T) {
	for _, text := range []string{"the solu\x02tion", "one\r\n\r\n\r\ntwo  ", "e\uFB03cient\x07 work", "", "\n\n"} {
		once := cut.Clean(text)
		if twice := cut.Clean(once); twice != once {
			t.Errorf("Clean(Clean(%q)) = %q, and Clean gave %q", text, twice, once)
		}
	}
}

// words are the words of a text, whatever the white space between them.
func words(text string) []string { return strings.Fields(text) }

func sentences(r *rand.Rand, n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "Sentence %d of this page says %d things about row %d", i+1, r.IntN(90)+10, r.IntN(9000))
		if r.IntN(4) == 0 {
			b.WriteString(", and a little more besides, to make its length differ from the others")
		}
		b.WriteString(".")
		switch r.IntN(6) {
		case 0:
			b.WriteString("\n\n")
		case 1:
			b.WriteString("\n")
		default:
			b.WriteString(" ")
		}
	}
	return cut.Clean(b.String())
}

// Every word of every page is in exactly one Passage, in the order of the
// text, and no Passage is longer than it may be, or on pages that are not
// its own.
func TestPassagesHoldEveryWordOnceInOrder(t *testing.T) {
	r := rand.New(rand.NewPCG(83, 12))
	for trial := range 60 {
		size := []int{200, 400, 1500, 3000}[trial%4]
		var pages []cut.Page
		var all []string
		for n := range r.IntN(8) + 1 {
			text := ""
			if r.IntN(6) != 0 { // one page in six is empty
				text = sentences(r, r.IntN(80)+1)
			}
			pages = append(pages, cut.Page{Number: n + 1, Text: text})
			all = append(all, words(text)...)
		}
		passages := cut.Passages(pages, size)
		var got []string
		lastFirst := 0
		for i, p := range passages {
			got = append(got, words(p.Text)...)
			switch {
			case p.Text == "" || p.Text != strings.TrimSpace(p.Text):
				t.Fatalf("trial %d: Passage %d is empty or has white space around it: %q", trial, i, p.Text)
			case utf8.RuneCountInString(p.Text) > cut.Longest(size):
				t.Fatalf("trial %d: Passage %d holds %d characters, and the longest for size %d is %d", trial, i,
					utf8.RuneCountInString(p.Text), size, cut.Longest(size))
			case p.First < 1 || p.Last < p.First || p.Last > p.First+1 || p.Last > len(pages):
				t.Fatalf("trial %d: Passage %d is said to be on pages %d to %d of %d", trial, i, p.First, p.Last, len(pages))
			case p.First < lastFirst:
				t.Fatalf("trial %d: Passage %d begins on page %d, after one that begins on page %d", trial, i, p.First, lastFirst)
			}
			lastFirst = p.First
			// It is on the pages it says: each of its words is a word of one
			// of them.
			on := strings.Join([]string{pages[p.First-1].Text, pages[p.Last-1].Text}, "\n")
			if !strings.Contains(strings.Join(words(on), " "), strings.Join(words(p.Text), " ")) && p.First == p.Last {
				t.Fatalf("trial %d: Passage %d is not a stretch of page %d:\n%s", trial, i, p.First, p.Text)
			}
		}
		if strings.Join(got, " ") != strings.Join(all, " ") {
			t.Fatalf("trial %d (size %d): the Passages do not hold the pages' words once each, in order:\n got %d words\nwant %d words",
				trial, size, len(got), len(all))
		}
	}
}

func TestAPassageIsAboutTheSizeAskedFor(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	page := sentences(r, 400)
	passages := cut.Passages([]cut.Page{{Number: 1, Text: page}}, 1500)
	if len(passages) < 10 {
		t.Fatalf("a page of %d characters was cut into %d Passages", utf8.RuneCountInString(page), len(passages))
	}
	for i, p := range passages {
		n := utf8.RuneCountInString(p.Text)
		// None is longer than a third over the size, and none but the last
		// shorter than half of it: a page is not cut into scraps.
		if n > 2000 || (i < len(passages)-1 && n < 700) {
			t.Errorf("Passage %d of %d holds %d characters, want about 1500", i+1, len(passages), n)
		}
		// Each ends where a sentence does.
		if !strings.HasSuffix(p.Text, ".") {
			t.Errorf("Passage %d does not end with a sentence: …%q", i+1, p.Text[len(p.Text)-40:])
		}
	}
}

// A Passage ends between two paragraphs when it can, and at the end of a
// sentence otherwise.
func TestAPassageEndsWhereTheTextDoes(t *testing.T) {
	paragraph := func(letter string, n int) string {
		return strings.TrimSpace(strings.Repeat(letter+letter+letter+" sentence. ", n))
	}
	// 1,000 characters, an empty line, and 1,200 more: the break between the
	// paragraphs is in reach, and is taken before any sentence's end.
	text := paragraph("a", 77) + "\n\n" + paragraph("b", 92)
	passages := cut.Passages([]cut.Page{{Number: 4, Text: text}}, 1500)
	if len(passages) != 2 || strings.Contains(passages[0].Text, "bbb") || strings.Contains(passages[1].Text, "aaa") {
		t.Fatalf("two paragraphs were cut into %d Passages, the first of %d characters: want one for each", len(passages), len(passages[0].Text))
	}
	if passages[0].First != 4 || passages[0].Last != 4 || passages[1].First != 4 {
		t.Errorf("the Passages are on pages %d-%d and %d-%d, want page 4", passages[0].First, passages[0].Last, passages[1].First, passages[1].Last)
	}
}

func TestAPageThatFitsIsOnePassage(t *testing.T) {
	passages := cut.Passages([]cut.Page{
		{Number: 1, Text: "The first page is short."},
		{Number: 2, Text: ""},
		{Number: 3, Text: "So is the third."},
	}, 1500)
	if len(passages) != 2 || passages[0].Text != "The first page is short." || passages[0].First != 1 || passages[0].Last != 1 ||
		passages[1].Text != "So is the third." || passages[1].First != 3 || passages[1].Last != 3 {
		t.Errorf("Passages = %+v, want one for each page that has text, on that page", passages)
	}
}

// A sentence that runs over the end of a page is kept whole: the Passage it
// begins in takes the rest of it, and is on both pages.
func TestASentenceOverAPageBreakStaysWhole(t *testing.T) {
	pages := []cut.Page{
		{Number: 46, Text: "Elimination is the first step. The pivots are on the diagonal of the\n46"},
		{Number: 47, Text: "triangle after elimination. Back substitution is the second step."},
		{Number: 48, Text: "The third page starts a sentence of its own."},
	}
	passages := cut.Passages(pages, 1500)
	if len(passages) != 3 {
		t.Fatalf("Passages = %+v, want three", passages)
	}
	first := passages[0]
	if !strings.Contains(strings.Join(words(first.Text), " "), "The pivots are on the diagonal of the 46 triangle after elimination.") {
		t.Errorf("the first Passage does not hold the sentence that runs over the page: %q", first.Text)
	}
	if first.First != 46 || first.Last != 47 {
		t.Errorf("the first Passage is on pages %d to %d, want 46 to 47", first.First, first.Last)
	}
	// What it took is not said twice, and the page after ends a sentence, so
	// its Passage keeps to it.
	if second := passages[1]; second.Text != "Back substitution is the second step." || second.First != 47 || second.Last != 47 {
		t.Errorf("the second Passage = %+v, want the rest of page 47, on page 47", second)
	}
	if third := passages[2]; third.First != 48 || third.Last != 48 {
		t.Errorf("the third Passage is on pages %d to %d, want 48", third.First, third.Last)
	}
}

func TestAPassageKeepsToItsPageWhenItCan(t *testing.T) {
	long := strings.Repeat("no sentence ends in the first lines of this page ", 12) // 600 characters without a stop
	for name, pages := range map[string][]cut.Page{
		"the page ends a sentence":             {{Number: 1, Text: "A page that ends well."}, {Number: 2, Text: "Another sentence. And more."}},
		"it ends a question in quotes":         {{Number: 1, Text: "She asked, \"Why?\""}, {Number: 2, Text: "Nobody knew. Nobody said."}},
		"it ends before a list":                {{Number: 1, Text: "The steps are these:"}, {Number: 2, Text: "First, eliminate. Then substitute."}},
		"the next page ends no sentence soon":  {{Number: 1, Text: "A page that stops in the middle of"}, {Number: 2, Text: long}},
		"the next page is empty":               {{Number: 1, Text: "A page that stops in the middle of"}, {Number: 2, Text: ""}, {Number: 3, Text: "a sentence. And more."}},
		"the next page is not the page after":  {{Number: 1, Text: "A page that stops in the middle of"}, {Number: 3, Text: "a sentence. And more."}},
		"a web page has no pages to run over":  {{Number: 0, Text: "A page that stops in the middle of"}, {Number: 0, Text: "a sentence. And more."}},
		"the page number at its foot is there": {{Number: 1, Text: "A page that ends well.\n\n17"}, {Number: 2, Text: "Another sentence. And more."}},
	} {
		for i, p := range cut.Passages(pages, 1500) {
			if p.First != p.Last {
				t.Errorf("%s: Passage %d is on pages %d to %d: %q", name, i, p.First, p.Last, p.Text)
			}
		}
	}
}

func TestAWebPageHasNoPages(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	passages := cut.Passages([]cut.Page{{Text: sentences(r, 200)}}, 1500)
	if len(passages) < 3 {
		t.Fatalf("a long web page was cut into %d Passages", len(passages))
	}
	for _, p := range passages {
		if p.First != 0 || p.Last != 0 {
			t.Errorf("a Passage of a web page is said to be on pages %d to %d", p.First, p.Last)
		}
	}
}

// Text that gives no place to cut, one word without end or a script written
// without spaces, is cut all the same, and no Passage is longer in bytes
// than the contract lets a Passage be, at any size a caller may ask for.
func TestNoPassageIsLongerThanTheContractAllows(t *testing.T) {
	for name, text := range map[string]string{
		"one word without end":       strings.Repeat("x", 40000),
		"a script without spaces":    strings.Repeat("\u6F22\u5B57\u306E\u6587\u7AE0", 8000),
		"characters of four bytes":   strings.Repeat("\U0001D54F ", 20000),
		"lines and no sentences":     strings.Repeat("a line of a table without a full stop\n", 2000),
		"sentences without a space":  strings.Repeat("3.14159.2.71828.", 3000),
		"nothing but a short line":   "x",
		"paragraphs of one word":     strings.Repeat("word\n\n", 5000),
		"a very long first sentence": strings.Repeat("and so on and so forth ", 2000) + ". The end.",
	} {
		for _, size := range []int{1, cut.MinSize, cut.DefaultSize, cut.MaxSize, 1 << 30} {
			pages := []cut.Page{{Number: 1, Text: text}, {Number: 2, Text: text}}
			passages := cut.Passages(pages, size)
			if len(passages) == 0 {
				t.Errorf("%s, size %d: no Passages", name, size)
			}
			count := 0
			for _, p := range passages {
				count += utf8.RuneCountInString(strings.Join(words(p.Text), ""))
				if len(p.Text) > plugincontract.MaxPassageBytes {
					t.Errorf("%s, size %d: a Passage of %d bytes, and the contract allows %d", name, size, len(p.Text), plugincontract.MaxPassageBytes)
				}
				if n := utf8.RuneCountInString(p.Text); n > cut.Longest(size) {
					t.Errorf("%s, size %d: a Passage of %d characters, and the longest is %d", name, size, n, cut.Longest(size))
				}
			}
			if want := 2 * utf8.RuneCountInString(strings.Join(words(text), "")); count != want {
				t.Errorf("%s, size %d: the Passages hold %d characters of text, and the pages %d", name, size, count, want)
			}
		}
	}
}

func TestTheSamePagesAreCutTheSameWay(t *testing.T) {
	r := rand.New(rand.NewPCG(9, 9))
	pages := []cut.Page{{Number: 1, Text: sentences(r, 150)}, {Number: 2, Text: sentences(r, 150)}}
	first, second := cut.Passages(pages, 1500), cut.Passages(pages, 1500)
	if fmt.Sprint(first) != fmt.Sprint(second) {
		t.Error("the same pages were cut into other Passages the second time: a Passage's id is made of its text and its place")
	}
}

func TestNoPagesNoPassages(t *testing.T) {
	if got := cut.Passages(nil, 1500); len(got) != 0 {
		t.Errorf("Passages(nil) = %+v", got)
	}
	if got := cut.Passages([]cut.Page{{Number: 1, Text: " \n\t "}}, 1500); len(got) != 0 {
		t.Errorf("a page of white space gave %+v", got)
	}
}
