package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/BurntSushi/toml"
)

// syllabus.toml is written from a Syllabus as a map, so settings this
// version of study does not know, at any level (the file, a Milestone, a
// Lesson), are kept, as in topic.toml. They live in the Extra fields, which
// never appear in Lamplight's own JSON; inside Event payloads they travel
// with the Syllabus (see syllabusData). Keys are written sorted, so the same
// Syllabus always gives the same bytes.

// Known keys at each level.
var (
	syllabusKeys  = []string{"format", "milestones"}
	milestoneKeys = []string{"id", "title", "outcome", "priority", "lessons"}
	lessonKeys    = []string{"id", "title", "hours"}
)

// extras returns the keys of m that are not known.
func extras(m map[string]any, known []string) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		if !slices.Contains(known, k) {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func withExtras(m, extra map[string]any) map[string]any {
	for k, v := range extra {
		if _, known := m[k]; !known {
			m[k] = v
		}
	}
	return m
}

func (s Syllabus) toMap() map[string]any {
	milestones := make([]map[string]any, 0, len(s.Milestones))
	for _, m := range s.Milestones {
		lessons := make([]map[string]any, 0, len(m.Lessons))
		for _, l := range m.Lessons {
			lm := map[string]any{"id": l.ID, "title": l.Title}
			if l.Hours != 0 {
				lm["hours"] = l.Hours
			}
			lessons = append(lessons, withExtras(lm, l.Extra))
		}
		mm := map[string]any{"id": m.ID, "title": m.Title, "priority": m.Priority, "lessons": lessons}
		if m.Outcome != "" {
			mm["outcome"] = m.Outcome
		}
		milestones = append(milestones, withExtras(mm, m.Extra))
	}
	return withExtras(map[string]any{"milestones": milestones}, s.Extra)
}

// syllabusFromMap reads a Syllabus from a decoded TOML or JSON document.
func syllabusFromMap(doc map[string]any) (Syllabus, error) {
	s := Syllabus{Extra: extras(doc, syllabusKeys)}
	milestones, err := tables(doc["milestones"], "milestones")
	if err != nil {
		return s, err
	}
	for i, mm := range milestones {
		m := Milestone{Extra: extras(mm, milestoneKeys)}
		where := fmt.Sprintf("milestone %d", i+1)
		if m.ID, err = str(mm, "id", where); err != nil {
			return s, err
		}
		if m.Title, err = str(mm, "title", where); err != nil {
			return s, err
		}
		if m.Outcome, err = str(mm, "outcome", where); err != nil {
			return s, err
		}
		if m.Priority, err = str(mm, "priority", where); err != nil {
			return s, err
		}
		lessons, err := tables(mm["lessons"], where+" lessons")
		if err != nil {
			return s, err
		}
		for j, lm := range lessons {
			l := SyllabusLesson{Extra: extras(lm, lessonKeys)}
			where := fmt.Sprintf("lesson %d of milestone %d", j+1, i+1)
			if l.ID, err = str(lm, "id", where); err != nil {
				return s, err
			}
			if l.Title, err = str(lm, "title", where); err != nil {
				return s, err
			}
			if l.Hours, err = number(lm, "hours", where); err != nil {
				return s, err
			}
			m.Lessons = append(m.Lessons, l)
		}
		s.Milestones = append(s.Milestones, m)
	}
	return s, nil
}

// tables reads an array of tables, as TOML or JSON decodes it.
func tables(v any, where string) ([]map[string]any, error) {
	switch t := v.(type) {
	case nil:
		return nil, nil
	case []map[string]any:
		return t, nil
	case []any:
		out := make([]map[string]any, 0, len(t))
		for _, e := range t {
			m, ok := e.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s holds something other than tables", where)
			}
			out = append(out, m)
		}
		return out, nil
	}
	return nil, fmt.Errorf("%s is not a list of tables", where)
}

func str(m map[string]any, key, where string) (string, error) {
	switch v := m[key].(type) {
	case nil:
		return "", nil
	case string:
		return v, nil
	}
	return "", fmt.Errorf("%s of %s is not text", key, where)
}

func number(m map[string]any, key, where string) (float64, error) {
	switch v := m[key].(type) {
	case nil:
		return 0, nil
	case float64:
		return v, nil
	case int64:
		return float64(v), nil
	case json.Number:
		return v.Float64()
	}
	return 0, fmt.Errorf("%s of %s is not a number", key, where)
}

// tomlValue converts what JSON decoding gives into what TOML encodes:
// json.Number becomes an integer when it is one, else a float.
func tomlValue(v any) any {
	switch t := v.(type) {
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i
		}
		f, _ := t.Float64()
		return f
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = tomlValue(e)
		}
		return out
	case []map[string]any:
		out := make([]map[string]any, len(t))
		for i, e := range t {
			out[i] = tomlValue(e).(map[string]any)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = tomlValue(e)
		}
		return out
	}
	return v
}

func encodeSyllabus(s Syllabus) ([]byte, error) {
	doc := tomlValue(s.toMap()).(map[string]any)
	doc["format"] = int64(FormatVersion)
	var buf bytes.Buffer
	buf.WriteString("# Syllabus. Lamplight rewrites this file from approved Revisions; comments are not kept.\n")
	enc := toml.NewEncoder(&buf)
	enc.Indent = ""
	if err := enc.Encode(doc); err != nil {
		return nil, internalError("encoding "+syllabusFile, err)
	}
	return buf.Bytes(), nil
}

// parseSyllabusFile reads syllabus.toml; where names it in errors.
func parseSyllabusFile(data []byte, where string) (Syllabus, error) {
	var doc map[string]any
	if _, err := toml.Decode(string(data), &doc); err != nil {
		return Syllabus{}, corruptf("%s is not valid TOML: %v", where, err)
	}
	format, ok := doc["format"].(int64)
	switch {
	case !ok || format < 1:
		return Syllabus{}, corruptf("%s has no format number: add format = %d", where, FormatVersion)
	case format > FormatVersion:
		return Syllabus{}, newerFormat(where, int(format))
	}
	s, err := syllabusFromMap(doc)
	if err != nil {
		return Syllabus{}, corruptf("%s: %v", where, err)
	}
	return s, nil
}

// syllabusData is a Syllabus inside an Event's payload: settings this
// version does not know travel with it, so a newer version's Revision is
// applied without losing them.
type syllabusData struct{ Syllabus }

func (d syllabusData) MarshalJSON() ([]byte, error) { return json.Marshal(d.Syllabus.toMap()) }

func (d *syllabusData) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return err
	}
	s, err := syllabusFromMap(doc)
	d.Syllabus = s
	return err
}

// keepExtras carries settings this version does not know from the current
// Syllabus into a proposed one, matching Milestones and Lessons by ID, so a
// Revision drafted by an agent never drops them.
func keepExtras(next Syllabus, current *Syllabus) Syllabus {
	if current == nil {
		return next
	}
	milestones := map[string]Milestone{}
	lessons := map[string]SyllabusLesson{}
	for _, m := range current.Milestones {
		milestones[m.ID] = m
		for _, l := range m.Lessons {
			lessons[l.ID] = l
		}
	}
	if next.Extra == nil {
		next.Extra = current.Extra
	}
	out := next
	out.Milestones = make([]Milestone, len(next.Milestones))
	for i, m := range next.Milestones {
		if m.Extra == nil {
			m.Extra = milestones[m.ID].Extra
		}
		ls := make([]SyllabusLesson, len(m.Lessons))
		for j, l := range m.Lessons {
			if l.Extra == nil {
				l.Extra = lessons[l.ID].Extra
			}
			ls[j] = l
		}
		m.Lessons = ls
		out.Milestones[i] = m
	}
	return out
}
