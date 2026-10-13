package plugintest

import (
	"fmt"
	"strings"
)

// Hidden spells a sentence of ASCII in characters that show nothing, as a
// plugin would that had something to say to the learner's agent and not to
// the learner. It returns the sentence spelled each way there is, by the
// name of the way:
//
//   - in the characters of Unicode's tag block, one for each letter;
//   - in variation selectors, one for each byte;
//   - in zero-width characters, eight for each byte: a zero-width space for
//     a bit that is 0 and a zero-width non-joiner for one that is 1.
//
// A program reads each back as it was written. study leaves all of them out
// of a plugin's words (LeftOfHidden says whether it did).
func Hidden(sentence string) map[string]string {
	var tags, selectors, zeroWidth strings.Builder
	for i := 0; i < len(sentence); i++ {
		c := sentence[i]
		tags.WriteRune(0xE0000 + rune(c&0x7F))
		if c < 16 {
			selectors.WriteRune(0xFE00 + rune(c))
		} else {
			selectors.WriteRune(0xE0100 + rune(c) - 16)
		}
		for bit := 7; bit >= 0; bit-- {
			zeroWidth.WriteRune(0x200B + rune(c>>bit&1))
		}
	}
	return map[string]string{"tags": tags.String(), "variation selectors": selectors.String(), "zero-width characters": zeroWidth.String()}
}

// LeftOfHidden says what text still holds of a sentence that Hidden spelled
// into it: a character of the kinds Hidden writes, or an escape written for
// one, in any of the ways study and JSON write an escape. It is empty for
// text that holds neither: one that reads to a program as it reads to a
// person.
func LeftOfHidden(text string) string {
	for _, r := range text {
		if r >= 0xE0000 && r <= 0xE007F || r >= 0xFE00 && r <= 0xFE0F || r >= 0xE0100 && r <= 0xE01EF || r == 0x200B || r == 0x200C {
			return fmt.Sprintf("the character U+%04X, which shows nothing", r)
		}
	}
	// The escapes, in small letters: of a tag and of a late variation
	// selector as study and Go write one, with four digits or eight, and as
	// JSON writes the first half of either; of an early variation selector;
	// and of the two zero-width characters.
	lower := strings.ToLower(text)
	for _, escape := range []string{`\ue00`, `\ue01`, `\u000e0`, `\udb40`, `\ufe0`, fmt.Sprintf(`\u%04x`, 0x200B), fmt.Sprintf(`\u%04x`, 0x200C)} {
		if at := strings.Index(lower, escape); at >= 0 {
			return "the escape " + text[at:min(len(text), at+12)] + ", written for a character that shows nothing"
		}
	}
	return ""
}
