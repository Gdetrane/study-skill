package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/BurntSushi/toml"
)

// syllabus.toml is written from a Syllabus as a map, so settings this
// version of study does not know, at any level (the file, a Milestone, a
// Lesson), are kept, as in topic.toml. They live in the Extra fields, which
// never appear in Lamplight's own JSON; inside Event payloads they travel
// with the Syllabus (see syllabusData). Keys are written sorted, so the same
// Syllabus always gives the same bytes.
//
// Dates are written as text ("2026-12-01"): TOML's local dates would go
// through Go's time.Time, whose time zone can shift them by a day. A native
// TOML date written by hand is read, and written back as text.

// Known keys at each level.
var (
	syllabusKeys  = []string{"format", "milestones"}
	milestoneKeys = []string{"id", "title", "outcome", "priority", "target", "lessons"}
	lessonKeys    = []string{"id", "title", "hours", "skipped"}
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
			m[k] = plainValue(v)
		}
	}
	return m
}

// plainValue turns TOML dates and times among settings this version does
// not know into their text, which survives a trip through JSON unchanged
// and is the same on every machine.
func plainValue(v any) any {
	switch t := v.(type) {
	case time.Time:
		switch t.Location().String() {
		case "date-local":
			return t.Format(dateLayout)
		case "datetime-local":
			return t.Format("2006-01-02T15:04:05.999999999")
		case "time-local":
			return t.Format("15:04:05.999999999")
		}
		return t.Format(time.RFC3339Nano)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = plainValue(e)
		}
		return out
	case []map[string]any:
		out := make([]map[string]any, len(t))
		for i, e := range t {
			out[i] = plainValue(e).(map[string]any)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = plainValue(e)
		}
		return out
	}
	return v
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
			if l.Skipped {
				lm["skipped"] = true
			}
			lessons = append(lessons, withExtras(lm, l.Extra))
		}
		mm := map[string]any{"id": m.ID, "title": m.Title, "priority": m.Priority, "lessons": lessons}
		if m.Outcome != "" {
			mm["outcome"] = m.Outcome
		}
		if m.Target != "" {
			mm["target"] = m.Target
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
		where := fmt.Sprintf("Milestone %d", i+1)
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
		if m.Target, err = date(mm, "target", where); err != nil {
			return s, err
		}
		lessons, err := tables(mm["lessons"], where+" lessons")
		if err != nil {
			return s, err
		}
		for j, lm := range lessons {
			l := SyllabusLesson{Extra: extras(lm, lessonKeys)}
			where := fmt.Sprintf("Lesson %d.%d", i+1, j+1)
			if l.ID, err = str(lm, "id", where); err != nil {
				return s, err
			}
			if l.Title, err = str(lm, "title", where); err != nil {
				return s, err
			}
			if l.Hours, err = number(lm, "hours", where); err != nil {
				return s, err
			}
			if l.Skipped, err = boolean(lm, "skipped", where); err != nil {
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

// date reads a date written as text or as a native TOML date.
func date(m map[string]any, key, where string) (string, error) {
	switch v := m[key].(type) {
	case nil:
		return "", nil
	case string:
		return v, nil
	case time.Time:
		if v.Location().String() == "date-local" {
			return v.Format(dateLayout), nil
		}
		return "", fmt.Errorf("%s of %s must be a date such as 2026-12-01, without a time", key, where)
	}
	return "", fmt.Errorf("%s of %s is not a date", key, where)
}

func boolean(m map[string]any, key, where string) (bool, error) {
	switch v := m[key].(type) {
	case nil:
		return false, nil
	case bool:
		return v, nil
	}
	return false, fmt.Errorf("%s of %s must be true or false", key, where)
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
