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
// Each criterion of a Check has exactly one of run, rubric or held_out:
//
//	check:
//	  - id: tests
//	    describe: The tests pass
//	    run: [go, test, ./...]
//	  - id: names
//	    rubric: Every function name says what it does
//	  - id: accuracy
//	    describe: Accuracy on the test set
//	    held_out: [python3, eval.py]

const checkKey = "check"

func lessonFile(lessonID string) string { return "lessons/" + lessonID + ".md" }

func checkItem(lessonID string) string { return lessonFile(lessonID) + "#" + checkKey }

// Criterion kinds.
const (
	// CriterionRun is a command that can be repeated, such as tests or a
	// build; it decides whether the work passes.
	CriterionRun = "run"
	// CriterionRubric is an item the agent grades with rubric_record, after
	// the learner checks themselves first.
	CriterionRubric = "rubric"
	// CriterionHeldOut is an evaluation on Held-out data. In v2.0 it is
	// diagnostic: its results never decide whether the work passes. The
	// same spelling is the YAML key, this kind and the JSON field.
	CriterionHeldOut = "held_out"
)

// Criterion is one thing a Lesson's Check measures.
type Criterion struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Describe says what the criterion measures, for the learner.
	Describe string `json:"describe,omitempty"`
	// Command is the command of a run or held_out criterion, as an
	// argument list, run in the Lesson's practice folder. The YAML header
	// gives it under run or held_out.
	Command []string `json:"command,omitempty"`
	// Rubric is what a rubric item asks of the work.
	Rubric string `json:"rubric,omitempty"`
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

// criterionYAML accepts any value for each kind's key, so a mistake gets a
// precise error rather than a YAML type error for the whole header.
type criterionYAML struct {
	ID       string `yaml:"id"`
	Describe string `yaml:"describe"`
	Run      any    `yaml:"run"`
	Rubric   any    `yaml:"rubric"`
	HeldOut  any    `yaml:"held_out"`
}

const (
	maxCriteria       = 30
	maxRubricRunes    = 500
	maxArgumentRunes  = 4096
	maxArgumentsInRun = 100
)

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
	if len(h.Check) > maxCriteria {
		return nil, corruptf("the Check has %d criteria; keep it to %d at most", len(h.Check), maxCriteria)
	}
	seen := map[string]bool{}
	out := make([]Criterion, 0, len(h.Check))
	for i, c := range h.Check {
		if err := validateEntityID("criterion", c.ID); err != nil {
			return nil, corruptf("criterion %d of the Check: %v", i+1, err)
		}
		if seen[c.ID] {
			return nil, corruptf("the Check has two criteria with the id %s", c.ID)
		}
		seen[c.ID] = true
		describe, err := cleanText("description of criterion "+c.ID, c.Describe, maxGoalRunes)
		if err != nil {
			return nil, corruptf("%v", err)
		}
		crit, err := criterionOf(c)
		if err != nil {
			return nil, err
		}
		crit.Describe = describe
		out = append(out, crit)
	}
	decides := false
	for _, crit := range out {
		if crit.Kind != CriterionHeldOut {
			decides = true
		}
	}
	if !decides {
		return nil, corruptf("the Check has only held_out criteria, whose results never decide whether a Lesson is " +
			"done: add a run criterion or a rubric item")
	}
	return out, nil
}

// criterionOf reads the one key that gives a criterion its kind: run,
// rubric or held_out.
func criterionOf(c criterionYAML) (Criterion, error) {
	given := 0
	for _, v := range []any{c.Run, c.Rubric, c.HeldOut} {
		if v != nil {
			given++
		}
	}
	switch given {
	case 0:
		return Criterion{}, corruptf("criterion %s of the Check needs one of run (a command, such as [go, test, ./...]), "+
			"rubric (what to grade) or held_out (a command run on the Held-out data)", c.ID)
	case 1:
	default:
		return Criterion{}, corruptf("criterion %s of the Check has more than one of run, rubric and held_out; "+
			"give each its own criterion", c.ID)
	}
	if c.Rubric != nil {
		text, ok := c.Rubric.(string)
		if !ok {
			return Criterion{}, corruptf("the rubric of criterion %s is not text: write what to grade, "+
				"such as \"Every function name says what it does\"", c.ID)
		}
		item, err := requiredText("rubric of criterion "+c.ID, text, maxRubricRunes)
		if err != nil {
			return Criterion{}, corruptf("%v", err)
		}
		return Criterion{ID: c.ID, Kind: CriterionRubric, Rubric: item}, nil
	}
	kind, key, raw := CriterionRun, "run", c.Run
	if c.HeldOut != nil {
		kind, key, raw = CriterionHeldOut, "held_out", c.HeldOut
	}
	args, ok := raw.([]any)
	if !ok || len(args) == 0 {
		return Criterion{}, corruptf("%s of criterion %s is not a command: give it as an argument list, "+
			"such as [go, test, ./...], never as one string for a shell", key, c.ID)
	}
	if len(args) > maxArgumentsInRun {
		return Criterion{}, corruptf("the command of criterion %s has more than %d arguments", c.ID, maxArgumentsInRun)
	}
	run := make([]string, len(args))
	for i, a := range args {
		switch v := a.(type) {
		case string:
			run[i] = v
		case int, int64, uint64, float64, bool:
			run[i] = fmt.Sprint(v)
		default:
			return Criterion{}, corruptf("argument %d of the command of criterion %s is not text", i+1, c.ID)
		}
		if _, err := cleanText("command of criterion "+c.ID, run[i], maxArgumentRunes); err != nil {
			return Criterion{}, corruptf("%v", err)
		}
	}
	if run[0] == "" {
		return Criterion{}, corruptf("the command of criterion %s has no program", c.ID)
	}
	return Criterion{ID: c.ID, Kind: kind, Command: run}, nil
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
