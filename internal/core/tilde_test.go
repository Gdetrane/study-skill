package core

import (
	"context"
	"strings"
	"testing"
)

// A path that starts with ~ fails when the home folder cannot be found, as
// with a relative HOME, and the error names HOME. Left to the folder study
// started in, the path would name the folder called ~ there, and a file the
// agent wrote in a Topic would be read as one from the learner's home.
func TestATildePathFailsWithoutAHomeFolder(t *testing.T) {
	ctx := context.Background()
	gitIdentity(t)
	studyHome := t.TempDir()
	// The folder study starts in holds a folder called ~, with a file for
	// each path below; the learner's home holds the same files.
	work, userHome := t.TempDir(), t.TempDir()
	files := map[string]string{
		"syllabus.json":         `{"milestones": [{"id": "basics", "title": "Basics", "lessons": [{"id": "answer", "title": "The answer"}]}]}`,
		"assessment.json":       `{"kind": "placement", "summary": "Knows the basics", "level": "beginner", "items": [{"area": "answers", "outcome": "correct"}]}`,
		"book.pdf":              "%PDF-1.4",
		"ws/.study-config.json": `{"version": 3, "topic": "Go"}` + "\n",
	}
	planted := map[string]string{}
	for name, content := range files {
		planted["~/"+name] = content
	}
	writeFiles(t, work, planted)
	writeFiles(t, userHome, files)
	// A v1 workspace whose config names a source in the home folder.
	v1 := v1Workspace(t, v1Options{config: map[string]any{"sources": []any{map[string]any{"path": "~/book.pdf", "title": "K&R"}}}})

	open := func(home string) *Core {
		t.Helper()
		c, err := Open(Options{
			Getenv: func(key string) string { return map[string]string{"STUDY_HOME": studyHome, "HOME": home}[key] },
			Dir:    work,
		})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	reads := func(c *Core) map[string]func() error {
		return map[string]func() error{
			"a Syllabus":    func() error { _, err := c.ReadSyllabusFile("~/syllabus.json"); return err },
			"an Assessment": func() error { _, err := c.ReadAssessmentFile("~/assessment.json"); return err },
			"a Source": func() error {
				_, err := c.AddSource(ctx, SourceSpec{Topic: "rust", File: "~/book.pdf", DryRun: true})
				return err
			},
			"a v1 workspace": func() error { _, err := c.ImportV1(ctx, ImportSpec{Dir: "~/ws", DryRun: true}); return err },
			"the home folder itself": func() error {
				_, err := c.ImportV1(ctx, ImportSpec{Dir: "~", DryRun: true})
				if CodeOf(err) == CodeInvalidArgument && strings.Contains(err.Error(), "is not a v1 workspace") {
					return nil // the home folder was found, and is none
				}
				return err
			},
		}
	}

	c := open("rel")
	if _, err := c.CreateTopic(ctx, TopicSpec{Title: "Rust"}); err != nil {
		t.Fatal(err)
	}
	for name, read := range reads(c) {
		err := read()
		if CodeOf(err) != CodeInvalidArgument || !strings.Contains(err.Error(), "starts with ~") ||
			!strings.Contains(err.Error(), `HOME is "rel", which is not an absolute path`) {
			t.Errorf("%s by a ~ path, with HOME relative = %v (%s); want invalid_argument, naming HOME", name, err, CodeOf(err))
		}
	}
	// A source the v1 config names by a ~ path is not looked for in the
	// folder study started in either: it is dropped, and the report says
	// why.
	got, err := c.ImportV1(ctx, ImportSpec{Dir: v1, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if detail := note(t, got.Dropped, "sources[0]"); len(got.Sources) != 0 || !strings.Contains(detail, `HOME is "rel"`) {
		t.Errorf("a v1 source by a ~ path, with HOME relative: sources %+v, dropped %q", got.Sources, detail)
	}

	// With a home folder the same paths name its files, and the ones in the
	// folder study started in are a folder like any other.
	c = open(userHome)
	for name, read := range reads(c) {
		if err := read(); err != nil {
			t.Errorf("%s by a ~ path, with a home folder: %v", name, err)
		}
	}
	got, err = c.ImportV1(ctx, ImportSpec{Dir: v1, DryRun: true})
	if err != nil || len(got.Sources) != 1 || got.Sources[0].FileName != "book.pdf" {
		t.Errorf("a v1 source by a ~ path, with a home folder: %+v, %v", got.Sources, err)
	}
	// The folder called ~ is reached by a path that says so, whatever HOME
	// is.
	if _, err := open("rel").ReadSyllabusFile("./~/syllabus.json"); err != nil {
		t.Errorf("a relative path through the folder called ~: %v", err)
	}
}
