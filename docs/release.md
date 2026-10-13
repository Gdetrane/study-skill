# Releasing Lamplight

Lamplight ships two programs for Linux and macOS (amd64 and arm64) as ADR-0010 decides:
`study`, and `study-shelf`, the Knowledge base plugin that ships with it
([ADR-0012](adr/0012-knowledge-base-plugins-speak-one-contract.md), [docs/shelf.md](shelf.md)).
Everything is on the GitHub release, and `install.sh` installs from it. The release holds
an archive for each platform, their checksums, and deb and rpm packages; `go install` works
too. Every archive and package carries both programs, the bash, zsh and fish completions,
the `study(1)` man page, and the licences and notices of what the two programs are built
from, in a folder named `NOTICES`.

The two are built apart, both without cgo: `study` links none of Shelf's code, nor SQLite,
nor the PDF reader, which are in `study-shelf` alone. They carry one version.

GoReleaser does the work; its configuration is `.goreleaser.yaml`, and
`.github/workflows/release.yml` runs it on version tags. A release needs no secret beyond
the token GitHub gives the workflow.

## What a release publishes

| File | What it is for |
|---|---|
| `lamplight_<os>_<arch>.tar.gz` | `install.sh`, or unpacking by hand. Holds `study`, `study-shelf`, `completions/`, `manpages/study.1.gz`, the README, `LICENSE` and `NOTICES/`. |
| `checksums.txt` | The SHA-256 of every file of the release. `install.sh` checks its download against it. |
| `lamplight_<version>_<arch>.deb`, `lamplight-<version>-1.<arch>.rpm` | Installing system-wide by hand. |

`<os>` is `linux` or `darwin`, and `<arch>` is `amd64` or `arm64`. The archives carry no
version in their names, so
`https://github.com/mordor-forge/lamplight/releases/latest/download/lamplight_linux_amd64.tar.gz`
is always the latest release's. `install.sh` relies on these names, on `checksums.txt`, and
on `study`, `study-shelf`, `manpages/study.1.gz` and `NOTICES` inside the archive. Changing
any of them breaks the copies of the script that people have already downloaded.

The deb and rpm packages install:

- `/usr/bin/study` and `/usr/bin/study-shelf`;
- completions: bash in `/usr/share/bash-completion/completions/study`, zsh as `_study` in
  `/usr/share/zsh/vendor-completions` (deb) or `/usr/share/zsh/site-functions` (rpm), fish
  in `/usr/share/fish/vendor_completions.d/study.fish`;
- the man page `/usr/share/man/man1/study.1.gz`;
- the licence: `/usr/share/doc/lamplight/copyright` (deb) or
  `/usr/share/licenses/lamplight/LICENSE` (rpm);
- the notices, beside it: `/usr/share/doc/lamplight/NOTICES` (deb) or
  `/usr/share/licenses/lamplight/NOTICES` (rpm).

`scripts/release-assets.sh` generates the completions and the man page before every
build, from `study completion <shell>` and `study man`.

The packages are named `lamplight`, not `study`, so they cannot clash with other packages
by name. The binary is still `/usr/bin/study`, a generic name: another package could
install a `study` binary, and the package manager would then report a file conflict.

We publish no Homebrew cask and no AUR package (ADR-0010). Anyone may package Lamplight
from these files.

## The notices

Lamplight is under the MIT licence. Its two programs are built from Go, from some fifty Go
modules, and, in `study-shelf`, from PDFium: under MIT, BSD and Apache licences and a few
others, most of which ask that their text goes wherever the program goes. `NOTICES`, at the
root of the repository, holds those texts, and its `README.md` lists every part, its
version, which program holds it and on which system. Every archive and package carries the
folder whole. It is the same in all of them: the two modules that only the macOS builds
link are in the Linux ones too, and the list says so.

The folder is checked in, so a change to it is read in a diff and a release needs no
network to make it. Most of it is written by a program:

```bash
make notices        # go run ./internal/notices/gen
```

It asks the go command what `./cmd/study` and `./cmd/study-shelf` link for each system a
release is built for, which is less than `go.mod` names, and copies from the Go module
cache, byte for byte, each module's licence files and the files that stand beside them.
Which files those are is decided by their names, and `kindOf` in `internal/notices` says
the rule: a licence is a file whose name holds `LICENSE`, `LICENCE` or `COPYING`, whatever
it begins with, or is `COPYRIGHT`, and beside it go the module's `NOTICE`, `AUTHORS` and
`CONTRIBUTORS`. Go's own licence stands for the standard library. Run it after any change
to a dependency, and commit what it writes.

One thing about each module is written by a person: the licence it is under, in the words
of the SPDX licence list, in `internal/notices/licences.go`. `make notices` refuses a
module that is not there, and one whose files do not read as the licence written down for
it. The list in `NOTICES/README.md` shows it for each part, with what a reader should know
besides: two modules have such a remark.

`internal/notices` has the tests that keep it so, in the required "Lamplight core" job.
One makes the folder again and fails on any difference: a module that is linked and has no
notice, a notice of a module that is no longer linked, a version or a text that changed.
It also fails when a module ships no licence file, and when a module other than go-pdfium
embeds a file, which may be a program built elsewhere: both wait for a person.

`NOTICES/pdfium` is that case, and is kept by hand. go-pdfium embeds PDFium built, as
`pdfium.wasm`, with no licence beside it. The folder's `README.md` says which PDFium the
file was built from, how the list of parts to look for was made, how each part was shown
to be in the file, and what is not in it; `sources.toml` names the licence of each part,
and the repository, the revision and the SHA-256 of every text. The test holds the folder
to the go-pdfium that `go.mod` names and to the `pdfium.wasm` in the module cache: **a new
go-pdfium fails it until the file has been looked at again**, as the last section of that
`README.md` describes.

The deb and rpm packages state as their licence every kind of licence in `NOTICES`, in
SPDX's words (`license` in `.goreleaser.yaml`). A test holds it to what is written down
for each part: it fails when a part is under a kind the packages do not name, and when
they name one that nothing is under.

## The install script

`install.sh`, at the root of the repository, is how the README says to install:

```bash
curl -fsSL https://raw.githubusercontent.com/mordor-forge/lamplight/main/install.sh | sh
```

It needs `curl`, `tar` and `sha256sum` or `shasum`, and no sudo. It:

1. picks the archive for the machine, and refuses any other platform with the `go install`
   command;
2. downloads the archive and `checksums.txt`, and installs nothing unless the archive's
   SHA-256 is the one listed;
3. puts `study` and `study-shelf` in `~/.local/bin`, replacing earlier ones by renaming,
   and only once both new ones have run `--version` there: the two are one release, and
   neither is installed when either does not run, and nothing is from an archive that
   holds only one. It puts the man page in `~/.local/share/man/man1`, and the archive's
   `NOTICES` folder in `~/.local/share/doc/lamplight`, in place of an earlier release's:
   the new folder is whole beside the earlier one before it takes its name, so a failure
   leaves the earlier one as it was, and the script says so;
4. runs `study completion install` for the learner's shell, and goes on when that fails;
5. says when `~/.local/bin` is not on the `PATH`, or when another `study` comes first there,
   and prints the next steps: `study setup`, which registers the `study-shelf` beside
   `study` as the Knowledge base plugin `shelf` among what it sets up, and
   `study knowledge-base check shelf`. It runs neither.

Running it again upgrades. Three variables change what it does, set for `sh`, as in
`curl ... | STUDY_VERSION=v2.0.0 sh`:

| Variable | Meaning |
|---|---|
| `STUDY_VERSION` | The release to install, such as `v2.0.0`. The latest when unset. A prerelease is installed only when named here. |
| `STUDY_INSTALL_DIR` | The folder `study` and `study-shelf` go in. The man page and the notices are installed only when this is a `bin` folder, in `share/man/man1` and `share/doc/lamplight` beside it. For any other folder the script says that the notices were not installed, and that they are in the archive. |
| `STUDY_DOWNLOAD_URL` | A folder of release files to download from instead of the GitHub release, as an `https://` or `file://` address, such as `file://$PWD/dist` after `make release-snapshot`. |

To remove what it installed: `study setup --remove`, which takes Shelf out of the registry
when setup registered it, `study completion uninstall`,
`study knowledge-base remove shelf` if you registered Shelf yourself, then
delete `~/.local/bin/study`, `~/.local/bin/study-shelf`,
`~/.local/share/man/man1/study.1.gz` and `~/.local/share/doc/lamplight`. The Study home
is the learner's and stays, and so does Shelf's index, in
`~/.local/state/lamplight/knowledge-base-plugins`, until it is removed by hand.

## Before the first release

- [ ] Rename the repository to `lamplight` (#18), so the module path
  `github.com/mordor-forge/lamplight/v2` resolves and the install script's addresses exist.
- [ ] Merge `v2` into `main`, where releases are tagged and where the install command reads
  `install.sh` from. GitHub runs the workflow file of the tagged commit, so a `v*` tag on
  any commit that has `release.yml` publishes a release: tag only release commits, such as
  a release candidate on `v2` or a release on `main`.
- [ ] Check the maintainer named in `.goreleaser.yaml` (deb and rpm metadata).

## Cutting a release

1. Make sure `main` is green and `make release-snapshot` succeeds locally (it needs
   `goreleaser` on the PATH, or `GORELEASER=/path/to/goreleaser make release-snapshot`).
2. Tag the release commit on `main` and push the tag:

   ```bash
   git tag -a v2.0.0 -m "Lamplight v2.0.0"
   git push origin v2.0.0
   ```

3. Watch the Release workflow. It builds, and publishes the GitHub release with notes from
   the merged pull requests.
4. Check the result: run the install command above, or install the deb or rpm from the
   release, then `study --version`.

Tags start with `v`. A tag with a prerelease suffix (`v2.1.0-rc.1`) publishes a GitHub
prerelease, which is never the latest release: the install script installs it only when
`STUDY_VERSION` names it.

## go install

`go install github.com/mordor-forge/lamplight/v2/cmd/study@latest` keeps working without
GoReleaser: the module path carries `/v2`, so the `v2.x.y` tags are the module's versions.
It builds from source with the user's Go toolchain (Go 1.26 or later), prints the tag as
its version, and installs no completions or man page; `study completion install` adds the
completions afterwards. Shelf is installed the same way, as a second command:
`go install github.com/mordor-forge/lamplight/v2/cmd/study-shelf@latest`.

## Checks on every pull request

CI's "Release snapshot" job runs `shellcheck` on `install.sh`, validates the configuration
with `goreleaser check`, builds every artefact with
`goreleaser release --snapshot --clean --skip=publish`, checks that the archive of each
platform holds both programs and that an archive, a deb and an rpm contain the
completions, the man page and the licence, and installs the snapshot with `install.sh`
into a temporary home. There it checks that the released `study` links nothing of Shelf's,
has `study setup` register the `study-shelf` that was installed, as it does for a learner
(with `--force`, since that home is a temporary folder, and with nothing else on `PATH`,
so that no agent of the runner's is set up), and has `study knowledge-base check` start
it. It needs no secrets and is not a required check.

The same job checks the notices in what was built: that each of the four archives holds
the `NOTICES` folder file for file as the repository does, that every deb and rpm holds
each of its files, that the rpm states a licence that names more than MIT, that
`install.sh` installs the folder whole, and that every module the installed `study` and
`study-shelf` name (`go version -m`) has its folder there.

The two "Shelf and the snapshot on …" jobs, one on macOS and one on Linux on arm64, take
the archives that job built, install the one for their platform with `install.sh`, have
`study` check `study-shelf` there, check the notices against the two programs as above,
run Shelf's tests, and run the conformance suite against the `study-shelf` that was
installed, with PDFs: the PDF reader is compiled to machine code on the computer it runs
on, which a build on another kind of computer cannot show to work. The snapshot job does
the same for Linux on amd64. Neither is a required check. The programs for macOS on Intel
are built and run nowhere.

The "Lamplight core" job, which is required, runs `internal/e2e`'s test of `install.sh`
against a local folder of release files that holds both programs: a good install, after
which the installed `study` checks the installed `study-shelf`, an upgrade, every refusal,
the notices, and the notes about the `PATH`. It also runs `internal/notices`, which holds
`NOTICES` to what the two programs link.
