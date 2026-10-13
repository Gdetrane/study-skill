package pluginclient

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"

	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
)

// A plugin reached by URL is registered at an address, and what follows the
// host of it, a path or a query, may be a key. study shows the scheme and
// host and no more (Address), and text a service wrote is shown without the
// rest: a service that repeats the address it was asked at, in a refusal or
// a reason, would otherwise have study print what it never prints itself.
//
// What is looked for are the parts of the address (secrets): what follows
// the host, the path, the query, and each long piece of the two. They are
// looked for in the text as it reads, and not as it is spelled (readAs): a
// service writes an address in capitals, with its escapes read, or with
// more of them than the registry has, and it is the address all the same.
//
// All of them are looked for in one reading of the text, whatever their
// number (parts): a plugin is not trusted with study's time, and an address
// may hold hundreds of values.

// secrets are the parts of a registered address that may be a key, each as
// it reads (readAs): what follows the host, the path, the query, and each
// long piece of the two.
//
// A piece of a path counts from eight bytes and a value of a query from
// six, as it reads: shorter, it is a word of its own, "v1" or "mcp", and
// taking it out of a service's words wherever it stands would leave little
// of them. A key that short, said on its own and not as part of the
// address, is shown.
func secrets(registered string) []string {
	found := map[string]bool{}
	add := func(part string, least int) {
		if read, _ := readAs(part); len(read) >= least {
			found[read] = true
		}
	}
	u, err := url.Parse(registered)
	if err != nil {
		// No address study could have been registered with: all of it.
		add(registered, 1)
	} else {
		add(u.RequestURI(), 2)
		add(u.EscapedPath(), 2)
		add(u.RawQuery, 1)
		for _, piece := range strings.Split(u.EscapedPath(), "/") {
			add(piece, 8)
		}
		for _, pair := range strings.Split(u.RawQuery, "&") {
			_, value, _ := strings.Cut(pair, "=")
			add(value, 6)
		}
	}
	parts := make([]string, 0, len(found))
	for part := range found {
		parts = append(parts, part)
	}
	return parts
}

// readAs is a text as a part of an address is looked for in it, and for
// each byte of that the place in the text of what it was read from, with
// the length of the text after the last. Two texts that read alike say the
// same part of an address, however each spells it:
//
//   - A percent sign and two hexadecimal digits are the byte they stand
//     for, and so again when that byte makes another such: "%2573" is "%73"
//     is "s". Any byte may be written so, and not only the ones a URL has
//     to escape.
//   - A plus sign is a space, as a query writes one, and white space of
//     any kind and length is one space, as study shows it (clean).
//   - A letter is the same in any case, as Unicode folds case: a long s is
//     an s, and a sharp s is two. And it is the same with its accent
//     written onto it or apart. What one language does otherwise is not
//     read so: a Turkish capital with a dot is no i.
//   - What shows nothing is not there (plugincontract.ShowsNothing): a key
//     with a zero-width space inside it is the key.
func readAs(text string) (string, []int32) {
	// The bytes, with every escape read, and where each came from.
	bytes := make([]byte, 0, len(text))
	from := make([]int32, 0, len(text))
	for i := 0; i < len(text); i++ {
		bytes, from = append(bytes, text[i]), append(from, int32(i))
		for n := len(bytes); n >= 3 && bytes[n-3] == '%' && isHex(bytes[n-2]) && isHex(bytes[n-1]); n = len(bytes) {
			bytes[n-3] = unhex(bytes[n-2])<<4 | unhex(bytes[n-1])
			bytes, from = bytes[:n-2], from[:n-2]
		}
	}
	// The characters they make, each as its cases have it in common.
	read := make([]byte, 0, len(bytes))
	where := make([]int32, 0, len(bytes)+1)
	fold := cases.Fold()
	for i := 0; i < len(bytes); {
		r, size := utf8.DecodeRune(bytes[i:])
		before := len(read)
		switch {
		case r == utf8.RuneError && size <= 1:
			// A byte that is no text is itself.
			read = append(read, bytes[i])
		case plugincontract.ShowsNothing(r):
		case r == '+' || unicode.IsSpace(r) || r == plugincontract.BrailleBlank:
			if len(read) == 0 || read[len(read)-1] != ' ' {
				read = append(read, ' ')
			}
		case r < utf8.RuneSelf:
			if r >= 'A' && r <= 'Z' {
				r += 'a' - 'A'
			}
			read = append(read, byte(r))
		case unicode.SimpleFold(r) == r && norm.NFD.Properties(bytes[i:i+size]).Decomposition() == nil:
			// No letter with another case, and nothing an accent is
			// written onto: itself, as most of what is not ASCII is.
			read = append(read, bytes[i:i+size]...)
		default:
			read = append(read, norm.NFD.String(fold.String(norm.NFD.String(string(r))))...)
		}
		for range len(read) - before {
			where = append(where, from[i])
		}
		i += size
	}
	return string(read), append(where, int32(len(text)))
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func unhex(c byte) byte {
	switch {
	case c >= 'a':
		return c - 'a' + 10
	case c >= 'A':
		return c - 'A' + 10
	}
	return c - '0'
}

// parts finds the parts of one registered address in a text, all of them in
// one reading of it: a state for every beginning of a part, and for each
// state and each byte the state that follows, which is the longest
// beginning of a part the text then ends in. Reading a text costs its
// length and nothing for the number of parts. It is made once for a
// Connection.
type parts struct {
	// next is the state that follows each state on each byte. State 0 has
	// read nothing of any part.
	next [][256]int32
	// depth is how many bytes of a part a state has read, and ends the
	// length of the longest part that ends where the state is reached, 0
	// when none does.
	depth, ends []int32
}

// partsOf is the parts of a registered address, and nil for an address with
// none, as a command has.
func partsOf(address string) *parts {
	if address == "" {
		return nil
	}
	patterns := secrets(address)
	if len(patterns) == 0 {
		return nil
	}
	p := &parts{next: make([][256]int32, 1), depth: []int32{0}, ends: []int32{0}}
	for _, pattern := range patterns {
		state := int32(0)
		for i := 0; i < len(pattern); i++ {
			to := p.next[state][pattern[i]]
			if to == 0 {
				to = int32(len(p.next))
				p.next = append(p.next, [256]int32{})
				p.depth, p.ends = append(p.depth, p.depth[state]+1), append(p.ends, 0)
				p.next[state][pattern[i]] = to
			}
			state = to
		}
		p.ends[state] = p.depth[state]
	}
	// Where a state has no way on for a byte, it goes where the longest end
	// of what it has read that begins a part goes: its fallback, which is
	// nearer the start and so complete by the time it is needed.
	fallback := make([]int32, len(p.next))
	var queue []int32
	for _, to := range p.next[0] {
		if to != 0 {
			queue = append(queue, to)
		}
	}
	for len(queue) > 0 {
		state := queue[0]
		queue = queue[1:]
		p.ends[state] = max(p.ends[state], p.ends[fallback[state]])
		for c, to := range p.next[state] {
			if to != 0 {
				fallback[to] = p.next[fallback[state]][c]
				queue = append(queue, to)
			} else {
				p.next[state][c] = p.next[fallback[state]][c]
			}
		}
	}
	return p
}

// cutShort reports whether a byte of what was read may be of something a
// cut left unfinished at a text's end, and not read for what it stands for:
// a percent sign or a digit of an escape, of which escaping an escape makes
// as many as one likes, or a byte of a character without its last.
func cutShort(c byte) bool { return c == '%' || isHex(c) || c >= utf8.RuneSelf }

// without is text with every place where a part of the address stands, as
// it reads, replaced by "…". Parts that overlap, or stand side by side, are
// one such place.
//
// cut says that text is the start of a longer one. It may then end in the
// start of a part, which is no part and is not found: all of a key but its
// last byte. So whatever the end of such a text reads as that begins a
// part is left out, and so is what begins a part and is followed to the
// end only by what a cut may have left unfinished (cutShort).
func (p *parts) without(text string, cut bool) string {
	if p == nil || text == "" {
		return text
	}
	read, where := readAs(text)
	// The places, from and to, in what was read: in order, and apart.
	var places [][2]int
	// The state after each byte, for a text that was cut.
	var states []int32
	if cut {
		states = make([]int32, 1, len(read)+1)
	}
	state := int32(0)
	for i := 0; i < len(read); i++ {
		state = p.next[state][read[i]]
		if cut {
			states = append(states, state)
		}
		n := int(p.ends[state])
		if n == 0 {
			continue
		}
		from, to := i+1-n, i+1
		for len(places) > 0 && places[len(places)-1][1] >= from {
			from = min(from, places[len(places)-1][0])
			places = places[:len(places)-1]
		}
		places = append(places, [2]int{from, to})
	}
	end := len(text)
	if cut {
		kept := len(read)
		for at := len(read); ; at-- {
			if begun := int(p.depth[states[at]]); begun > 0 {
				kept = min(kept, at-begun)
			}
			if at == 0 || !cutShort(read[at-1]) {
				break
			}
		}
		end = int(where[kept])
	}
	if len(places) == 0 && end == len(text) {
		return text
	}
	var b strings.Builder
	done := 0
	for _, place := range places {
		from, to := int(where[place[0]]), int(where[place[1]])
		if from >= end {
			break
		}
		b.WriteString(text[done:from])
		b.WriteString("…")
		done = min(to, end)
	}
	if done < end {
		b.WriteString(text[done:end])
	}
	return b.String()
}

// withoutIn is a JSON document with the parts of the address taken out of
// every text in it, a name of a field included, and whether any was. It is
// for saying what is wrong with an answer: see Connection.wrong.
func (p *parts) withoutIn(data []byte) (shown []byte, changed bool) {
	if p == nil {
		return data, false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var document any
	if decoder.Decode(&document) != nil {
		return data, false
	}
	var walk func(any) any
	walk = func(value any) any {
		switch value := value.(type) {
		case string:
			said := p.without(value, false)
			changed = changed || said != value
			return said
		case []any:
			for i := range value {
				value[i] = walk(value[i])
			}
		case map[string]any:
			fields := make(map[string]any, len(value))
			for name, held := range value {
				fields[walk(name).(string)] = walk(held)
			}
			return fields
		}
		return value
	}
	shown, err := json.Marshal(walk(document))
	if err != nil || !changed {
		return data, false
	}
	return shown, true
}

// without takes the parts of a registered address that may be a key out of
// a text, for whoever has no Connection to ask (parts.without).
func without(text, address string) string { return partsOf(address).without(text, false) }
