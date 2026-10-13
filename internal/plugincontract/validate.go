package plugincontract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// What the contract's names and ids look like. study chooses a collection's
// name and a Source's id, and a plugin chooses a Passage's id; each is safe
// as a file's name, on a command line and on a terminal.
var (
	// A collection's name follows the rule of a Topic's id.
	namePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	// A Source's id is lowercase letters, digits, hyphens and dots, and
	// starts with a letter or a digit.
	sourceIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*$`)
	// A Passage's id is letters, digits, dots, underscores, colons and
	// hyphens, and does not start with a hyphen.
	passageIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:][A-Za-z0-9._:-]*$`)
	hashPattern      = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	// A kind, of Source, of search or of location, is a lowercase word.
	kindPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

// CheckCollection checks a collection's name: lowercase letters, digits and
// single hyphens, up to MaxNameBytes characters.
func CheckCollection(name string) error {
	if name == "" {
		return errors.New("collection is missing")
	}
	if len(name) > MaxNameBytes || !namePattern.MatchString(name) {
		return fmt.Errorf("collection %s is not lowercase letters, digits and single hyphens, up to %d characters", quote(name), MaxNameBytes)
	}
	return nil
}

// CheckSourceID checks a Source's id: lowercase letters, digits, hyphens and
// dots, starting with a letter or a digit, up to MaxIDBytes characters.
func CheckSourceID(id string) error {
	if id == "" {
		return errors.New("the Source's id is missing")
	}
	if len(id) > MaxIDBytes || !sourceIDPattern.MatchString(id) {
		return fmt.Errorf("%s is not a Source's id: lowercase letters, digits, hyphens and dots, starting with a letter "+
			"or a digit, up to %d characters", quote(id), MaxIDBytes)
	}
	return nil
}

// CheckPassageID checks a Passage's id: letters, digits, dots, underscores,
// colons and hyphens, not starting with a hyphen, up to MaxIDBytes
// characters.
func CheckPassageID(id string) error {
	if id == "" {
		return errors.New("the Passage's id is missing")
	}
	if len(id) > MaxIDBytes || !passageIDPattern.MatchString(id) {
		return fmt.Errorf("%s is not a Passage's id: letters, digits, dots, underscores, colons and hyphens, not starting "+
			"with a hyphen, up to %d characters", quote(id), MaxIDBytes)
	}
	return nil
}

// CheckHash checks a content hash: "sha256:" and 64 lowercase hexadecimal
// digits.
func CheckHash(hash string) error {
	if !hashPattern.MatchString(hash) {
		return fmt.Errorf("%s is not a content hash: sha256: and 64 lowercase hexadecimal digits", quote(hash))
	}
	return nil
}

// quote shows a value that came from the other side in a message: quoted,
// with anything a terminal would interpret escaped, and cut short.
func quote(s string) string {
	const most = 80
	if len(s) > most {
		s = Cut(s, most) + "…"
	}
	return strconv.QuoteToASCII(s)
}

// Cut returns s cut to at most max bytes, at the end of a character.
func Cut(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}

// NewerVersionError says that a plugin speaks a contract version newer than
// this package describes.
type NewerVersionError struct {
	// Version is the version the plugin reported, as it wrote the number: a
	// version can be larger than any number this program counts with.
	Version string
}

func (e *NewerVersionError) Error() string {
	return fmt.Sprintf("the plugin speaks version %s of the contract, and this is version %d", e.Version, Version)
}

// Decode reads the JSON object of a tool's input or result into v, a
// pointer to one of the contract's types, and holds it to that type's shape
// (Shape): every field the contract requires is there, under exactly its
// name. Fields the contract does not define are passed over, so a plugin
// may say more than study asks.
//
// The error may repeat a key as the JSON spelled it, which is the text of
// whoever wrote the JSON: clean it before it is shown.
func Decode(data []byte, v any) error {
	if err := decode(data, v); err != nil {
		return err
	}
	if problems := Shape(data, reflect.TypeOf(v).Elem()); len(problems) > 0 {
		return errors.New(problems[0])
	}
	return nil
}

// decode reads a JSON object into v as encoding/json does: a field that is
// missing is left as it was, and a name in another letter case is taken for
// the field's.
func decode(data []byte, v any) error {
	if trimmed := bytes.TrimSpace(data); len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("it is not a JSON object")
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("it is not the JSON the contract describes: %s", jsonProblem(err))
	}
	return nil
}

// jsonProblem says what is wrong with a JSON document without repeating
// what the document holds.
func jsonProblem(err error) string {
	var wrongType *json.UnmarshalTypeError
	if errors.As(err, &wrongType) {
		// Where in a list the value is, is left out: versions of Go differ on
		// whether they say, and the field's name is what a plugin's author
		// looks for.
		field := strings.Join(slices.DeleteFunc(strings.Split(wrongType.Field, "."), func(part string) bool {
			_, err := strconv.Atoi(part)
			return err == nil
		}), ".")
		return fmt.Sprintf("%s is a %s, not a %s", field, wrongType.Value, wrongType.Type)
	}
	return "it is not valid JSON"
}

// ParseContract reads what the contract tool gave back. The version is read
// before anything else is judged: a newer contract, whatever its shape, is a
// *NewerVersionError and not a broken answer.
func ParseContract(data []byte) (Contract, error) {
	var head struct {
		Version json.RawMessage `json:"version"`
	}
	if err := decode(data, &head); err != nil {
		return Contract{}, err
	}
	// The version is read as the number it is, however large and however
	// written: 2, 2.0, 1e30 and a number past what an int holds are all
	// newer than this version, not broken answers.
	said := string(bytes.TrimSpace(head.Version))
	version, err := strconv.ParseFloat(said, 64)
	switch {
	case len(head.Version) == 0:
		return Contract{}, errors.New("version is missing")
	case err != nil && !errors.Is(err, strconv.ErrRange), math.IsNaN(version), version != math.Trunc(version), version < 1:
		return Contract{}, errors.New("version is not a whole number of 1 or more")
	case version > Version:
		if len(said) > 40 {
			said = said[:40] + "…" // a number is digits: any byte is a place to cut
		}
		return Contract{}, &NewerVersionError{Version: said}
	}
	// The rest is read apart from the version, which was read above: 1.0
	// and 1e0 are version 1 too.
	var rest struct {
		Contract
		Version json.RawMessage `json:"version"`
	}
	if err := decode(data, &rest); err != nil {
		return Contract{}, err
	}
	// Each field is there under its own name, the three lists as lists: an
	// empty one says that the plugin does nothing of the kind, and a missing
	// one says nothing. The version was judged above, however it is written.
	if problems := Shape(data, reflect.TypeFor[struct {
		Version   json.RawMessage `json:"version"`
		Sources   []string        `json:"sources"`
		Search    []string        `json:"search"`
		Locations []string        `json:"locations"`
	}]()); len(problems) > 0 {
		return Contract{}, errors.New(problems[0])
	}
	c := rest.Contract
	c.Version = int(version)
	if err := c.Validate(); err != nil {
		return Contract{}, err
	}
	return c, nil
}

// Validate checks what a plugin says it can do. A kind this version does not
// know is kept: kinds are added without a new contract version.
func (c Contract) Validate() error {
	if c.Version < 1 || c.Version > Version {
		return fmt.Errorf("version is %d, not a version from 1 to %d", c.Version, Version)
	}
	for _, list := range []struct {
		name  string
		kinds []string
		known []string
	}{
		{"sources", c.Sources, []string{SourceFile, SourceURL}},
		{"search", c.Search, []string{SearchKeyword, SearchEmbedding}},
		{"locations", c.Locations, nil},
	} {
		if len(list.kinds) > MaxKinds {
			return fmt.Errorf("%s lists more than %d kinds", list.name, MaxKinds)
		}
		for i, kind := range list.kinds {
			if len(kind) > MaxKindBytes || !kindPattern.MatchString(kind) {
				return fmt.Errorf("%s[%d] is %s, not a kind: a lowercase word of letters, digits and underscores, up to %d "+
					"characters", list.name, i, quote(kind), MaxKindBytes)
			}
			if slices.Contains(list.kinds[:i], kind) {
				return fmt.Errorf("%s lists %s twice", list.name, kind)
			}
		}
		if list.known != nil && !slices.ContainsFunc(list.kinds, func(kind string) bool { return slices.Contains(list.known, kind) }) {
			return fmt.Errorf("%s lists none of %s", list.name, strings.Join(list.known, ", "))
		}
	}
	return nil
}

// Validate checks a Source as the index tool is given it.
func (s Source) Validate() error {
	if err := CheckSourceID(s.ID); err != nil {
		return err
	}
	switch {
	case strings.TrimSpace(s.Title) == "":
		return errors.New("source.title is missing")
	case !printableText(s.Title, false) || utf8.RuneCountInString(s.Title) > MaxTitleRunes:
		return fmt.Errorf("source.title is not text of up to %d characters without control characters", MaxTitleRunes)
	}
	switch s.Kind {
	case SourceFile:
		switch {
		case s.URL != "" || s.Refetch:
			return errors.New("a file Source has a path and a hash, and neither url nor refetch")
		case s.Path == "":
			return errors.New("source.path is missing")
		case !strings.HasPrefix(s.Path, "/") || len(s.Path) > MaxPathBytes || !utf8.ValidString(s.Path) || strings.ContainsRune(s.Path, 0):
			return fmt.Errorf("source.path is not an absolute path of up to %d bytes of UTF-8", MaxPathBytes)
		case s.Hash == "":
			return errors.New("source.hash is missing")
		}
		if err := CheckHash(s.Hash); err != nil {
			return fmt.Errorf("source.hash: %w", err)
		}
	case SourceURL:
		if s.Path != "" || s.Hash != "" {
			return errors.New("a url Source has a url, and neither path nor hash")
		}
		if err := checkURL(s.URL); err != nil {
			return err
		}
	case "":
		return errors.New("source.kind is missing")
	default:
		return fmt.Errorf("source.kind is %s, not %s or %s", quote(s.Kind), SourceFile, SourceURL)
	}
	return nil
}

// checkURL checks the address of a web page: an absolute http or https URL
// with a host and without a user name or password.
func checkURL(raw string) error {
	if raw == "" {
		return errors.New("source.url is missing")
	}
	u, err := url.Parse(raw)
	if err != nil || len(raw) > MaxPathBytes || !printableText(raw, false) || strings.ContainsAny(raw, " \t") ||
		(u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return fmt.Errorf("source.url is not an http or https address of up to %d bytes without a user name or password", MaxPathBytes)
	}
	return nil
}

// printableText reports whether s is valid UTF-8 without control characters
// or the characters that change the direction text is shown in. Line breaks
// and tabs are let through when lines says so.
func printableText(s string, lines bool) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if lines && (r == '\n' || r == '\t') {
			continue
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			return false
		}
	}
	return true
}

// Validate checks what the index tool is given.
func (in IndexInput) Validate() error {
	if err := CheckCollection(in.Collection); err != nil {
		return err
	}
	return in.Source.Validate()
}

// Validate checks what the status tool is given.
func (in StatusInput) Validate() error { return CheckCollection(in.Collection) }

// Validate checks what the cancel tool is given.
func (in CancelInput) Validate() error {
	if err := CheckCollection(in.Collection); err != nil {
		return err
	}
	return CheckSourceID(in.Source)
}

// Validate checks what the search tool is given.
func (in SearchInput) Validate() error {
	if err := CheckCollection(in.Collection); err != nil {
		return err
	}
	switch {
	case strings.TrimSpace(in.Query) == "":
		return errors.New("query is missing")
	case len(in.Query) > MaxQueryBytes || !printableText(in.Query, true):
		return fmt.Errorf("query is not text of up to %d bytes of UTF-8 without control characters", MaxQueryBytes)
	case in.Limit < 1 || in.Limit > MaxSearchLimit:
		return fmt.Errorf("limit is %d, not a number from 1 to %d", in.Limit, MaxSearchLimit)
	case in.Sources != nil && len(in.Sources) == 0:
		return errors.New("sources is empty: leave it out to search the whole collection")
	case len(in.Sources) > MaxSources:
		return fmt.Errorf("sources lists more than %d Sources", MaxSources)
	}
	seen := make(map[string]bool, len(in.Sources))
	for i, id := range in.Sources {
		if err := CheckSourceID(id); err != nil {
			return fmt.Errorf("sources[%d]: %w", i, err)
		}
		if seen[id] {
			return fmt.Errorf("sources lists %s twice", id)
		}
		seen[id] = true
	}
	return nil
}

// Validate checks what the passage tool is given.
func (in PassageInput) Validate() error {
	if err := CheckCollection(in.Collection); err != nil {
		return err
	}
	return CheckPassageID(in.ID)
}

// Validate checks what a plugin says of one Source.
func (s SourceStatus) Validate() error {
	if err := CheckSourceID(s.ID); err != nil {
		return err
	}
	switch s.State {
	case StateIndexed:
		if s.Hash == "" {
			return fmt.Errorf("Source %s is indexed and has no hash", s.ID)
		}
	case StateIndexing:
	case StateFailed:
		if strings.TrimSpace(s.Reason) == "" {
			return fmt.Errorf("Source %s failed and has no reason", s.ID)
		}
	case StateOutdated:
		// Found as it is, so of some content, and to be indexed again for
		// a reason the learner is shown.
		switch {
		case s.Hash == "":
			return fmt.Errorf("Source %s is outdated and has no hash", s.ID)
		case strings.TrimSpace(s.Reason) == "":
			return fmt.Errorf("Source %s is outdated and has no reason", s.ID)
		}
	case "":
		return fmt.Errorf("Source %s has no state", s.ID)
	default:
		return fmt.Errorf("the state of Source %s is %s, not %s, %s, %s or %s", s.ID, quote(s.State), StateIndexed, StateIndexing,
			StateFailed, StateOutdated)
	}
	if s.Hash != "" {
		if err := CheckHash(s.Hash); err != nil {
			return fmt.Errorf("the hash of Source %s: %w", s.ID, err)
		}
	}
	if p := s.Progress; p != nil && (math.IsNaN(*p) || *p < 0 || *p > 1) {
		return fmt.Errorf("the progress of Source %s is not a number from 0 to 1", s.ID)
	}
	return nil
}

// Validate checks the index tool's answer to in.
func (r IndexResult) Validate(in IndexInput) error {
	if err := r.Source.Validate(); err != nil {
		return err
	}
	if r.Source.ID != in.Source.ID {
		return fmt.Errorf("it speaks of Source %s, and Source %s was to be indexed", r.Source.ID, in.Source.ID)
	}
	if r.Source.State == StateOutdated {
		// Asked to index it, a plugin is at work on it, has it indexed, or
		// failed: what was outdated was just asked to be brought up to date.
		return fmt.Errorf("it says Source %s is %s, which a Source that was just asked to be indexed is not", r.Source.ID, StateOutdated)
	}
	return nil
}

// Validate checks the status tool's answer.
func (r StatusResult) Validate() error {
	if len(r.Sources) > MaxSources {
		return fmt.Errorf("it lists more than %d Sources", MaxSources)
	}
	seen := make(map[string]bool, len(r.Sources))
	for _, s := range r.Sources {
		if err := s.Validate(); err != nil {
			return err
		}
		if seen[s.ID] {
			return fmt.Errorf("it lists Source %s twice", s.ID)
		}
		seen[s.ID] = true
	}
	return nil
}

// Validate checks a Passage.
func (p Passage) Validate() error {
	if err := CheckPassageID(p.ID); err != nil {
		return err
	}
	if err := CheckSourceID(p.Source); err != nil {
		return fmt.Errorf("Passage %s: %w", p.ID, err)
	}
	switch {
	case strings.TrimSpace(p.Text) == "":
		return fmt.Errorf("Passage %s has no text", p.ID)
	case len(p.Text) > MaxPassageBytes:
		return fmt.Errorf("the text of Passage %s is longer than %d bytes", p.ID, MaxPassageBytes)
	}
	if l := p.Location; l != nil {
		if len(l.Kind) > MaxKindBytes || !kindPattern.MatchString(l.Kind) {
			return fmt.Errorf("the location of Passage %s has the kind %s, which is not a kind: a lowercase word", p.ID, quote(l.Kind))
		}
		if l.Kind == LocationPages && (l.First < 1 || l.Last < l.First) {
			return fmt.Errorf("the pages of Passage %s are %d to %d, not a first page of 1 or more and a last page that is "+
				"not before it", p.ID, l.First, l.Last)
		}
	}
	return nil
}

// Validate checks the search tool's answer to in: no more Passages than were
// asked for, none twice, none from a Source the search was not limited to,
// and the best first.
//
// What a notice says, and at what length, is not checked here, as a
// reason's text is not: it is words for the learner, and an answer is not
// refused for them. study's client cleans them and cuts them to
// MaxNoticeBytes.
func (r SearchResult) Validate(in SearchInput) error {
	if len(r.Passages) > in.Limit {
		return fmt.Errorf("it holds %d Passages, and %d were asked for", len(r.Passages), in.Limit)
	}
	seen := make(map[string]bool, len(r.Passages))
	for i, p := range r.Passages {
		if err := p.Passage.Validate(); err != nil {
			return err
		}
		switch {
		case seen[p.ID]:
			return fmt.Errorf("it holds Passage %s twice", p.ID)
		case in.Sources != nil && !slices.Contains(in.Sources, p.Source):
			return fmt.Errorf("Passage %s is from Source %s, which the search was not limited to", p.ID, p.Source)
		case math.IsNaN(p.Score) || math.IsInf(p.Score, 0):
			return fmt.Errorf("the score of Passage %s is not a number", p.ID)
		case i > 0 && p.Score > r.Passages[i-1].Score:
			return fmt.Errorf("Passage %s has a higher score than the one before it: the best comes first", p.ID)
		}
		seen[p.ID] = true
	}
	return nil
}

// Validate checks the passage tool's answer to in.
func (r PassageResult) Validate(in PassageInput) error {
	if err := r.Passage.Validate(); err != nil {
		return err
	}
	if r.Passage.ID != in.ID {
		return fmt.Errorf("it is Passage %s, and Passage %s was asked for", r.Passage.ID, in.ID)
	}
	return nil
}

// Validate checks the diagnose tool's answer: no more findings than an
// answer may hold, each under a name of its own that is a lowercase word,
// and each with a message. How long a message is, is not checked: study
// keeps the first MaxFindingBytes of it.
func (r DiagnoseResult) Validate() error {
	if len(r.Findings) > MaxFindings {
		return fmt.Errorf("it holds %d findings, more than the %d an answer may hold", len(r.Findings), MaxFindings)
	}
	seen := make(map[string]bool, len(r.Findings))
	for i, f := range r.Findings {
		switch {
		case f.Name == "":
			return fmt.Errorf("findings[%d] has no name", i)
		case len(f.Name) > MaxKindBytes || !kindPattern.MatchString(f.Name):
			return fmt.Errorf("findings[%d] is named %s, which is no name: a lowercase word of letters, digits and underscores, "+
				"up to %d characters", i, quote(f.Name), MaxKindBytes)
		case seen[f.Name]:
			return fmt.Errorf("it holds the finding %s twice", f.Name)
		case strings.TrimSpace(f.Message) == "":
			return fmt.Errorf("the finding %s has no message", f.Name)
		case !Shows(f.Message):
			// study drops what shows nothing from a plugin's words: the
			// learner would read a name and no sentence.
			return fmt.Errorf("the finding %s has a message that shows nothing", f.Name)
		}
		seen[f.Name] = true
	}
	return nil
}
