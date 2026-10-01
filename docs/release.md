# Releasing Lamplight

Lamplight ships the `study` binary for Linux and macOS (amd64 and arm64) as ADR-0008
decides: a Homebrew cask in our own tap, an AUR `-bin` package, deb and rpm packages, plain
archives on the GitHub release, and `go install`. Every package carries the bash, zsh and
fish completions and the `study(1)` man page.

GoReleaser does the work; its configuration is `.goreleaser.yaml`, and
`.github/workflows/release.yml` runs it on version tags.

## What a release publishes

| Where | Name | Needs |
|---|---|---|
| GitHub release | `lamplight_<version>_<os>_<arch>.tar.gz`, `checksums.txt` | nothing extra |
| GitHub release | `lamplight_<version>_<arch>.deb`, `lamplight-<version>-1.<arch>.rpm` | nothing extra |
| Homebrew | cask `lamplight` in `mordor-forge/homebrew-tap` | `HOMEBREW_TAP_TOKEN` |
| AUR | `lamplight-bin` | `AUR_KEY` |

Each package installs:

- `/usr/bin/study` (Homebrew links it into its own prefix);
- completions: bash in `/usr/share/bash-completion/completions/study`, zsh as `_study` in
  `/usr/share/zsh/vendor-completions` (deb) or `/usr/share/zsh/site-functions` (rpm, AUR),
  fish in `/usr/share/fish/vendor_completions.d/study.fish`;
- the man page `/usr/share/man/man1/study.1.gz`.

`scripts/release-assets.sh` generates the completions and the man page before every
build, from `study completion <shell>` and `study man`.

The Homebrew tap and the AUR are each published only when their secret is set, and never
for a prerelease tag (`v2.1.0-rc.1`). Without the secrets, a release still publishes the
GitHub release with every archive and package; the generated cask and PKGBUILD are then in
the workflow's `dist/` folder.

Packages are named `lamplight` (`lamplight-bin` on the AUR), not `study`, so they cannot
clash with other packages by name. The binary is still `/usr/bin/study`, a generic name: in
October 2026 no Arch or AUR package was named `study` or `lamplight`, but another package
could install a `study` binary, and the package managers would then report a file
conflict.

## Before the first release

- [ ] Rename the repository to `lamplight` (#18), so the module path
  `github.com/mordor-forge/lamplight/v2` resolves and the cask and PKGBUILD point at the
  right releases.
- [ ] Merge `v2` into `main`, where releases are tagged. GitHub runs the workflow file of
  the tagged commit, so a `v*` tag on any commit that has `release.yml` publishes a release:
  tag only release commits, such as a release candidate on `v2` or a release on `main`.
- [ ] Add a `LICENSE` file (the README says MIT); the archives and packages should ship it.
- [ ] Create the tap repository `mordor-forge/homebrew-tap` (public, with a `Casks/`
  folder).
- [ ] Create a fine-grained token with contents write access to that repository only, and
  store it as the Actions secret `HOMEBREW_TAP_TOKEN` on the Lamplight repository.
- [ ] Create an AUR account, register a password-less SSH key on it, claim
  `lamplight-bin` by pushing to `ssh://aur@aur.archlinux.org/lamplight-bin.git`, and store
  the private key as the Actions secret `AUR_KEY`.
- [ ] Check the maintainer named in `.goreleaser.yaml` (deb, rpm and AUR metadata).

## Cutting a release

1. Make sure `main` is green and `make release-snapshot` succeeds locally (it needs
   `goreleaser` on the PATH, or `GORELEASER=/path/to/goreleaser make release-snapshot`).
2. Tag the release commit on `main` and push the tag:

   ```bash
   git tag -a v2.0.0 -m "Lamplight v2.0.0"
   git push origin v2.0.0
   ```

3. Watch the Release workflow. It builds, publishes the GitHub release with notes from the
   merged pull requests, and updates the tap and the AUR when their secrets are set.
4. Check the result: `brew install --cask mordor-forge/tap/lamplight`, `yay -S
   lamplight-bin`, or install the deb or rpm from the release, then `study --version`.

Tags start with `v`. A tag with a prerelease suffix publishes a GitHub prerelease and skips
the Homebrew tap and the AUR.

## go install

`go install github.com/mordor-forge/lamplight/v2/cmd/study@latest` keeps working without
GoReleaser: the module path carries `/v2`, so the `v2.x.y` tags are the module's versions.
It builds from source with the user's Go toolchain (Go 1.26 or later), prints the tag as
its version, and installs no completions or man page; `study completion install` adds the
completions afterwards.

## Checks on every pull request

CI's "Release snapshot" job validates the configuration with `goreleaser check`, builds
every artefact with `goreleaser release --snapshot --clean --skip=publish`, and checks that
the archives and the deb package contain the completions and the man page. It needs no
secrets and is not a required check.
