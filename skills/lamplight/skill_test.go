package lamplight_test

import (
	"bytes"
	"context"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.yaml.in/yaml/v3"

	"github.com/mordor-forge/lamplight/v2/internal/cli"
	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/internal/mcpserver"
	"github.com/mordor-forge/lamplight/v2/skills/lamplight"
)

// pendingTools are tools the skill already teaches but the MCP server does not
// have yet, with the issue that adds them. Each may appear only inside a
// "<!-- pending #N -->" … "<!-- /pending #N -->" block for its issue. Once the
// server has the tool, TestToolNames fails until the block's markers and the
// entry here are removed.
var pendingTools = map[string]string{
	"assessment_record": "#31",
	"hint_record":       "#31",
}

// requiredTools are the tools the teaching method cannot do without: the skill
// must mention each one.
var requiredTools = []string{
	"status", "session_open", "session_close", "break_point_reached", "topic_create",
	"phase_set", "check_results", "rubric_record", "lesson_complete",
	"due_cards", "review_record", "card_add",
	"revision_propose", "revision_apply", "revision_decline",
}

// v1Leftovers are commands and files of the v1 study skill, which Lamplight
// no longer has.
var v1Leftovers = []string{
	"/study ", "study init", "study start", "study break", "study add-source",
	"study catalog", ".study-config.json", "scripts/fsrs", "scripts/catalog", ".fsrs",
	"session_state", "pending_action", "difficulty_override", "book-catalog.json",
}

// skillFile is one Markdown file of the skill, with its fenced code blocks
// separated from its prose.
type skillFile struct {
	name  string
	text  string
	prose string   // text with fenced code blocks blanked out, offsets kept
	code  []string // lines inside fenced code blocks
}

func readSkill(t *testing.T) []skillFile {
	t.Helper()
	var files []skillFile
	err := fs.WalkDir(lamplight.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if name != "SKILL.md" && !(path.Dir(name) == "references" && path.Ext(name) == ".md") {
			t.Errorf("unexpected file embedded in the skill: %s", name)
			return nil
		}
		data, err := fs.ReadFile(lamplight.FS(), name)
		if err != nil {
			return err
		}
		files = append(files, splitCode(name, string(data)))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 2 {
		t.Fatalf("the skill has %d files; want SKILL.md and its references", len(files))
	}
	return files
}

func splitCode(name, text string) skillFile {
	f := skillFile{name: name, text: text}
	prose := []byte(text)
	inFence := false
	offset := 0
	for _, line := range strings.SplitAfter(text, "\n") {
		trimmed := strings.TrimSpace(line)
		fence := strings.HasPrefix(trimmed, "```")
		if inFence || fence {
			for i := offset; i < offset+len(line); i++ {
				if prose[i] != '\n' {
					prose[i] = ' '
				}
			}
			if inFence && !fence {
				f.code = append(f.code, strings.TrimRight(line, "\n"))
			}
		}
		if fence {
			inFence = !inFence
		}
		offset += len(line)
	}
	f.prose = string(prose)
	return f
}

func TestFrontmatter(t *testing.T) {
	data, err := fs.ReadFile(lamplight.FS(), "SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.HasPrefix(text, "---\n") {
		t.Fatal("SKILL.md must start with YAML frontmatter")
	}
	end := strings.Index(text[4:], "\n---\n")
	if end < 0 {
		t.Fatal("SKILL.md must close its frontmatter with ---")
	}
	var meta map[string]any
	if err := yaml.Unmarshal([]byte(text[4:4+end]), &meta); err != nil {
		t.Fatalf("frontmatter is not valid YAML: %v", err)
	}
	for key := range meta {
		if key != "name" && key != "description" && key != "metadata" {
			t.Errorf("frontmatter field %q is not portable across agents; use name, description and metadata", key)
		}
	}
	name, _ := meta["name"].(string)
	if name != lamplight.Name {
		t.Errorf("name = %q, want %q", name, lamplight.Name)
	}
	if !regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`).MatchString(name) || len(name) > 64 {
		t.Errorf("name %q must be at most 64 lowercase letters, digits and hyphens", name)
	}
	description, _ := meta["description"].(string)
	switch {
	case len(strings.TrimSpace(description)) < 50:
		t.Errorf("description is too short to say when to use the skill: %q", description)
	case len(description) > 1024:
		t.Errorf("description has %d characters; agents allow at most 1024", len(description))
	case strings.ContainsAny(description, "<>"):
		t.Error("description must not contain angle brackets")
	}
	if !strings.Contains(description, "Use when") {
		t.Error("description must say when to use the skill (\"Use when …\")")
	}
}

func TestSkillIsConcise(t *testing.T) {
	for _, f := range readSkill(t) {
		lines := strings.Count(f.text, "\n")
		limit := 300
		if f.name == "SKILL.md" {
			limit = 200
		}
		if lines > limit {
			t.Errorf("%s has %d lines; keep it under %d and move detail into a reference", f.name, lines, limit)
		}
	}
}

var linkPattern = regexp.MustCompile(`\]\(([^)\s]+)\)`)

func TestLinksResolve(t *testing.T) {
	files := readSkill(t)
	byName := map[string]skillFile{}
	for _, f := range files {
		byName[f.name] = f
	}
	linkedFromSkill := map[string]bool{}
	for _, f := range files {
		for _, m := range linkPattern.FindAllStringSubmatch(f.prose, -1) {
			target := m[1]
			if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			file, anchor, _ := strings.Cut(target, "#")
			if file == "" {
				file = f.name
			} else {
				file = path.Clean(path.Join(path.Dir(f.name), file))
			}
			dest, ok := byName[file]
			if !ok {
				t.Errorf("%s links to %s, which the skill does not have", f.name, target)
				continue
			}
			if f.name == "SKILL.md" {
				linkedFromSkill[file] = true
			}
			if anchor != "" && !slices.Contains(headingAnchors(dest.prose), anchor) {
				t.Errorf("%s links to %s, but %s has no heading with that anchor", f.name, target, file)
			}
		}
	}
	for _, f := range files {
		if f.name != "SKILL.md" && !linkedFromSkill[f.name] {
			t.Errorf("%s is not linked from SKILL.md, so agents never reach it", f.name)
		}
	}
}

// headingAnchors returns the anchors of a Markdown file's headings, as
// GitHub and most renderers make them.
func headingAnchors(prose string) []string {
	var anchors []string
	for _, line := range strings.Split(prose, "\n") {
		if !strings.HasPrefix(line, "#") {
			continue
		}
		heading := strings.ToLower(strings.TrimSpace(strings.TrimLeft(line, "#")))
		var b strings.Builder
		for _, r := range heading {
			switch {
			case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_':
				b.WriteRune(r)
			case r == ' ':
				b.WriteRune('-')
			}
		}
		anchors = append(anchors, b.String())
	}
	return anchors
}

var (
	spanPattern    = regexp.MustCompile("`([^`\n]+)`")
	pendingPattern = regexp.MustCompile(`(?s)<!-- pending (#\d+) -->.*?<!-- /pending (#\d+) -->`)
	identPattern   = regexp.MustCompile(`^[a-z]+(_[a-z]+)*$`)
)

// toolSuffixes and toolWords tell a name that can only be a tool, such as
// card_add or status, from a field such as next_step, so a tool the server
// does not have is caught. notTools are fields that look like tools anyway.
var (
	toolSuffixes = []string{"_record", "_add", "_edit", "_delete", "_suspend", "_propose", "_apply",
		"_decline", "_create", "_update", "_complete", "_search", "_retract", "_dismiss", "_reached",
		"_done", "_open", "_close", "_set", "_results", "_cards"}
	toolWords = []string{"status", "syllabus", "cards", "sources", "evidence", "tasks", "checkpoint",
		"lesson", "history"}
	notTools = []string{"can_complete"}
)

func looksLikeTool(name string) bool {
	if slices.Contains(notTools, name) {
		return false
	}
	if slices.Contains(toolWords, name) {
		return true
	}
	for _, suffix := range toolSuffixes {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

// serverTools lists the tools of the real MCP server, in process.
func serverTools(t *testing.T) map[string]bool {
	t.Helper()
	ctx := context.Background()
	home := t.TempDir()
	c, err := core.Open(core.Options{
		Getenv: func(key string) string { return map[string]string{"STUDY_HOME": home, "HOME": home}[key] },
		Dir:    home,
	})
	if err != nil {
		t.Fatal(err)
	}
	serverSide, clientSide := mcp.NewInMemoryTransports()
	if _, err := mcpserver.New(c, "test", nil).Connect(ctx, serverSide, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "skill-test", Version: "1"}, nil).Connect(ctx, clientSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	tools := map[string]bool{}
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		tools[tool.Name] = true
	}
	if len(tools) == 0 {
		t.Fatal("the MCP server lists no tools")
	}
	return tools
}

func TestToolNames(t *testing.T) {
	tools := serverTools(t)
	for name, issue := range pendingTools {
		if tools[name] {
			t.Errorf("%s now exists: remove its <!-- pending %s --> markers from the skill and its pendingTools entry", name, issue)
		}
	}
	mentioned := map[string]bool{}
	for _, f := range readSkill(t) {
		blocks := pendingPattern.FindAllStringSubmatchIndex(f.prose, -1)
		for _, b := range blocks {
			if open, closing := f.prose[b[2]:b[3]], f.prose[b[4]:b[5]]; open != closing {
				t.Errorf("%s: pending block for %s is closed as %s", f.name, open, closing)
			}
		}
		inPending := func(at int, issue string) bool {
			for _, b := range blocks {
				if at >= b[0] && at < b[1] && f.prose[b[2]:b[3]] == issue {
					return true
				}
			}
			return false
		}
		for _, m := range spanPattern.FindAllStringSubmatchIndex(f.prose, -1) {
			name := f.prose[m[2]:m[3]]
			if !identPattern.MatchString(name) {
				continue
			}
			switch issue, pending := pendingTools[name]; {
			case tools[name]:
				mentioned[name] = true
			case pending && !inPending(m[0], issue):
				t.Errorf("%s mentions %s outside a <!-- pending %s --> block; the server has no such tool yet", f.name, name, issue)
			case !pending && looksLikeTool(name):
				t.Errorf("%s mentions `%s`, which is not a tool of the MCP server", f.name, name)
			}
		}
	}
	for _, name := range requiredTools {
		if !tools[name] {
			t.Errorf("required tool %s is not a tool of the MCP server", name)
		} else if !mentioned[name] {
			t.Errorf("the skill never mentions %s, which the teaching method needs", name)
		}
	}
}

func TestCLICommands(t *testing.T) {
	home := t.TempDir()
	opts := core.Options{
		Getenv: func(key string) string { return map[string]string{"STUDY_HOME": home, "HOME": home}[key] },
		Dir:    home,
	}
	checked := map[string]bool{}
	for _, f := range readSkill(t) {
		var commands []string
		for _, m := range spanPattern.FindAllStringSubmatch(f.prose, -1) {
			commands = append(commands, m[1])
		}
		for _, line := range f.code {
			commands = append(commands, strings.TrimSpace(line))
		}
		for _, command := range commands {
			words := commandWords(command)
			key := strings.Join(words, " ")
			if len(words) == 0 || checked[key] {
				continue
			}
			checked[key] = true
			var stdout, stderr bytes.Buffer
			code := cli.Run(context.Background(), append(slices.Clone(words), "--help"), strings.NewReader(""), &stdout, &stderr, opts)
			usage := usageLine(stdout.String())
			want := "study " + key
			if code != cli.ExitOK || !(usage == want || strings.HasPrefix(usage, want+" ")) {
				t.Errorf("%s mentions `%s`, but study has no command %q (help shows %q)", f.name, command, want, usage)
			}
		}
	}
	if len(checked) == 0 {
		t.Error("the skill mentions no study commands; it should show at least study check")
	}
}

// commandWords returns the command words of a study command line, up to its
// first argument or flag: "study check <lesson-id> --topic <topic-id>" gives
// ["check"].
func commandWords(line string) []string {
	fields := strings.Fields(line)
	if len(fields) < 2 || fields[0] != "study" {
		return nil
	}
	var words []string
	for _, w := range fields[1:] {
		if !regexp.MustCompile(`^[a-z][a-z-]*$`).MatchString(w) {
			break
		}
		words = append(words, w)
	}
	return words
}

// usageLine returns the first line under USAGE in help output.
func usageLine(help string) string {
	lines := strings.Split(help, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) != "USAGE" {
			continue
		}
		for _, next := range lines[i+1:] {
			if s := strings.TrimSpace(next); s != "" {
				return s
			}
		}
	}
	return ""
}

func TestNoV1Leftovers(t *testing.T) {
	for _, f := range readSkill(t) {
		for _, stale := range v1Leftovers {
			if strings.Contains(f.text, stale) {
				t.Errorf("%s mentions %q, which belongs to the v1 study skill", f.name, stale)
			}
		}
	}
}
