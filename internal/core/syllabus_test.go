package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readSyllabusFile(t *testing.T, m *machine) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(m.home, "c", syllabusFile))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestAHandEditOfTheSyllabusIsAdoptedNotOverwritten: the learner edits
// syllabus.toml by hand. A Revision drafted from the History would
// overwrite the edit, so it is refused; proposing the file itself adopts the
// edit once approved, keeping what this version does not know at every level.
func TestAHandEditOfTheSyllabusIsAdoptedNotOverwritten(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	withSyllabus(t, m)
	edited := `format = 1
notes = "the learner's own key"

[[milestones]]
id = "basics"
title = "Basics"
priority = "must"
colour = "red"

[[milestones.lessons]]
id = "answer"
title = "The answer to everything"
difficulty = 3
`
	writeFile(t, m, syllabusFile, edited)

	_, err := m.ProposeRevision(ctx, "c", RevisionSpec{Summary: "Add a Lesson", Syllabus: oneLessonSyllabus})
	if CodeOf(err) != CodeFailedPrecondition || !strings.Contains(err.Error(), "from_file") {
		t.Fatalf("a Revision over a hand edit: err = %v, want failed_precondition naming from_file", err)
	}
	adopt, err := m.ProposeRevision(ctx, "c", RevisionSpec{Summary: "Keep the learner's edit", FromFile: true})
	if err != nil {
		t.Fatalf("proposing the file: %v", err)
	}
	if got := adopt.Syllabus.Milestones[0].Lessons[0].Title; got != "The answer to everything" {
		t.Errorf("the adopted Lesson title = %q", got)
	}
	if _, err := m.ApplyRevision(ctx, "c", adopt.Revision, Approval{Via: "chat", LearnerSaid: "yes, keep it"}, false); err != nil {
		t.Fatalf("applying the adopted edit: %v", err)
	}
	topic, err := m.readTopic("c")
	if err != nil || hasFlag(topic.Flags, FlagEditedOutside, syllabusFile) {
		t.Errorf("flags after adopting = %+v, %v", topic.Flags, err)
	}
	for _, kept := range []string{`notes = "the learner's own key"`, `colour = "red"`, `difficulty = 3`} {
		if !strings.Contains(readSyllabusFile(t, m), kept) {
			t.Errorf("syllabus.toml lost %s:\n%s", kept, readSyllabusFile(t, m))
		}
	}

	// An agent's next Revision, drafted without those keys, keeps them.
	next := Syllabus{Milestones: []Milestone{{ID: "basics", Title: "Basics", Lessons: []SyllabusLesson{
		{ID: "answer", Title: "The answer to everything"}, {ID: "question", Title: "The question"},
	}}}}
	p, err := m.ProposeRevision(ctx, "c", RevisionSpec{Summary: "Add the question", Syllabus: next})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ApplyRevision(ctx, "c", p.Revision, Approval{Via: "chat", LearnerSaid: "ok"}, false); err != nil {
		t.Fatal(err)
	}
	after := readSyllabusFile(t, m)
	for _, kept := range []string{`notes = "the learner's own key"`, `colour = "red"`, `difficulty = 3`, `id = "question"`} {
		if !strings.Contains(after, kept) {
			t.Errorf("after the next Revision, syllabus.toml lacks %s:\n%s", kept, after)
		}
	}
	s, err := parseSyllabusFile([]byte(after), syllabusFile)
	if err != nil {
		t.Fatal(err)
	}
	again, err := encodeSyllabus(s)
	if err != nil || string(again) != after {
		t.Errorf("encoding the same Syllabus twice gave different bytes:\n%s\n%s", after, again)
	}
}

func TestASyllabusInANewerFormatIsRefused(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	writeFile(t, m, syllabusFile, "format = 99\n")
	if _, err := m.ProposeRevision(ctx, "c", RevisionSpec{Summary: "s", FromFile: true}); CodeOf(err) != CodeNewerFormat {
		t.Errorf("a newer syllabus.toml: err = %v, want newer_format", err)
	}
	writeFile(t, m, syllabusFile, "[[milestones]]\nid = \"m\"\n")
	if _, err := m.ProposeRevision(ctx, "c", RevisionSpec{Summary: "s", FromFile: true}); CodeOf(err) != CodeCorrupt {
		t.Errorf("a syllabus.toml without a format: err = %v, want corrupt", err)
	}
}

// TestARevisionsBaseIsTheRecordedVersion: the base comes from the History,
// so a hand edit made between proposing and applying is caught.
func TestARevisionsBaseIsTheRecordedVersion(t *testing.T) {
	ctx := context.Background()
	m := newTopic(t)
	withSyllabus(t, m)
	p, err := m.ProposeRevision(ctx, "c", RevisionSpec{Summary: "Retitle", Syllabus: oneLessonSyllabus})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, m, syllabusFile, strings.Replace(readSyllabusFile(t, m), "The answer", "Edited by hand", 1))
	if _, err := m.ApplyRevision(ctx, "c", p.Revision, Approval{Via: "chat", LearnerSaid: "ok"}, false); CodeOf(err) != CodeFailedPrecondition {
		t.Errorf("applying over a hand edit: err = %v, want failed_precondition", err)
	}
	if !strings.Contains(readSyllabusFile(t, m), "Edited by hand") {
		t.Error("the hand edit was overwritten")
	}
}
