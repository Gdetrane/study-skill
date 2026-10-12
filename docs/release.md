# Releasing Lamplight

Lamplight ships two programs for Linux and macOS (amd64 and arm64) as ADR-0010 decides:
`study`, and `study-shelf`, the Knowledge base plugin that ships with it
([ADR-0012](adr/0012-knowledge-base-plugins-speak-one-contract.md), [docs/shelf.md](shelf.md)).
Everything is on the GitHub release, and `install.sh` installs from it. The release holds
an archive for each platform, their checksums, and deb and rpm packages; `go install` works
too. Every archive and package carries both programs, the bash, zsh and fish completions
and the `study(1)` man page.

The two are built apart, both without cgo: `study` links none of Shelf's code, nor SQLite,
nor the PDF reader, which are in `study-shelf` alone. They carry one version.

GoReleaser does the work; its configuration is `.goreleaser.yaml`, and
`.github/workflows/release.yml` runs it on version tags. A release needs no secret beyond
the token GitHub gives the workflow.

## What a release publishes

| File | What it is for |
|---|---|
| `lamplight_<os>_<arch>.tar.gz` | `install.sh`, or unpacking by hand. Holds `study`, `study-shelf`, `completions/`, `manpages/study.1.gz`, the README and `LICENSE`. |
| `checksums.txt` | The SHA-256 of every file of the release. `install.sh` checks its download against it. |
| `lamplight_<version>_<arch>.deb`, `lamplight-<version>-1.<arch>.rpm` | Installing system-wide by hand. |

`<os>` is `linux` or `darwin`, and `<arch>` is `amd64` or `arm64`. The archives carry no
version in their names, so
`https://github.com/mordor-forge/lamplight/releases/latest/download/lamplight_linux_amd64.tar.gz`
is always the latest release's. `install.sh` relies on these names, on `checksums.txt`, and
on `study`, `study-shelf` and `manpages/study.1.gz` inside the archive. Changing any of
them breaks the copies of the script that people have already downloaded.

The deb and rpm packages install:

- `/usr/bin/study` and `/usr/bin/study-shelf`;
- completions: bash in `/usr/share/bash-completion/completions/study`, zsh as `_study` in
  `/usr/share/zsh/vendor-completions` (deb) or `/usr/share/zsh/site-functions` (rpm), fish
  in `/usr/share/fish/vendor_completions.d/study.fish`;
- the man page `/usr/share/man/man1/study.1.gz`;
- the licence: `/usr/share/doc/lamplight/copyright` (deb) or
  `/usr/share/licenses/lamplight/LICENSE` (rpm).

`scripts/release-assets.sh` generates the completions and the man page before every
build, from `study completion <shell>` and `study man`.

The packages are named `lamplight`, not `study`, so they cannot clash with other packages
by name. The binary is still `/usr/bin/study`, a generic name: another package could
install a `study` binary, and the package manager would then report a file conflict.

We publish no Homebrew cask and no AUR package (ADR-0010). Anyone may package Lamplight
from these files.

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
   neither is installed when either does not run. It puts the man page in
   `~/.local/share/man/man1`. From a release that holds no `study-shelf`, one from before
   Shelf, it installs `study` alone and says so;
4. runs `study completion install` for the learner's shell, and goes on when that fails;
5. says when `~/.local/bin` is not on the `PATH`, or when another `study` comes first there,
   and prints `study setup` as the next step. It never runs `study setup`.

Running it again upgrades. Three variables change what it does, set for `sh`, as in
`curl ... | STUDY_VERSION=v2.0.0 sh`:

| Variable | Meaning |
|---|---|
| `STUDY_VERSION` | The release to install, such as `v2.0.0`. The latest when unset. A prerelease is installed only when named here. |
| `STUDY_INSTALL_DIR` | The folder `study` and `study-shelf` go in. The man page is installed only when this is a `bin` folder, in `share/man/man1` beside it. |
| `STUDY_DOWNLOAD_URL` | A folder of release files to download from instead of the GitHub release, as an `https://` or `file://` address, such as `file://$PWD/dist` after `make release-snapshot`. |

To remove what it installed: `study setup --remove`, `study completion uninstall`, then
delete `~/.local/bin/study`, `~/.local/bin/study-shelf` and
`~/.local/share/man/man1/study.1.gz`. The Study home is the learner's and stays, and so
does Shelf's index, in `~/.local/state/lamplight/knowledge-base-plugins`, until it is
removed by hand.

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
registers the `study-shelf` that was installed and has `study knowledge-base check` start
it. It needs no secrets and is not a required check.

The "Shelf and the snapshot on macOS" job takes the archives that job built, installs the
macOS one with `install.sh` on a Mac, has `study` check `study-shelf` there, and runs
Shelf's tests: the PDF reader is compiled to machine code on the computer it runs on, which
a build on Linux cannot show to work on a Mac. It is not a required check either.

The "Lamplight core" job, which is required, runs `internal/e2e`'s test of `install.sh`
against a local folder of release files that holds both programs: a good install, after
which the installed `study` checks the installed `study-shelf`, an upgrade, every refusal,
and the notes about the `PATH`.
