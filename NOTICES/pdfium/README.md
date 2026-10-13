# What is inside pdfium.wasm

`study-shelf` reads PDFs with PDFium. `github.com/klippa-app/go-pdfium` holds PDFium built,
as the WebAssembly file `internal/pdfium_wasm/pdfium.wasm`, which it embeds: the file is
inside every `study-shelf`. It arrives with no licence beside it and no word of what it was
built from. This folder holds the licences of what it is made of, each copied as its
repository holds it. `sources.toml` names every file here, where it came from and at which
revision.

It is kept by hand, for **go-pdfium v1.21.3**. A test in `internal/notices` fails when
`go.mod` names another go-pdfium, when the `pdfium.wasm` in the module cache is not the one
these notices were written for, and when a file here is not the one `sources.toml` names.
[What to do then](#when-go-pdfium-changes) is at the end.

## What the file was built from

Neither the file nor go-pdfium v1.21.3 says. The file has no custom section: no names, no
record of what produced it. This is what the two repositories' histories show, as read on
2026-10-13.

- The file's SHA-256 is `d1dbc423597c0c65cd932cd1bb6a452fdc457a1f3b5009d3d5ec4ca768c47f53`,
  and its git blob is `7be4a9155e3151aba97ae2a158d7bb8ca264742e`, the one go-pdfium's
  repository holds at the tag `v1.21.3`.
- go-pdfium's `NATIVE_VS_WEBASSEMBLY.md` says the file's patches and build steps are in the
  `wasm-standalone` branch of `github.com/jerbob92/pdfium-binaries`, and that the build is
  "release, AGG, no V8/XFA". Its README says "The WebAssembly build will always be the
  latest PDFium version that we added support for", and 8057 is the newest PDFium its own
  tests run against.
- In go-pdfium's history the file was last changed on 2026-09-17: "Upgrade pdfium to 8057"
  (`db4291d`) replaced it, and "Also add wazy" (`7213682`), at 19:27 UTC, changed it once
  more, into the file of v1.21.3, which is 22 bytes longer.
- In pdfium-binaries, `wasm-standalone` took "Merge in chromium/8057" (`6179a5a`) that
  day, and its last commit, `b54b0ea`, "Update Emscripten patch to calculate WASI rights
  correctly", is from 19:17 UTC, ten minutes before the file's last change. Its scripts
  install Emscripten 6.0.9 since `3e24816` (2026-09-14), check out the head of the PDFium
  branch they are given, and configure the build with `pdf_enable_v8 = false`,
  `pdf_enable_xfa = false`, `pdf_use_partition_alloc = false` and
  `use_custom_libcxx = false`, without checking out Skia.

So the notices are taken from PDFium's branch `chromium/8057`, whose head was
`a5a7089234f121990b336b3841008009dca143bf`, from what PDFium's `DEPS` file names at that
commit, from pdfium-binaries at `b54b0ea`, and from Emscripten 6.0.9.

## What is in the file, and how each part was shown

A part has a notice here only when the file itself shows the part is in it. "String" means
text in the file's data, "export" and "import" a function the file names.

| Part | Shown by | Notice |
|---|---|---|
| PDFium | The exports are PDFium's API: `FPDF_InitLibraryWithConfig`, `FPDF_LoadPage`, `FPDFText_GetText` and more than 400 others. | `pdfium/LICENSE` |
| Anti-Grain Geometry 2.3 | PDFium draws with AGG or Skia, and this build has no Skia: no string of Skia's is in the file. Its code holds the constant `1e-30f` five times, AGG's `intersection_epsilon` and `curve_collinearity_epsilon`. AGG has no text of its own to find. | `agg/copying` |
| Little CMS 2.19 | Strings: "Unsupported device class '0x%x'", "Couldn't link the profiles". | `lcms/LICENSE` |
| OpenJPEG 2.5.4 | Strings: "Malformed JP2 file format: first box must be JPEG 2000 signature box", "OPJ_NUM_THREADS". | `openjpeg/LICENSE` |
| FreeType 2.14.3 | Strings: "FREETYPE_PROPERTIES", and its modules' names: "truetype", "t1cid", "pshinter", "psaux". | `freetype/LICENSE.TXT`, `freetype/docs/FTL.TXT` |
| ICU 78 | Strings: "icudt78l", "ICU_DATA". | `icu/LICENSE` |
| libjpeg-turbo 3.1.0 | Strings: "libjpeg-turbo version 3.1.0 (build )", "Copyright (C) 1991-2024 The libjpeg-turbo Project and many others". | `libjpeg-turbo/LICENSE.md`, `libjpeg-turbo/README.ijg` |
| zlib 1.3.2 | Strings: "1.3.2.1-motley", "incorrect header check", "invalid distance too far back". | `zlib/LICENSE` |
| Abseil | String: "../third_party/abseil-cpp/absl/base/throw_delegate.cc". | `abseil-cpp/LICENSE` |
| fast_float | The data holds the start of its table of powers of five, `0xeef453d6923bd65a, 0x113faa2906a13b3f, 0x9558b4661b6565f8, 0x4ac7ca59a424c507`, as `fast_table.h` has it. PDFium's `core/fxcrt/fx_string.cpp` calls `fast_float::from_chars`. | `fast_float/LICENSE-MIT` |
| pdfium-binaries | Exports: `pdfium_jpeg_encode` and `pdfium_jpeg_free`, which its `patches/wasm/jpeg_encode_shim.c` defines. | `pdfium-binaries/LICENSE` |
| Emscripten | Exports: `emscripten_stack_get_current`, `_emscripten_stack_restore`. Import: `emscripten_notify_memory_growth`. | `emscripten/LICENSE` |
| musl, Emscripten's C library | Strings: "-+   0X0x", "/usr/share/zoneinfo/", and its table of error messages in its own order: "Illegal byte sequence", "Domain error", "Result not representable", "Not a tty". | `musl/COPYRIGHT` |
| libc++, Emscripten's C++ library | Strings: the type names "NSt3__28ios_baseE" and "NSt3__26locale5facetE". | `libcxx/LICENSE.TXT` |
| libc++abi, in Emscripten | Strings: the type names "N10__cxxabiv117__class_type_infoE" and "N10__cxxabiv120__si_class_type_infoE". | `libcxxabi/LICENSE.TXT` |

The file also holds a notice that has no licence file anywhere in PDFium's tree. PDFium's
two built-in multiple master fonts, `ChromeSansMM` and `ChromeSerifMM`
(`core/fxge/fontdata/chromefontdata`), each carry these two lines in their PostScript, and
so does the file, twice:

```
% Copyright (c) 1990-1994 Adobe Systems Incorporated
% All Rights Reserved.
```

## What has no notice here, and why

Looked for, and not in the file: Skia, V8, libpng, libtiff, HarfBuzz, Brotli,
PartitionAlloc and simdutf, which PDFium uses in other builds. The file holds no string of
theirs, and the build's settings leave them out. dragonbox, which PDFium's `DEPS` fetches,
is not in it either: the file holds fast_float's table of powers of five and not
dragonbox's, whose numbers are rounded the other way.

Not shown either way, and so left out:

- **compiler-rt and libunwind**, which Emscripten links where the code needs them, and
  Emscripten's allocator, dlmalloc by default. None leaves text in the file. The first
  two are LLVM's, as libc++ is, and dlmalloc is in the public domain.
- **Chromium's additions to zlib**: `adler32_simd.c` and its like say "Copyright The
  Chromium Authors" and name the `LICENSE` file of Chromium's repository. go-pdfium's notes
  say the build turns them on for WebAssembly. A search for the numbers `adler32_simd.c`
  multiplies by found nothing that tells its code from any other.
- **`fthash.c`**, which FreeType's `LICENSE.TXT`, here, names as under a licence like the X
  Window System's. PDFium compiles it with the rest of FreeType's base, and nothing in the
  file tells whether the linker kept any of it.

## When go-pdfium changes

1. Find which PDFium its `pdfium.wasm` was built from: its release notes and README, the
   history of the file in its repository, and the history of the build scripts it names.
2. Read PDFium's `DEPS` at that revision for the revision of each part it fetches, and
   `third_party/*/README.pdfium` for the parts in its tree.
3. Look in the file for each part, as the table above does: `strings` for text, and the
   import and export sections for names. Add a part that is now there, and remove one that
   is gone.
4. Copy each licence file from its repository at the revision, as bytes: gitiles serves a
   file base64-encoded at `<repository>/+/<revision>/<file>?format=TEXT`. Never retype or
   reflow one.
5. Update `sources.toml`, with the new version and SHA-256 of the file and of each text,
   and this page. Then `make notices`, which lists the parts in `../README.md`.
