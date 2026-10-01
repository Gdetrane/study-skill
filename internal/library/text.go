package library

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// stopWords are dropped by the normalizer. None of them is a topic name, and
// short language names such as "c", "r" and "go" must never be added here.
var stopWords = map[string]bool{
	"a": true, "an": true, "and": true, "at": true, "by": true,
	"for": true, "from": true, "in": true, "into": true, "of": true,
	"on": true, "or": true, "the": true, "to": true, "with": true,
}

// tokens is the one text normalizer of the package: it lowercases text and
// splits it into whole-word tokens, dropping stop words. A token is a run of
// letters, digits and combining marks; it keeps an inner "&" between two such
// runs (k&r) and a trailing run of "+" or "#" that ends the word (c++, c#).
func tokens(text string) []string {
	rs := []rune(strings.ToLower(text))
	var out []string
	for i := 0; i < len(rs); {
		if !isWordRune(rs[i]) {
			i++
			continue
		}
		j := i + 1
		for j < len(rs) {
			if isWordRune(rs[j]) || (rs[j] == '&' && j+1 < len(rs) && isWordRune(rs[j+1])) {
				j++
				continue
			}
			break
		}
		end := j
		for end < len(rs) && (rs[end] == '+' || rs[end] == '#') {
			end++
		}
		if end > j && end < len(rs) && isWordRune(rs[end]) {
			end = j // "a+b" is two words, not "a+" and "b"
		}
		if tok := string(rs[i:end]); !stopWords[tok] {
			out = append(out, tok)
		}
		i = end
	}
	return out
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r)
}

// cleanTitle turns a file or folder name into a readable title: dots,
// underscores and hyphens become spaces, whitespace collapses, and the result
// is title-cased.
func cleanTitle(name string) string {
	spaced := strings.Map(func(r rune) rune {
		switch r {
		case '.', '_', '-':
			return ' '
		}
		return r
	}, name)
	return titleCase(strings.Join(strings.Fields(spaced), " "))
}

// titleCase capitalises the first letter of each space-separated word and
// leaves the rest of the word alone, so "CS" stays "CS", "2nd" stays "2nd",
// "let's" becomes "Let's" and "c++" becomes "C++". (v1 used Python's
// str.title, which turned these into "Cs", "2Nd" and "Let'S".)
func titleCase(s string) string {
	words := strings.Split(s, " ")
	for i, word := range words {
		r, size := utf8.DecodeRuneInString(word)
		if size > 0 && unicode.IsLower(r) {
			words[i] = string(unicode.ToTitle(r)) + word[size:]
		}
	}
	return strings.Join(words, " ")
}
