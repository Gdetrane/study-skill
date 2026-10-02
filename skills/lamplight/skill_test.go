package lamplight_test

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"

	"github.com/mordor-forge/lamplight/v2/internal/cli"
	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/internal/mcpserver"
	"github.com/mordor-forge/lamplight/v2/skills/lamplight"
)

// pendingName is a name the skill already teaches but Lamplight does not have
// yet, with the issue that adds it. It may appear only inside a
// "<!-- pending #N -->" … "<!-- /pending #N -->" block for its issue. lands
// says where it will appear: a tool ("hint_record"), a field of a tool's input
// ("topic_update.level") or a command ("study setup"). Once it is there, the
// test fails until the block's markers and the entry are removed.
type pendingName struct {
	issue string
	lands string
}

var pendingNames = map[string]pendingName{
	"assessment_record": {"#31", "assessment_record"},
	"hint_record":       {"#31", "hint_record"},
	"level":             {"#31", "topic_update.level"},
	"approach":          {"#31", "topic_update.approach"},
	"study setup":       {"#33", "study setup"},
}

// requiredTools are the tools the teaching method cannot do without: the skill
// must mention each one.
var requiredTools = []string{
	"status", "session_open", "session_close", "break_point_reached", "topic_create",
	"phase_set", "check_results", "rubric_record", "lesson_complete",
	"due_cards", "review_record", "card_add",
	"revision_propose", "revision_apply", "revision_decline",
}

// v1Leftovers match commands and files of the v1 study skill, which Lamplight
// no longer has. They match v1's command forms, not words: "a short study
// break" is fine, "/study break" is not.
var v1Leftovers = []*regexp.Regexp{
	regexp.MustCompile(`(^|[^\w~./-])/study\b`),
	regexp.MustCompile(`\bfsrs\b`),
	regexp.MustCompile(`\bFSRS_STORE\b`),
	regexp.MustCompile(`\.study-config\.json|book-catalog\.json|scripts/catalog`),
	regexp.MustCompile(`(?i)\bgit commit\b`),
}

// v1Allowed are the only places a v1Leftovers match is right.
var v1Allowed = regexp.MustCompile(`(?i)never run git commit\b`)

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
	for key, value := range meta {
		switch key {
		case "name", "description":
		case "license", "compatibility":
			if s, ok := value.(string); !ok || strings.TrimSpace(s) == "" {
				t.Errorf("frontmatter field %s must be a non-empty string", key)
			}
		case "metadata":
			fields, ok := value.(map[string]any)
			if !ok {
				t.Errorf("frontmatter field metadata must be a mapping, not %T", value)
			}
			for k, v := range fields {
				if _, ok := v.(string); !ok {
					t.Errorf("metadata.%s must be a string, not %T", k, v)
				}
			}
		default:
			t.Errorf("frontmatter field %q is not portable across agents; use name, description, license, "+
				"compatibility and metadata", key)
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
	if c, ok := meta["compatibility"].(string); ok && len(c) > 500 {
		t.Errorf("compatibility has %d characters; agents allow at most 500", len(c))
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
	spanPattern  = regexp.MustCompile("`([^`\n]+)`")
	snakePattern = regexp.MustCompile(`\b[a-z][a-z0-9]*(?:_[a-z0-9]+)+\b`)
	wordPattern  = regexp.MustCompile(`^[a-z][a-z0-9]*$`)
	textWords    = regexp.MustCompile(`[a-z][a-z0-9]*(?:_[a-z0-9]+)*`)
)

// server is what the real MCP server tells an agent: its tools, the fields
// of their input and output schemas, and the words of its instructions and
// descriptions.
type server struct {
	tools  map[string]bool
	inputs map[string]map[string]bool // tool → its input's top-level fields
	fields map[string]bool            // every field of every schema, at any depth
	words  map[string]bool            // words and snake_case values the server writes
}

// known reports whether name is something the server has: a tool, a field
// of a schema, or a value its own text names, such as resume_topic.
func (s server) known(name string) bool {
	return s.tools[name] || s.fields[name] || s.words[name]
}

func connectServer(t *testing.T) server {
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
	s := server{tools: map[string]bool{}, inputs: map[string]map[string]bool{}, fields: map[string]bool{}, words: map[string]bool{}}
	addWords := func(text string) {
		for _, w := range textWords.FindAllString(strings.ToLower(text), -1) {
			s.words[w] = true
		}
	}
	addWords(session.InitializeResult().Instructions)
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		s.tools[tool.Name] = true
		addWords(tool.Description)
		s.inputs[tool.Name] = map[string]bool{}
		if props, ok := asMap(tool.InputSchema)["properties"].(map[string]any); ok {
			for name := range props {
				s.inputs[tool.Name][name] = true
			}
		}
		walkSchema(tool.InputSchema, s.fields, addWords)
		walkSchema(tool.OutputSchema, s.fields, addWords)
	}
	if len(s.tools) == 0 || len(s.fields) == 0 {
		t.Fatalf("the MCP server lists %d tools and %d schema fields", len(s.tools), len(s.fields))
	}
	return s
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// walkSchema adds the property names of a JSON schema and all its nested
// schemas to fields, and passes its descriptions and enum values to words.
func walkSchema(v any, fields map[string]bool, words func(string)) {
	switch v := v.(type) {
	case map[string]any:
		if props, ok := v["properties"].(map[string]any); ok {
			for name := range props {
				fields[name] = true
			}
		}
		if d, ok := v["description"].(string); ok {
			words(d)
		}
		if enum, ok := v["enum"].([]any); ok {
			for _, e := range enum {
				if s, ok := e.(string); ok {
					words(s)
				}
			}
		}
		for _, child := range v {
			walkSchema(child, fields, words)
		}
	case []any:
		for _, child := range v {
			walkSchema(child, fields, words)
		}
	}
}

// pendingBlocks finds a file's pending blocks, reporting markers that do not
// pair up.
func pendingBlocks(t *testing.T, f skillFile) []pendingBlock {
	t.Helper()
	var blocks []pendingBlock
	marker := regexp.MustCompile(`<!-- (/?)pending (#\d+) -->`)
	var open *pendingBlock
	for _, m := range marker.FindAllStringSubmatchIndex(f.text, -1) {
		closing, issue := f.text[m[2]:m[3]] == "/", f.text[m[4]:m[5]]
		switch {
		case !closing && open != nil:
			t.Errorf("%s: <!-- pending %s --> opens inside the block for %s", f.name, issue, open.issue)
		case !closing:
			open = &pendingBlock{issue: issue, start: m[1]}
		case open == nil:
			t.Errorf("%s: <!-- /pending %s --> closes no block", f.name, issue)
		case open.issue != issue:
			t.Errorf("%s: the block for %s is closed as %s", f.name, open.issue, issue)
			open = nil
		default:
			open.end = m[0]
			blocks = append(blocks, *open)
			open = nil
		}
	}
	if open != nil {
		t.Errorf("%s: <!-- pending %s --> is never closed", f.name, open.issue)
	}
	return blocks
}

type pendingBlock struct {
	issue      string
	start, end int // the block's content, between its markers
}

func inBlock(blocks []pendingBlock, at int, issue string) bool {
	for _, b := range blocks {
		if b.issue == issue && at >= b.start && at < b.end {
			return true
		}
	}
	return false
}

// mention is a name the skill writes, at an offset in its file's text.
type mention struct {
	name string
	at   int
}

// names returns what the skill writes that a tool or schema might have:
// every snake_case name anywhere, code blocks included, and every single
// lowercase word written in backticks outside code blocks.
func names(f skillFile) []mention {
	var found []mention
	for _, m := range snakePattern.FindAllStringIndex(f.text, -1) {
		found = append(found, mention{f.text[m[0]:m[1]], m[0]})
	}
	for _, m := range spanPattern.FindAllStringSubmatchIndex(f.prose, -1) {
		if name := f.prose[m[2]:m[3]]; wordPattern.MatchString(name) {
			found = append(found, mention{name, m[2]})
		}
	}
	return found
}

func TestToolNames(t *testing.T) {
	s := connectServer(t)
	commands := cli.CommandTree()
	for _, p := range pendingNames {
		if pendingHasLanded(s, commands, p.lands) {
			t.Errorf("%s now exists: remove its <!-- pending %s --> markers from the skill and its pendingNames entry",
				p.lands, p.issue)
		}
	}
	mentioned := map[string]bool{}
	pendingSeen := map[string]bool{}
	for _, f := range readSkill(t) {
		blocks := pendingBlocks(t, f)
		for _, m := range regexp.MustCompile(`pending (#\d+)`).FindAllStringSubmatch(f.text, -1) {
			if !slices.ContainsFunc(mapValues(pendingNames), func(p pendingName) bool { return p.issue == m[1] }) {
				t.Errorf("%s mentions pending %s, but pendingNames has nothing pending on %s", f.name, m[1], m[1])
			}
		}
		used := map[int]bool{} // blocks that hold a name pending on their issue
		for _, m := range names(f) {
			if p, pending := pendingNames[m.name]; pending {
				if !inBlock(blocks, m.at, p.issue) {
					t.Errorf("%s mentions %s outside a <!-- pending %s --> block; Lamplight does not have it yet",
						f.name, m.name, p.issue)
				}
				pendingSeen[m.name] = true
				for i, b := range blocks {
					if b.issue == p.issue && m.at >= b.start && m.at < b.end {
						used[i] = true
					}
				}
				continue
			}
			if !s.known(m.name) {
				t.Errorf("%s mentions %s, which is neither a tool of the MCP server nor a field or value it names",
					f.name, m.name)
			}
			if s.tools[m.name] {
				mentioned[m.name] = true
			}
		}
		for _, c := range studyCommands(f) {
			if p, pending := pendingNames[pendingCommand(c.line)]; pending {
				if !inBlock(blocks, c.at, p.issue) {
					t.Errorf("%s shows `%s` outside a <!-- pending %s --> block; study has no such command yet",
						f.name, strings.TrimSpace(c.text), p.issue)
				}
				pendingSeen[pendingCommand(c.line)] = true
				for i, b := range blocks {
					if b.issue == p.issue && c.at >= b.start && c.at < b.end {
						used[i] = true
					}
				}
			}
		}
		for i, b := range blocks {
			if !used[i] {
				t.Errorf("%s: the block for pending %s names nothing pending on %s", f.name, b.issue, b.issue)
			}
		}
	}
	for name, p := range pendingNames {
		if !pendingSeen[name] {
			t.Errorf("pendingNames lists %s for %s, but the skill never mentions it", name, p.issue)
		}
	}
	for _, name := range requiredTools {
		if !s.tools[name] {
			t.Errorf("required tool %s is not a tool of the MCP server", name)
		} else if !mentioned[name] {
			t.Errorf("the skill never mentions %s, which the teaching method needs", name)
		}
	}
}

func mapValues[K comparable, V any](m map[K]V) []V {
	var values []V
	for _, v := range m {
		values = append(values, v)
	}
	return values
}

// pendingHasLanded reports whether the server or the CLI now has what a
// pending entry waits for.
func pendingHasLanded(s server, root *cobra.Command, lands string) bool {
	if line, ok := strings.CutPrefix(lands, "study "); ok {
		cmd, _, err := root.Find(strings.Fields(line))
		return err == nil && cmd != root
	}
	if tool, field, ok := strings.Cut(lands, "."); ok {
		return s.inputs[tool][field]
	}
	return s.tools[lands]
}

// pendingCommand returns the "study <command>" key a pending entry would use
// for a command line.
func pendingCommand(line []string) string {
	if len(line) == 0 {
		return "study"
	}
	return "study " + line[0]
}

// commandLine is a study command the skill shows, as the words after
// "study", at an offset in its file's text.
type commandLine struct {
	text string
	line []string
	at   int
}

// studyCommands returns the study commands the skill shows: in backticks, or
// on a line of a code block, either at the start or after "--", as in
// "claude mcp add lamplight -- study mcp". Prose such as "a short study
// break" is not a command.
func studyCommands(f skillFile) []commandLine {
	var found []commandLine
	add := func(text string, at int) {
		fields := strings.Fields(text)
		for i, field := range fields {
			if field == "study" && (i == 0 || fields[i-1] == "--") {
				found = append(found, commandLine{text: text, line: fields[i+1:], at: at})
				return
			}
		}
	}
	for _, m := range spanPattern.FindAllStringSubmatchIndex(f.prose, -1) {
		add(f.prose[m[2]:m[3]], m[2])
	}
	offset := 0
	for _, line := range strings.SplitAfter(f.text, "\n") {
		if f.prose[offset:offset+len(line)] != line {
			add(line, offset) // a line of a code block
		}
		offset += len(line)
	}
	return found
}

func TestCLICommands(t *testing.T) {
	checked := 0
	for _, f := range readSkill(t) {
		for _, c := range studyCommands(f) {
			if _, pending := pendingNames[pendingCommand(c.line)]; pending {
				continue // TestToolNames keeps it inside its block
			}
			checked++
			if err := checkCommand(cli.CommandTree(), c.line); err != nil {
				t.Errorf("%s shows `%s`: %v", f.name, strings.TrimSpace(c.text), err)
			}
		}
	}
	if checked == 0 {
		t.Error("the skill shows no study commands; it should show at least study check")
	}
}

// checkCommand resolves a study command line against the CLI's command
// tree, as cobra would run it: the words up to the first argument or flag
// name the command, and its flags and number of arguments must fit it.
// Placeholders such as <lesson-id> count as arguments.
func checkCommand(root *cobra.Command, line []string) error {
	cmd, args, err := root.Find(line)
	if err != nil {
		return err
	}
	if err := cmd.ParseFlags(args); err != nil {
		return fmt.Errorf("%s: %v", cmd.CommandPath(), err)
	}
	if err := cmd.ValidateArgs(cmd.Flags().Args()); err != nil {
		return fmt.Errorf("%s: %v", cmd.CommandPath(), err)
	}
	return nil
}

func TestCheckCommandCatchesMistakes(t *testing.T) {
	for _, line := range []string{
		"study check <lesson-id> --topic <topic-id>",
		"study session open <topic> --session ID --focus learn",
		"study status",
		"study mcp",
	} {
		if err := checkCommand(cli.CommandTree(), strings.Fields(line)[1:]); err != nil {
			t.Errorf("%s: %v", line, err)
		}
	}
	for _, line := range []string{
		"study chek <lesson-id>",             // no such command
		"study check <lesson-id> --lesson x", // no such flag
		"study check",                        // a missing argument
		"study session opne <topic>",         // no such subcommand
		"study mcp extra",                    // an argument too many
	} {
		if checkCommand(cli.CommandTree(), strings.Fields(line)[1:]) == nil {
			t.Errorf("%s was accepted", line)
		}
	}
}

func TestNoV1Leftovers(t *testing.T) {
	for _, f := range readSkill(t) {
		for _, pattern := range v1Leftovers {
			for _, m := range pattern.FindAllStringIndex(f.text, -1) {
				if allowed := v1Allowed.FindAllStringIndex(f.text, -1); slices.ContainsFunc(allowed, func(a []int) bool {
					return m[0] >= a[0] && m[1] <= a[1]
				}) {
					continue
				}
				t.Errorf("%s mentions %q, which belongs to the v1 study skill", f.name, strings.TrimSpace(f.text[m[0]:m[1]]))
			}
		}
	}
}

func TestV1LeftoversMatchCommandsNotWords(t *testing.T) {
	for text, want := range map[string]bool{
		"Run /study start":                    true,
		"type /study":                         true,
		"the fsrs binary":                     true,
		"set FSRS_STORE":                      true,
		"then git commit the work":            true,
		"Never run git commit yourself.":      false,
		"a short study break":                 false,
		"~/study/notes":                       false,
		"installed in ~/.agents/skills/study": false,
	} {
		found := false
		for _, pattern := range v1Leftovers {
			for _, m := range pattern.FindAllStringIndex(text, -1) {
				if !slices.ContainsFunc(v1Allowed.FindAllStringIndex(text, -1), func(a []int) bool {
					return m[0] >= a[0] && m[1] <= a[1]
				}) {
					found = true
				}
			}
		}
		if found != want {
			t.Errorf("%q: leftover found = %v, want %v", text, found, want)
		}
	}
}
