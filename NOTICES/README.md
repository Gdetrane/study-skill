# Notices

Lamplight's two programs, `study` and `study-shelf`, are built from what is listed here.
This folder holds the licence of each part as its authors publish it, with the files that
stand beside it: its `NOTICE`, and its list of authors where it has one. Lamplight's own
licence is the `LICENSE` file beside this folder.

`make notices` writes this file and the folders `go` and `modules` from the Go module
cache: do not edit them. The folder `pdfium` is kept by hand, as its `README.md` says. A
test fails when a program is built from a module that has no notice here.

Each part is listed with the licence it is under, in the words of the SPDX licence list,
as a person read it in the part's files. Where a part is offered under a choice of
licences, the one named is the one it is taken under.

## Go

Both programs hold Go's runtime and standard library, BSD-3-Clause: [LICENSE](go/LICENSE).

## Go modules

What the go command links into each program, for Linux and macOS, on amd64 and arm64.

- `charm.land/lipgloss/v2` v2.0.6 (MIT), in `study`: [LICENSE](modules/charm.land/lipgloss/v2/LICENSE)
- `charm.land/log/v2` v2.0.1 (MIT), in `study`: [LICENSE](modules/charm.land/log/v2/LICENSE)
- `codeberg.org/readeck/go-readability/v2` v2.1.3 (MIT), in `study-shelf`: [LICENSE](modules/codeberg.org/readeck/go-readability/v2/LICENSE)

  Its README says it began as a line-by-line port to Go of Mozilla's Readability.js, which is under Apache-2.0. The module ships the MIT licence here and no other, and no other is added.

- `github.com/BurntSushi/toml` v1.6.0 (MIT), in `study` and `study-shelf`: [COPYING](modules/github.com/BurntSushi/toml/COPYING)
- `github.com/andybalholm/cascadia` v1.3.3 (BSD-2-Clause), in `study-shelf`: [LICENSE](modules/github.com/andybalholm/cascadia/LICENSE)
- `github.com/charmbracelet/colorprofile` v0.4.3 (MIT), in `study`: [LICENSE](modules/github.com/charmbracelet/colorprofile/LICENSE)
- `github.com/charmbracelet/fang` v1.0.0 (MIT), in `study`: [LICENSE.md](modules/github.com/charmbracelet/fang/LICENSE.md)
- `github.com/charmbracelet/ultraviolet` v0.0.0-20260811164956-006e29f97886 (MIT), in `study`: [LICENSE](modules/github.com/charmbracelet/ultraviolet/LICENSE)
- `github.com/charmbracelet/x/ansi` v0.11.8 (MIT), in `study`: [LICENSE](modules/github.com/charmbracelet/x/ansi/LICENSE)
- `github.com/charmbracelet/x/exp/charmtone` v0.0.0-20250603201427-c31516f43444 (MIT), in `study`: [LICENSE](modules/github.com/charmbracelet/x/exp/charmtone/LICENSE)
- `github.com/charmbracelet/x/term` v0.2.2 (MIT), in `study`: [LICENSE](modules/github.com/charmbracelet/x/term/LICENSE)
- `github.com/charmbracelet/x/termios` v0.1.1 (MIT), in `study`: [LICENSE](modules/github.com/charmbracelet/x/termios/LICENSE)
- `github.com/charmbracelet/x/windows` v0.2.2 (MIT), in `study`: [LICENSE](modules/github.com/charmbracelet/x/windows/LICENSE)
- `github.com/clipperhouse/displaywidth` v0.11.0 (MIT), in `study`: [LICENSE](modules/github.com/clipperhouse/displaywidth/LICENSE)
- `github.com/clipperhouse/uax29/v2` v2.7.0 (MIT), in `study`: [LICENSE](modules/github.com/clipperhouse/uax29/v2/LICENSE)
- `github.com/dustin/go-humanize` v1.0.1 (MIT), in `study-shelf`: [LICENSE](modules/github.com/dustin/go-humanize/LICENSE)
- `github.com/go-logfmt/logfmt` v0.6.1 (MIT), in `study`: [LICENSE](modules/github.com/go-logfmt/logfmt/LICENSE)
- `github.com/go-shiori/dom` v0.0.0-20230515143342-73569d674e1c (MIT), in `study-shelf`: [LICENSE](modules/github.com/go-shiori/dom/LICENSE)
- `github.com/gogs/chardet` v0.0.0-20211120154057-b7413eaefb8f (MIT AND ICU), in `study-shelf`: [AUTHORS](modules/github.com/gogs/chardet/AUTHORS), [LICENSE](modules/github.com/gogs/chardet/LICENSE), [icu-license.html](modules/github.com/gogs/chardet/icu-license.html)
- `github.com/google/jsonschema-go` v0.4.3 (MIT), in `study` and `study-shelf`: [LICENSE](modules/github.com/google/jsonschema-go/LICENSE)
- `github.com/google/uuid` v1.6.0 (BSD-3-Clause), in `study-shelf`: [CONTRIBUTORS](modules/github.com/google/uuid/CONTRIBUTORS), [LICENSE](modules/github.com/google/uuid/LICENSE)
- `github.com/itlightning/dateparse` v0.2.1 (MIT), in `study-shelf`: [LICENSE](modules/github.com/itlightning/dateparse/LICENSE)
- `github.com/jolestar/go-commons-pool/v2` v2.1.2 (Apache-2.0), in `study-shelf`: [LICENSE](modules/github.com/jolestar/go-commons-pool/v2/LICENSE)
- `github.com/klippa-app/go-pdfium` v1.21.3 (MIT), in `study-shelf`: [LICENSE](modules/github.com/klippa-app/go-pdfium/LICENSE)
- `github.com/lucasb-eyer/go-colorful` v1.4.1 (MIT), in `study`: [LICENSE](modules/github.com/lucasb-eyer/go-colorful/LICENSE)
- `github.com/mattn/go-isatty` v0.0.24 (MIT), in `study-shelf` on macOS: [LICENSE](modules/github.com/mattn/go-isatty/LICENSE)
- `github.com/mattn/go-runewidth` v0.0.24 (MIT), in `study`: [LICENSE](modules/github.com/mattn/go-runewidth/LICENSE)
- `github.com/modelcontextprotocol/go-sdk` v1.8.0 (Apache-2.0 AND MIT), in `study` and `study-shelf`: [LICENSE](modules/github.com/modelcontextprotocol/go-sdk/LICENSE)
- `github.com/muesli/cancelreader` v0.2.2 (MIT), in `study`: [LICENSE](modules/github.com/muesli/cancelreader/LICENSE)
- `github.com/muesli/mango` v0.1.0 (MIT), in `study`: [LICENSE](modules/github.com/muesli/mango/LICENSE)
- `github.com/muesli/mango-cobra` v1.2.0 (MIT), in `study`: [LICENSE](modules/github.com/muesli/mango-cobra/LICENSE)
- `github.com/muesli/mango-pflag` v0.1.0 (MIT), in `study`: [LICENSE](modules/github.com/muesli/mango-pflag/LICENSE)
- `github.com/muesli/roff` v0.1.0 (MIT), in `study`: [LICENSE](modules/github.com/muesli/roff/LICENSE)
- `github.com/ncruces/go-strftime` v1.0.0 (MIT), in `study-shelf` on macOS: [LICENSE](modules/github.com/ncruces/go-strftime/LICENSE)
- `github.com/open-spaced-repetition/go-fsrs/v4` v4.0.0 (MIT), in `study`: [LICENSE](modules/github.com/open-spaced-repetition/go-fsrs/v4/LICENSE)
- `github.com/remyoudompheng/bigfft` v0.0.0-20230129092748-24d4a6f8daec (BSD-3-Clause), in `study-shelf`: [LICENSE](modules/github.com/remyoudompheng/bigfft/LICENSE)
- `github.com/rivo/uniseg` v0.4.7 (MIT), in `study`: [LICENSE.txt](modules/github.com/rivo/uniseg/LICENSE.txt)
- `github.com/segmentio/asm` v1.1.3 (MIT), in `study` and `study-shelf`: [LICENSE](modules/github.com/segmentio/asm/LICENSE)
- `github.com/segmentio/encoding` v0.5.4 (MIT), in `study` and `study-shelf`: [LICENSE](modules/github.com/segmentio/encoding/LICENSE)
- `github.com/spf13/cobra` v1.10.2 (Apache-2.0), in `study`: [LICENSE.txt](modules/github.com/spf13/cobra/LICENSE.txt)
- `github.com/spf13/pflag` v1.0.9 (BSD-3-Clause), in `study`: [LICENSE](modules/github.com/spf13/pflag/LICENSE)
- `github.com/tetratelabs/wazero` v1.12.0 (Apache-2.0), in `study-shelf`: [LICENSE](modules/github.com/tetratelabs/wazero/LICENSE), [NOTICE](modules/github.com/tetratelabs/wazero/NOTICE)
- `github.com/xo/terminfo` v0.0.0-20220910002029-abceb7e1c41e (MIT), in `study`: [LICENSE](modules/github.com/xo/terminfo/LICENSE)
- `github.com/yosida95/uritemplate/v3` v3.0.2 (BSD-3-Clause), in `study` and `study-shelf`: [LICENSE](modules/github.com/yosida95/uritemplate/v3/LICENSE)
- `go.yaml.in/yaml/v3` v3.0.5 (MIT AND Apache-2.0), in `study`: [LICENSE](modules/go.yaml.in/yaml/v3/LICENSE), [NOTICE](modules/go.yaml.in/yaml/v3/NOTICE)
- `golang.org/x/net` v0.59.0 (BSD-3-Clause), in `study-shelf`: [LICENSE](modules/golang.org/x/net/LICENSE)
- `golang.org/x/oauth2` v0.35.0 (BSD-3-Clause), in `study` and `study-shelf`: [LICENSE](modules/golang.org/x/oauth2/LICENSE)
- `golang.org/x/sync` v0.23.0 (BSD-3-Clause), in `study` and `study-shelf`: [LICENSE](modules/golang.org/x/sync/LICENSE)
- `golang.org/x/sys` v0.48.0 (BSD-3-Clause), in `study` and `study-shelf`: [LICENSE](modules/golang.org/x/sys/LICENSE)
- `golang.org/x/text` v0.42.0 (BSD-3-Clause), in `study` and `study-shelf`: [LICENSE](modules/golang.org/x/text/LICENSE)
- `golang.org/x/time` v0.15.0 (BSD-3-Clause), in `study` and `study-shelf`: [LICENSE](modules/golang.org/x/time/LICENSE)
- `modernc.org/libc` v1.77.1 (BSD-3-Clause AND MIT), in `study-shelf`: [AUTHORS](modules/modernc.org/libc/AUTHORS), [CONTRIBUTORS](modules/modernc.org/libc/CONTRIBUTORS), [LICENSE](modules/modernc.org/libc/LICENSE), [LICENSE-3RD-PARTY.md](modules/modernc.org/libc/LICENSE-3RD-PARTY.md)

  Some of its files are made by a program from the C headers of the system they are for, and keep the comments of those headers: Apple's, under the Apple Public Source License 2.0, in the macOS builds, and the GNU C library's and GCC's, under the LGPL and the GPL with its runtime exception. Those files hold types and constants and no function. The module's own licence files name neither licence, and neither is added.

- `modernc.org/mathutil` v1.7.1 (BSD-3-Clause), in `study-shelf`: [AUTHORS](modules/modernc.org/mathutil/AUTHORS), [CONTRIBUTORS](modules/modernc.org/mathutil/CONTRIBUTORS), [LICENSE](modules/modernc.org/mathutil/LICENSE)
- `modernc.org/memory` v1.12.1 (BSD-3-Clause), in `study-shelf`: [AUTHORS](modules/modernc.org/memory/AUTHORS), [CONTRIBUTORS](modules/modernc.org/memory/CONTRIBUTORS), [LICENSE](modules/modernc.org/memory/LICENSE), [LICENSE-GO](modules/modernc.org/memory/LICENSE-GO), [LICENSE-LOGO](modules/modernc.org/memory/LICENSE-LOGO), [LICENSE-MMAP-GO](modules/modernc.org/memory/LICENSE-MMAP-GO)
- `modernc.org/sqlite` v1.60.1 (BSD-3-Clause AND blessing AND MIT), in `study-shelf`: [AUTHORS](modules/modernc.org/sqlite/AUTHORS), [CONTRIBUTORS](modules/modernc.org/sqlite/CONTRIBUTORS), [LICENSE](modules/modernc.org/sqlite/LICENSE), [LICENSE-3RD-PARTY.md](modules/modernc.org/sqlite/LICENSE-3RD-PARTY.md), [LICENSE-SQLITE](modules/modernc.org/sqlite/LICENSE-SQLITE), [LICENSE-SQLITE_VEC](modules/modernc.org/sqlite/LICENSE-SQLITE_VEC)

## Inside PDFium

`study-shelf` reads PDFs with PDFium, which `github.com/klippa-app/go-pdfium` v1.21.3
holds built, as the WebAssembly file `pdfium.wasm`. [pdfium/README.md](pdfium/README.md) says what that
file is made of and how that was found out.

- PDFium chromium/8057 (BSD-3-Clause AND Apache-2.0): [LICENSE](pdfium/pdfium/LICENSE)
- Anti-Grain Geometry 2.3 (LicenseRef-AGG-2.3): [copying](pdfium/agg/copying)
- Little CMS 2.19 (MIT): [LICENSE](pdfium/lcms/LICENSE)
- OpenJPEG 2.5.4 (BSD-2-Clause): [LICENSE](pdfium/openjpeg/LICENSE)
- FreeType 2.14.3 (FTL AND MIT): [LICENSE.TXT](pdfium/freetype/LICENSE.TXT), [FTL.TXT](pdfium/freetype/docs/FTL.TXT), [fthash.c](pdfium/freetype/src/base/fthash.c)
- HarfBuzz 14.3.1 (MIT-Modern-Variant): [COPYING](pdfium/harfbuzz/COPYING)
- ICU 78 (Unicode-3.0 AND ICU): [LICENSE](pdfium/icu/LICENSE)
- libjpeg-turbo 3.1.0 (IJG AND BSD-3-Clause): [LICENSE.md](pdfium/libjpeg-turbo/LICENSE.md), [README.ijg](pdfium/libjpeg-turbo/README.ijg)
- zlib 1.3.2 (Zlib): [LICENSE](pdfium/zlib/LICENSE)
- Chromium, in zlib (BSD-3-Clause): [LICENSE](pdfium/chromium/LICENSE)
- Abseil (Apache-2.0): [LICENSE](pdfium/abseil-cpp/LICENSE)
- fast_float (MIT): [LICENSE-MIT](pdfium/fast_float/LICENSE-MIT)
- dragonbox (Apache-2.0 WITH LLVM-exception): [LICENSE-Apache2-LLVM](pdfium/dragonbox/LICENSE-Apache2-LLVM), [LICENSE-Boost](pdfium/dragonbox/LICENSE-Boost)
- pdfium-binaries (MIT): [LICENSE](pdfium/pdfium-binaries/LICENSE)
- Emscripten 6.0.9 (MIT): [LICENSE](pdfium/emscripten/LICENSE)
- musl, in Emscripten (MIT): [COPYRIGHT](pdfium/musl/COPYRIGHT)
- libc++, in Emscripten (Apache-2.0 WITH LLVM-exception): [LICENSE.TXT](pdfium/libcxx/LICENSE.TXT)
- libc++abi, in Emscripten (Apache-2.0 WITH LLVM-exception): [LICENSE.TXT](pdfium/libcxxabi/LICENSE.TXT)
- compiler-rt, in Emscripten (Apache-2.0 WITH LLVM-exception): [LICENSE.TXT](pdfium/compiler-rt/LICENSE.TXT)
