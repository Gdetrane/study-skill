// Package lamplight embeds the lamplight skill: SKILL.md and its references,
// the teaching method agents follow when they use Lamplight's MCP tools. The
// files live next to this package so the skill can also be installed straight
// from the repository; `study setup` installs the embedded copy.
package lamplight

import (
	"embed"
	"io/fs"
)

// Name is the skill's name, as its frontmatter and its install folder spell it.
const Name = "lamplight"

//go:embed SKILL.md references
var files embed.FS

// FS returns the skill's files: SKILL.md at the root and the references/
// folder, nothing else.
func FS() fs.FS { return files }
