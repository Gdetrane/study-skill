# 0012. Knowledge Base Plugins Speak One Contract Over MCP

## Status

Accepted. Decides the "Later" part of ADR-0007, which ADR-0011 made part of v2.0.

## Context

ADR-0007 planned Knowledge base plugins: MCP servers that implement a contract of
Lamplight's own. ADR-0011 removed NotebookLM, so v2.0 waits for a Knowledge base that needs
no account. What forces the shape of it:

- Indexing a book takes minutes. MCP has progress notifications and cancellation; tasks are
  an extension that the Go SDK Lamplight uses (v1.8.0) does not implement.
- A Topic syncs between machines and can be cloned from anywhere, so nothing in it may
  decide which program runs (ADR-0009).
- The MCP server runs outside the agent's sandbox (ADR-0009). Anything it reads because the
  agent named it, and then hands back, is a way around the sandbox.
- "An exact quote, never a paraphrase" was only an instruction to the agent. Nothing
  checked it.
- A survey of six local servers found none that indexes a file or URL on request, reports
  indexing, and returns the Source's own text with a page. The closest needs Node, embeds
  only in-process, fetches no URLs and has an AGPL PDF library; an adapter for it would be
  about as much code as the search itself.
- The learner's Sources are mostly PDFs with a text layer. Their text and page boundaries
  can be read without OCR; formulas come out garbled.
- Lessons may one day come from recordings and video, where a location is a time and not a
  page.

## Decision

**The agent talks to `study`, and `study` talks to the plugin.** `study` is the MCP client
of the Topic's Knowledge base plugin. The agent installs no second server and calls none.
`study` itself ships no retrieval: it never parses a document, as ADR-0007 said.

### The contract, version 1

A Knowledge base plugin is an MCP server with these five tools. Their names and shapes are
Lamplight's; what is behind them is the plugin's.

| Tool | Takes | Gives back |
| --- | --- | --- |
| `contract` | nothing | the contract version, and what the plugin can do: file Sources, URL Sources, keyword search, embedding search, which kinds of location |
| `index` | a collection, and one Source: id, title, a path with its content hash, or a URL | an answer at once; the work goes on afterwards |
| `status` | a collection | for each Source: indexed, indexing with its progress, or failed with the reason, and the hash of the content that was indexed |
| `search` | a collection, a query, a limit, optionally some Sources | Passages, best first |
| `passage` | a collection and a Passage's id | that Passage |

- A **collection** is a name `study` chooses for a Topic. A plugin keeps collections apart
  and knows nothing else about Topics.
- A **Passage** has an id, the Source's id, its text, a score and, when the plugin knows
  one, a location. The text is the Source's own, as the plugin extracted it: never a summary
  and never a generated answer.
- A **location** says what kind it is. Version 1 has `pages`, with a first and last page.
  A Passage from a web page has none. Other kinds, such as a time range in a recording, are
  added to the same field without a new contract version; `contract` says which kinds a
  plugin gives.
- `index` never blocks. It returns at once and `status` is asked again, because tasks are
  not available to us. A plugin that was stopped halfway carries on or starts over when
  asked again.
- Indexing a file whose content hash is already indexed does nothing. A URL has no hash
  until it is fetched: the plugin fetches it, `status` gives the hash of what it indexed,
  and it is fetched again only when the learner asks for that.
- A plugin that reports a contract version newer than `study` knows is refused, like every
  newer format.

### Which program a Topic can start

- A plugin is registered on each machine, by name, with `study knowledge-base add`: a
  command with its arguments, or a URL. The registry is a file with a `format` number in
  the learner's configuration folder, outside the Study home.
- A Topic records only the plugin's name: `kind = "plugin"` and `plugin = "<name>"` in
  `[knowledge_base]`.
- Registering, changing and removing a plugin are for the learner and are CLI-only. No MCP
  tool takes a command line or a URL for a plugin. A test enforces it.
- `study` starts a registered command directly, without a shell, with pipes of its own,
  when a Topic first needs it, and stops it when `study` exits. It runs as the learner.
- `study` gives a command plugin a folder of its own under the Study home's `.lamplight`
  to keep its index in. That is this machine's state, never synced, and rebuilt by indexing
  again.
- A plugin reached by URL reads files on its own machine, whatever sandbox the agent is in.
  It must be told which folders it may read; Shelf refuses a path outside them.
- A Topic that names a plugin this machine does not have is treated as `none`, and
  `status` gives the one action that fixes it. So is one whose plugin does not start or
  does not speak the contract.

ADR-0009 stands: the core runs nothing that a Topic's files name.

### Indexing runs only through the CLI

- `study source index` is the one thing that asks a plugin to index. It indexes a Topic's
  Sources and shows progress until it is done; stopping it leaves what was indexed.
- It is CLI-only, like `study check` (ADR-0009): the agent runs it in its own shell, or the
  learner does, so the harness's sandbox and approval prompts apply to every file the
  plugin reads and every address it fetches. No MCP tool starts indexing. If one did, an
  agent could add any file on the computer as a Source over MCP and read its text back
  through a search.
- Adding a Source, over MCP or the CLI, records it and indexes nothing. `sources` and
  `status` say which Sources are not indexed and give the command.
- Whether a Source is indexed is the plugin's to say, on this machine. It is not in the
  History and records no Event.

### Searching

- `evidence_search`, a command and an MCP tool, returns Passages for a query. A search
  reads the plugin's index and never a Source.
- Nothing waits for a plugin: a Session opens and teaching goes on while Sources are being
  indexed or the plugin is missing, and Lessons without Evidence are marked, never blocked.

### Evidence checked against its Passage

Evidence can be recorded with the id of the Passage it quotes. `study` then asks the plugin
for that Passage again, checks that the quote is in its text, and takes the location from
it, with `knowledge_base` as where the location came from. A quote that is not in its
Passage is refused. Evidence recorded without a Passage stays possible and is not checked.
The Passage makes a new payload, so it is recorded by a new Event type.

### Shelf, the plugin that ships with Lamplight

`study-shelf` is a second program in this repository, in the same release as `study`.
`study` links none of its code, and a test keeps it so.

- Go, without cgo. One SQLite file per collection holds keyword search (FTS5) and vectors.
- It reads PDFs page by page, and web pages. Which PDF reader it uses, one compiled into
  the binary or `pdftotext` from the `PATH`, is settled by a prototype on real books.
- Embeddings come from any endpoint that speaks the OpenAI embeddings API, such as
  llama.cpp, Ollama or vLLM on the learner's own machine. Keyword and embedding results are
  merged by rank. With no endpoint it searches by keyword alone.
- A **recipe** is a named set of Shelf's settings for one embedding model: the model, the
  text it wants before a query and before a document, the size of its vectors, and how
  long a Passage may be. Shelf works with any model; the first recipe is for
  EmbeddingGemma 2, which has open weights under Apache 2.0.
- A reranking endpoint is an optional setting. When one is given, Shelf has it reorder the
  best Passages before it returns them. In a test on four of the maintainer's books, with
  300 questions, it was the largest gain after embeddings themselves: the right Passage
  came first for 64% of the questions, against 51% with embeddings alone and 32% with
  keyword search.
- One further setting can follow without changing the contract: a short context written by
  a language model for each Passage, used only for indexing and never returned as the
  Passage's text. In the same test it gained a point or two, for about a hundred times the
  indexing time.

Considered and rejected:

- **Retrieval inside `study`.** Smaller to ship, but it ties the core to one way of
  searching and to parsing documents.
- **An adapter around an existing server.** See the survey above.
- **Shelf in a repository of its own.** A second CI, release and install path for one
  maintainer, and a contract that could drift from its first plugin.
- **The agent calling the plugin itself**, as it did NotebookLM. Every agent would need the
  second server installed, and nothing could check a quote.
- **Sending a file's bytes over MCP.** Books are tens of megabytes. The contract passes a
  path.
- **Indexing when a Source is added, or from an MCP tool.** Convenient, but it makes the MCP
  server read files for the agent outside its sandbox, and a command that exits cannot
  leave a plugin indexing behind it.

## Consequences

- v2.0 ships the contract, `study` as its client, the registry, Topics naming a plugin,
  `evidence_search` with the quote check, and Shelf with keyword and embedding search and
  optional reranking. Layout-aware conversion and a context for each Passage come later, as
  settings of Shelf or as other plugins.
- The release holds two programs. `.goreleaser.yaml`, `install.sh` and `docs/release.md`
  change together.
- The module gains SQLite as a dependency of Shelf. `study` stays free of it.
- A plugin on another machine must see the same files, since it is handed a path.
- An agent without a shell cannot index, as it cannot run a Check. The learner can always
  run `study source index` themselves.
- Indexing inside a sandbox needs the sandbox to allow the Study home, and the embedding
  endpoint if there is one.
- A formula is quoted as it was extracted, or recorded without a Passage, unchecked.
- Changing the embedding model means embedding a collection again, which `status` reports.
- Recordings, video and images as Sources need no new contract version. What an exact
  quote means for them is not decided.
