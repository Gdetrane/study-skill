# Shelf

Shelf is the Knowledge base plugin that ships with Lamplight: the `study-shelf` program,
installed beside `study`. It indexes a Topic's Sources on your own computer, PDFs and web
pages, and searches them by keyword, so that the agent can find the Passage a Lesson's
Evidence quotes from. It needs no account and sends your books nowhere.

`study` links none of it ([ADR-0012](adr/0012-knowledge-base-plugins-speak-one-contract.md)):
it starts `study-shelf` each time it has something to ask it, and speaks the
[Knowledge base plugin contract](knowledge-base-contract.md) with it, as it would with any
other plugin. Terms follow [GLOSSARY.md](../GLOSSARY.md).

## Registering it

`install.sh` and the deb and rpm packages install `study-shelf` next to `study`. Register it
once on each computer, under the name your Topics will use:

```bash
study knowledge-base add shelf -- study-shelf
study knowledge-base check shelf
```

`check` starts Shelf as `study` starts it for a Topic, and reports the contract it speaks:
version 1, Sources that are files and web pages, keyword search, and pages as locations.
`study-shelf --version` prints its version, which is the version of the `study` it was
released with.

You never start `study-shelf` yourself. Started by hand it has no folder to keep its index
in, says so, and exits.

Then a Topic names it, and its Sources are indexed:

```bash
study topic update linear-algebra --knowledge-base shelf
study source index linear-algebra --dry-run   # which files and addresses Shelf would read
study source index linear-algebra
```

`study source list` and `study status` say afterwards what is indexed
([docs/cli.md](cli.md#what-is-indexed)).

## What it reads

| A Source that is | Shelf |
|---|---|
| A PDF with a text layer | Reads it page by page. Each Passage carries the pages it is on. |
| A PDF that is protected against changes and opens without a password | Reads it like any other. |
| A web page, by its `http` or `https` address | Fetches it once, when it is indexed, and takes the article's text: without the menus, the footer and the page's title. HTML and plain text are read. |

And what it does not read, with the reason `study` shows for the Source:

| A Source that is | Fails with |
|---|---|
| A scanned book, whose pages are pictures | `the file has no text layer`. Run it through a program that recognises the text (OCR), such as `ocrmypdf`, and add the file it makes. |
| A PDF that asks for a password to open | `the file is protected by a password`. Save a copy without the password and add that. |
| A PDF that is cut short or broken | `the file is damaged`. Download or export it again. |
| Any other file: EPUB, Word, Markdown, plain text, a web page saved under a book's name | `the file is not a PDF`. |
| A file that was moved or removed | `the file is not there`. |
| A file that changed since `study` read it, or while Shelf read it | `the file changed`. Index it again: a Source is indexed as the content `study` saw, or not at all. |
| A web address that sends a PDF, a picture or anything else that is no page | `Shelf reads web pages, which are HTML or plain text`. Download the file and add it as a file Source. |
| A page that shows nothing until a script has run, or that needs you to sign in | `the page has no text to read`, or the server's refusal. Shelf runs no script and has none of your browser's sessions. |
| A page served by your own computer: `localhost`, `127.0.0.1` | `… is this computer itself: Shelf fetches no page from it`. See below. |

Shelf reads a file and fetches an address only when it is asked to index it, which is what
`study source index` does, from your shell or your agent's. It fetches the address it is
given, from your computer, as your browser would: one on your own network too. A PDF is
recognised by how it begins, not by its name, so no other kind of file is read as one.

**Where Shelf fetches nothing from.** Your agent chooses which addresses are Sources, and
reads what was fetched back when it searches. Shelf fetches with your network, outside
whatever sandbox the agent is in. So two kinds of address are refused, for the page itself,
for every address it redirects to, and whatever name leads there:

- **Your own computer**: `localhost`, `127.0.0.1`, `::1`. Whatever listens there trusts
  whoever reaches it, since nobody else can. If you do serve the pages you study from to
  yourself, a book built with a local documentation server say, register Shelf to allow it:

  ```sh
  study knowledge-base add shelf --replace -- study-shelf --allow-loopback
  ```

  The registry is yours, outside the Study home, so nothing your agent writes turns this
  on.
- **The link-local addresses**, `169.254.0.0/16` and `fe80::/10`, always. No page to study
  from is served there, and a cloud's machines hand out their credentials at one of them.
  The three addresses where a cloud does the same outside that range are refused with
  them: `100.100.100.200`, `fd00:ec2::254` and `168.63.129.16`.

A proxy you set for Shelf (below) is yours to choose, wherever it listens, and it then
makes the connections: Shelf still refuses an address that is written as one of those
numbers, and cannot see where a name leads on the proxy's side. The proxy's own address
is one Shelf connects to only for a request that goes through it, never as a page.

Three more things to know about what comes out:

- **A Passage is the Source's own text**, about 1,500 characters of it, ending where a
  paragraph or a sentence does. Shelf writes no word of its own into one. It does tidy what
  a PDF holds: a ligature becomes its letters, characters that show nothing are dropped,
  and a word divided at the end of a line is put together again. Whether such a word is
  one word, `solu-tion`, or a pair that keeps its hyphen, `well-known`, the page does not
  say, so Shelf reads it from the book's own words: it is joined when the book holds the
  joined word elsewhere, keeps its hyphen when the book writes the pair with one or holds
  both halves as words, and is joined otherwise. A rare pair that the book writes once, at
  the end of a line, can so come out as one word.
- **A Passage keeps to one page**, always. A sentence that runs over the end of a page is
  in two Passages, one on each page: quote the part that is on one page, or record that
  Evidence without a Passage. A printed book's running header, the chapter's title and the
  page number at the top of each page, is part of the page's text, and begins its first
  Passage.
- **Formulas come out as the PDF holds them**, which is often not as they look: a book
  typeset without a table of its symbols gives letters and odd characters where the page
  shows a sum. Quote the words around a formula, or record that Evidence without a Passage.

## Searching

Shelf finds Passages by their words (SQLite's full-text index, ranked by BM25). A query is
words to look for, and any of them is enough: the Passages that hold more of them, and
rarer ones, come first. Words match without their case and their accents, and by their
English stem, so `eliminating` finds `elimination`. Nothing in a query is an operator.
A word is letters and numbers together, the small ones too, so `H₂O` and `m²` are words
and are found as they are written. **Text written without spaces between words, as
Chinese and Japanese are, cannot be searched by word**: a whole run of such text is one
word to the index.

Searching reads the index and never a Source: a book that was moved away is still found,
and a search is answered while another book is being indexed.

`study` does not search a plugin yet: `study evidence search` and the agent's
`evidence_search` tool are the next change (issue #82), and until then what Shelf indexed
is found by nothing but Shelf's own tests. Search by meaning, through an embedding model
you run, and reranking follow that (issue #84).

## Where the index is

In your state folder, outside the Study home, one file for each Topic:

```
~/.local/state/lamplight/knowledge-base-plugins/shelf/<collection>.sqlite
```

(`$XDG_STATE_HOME` instead of `~/.local/state` when it is set; `shelf` is the name you
registered it under, and the collection is the name `study` gives the Topic's Sources when
it speaks to a plugin: it begins with the Topic's id.) Beside it are `<collection>.lock`,
which says which Sources are being indexed now, and, while a Shelf runs, two files of
SQLite's, ending in `-wal` and `-shm`.

- It is that computer's state. It is not synced with the Topic, and it is rebuilt by
  indexing the Topic's Sources again. To have a Topic indexed from nothing, remove its
  files.
- Indexing a Source whose content is indexed already does nothing. Content that changed
  replaces what was indexed, in one step: a search finds the old text until the new text is
  all there, and never half of either. Stopping an indexing, or losing power in the middle
  of one, leaves what was indexed before, and the Source is indexed when you ask again.
- `study` starts Shelf for each thing it asks and stops it again, so several run at once:
  a `study status` in one terminal while `study source index` works in another. Each
  finds what another indexed, and says of a Source another is indexing that it is being
  indexed, and how far. Why a Source failed is kept in the collection, so it is still
  said the next day.
- Each file carries a format number. A file written by a newer Shelf is refused, with the
  reason, and left as it is.
- **A sandbox that `study source index` runs in must allow that folder**, since Shelf is
  then started inside the sandbox
  ([the contract](knowledge-base-contract.md#the-plugins-folder)). Or index from a shell of
  your own. Where Shelf may read the folder and not write in it, what is indexed is still
  said and still found, and indexing fails with that as its reason.

Shelf opens that folder once and follows no link out of it. It writes nowhere else: not in
the folder it is started in, and not in a temporary folder.

## What it costs

Asking what is indexed and searching are quick and small: Shelf starts, answers and is gone
in a few hundredths of a second. Indexing a PDF is the work:

- The PDF reader is compiled for your computer each time `study source index` has a PDF to
  index, which takes about three seconds.
- A book of a thousand pages then takes ten to fifteen seconds, and one with a drawing on
  every page a minute or more. Shelf holds a few hundred megabytes while it reads.
- One file may take 15 minutes and 64 MB of text, and the PDF reader 1 GB of memory. A
  file that needs more fails with the reason. A web page may be 16 MB, take 60 seconds, and
  redirect five times.

## Settings

This version has none. Shelf takes no key and no address of a model yet: those arrive with
embedding search, in a settings file of Shelf's own and never on its command line, which
shows in the process list.

**Behind a proxy**, Shelf needs to be told, to fetch web pages. `study` starts a plugin
with a small environment of its own making, in which no proxy variable arrives. Register a
script of your own, kept outside the Study home, that sets it:

```sh
#!/bin/sh
export HTTPS_PROXY=http://proxy.example:3128
exec study-shelf "$@"
```

## What is inside

Shelf is one program without dependencies to install. It is built from:

| Part | Is | Licence |
|---|---|---|
| The index | SQLite, through `modernc.org/sqlite`, a translation of SQLite to Go | Public domain; BSD-3-Clause |
| The PDF reader | PDFium, the reader Chrome uses, compiled to WebAssembly and packaged by `github.com/klippa-app/go-pdfium` | BSD-3-Clause and Apache-2.0; MIT |
| What runs the PDF reader | `github.com/tetratelabs/wazero`, a WebAssembly runtime | Apache-2.0 |
| The text of a web page | `codeberg.org/readeck/go-readability`, after the reader view of Firefox | MIT |

The PDF reader runs inside Shelf, shut in: it is given the one file to read, and can open
no other file, reach no network, and take no more memory than it is allowed. Why PDFium was
chosen over `pdftotext` is in
[ADR-0012](adr/0012-knowledge-base-plugins-speak-one-contract.md).
