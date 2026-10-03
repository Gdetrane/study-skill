package core

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// LessonDetail is one Lesson as the agent needs to see it: where it sits in
// the Syllabus, its progress, its file, and what its YAML header declares.
// It shows the Check's criteria and commands, never Held-out data: for
// Attempts and grades, see check_results.
type LessonDetail struct {
	Topic string `json:"topic"`
	// Number is the Lesson's display number, such as "2.3".
	Number    string         `json:"number"`
	ID        string         `json:"id"`
	Title     string         `json:"title"`
	Hours     float64        `json:"hours,omitempty"`
	Status    string         `json:"status"`
	Phase     string         `json:"phase,omitempty"`
	Milestone LessonPosition `json:"milestone"`
	// File is the Lesson file, relative to the Topic, and Path the same
	// file as an absolute path; Exists is false until the agent writes it.
	File   string `json:"file"`
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	// HeaderReadable is false when the file is missing or its YAML header
	// cannot be read; HeaderError says why.
	HeaderReadable bool   `json:"header_readable"`
	HeaderError    string `json:"header_error,omitempty"`
	// Check is the Check the header declares now, and CheckVersion its
	// version. CheckShown reports whether that is the version shown to the
	// learner when practicing last started, the one completion counts;
	// ShownCheck is that version, if practicing started.
	Check        []Criterion  `json:"check"`
	CheckVersion string       `json:"check_version,omitempty"`
	CheckShown   bool         `json:"check_shown"`
	ShownCheck   string       `json:"shown_check,omitempty"`
	BreakPoints  []BreakPoint `json:"break_points"`
}

// LessonPosition names the Milestone that holds a Lesson.
type LessonPosition struct {
	Number int    `json:"number"`
	ID     string `json:"id"`
	Title  string `json:"title"`
}

// LessonOf returns one Lesson of a Topic's Syllabus as LessonDetail. It reads
// the Lesson file but runs nothing and writes nothing.
func (c *Core) LessonOf(ctx context.Context, topicID, lessonID string) (LessonDetail, error) {
	if err := ctx.Err(); err != nil {
		return LessonDetail{}, err
	}
	home, topic, err := c.openTopicFolder(topicID)
	if err != nil {
		return LessonDetail{}, err
	}
	defer home.Close()
	defer topic.Close()
	h, err := readHistory(topic, topicID)
	if err != nil {
		return LessonDetail{}, err
	}
	s := replayHistory(h)
	if _, err := requireLesson(s, topicID, lessonID); err != nil {
		return LessonDetail{}, err
	}
	d := LessonDetail{Topic: topicID, File: lessonFile(lessonID), Path: filepath.Join(c.topicDir(topicID), lessonFile(lessonID)),
		Check: []Criterion{}, BreakPoints: []BreakPoint{}}
	for i, m := range s.study.syllabus.Milestones {
		for j, l := range m.Lessons {
			if l.ID != lessonID {
				continue
			}
			v := s.study.lessonView(l)
			d.Number, d.ID, d.Title, d.Hours, d.Status, d.Phase = fmt.Sprintf("%d.%d", i+1, j+1), v.ID, v.Title, v.Hours, v.Status, v.Phase
			d.Milestone = LessonPosition{Number: i + 1, ID: m.ID, Title: m.Title}
		}
	}
	if ls := s.study.lessons[lessonID]; ls != nil {
		d.ShownCheck = ls.shownCheck
	}
	data, exists, err := readFile(topic, d.File)
	if err != nil {
		return LessonDetail{}, err
	}
	d.Exists = exists
	if !exists {
		if d.Status == LessonSkipped {
			d.HeaderError = d.File + " does not exist, and the Lesson is skipped"
		} else {
			d.HeaderError = d.File + " does not exist yet: write the Lesson, with its Check in the YAML header"
		}
		return d, nil
	}
	var problems []string
	if check, err := parseCheck(data); err != nil {
		problems = append(problems, err.Error())
	} else if check != nil {
		d.Check = check
		if canonical, _, err := lessonCodec.get(data, checkKey); err == nil {
			d.CheckVersion = contentHash(canonical, true)
		}
	}
	if points, err := parseBreakPoints(data); err != nil {
		problems = append(problems, err.Error())
	} else if points != nil {
		d.BreakPoints = points
	}
	d.HeaderReadable = len(problems) == 0
	if !d.HeaderReadable {
		d.HeaderError = strings.Join(problems, "; ")
	}
	d.CheckShown = d.CheckVersion != "" && d.CheckVersion == d.ShownCheck
	return d, nil
}
