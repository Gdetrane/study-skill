package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"go.yaml.in/yaml/v3"
)

// A Lesson's text lives in lessons/<lesson-id>.md, which the agent writes.
// Its YAML header holds the Check and the Break points:
//
//	---
//	check:
//	  - id: tests
//	    describe: The tests pass
//	    run: [go, test, ./...]
//	break_points:
//	  - id: parser
//	    describe: The parser reads every token
//	  - id: errors
//	    describe: Errors name the line they come from
//	---
//	# Pointers
//	...
//
// The Check gates completion, so it is an item of its own,
// "lessons/<lesson-id>.md#check", whose version is the hash of its
// canonical form. Lamplight records that version when practicing starts and
// with every Attempt, and status flags a Check changed since.
//
// Break points are named steps, in order, where a Session can stop and a
// later one resume. They gate nothing, so they are not an item: reaching one
// records its id, and its description is read back from the file.
//
// TODO(#30): rubric and held-out criteria, and per-criterion results files.

const checkKey = "check"

func lessonFile(lessonID string) string { return "lessons/" + lessonID + ".md" }

func checkItem(lessonID string) string { return lessonFile(lessonID) + "#" + checkKey }

// Criterion is one thing a Lesson's Check measures. In v2.0's first cut,
// every criterion is a run: a command, as an argument list, run in the
// Lesson's practice folder, that passes when it exits with status 0.
type Criterion struct {
	ID       string   `json:"id" yaml:"id"`
	Describe string   `json:"describe,omitempty" yaml:"describe,omitempty"`
	Run      []string `json:"run" yaml:"run"`
}

// lessonHeader is the part of a Lesson file's YAML header that holds the
// Check. It is read on its own, so a wrong type or invalid value elsewhere
// in the header, such as in the Break points, never makes the Check
// unreadable; a YAML syntax error breaks the whole header, as it would any
// header. Settings
// Lamplight does not know are ignored.
type lessonHeader struct {
	Check []criterionYAML `yaml:"check"`
}

// breakPointHeader is the part of a Lesson file's YAML header that holds the
// Break points.
type breakPointHeader struct {
	BreakPoints []breakPointYAML `yaml:"break_points"`
}

// BreakPoint is a named step in a Lesson where a Session can stop and a
// later one resume.
type BreakPoint struct {
	ID       string `json:"id"`
	Describe string `json:"describe,omitempty"`
}

type breakPointYAML struct {
	ID       string `yaml:"id"`
	Describe string `yaml:"describe"`
}

// criterionYAML accepts any criterion so an unsupported kind gets a clear
// error rather than being silently dropped.
type criterionYAML struct {
	ID       string   `yaml:"id"`
	Describe string   `yaml:"describe"`
	Run      []string `yaml:"run"`
	Rubric   any      `yaml:"rubric"`
	HeldOut  any      `yaml:"held_out"`
}

// splitFrontMatter returns the YAML header of a Markdown file, or nil when it
// has none.
func splitFrontMatter(data []byte) ([]byte, error) {
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	if !bytes.HasPrefix(data, []byte("---\n")) {
		return nil, nil
	}
	rest := data[len("---\n"):]
	// The header ends at the first line that is exactly "---".
	for off := 0; off < len(rest); {
		line, _, found := bytes.Cut(rest[off:], []byte{'\n'})
		if string(line) == "---" {
			return rest[:off], nil
		}
		if !found {
			break
		}
		off += len(line) + 1
	}
	return nil, corruptf("the YAML header is not closed with a --- line")
}

// parseCheck reads the Check from a Lesson file. It returns nil when the
// Lesson has no Check.
func parseCheck(data []byte) ([]Criterion, error) {
	header, err := splitFrontMatter(data)
	if err != nil || header == nil {
		return nil, err
	}
	var h lessonHeader
	if err := yaml.Unmarshal(header, &h); err != nil {
		return nil, corruptf("the YAML header is not valid: %v", err)
	}
	if len(h.Check) == 0 {
		return nil, nil
	}
	seen := map[string]bool{}
	out := make([]Criterion, 0, len(h.Check))
	for i, c := range h.Check {
		if c.Rubric != nil || c.HeldOut != nil {
			return nil, corruptf("criterion %d of the Check is a rubric or held-out criterion, which arrive with "+
				"a later version of study: use run criteria for now", i+1)
		}
		if err := validateEntityID("criterion", c.ID); err != nil {
			return nil, corruptf("criterion %d of the Check: %v", i+1, err)
		}
		if seen[c.ID] {
			return nil, corruptf("the Check has two criteria with the id %s", c.ID)
		}
		seen[c.ID] = true
		if len(c.Run) == 0 || c.Run[0] == "" {
			return nil, corruptf("criterion %s of the Check has no command: give run as an argument list, "+
				"such as [go, test, ./...]", c.ID)
		}
		describe, err := cleanText("description of criterion "+c.ID, c.Describe, maxGoalRunes)
		if err != nil {
			return nil, corruptf("%v", err)
		}
		for _, arg := range c.Run {
			if _, err := cleanText("command of criterion "+c.ID, arg, 4096); err != nil {
				return nil, corruptf("%v", err)
			}
		}
		out = append(out, Criterion{ID: c.ID, Describe: describe, Run: c.Run})
	}
	return out, nil
}

// parseBreakPoints reads the Break points from a Lesson file, in order. It
// returns nil when the Lesson declares none.
func parseBreakPoints(data []byte) ([]BreakPoint, error) {
	header, err := splitFrontMatter(data)
	if err != nil || header == nil {
		return nil, err
	}
	var h breakPointHeader
	if err := yaml.Unmarshal(header, &h); err != nil {
		return nil, corruptf("the Break points in the YAML header are not valid: %v", err)
	}
	seen := map[string]bool{}
	var out []BreakPoint
	for i, b := range h.BreakPoints {
		if err := validateEntityID("Break point", b.ID); err != nil {
			return nil, corruptf("Break point %d: %v", i+1, err)
		}
		if seen[b.ID] {
			return nil, corruptf("two Break points have the id %s", b.ID)
		}
		seen[b.ID] = true
		describe, err := cleanText("description of Break point "+b.ID, b.Describe, maxGoalRunes)
		if err != nil {
			return nil, corruptf("%v", err)
		}
		out = append(out, BreakPoint{ID: b.ID, Describe: describe})
	}
	return out, nil
}

// readBreakPoints reads a Lesson's Break points from the Topic. A Lesson
// file that does not exist yet declares none.
func readBreakPoints(topic *os.Root, lessonID string) ([]BreakPoint, error) {
	data, exists, err := readFile(topic, lessonFile(lessonID))
	if err != nil || !exists {
		return nil, err
	}
	points, err := parseBreakPoints(data)
	if err != nil {
		return nil, corruptf("%s: %v", lessonFile(lessonID), err)
	}
	return points, nil
}

// lessonCodec reads the Check of a Lesson file as an item. Its canonical
// form is the Check's JSON, so reformatting the header, or editing the
// Lesson's text, does not change its version. Lamplight never writes Lesson
// files: the agent does.
var lessonCodec = fileCodec{
	get: func(file []byte, key string) ([]byte, bool, error) {
		if key != checkKey {
			return nil, false, corruptf("a Lesson file has no item %q", key)
		}
		check, err := parseCheck(file)
		if err != nil || check == nil {
			return nil, false, err
		}
		data, err := json.Marshal(check)
		if err != nil {
			return nil, false, internalError("encoding a Check", err)
		}
		return data, true, nil
	},
	put: func([]byte, string, []byte, bool) ([]byte, error) {
		return nil, internalError("writing a Check", fmt.Errorf("Lamplight never writes Lesson files"))
	},
}

// readCheck reads a Lesson's Check and its version from the Topic.
func readCheck(topic *os.Root, lessonID string) ([]Criterion, string, error) {
	data, exists, err := readFile(topic, lessonFile(lessonID))
	if err != nil {
		return nil, "", err
	}
	if !exists {
		return nil, "", &Error{Code: CodeFailedPrecondition, Message: lessonFile(lessonID) +
			" does not exist: write the Lesson, with its Check in the YAML header, first"}
	}
	check, err := parseCheck(data)
	if err != nil {
		return nil, "", corruptf("%s: %v", lessonFile(lessonID), err)
	}
	if check == nil {
		return nil, "", &Error{Code: CodeFailedPrecondition, Message: lessonFile(lessonID) +
			" has no Check: write its criteria under check: in the YAML header"}
	}
	canonical, _, err := lessonCodec.get(data, checkKey)
	if err != nil {
		return nil, "", err
	}
	return check, contentHash(canonical, true), nil
}
