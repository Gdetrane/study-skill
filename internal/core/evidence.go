package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	eventEvidenceRecorded  = "evidence.recorded"
	eventEvidenceRetracted = "evidence.retracted"

	maxQuoteRunes = 4000 // an Evidence quote
	maxPlaceRunes = 200  // an Evidence location
)

// Where an Evidence location came from.
const (
	// LocationFromSource: read in the Source itself, such as a printed page
	// number or a section heading.
	LocationFromSource = "source"
	// LocationFromKnowledgeBase: reported by the Knowledge base with the
	// passage, recorded as given.
	LocationFromKnowledgeBase = "knowledge_base"
	// LocationFromLearner: given by the learner.
	LocationFromLearner = "learner"
	// LocationFromEstimate: the agent's estimate, which may be off.
	LocationFromEstimate = "estimate"
)

func init() {
	eventKinds[eventEvidenceRecorded] = eventKind{apply: applyNothing, replay: replayEvidenceRecorded}
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
	Quote        string
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
// a Lesson cites. The core does not check the quote against the Source: it
// never parses documents. Recording Evidence that is already recorded, and
// not retracted, records nothing.
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
	location, err := cleanText("location", spec.Location, maxPlaceRunes)
	if err != nil {
		return EvidenceResult{}, err
	}
	from := strings.TrimSpace(spec.LocationFrom)
	switch {
	case location == "" && from != "":
		return EvidenceResult{}, invalidf("location_from says where a location came from: give the location too")
	case location != "" && !validLocationFrom(from):
		return EvidenceResult{}, invalidf("say where the location came from: %s, %s, %s or %s",
			LocationFromSource, LocationFromKnowledgeBase, LocationFromLearner, LocationFromEstimate)
	}
	want := evidenceData{Lesson: spec.Lesson, Source: spec.Source, Quote: quote, Location: location, LocationFrom: from}

	var existing *Evidence
	ev, err := c.writeTopic(ctx, spec.Topic, func(s *replayed, _ *topicView) (*change, error) {
		k := s.knowledge()
		if _, ok := k.sources[spec.Source]; !ok {
			return nil, &Error{Code: CodeNotFound, Message: fmt.Sprintf(
				"Topic %s has no Source %s: add it with study source add first", spec.Topic, spec.Source)}
		}
		for i, e := range k.evidence {
			if !e.Retracted && e.Lesson == want.Lesson && e.Source == want.Source && e.Quote == want.Quote &&
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
// are kept; invalid UTF-8 and other control characters are refused.
func cleanQuote(s string) (string, error) {
	if !utf8.ValidString(s) {
		return "", invalidf("the quote is not valid UTF-8 text")
	}
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
	if s == "" {
		return "", invalidf("give the exact quote, as the Source has it")
	}
	for _, r := range s {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return "", invalidf("the quote contains a control character")
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
