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

A Knowledge base plugin is an MCP server with these six tools. Their names and shapes are
Lamplight's; what is behind them is the plugin's.

| Tool | Takes | Gives back |
| --- | --- | --- |
| `contract` | nothing | the contract version, and what the plugin can do: file Sources, URL Sources, keyword search, embedding search, which kinds of location |
| `index` | a collection, and one Source: id, title, a path with its content hash, or a URL | an answer at once; the work goes on afterwards |
| `status` | a collection | for each Source: indexed, indexing with its progress, or failed with the reason, and the hash of the content that was indexed |
| `cancel` | a collection and a Source's id | indexing of that Source stops; what was indexed stays |
| `search` | a collection, a query, a limit, optionally some Sources | Passages, best first, each with its score for that query |
| `passage` | a collection and a Passage's id | that Passage |

- A **collection** is a name `study` chooses for a Topic. A plugin keeps collections apart
  and knows nothing else about Topics.
- A **Passage** has an id, the Source's id, its text and, when the plugin knows one, a
  location. The text is the Source's own, as the plugin extracted it: never a summary and
  never a generated answer. A score belongs to one search, so `search` gives it and
  `passage` does not.
- A **location** says what kind it is. Version 1 has `pages`, with a first and last page.
  A Passage from a web page has none. Other kinds, such as a time range in a recording, are
  added to the same field without a new contract version; `contract` says which kinds a
  plugin gives.
- `index` never blocks. It returns at once and `status` is asked again, because tasks are
  not available to us. A plugin that was stopped halfway carries on or starts over when
  asked again.
- `cancel` is how indexing is stopped, since no request is open to cancel once `index` has
  returned. `study source index` calls it when it is stopped, so no work goes on behind a
  command that has exited, however long the plugin itself lives.
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
- The command's program is resolved to an absolute path when it is registered, and a path
  inside the Study home is refused. It is started from the registry's folder, never from
  the Topic or Study home that `study` was started in: a command such as `python -m …`
  would otherwise load a file the agent left in the Topic.
- `study` gives a command plugin a folder of its own under the Study home's `.lamplight`
  to keep its index in. That is this machine's state, never synced, and rebuilt by indexing
  again.
- A plugin reached by URL is a service the learner runs. It reads files and fetches
  addresses from where it runs, outside any sandbox the agent is in, so running
  `study source index` inside a sandbox does not limit what that service can reach. Such a
  plugin must itself be told what it may read and fetch. Shelf, served over a URL, refuses
  a file outside the folders it was given, and fetches no URL Source unless fetching was
  turned on for it, and then never a loopback, link-local or private address.
- A Topic that names a plugin this machine does not have is treated as `none`, and
  `status` gives the one action that fixes it. So is one whose plugin does not start or
  does not speak the contract.

Building the registry showed that three of these rules needed more than they say:

- "Inside the Study home" is decided by file identity, at every step on the way to a folder
  or a program, from folders that are already open. Deciding from names let an agent repoint
  a link between the check and the use, and let another letter case through on a file
  system that ignores case.
  - A folder on the way is opened to be passed through, not to be listed. The first
    registry opened each folder as Go's `os.Root` does, which asks to list it, and so
    failed wherever a folder may be passed through and not listed: `/home` with mode 711
    on a shared computer, above everything the learner has. `study` now asks to list a
    folder and, where that is refused, asks only to pass through it (`O_PATH` on Linux,
    `O_SEARCH` on macOS 13 and later), and looks at each name beside the folder's
    descriptor itself, since an `os.Root` cannot be made from one.
  - **A path that cannot be followed to its end is never outside the Study home.** The
    first registry called such a path "not inside", reasoning that whatever stopped
    `study` would stop whoever used the path; the folders it could not list showed that
    this need not hold. Where a walk stops, nothing was decided: a program or an argument
    by such a path is refused, the environment leaves it out, no folder is made below it,
    and the listing gives the plugin a `problem`. The learner can still allow an argument,
    as one inside the Study home.
- A command's arguments are checked as its program is. `python3` with a script inside the
  Study home is refused unless the learner allows it when registering, and the registry
  records that they did. This catches the honest mistake and is not a guarantee: a command
  such as `sh -c` decides more when it starts than any check can see.
- What writes the registry is in a package that only the CLI imports, and a test checks
  that the MCP server's dependencies do not contain it. A rule about names was passed by
  one wrapper function.

Building the client, and having it attacked in review, showed that starting a command
needed more than these rules say too:

- What the system starts for a script is the interpreter its first line names, so that
  program is checked as the plugin's own is, and its own interpreter after it. An
  interpreter named by a relative path is refused: the system would look for it from the
  folder the plugin is started in. And `#!/usr/bin/env python3` names `env`, which is no
  interpreter: it starts whatever the plugin's `PATH` has under the name first. A folder of
  that `PATH` is outside the Study home, and a link in it need not lead outside. So `study`
  finds what `env` would start, as `env` finds it, and checks that. An interpreter `study`
  cannot read is refused, and so is a script past the fourth in a row: the system reads
  and follows what `study` would have left unchecked.
- **A plugin gets a small environment that `study` makes, not the caller's with things
  taken out.** The environment `study` has can point into the Study home: an agent started
  in a Topic with its virtual environment switched on hands the MCP server a `PATH` and a
  `PYTHONPATH` into the Topic, and a plugin started with those, outside the sandbox, would
  run what the agent wrote there. The first client took out of the environment whatever
  named something inside the Study home, and the review showed that this does not hold: a
  variable has many ways to have a program load something, and a list of what to take out
  is never finished. So a plugin gets `PATH`, `HOME`, `USER`, `LOGNAME`, `LANG`, `LC_*`,
  `TZ`, `TMPDIR`, the XDG base folders and `LAMPLIGHT_PLUGIN_DATA`, each only when it leads
  nowhere inside the Study home, and nothing else. A path the check cannot decide counts as
  inside. A plugin that needs more, a key or a proxy, takes it in its arguments, such as
  the path of a file that holds the key, or the learner registers a script of their own,
  outside the Study home, that sets it.
- **The plugin's folder is in the learner's state folder, outside the Study home**:
  `$XDG_STATE_HOME/lamplight/knowledge-base-plugins/<name>`. The first client put it under
  the Study home's `.lamplight`, where the agent writes, and refused to start a plugin
  whose folder was a symbolic link. The review showed that this does not hold either: it is
  a look at one moment, and the plugin opens the name for as long as it runs, so the folder
  can be exchanged for a link afterwards and the plugin writes where the link leads. And an
  agent that can write the index can write what a search finds, so a quote checked against
  a Passage would be as good as the agent's word. A state folder that is the Study home,
  is inside it or is reached through it is refused by the registry's rule, and the folder
  is the learner's alone to read. The cost:
  `study source index` run inside a sandbox starts the plugin inside it, so the sandbox
  must allow that folder, or the learner indexes from their own shell. The contract still
  has a plugin open its folder once and follow no link out of it, and the conformance suite
  checks that.
- A plugin is started with no file of `study`'s but its three pipes. Whatever `study` was
  itself started with, a descriptor for a file or a copy of its standard output, is closed
  in the plugin: in the review a plugin wrote a line in front of the `--json` answer
  through one.
- Only Linux can run a program through a folder that is already open. There `study` opens
  the folder that really holds the program when it checks it, and runs the program by its
  own name in that folder, so no folder on the way is looked up again. Running the open
  file itself would pin the file as well as its folder, and was the first way; but the
  system then calls the process by a number, and a script is handed a descriptor's name
  with nothing beside it, so a script that starts the program next to itself works in a
  terminal and not under `study`. Whoever can put another file under the program's name in
  its own folder could have done so before the check, and the agent cannot: the folder is
  outside the Study home. On macOS the program is started by its path after a last look
  that the path still names the file that was checked, which leaves the moment between
  that look and the system's own.
- **A plugin sends `study` no requests.** `study` offers a plugin nothing to call, and the
  MCP library answers every request a server sends in a goroutine that then waits to write
  its answer: a plugin that sends requests without end, and reads no answer, has `study`
  allocate without bound. The first request ends the session, on both transports.
- A `study` that is killed can stop nothing. Its end of the plugin's standard input closes
  with it, and the contract has a plugin exit when that happens. A program a plugin moved
  to a process group or a session of its own is not stopped by `study` either: the plugin
  stops what it starts.
- A rule about how the core is written was passed three ways, as the rule about names
  above was passed by one wrapper. So what keeps a tool from starting a plugin is checked
  by what the tools do: plugins that record being started or reached are registered, every
  tool is called, and none of them was. Reading the source is the second line.

ADR-0009 stands: the core runs nothing that a Topic's files name.

### Indexing runs only through the CLI

- `study source index` is the one thing that asks a plugin to index. It indexes a Topic's
  Sources and shows progress until it is done; stopping it leaves what was indexed.
- It is CLI-only, like `study check` (ADR-0009): the agent runs it in its own shell, or the
  learner does. A command plugin is then started inside the harness's sandbox, so the
  sandbox and approval prompts apply to every file it reads and every address it fetches.
  A plugin reached by URL is outside that sandbox and keeps to its own limits, as above.
  No MCP tool starts indexing. If one did, an agent could add any file on the computer as
  a Source over MCP and read its text back through a search.
- Adding a Source, over MCP or the CLI, records it and indexes nothing. `sources` and
  `status` say which Sources are not indexed and give the command.
- Whether a Source is indexed is the plugin's to say, on this machine. It is not in the
  History and records no Event.

Building the Topic's plugin and `study source index` showed what these rules needed, and
changed one of the ones above:

- **A tool has a plugin started, and never has it index.** The client was built with a
  test that no MCP tool starts or reaches a plugin. `status` and `sources` have to say
  which Sources are indexed, and only the plugin knows: so those two tools have the core
  start the Topic's plugin, or reach it, and ask its `status` tool. What is kept is the
  rule that matters: no tool causes `index` to be called. The test now registers plugins
  that record what they are asked, has the Topic name one, calls every tool, and fails for
  anything but `contract` and `status`; it also fixes which tools ask a plugin anything.
  A second test reads the core for the one function that asks a plugin to index, and for
  who can reach it. Review showed both too weak as first written. The first named the
  plugin and added a Source halfway through, so seventeen tools were never called on a
  Topic a plugin could index for: the Topic now has both from before the first call, and
  the test fails when its Source is gone or indexed at the end. The second read names
  alone: it now tells a method from one of the same name on another type, watches the
  `cancel`, `search` and `passage` of a Connection as it watches `index`, refuses
  reflection in the core, which calls by a name no test can read, and a third test holds
  the client to asking each tool in the one method named after it.
- **A plugin is not kept running.** This ADR had `study` start a command when a Topic
  first needs it and stop it when `study` exits. That is so for one command, and it was not
  built for `study mcp`: each tool call that needs a plugin starts it, asks, and stops it
  before it answers. A program kept for the length of an agent's session would be a
  second long-lived process outside the agent's sandbox, and would outlive a change of the
  registry: a plugin the learner replaced or removed would go on being asked. The cost is
  a start for every `status`, and it is why the next rule cuts a slow plugin off.
- **Nothing waits for a plugin, so `status` gives one two seconds.** The client gives a
  plugin thirty seconds to start, which is right for indexing and far too long for
  showing where the learner is. For `status` and `sources`, reading the registry, starting
  the plugin and its answers share one deadline of two seconds; a plugin that misses it is
  stopped, and its Sources are shown as not known, with the command that says what is
  wrong. Opening a Session asks no plugin anything. The cost: a plugin that takes longer
  than that to start, as one that loads a model first would, never says in `status` what
  it has indexed. `study source index` and `study knowledge-base check` still give it the
  thirty seconds. The Topics that name one plugin share those two seconds and are asked
  one after the other, the Active topic first. `status` asks only about Topics that are
  active: one that is paused or finished has nothing to do next. A Topic the time did not
  reach says so, and its action is `study source list`, which asks about it alone: the
  command that says what is wrong with a plugin would say that nothing is. The
  session-start hook of the Claude Code plugin prints `status`, so it asks too, and a
  session starts at most that much later for a plugin that hangs.
- **A collection's name tells Study homes apart.** A plugin's folder is one for the
  plugin on a machine, not one for each Study home, and a service may serve several
  machines. Named after the Topic's id alone, two Study homes that each hold a Topic
  `linear-algebra` would share a collection, and a search in one would find the other's
  books. The name is the Topic's id, cut to fit, a hyphen, and 32 characters of a SHA-256
  of the Study home's real path and the id. The path is one the agent does not choose: it
  writes inside the Study home, and cannot have another folder found at that path. Every
  name ends with the hash, so no two Topics share one, whatever their ids were cut to. A
  Study home that is moved or renamed has its Sources indexed again; what the plugin holds
  under the old name stays, unused, since nothing removes a collection. Two limits follow.
  A Study home that `study` sees under another path, inside a sandbox that shows it
  elsewhere, is another collection there. And a Topic that is removed and another made
  later under its id share one, with the removed Topic's Sources still in it under their
  own ids: so a search must be limited to the Sources the Topic has, by their ids, and to
  the content the Topic records, by its hash.
- **`cancel` is asked with a context of the command's own.** When `study source index` is
  stopped, its context is cancelled, and the client asks a plugin nothing with a cancelled
  context. So the plugin is told to stop under a context made then, which lasts ten
  seconds, for the one Source the command had asked for and not seen finished. A command
  plugin is stopped after that whatever it answered, and its indexing ends with it. A
  service that cannot be told is reported as possibly still indexing: that much can go on
  behind a command that has exited, and the learner is told.
- **A Source is its content, so a plugin keeps nothing of a file that is another.** The
  contract has `study` hand a file over with the hash of the content it read. A Source
  records the hash of the file that was added, Evidence quotes that content, and a file
  with other content is another Source. So `study` reads the file once more before it
  hands it over, and skips one that is missing or has other content now, with what to do.
  The path handed over is the file's real one, with every link followed, and the command
  shows it: a Source inside a Topic can be a link the agent made, and a plugin the learner
  runs in their own shell reads what it is handed with the learner's permissions.

  The contract first let a plugin index whatever it read and say so through the hash, and
  `study` read every file before it asked for the first. Review showed what that allowed.
  A plain file inside the Study home is the agent's to exchange for a link, to any file of
  the learner's, once `study` has read it; for the second Source it had as long as the
  first took to index. The plugin then read the learner's file, with the learner's
  permissions, and held its text in the Topic's collection for the next search, while the
  command reported a file that "changed while it was indexed". So the rule is now the
  plugin's: **it keeps nothing of a file whose bytes are not the hash it was handed**, and
  the Source is failed. The hash `study` checked is then the only content that can come to
  be indexed, whatever a path leads to by the time the plugin reads it. `study` also reads
  each file just before it asks for it, which leaves the agent milliseconds and not
  minutes, and still counts a Source as indexed only when the plugin's hash is the
  Source's, for a plugin that breaks the rule.

  This does not make indexing in one's own shell safe against an agent that added a file
  it should not have as a Source. `source_add` records any file the learner can read, the
  command shows where each file really is, on a line under a title the agent chose, and
  `--dry-run` shows them first: whoever runs it without reading that list has a plugin
  read those files, and the next search gives their text to the agent. What a sandbox
  keeps an agent from reading, it can ask for this way. Asking the learner before files
  outside the Study home are handed over for the first time would close it, and is not
  built.
- **What goes wrong with a Source is no error of the command**, as it is none of `index`
  in the contract: the Source is failed, with the plugin's reason, the others are indexed,
  and the command exits with 0. It fails when it could not do its work at all: no plugin,
  one that does not start or stops answering, a folder it cannot write in. A plugin that
  answers `index` with what is not the contract's is one of those: only what the plugin
  refuses, as the contract has a refusal, is a Source's failure.
- **What `study` cannot stop.** A `study` that is killed, or ends on `SIGQUIT` or on a
  closed terminal, stops nothing: a plugin exits when its input closes, and one that does
  not is left running. A program a plugin moved out of its process group is left by every
  `status`, which an agent can now call as often as it likes. Both are a plugin breaking
  the contract, and the learner registered it.
- **A failure outlives the plugin's program, and is no next action.** The contract let a
  plugin forget that a Source failed once it stopped. A command is now stopped after every
  `study` command, so the one that shows the learner what failed is never the one that saw
  it fail: `status` said "not indexed" of a scan without text, the one action was to index
  it, and indexing it failed again, every Session. The contract now has a Source stay
  `failed`, with its reason, however often the plugin is started, until it is asked to be
  indexed again, and the conformance suite asks over a new connection. And a failed Source
  is shown with its reason without being the next action: `study source index` is offered
  for Sources the plugin has not tried, and has the failed ones tried again when someone
  runs it. For the same reason it is not offered for a file that is missing or has changed,
  which the command skips: `status` then points at `study source list`, which says what to
  do about the file.
- **A folder that cannot be written in is found before the plugin starts.** A sandbox
  refuses what a folder's mode allows, and a plugin would only say, each in its own words,
  that its index cannot be written. `study` makes and removes an empty file in the
  plugin's folder before it starts a plugin to index, and the error for a folder that
  cannot be made or written in gives both ways out: allow the folder in the sandbox, or
  run the command in a shell of one's own. A plugin passes over a name in its folder that
  begins with `.lamplight-`.
- **A name is recorded whether or not this machine has it**, since another may: naming a
  plugin over MCP or the CLI never fails for a name that is not registered, and says what
  is missing. A name is its own payload, so it is recorded by an Event type of its own,
  `knowledge_base_plugin.set`; `knowledge_base.set` records a kind alone, as before, and
  is read so whatever its payload holds.

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

Building the search and the check settled what these two sections left open, and showed
what they needed:

- **A tool searches a plugin, and still never has it index.** `evidence_search` and
  `evidence_record` are tools, and both have the core start the Topic's plugin: the first
  asks its `status` and `search` tools, the second, given a Passage, `status` and
  `passage`. Each reads the plugin's index and none reads a Source, which is what the rule
  against indexing from a tool is for. The test that calls every tool now fixes, for each
  tool, exactly what it has a plugin asked, in order, on a Topic that has one Source the
  plugin has indexed and one it has not: so a tool that came to ask more, `index` and
  `cancel` above all, is seen whichever tool it is. The test that reads the core names the
  one function in which a plugin is searched and the one in which it is asked for a
  Passage, and who may use them.
- **A search has 45 seconds for all of it.** The client gives a plugin 30 seconds to start
  and 60 to answer a search, which together are more than an agent's tool call can wait:
  many MCP clients give a call up after a minute, and `study` must have stopped the plugin
  and said what is wrong before then. So reading the registry, starting the plugin and its
  answers share one deadline of 45 seconds, for a search and for the Passages of a quote
  alike: more than a plugin has to start, because one that needs most of that still has a
  query to embed, and far more than the 2 seconds of `status`, because this is work the
  agent asked for and not a look at where the learner is. A plugin that misses it is
  stopped at once. The cost is the contract's: a plugin whose model takes long to load
  loads it for every search, since no plugin is kept running, so the model belongs in a
  service that stays up. Recording Evidence can take as long again when another write
  holds the Topic's lock, which is why the plugin is asked before the lock is taken.
- **The plugin says what it holds before it is asked to search.** A search is limited to
  the Topic's Sources, by their ids, and of those to the ones the plugin says are indexed,
  for a file with the hash of the Source's own content. `status` is asked first and
  `search` is given the ids that passed: asked the other way round, the Passages of a
  Source that did not pass would have taken places among the best. A plugin that answers
  with a Passage of a Source it was not asked for breaks the contract, and its whole answer
  is refused, as the client already refused it. The same holds for the Passages of a
  quote: the Source must be indexed as the Topic records it, and each Passage must be of
  that Source, or a quote could be "checked" against the text of a Topic that was removed.
- **No plugin is an error of a search, and nothing indexed is not.** A Topic that names a
  plugin this machine does not have is treated as `none` wherever something is shown. A
  search is asked for, so it says what it could not do: a Topic that names no plugin, and
  a plugin that is missing, does not start or does not answer in time, are
  `failed_precondition`, with the sentence that the agent reads the Sources itself
  meanwhile. Sources that are not indexed are no failure: they are left out, and the
  result names them, why, and `study source index`.
- **What "in its text" allows.** A Passage is text as it was extracted, and whoever quotes
  it writes the same words and seldom the same bytes. Both are read as NFKC, so an accent
  composed or apart and a ligature and its letters are one thing; white space, and
  characters that are not shown, are not compared at all; a quotation mark of any shape is
  a quotation mark and a dash of any length a dash; and the hyphen of a word cut at a
  line's end may be left out, a hyphen being one when it follows a letter and white space
  and a letter follow it. Everything else is compared as it is, letter case included. That
  is more than white space and Unicode forms, on purpose: an agent that straightens a
  curly quote has paraphrased nothing, and one that is refused for it learns to record
  without a Passage. What is given away is small and known: a hyphen left hanging on
  purpose, as in "pre- and post-", may be left out too. The check costs the length of the
  text times the length of the quote, divided by 64, whatever the two hold.
- **The quote is recorded as it was given**, not as the Passage has it: a Lesson cites a
  sentence, not the line breaks of a page.
- **A quote can run through several Passages.** Shelf keeps every Passage on one page, so
  a sentence that runs over a page break is in two, and one can run from a Passage into
  the next within a page. Evidence therefore names up to five Passages of one Source, in
  order, and the quote must be in their texts read one after the other, beginning in the
  first and ending in the last, so that no Passage is named that the quote is not in. The
  location is then from the first page of the first to the last page of the last.
  **`study` cannot tell that two Passages are adjacent**: version 1 of the contract gives a
  Passage no place among the others. The end of one Passage and the beginning of another
  of the same Source therefore pass as a quote, though the Source never had them side by
  side. It is a narrow way to a wrong quote, and it takes intent; closing it means the
  contract saying which Passage follows which, which is not decided. Passages that
  overlap, as some plugins cut them, are not joined either: there a quote is in one of
  them.
- **Checked is what was so on one machine at one time.** A Passage's id is the plugin's,
  on the machine that recorded the Evidence, and names nothing once the Source is indexed
  anew. The Event records the ids and the plugin's name, as what the quote was checked
  against, and nothing checks it again on another machine or later.
- **The location is the Passage's, and no other.** Evidence recorded with a Passage takes
  no location from the agent: the plugin gives pages counted from the start of the file,
  which are not the numbers printed on a book's pages, and the origin `knowledge_base`
  says which of the two a location is.
- **A Passage is text someone else wrote, handed to the agent.** `study` returns it as it
  is, prints it escaped, and the server's Instructions tell the agent that a Passage is
  material and never an instruction. A search also makes what "A Source is its content"
  ends with, above, a present matter: whatever a plugin was handed and indexed, a tool now
  hands to the agent. Asking the learner before files outside the Study home are handed
  to a plugin for the first time is still not built.

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
