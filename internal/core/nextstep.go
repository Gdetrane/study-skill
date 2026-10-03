package core

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// notAnAction holds first words that show a Next step is a description of
// where things stand rather than an action: articles, pronouns and
// determiners, and words that report a state ("done", "stuck"). The list is
// English, and short on purpose; see checkNextStep.
var notAnAction = map[string]bool{
	"a": true, "an": true, "the": true, "this": true, "that": true, "these": true, "those": true,
	"my": true, "our": true, "your": true, "his": true, "her": true, "their": true, "its": true,
	"i": true, "we": true, "you": true, "he": true, "she": true, "it": true, "they": true,
	"there": true, "here": true, "some": true, "all": true, "nothing": true, "none": true,
	"maybe": true, "perhaps": true, "probably": true, "still": true, "already": true, "almost": true,
	"halfway": true, "good": true, "great": true, "ok": true, "okay": true, "tbd": true, "wip": true,
	"done": true, "finished": true, "completed": true, "stopped": true, "ended": true, "stuck": true,
	"was": true, "is": true,
}

// minUnspacedRunes is how long a Next step must be in a script that does not
// separate words with spaces, where the two-word rule cannot apply.
const minUnspacedRunes = 4

// checkNextStep cleans a Next step and checks that it reads as an action
// that starts with a verb, such as "Fix the off-by-one in parse.go".
//
// The check is a heuristic with no language model behind it, chosen to
// catch the common mistakes without refusing good steps in any language.
// Opening punctuation and symbols (quotes, ¿, a backtick) are skipped; the
// step must then start with a letter, and say what to act on: at least two
// words, or, in scripts written without spaces between words (Chinese,
// Japanese, Thai and the like), at least four characters. Its first word,
// the leading run of letters so that "It's" counts as "it", must not
// introduce a description ("The parser is half done", "I'm on Lesson 3",
// "Done with the parser"). That word list is English; a step in another
// language passes on the other rules. Invisible formatting characters, such
// as zero-width spaces, are refused.
func checkNextStep(step string) (string, error) {
	step, err := requiredText("Next step", step, maxNextStepRunes)
	if err != nil {
		return "", err
	}
	if i := strings.IndexFunc(step, func(r rune) bool { return unicode.Is(unicode.Cf, r) }); i >= 0 {
		r, _ := utf8.DecodeRuneInString(step[i:])
		return "", invalidf("the Next step contains an invisible formatting character (U+%04X): type it again without it", r)
	}
	rest := strings.TrimLeftFunc(step, func(r rune) bool {
		return unicode.IsPunct(r) || unicode.IsSymbol(r) || unicode.IsSpace(r)
	})
	first, _ := utf8.DecodeRuneInString(rest)
	example := "start it with a verb and say what to act on, such as \"Fix the off-by-one in parse.go\""
	switch {
	case !unicode.IsLetter(first):
		return "", invalidf("the Next step %q does not start with a word: %s", step, example)
	case unspacedScript(first):
		if utf8.RuneCountInString(rest) < minUnspacedRunes {
			return "", invalidf("the Next step %q is too short to say what to do: %s", step, example)
		}
	case len(strings.Fields(rest)) < 2:
		return "", invalidf("the Next step %q is too short to say what to do: %s", step, example)
	case notAnAction[leadingWord(rest)]:
		return "", invalidf("the Next step %q describes where things stand rather than what to do: %s", step, example)
	}
	return step, nil
}

// leadingWord is the leading run of letters of s, lowercased, so a
// contraction ("It's", "I’m") counts by its first part.
func leadingWord(s string) string {
	end := strings.IndexFunc(s, func(r rune) bool { return !unicode.IsLetter(r) })
	if end < 0 {
		end = len(s)
	}
	return strings.ToLower(s[:end])
}

// unspacedScript reports whether r belongs to a script written without
// spaces between words.
func unspacedScript(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Thai, unicode.Lao,
		unicode.Khmer, unicode.Myanmar)
}
