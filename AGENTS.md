# Agent Guide

## Project Overview

This repository holds Lamplight v2: a Go program, `study`, whose core owns all study state
(Topics, the Syllabus, Checks and Attempts, Cards, the History) in plain files inside the
learner's Study home. A command line and an MCP server are thin adapters over the core, and
the `lamplight` skill, embedded in the binary, teaches agents to tutor with them. A second
program, `study-shelf`, is Shelf, the Knowledge base plugin that ships with Lamplight:
`study` starts it and links none of it. Until v2.0 is released it all lives on the `v2`
branch, and `main` keeps v1, the Markdown study skill.

Read these before changing behaviour:

- `GLOSSARY.md`: the glossary. Use its terms in code, comments and docs.
- `docs/design/lamplight-v2.md`: the design.
- `docs/adr/`: the decisions behind it, 0004 onwards. 0001 and 0002 are superseded by 0004.
  0003 was written for v1's Python book catalog; the Go Library (`internal/library`), which
  replaced that catalog, keeps its walk-once and case-insensitive extension rules and
  replaces its title clean-up with one shared normalizer. 0010 replaces the packaging
  channels of 0008: releases are on GitHub alone, installed by `install.sh`. 0011 replaces
  the v2.0 part of 0007: Lamplight does not use NotebookLM. 0012 decides what is built in
  its place: Knowledge base plugins that speak one contract over MCP, with `study` as their
  client, and Shelf as the first. A Topic's Knowledge base is `none`, or a plugin it names.
- `docs/cli.md`: the command-line contract that scripts and agents rely on. A test checks
  that it mentions every command and flag.
- `docs/knowledge-base-contract.md`: the contract a Knowledge base plugin speaks, for
  whoever writes one. `internal/plugincontract` is the same contract in Go, and a test
  keeps the two in step: every field, kind, code and limit, and every example on the page.
- `docs/shelf.md`: Shelf for the learner: what it reads and what it does not, with the
  reason it gives for each, how it searches, its settings file and Recipes, how to serve
  a model for it, the measurements behind its choices, and where its index is. Keep it
  true when Shelf changes.

## Development Commands

Use the root `Makefile` as the stable entrypoint:

```bash
make test         # go test -race ./...
make lint         # gofmt check and go vet
make coverage     # tests with a coverage profile
make build        # build ./study and ./study-shelf
```

Narrow checks:

```bash
go test ./internal/core -run TestCreateTopic
go test ./internal/cli -run TestJSONOutput
go test ./internal/cli -update      # rewrite CLI golden files, then review the diff
go test ./skills/...                # the skill against the real tools and commands
go test ./internal/e2e              # the learner loop, the v2.0 acceptance walkthrough, install.sh
go test ./internal/shelf/...        # Shelf, and the conformance suite against a built study-shelf
go test -short ./internal/shelf/... # the same without what builds study-shelf: seconds, not minutes
go test ./internal/shelf/index -run '^$' -bench Nearest -benchtime 10x   # what a search by vectors costs
go test -shuffle=on ./...           # no test may depend on another's order
GOOS=darwin go vet ./...            # macOS is supported too
```

## Code Conventions

- Go 1.26 or later (go-fsrs v4 needs it); the module is `github.com/mordor-forge/lamplight/v2`.
- Domain logic lives in `internal/core`. `internal/cli` and `internal/mcpserver` only
  parse input, call the core and render results. The one exception is what changes the
  Knowledge base plugin registry, which the core must not hold (see below).
- Core errors are `*core.Error` with a documented code (`invalid_argument`, `corrupt`,
  `busy`, …); adapters map codes to exit codes and MCP tool errors. The registry's packages
  carry the same codes on an error type of their own, which `core.CodeOf` reads.
- Every write is an Event in the Topic's History, written first, then the content, under
  the Topic's lock. Event types are versioned by name: a new payload shape is a new type.
  Domain time is the Event's `wall`; `time` only orders Events. `study topic remove` and
  `study topic restore` record none: they move a Topic's folder on one computer and change
  nothing inside it. Nor do `study knowledge-base add` and `remove`: the registry they
  change is one computer's and no Topic's. Nor does `study source index`: whether a Source
  is indexed is the plugin's to say on one computer, and is in no file of the Topic.
- Conflicts between machines are flagged in `status`, never resolved silently.
- Files in a Topic are read and written through `os.Root`. git runs only through the
  hardened `internal/checkpoint` package or `gitCommand`, never with the caller's `GIT_*`
  environment, and never runs a program the repository's configuration names (ADR-0009).
- No MCP tool runs learner or agent code; Checks run only through `study check` in the
  agent's own shell (ADR-0009). A test enforces it.
- The Knowledge base plugin registry says which program or URL each plugin's name stands
  for (ADR-0012). No MCP tool registers, changes or removes a plugin, or takes a command
  line or a URL for one, and the build keeps it so: `internal/pluginregistry` only reads
  the registry, and the core and the MCP server link it; `internal/pluginregistry/registrar`
  changes it, and only `internal/cli` may import that. Never import the registrar from the
  core or the server, and never give the core a way to write the registry: a test fails
  when `go list -deps ./internal/mcpserver` contains it.
- Nothing the registry depends on may be in the Study home or be reached through it: not
  Lamplight's configuration folder, not a plugin's program. That is decided by file
  identity, one hop of the path at a time, on folders that are then used as they were
  opened (`regfile.Walker`). Never decide it from a path's name, and never open by name
  again what was checked. A folder on the way is opened to be passed through where it may
  not be listed (`regfile.Dir`, not `os.Root`, which opens a folder to list it). A walk
  that cannot be finished has decided nothing: never take a path it could not follow for
  one outside the Study home (`Walker.Follow`), and test each new caller with a folder
  that cannot be passed through.
- A Knowledge base plugin is started, or reached, only through `internal/pluginclient`,
  which only the core imports, besides the conformance suite in `internal/plugintest`; the
  MCP server reaches a plugin only through the core. A command is the program
  `pluginregistry.OpenCommand` checked, run without a shell in the registry's folder
  (`pluginregistry.OpenFolder`), never in the Topic or the Study home, with pipes of its
  own and no other file of `study`'s, with a small environment `study` makes and never the
  caller's (`childEnviron`: a list of what goes in, each variable only when
  `pluginregistry.Guard` knows it leads nowhere inside the Study home), with a folder of
  its own in the learner's state folder, outside the Study home, and stopped with its
  process group when its Connection is closed.
- Four things in the core start a plugin, each through `startPlugin`, and each stops it
  before it returns: `CheckPlugin` (`study knowledge-base check`), `IndexSources`
  (`study source index`), `askPlugins`, which `Status` and `ListSources` use to ask what a
  Topic's plugin has indexed, and `startTopicPlugin`, which `SearchEvidence` and
  `recordQuoted` use to search a Topic's Sources and to read the Passages a quote is
  checked against. The last two are reached by MCP tools, and they ask a plugin's
  `status`, `search` and `passage` tools and nothing else. **No MCP tool has a plugin
  index, or stop indexing**: the server runs outside the agent's sandbox, and a plugin
  that indexed for it would read any file the agent named as a Source. Three tests in
  `internal/mcpserver` keep it so.
  `TestNoToolHasAPluginIndex` registers plugins that record what they are asked, has the
  Topic name one and hold a Source it has not indexed from before the first tool is
  called, calls every tool (`everyToolOn`: add a new tool there) and fails when the plugin
  was asked `index` or `cancel`, or anything that is not in `askedOfAPlugin`, or when the
  registry changed; `toolsThatAskAPlugin` says, for each tool that asks a plugin anything,
  exactly which of its tools are asked and in which order, in each way the session takes,
  and the session takes the ways that end in a refusal too.
  `TestTheCoreAsksAPluginToIndexInOnePlace` reads the core: a Connection's `Index` is
  called in `indexRun.askToIndex` alone, which only `IndexSources` reaches, which the
  server cannot name, its `Cancel` in `indexRun.ended` alone, its `Search` in
  `topicPlugin.searchPassages` and its `Passage` in `topicPlugin.readPassage`
  (`askedOfAConnection`). `TestTheClientAsksEachToolInOneMethod` holds the client to
  naming each tool in the one method called after it, and a Connection to the methods
  those tests know: say there what a new one is. Change them on purpose when a tool
  comes to ask a plugin something else, name nothing else in the core `Index`, `Cancel`,
  `Search` or `Passage`, and keep `reflect` out of the core: it calls by a name those
  tests cannot read.
- Nothing that shows where the learner is waits for a plugin. `askPlugins` has
  `core.PluginGlanceTimeout`, two seconds, for the registry, the start and the answers
  together, and shows the Sources as not known when it is missed. Opening a Session asks
  no plugin anything, and a command that only needs to know which Topic it is about calls
  `ActiveTopic`, not `Status`. Never give a plugin the client's thirty seconds on such a
  path. The one action `status` names is a command that would help: `study source index`
  only for a Source it would hand over and the plugin has not tried, or calls `outdated`,
  never for one that failed or whose file is missing, changed or unreadable. A search, and a refused
  recording, decide it by the same code (`indexAsk`): never write that rule a second time.
- A search of a Topic's Sources, and the reading of a Passage to check a quote, have
  `core.PluginEvidenceTimeout`, forty-five seconds, for the registry, the start and every
  answer together (`PluginOptions.EvidenceTimeout` in tests), and the plugin is asked
  before the Topic's lock is taken. A search returns only Passages of Sources the Topic
  has and the plugin holds indexed with the Source's own hash. Evidence is recorded as
  checked only when its quote is in the text of the Passages it names
  (`internal/core/quote.go` says what may differ): never widen what that lets through
  without saying so in ADR-0012 and `docs/cli.md`. One process has at most `mostAtOnce`
  copies of a plugin searching (`takeTurn`): whatever takes a turn gives it back on every
  way out, and `TestATurnIsGivenBackHoweverItEnds` lists the ways. A search's answer is
  bounded (`MaxSearchTextBytes`), and a Passage is handed on through `withoutTags`.
- A collection is named by `collectionName`: the Topic's id and a hash of the Study home's
  real path and the id, never the id alone, since a plugin's folder is one for the plugin
  on a computer. A Source is its content: a file goes to a plugin only when it is the file
  the Topic records, read just before it is handed over, and counts as indexed only when
  the plugin's hash is the Source's. The contract has a plugin keep nothing of a file whose
  bytes are not the hash it was handed: a file inside the Study home is the agent's to
  exchange for a link once `study` has read it.
- A plugin is not trusted with `study`'s time, memory or terminal. Every call has a
  deadline, a message is read up to a limit, every answer is checked against the contract
  (`plugincontract`'s `Validate`), and text a plugin wrote reaches an error only through
  the client's `clean`. A Passage's text is returned as it is: print it through
  `printable()`.
- `study` links nothing of Shelf's (ADR-0012): not `internal/shelf`, not SQLite, not the
  PDF reader, not what reads web pages. `TestStudyLinksNothingOfShelf` fails when
  `go list -deps` of `cmd/study`, the core, the command line, the MCP server or the plugin
  client contains any of them: a library added for Shelf is added to that test's list.
  Shelf imports `internal/plugincontract` and nothing else of `study`'s.
- Shelf is held to the contract page like any plugin, and to these besides:
  - `study` keeps no plugin running: it starts Shelf for every question it has, a glance
    at what is indexed among them, and stops it. So Shelf does nothing slow when it
    starts, no PDF reader set up and no collection opened before it is asked about
    (`TestAGlanceIsQuick`), and whatever one Shelf has to tell the next is in the
    collection: what is indexed, why an indexing failed, what is being indexed now.
  - Only MCP goes to its standard output, it sends `study` no request, and it exits when
    its input closes, within the quarter of a second `study` gives it after a glance,
    whatever it is indexing (`shelf.StopsWithin`, and no longer: the index is whole
    wherever the work was).
  - A Source that another copy of Shelf is indexing is `indexing` to every copy
    (`index.Work`): a row in the collection, which counts only while the copy that wrote
    it holds a lock the system lets go of when the program ends. Never record work in
    progress without that lock: a Shelf that was killed would be indexing for ever.
  - A file is indexed only when its bytes are the hash `study` handed over, hashed from
    the open file its pages are read from, before and after. `study` counts a Source as
    indexed when Shelf gives that hash.
  - It reads where it may not write: a glance from an agent's sandbox opens a collection
    for reading (`index`'s connector), and what would write fails with the reason.
  - It writes in its own folder and nowhere else: not where it is started, not in a
    temporary folder. Everything Shelf itself does there goes through the folder as it was
    opened (`index.Folder`, an `os.Root`). SQLite opens its files by name, so every
    connection goes through `index`'s connector, which looks at the names first, has
    SQLite open the file Shelf made (`mode=rw`, never `rwc`), and asks SQLite which file
    it opened before anything is written. Never open a collection's file another way.
  - A Source is read only by `index`, and a web page fetched only then. `search`,
    `passage` and `status` read the index.
  - A Source's content is in the index whole or not at all: new content replaces the old
    in one transaction (`Collection.Replace`). A Passage's id is made of its Source, the
    content's hash, its place and its text (`index.PassageID`), so it never comes to name
    another text: do not change how it is made.
  - A Passage is the Source's own text, on one page. `cut.Clean` drops what is no text and
    writes no word; nothing summarises or rewrites. A word divided at the end of a line is
    joined or keeps its hyphen as the Source's own words say (`cut.WordsOf`).
  - Shelf fetches no page from the computer it runs on unless it was started with
    `--allow-loopback`, and none from a link-local address, checked as the connection is
    made (`web.Fetcher`): the agent chooses which addresses are Sources.
  - A PDF is read by PDFium inside wazero (`pdf.Read`), which can open no file and reach
    no network. Never read a Source with a program found on the `PATH`.
  - Shelf's settings are a file of the learner's, `shelf.toml` in Lamplight's
    configuration folder (`settings.Load`): an embedding endpoint, a Recipe or a model's
    own settings, a key, a reranking endpoint. Nothing is a setting in an argument or in
    the environment, which says only where that folder is. A file Shelf cannot use ends
    `study-shelf` with the reason on standard error, never a quiet search by keyword, and
    the reason names a line or a setting and repeats no value: a value may be the key. A
    file inside Shelf's own folder, or reached through it, is refused, by what the folders
    are and not by their names: the learner may have let an agent's sandbox write there.
  - An endpoint is asked only through `internal/shelf/endpoint`. A request holds the model,
    the texts and what the settings name, and no key of Shelf's own choosing: some servers
    refuse a key they do not know. A question and a Passage are marked by text Shelf
    writes or by fields of the request, never both (`endpoint.Marking`). Every request has
    a deadline, every answer is read up to a limit and checked (the number of vectors,
    their size) before it is used, no redirect is followed, and what goes wrong is an
    `*endpoint.Problem`, which names the endpoint by its host and never holds the key or
    the rest of the address: a server's own words reach it only through `caller.said`.
    A list of an answer is decoded through `counted`, and a vector through `numbers`:
    what an answer decodes to is bounded by what was asked for, never by its bytes alone.
    An `http` endpoint is asked without the environment's proxy.
  - An endpoint is the learner's choice, so it is not held to `web.Fetcher`'s rule, and is
    usually on the computer Shelf runs on. Never loosen that rule for Sources to make an
    endpoint reachable, and never fetch a Source's page through the endpoint's client.
  - Only indexing and searching ask an endpoint. Starting, `contract` and `status` ask
    none (`TestAGlanceIsQuick` counts the requests): `contract` lists `embedding` because
    the settings name an endpoint, whether or not it answers.
  - A Source is indexed with a vector for each of its Passages or not at all: the vectors
    are written in the transaction that writes the Passages (`Collection.Replace`), or
    given to Passages that are there in one of their own (`Collection.Embed`), which
    leaves the Passages and their ids as they are. An endpoint that fails leaves the
    Source `failed`. A question's vector is compared only with vectors made the same way
    (`index.Made`: model, marking, size).
  - A Source that is indexed and has no such vectors, though the settings name an
    endpoint, is `outdated`, with the hash of what is still found and the reason
    (`Shelf.behind`): the contract's word for what is indexed and to be indexed again.
    `study` shows the reason, names `study source index` as what to do next, and still
    searches the Source. Never say `failed` of what is found and only wants its vectors,
    and keep the reason under the 300 bytes `study` shows of one.
  - A search is by keyword always, and an endpoint that fails takes from it only what it
    would have added: the answer then says so in Shelf's own fields beside the Passages
    (`search`, `reranked`, `notice`). The two rankings are taken two of the embeddings'
    for one of keyword's (`merge`), which was measured against reciprocal rank fusion
    (docs/shelf.md): do not change it without measuring again.
- Every file Lamplight owns carries a `format` number, and newer formats are refused. A
  collection's file carries `index.Format` in its header, and Shelf's settings file names
  `settings.Format` in its first setting.
- Text from files or the History goes through `printable()` before it reaches a terminal;
  `--json` output never triggers terminal queries.
- The learner never sees overdue counts, "behind" or streaks: show where they are and one
  next action.
- Use conventional commit prefixes such as `fix:`, `feat:`, `docs:`, `test:` and `ci:`.
- Do not commit built binaries, Study homes or agent assessment reports.

## Tests

- Tests go through each package's public interface, with a fixed clock, injected IDs and
  a temporary Study home. CLI output is checked with golden files under `testdata/`.
- New Event types get crash-injection tests at every crash point, and a check that the dry
  run reports what the real run does.
- Use a temporary `HOME`, `XDG_*` folders and `STUDY_HOME`. Never touch the real
  `~/.agents`, `~/.claude`, `~/.codex`, `~/.config`, a real Study home, or the installed v1
  skill in `~/.agents/skills/study`.
- Tests of `study setup` and the Claude Code plugin run with a `PATH` holding only fake
  `claude` and `codex` scripts; a guard fails the test if the real ones could be reached.
  Never run the real agents with commands that change anything.
- Every git configuration a test writes includes `[maintenance]` `auto = false`: git's
  background maintenance would otherwise still be writing to `.git` while the test removes
  its folder. A test that writes a `.gitconfig` also points `GIT_CONFIG_GLOBAL` at it and
  sets `GIT_CONFIG_NOSYSTEM=1`, so git run by the test reads that file whatever the
  caller's environment says (the core strips `GIT_*` on its own).
- A package whose tests run git or the go command themselves clears the caller's `GIT_*`
  variables in `TestMain`: a commit hook that runs the tests sets `GIT_INDEX_FILE` and
  `GIT_DIR`, which point those commands at the repository being committed.
  `TestTheTestsIgnoreTheCallersGitEnvironment` checks it in each such package. Tests that
  build `study` pass `-buildvcs=false`, so the build never needs git to report on the checkout.
- The test plugin (`internal/plugintest`) is the only Knowledge base plugin a test of
  `study` starts, and it is the test binary itself: call `plugintest.Main()` first in
  `TestMain`, and register `plugintest.Command(...)`, whose flags make the plugin
  misbehave. Never start a plugin that is installed on the computer, and never reach an
  address that is not a test server on this computer. Give the plugin the test's
  environment (`core.Options.Plugins.Environ`) and short deadlines. A plugin of any kind
  is held to the contract with `plugintest.Conformance`. Started with `--record FILE`, the
  test plugin writes what it is asked beside the record (`plugintest.ReadCalls`): assert
  on that, the collection and the Source of each call, rather than on what `study` says it
  asked.
- Shelf's own tests, and the test of `install.sh`, start the `study-shelf` built from the
  checkout (`shelftest.Shelf`, which a `-short` run skips), on a `plugintest.Computer`.
  No test reads a real book: a PDF is written for the test (`shelftest.PDF`), and a web
  page is served by the test. A new point where a write of the index could be cut short
  gets a name in `index` and a row in the test that kills Shelf there. The tests that
  run a built `study-shelf` for a minute, the conformance suite with and without endpoints
  and the kills, run beside each other (`t.Parallel`), each on a computer of its own: the
  package would otherwise near go's ten minutes on a busy computer. A test that measures
  by the clock, as `TestAGlanceIsQuick` does, is never one of them.
- An endpoint in a test is `shelftest.Endpoint`, a server on this computer with no model
  behind it, which remembers what it was asked: a test that says an endpoint was asked
  something asserts what it received, the keys of the request among it, and one that says
  it was not asked counts none. Give Shelf a short deadline (`endpoint.Embedding.Timeout`,
  `QueryTimeout`) where a test has an endpoint miss it, so that no test waits out a
  default, and there alone: a Shelf that must get its answer keeps the defaults, since an
  answer that took its time on a busy computer is no failure. The built
  `study-shelf` is told of the endpoint by a settings file in the test computer's
  configuration folder, as a learner tells it. One test asks a real model,
  `TestTheRecipeAgainstARealEndpoint`, and only when `LAMPLIGHT_TEST_EMBEDDING_ENDPOINT`
  names its address: never set that in a run of your own to an address that is not yours.
- Tests must pass with no git identity configured (CI has none), and in any order.

## Important Paths

- `cmd/study`: the entry point.
- `internal/core`: everything the learner's data goes through, behind one `Core`: Topics
  and `status`, the History engine (`history.go`, `replay.go`, `write.go`, `items.go`),
  the Syllabus and Revisions, Sessions and the Resume point, Checks and Attempts, Cards and
  FSRS scheduling (`schedule.go`), Sources and Evidence, Goal and Pace, Assessments and
  signals, the read views, and `study import`. It reads the Knowledge base plugin registry
  (`plugin_registry.go`) and changes nothing in it, starts or reaches a plugin
  (`plugin_client.go`), asks a Topic's plugin what it has indexed, or to index
  (`source_index.go`), searches it and reads its Passages (`evidence_search.go`), and
  checks a quote against them (`quote.go`).
- `internal/plugincontract`: the contract a Knowledge base plugin speaks, version 1: the
  six tools' inputs and results as Go types, the limits, the error codes, and what each
  answer must hold (`Validate`). It holds no client and no server.
- `internal/pluginclient`: `study` as the MCP client of a plugin: starting a registered
  command or reaching a registered URL, the deadlines, and a `Connection` with the contract's
  tools as methods.
- `internal/plugintest`: for tests only: the test plugin, a learner's computer made of
  temporary folders (`Computer`), and the conformance suite.
- `cmd/study-shelf`: Shelf's entry point: its few flags, and the MCP server over the
  standard streams.
- `internal/shelf`: Shelf. The package itself holds the six tools (`shelf.go`), the
  indexing of one Source, which goes on after `index` has answered (`work.go`), and one
  search, by keyword, by vectors and with a reranker (`search.go`). Below it:
  `index`, one SQLite file for each collection, with the format, the full-text search, the
  Passages' vectors and what keeps SQLite inside Shelf's folder; `pdf`, a PDF's pages
  through PDFium; `web`, a web page fetched within limits and its readable text; `cut`,
  text tidied and cut into Passages; `endpoint`, the embedding and the reranking endpoint
  as Shelf asks them; `settings`, Shelf's settings file and the Recipes; and `shelftest`,
  for tests only: PDFs written for a test, the test endpoint, and the built `study-shelf`.
- `internal/pluginregistry`: the Knowledge base plugin registry, which is the computer's
  and not the Study home's. The package itself only reads (`Read`, and `OpenCommand`,
  `OpenFolder` and `Guard` for starting a plugin). `registrar` below it registers and removes
  plugins, for `internal/cli` alone. `internal/regfile` below it is what the two share and
  nothing else can import: the walk that keeps the Study home out, the configuration
  folder as it was opened, and the file's shape. `internal/regtest` is their tests'
  fixture.
- `internal/checkpoint`: hardened git: Checkpoints, work snapshots for Attempts, changes
  since a Checkpoint, and reading history.
- `internal/library`: building and searching the Library index.
- `internal/xdg`: the one place the home folder and the `XDG_*` folders are read from the
  environment. A relative one is ignored, as the XDG specification says.
- `internal/cli`: the `study` command line (cobra and fang), including `study setup`.
- `internal/mcpserver`: the MCP server, its tools and its Instructions for agents.
- `internal/claudeplugin` and `.claude-plugin/marketplace.json`: the Claude Code plugin,
  generated by `study claude-plugin-path`.
- `internal/e2e`: end-to-end tests: the learner loop driven by a scripted agent, the
  v2.0 acceptance walkthrough from `study setup` to an imported v1 Topic, `install.sh`
  run against a local folder of release files, which holds both programs, and a built
  `study` that starts the test plugin and stops it when it exits or is signalled.
- `skills/lamplight`: the `lamplight` skill, embedded in the binary.
- `install.sh`: the install script the README gives (ADR-0010), in POSIX `sh`. It installs
  `study` and `study-shelf`, and relies on the names of the release files and of the files
  in an archive, set in `.goreleaser.yaml` and described in `docs/release.md`: change them
  together. CI runs `shellcheck` on it and installs every release snapshot with it, on
  Linux and on macOS.
- `docs/release-checklist.md`: the maintainer's steps to ship v2.0.

## Patterns

- New operation: add it to the core with tests, then expose it in the CLI (with `--json`,
  and `--dry-run` for writes) and the MCP server, document it in `docs/cli.md`, and mention
  it in the skill if agents should use it. Some operations are CLI-only on purpose and get
  no MCP tool: `study check`, which runs code (ADR-0009), `study source index`, which has a
  plugin read files and fetch addresses (ADR-0012), and what is for the learner or the
  installation alone, such as `study topic remove`, `study topic restore`, `study import`,
  `study knowledge-base add`, `study knowledge-base list`, `study knowledge-base remove`,
  `study knowledge-base check`, `study setup`, `study doctor`, `study completion`, `study library build` and terminal
  `study review`.
- New file or Event format: bump nothing silently; add a `format` field and a test that
  newer formats are refused.
- Design changes: update `GLOSSARY.md`, the design and a new ADR together.

## Agent skills

- **Issue tracker:** GitHub `mordor-forge/study-skill`. See `docs/agents/issue-tracker.md`.
- **Triage labels:** the five canonical roles. See `docs/agents/triage-labels.md`.
- **Domain docs:** single root glossary and ADRs. See `docs/agents/domain.md`.
- **Specs, reviews and PRs:** see `docs/agents/workflow.md` when using Matt's engineering workflow.
