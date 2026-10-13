package notices

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// licences says under which licence each linked module is, in SPDX's words:
// what a person read in the module's licence files. A module that is not
// here cannot be released, and neither can one whose files no longer read as
// the licence named here (wording). Where a module is under several, they
// are joined by AND; none here is offered under a choice.
//
// The list in NOTICES/README.md shows each, and the packages state the kinds
// of all of them as their licence (.goreleaser.yaml), which a test holds to
// this table.
var licences = map[string]string{
	"charm.land/lipgloss/v2":                       "MIT",
	"charm.land/log/v2":                            "MIT",
	"codeberg.org/readeck/go-readability/v2":       "MIT",
	"github.com/BurntSushi/toml":                   "MIT",
	"github.com/andybalholm/cascadia":              "BSD-2-Clause",
	"github.com/charmbracelet/colorprofile":        "MIT",
	"github.com/charmbracelet/fang":                "MIT",
	"github.com/charmbracelet/ultraviolet":         "MIT",
	"github.com/charmbracelet/x/ansi":              "MIT",
	"github.com/charmbracelet/x/exp/charmtone":     "MIT",
	"github.com/charmbracelet/x/term":              "MIT",
	"github.com/charmbracelet/x/termios":           "MIT",
	"github.com/charmbracelet/x/windows":           "MIT",
	"github.com/clipperhouse/displaywidth":         "MIT",
	"github.com/clipperhouse/uax29/v2":             "MIT",
	"github.com/dustin/go-humanize":                "MIT",
	"github.com/go-logfmt/logfmt":                  "MIT",
	"github.com/go-shiori/dom":                     "MIT",
	"github.com/gogs/chardet":                      "MIT AND ICU",
	"github.com/google/jsonschema-go":              "MIT",
	"github.com/google/uuid":                       "BSD-3-Clause",
	"github.com/itlightning/dateparse":             "MIT",
	"github.com/jolestar/go-commons-pool/v2":       "Apache-2.0",
	"github.com/klippa-app/go-pdfium":              "MIT",
	"github.com/lucasb-eyer/go-colorful":           "MIT",
	"github.com/mattn/go-isatty":                   "MIT",
	"github.com/mattn/go-runewidth":                "MIT",
	"github.com/modelcontextprotocol/go-sdk":       "Apache-2.0 AND MIT",
	"github.com/muesli/cancelreader":               "MIT",
	"github.com/muesli/mango":                      "MIT",
	"github.com/muesli/mango-cobra":                "MIT",
	"github.com/muesli/mango-pflag":                "MIT",
	"github.com/muesli/roff":                       "MIT",
	"github.com/ncruces/go-strftime":               "MIT",
	"github.com/open-spaced-repetition/go-fsrs/v4": "MIT",
	"github.com/remyoudompheng/bigfft":             "BSD-3-Clause",
	"github.com/rivo/uniseg":                       "MIT",
	"github.com/segmentio/asm":                     "MIT",
	"github.com/segmentio/encoding":                "MIT",
	"github.com/spf13/cobra":                       "Apache-2.0",
	"github.com/spf13/pflag":                       "BSD-3-Clause",
	"github.com/tetratelabs/wazero":                "Apache-2.0",
	"github.com/xo/terminfo":                       "MIT",
	"github.com/yosida95/uritemplate/v3":           "BSD-3-Clause",
	"go.yaml.in/yaml/v3":                           "MIT AND Apache-2.0",
	"golang.org/x/net":                             "BSD-3-Clause",
	"golang.org/x/oauth2":                          "BSD-3-Clause",
	"golang.org/x/sync":                            "BSD-3-Clause",
	"golang.org/x/sys":                             "BSD-3-Clause",
	"golang.org/x/text":                            "BSD-3-Clause",
	"golang.org/x/time":                            "BSD-3-Clause",
	// With musl, go-netdb and nixpkgs, whose texts LICENSE-3RD-PARTY.md has.
	"modernc.org/libc":     "BSD-3-Clause AND MIT",
	"modernc.org/mathutil": "BSD-3-Clause",
	"modernc.org/memory":   "BSD-3-Clause",
	// SQLite itself is in the public domain, which SPDX calls blessing after
	// the words its source carries. sqlite-vec is MIT.
	"modernc.org/sqlite": "BSD-3-Clause AND blessing AND MIT",
}

// goLicence is the licence of Go's runtime and standard library.
const goLicence = "BSD-3-Clause"

// remarks are what a reader of the list should know of a module and its
// licence file does not say. Each states what is so and what the folder
// holds, and is printed under the module in NOTICES/README.md.
var remarks = map[string]string{
	"codeberg.org/readeck/go-readability/v2": "Its README says it began as a line-by-line port to Go of Mozilla's Readability.js, " +
		"which is under Apache-2.0. The module ships the MIT licence here and no other, and no other is added.",
	"modernc.org/libc": "Some of its files are made by a program from the C headers of the system they are for, and keep the " +
		"comments of those headers: Apple's, under the Apple Public Source License 2.0, in the macOS builds, and the GNU C " +
		"library's and GCC's, under the LGPL and the GPL with its runtime exception. Those files hold types and constants " +
		"and no function. The module's own licence files name neither licence, and neither is added.",
}

// wording is, for each licence in SPDX's words, what any text of it says:
// patterns that must all be found in a module's licence files, read with
// every run of white space as one space. It catches a table that names a
// licence the files do not bear out, as when a module changes its licence
// and its line in the table stays; it is no reading of the licence.
var wording = map[string][]string{
	"Apache-2.0":                     {`Apache License,? Version 2\.0`},
	"Apache-2.0 WITH LLVM-exception": {`Apache License,? Version 2\.0`, `LLVM Exceptions to the Apache 2\.0 License`},
	"BSD-2-Clause":                   {`Redistribution and use in source and binary forms`},
	"BSD-3-Clause":                   {`Redistribution and use in source and binary forms`, `(?i)neither the names? of`},
	"FTL":                            {`FreeType Project LICENSE`},
	"ICU":                            {`ICU License`},
	"IJG":                            {`Independent JPEG Group`},
	"MIT":                            {`Permission is hereby granted, free of charge`},
	"MIT-Modern-Variant":             {`Permission is hereby granted, without written agreement`},
	"Unicode-3.0":                    {`UNICODE LICENSE V3`},
	"Zlib":                           {`The origin of this software must not be misrepresented`},
	"blessing":                       {`dedicated to the public domain`},
	// Anti-Grain Geometry 2.3's licence has no name in SPDX's list.
	"LicenseRef-AGG-2.3": {`Permission to copy, use, modify, sell and distribute this software`},
}

// kinds splits a licence written with AND into the licences it names.
func kinds(licence string) []string {
	var out []string
	for _, kind := range strings.Split(licence, " AND ") {
		if kind = strings.TrimSpace(kind); kind != "" {
			out = append(out, kind)
		}
	}
	return out
}

var space = regexp.MustCompile(`\s+`)

// borneOut checks that texts read as every licence that licence names.
func borneOut(licence string, texts ...[]byte) error {
	if len(kinds(licence)) == 0 {
		return fmt.Errorf("nothing says under which licence it is")
	}
	var all []byte
	for _, text := range texts {
		all = append(append(all, text...), ' ')
	}
	all = space.ReplaceAll(all, []byte(" "))
	for _, kind := range kinds(licence) {
		patterns, known := wording[kind]
		if !known {
			return fmt.Errorf("%q is a licence internal/notices knows no wording of: add it to wording in licences.go, in SPDX's words", kind)
		}
		for _, pattern := range patterns {
			if !regexp.MustCompile(pattern).Match(all) {
				return fmt.Errorf("it is said to be under %s, and none of its licence files says %q", kind, pattern)
			}
		}
	}
	return nil
}

// Licences are the kinds of licence of everything in the set, in SPDX's
// words and in order: what a package that carries the set states as its own.
func (s *Set) Licences() []string {
	seen := map[string]bool{}
	for _, kind := range kinds(goLicence) {
		seen[kind] = true
	}
	for _, notice := range s.Modules {
		for _, kind := range kinds(notice.Licence) {
			seen[kind] = true
		}
	}
	if s.PDFium != nil {
		for _, part := range s.PDFium.Parts {
			for _, kind := range kinds(part.Licence) {
				seen[kind] = true
			}
		}
	}
	var out []string
	for kind := range seen {
		out = append(out, kind)
	}
	sort.Strings(out)
	return out
}
