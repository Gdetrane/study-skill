package plugincontract_test

import (
	"strings"
	"testing"
	"unicode"

	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
)

// The characters that show nothing are the ones a sentence can be spelled
// in for a program alone: every format character but the marks that turn
// the direction of text and the signs that show, every variation selector,
// and every other code point a display ignores. Nothing that shows is one
// of them, and nothing a terminal would interpret: those are written out.
func TestWhatShowsNothing(t *testing.T) {
	for _, r := range []rune{
		0x00AD, 0x034F, 0x115F, 0x1160, 0x17B4, 0x17B5, 0x180B, 0x180D, 0x180E, 0x180F, 0x200B, 0x200C, 0x200D, 0x2060, 0x2064, 0x2065,
		0x206A, 0x206F, 0x3164, 0xFE00, 0xFE0F, 0xFEFF, 0xFFA0, 0xFFF0, 0xFFF9, 0xFFFB, 0x1BCA0, 0x1D173, 0x1D17A,
		0xE0000, 0xE0001, 0xE0020, 0xE0041, 0xE007F, 0xE0080, 0xE00FF, 0xE0100, 0xE01EF, 0xE01F0, 0xE0FFF,
	} {
		if !plugincontract.ShowsNothing(r) {
			t.Errorf("U+%04X is not said to show nothing", r)
		}
	}
	for _, r := range []rune{
		// What turns the direction of text, which is written out.
		0x061C, 0x200E, 0x200F, 0x202A, 0x202E, 0x2066, 0x2069,
		// Signs written before the digits they belong to, which show.
		0x0600, 0x0605, 0x06DD, 0x070F, 0x08E2, 0x110BD, 0x110CD,
		// Control characters, which are written out, and white space.
		0x00, 0x07, 0x1B, 0x7F, 0x85, 0x9B, '\n', '\t', ' ', 0x00A0, 0x2028, 0x3000,
		// What shows: letters, marks on them, signs, and a blank that takes
		// room; and what is nobody's to say of.
		'a', 'é', 0x0301, 0x65E5, 0x2800, 0x1F600, 0xE000, 0xFFFD, 0xE1000,
	} {
		if plugincontract.ShowsNothing(r) {
			t.Errorf("U+%04X is said to show nothing", r)
		}
	}
	// No character that shows nothing is white space or a control
	// character: whoever cleans a text decides those first, and the same.
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if plugincontract.ShowsNothing(r) && (unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r)) {
			t.Fatalf("U+%04X shows nothing, and is white space, a control character or a mark of direction too", r)
		}
	}
}

// The page says what becomes of the words a plugin writes for the learner:
// what a terminal would interpret is written out, and what shows nothing is
// left out, of a reason, a notice and an error's message alike.
func TestThePageSaysWhatBecomesOfAPluginsWords(t *testing.T) {
	doc := strings.Join(strings.Fields(readDoc(t)), " ")
	for _, want := range []string{
		"**Words a plugin writes for the learner reach the learner's agent too**: a `reason`, a `notice` and an error's `message`.",
		"**What a terminal would interpret is written out as an escape**",
		"**What shows nothing is left out**",
		"The tag block (U+E0000 to U+E007F) and the zero-width characters are among them.",
		"`study` puts it on one line, escapes what a terminal would interpret, leaves out the characters that show nothing, and shows the first 2,000 bytes.",
		"`study` puts it on one line, escapes what a terminal would interpret, leaves out the characters that show nothing, so that the agent reads of it what the learner reads",
		"a `notice` that is no text, or is `null`, breaks the contract as any field does",
		"an answer that is larger than a message may be is refused like any other",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("the page does not say: %s", want)
		}
	}
	// Nowhere does it still say that such a character is escaped.
	if strings.Contains(doc, "and the characters of Unicode's tag block, which show nothing and spell out text") {
		t.Error("the page still says that the tag block is escaped")
	}
}
