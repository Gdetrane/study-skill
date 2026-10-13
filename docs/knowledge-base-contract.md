# The Knowledge base plugin contract, version 1

A Knowledge base plugin indexes a Topic's Sources and searches them. `study` ships no
retrieval of its own and never parses a document: it is the MCP client of the plugin a Topic
names, and the plugin does the reading
([ADR-0012](adr/0012-knowledge-base-plugins-speak-one-contract.md)). This page is the
contract between the two. It is enough to write a plugin from, in any language, without
reading Lamplight's source. Terms follow [GLOSSARY.md](../GLOSSARY.md).

A plugin is **an MCP server with seven tools**: `contract`, `index`, `status`, `cancel`,
`search`, `passage` and `diagnose`. Their names and what they take and give back are
Lamplight's. What is behind them, how text is extracted, cut and found, is the plugin's.

Three words are used throughout:

- A **Source** is a file or a web page a Topic learns from. `study` gives it an id.
- A **collection** is a name `study` chooses for a Topic. A plugin keeps collections apart
  and knows nothing else about Topics.
- A **Passage** is a stretch of a Source's own text, as the plugin extracted it, with where
  it is in the Source when the plugin knows. It is never a summary and never a generated
  answer: the learner's Evidence quotes from it, and `study` checks the quote against it.

The Go types in `internal/plugincontract` say the same as this page, and a test keeps the
two in step. `study knowledge-base check <name>` starts or reaches a registered plugin and
reports which contract it speaks, and what the plugin found when it tested what it depends
on ([docs/cli.md](cli.md#knowledge-base-plugins)).

## How study reaches a plugin

The learner registers a plugin on each computer with `study knowledge-base add`, by the
command that starts it or by the URL it answers at.

### MCP

`study` opens an MCP session with `initialize`, asking for protocol revision `2025-11-25`.
A plugin may answer with an earlier revision it speaks. `study` then lists the plugin's tools
(`tools/list`, following pages up to 1,000 tools), checks that the seven are among them, and
calls `contract`. All of that must be done within 30 seconds of `study` setting out to start
the plugin.

**Be quick to start, and to say what is indexed.** `study status`, and whatever else shows
the learner where they are, asks a plugin which Sources it has indexed, and waits for no
plugin: there, starting the plugin, the answer of `contract` and every answer of `status`
have **2 seconds, all of them together**, and the plugin is started anew each time. One
that takes longer is stopped, and its Sources are shown as not known. So load what is slow
to load, a model say, when `index` or `search` first needs it, and not when you start.

**A search has 45 seconds, starting included.** When the learner's agent searches a
Topic's Sources, starting the plugin and the answers of `contract`, `status` and `search`
have 45 seconds, **all of them together**. So have starting, `contract`, `status` and every
`passage` when `study` checks a quote against the Passages it is from. The agent's tool
call waits for the answer, and is not kept waiting for a minute. A plugin that takes
longer is stopped, and the search fails. `study` has at most two copies of a plugin
searching at a time, so a search may also wait for another to end, inside the same 45
seconds. The plugin is started anew each time,
so what is slow to load is loaded again for every search: keep a model in a service that
stays up, as an embedding endpoint does, and not in the plugin's own program.

**A plugin answers what it is asked, and sends `study` no requests.** `study` offers a
plugin nothing to call back: no roots, no sampling and no questions to the learner. The first
request a plugin sends, a `ping` or a `roots/list` as much as any other, ends the session:
`study` stops a plugin that is a command, hangs up on one that is a service, and reports that
the plugin does not speak the contract. Turn off whatever in your MCP library sends requests
on its own, such as a keep-alive `ping`. A tool that asks for more input is not speaking the
contract either.

A plugin may send notifications, and `study` passes over them, log messages and progress
included. `study` does not read the tools' descriptions or their schemas, and uses no
resources and no prompts. When the learner stops a command, `study` sends
`notifications/cancelled` for the call under way.

### A plugin started by a command

`study` starts the program when it first needs the plugin, and speaks MCP over its standard
input and output: one JSON-RPC message on each line, in UTF-8. The MCP server the agent
uses starts a plugin **outside any sandbox the agent is in**, and the agent writes in the
Study home. So `study` starts a plugin with nothing the agent could have prepared:

- **Standard output carries MCP messages and nothing else.** Anything else there, a banner
  or a log line, ends the session. Write everything else on standard error. `study` reads
  it, keeps the last 8 KiB, and repeats the last 600 bytes of that, cleaned, when the plugin
  fails: put the reason last. It never reaches the learner's terminal as it is.
- **No shell.** The program is started with the arguments it was registered with, word
  for word.
- **The working folder** is the folder of the registry, Lamplight's configuration folder.
  It is never the Topic or the Study home `study` was started in. Rely on nothing in it,
  and **write nothing there**.
- **The environment is a small one `study` makes.** It is not the environment `study` was
  started with: that one is often an agent's, started from a Topic with the Topic's virtual
  environment switched on, and `PATH`, `PYTHONPATH`, `NODE_OPTIONS`, `PERL5OPT`, `BASH_ENV`,
  `LD_PRELOAD` and many more could each have a plugin load what the agent wrote. A plugin
  gets these variables, when `study` has them, and no others:

  | Variable | As |
  |---|---|
  | `PATH`, `XDG_DATA_DIRS`, `XDG_CONFIG_DIRS` | The folders of the list that are absolute paths outside the Study home. The others are dropped from the list. |
  | `HOME`, `TMPDIR`, `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_STATE_HOME`, `XDG_CACHE_HOME`, `XDG_RUNTIME_DIR` | As it is, when it is an absolute path outside the Study home. Left out otherwise. |
  | `USER`, `LOGNAME`, `LANG`, `LC_*` | As it is, when it is a name and no path. |
  | `TZ` | As it is, when it names a zone or a file outside the Study home. |
  | `LAMPLIGHT_PLUGIN_DATA` | The plugin's folder (below). |

  "Outside the Study home" is decided as the registry decides it for the program: by what
  each folder of the path is, links followed, and never by its name. A path `study` cannot
  follow counts as inside. So a plugin does not find programs, modules or settings where
  the agent writes, and is not told where the Study home is.

  **A plugin that needs anything else takes it in its arguments**, a key file's path or a
  proxy's address, or the learner registers a small script of their own, kept outside the
  Study home, that sets the variable and starts the plugin. Say which in your plugin's
  instructions. `study` warns nobody that a variable was left out.
- **Standard input is a pipe of the plugin's own**, never the terminal and never what
  `study` itself reads. The plugin runs in a session of its own, with no terminal: it cannot
  ask the learner anything, and a Ctrl-C reaches `study`, not the plugin.
- **No other file of `study`'s is open in the plugin.** Whatever `study` itself was handed
  by the program that started it, a descriptor for a file or a copy of its standard output,
  is closed in the plugin.
- **The program that runs is the one `study` checked.** `study` opens the folder that
  really holds the program when it checks it, and on Linux runs the program by its own name
  in that open folder, as `/proc/self/fd/3/<name>`, so that no folder on the way is looked up
  again. What a plugin sees of that:
  - Descriptor 3 is that folder, open for reading. It is the one thing a compiled program
    sees that it would not see when started from a terminal: its first argument is the path
    it is registered by, the system calls the process by the program's name, and
    `/proc/self/exe` leads to the program. When the folder is one the learner may pass
    through and not list (mode 711, say), descriptor 3 is open only to find names in it:
    it cannot be read to list the folder, which the learner could not list from a
    terminal either.
  - A script is handed `/proc/self/fd/3/<name>` as its own name (`$0`, `sys.argv[0]`,
    `__file__`). What is beside the script is found through that name, as from a terminal,
    for as long as descriptor 3 is open, in the script and in the programs it starts. A
    script that closes the descriptors it did not open, or starts programs that do (Python's
    `subprocess` closes them by default), asks once at the start where it really is:
    `readlink -f "$0"`, `os.path.realpath(__file__)`.
  - On macOS, which cannot run a program through an open folder, the program is run by the
    path it is registered by, after a last look that the path still leads to the file that
    was checked.
- **A script's interpreter is held to what the program is held to.** The system runs the
  program a script's first line names, so `study` checks that program too, and the one
  its first line names if it is a script itself: an interpreter that is inside the Study
  home, or reached through it, is not started. **An interpreter named by a relative path is
  refused**: the system would look for it from the working folder. Write
  `#!/usr/bin/env python3`, or the interpreter's full path.

  **`env` is followed to what it starts.** With `#!/usr/bin/env python3`, or
  `#!/usr/bin/env -S python3 -u` to give the interpreter arguments, `env` finds `python3`
  in the folders of the `PATH` above. `study` looks it up the same way and checks the
  first one found: when that one is inside the Study home or reached through it, as a
  link in `~/.local/bin` to a Topic's virtual environment is, the plugin is not started,
  whatever comes later in `PATH`. A plugin with no `PATH`, or with none that has the name,
  is not started either. Anything else given to `env` on that line, an option or a
  `NAME=value` setting, is refused: `study` cannot tell what it would start. Without `-S`
  the line gives `env` a name and nothing after it: Linux hands `env` the rest of the line
  as one word, so `#!/usr/bin/env python3 -u` looks for a program called `python3 -u`. A
  file called `env` that is itself a script is followed as any script is.

  **Keep the first line short and plain**: it ends within 255 bytes, with a plain line
  ending and no other control character. Systems differ in how much of a longer line they
  read and in what they make of a carriage return, and `study` starts nothing it cannot
  read as they all do.

  **Every interpreter must be readable, and four in a row is the most.** The system reads
  a program's first line whether or not you may read the file, so an interpreter `study`
  cannot read is refused: it could name another that nobody checked. A script whose
  interpreter is a script is followed four times; a fifth is refused.
- **Stopping.** `study` stops the plugin when it is done with it: it closes the plugin's
  standard input and output. **Exit when your standard input closes**, whatever you are
  doing. A plugin still running 2 seconds later gets `SIGTERM`, sent to its process group,
  and 2 seconds after that the group is killed. A plugin that was only asked what it has
  indexed has nothing to finish, and gets a quarter of a second each time. When the program
  exits, whatever it started and left in its process group is killed with it. **A program
  the plugin moved to a process group or a session of its own is not stopped by `study`**:
  a plugin that starts one stops it itself.
- **Signals to `study`.** On `SIGINT`, `SIGTERM` and `SIGHUP`, `study` stops the request,
  sends `notifications/cancelled` for the call under way, and stops the plugin as above
  before it exits. A `study` that is killed (`SIGKILL`) stops nothing: its end of the
  plugin's standard input closes with it, which is one more reason to exit when it does.
  None of those signals reaches the plugin itself, which has no terminal and is in a
  session of its own.
- **More than one copy can run at once**, on the same folder: the MCP server the agent
  uses and a `study source index` in a shell each start their own. A copy must find what
  another indexed, and searching while another copy indexes must work. A copy that can
  tell that another is indexing a Source **now** says `indexing` of it, and one that
  cannot says what the index holds: never `indexing` for work that a copy which was
  stopped left unfinished. `cancel` stops what the copy that is asked is indexing itself,
  and answers `false` for another copy's work.
- **Nothing is kept between runs but what is in the folder.** Indexing goes on only while
  the copy that was asked runs.

### The plugin's folder

A plugin started by a command keeps its index in the folder `LAMPLIGHT_PLUGIN_DATA` names:
an absolute path, of a folder that exists when the plugin starts and that only the learner
can read (mode 0700): `study` makes it so, and does not start a plugin whose folder others
can read or write. It is `$XDG_STATE_HOME/lamplight/knowledge-base-plugins/<name>`, or
`~/.local/state/lamplight/knowledge-base-plugins/<name>` when `XDG_STATE_HOME` is not set
or is not an absolute path, `<name>` being the name the plugin is registered under. It is
that computer's state, never synced, and rebuilt by indexing again.

**The folder is outside the Study home**, where the learner's agent writes. A state folder
that is the Study home, is inside it, or is reached through it by a link is refused, and the
plugin is not started: an index the agent could write is one whose Passages the agent could
write, and a quote is checked against a Passage.

Two things follow for whoever writes or runs a plugin:

- **Open the folder once, and stay inside it.** Follow no symbolic link in it that leads
  out of it, and load no code, no extension and no settings from it: it holds data. The
  conformance suite puts links in the folder and fails a plugin that writes where they lead.
- **A sandbox that `study` itself runs in must allow the folder.** `study source index`
  run inside an agent's sandbox starts the plugin inside that sandbox, and the plugin writes
  its index in the learner's state folder, which a sandbox that allows only the Study home
  does not let it do. Allow `$XDG_STATE_HOME/lamplight/knowledge-base-plugins` there, or
  index from a shell of the learner's own. `study` says so itself when the folder cannot be
  made, or cannot be written in.
- **Pass over a name in the folder that begins with `.lamplight-`.** Before it starts a
  plugin to index, `study` makes an empty file with such a name in the folder and removes
  it again, to learn whether the folder can be written in: a sandbox refuses what a
  folder's mode allows. Another copy of the plugin may see the file for a moment. It is
  no part of an index, and no collection. The conformance suite leaves such a file in the
  folder and fails a plugin that does not index beside it.

### A plugin reached by URL

A plugin registered by URL is a service the learner runs. `study` reaches it over MCP's
streamable HTTP transport: each message is a `POST` to the registered address, answered
with `application/json` or with a stream of server-sent events.

- A redirect is not followed: register the address that answers.
- `study` opens no stream for messages the service starts, and tries no request twice. A
  request the service sends in the stream that answers a call ends the session, as above.
- A session id (`Mcp-Session-Id`) is kept and sent back, every request after `initialize`
  names the protocol revision (`Mcp-Protocol-Version`), and the session is ended with
  `DELETE` when `study` is done.
- **A service that refuses a request says why in the body of its answer.** `study` shows
  the learner the start of it, about 300 bytes, cleaned, beside the status: a service that
  speaks only a later revision of MCP says which there.
- **Only the scheme and host of the address are shown** by `study`, outside
  `study knowledge-base list`: the rest of an address, a path or a query, may be a key.
  Whatever a service writes, in a refusal, a `reason`, a `notice`, a finding or its own
  name, is shown without the path and the query of the address it is registered at, as
  they read: in capitals or small letters, and with any of their bytes as percent-escapes
  or none. A piece of the path counts from eight bytes and a value of the query from six:
  a shorter one said on its own is shown. Give a service a key longer than that, of plain
  letters and digits, or better, keep the key out of what it writes.
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
| Starting, `contract` and every `status`, together, when `study` shows what is indexed | 2 seconds |
| Starting, `contract`, `status` and `search`, together, when `study` searches a Topic's Sources; and starting, `contract`, `status` and every `passage`, together, when it checks a quote | 45 seconds |
| Exiting then, once standard input is closed, and again after `SIGTERM` | 250 milliseconds |
| Answering `cancel`, when `study source index` was stopped | 10 seconds |
| `diagnose`: the whole answer | 10 seconds |
| One MCP message from the plugin | 16,777,216 bytes |
| Values in one MCP message from the plugin | 250,000 |
| A Passage's `text` | 32,768 bytes of UTF-8 |
| A `query` | 4,096 bytes of UTF-8 |
| `limit` of a search | 50 |
| A search's `notice`, as `study` shows it | 2,000 bytes at most, and `…` after them when it was cut |
| Sources in a `status` answer, and in the `sources` of a search | 10,000 |
| A collection's name | 64 characters |
| The id of a Source or of a Passage | 128 characters |
| A Source's `title` | 200 characters |
| A `path` or a `url` | 4,096 bytes |
| Kinds in each list of a `contract` answer, and a kind's name | 16, and 32 characters |
| Findings in a `diagnose` answer | 16 |
| A finding's `name` | 32 characters |
| A finding's `message`, as `study` shows it | 600 bytes |
| A `reason` or an error's `message`, as `study` shows it | 2,000 bytes |

The values of a message are counted generously: each list, each object, and each member
or element after the first. A `status` of 10,000 Sources holds about a quarter of the
limit. A message that goes over ends the session, as one that is too large does.

The 60 seconds of `search` are what one call has when nothing sets a shorter time. `study`
always sets one when it searches a Topic's Sources: the 45 seconds for all of it, so that
is the time to build for.

A plugin that misses a deadline is stopped, when it is a command, and not asked again in
that session. `study` never sends more than these limits allow, and refuses an answer that
goes over them, except that a `reason`, a `message` or a `notice` is cut short instead. The 30 seconds
of starting cover what `study` does first as well: finding the program and making the
folder.

The 10 seconds of `diagnose` are longer than the 2 seconds a plugin has when `study` shows
what is indexed, because what a plugin tests there is asked over a network, and may have a
model to wake. They are no longer, because `study doctor` asks every registered plugin,
and a learner waits for it.

## Results and errors

Every tool takes one JSON object as its arguments and gives back one JSON object.

- **The result** is the tool result's `structuredContent`. A plugin whose MCP library
  gives only text may put the same JSON object in the text of the first block of `content`
  instead: `study` reads `structuredContent` when there is one, and that text otherwise.
- **Names are spelled as on this page**, in lower case. A list is a JSON list even when it
  is empty.
- **Fields this page does not name are passed over, in both directions.** A plugin may
  add fields of its own to a result, and `study` reads only the ones this page names: what
  a plugin says in a field of its own reaches neither the learner nor the learner's agent.
  `study` sends no field that this version
  does not define, and a plugin passes over one it does not know all the same, without
  refusing: a field added later must not break a plugin written now.
- **Every field of a tool's arguments is required unless its row says it is optional.**
  Arguments that lack a required field, or hold a value this page does not allow, are
  refused with `invalid_argument`. `study` checks what it sends before it sends it, so a
  plugin sees such arguments only from another client; it refuses them all the same.
- **An optional field is left out, never given as `null`**, in arguments and in results.
  A search whose `sources` is `null` is refused, not taken for a search in every Source,
  and `study` refuses a Passage whose `location` is `null`.
- **Every field of a result is required too, unless its row says otherwise, under exactly
  its name.** `study` refuses a result that leaves one out, gives it as `null`, or spells
  it in another letter case: a `cancel` that answers `{}` has not said that it stopped
  nothing, and a Passage without a `score` has not scored 0.
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
| `message` | What went wrong, in words for the learner. `study` puts it on one line, escapes what a terminal would interpret, leaves out the characters that show nothing, and shows the first 2,000 bytes. |

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

**Words a plugin writes for the learner reach the learner's agent too**: a `reason`, a
`notice` and an error's `message`. `study` shows each on one line, cut short where the
table of limits says, and deals with two kinds of character that are not shown:

- **What a terminal would interpret is written out as an escape** that names it: a control
  character, and a mark that turns the direction of text.
- **What shows nothing is left out**: Unicode's format characters, other than those marks
  and than the signs that are written before digits and show; its variation selectors;
  and the other code points every display ignores. The tag block (U+E0000 to U+E007F) and
  the zero-width characters are among them. A sentence can be spelled in such characters,
  for a program to read and no person, and an escape written for each would still spell
  it: so none is written.

A plugin that takes a key or an address out of the words it repeats looks for it once
these characters are out: a key that a server repeated with one of them inside it is
whole again where `study` shows it. A Passage's `text` is the Source's, and no words of
the plugin's: what `study` does with it is said under [`passage`](#passage).

**What goes wrong with a Source is not an error of `index`.** A file that is not there, a
format the plugin cannot read, a page that cannot be fetched: `index` answers, and
`status` says `failed` with the reason.

## Names, ids and hashes

| What | Who chooses it | It is |
|---|---|---|
| A collection's name | `study` | Lowercase letters, digits and single hyphens, up to 64 characters: `linear-algebra`. It is safe as a file's name. The name `study` gives a Topic's collection is the Topic's id and a hash that tells the learner's Study homes apart, such as `linear-algebra-mfrggzdfmztwq2lknnwg23tpobyxe43u`: a plugin makes nothing of what a name says. |
| A Source's id | `study` | Lowercase letters, digits, hyphens and dots, starting with a letter or a digit, up to 128 characters: `strang-linear-algebra.k3f9a2`. |
| A Passage's id | the plugin | Letters, digits, dots, underscores, colons and hyphens, not starting with a hyphen, up to 128 characters. It names one Passage within its collection. |
| A content hash | whoever read the bytes | `sha256:` and the 64 lowercase hexadecimal digits of the SHA-256 of the bytes: of the file, or of a web page's body as it was fetched. |

Refuse a name or an id that is not shaped like this with `invalid_argument`: a collection
called `../other` must not become a path.

A Passage's id keeps naming the same text for as long as its Source's indexed content does
not change, whether or not the plugin was stopped and started in between. Once the content
changed, the id names nothing (`not_found`), or still the old text: never another text.

## The seven tools

### `contract`

Says which version of this contract the plugin speaks, and what it can do. It takes no
arguments, and gives the same answer every time in a session.

<!-- example: contract result -->
```json
{"version": 1, "sources": ["file", "url"], "search": ["keyword", "embedding"], "locations": ["pages"]}
```

| Field | Is |
|---|---|
| `version` | The contract version: `1`. **A plugin that reports a version newer than `study` knows is refused**, like every newer format, so report the version you speak, as a whole number. Any number larger than the version `study` knows is a newer version, however large it is and however it is written. |
| `sources` | The kinds of Source the plugin indexes: `file`, `url`, or both. At least one. |
| `search` | How it searches now: `keyword`, `embedding`, or both. At least one. A plugin that embeds only when an endpoint is configured lists `embedding` only then. |
| `locations` | The kinds of location its Passages can carry: `pages`. An empty list for a plugin that never knows where a Passage is. |

A kind is a lowercase word of letters, digits and underscores. Kinds are added to these
lists without a new contract version, a time range in a recording for instance, so `study`
keeps a kind it does not know and a plugin may list one.

Version 1 has seven tools. `diagnose` was added to it after the other six and before any
`study` that speaks the contract was released, so the version did not change: there is no
version 1 without it, and `study` refuses a plugin that lacks any of the seven.

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
| `title` | The Source's title, up to 200 characters and not empty, for the plugin's own messages. It is no content: a new title alone is nothing to index. |
| `kind` | `file` or `url`. Anything else is refused with `invalid_argument`. |
| `path` | A file's absolute path, on the computer `study` runs on. Required with `file`, and not allowed with `url`. A relative path is refused with `invalid_argument`. |
| `hash` | The content hash of the file as `study` read it. Required with `file`, and not allowed with `url`. |
| `url` | A web page's `http` or `https` address, without a user name or password. Required with `url`, and not allowed with `file`. |
| `refetch` | Optional. `true` asks for a web page that is indexed already to be fetched again. Left out otherwise. Only with `url`. |

<!-- example: index result -->
```json
{"source": {"id": "strang-linear-algebra.k3f9a2", "state": "indexing", "progress": 0}}
```

The result is `source`: what the plugin says of the Source now, shaped as in `status`,
and never `outdated`. Its
state is `indexed` when there was nothing to do, `indexing` when the work has begun, and
`failed`, with the `reason`, when the plugin can tell at once that it cannot index the
Source: `status` then says `failed` too. A plugin that finds out only later answers
`indexing`, and `status` says `failed` once it knows.

- **A file whose `hash` is the hash of what is indexed for that Source does nothing.** The
  answer is `indexed`, at once, and the Passages and their ids stay as they are.
- **Unless the plugin says the Source is `outdated`**: then being asked again is what
  brings it up to date, and the answer is `indexing`, or `indexed` when it took no time.
  The plugin may do that from what it holds, without reading the file again, and then the
  Passages and their ids stay too. `index` never answers `outdated`.
- Otherwise the plugin reads the file at `path`. Its content replaces what the index held
  for the Source: the old Passages are no longer found.
- **A file is indexed only when its bytes are the `hash` it was handed.** The plugin
  hashes the bytes it reads, the same bytes it indexes, in one reading. When they are not
  the `hash` of the `index` call, the file is not the one `study` read: it changed since,
  or its path leads elsewhere by now. The plugin then **keeps nothing of what it read**.
  The Source is `failed`, with a reason such as "the file changed since study read it",
  and what was indexed for it before is still found. This is no formality. A Source can be
  a file inside the Study home, where the learner's agent writes: the agent could exchange
  it for a link to any file of the learner's once `study` has read it, and a plugin that
  indexed what it found there would hand that file's text to the next search.
- **`status` gives the hash of the bytes the plugin read**, which it computes itself: it
  never gives the hash it was handed for content it did not hash.
- **A web page has no hash until it is fetched.** The plugin fetches it, and `status`
  gives the hash of what it fetched. A page that is indexed is fetched again only when
  `refetch` is `true`: otherwise the answer is `indexed`, at once.
- A Source that is being indexed already goes on being indexed when it is asked for with
  the same content, and starts over when it is asked for with other content.
- A kind the plugin's `contract` does not list, of `file` and `url`, is refused with
  `unsupported`. A kind that is neither is refused with `invalid_argument`.
- Indexing reads the Source. Nothing else does: `search` and `passage` read the index, and
  find what was indexed after the file has been moved away.
- **What was indexed for a Source is there to go back to until new content has replaced
  it.** A `cancel` that stops the indexing of new content leaves the earlier content found,
  with its `hash`. While new content is being indexed, and after indexing it has failed, a
  plugin either still finds the earlier content, and `status` gives its `hash`, or finds
  none of it, and `status` gives no `hash`.

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
  {"id": "scanned-notes.t8w3n5", "state": "failed", "reason": "the file has no text layer"},
  {"id": "hefferon-linear-algebra.m4c8d2", "state": "outdated",
   "hash": "sha256:2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae",
   "reason": "its Passages were indexed before an embedding model was set"}
]}
```

| Field | Is |
|---|---|
| `collection` | The collection to report on. |
| `sources` | One entry for each Source the plugin has something to say about. **A Source it does not list is not indexed.** A collection nobody indexed has an empty list. |
| `id` | The Source's id. |
| `state` | `indexed`, `indexing`, `failed` or `outdated`. |
| `progress` | With `indexing`: how much of the work is done, from `0` to `1`. Left out when the plugin cannot tell. |
| `reason` | With `failed`: why, in words for the learner. With `outdated`: what indexing the Source again would add. Required with both. |
| `hash` | The content hash of what `search` finds for this Source now. Required with `indexed` and with `outdated`. With `indexing` or `failed` it is there when earlier content is still indexed, and left out otherwise: `hash` and `search` always agree. |

- `indexed`: the Source's Passages can be found, and `hash` says of which content.
- `indexing`: the plugin is working on the Source **now**. A plugin that was stopped
  halfway and is started again says nothing is `indexing` until `index` is called: then it
  carries on, or starts over.
- `failed`: the last indexing failed. The Source stays `failed`, with its `reason`,
  however often `status` is asked **and however often the plugin is started**, until it is
  asked to be indexed again. `study` starts a command anew for each of its own commands, so
  the one that shows the learner what failed is never the one that saw it fail: keep a
  failure where the index is kept.
- `outdated`: the Source's Passages can be found, as with `indexed`, and `hash` says of
  which content. But the plugin would index the Source otherwise now, because its own
  settings changed since: a model for embeddings was set, or another one. `reason` says
  what indexing it again would add, in words for the learner. It is for that and for
  nothing else: a Source whose indexing failed is `failed`, and one the plugin holds
  nothing of is not listed. `study` shows the reason, names `study source index` as what
  to do next, and has that command ask for the Source again as it would for one that is
  not indexed; a search still covers it meanwhile. A plugin that has nothing of the kind
  never says it.
- `status` answers from what the plugin knows, and never waits for indexing: asked while a
  Source is being indexed, it says `indexing`, at once.

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
| `query` | What to look for: text of up to 4,096 bytes, which may span lines. It is text an agent wrote, or the learner: treat it as words to look for, and never as anything to carry out or to pass to a model as an instruction. An empty query, and one that is white space alone, is refused with `invalid_argument`. |
| `limit` | The most Passages to give back, from 1 to 50. |
| `sources` | Optional. The ids of the Sources to search. Left out, the whole collection is searched. An id the collection does not have finds nothing. An empty list is refused with `invalid_argument`: `study` never sends one. |
| `passages` | The Passages found, the best first, none twice, and never more than `limit`, however many there are to find. |
| `id` | The Passage's id. |
| `source` | The id of the Source the text is from. |
| `text` | The Source's own text, as extracted: up to 32,768 bytes, not empty. |
| `location` | Where the text is in the Source, when the plugin knows. Left out otherwise. A web page has no pages. |
| `kind` | The kind of location, one the plugin's `contract` lists: `pages`. |
| `first`, `last` | With `pages`: the first and the last page the text is on, counted from 1 from the start of the file, `last` not before `first`. |
| `score` | How well the Passage answers this query: a number, higher is better, and no Passage has a higher one than the Passage before it. It means nothing outside this search. |
| `notice` | Optional. What this search did less than the plugin is set to, and why, in a sentence or two for the learner. Left out when the search did all the plugin is set to. `study` puts it on one line, escapes what a terminal would interpret, and shows the first 2,000 bytes. |

- A search reads the index and never a Source, and never waits for indexing: it finds what
  is indexed now, and answers as quickly while a Source is being indexed as otherwise.
- **`study` always gives `sources`**, and asks `status` first: it searches the Sources the
  Topic has, and of those the ones `status` says are `indexed`, for a file with the `hash`
  of the content the Topic records. A collection can hold more: the Sources of a Topic
  that was removed stay in it. **A Passage of a Source that `sources` does not list is
  refused** by `study`, with the whole answer.
- **A score belongs to one search**, so `search` gives it and `passage` does not.
- A location's kind decides its other fields. Version 1 has `pages`. A plugin that lists
  another kind in its `contract` gives that kind its own fields, which this version of
  `study` passes over. **A location of a kind the plugin's `contract` does not list is
  refused** by `study`, with the whole answer.

**A search that did less than the plugin is set to says so in `notice`**, beside what it
found:

<!-- example: search result -->
```json
{"passages": [
  {"id": "7f3a9c12e5b04d68", "source": "strang-linear-algebra.k3f9a2",
   "text": "Elimination produces an upper triangular system.",
   "location": {"kind": "pages", "first": 46, "last": 46}, "score": 4.2}
 ],
 "notice": "These Passages were found by keyword alone: the embedding endpoint 127.0.0.1:8080 did not answer within 10 seconds."}
```

- A plugin whose search leans on something that can fail, an endpoint that embeds the
  query or a model that reorders what was found, may answer with what is left when it
  does: by keyword alone, say. Those Passages are then not the best the Sources hold, and
  whoever reads them must know. `notice` says what the search did without, and why, in a
  sentence or two for the learner, in the plugin's own words. A search that did all the
  plugin is set to has none.
- **A notice is no error.** `study` shows it to the learner before the Passages, gives it
  to the learner's agent with them, and does nothing else about it: the search succeeded.
- **It is the plugin's text, and `study` treats it as it treats a `reason`.** An answer is
  not refused for what the text of its notice says, or for a notice of more than 2,000
  bytes: `study` puts it on one line, escapes what a terminal would interpret, leaves out
  the characters that show nothing, so that the agent reads of it what the learner reads,
  and cuts it short after 2,000 bytes.
  Keep a notice within them all the same: the learner reads no further, and the
  conformance suite fails a plugin whose notice is longer. That is all it is spared: a
  `notice` that is no text, or is `null`, breaks the contract as any field does that is
  not what this page says, and an answer that is larger than a message may be is refused
  like any other.
- **It reaches the learner's agent, so it holds nothing the learner would not show one**:
  no key, and of an endpoint's address no more than its host, since a path or a query may
  be a key. Repeat a server's own words only without them.
- **Whatever else a plugin says of a search, it says in fields of its own**, which `study`
  passes over, as it does every field this page does not name: Shelf adds `search`, the
  kinds of search it used for the query, and `reranked`. Of what a plugin says about a
  search, only `notice` reaches the learner.

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
- **What `study` does with it.** It asks `status` first, and for a Passage only when the
  `hash` there says that what `search` finds of the Source is the content the Topic
  records, whatever the state; a Passage of another Source is refused. The quote must then be in the Passage's `text`, beginning
  and ending where a word does, with nothing differing but how much white space there is
  and of which kind, an accent written apart or a ligature, the shape of a quotation mark,
  the length of a dash, and the hyphen and line break of a word cut at a line's end. White
  space is compared: it must be there where the Passage has it, and nowhere else. So
  **give a Passage's text as it was extracted, with its line breaks**:
  - A word cut at a line's end is known by the line break after its hyphen (or the end of
    the Passage): `elimi-`, a line break and `nation` may be quoted as `elimination`. A
    plugin that makes a space of that line break, or takes it out and keeps the hyphen,
    gives `elimi- nation` or `elimi-nation`, and only that is then a quote. So leave a
    division as it was extracted, or join the word and drop the hyphen where you know it
    for a division, as Shelf does.
  - A space the extraction lost between two words, or made up inside one, is in the
    Passage as you give it, and a quote must have it the same way. Text that reads right
    is what gets quoted.
  - `study` hands a Passage on without the characters of Unicode's tag block (U+E0000 to
    U+E007F), which show nothing, and compares a quote without them and without any other
    format character. Nothing else is taken out of a Passage, though it shows nothing: a
    joiner, a soft hyphen or a variation selector may be part of what the Source says. A
    variation selector is compared as the mark it is, like an accent: a quote has it
    where the Passage does.
- **Cut Passages where the Source has white space.** `study` reads the end of one Passage
  and the beginning of the next as separated by white space, since it cannot know
  otherwise: a quote that runs through both must have white space there, unless a word
  was cut with a hyphen or the script is written without spaces. A Passage that ends
  inside a word has that word quoted from neither.
- **A quote can run from one Passage into the next.** `study` then asks for each, up to
  five, and looks for the quote in their texts read one after the other, in the order the
  learner's agent gave. Nothing in this version says which Passage follows which in a
  Source, so `study` cannot tell that the Passages it is given are adjacent.

### `diagnose`

Has the plugin test what it depends on, and say what it found: an embedding endpoint that
answers, with vectors of the size the plugin's settings name; a program it runs, where it
should be. It takes no arguments. A plugin starts and speaks the contract whether or not
what it depends on works, and without this tool the learner finds out by using it: when a
book was read and its Passages could not be embedded, or when a search fell back to
keyword.

<!-- example: diagnose result -->
```json
{"findings": [
  {"name": "embedding", "ok": true,
   "message": "the embedding endpoint 127.0.0.1:8080 gave a question's vector, of 512 numbers"},
  {"name": "reranking", "ok": false,
   "message": "the reranking endpoint 127.0.0.1:8081 could not be reached: connection refused"}]}
```

| Field | Is |
|---|---|
| `findings` | A finding for each thing the plugin depends on and tested, 16 at most. **An empty list for a plugin that depends on nothing of the kind**: that is an answer like any other, and the plugin still has the tool. |
| `name` | What was tested, in a word: a lowercase word of letters, digits and underscores, up to 32 characters, such as `embedding`. Each name once in an answer, and the same names every time, in whatever order: `study` shows the findings in the order of the answer, each under its name, and a learner tells one finding from the next by it. `study` shows a name as it is, so it refuses an answer with a name that holds a part of the address the plugin is registered at. |
| `ok` | `true` when it works as the plugin needs it to, `false` when it does not. |
| `message` | What was found, in a sentence for the learner: what answered, or what is wrong and what the plugin does without it. Required, for a finding that is all right too. `study` puts it on one line, drops the characters that show nothing, escapes what a terminal would interpret, and shows the first 600 bytes. A message of which nothing is left then is none, and `study` refuses the answer. |

- **Test it as you use it.** Send what indexing or a search sends: the same request, with
  the key, to the same address, and hold the answer to what you hold it to there. A test
  that only opens a connection finds an endpoint up when its model is missing.
- **Test with text of your own.** Nothing of a Source, a collection or a learner's
  question goes into a test. `diagnose` is given no collection, and is asked when the
  learner looks at their setup, not when they study.
- **Answer within 10 seconds, whatever you depend on does.** That is for all of it: every
  test, and the answer. Give each thing you test less than that, and test them side by
  side, so that an endpoint that never answers costs its own finding and not the whole
  answer. A plugin that does not answer in time is stopped, and `study` reports that it
  did not say what it found, and goes on using it: no other tool is the worse for this
  one. `study doctor` does not start it again, in that run, to ask what it has indexed.
- **A finding that is not all right is no error.** The tool answers, and the finding says
  so. An error is for a plugin that could not test at all. `study` then reports that the
  tool failed, as `failed_precondition` whatever `code` the error has: the tool is asked
  nothing a plugin could find wrong or missing.
- **Put nothing in a message that is not for the learner's screen**: no key, and of an
  address no more than its host. `study` cannot take out what it does not know. It takes
  out the address the plugin itself is registered at, and nothing else.
- **Give no command to run.** `study` shows a finding as the plugin's words, and adds no
  command to it. A field of your own, a `fix` say, is passed over like any other this page
  does not name.
- **Who asks.** `study knowledge-base check <name>` and `study doctor`, which the learner
  runs, or their agent in its own shell. There a finding that is not all right is a
  warning, and never keeps the learner from studying. **No tool of `study`'s MCP server
  asks `diagnose`**: whether what a plugin depends on works is the learner's question,
  which no agent needs the answer to, and no tool waits 10 seconds for a plugin.

## Checking a plugin

`study knowledge-base check <name>` starts or reaches a registered plugin as `study` does
for a Topic, and reports the contract version it speaks, what it can do, and what its
`diagnose` found, or what is wrong and what to do about it. `study doctor` does the same
for every registered plugin.

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
		// Each connection starts the command again, as study starts a registered
		// plugin, on a computer made of temporary folders.
		// For a service: plugintest.AsService(address).
		Connect:  plugintest.AsCommand(plugintest.NewComputer(t), "/opt/notes-kb/bin/notes-kb", "--stdio"),
		Restarts: true,
		// A plugin that does not read plain text says how to write a file it reads.
		WriteFile: writePDF,
	})
}
```

- **A command is started exactly as `study` starts one**: the same environment, working
  folder, pipes and folder as above. So the suite also sees what only a command can get
  wrong: that it exits when its standard input closes, writes nothing in the folder it is
  started in, and follows no link out of its own folder.
- **The files it writes**, unless `WriteFile` says otherwise, are plain text in UTF-8 with
  the extension `.txt`: paragraphs divided by a blank line, and pages by a form feed
  (U+000C) at the start of a line, as `pdftotext` divides them. Most have three pages of
  one or two short paragraphs; the ones meant to take a while have 400 pages of one
  paragraph each. Every file holds a made-up word that is in no other, which the suite
  searches for. `WriteFile` is given the same pages, as lists of paragraphs.
- **The web pages it serves**, unless `ServePage` says otherwise, are HTML from
  `127.0.0.1`: a `<title>`, an `<h1>` and one `<p>` for each paragraph.
- **A plugin that stops answering fails the check that met it, and the suite ends there.**
  A call has 30 seconds (`Patience`).
- **`diagnose` is asked as it is**, with whatever the plugin was started with, and twice:
  the suite holds each answer to the 10 seconds, to the limits and to the shape of a
  finding, and the two to the same names, in whatever order. It takes no view on whether
  a finding is all right. A plugin that depends on an endpoint is so checked with the
  endpoint the test gave it, or with none.

The same package holds a small plugin that passes the suite, to read beside this page.

## What version 1 leaves out

- Nothing removes a Source, or a collection, from an index. A Source the Topic no longer
  has stays indexed; `study` limits its searches to the Topic's Sources with `sources`.
- `study` sends no file's bytes. A plugin is handed a path.
- A plugin is handed no environment of the learner's but the few variables above, and no
  way to ask `study`, or the learner, for anything.
- A plugin is told nothing of Topics, Lessons or the learner.
- A Passage does not say which Passage comes before it or after it in its Source, nor
  whether it overlaps another.
