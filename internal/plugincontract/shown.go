package plugincontract

import "unicode"

// ShowsNothing reports whether r is a character that shows nothing wherever
// it is printed, alone or beside any other, and changes nothing of what is
// shown around it: a format character, a variation selector, or another of
// the code points Unicode has every display ignore. The characters of the
// tag block are among them, and so are the zero-width ones.
//
// Such a character holds what a program reads and a person does not: a
// sentence can be spelled in tags, or in variation selectors, a byte to
// each. study leaves them out of the words a plugin wrote for the learner,
// a reason, a notice and an error's message, since those words reach the
// learner's agent as well. A plugin that looks for something in the words
// it repeats, a key say, looks in them without these characters: as study
// will show them.
//
// Two kinds of format character are none of these. The marks that turn the
// direction of text show nothing themselves and change what is shown
// around them: study writes them out as escapes, like a control character
// (unicode.Bidi_Control). And a sign that is written before the digits it
// belongs to, an Arabic number sign say, is a format character that shows.
//
// BrailleBlank, below, is not one of them either: it takes room.
func ShowsNothing(r rune) bool {
	if unicode.Is(unicode.Bidi_Control, r) || unicode.Is(unicode.Prepended_Concatenation_Mark, r) {
		return false
	}
	return unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Variation_Selector, r) ||
		unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r)
}

// BrailleBlank is the braille pattern with no dot raised. It shows as a
// blank that takes room, and is neither white space to Unicode nor one of
// the characters a display ignores. study takes it for white space in the
// words a plugin wrote, and a plugin that looks for something in the words
// it repeats does the same first: what stands behind this blank stands
// behind a space where study shows it. It is the one blank of its kind that
// is common: this is no claim that no other character shows as one.
const BrailleBlank rune = 0x2800
