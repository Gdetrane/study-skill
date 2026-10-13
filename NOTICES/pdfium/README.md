# What is inside pdfium.wasm

`study-shelf` reads PDFs with PDFium. `github.com/klippa-app/go-pdfium` holds PDFium built,
as the WebAssembly file `internal/pdfium_wasm/pdfium.wasm`, which it embeds: the file is
inside every `study-shelf`. It arrives with no licence beside it and no word of what it was
built from. This folder holds the licences of what it is made of, each copied as its
repository holds it. `sources.toml` names every file here, where it came from and at which
revision, and the licence each part is under.

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
  branch they are given, without Skia, and configure the build with
  `pdf_enable_v8 = false`, `pdf_enable_xfa = false`, `pdf_use_partition_alloc = false`,
  `use_custom_libcxx = false`, `zlib_wasm_simd = true` and `jpeg_wasm_simd = true`.

So the notices are taken from PDFium's branch `chromium/8057`, whose head was
`a5a7089234f121990b336b3841008009dca143bf`, from what PDFium's `DEPS` file names at that
commit, from pdfium-binaries at `b54b0ea`, and from Emscripten 6.0.9.

## How the list of parts was made

Not from memory of what PDFium uses. The parts looked for are, at those revisions:

- every target the library depends on in PDFium's `BUILD.gn` files (the root, `core/*`,
  `fpdfsdk/*` and `third_party`), read with the build's settings;
- every `third_party/*/README.pdfium` that says "Shipped: yes", and every repository that
  `DEPS` fetches into `third_party`;
- what pdfium-binaries adds: its patches and the files it links in (`steps/03-patch.sh`,
  `steps/06-build.sh`);
- the libraries Emscripten links into what it builds: its C and C++ libraries,
  compiler-rt, libunwind and its allocator.

Each was then looked for in the file itself: by text in its data, by the names it imports
and exports, and, since most compiled code leaves no text, by numbers that only that code
has, a table in the data or a constant in a function. A part has a notice here when the
file shows it. The build's settings alone put no part in the list and take none out.

## What is in the file, and how each part was shown

"String" means text in the file's data, "export" and "import" a function the file names. A
function's number counts the imported ones first.

| Part | Shown by | Notice |
|---|---|---|
| PDFium | The exports are PDFium's API: `FPDF_InitLibraryWithConfig`, `FPDF_LoadPage`, `FPDFText_GetText` and more than 400 others. | `pdfium/LICENSE` |
| Anti-Grain Geometry 2.3 | PDFium draws with AGG or Skia, and this build has no Skia: no string of Skia's is in the file. Its code holds the constant `1e-30f` five times, AGG's `intersection_epsilon` and `curve_collinearity_epsilon`. AGG has no text of its own to find. | `agg/copying` |
| Little CMS 2.19 | Strings: "Unsupported device class '0x%x'", "Couldn't link the profiles". | `lcms/LICENSE` |
| OpenJPEG 2.5.4 | Strings: "Malformed JP2 file format: first box must be JPEG 2000 signature box", "OPJ_NUM_THREADS". | `openjpeg/LICENSE` |
| FreeType 2.14.3 | Strings: "FREETYPE_PROPERTIES", and its modules' names: "truetype", "t1cid", "pshinter", "psaux". | `freetype/LICENSE.TXT`, `freetype/docs/FTL.TXT` |
| FreeType's `fthash.c` | Function 6150, of 48 bytes, is its `hash_num_lookup`: it loads a number and makes `((b0*31 + b1)*31 + b2)*31 + b3` of its four bytes. No other function of the file has that shape. `LICENSE.TXT` says the file is under a licence of its own, whose text is at its head. | `freetype/src/base/fthash.c`, the file itself |
| HarfBuzz 14.3.1 | The data holds, once and in order, the seventeen table tags of `default_drop_tables` in its `hb-subset-input.cc`, from `morx` to `Sill`. PDFium's `core/fpdfapi/edit` depends on `//third_party/harfbuzz` whatever the settings, and subsets fonts with `hb_subset_or_fail`. No string names HarfBuzz. | `harfbuzz/COPYING` |
| ICU 78 | Strings: "icudt78l", "ICU_DATA". | `icu/LICENSE` |
| libjpeg-turbo 3.1.0 | Strings: "libjpeg-turbo version 3.1.0 (build )", "Copyright (C) 1991-2024 The libjpeg-turbo Project and many others". | `libjpeg-turbo/LICENSE.md`, `libjpeg-turbo/README.ijg` |
| zlib 1.3.2 | Strings: "1.3.2.1-motley", "incorrect header check", "invalid distance too far back". | `zlib/LICENSE` |
| Chromium, in zlib | Function 573 is `adler32_simd.c`, which is Chromium's own and names Chromium's `LICENSE`: it holds zlib's `BASE` (65521) and `NMAX` (5552), and the weights 32 down to 1 as eight vector constants. pdfium-binaries' `adler32_simd_wasm.patch` gives that file its WebAssembly path. | `chromium/LICENSE` |
| Abseil | String: "../third_party/abseil-cpp/absl/base/throw_delegate.cc". | `abseil-cpp/LICENSE` |
| fast_float | The data holds the start of its table of powers of five, `0xeef453d6923bd65a, 0x113faa2906a13b3f, 0x9558b4661b6565f8, 0x4ac7ca59a424c507`, as `fast_table.h` has it. PDFium's `core/fxcrt/fx_string.cpp` calls `fast_float::from_chars`. | `fast_float/LICENSE-MIT` |
| dragonbox | The data holds, once and in order, all 78 numbers of its table for `float` (`cache_holder<ieee754_binary32>` in `dragonbox.h`). PDFium's `core/fpdfapi/edit` depends on `//third_party/dragonbox`. | `dragonbox/LICENSE-Apache2-LLVM`, `dragonbox/LICENSE-Boost` |
| pdfium-binaries | Exports: `pdfium_jpeg_encode` and `pdfium_jpeg_free`, which its `patches/wasm/jpeg_encode_shim.c` defines. | `pdfium-binaries/LICENSE` |
| Emscripten | Exports: `emscripten_stack_get_current`, `_emscripten_stack_restore`. Import: `emscripten_notify_memory_growth`. | `emscripten/LICENSE` |
| musl, Emscripten's C library | Strings: "-+   0X0x", "/usr/share/zoneinfo/", and its table of error messages in its own order: "Illegal byte sequence", "Domain error", "Result not representable", "Not a tty". | `musl/COPYRIGHT` |
| libc++, Emscripten's C++ library | Strings: the type names "NSt3__28ios_baseE" and "NSt3__26locale5facetE". | `libcxx/LICENSE.TXT` |
| libc++abi, in Emscripten | Strings: the type names "N10__cxxabiv117__class_type_infoE" and "N10__cxxabiv120__si_class_type_infoE". | `libcxxabi/LICENSE.TXT` |
| compiler-rt, in Emscripten | Function 3148 holds `0x7504f33300000000`, the first guess of a reciprocal in its `fp_div_impl.inc`, as the division of two 128-bit `long double` has it. | `compiler-rt/LICENSE.TXT` |

Two things are in the file and have no notice in this folder:

- **dlmalloc**, Emscripten's allocator. The exported `malloc` compares what it is asked for
  with 244 and with `0xffffffbf`, dlmalloc's `MAX_SMALL_REQUEST` and the last size below
  its `MAX_REQUEST`. Its author released it to the public domain, in the words of CC0, at
  the head of Emscripten's `system/lib/dlmalloc.c`. It has no licence file and asks for no
  notice.
- **Two lines of Adobe's**, which have no licence file anywhere in PDFium's tree. PDFium's
  two built-in multiple master fonts, `ChromeSansMM` and `ChromeSerifMM`
  (`core/fxge/fontdata/chromefontdata`), each carry them in their PostScript, and so does
  the file, twice:

  ```
  % Copyright (c) 1990-1994 Adobe Systems Incorporated
  % All Rights Reserved.
  ```

## What was looked for and is not in the file

- **libtiff and bigint**: "Shipped: yes", and built only with XFA, which this build is
  without. No string of libtiff's is in the file.
- **FP16 and Highway**: "Shipped: yes", and fetched only with V8, which this build is
  without. No string of V8's is in the file.
- **Skia, libpng, Brotli, PartitionAlloc and simdutf**, which `DEPS` fetches or PDFium
  can be built with: under the build's settings nothing the library is built from depends
  on them, and the file holds no string of theirs.
- **libunwind**, which Emscripten links where the code needs it: the file holds no string
  of its own, and nothing else was found that is only its.
- **The rest of HarfBuzz**: its `COPYING` says a folder may have a licence of its own, and
  the one under `src` that has, `ms-use`, is data for a shaper. Only the subsetter was
  shown.

## When go-pdfium changes

1. Find which PDFium its `pdfium.wasm` was built from: its release notes and README, the
   history of the file in its repository, and the history of the build scripts it names.
2. Make the list of parts to look for as [above](#how-the-list-of-parts-was-made), from
   the `BUILD.gn` files, the `README.pdfium` files and `DEPS` at that revision, from the
   build scripts and from Emscripten's libraries.
3. Look in the file for each. `strings` finds text, and the import and export sections
   hold names. For code without text, take a table or a constant from the part's source
   and search the file's bytes for it: a table as it lies in memory, a constant as the
   instruction that loads it (`i32.const` is `41`, `i64.const` `42`, `f32.const` `43`,
   `v128.const` `fd 0c`, each followed by the number). Add a part that is now there, and
   remove one that is gone.
4. Copy each licence file from its repository at the revision, as bytes: gitiles serves a
   file base64-encoded at `<repository>/+/<revision>/<file>?format=TEXT`. Never retype or
   reflow one.
5. Update `sources.toml`, with the new version and SHA-256 of the file, and the licence
   and the SHA-256 of each text, and this page. Then `make notices`, which lists the
   parts in `../README.md`. A kind of licence that is new goes into `license` in
   `.goreleaser.yaml`, which a test holds to the list.
