package core

import (
	"strings"
	"unicode"
)

// notAnAction holds first words that show a Next step is a description of
// where things stand rather than an action: articles, pronouns and
// determiners, and words that report a state ("done", "finished"). The list
// is English, and short on purpose; see checkNextStep.
var notAnAction = map[string]bool{
	"a": true, "an": true, "the": true, "this": true, "that": true, "these": true, "those": true,
	"my": true, "our": true, "your": true, "his": true, "her": true, "their": true, "its": true,
	"i": true, "we": true, "you": true, "he": true, "she": true, "it": true, "they": true,
	"there": true, "here": true, "some": true, "nothing": true, "none": true,
	"maybe": true, "perhaps": true, "probably": true, "still": true, "already": true, "almost": true,
	"next": true, "lesson": true, "step": true, "todo": true, "tbd": true, "wip": true,
	"done": true, "finished": true, "completed": true, "stopped": true, "was": true, "is": true,
}

// checkNextStep cleans a Next step and checks that it reads as an action
// that starts with a verb, such as "Fix the off-by-one in parse.go".
//
// The check is a heuristic with no language model behind it, chosen to
// catch the common mistakes without refusing good steps in any language:
// the step must start with a letter, have at least two words (a verb and
// what it acts on, so not "Continue"), and not start with a word that
// introduces a description ("The parser is half done", "I was on lesson 3",
// "Done with the parser"). That word list is English; a step in another
// language passes on the first two rules, and the tools ask agents for a
// verb in any case.
func checkNextStep(step string) (string, error) {
	step, err := requiredText("Next step", step, maxNextStepRunes)
	if err != nil {
		return "", err
	}
	words := strings.Fields(step)
	first := []rune(words[0])
	example := "start it with a verb and say what to act on, such as \"Fix the off-by-one in parse.go\""
	switch {
	case !unicode.IsLetter(first[0]):
		return "", invalidf("the Next step %q does not start with a word: %s", step, example)
	case len(words) < 2:
		return "", invalidf("the Next step %q is a single word: %s", step, example)
	case notAnAction[strings.ToLower(strings.TrimRight(words[0], ".,:;!?"))]:
		return "", invalidf("the Next step %q describes where things stand rather than what to do: %s", step, example)
	}
	return step, nil
}
