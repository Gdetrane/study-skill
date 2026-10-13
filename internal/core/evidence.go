package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
	"github.com/mordor-forge/lamplight/v2/internal/pluginregistry"
)

const (
	eventEvidenceRecorded = "evidence.recorded"
	// eventEvidencePassagesRecorded records Evidence that was checked
	// against the Passages it quotes. The Passages make another payload
	// than a quote alone, so it is another type: an evidence.recorded Event
	// carries Evidence nobody checked, as it always has.
	eventEvidencePassagesRecorded = "evidence_passages.recorded"
	eventEvidenceRetracted        = "evidence.retracted"

	maxQuoteRunes = 4000 // an Evidence quote
	maxPlaceRunes = 200  // an Evidence location
)

// Where an Evidence location came from.
const (
	// LocationFromSource: read in the Source itself, such as a printed page
	// number or a section heading.
	LocationFromSource = "source"
	// LocationFromKnowledgeBase: reported by the Knowledge base with the
	// Passage. Evidence recorded with its Passages has its location from
	// them, and no other: pages counted from the start of the file.
	LocationFromKnowledgeBase = "knowledge_base"
	// LocationFromLearner: given by the learner.
	LocationFromLearner = "learner"
	// LocationFromEstimate: the agent's estimate, which may be off.
	LocationFromEstimate = "estimate"
)

func init() {
	eventKinds[eventEvidenceRecorded] = eventKind{apply: applyNothing, replay: replayEvidenceRecorded}
	eventKinds[eventEvidencePassagesRecorded] = eventKind{apply: applyNothing, replay: replayEvidencePassagesRecorded}
	eventKinds[eventEvidenceRetracted] = eventKind{apply: applyNothing, replay: replayEvidenceRetracted}
}

// Evidence is an exact quote from a Source, with its location when known,
// cited by a Lesson. It lives in the History only.
type Evidence struct {
	ID     string `json:"id"`
	Lesson string `json:"lesson"`
	Source string `json:"source"`
	Quote  string `json:"quote"`
	// Location is where the quote is in the Source, such as "p. 42" or
	// "§3.2", when known; LocationFrom says how it is known.
	Location     string `json:"location,omitempty"`
	LocationFrom string `json:"location_from,omitempty"`
	// Checked says that the quote was checked when it was recorded: it was
	// in the text of the Passages below, as the Topic's Knowledge base plugin
	// gave them then. Evidence recorded without a Passage is not checked.
	Checked bool `json:"checked"`
	// Passages are the ids of the Passages the quote was checked against, in
	// the order it runs through them, and Plugin the name of the Knowledge
	// base plugin that gave them. The ids are that plugin's, on the computer
	// that recorded the Evidence: they say what was checked, and need not
	// name anything elsewhere, or once the Source is indexed anew.
	Passages []string `json:"passages,omitempty"`
	Plugin   string   `json:"plugin,omitempty"`
	// Recorded is when the Evidence was recorded, by the recording
	// computer's clock.
	Recorded time.Time `json:"recorded"`
	// Retracted is set once the Evidence was taken back. Retracted Evidence
	// is listed only on request, and no Lesson cites it any more.
	Retracted bool `json:"retracted,omitempty"`
}

// evidenceData is the payload of an evidence.recorded Event.
type evidenceData struct {
	ID           string `json:"id"`
	Lesson       string `json:"lesson"`
	Source       string `json:"source"`
	Quote        string `json:"quote"`
	Location     string `json:"location,omitempty"`
	LocationFrom string `json:"location_from,omitempty"`
}

// evidencePassagesData is the payload of an evidence_passages.recorded
// Event: Evidence whose quote was checked against Passages of the Topic's
// Knowledge base plugin.
type evidencePassagesData struct {
	ID     string `json:"id"`
	Lesson string `json:"lesson"`
	Source string `json:"source"`
	Quote  string `json:"quote"`
	// Passages are the ids of the Passages the quote is in, in order, and
	// Plugin the name of the plugin that gave them.
	Passages []string `json:"passages"`
	Plugin   string   `json:"plugin"`
	// Location is where the Passages are in the Source, when the plugin
	// said. It came from the Knowledge base, always.
	Location string `json:"location,omitempty"`
}

// evidenceRetractedData is the payload of an evidence.retracted Event.
type evidenceRetractedData struct {
	ID string `json:"id"`
}

// EvidenceSpec describes Evidence to record.
type EvidenceSpec struct {
	Topic string
	// Lesson is the id of the Lesson that cites the Evidence.
	Lesson string
	// Source is the id of the Source the quote comes from.
	Source string
	// Quote is the exact text, as the Source has it.
	Quote string
	// Passages are the ids of the Passages the quote is from, as a search
	// gave them, in the order the quote runs through them:
	// MaxEvidencePassages at most. With them the quote is checked against
	// their text, and the location is theirs. Without, nothing is checked.
	Passages     []string
	Location     string
	LocationFrom string
	DryRun       bool
}

// EvidenceResult is the result of RecordEvidence and RetractEvidence.
type EvidenceResult struct {
	Topic    string   `json:"topic"`
	Evidence Evidence `json:"evidence"`
	// Changed is false when the same Evidence was already recorded, or
	// already retracted.
	Changed bool `json:"changed"`
	DryRun  bool `json:"dry_run,omitempty"`
}

// EvidenceQuery selects Evidence to list.
type EvidenceQuery struct {
	Topic string
	// Lesson, when set, lists only what that Lesson cites.
	Lesson string
	// All includes retracted Evidence.
	All bool
}

// EvidenceList is the Evidence recorded in a Topic.
type EvidenceList struct {
	Topic string `json:"topic"`
	// Lesson is set when the list was asked for one Lesson.
	Lesson   string     `json:"lesson,omitempty"`
	Evidence []Evidence `json:"evidence"`
}

var evidenceIDPattern = regexp.MustCompile(`^[a-z0-9]{1,26}$`)

// RecordEvidence records an exact quote from one of the Topic's Sources that
// a Lesson cites. Recording Evidence that is already recorded, and not
// retracted, records nothing.
//
// With the ids of the Passages the quote is from, it is checked: study asks
// the Topic's Knowledge base plugin for those Passages again, refuses a
// quote that is not in their text (quote.go says what that allows), and
// takes the location from them (recordQuoted). Without a Passage the core
// checks nothing: it never parses a document.
func (c *Core) RecordEvidence(ctx context.Context, spec EvidenceSpec) (EvidenceResult, error) {
	if err := checkTopicID(spec.Topic); err != nil {
		return EvidenceResult{}, err
	}
	if err := checkLessonID(spec.Lesson); err != nil {
		return EvidenceResult{}, err
	}
	if !sourceIDPattern.MatchString(spec.Source) {
		return EvidenceResult{}, invalidf("%q is not a Source id: study source list shows them", spec.Source)
	}
	quote, err := cleanQuote(spec.Quote)
	if err != nil {
		return EvidenceResult{}, err
	}
	if len(spec.Passages) > 0 {
		return c.recordQuoted(ctx, spec, quote)
	}
	location, err := cleanText("location", spec.Location, maxPlaceRunes)
	if err != nil {
		return EvidenceResult{}, err
	}
	from := strings.TrimSpace(spec.LocationFrom)
	switch {
	case location == "" && from != "":
		return EvidenceResult{}, invalidf("location_from says where a location came from: give the location too")
	case from == LocationFromKnowledgeBase:
		// What the Knowledge base says of a location it says of a Passage,
		// and Evidence recorded with the Passage takes it from there: said
		// by hand it would be a claim nothing checked.
		return EvidenceResult{}, invalidf("a location from the Knowledge base comes with its Passage: record the Evidence "+
			"with the Passage's id, which gives the location, or say where else the location is from: %s, %s or %s",
			LocationFromSource, LocationFromLearner, LocationFromEstimate)
	case location != "" && !validLocationFrom(from):
		return EvidenceResult{}, invalidf("say where the location came from: %s, %s or %s",
			LocationFromSource, LocationFromLearner, LocationFromEstimate)
	}
	want := evidenceData{Lesson: spec.Lesson, Source: spec.Source, Quote: quote, Location: location, LocationFrom: from}

	var existing *Evidence
	ev, err := c.writeTopic(ctx, spec.Topic, func(s *replayed, _ *topicView) (*change, error) {
		k := s.knowledge()
		if _, ok := k.sources[spec.Source]; !ok {
			return nil, noSuchSource(spec.Topic, spec.Source)
		}
		for i, e := range k.evidence {
			// Evidence that was checked against its Passages is another
			// record than the same quote unchecked.
			if !e.Retracted && !e.Checked && e.Lesson == want.Lesson && e.Source == want.Source && e.Quote == want.Quote &&
				e.Location == want.Location && e.LocationFrom == want.LocationFrom {
				existing = &k.evidence[i]
				return nil, nil
			}
		}
		want.ID = c.newEvidenceID(k)
		return &change{Type: eventEvidenceRecorded, Data: want}, nil
	}, spec.DryRun)
	if err != nil {
		return EvidenceResult{}, err
	}
	result := EvidenceResult{Topic: spec.Topic, DryRun: spec.DryRun}
	switch {
	case existing != nil:
		result.Evidence = *existing
	case ev != nil:
		result.Evidence = evidenceOf(want, ev.Wall)
		result.Changed = true
	}
	return result, nil
}

// recordQuoted records Evidence with the Passages it quotes, once the quote
// was found in their text.
//
// The plugin is asked before the Topic's lock is taken, and stopped before
// anything is written: a plugin has most of a minute to answer, and no other
// write waits for it. The folder that was read is the one written to, so a
// Topic removed or replaced meanwhile receives nothing. A dry run asks the
// plugin too, and checks the quote: it says what the real run does.
//
// It fails, and records nothing, with invalid_argument for a quote that is
// not in the Passages, and for a Passage of another Source; with not_found
// for a Passage the plugin does not have; and with failed_precondition when
// the Topic names no plugin, when the plugin cannot be had or does not
// answer in time, and when it does not hold the Source indexed as the Topic
// records it.
func (c *Core) recordQuoted(ctx context.Context, spec EvidenceSpec, quote string) (EvidenceResult, error) {
	const unchecked = "record the Evidence without a Passage, which is then not checked"
	if len(spec.Passages) > MaxEvidencePassages {
		return EvidenceResult{}, invalidf("Evidence quotes from at most %d Passages: a quote that runs through more is "+
			"two quotes", MaxEvidencePassages)
	}
	for i, id := range spec.Passages {
		if plugincontract.CheckPassageID(id) != nil {
			return EvidenceResult{}, invalidf("%q is not a Passage's id: study evidence search %s <query> gives them", id, spec.Topic)
		}
		if slices.Contains(spec.Passages[:i], id) {
			return EvidenceResult{}, invalidf("Passage %s is named twice: name each Passage the quote runs through once, "+
				"in order", id)
		}
	}
	if strings.TrimSpace(spec.Location) != "" || strings.TrimSpace(spec.LocationFrom) != "" {
		return EvidenceResult{}, invalidf("Evidence recorded with a Passage takes its location from the Passage: leave " +
			"the location, and where it came from, out")
	}
	switch folded := foldQuote(quote); {
	case !quotable(folded):
		return EvidenceResult{}, invalidf("the quote holds no word to check: quote the words of the Passage that the Lesson " +
			"relies on")
	case len(folded) > maxQuoteRunes:
		return EvidenceResult{}, invalidf("the quote is longer than %d characters once its ligatures and the like are "+
			"written out: quote the passage that matters", maxQuoteRunes)
	}
	if err := ctx.Err(); err != nil {
		return EvidenceResult{}, err
	}
	home, topic, err := c.openTopicFolder(spec.Topic)
	if err != nil {
		return EvidenceResult{}, err
	}
	defer home.Close()
	defer topic.Close()
	kb, err := topicKnowledgeBase(&topicView{root: topic}, spec.Topic)
	if err != nil {
		return EvidenceResult{}, err
	}
	name, ok := pluginOf(kb)
	if !ok {
		return EvidenceResult{}, &Error{Code: CodeFailedPrecondition, Message: "Topic " + spec.Topic + " names no Knowledge " +
			"base plugin, so there is no Passage to check a quote against: " + unchecked}
	}
	h, err := readHistory(topic, spec.Topic)
	if err != nil {
		return EvidenceResult{}, err
	}
	read := replayHistory(h)
	// A Topic this version cannot write is refused before its plugin is
	// started for nothing.
	if err := refuseNewer(spec.Topic, read); err != nil {
		return EvidenceResult{}, err
	}
	src, known := read.knowledge().sources[spec.Source]
	if !known {
		return EvidenceResult{}, noSuchSource(spec.Topic, spec.Source)
	}

	p, err := c.startTopicPlugin(ctx, spec.Topic, name, "give the Passages the quote is from",
		"Nothing was recorded. Once that is put right, record the Evidence again, or "+unchecked)
	if err != nil {
		return EvidenceResult{}, err
	}
	passages, err := p.quotedPassages(ctx, *src, spec.Passages, c.fileStateIn(home, spec.Topic))
	p.stop()
	if err != nil {
		return EvidenceResult{}, err
	}
	// Stopped while the plugin was being stopped, a command says so: a dry
	// run plans nothing after it, and says no "would record".
	if err := ctx.Err(); err != nil {
		return EvidenceResult{}, err
	}
	texts := make([]string, len(passages))
	locations := make([]*plugincontract.Location, len(passages))
	for i, passage := range passages {
		texts[i], locations[i] = passage.Text, passage.Location
	}
	if found, alone := quoteIn(quote, texts); !found {
		return EvidenceResult{}, notInItsPassages(spec.Passages, alone)
	}
	want := evidencePassagesData{Lesson: spec.Lesson, Source: spec.Source, Quote: quote, Passages: spec.Passages, Plugin: name,
		Location: placeOf(locations...)}

	// The plugin's time is this operation's time: waiting for another
	// process that writes the Topic has what is left of it, and no time of
	// its own on top.
	within, cancel := context.WithDeadline(ctx, p.deadline)
	defer cancel()
	// The same quote, as a quote is compared: white space of another kind
	// or length makes no second piece of Evidence.
	same := string(foldQuote(quote))
	var existing *Evidence
	ev, err := c.writeOpenedTopic(within, home, topic, spec.Topic, func(s *replayed, _ *topicView) (*change, error) {
		k := s.knowledge()
		if _, ok := k.sources[spec.Source]; !ok {
			return nil, noSuchSource(spec.Topic, spec.Source)
		}
		for i, e := range k.evidence {
			if !e.Retracted && e.Checked && e.Lesson == want.Lesson && e.Source == want.Source &&
				string(foldQuote(e.Quote)) == same && e.Plugin == want.Plugin && slices.Equal(e.Passages, want.Passages) {
				existing = &k.evidence[i]
				return nil, nil
			}
		}
		want.ID = c.newEvidenceID(k)
		return &change{Type: eventEvidencePassagesRecorded, Data: want}, nil
	}, spec.DryRun)
	if err != nil {
		return EvidenceResult{}, err
	}
	result := EvidenceResult{Topic: spec.Topic, DryRun: spec.DryRun}
	switch {
	case existing != nil:
		result.Evidence = *existing
	case ev != nil:
		result.Evidence = quotedEvidenceOf(want, ev.Wall)
		result.Changed = true
	}
	return result, nil
}

// noSuchSource is the error for Evidence from a Source the Topic does not
// have.
func noSuchSource(topicID, sourceID string) error {
	return &Error{Code: CodeNotFound, Message: fmt.Sprintf(
		"Topic %s has no Source %s: add it with study source add first", topicID, sourceID)}
}

// notInItsPassages is the error for a quote that is not in the text of the
// Passages it was recorded with. alone is the place of the one Passage that
// holds it whole, among several, or -1.
func notInItsPassages(passages []string, alone int) error {
	const allowed = "It begins and ends where a word of the Passage does. How much white space there is and of which kind, " +
		"a word cut at a line's end, a letter's accent written apart, and the shape of a quotation mark or the length of a " +
		"dash may differ, and nothing else: not a word, not a space added or left out, not a letter's case"
	switch {
	case alone >= 0:
		return invalidf("the quote is all in Passage %s, and does not run through the other Passages named: record it "+
			"with that Passage alone", passages[alone])
	case len(passages) == 1:
		return invalidf("the quote is not in the text of Passage %s, so nothing was recorded. Evidence is the Source's own "+
			"words: quote the Passage as the search gave it, never a paraphrase or a summary. %s. A quote that runs on "+
			"into the next Passage names both, in order", passages[0], allowed)
	}
	return invalidf("the quote is not in the texts of Passages %s read one after the other, beginning in the first and "+
		"ending in the last, so nothing was recorded. Evidence is the Source's own words: quote the Passages as the search "+
		"gave them, never a paraphrase or a summary. %s", strings.Join(passages, ", "), allowed)
}

// RetractEvidence takes Evidence back, such as a quote recorded by mistake.
// It is recorded, never deleted; retracting it again changes nothing.
func (c *Core) RetractEvidence(ctx context.Context, topicID, evidenceID string, dryRun bool) (EvidenceResult, error) {
	if err := checkTopicID(topicID); err != nil {
		return EvidenceResult{}, err
	}
	if !evidenceIDPattern.MatchString(evidenceID) {
		return EvidenceResult{}, invalidf("%q is not an Evidence id: study evidence list shows them", evidenceID)
	}
	var found Evidence
	ev, err := c.writeTopic(ctx, topicID, func(s *replayed, _ *topicView) (*change, error) {
		k := s.knowledge()
		i, ok := k.byID[evidenceID]
		if !ok {
			return nil, &Error{Code: CodeNotFound, Message: fmt.Sprintf(
				"Topic %s has no Evidence %s: study evidence list %s --all shows it all", topicID, evidenceID, topicID)}
		}
		found = k.evidence[i]
		if found.Retracted {
			return nil, nil
		}
		return &change{Type: eventEvidenceRetracted, Data: evidenceRetractedData{ID: evidenceID}}, nil
	}, dryRun)
	if err != nil {
		return EvidenceResult{}, err
	}
	if ev != nil {
		found.Retracted = true
	}
	return EvidenceResult{Topic: topicID, Evidence: found, Changed: ev != nil, DryRun: dryRun}, nil
}

// ListEvidence lists the Evidence recorded in a Topic, in the order it was
// recorded.
func (c *Core) ListEvidence(ctx context.Context, q EvidenceQuery) (EvidenceList, error) {
	if q.Lesson != "" {
		if err := checkLessonID(q.Lesson); err != nil {
			return EvidenceList{}, err
		}
	}
	home, topic, err := c.openTopicFolder(q.Topic)
	if err != nil {
		return EvidenceList{}, err
	}
	defer home.Close()
	defer topic.Close()
	if err := ctx.Err(); err != nil {
		return EvidenceList{}, err
	}
	h, err := readHistory(topic, q.Topic)
	if err != nil {
		return EvidenceList{}, err
	}
	list := EvidenceList{Topic: q.Topic, Lesson: q.Lesson, Evidence: []Evidence{}}
	for _, e := range replayHistory(h).knowledge().evidence {
		if (q.Lesson == "" || e.Lesson == q.Lesson) && (q.All || !e.Retracted) {
			list.Evidence = append(list.Evidence, e)
		}
	}
	return list, nil
}

func evidenceOf(d evidenceData, recorded time.Time) Evidence {
	return Evidence{ID: d.ID, Lesson: d.Lesson, Source: d.Source, Quote: d.Quote,
		Location: d.Location, LocationFrom: d.LocationFrom, Recorded: recorded.UTC()}
}

// quotedEvidenceOf is the Evidence an evidence_passages.recorded Event
// records: checked, with a location that came from the Knowledge base when
// it has one.
func quotedEvidenceOf(d evidencePassagesData, recorded time.Time) Evidence {
	e := Evidence{ID: d.ID, Lesson: d.Lesson, Source: d.Source, Quote: d.Quote, Location: d.Location, Checked: true,
		Passages: d.Passages, Plugin: d.Plugin, Recorded: recorded.UTC()}
	if e.Location != "" {
		e.LocationFrom = LocationFromKnowledgeBase
	}
	return e
}

// newEvidenceID makes a short random id for Evidence, different from every
// id the History knows.
func (c *Core) newEvidenceID(k *knowledgeState) string {
	for {
		var id strings.Builder
		for _, r := range strings.ToLower(c.newID()) {
			if id.Len() == 10 {
				break
			}
			if r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
				id.WriteRune(r)
			}
		}
		if _, taken := k.byID[id.String()]; !taken && evidenceIDPattern.MatchString(id.String()) {
			return id.String()
		}
	}
}

func validLocationFrom(from string) bool {
	switch from {
	case LocationFromSource, LocationFromKnowledgeBase, LocationFromLearner, LocationFromEstimate:
		return true
	}
	return false
}

// checkLessonID checks a Lesson id. Lessons belong to the Syllabus (#25); the
// Knowledge seam only stores the id, so it checks its form.
func checkLessonID(id string) error {
	if strings.TrimSpace(id) == "" {
		return invalidf("name the Lesson that cites the Evidence")
	}
	if len(id) > 64 || !topicIDPattern.MatchString(id) {
		return invalidf("%q is not a valid Lesson id: use lowercase letters, digits and single hyphens, up to 64 characters", id)
	}
	return nil
}

// cleanQuote checks an exact quote. It may span lines, so newlines and tabs
// are kept; invalid UTF-8 and other control characters are refused, and so
// are characters that are not shown: a direction override makes a quote
// read as another than it is, and a tag character holds text that only a
// program reads. The three a script needs to be written stay: the soft
// hyphen and the two joiners.
func cleanQuote(s string) (string, error) {
	if !utf8.ValidString(s) {
		return "", invalidf("the quote is not valid UTF-8 text")
	}
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
	if s == "" {
		return "", invalidf("give the exact quote, as the Source has it")
	}
	for _, r := range s {
		switch {
		case unicode.IsControl(r) && r != '\n' && r != '\t':
			return "", invalidf("the quote contains a control character")
		case r == '\u00AD' || r == '\u200C' || r == '\u200D':
		case unicode.Is(unicode.Cf, r) || isTag(r) || r == '\u2028' || r == '\u2029':
			return "", invalidf("the quote contains a character that is not shown (U+%04X): quote the words as they are read, "+
				"without it", r)
		}
	}
	if utf8.RuneCountInString(s) > maxQuoteRunes {
		return "", invalidf("the quote is longer than %d characters: quote the passage that matters", maxQuoteRunes)
	}
	return s, nil
}

// citingLessons returns the Syllabus's Lessons that should cite Evidence:
// those started or done, in Syllabus order, once the Topic has Sources to
// cite. Lessons not started yet are left out, so status does not mark the
// whole Syllabus.
func (s *replayed) citingLessons() []string {
	syllabus := s.study.syllabus
	if syllabus == nil || len(s.knowledge().sources) == 0 {
		return nil
	}
	var lessons []string
	for _, m := range syllabus.Milestones {
		for _, l := range m.Lessons {
			if st := s.study.lessons[l.ID]; !l.Skipped && st != nil && (st.phase != "" || st.completed != nil) {
				lessons = append(lessons, l.ID)
			}
		}
	}
	return lessons
}

// lessonsWithoutEvidence returns the lessons, of those given, that cite no
// Evidence, retracted Evidence aside. Lessons without Evidence are marked in
// status, never blocked.
func (s *replayed) lessonsWithoutEvidence(lessons []string) []string {
	cited := map[string]bool{}
	for _, e := range s.knowledge().evidence {
		if !e.Retracted {
			cited[e.Lesson] = true
		}
	}
	var out []string
	for _, l := range lessons {
		if !cited[l] {
			out = append(out, l)
		}
	}
	return out
}

func replayEvidenceRecorded(s *replayed, ev event) error {
	var d evidenceData
	if err := json.Unmarshal(ev.Data, &d); err != nil || d.ID == "" || d.Lesson == "" || d.Source == "" || d.Quote == "" {
		return errors.New("its payload is not Evidence")
	}
	k := s.knowledge()
	if _, ok := k.sources[d.Source]; !ok {
		return fmt.Errorf("%w: Source %s", errUnknownItem, d.Source)
	}
	if _, ok := k.byID[d.ID]; ok {
		return nil
	}
	k.byID[d.ID] = len(k.evidence)
	k.evidence = append(k.evidence, evidenceOf(d, ev.Wall))
	return nil
}

// replayEvidencePassagesRecorded replays Evidence that was checked against
// its Passages. It shares its ids with Evidence that was not, so one
// retraction takes back either, and a Card cites either.
//
// The payload is held to what makes it safe to show and no further: ids that
// are a Passage's, however many, and a name a plugin can have. How many
// Passages a quote may run through is a rule for whoever records, which a
// later version may change.
func replayEvidencePassagesRecorded(s *replayed, ev event) error {
	var d evidencePassagesData
	if err := json.Unmarshal(ev.Data, &d); err != nil || d.ID == "" || d.Lesson == "" || d.Source == "" || d.Quote == "" ||
		len(d.Passages) == 0 || pluginregistry.CheckName(d.Plugin) != nil ||
		slices.ContainsFunc(d.Passages, func(id string) bool { return plugincontract.CheckPassageID(id) != nil }) {
		return errors.New("its payload is not Evidence quoted from Passages")
	}
	k := s.knowledge()
	if _, ok := k.sources[d.Source]; !ok {
		return fmt.Errorf("%w: Source %s", errUnknownItem, d.Source)
	}
	if _, ok := k.byID[d.ID]; ok {
		return nil
	}
	k.byID[d.ID] = len(k.evidence)
	k.evidence = append(k.evidence, quotedEvidenceOf(d, ev.Wall))
	return nil
}

func replayEvidenceRetracted(s *replayed, ev event) error {
	var d evidenceRetractedData
	if err := json.Unmarshal(ev.Data, &d); err != nil || d.ID == "" {
		return errors.New("its payload names no Evidence")
	}
	k := s.knowledge()
	i, ok := k.byID[d.ID]
	if !ok {
		return fmt.Errorf("%w: Evidence %s", errUnknownItem, d.ID)
	}
	k.evidence[i].Retracted = true
	return nil
}
