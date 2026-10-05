# 0010. One Release Channel and an Install Script

## Status

Accepted. Replaces the packaging channels of ADR-0008; the rest of ADR-0008 stands.

## Context

ADR-0008 decided that v2.0 would publish a Homebrew cask in our own tap and an AUR `-bin`
package, next to deb and rpm packages and `go install`. Before the first release, the
maintainer decided not to keep those two:

- A tap is a second repository, and a token with write access to it stored as a secret. An
  AUR package is an account, an SSH key stored as a secret, and a package to answer for.
- The cask needed a hook that removes macOS's quarantine attribute after installing, because
  the binary is not notarized.

Neither buys much. `study` is one static binary, and ADR-0008's contract is only that
`study` is on the `PATH`: `study setup` and `study completion install` do everything after
that. GoReleaser already publishes the GitHub release, with archives for every platform and
their checksums, using only the token GitHub gives the workflow.

## Decision

The GitHub release is the only place Lamplight is published. It holds one archive for each
of Linux and macOS on amd64 and arm64, `checksums.txt`, and the deb and rpm packages, which
need no secret and stay as plain downloads, in no package repository.

`install.sh`, at the root of the repository, is the way to install that the README gives:

```sh
curl -fsSL https://raw.githubusercontent.com/mordor-forge/lamplight/main/install.sh | sh
```

- It downloads the archive for the machine and `checksums.txt`, and installs nothing unless
  the archive's SHA-256 is the one listed.
- It puts `study` in `~/.local/bin`, or the folder `STUDY_INSTALL_DIR` names, without sudo,
  and replaces an earlier `study` by renaming, never by writing into it, and only once the
  new one has run `--version` there: a `study` that does not run installs nothing. When that
  folder is a `bin` folder, the man page goes in `share/man/man1` beside it, where `man`
  looks.
- It then runs `study completion install`, and goes on when that fails. It never runs
  `study setup`: it prints it as the next step, and says so when the folder is not on the
  `PATH` or another `study` comes first there.
- Running it again upgrades. `STUDY_VERSION` installs one release instead of the latest.
- A platform with no build is refused with the `go install` command.

Archive names carry no version (`lamplight_<os>_<arch>.tar.gz`), so the script downloads
`releases/latest/download/<name>` without having to ask which release is the latest.

`go install` keeps working, as in ADR-0008. We publish no Homebrew cask and no AUR package.
Anyone may package Lamplight from the release files; such a package is theirs to keep.

## Consequences

- A release needs no secret of ours, no second repository and no account elsewhere.
- Nothing updates `study` by itself, and nothing tells the learner that a release is out:
  they run the script again. A notice in `study doctor` or a `study` command that upgrades
  could come later.
- Some people will not pipe a download into a shell. They can read the script first,
  download an archive or a package by hand, or use `go install`. The checksum catches a
  damaged or cut-short download; it comes from the same release as the archive, so trust
  rests on GitHub and HTTPS, as it did with the tap.
- macOS needs no quarantine hook: a file that `curl` downloads carries no quarantine
  attribute.
- The names of the archives and of `checksums.txt`, and the files inside an archive, are
  now a contract with every copy of the script already downloaded. CI installs each
  snapshot build with the script, and `internal/e2e` runs it against a local folder of
  release files.
- The archive and `checksums.txt` are two downloads. A release published between them makes
  the checksum fail; the script then installs nothing, and running it again works.
- deb and rpm packages install completions and the man page system-wide, and are upgraded by
  installing the next file by hand.
