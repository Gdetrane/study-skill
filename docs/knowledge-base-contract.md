# The Knowledge base plugin contract, version 1

A Knowledge base plugin indexes a Topic's Sources and searches them. `study` ships no
retrieval of its own and never parses a document: it is the MCP client of the plugin a Topic
names, and the plugin does the reading
([ADR-0012](adr/0012-knowledge-base-plugins-speak-one-contract.md)). This page is the
contract between the two. It is enough to write a plugin from, in any language, without
reading Lamplight's source. Terms follow [GLOSSARY.md](../GLOSSARY.md).

A plugin is **an MCP server with six tools**: `contract`, `index`, `status`, `cancel`,
`search` and `passage`. Their names and what they take and give back are Lamplight's. What
is behind them, how text is extracted, cut and found, is the plugin's.

Three words are used throughout:

- A **Source** is a file or a web page a Topic learns from. `study` gives it an id.
- A **collection** is a name `study` chooses for a Topic. A plugin keeps collections apart
  and knows nothing else about Topics.
- A **Passage** is a stretch of a Source's own text, as the plugin extracted it, with where
  it is in the Source when the plugin knows. It is never a summary and never a generated
  answer: the learner's Evidence quotes from it, and `study` checks the quote against it.

The Go types in `internal/plugincontract` say the same as this page, and a test keeps the
two in step. `study knowledge-base check <name>` starts or reaches a registered plugin and
reports which contract it speaks ([docs/cli.md](cli.md#knowledge-base-plugins)).

## How study reaches a plugin

The learner registers a plugin on each computer with `study knowledge-base add`, by the
command that starts it or by the URL it answers at.

### MCP

`study` opens an MCP session with `initialize`, asking for protocol version `2025-11-25`.
A plugin may answer with an earlier version it speaks. `study` then lists the plugin's tools
(`tools/list`, following pages up to 1,000 tools), checks that the six are among them, and
calls `contract`. All of that must be done within 30 seconds of starting the plugin.

`study` offers a plugin nothing to call back: no roots, no sampling and no questions to the
learner. A tool that asks for more input is not speaking the contract. `study` does not read
the tools' descriptions or their schemas, uses no resources and no prompts, and passes over
notifications, log messages included. When the learner stops a command, `study` sends
`notifications/cancelled` for the call under way.

### A plugin started by a command

`study` starts the program when it first needs the plugin, and speaks MCP over its standard
input and output: one JSON-RPC message on each line, in UTF-8.

- **Standard output carries MCP messages and nothing else.** Anything else there, a banner
  or a log line, ends the session. Write everything else on standard error. `study` reads
  it, keeps the last 8 KiB, and repeats the end of it, cleaned, when the plugin fails. It
  never reaches the learner's terminal as it is.
- **No shell.** The program is started with the arguments it was registered with, word
  for word.
- **The working folder** is the folder of the registry, Lamplight's configuration folder.
  It is never the Topic or the Study home `study` was started in. Rely on nothing in it,
  and write nothing there.
- **The environment** is the learner's, with these changes:
  - every `GIT_*` variable is taken out;
  - `PWD` names the working folder, and `OLDPWD` is taken out;
  - **whatever names something inside the Study home is taken out.** The learner's agent
    writes in the Study home, and the learner often starts it from a Topic with that
    Topic's virtual environment switched on, so the environment can point there: a `PATH`
    that begins with the Topic's `.venv/bin`, a `PYTHONPATH` into the Topic. Each value is
    read as a list of paths divided as `PATH` is. A path that is the Study home, is inside
    it, or is reached through it by a link, is dropped from its list, and a variable left
    with nothing is dropped whole. A variable that names such a path among other words,
    as `NODE_OPTIONS` does after `--require`, is dropped whole too. So a plugin does not
    find programs or modules where the agent writes, and is not told where the Study home
    is;
  - `LAMPLIGHT_PLUGIN_DATA` names the plugin's folder (below), the one thing in the Study
    home a plugin is told of.
- **Standard input is a pipe of the plugin's own**, never the terminal and never what
  `study` itself reads. The plugin runs in a session of its own, with no terminal: it cannot
  ask the learner anything, and a Ctrl-C reaches `study`, not the plugin.
- **On Linux the program is run from the file `study` opened and checked**, as
  `/proc/self/fd/3`, and not from its path again. A compiled program notices nothing: its
  first argument is still the path it is registered by. A script is handed
  `/proc/self/fd/3` as its own name, so it must not look for its other files beside that
  name, and the interpreter its `#!` line names must read the script itself. For a script
  that needs either, register the interpreter with the script as an argument, such as
  `study knowledge-base add notes -- /usr/bin/python3 /opt/notes-kb/main.py`.
- **A script's interpreter is held to what the program is held to.** The system runs the
  program a script's first line names, so `study` checks that program too, and the one
  its first line names if it is a script itself: an interpreter that is inside the Study
  home, or reached through it, is not started.
- **Stopping.** `study` stops the plugin when it is done with it, and when the learner
  stops the command: it closes the plugin's standard input and output. **Exit when your
  standard input closes.** A plugin still running 2 seconds later gets `SIGTERM`, sent to
  its process group, and 2 seconds after that the group is killed. When the program exits,
  whatever it started and left in its process group is killed with it.
- **More than one copy can run at once**, on the same folder: the MCP server the agent
  uses and a `study source index` in a shell each start their own. A copy must find what
  another indexed, and searching while another copy indexes must work.
- **Nothing is kept between runs but what is in the folder.** Indexing goes on only while
  the copy that was asked runs.

### The plugin's folder

A plugin started by a command keeps its index in the folder `LAMPLIGHT_PLUGIN_DATA` names:
an absolute path, of a folder that exists when the plugin starts and that only the learner
can read. It is `.lamplight/knowledge-base-plugins/<name>` in the Study home, `<name>` being
the name the plugin is registered under. It is that computer's state, never synced, and
rebuilt by indexing again.

**The folder is inside the Study home, where the learner's agent can write, and the plugin
runs outside the agent's sandbox.** So treat everything in it as data. Load no code, no
extension and no settings from it, and follow no symbolic link out of it. `study` refuses to
start a plugin whose folder, or a folder on the way to it, is a symbolic link; that is a
look at one moment, and the plugin opens the name for as long as it runs.

### A plugin reached by URL

A plugin registered by URL is a service the learner runs. `study` reaches it over MCP's
streamable HTTP transport: each message is a `POST` to the registered address, answered
with `application/json` or with a stream of server-sent events.

- A redirect is not followed: register the address that answers.
- `study` opens no stream for messages the service starts, and tries no request twice.
- A session id (`Mcp-Session-Id`) is kept and sent back, and the session is ended with
  `DELETE` when `study` is done.
- A service gets no folder from `study`, and keeps its index where it was told to.
- **A service reads files and fetches addresses from where it runs, outside any sandbox
  the agent is in.** It is handed a file's path and must see the same file there. It must
  itself be told which folders it may read and whether it may fetch, and refuse the rest.
- Indexing goes on in a service after `study` has gone. `study` calls `cancel` when the
  learner stops it.

### Deadlines and limits

| What | Limit |
|---|---|
| Starting, or being reached, up to the answer of `contract` | 30 seconds |
| `index`, `status`, `cancel`, `passage`: each call | 30 seconds |
| `search` | 60 seconds |
| Exiting once standard input is closed, and again after `SIGTERM` | 2 seconds |
| One MCP message from the plugin | 16,777,216 bytes |
| A Passage's `text` | 32,768 bytes of UTF-8 |
| A `query` | 4,096 bytes of UTF-8 |
| `limit` of a search | 50 |
| Sources in a `status` answer, and in the `sources` of a search | 10,000 |
| A collection's name | 64 characters |
| The id of a Source or of a Passage | 128 characters |
| A Source's `title` | 200 characters |
| A `path` or a `url` | 4,096 bytes |
| Kinds in each list of a `contract` answer, and a kind's name | 16, and 32 characters |
| A `reason` or an error's `message`, as `study` shows it | 2,000 bytes |

A plugin that misses a deadline is stopped, when it is a command, and not asked again in
that session. `study` never sends more than these limits allow, and refuses an answer that
goes over them, except that a `reason` or a `message` is cut short instead.

## Results and errors

Every tool takes one JSON object as its arguments and gives back one JSON object.

- **The result** is the tool result's `structuredContent`. A plugin whose MCP library
  gives only text may put the same JSON object in the text of the first block of `content`
  instead: `study` reads `structuredContent` when there is one, and that text otherwise.
- **Names are spelled as on this page**, in lower case. A list is a JSON list even when it
  is empty.
- **Fields this page does not name are passed over**, so a plugin may say more. `study`
  sends none that this version does not define.
- **A refusal** is a tool result with `isError` set to `true` whose JSON object is an
  `error` with a `code` and a `message`:

<!-- example: error -->
```json
{"error": {"code": "not_found", "message": "collection linear-algebra has no Passage 7f3a9c12e5b04d68"}}
```

| Field | Is |
|---|---|
| `error` | An object with `code` and `message`. |
| `code` | One of the codes below. Any other, and none, is read as `internal`. |
| `message` | What went wrong, in words for the learner. `study` puts it on one line, escapes what a terminal would interpret, and shows the first 2,000 bytes. |

| `code` | When | `study` reports it as |
|---|---|---|
| `invalid_argument` | The arguments are not what this page describes, or hold a value this plugin does not take. | `invalid_argument` |
| `not_found` | `passage`: the collection has no Passage with that id. | `not_found` |
| `unsupported` | The plugin cannot do this at all: `index` of a kind of Source its `contract` does not list. | `failed_precondition` |
| `internal` | Anything else that went wrong inside the plugin. | `failed_precondition` |

An error result that is plain text, as an MCP library makes of an exception, is read as
`internal` with that text as its message. An error of the protocol itself, a JSON-RPC
error response, is read as `invalid_argument` when its code is `-32602` (invalid params)
and as `internal` otherwise. So a plugin that lets its library refuse malformed arguments
needs no code of its own for that; where this page names a code, `not_found` and
`unsupported`, the plugin must give it.

**What goes wrong with a Source is not an error of `index`.** A file that is not there, a
format the plugin cannot read, a page that cannot be fetched: `index` answers, and
`status` says `failed` with the reason.

## Names, ids and hashes

| What | Who chooses it | It is |
|---|---|---|
| A collection's name | `study` | Lowercase letters, digits and single hyphens, up to 64 characters: `linear-algebra`. It is safe as a file's name. |
| A Source's id | `study` | Lowercase letters, digits, hyphens and dots, starting with a letter or a digit, up to 128 characters: `strang-linear-algebra.k3f9a2`. |
| A Passage's id | the plugin | Letters, digits, dots, underscores, colons and hyphens, not starting with a hyphen, up to 128 characters. It names one Passage within its collection. |
| A content hash | whoever read the bytes | `sha256:` and the 64 lowercase hexadecimal digits of the SHA-256 of the bytes: of the file, or of a web page's body as it was fetched. |

Refuse a name or an id that is not shaped like this with `invalid_argument`: a collection
called `../other` must not become a path.

A Passage's id keeps naming the same text for as long as its Source's indexed content does
not change, whether or not the plugin was stopped and started in between. Once the content
changed, the id names nothing (`not_found`), or still the old text: never another text.

## The six tools

### `contract`

Says which version of this contract the plugin speaks, and what it can do. It takes no
arguments, and gives the same answer every time in a session.

<!-- example: contract result -->
```json
{"version": 1, "sources": ["file", "url"], "search": ["keyword", "embedding"], "locations": ["pages"]}
```

| Field | Is |
|---|---|
| `version` | The contract version: `1`. **A plugin that reports a version newer than `study` knows is refused**, like every newer format, so report the version you speak, as a whole number. |
| `sources` | The kinds of Source the plugin indexes: `file`, `url`, or both. At least one. |
| `search` | How it searches now: `keyword`, `embedding`, or both. At least one. A plugin that embeds only when an endpoint is configured lists `embedding` only then. |
| `locations` | The kinds of location its Passages can carry: `pages`. An empty list for a plugin that never knows where a Passage is. |

A kind is a lowercase word of letters, digits and underscores. Kinds are added to these
lists without a new contract version, a time range in a recording for instance, so `study`
keeps a kind it does not know and a plugin may list one.

### `index`

Asks the plugin to index one Source of a collection. **It answers at once, and the work
goes on afterwards**: `status` is asked again until the Source is no longer `indexing`.

<!-- example: index input -->
```json
{"collection": "linear-algebra",
 "source": {"id": "strang-linear-algebra.k3f9a2", "title": "Linear Algebra", "kind": "file",
            "path": "/home/ada/Books/strang.pdf",
            "hash": "sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"}}
```

<!-- example: index input -->
```json
{"collection": "go-concurrency",
 "source": {"id": "go-dev-blog-context.m4k8s3", "title": "Go Concurrency Patterns: Context", "kind": "url",
            "url": "https://go.dev/blog/context", "refetch": true}}
```

| Field | Is |
|---|---|
| `collection` | The collection the Source belongs to. |
| `source` | The Source to index. |
| `id` | The Source's id. |
| `title` | The Source's title, up to 200 characters, for the plugin's own messages. It is no content: a new title alone is nothing to index. |
| `kind` | `file` or `url`. |
| `path` | A file's absolute path, on the computer `study` runs on. Only with `file`. |
| `hash` | The content hash of the file as `study` read it. Only with `file`. |
| `url` | A web page's `http` or `https` address, without a user name or password. Only with `url`. |
| `refetch` | `true` asks for a web page that is indexed already to be fetched again. Left out otherwise. Only with `url`. |

<!-- example: index result -->
```json
{"source": {"id": "strang-linear-algebra.k3f9a2", "state": "indexing", "progress": 0}}
```

The result is `source`: what the plugin says of the Source now, shaped as in `status`.

- **A file whose `hash` is the hash of what is indexed for that Source does nothing.** The
  answer is `indexed`, at once, and the Passages and their ids stay as they are.
- Otherwise the plugin reads the file at `path`. Its content replaces what the index held
  for the Source: the old Passages are no longer found.
- **`status` gives the hash of the bytes the plugin read**, which it computes itself. The
  file may have changed since `study` hashed it. Then the plugin indexes what it read and
  says so through that hash, or fails the Source: it never gives the hash it was handed for
  content it did not hash.
- **A web page has no hash until it is fetched.** The plugin fetches it, and `status`
  gives the hash of what it fetched. A page that is indexed is fetched again only when
  `refetch` is `true`: otherwise the answer is `indexed`, at once.
- A Source that is being indexed already goes on being indexed when it is asked for with
  the same content, and starts over when it is asked for with other content.
- A kind the plugin's `contract` does not list is refused with `unsupported`.
- Indexing reads the Source. Nothing else does: `search` and `passage` read the index.

### `status`

Says what is indexed in a collection.

<!-- example: status input -->
```json
{"collection": "linear-algebra"}
```

<!-- example: status result -->
```json
{"sources": [
  {"id": "strang-linear-algebra.k3f9a2", "state": "indexed",
   "hash": "sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"},
  {"id": "axler-linear-algebra-done-right.p2x7q1", "state": "indexing", "progress": 0.4},
  {"id": "scanned-notes.t8w3n5", "state": "failed", "reason": "the file has no text layer"}
]}
```

| Field | Is |
|---|---|
| `collection` | The collection to report on. |
| `sources` | One entry for each Source the plugin has something to say about. **A Source it does not list is not indexed.** A collection nobody indexed has an empty list. |
| `id` | The Source's id. |
| `state` | `indexed`, `indexing` or `failed`. |
| `progress` | With `indexing`: how much of the work is done, from `0` to `1`. Left out when the plugin cannot tell. |
| `reason` | With `failed`: why, in words for the learner. Required then. |
| `hash` | The content hash of what `search` finds for this Source now. Required with `indexed`. With `indexing` or `failed` it is there when earlier content is still indexed, and left out otherwise. |

- `indexed`: the Source's Passages can be found, and `hash` says of which content.
- `indexing`: the plugin is working on the Source **now**. A plugin that was stopped
  halfway and is started again says nothing is `indexing` until `index` is called: then it
  carries on, or starts over.
- `failed`: the last indexing failed. The Source stays `failed` until it is indexed again,
  or at least until the plugin stops.
- `status` answers from what the plugin knows, and never waits for indexing.

### `cancel`

Stops the indexing of one Source. It answers once the work has stopped, so that nothing
goes on behind a command that has exited.

<!-- example: cancel input -->
```json
{"collection": "linear-algebra", "source": "axler-linear-algebra-done-right.p2x7q1"}
```

<!-- example: cancel result -->
```json
{"stopped": true}
```

| Field | Is |
|---|---|
| `collection` | The collection the Source belongs to. |
| `source` | The id of the Source whose indexing is to stop. |
| `stopped` | `true` when the Source was being indexed and no longer is; `false` when there was nothing to stop. |

- **What was indexed stays.** After a `cancel` that stopped something, the Source is as it
  was before `index` was called: `indexed` with its earlier content when it had any, and
  not listed by `status` otherwise. Being stopped is no failure.
- A Source that is not being indexed, or that the collection does not have, is no error:
  the answer is `stopped: false`.
- A plugin may keep what it had done, to carry on when `index` is called again.

### `search`

Finds the Passages of a collection that answer a query, **best first**.

<!-- example: search input -->
```json
{"collection": "linear-algebra", "query": "why does elimination produce a triangular system?", "limit": 5,
 "sources": ["strang-linear-algebra.k3f9a2"]}
```

<!-- example: search result -->
```json
{"passages": [
  {"id": "7f3a9c12e5b04d68", "source": "strang-linear-algebra.k3f9a2",
   "text": "Elimination produces an upper triangular system.",
   "location": {"kind": "pages", "first": 46, "last": 46}, "score": 0.83},
  {"id": "2b9d4e71a0c3f586", "source": "strang-linear-algebra.k3f9a2",
   "text": "The pivots are on the diagonal of the triangle after elimination.",
   "location": {"kind": "pages", "first": 46, "last": 47}, "score": 0.41}
]}
```

| Field | Is |
|---|---|
| `collection` | The collection to search. One nobody indexed gives an empty list, not an error. |
| `query` | What to look for: text of up to 4,096 bytes, not empty. |
| `limit` | The most Passages to give back, from 1 to 50. |
| `sources` | The ids of the Sources to search. Left out, the whole collection is searched. An id the collection does not have finds nothing. `study` never sends an empty list. |
| `passages` | The Passages found, the best first, `limit` at most, none twice. |
| `id` | The Passage's id. |
| `source` | The id of the Source the text is from. |
| `text` | The Source's own text, as extracted: up to 32,768 bytes, not empty. |
| `location` | Where the text is in the Source, when the plugin knows. Left out otherwise. A web page has no pages. |
| `kind` | The kind of location, one the plugin's `contract` lists: `pages`. |
| `first`, `last` | With `pages`: the first and the last page the text is on, counted from 1 from the start of the file, `last` not before `first`. |
| `score` | How well the Passage answers this query: a number, higher is better, and no Passage has a higher one than the Passage before it. It means nothing outside this search. |

- A search reads the index and never a Source, and never waits for indexing: it finds what
  is indexed now.
- **A score belongs to one search**, so `search` gives it and `passage` does not.
- A location's kind decides its other fields. Version 1 has `pages`. A plugin that lists
  another kind in its `contract` gives that kind its own fields, which this version of
  `study` passes over.

### `passage`

Gives one Passage again, by the id a search gave. `study` asks for it when Evidence quotes
from the Passage, to check the quote against its text.

<!-- example: passage input -->
```json
{"collection": "linear-algebra", "id": "7f3a9c12e5b04d68"}
```

<!-- example: passage result -->
```json
{"passage": {"id": "7f3a9c12e5b04d68", "source": "strang-linear-algebra.k3f9a2",
             "text": "Elimination produces an upper triangular system.",
             "location": {"kind": "pages", "first": 46, "last": 46}}}
```

| Field | Is |
|---|---|
| `collection` | The collection the Passage is in. |
| `id` | The Passage's id. |
| `passage` | The Passage, with the `id`, `source`, `text` and `location` a search gives, and no `score`. |

- The Passage is the one the search gave: the same text, Source and location.
- An id the collection does not have is refused with `not_found`.

## Checking a plugin

`study knowledge-base check <name>` starts or reaches a registered plugin as `study` does
for a Topic, and reports the contract version it speaks and what it can do, or what is
wrong and what to do about it.

The conformance suite holds a plugin to everything on this page that can be seen from
outside it. It is a Go test helper, `plugintest.Conformance` in `internal/plugintest`,
which takes a way to connect to the plugin, a command or a URL, and a way to write a file
the plugin can index. It writes its own Sources, indexes them in collections of its own
that no other run shares, and fails for whatever the plugin does that this page does not
allow. The package is inside Lamplight's module, so a plugin written elsewhere is checked
from a test in a checkout of Lamplight:

```go
func TestMyPluginConforms(t *testing.T) {
	plugintest.Conformance(t, plugintest.Target{
		// Each connection starts the command again, with the same folder in
		// LAMPLIGHT_PLUGIN_DATA. For a service: plugintest.URLTransport(address).
		Connect:  plugintest.CommandTransport(t.TempDir(), "/opt/notes-kb/bin/notes-kb", "--stdio"),
		Restarts: true,
		// A plugin that does not read plain text says how to write a file it reads.
		WriteFile: writePDF,
	})
}
```

The same package holds a small plugin that passes the suite, to read beside this page.

## What version 1 leaves out

- Nothing removes a Source, or a collection, from an index. A Source the Topic no longer
  has stays indexed; `study` limits its searches to the Topic's Sources with `sources`.
- `study` sends no file's bytes. A plugin is handed a path.
- A plugin is told nothing of Topics, Lessons or the learner.
