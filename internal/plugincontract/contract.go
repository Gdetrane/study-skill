// Package plugincontract is the contract a Knowledge base plugin speaks,
// version 1 (ADR-0012): the six tools of the MCP server a plugin is, what
// each takes and gives back as JSON, the limits, and how a refusal is
// reported. docs/knowledge-base-contract.md says the same to someone writing
// a plugin in any language, and a test keeps the two in step.
//
// The package holds no client and no server. study's client
// (internal/pluginclient), the test plugin (internal/plugintest) and Shelf
// (internal/shelf) are all written on these types: a plugin and study agree
// because they read one description.
package plugincontract

import "time"

// Version is the contract version this package describes: the newest study
// knows. A plugin that reports a newer one is refused, like every newer
// format.
const Version = 1

// The six tools. A plugin is an MCP server that has all of them.
const (
	ToolContract = "contract"
	ToolIndex    = "index"
	ToolStatus   = "status"
	ToolCancel   = "cancel"
	ToolSearch   = "search"
	ToolPassage  = "passage"
)

// Tools lists the six tools, in the order the documentation describes them.
var Tools = []string{ToolContract, ToolIndex, ToolStatus, ToolCancel, ToolSearch, ToolPassage}

// DataFolderEnv is the environment variable in which study tells a plugin
// started by a command where its folder is: an absolute path, of a folder
// that exists, in the learner's state folder and outside the Study home. A
// plugin reached by URL is a service the learner runs, and keeps its index
// where it was told to.
const DataFolderEnv = "LAMPLIGHT_PLUGIN_DATA"

// DataFoldersName is the name of the folder, in Lamplight's folder of the
// learner's state folder, that holds the folder of each plugin started by a
// command, under the plugin's name. It is the folder a learner lets an
// agent's sandbox write in so that indexing can run there, so a plugin
// takes nothing from it, or from its own folder in it, for a setting.
const DataFoldersName = "knowledge-base-plugins"

// Kinds of Source a plugin can index.
const (
	SourceFile = "file"
	SourceURL  = "url"
)

// Ways a plugin can search.
const (
	SearchKeyword   = "keyword"
	SearchEmbedding = "embedding"
)

// LocationPages is the one kind of location version 1 defines: a first and a
// last page. Other kinds are added to the same field without a new contract
// version, so a kind this version does not know is no error.
const LocationPages = "pages"

// States of a Source in a plugin's index.
const (
	// StateIndexed: its Passages can be found, and Hash says of which
	// content.
	StateIndexed = "indexed"
	// StateIndexing: the plugin is working on it now.
	StateIndexing = "indexing"
	// StateFailed: the last indexing failed, and Reason says why.
	StateFailed = "failed"
	// StateOutdated: its Passages can be found, and Hash says of which
	// content, as with StateIndexed; but the plugin would index it otherwise
	// now, since its settings changed, and Reason says what indexing it
	// again would add.
	StateOutdated = "outdated"
)

// Codes of a tool error. Any other code, and none, is read as CodeInternal.
const (
	// CodeInvalidArgument: the input is not what the contract describes, or
	// holds a value this plugin does not take.
	CodeInvalidArgument = "invalid_argument"
	// CodeNotFound: the collection has no Passage with that id.
	CodeNotFound = "not_found"
	// CodeUnsupported: the plugin cannot do this at all, such as indexing a
	// kind of Source its contract does not list.
	CodeUnsupported = "unsupported"
	// CodeInternal: anything else that went wrong inside the plugin.
	CodeInternal = "internal"
)

// How long a plugin has when study only shows what is indexed, as study
// status does. study waits for no plugin there.
const (
	// GlanceTime is how long a plugin has to say what it has indexed, from
	// the moment study sets out to start it or to reach it: starting, the
	// answer of contract and every answer of status, all of them together.
	GlanceTime = 2 * time.Second
	// GlanceGrace is how long a command that was only asked what it has
	// indexed has to exit once its input is closed, and again once it is
	// signalled. It had nothing to finish.
	GlanceGrace = 250 * time.Millisecond
)

// EvidenceTime is how long a plugin has when study searches a Topic's
// Sources, or reads again the Passages a quote is checked against, from the
// moment study sets out to start it or to reach it: starting, the answers of
// contract and status, and the search or every passage, all of them
// together. An agent's tool call waits for it, and the learner for the
// agent, so it is less than the minute after which many MCP clients give a
// tool call up, and more than the half minute a plugin has to start.
const EvidenceTime = 45 * time.Second

// Limits. study refuses what goes over them, on the way out and on the way
// in, so a plugin never has to hold more and a broken one cannot make study
// hold more.
const (
	// MaxMessageBytes is how large one MCP message from a plugin may be.
	MaxMessageBytes = 16 << 20
	// MaxMessageValues is how many values one MCP message from a plugin may
	// hold: its lists and objects, and what they hold. A status of
	// MaxSources Sources holds about a quarter of it.
	MaxMessageValues = 250000
	// MaxPassageBytes is how long a Passage's text may be, in bytes of
	// UTF-8.
	MaxPassageBytes = 32 << 10
	// MaxSearchLimit is the most Passages one search asks for.
	MaxSearchLimit = 50
	// MaxNoticeBytes is how much of a search's notice study keeps, in bytes
	// of UTF-8. More is cut off, never refused, as with a reason.
	MaxNoticeBytes = 2000
	// MaxQueryBytes is how long a query may be, in bytes of UTF-8.
	MaxQueryBytes = 4096
	// MaxSources is how many Sources a collection's status may list, and how
	// many a search may be limited to.
	MaxSources = 10000
	// MaxNameBytes is how long a collection's name may be.
	MaxNameBytes = 64
	// MaxIDBytes is how long the id of a Source or of a Passage may be.
	MaxIDBytes = 128
	// MaxTitleRunes is how long a Source's title may be, in characters.
	MaxTitleRunes = 200
	// MaxPathBytes is how long a file's path or a URL may be.
	MaxPathBytes = 4096
	// MaxKinds is how many kinds each list of the contract tool's answer may
	// hold, and MaxKindBytes how long a kind's name may be.
	MaxKinds     = 16
	MaxKindBytes = 32
	// MaxTextBytes is how much of a failure's reason and of an error's
	// message study keeps. More is cut off, never refused.
	MaxTextBytes = 2000
)

// Contract is what the contract tool gives back: the version of the contract
// the plugin speaks, and what it can do.
type Contract struct {
	// Version is the contract version: 1.
	Version int `json:"version"`
	// Sources are the kinds of Source the plugin indexes: "file", "url".
	Sources []string `json:"sources"`
	// Search is how it searches: "keyword", "embedding".
	Search []string `json:"search"`
	// Locations are the kinds of location its Passages can carry: "pages".
	// It is empty for a plugin that never knows where a Passage is.
	Locations []string `json:"locations"`
}

// Source is one Source of a Topic, as the index tool is given it.
type Source struct {
	// ID is the Source's id, which study chose.
	ID string `json:"id"`
	// Title is the Source's title, for the plugin's own messages.
	Title string `json:"title"`
	// Kind is "file" or "url".
	Kind string `json:"kind"`
	// Path is a file's absolute path on the computer study runs on.
	Path string `json:"path,omitempty"`
	// Hash is the hash of the file's content as study read it:
	// "sha256:" and 64 hexadecimal digits.
	Hash string `json:"hash,omitempty"`
	// URL is a web page's http or https address.
	URL string `json:"url,omitempty"`
	// Refetch asks for a web page that is indexed already to be fetched
	// again.
	Refetch bool `json:"refetch,omitempty"`
}

// SourceStatus is what a plugin says of one Source in a collection.
type SourceStatus struct {
	// ID is the Source's id.
	ID string `json:"id"`
	// State is "indexed", "indexing", "failed" or "outdated".
	State string `json:"state"`
	// Progress is how much of the indexing is done, from 0 to 1, when the
	// plugin can tell.
	Progress *float64 `json:"progress,omitempty"`
	// Reason says why the indexing failed, or what indexing a Source that
	// is outdated again would add.
	Reason string `json:"reason,omitempty"`
	// Hash is the hash of the content whose Passages can be found now:
	// "sha256:" and 64 hexadecimal digits of the bytes the plugin read.
	Hash string `json:"hash,omitempty"`
}

// Location says where in its Source a Passage is. Kind decides which other
// fields there are.
type Location struct {
	// Kind is the kind of location: "pages".
	Kind string `json:"kind"`
	// First and Last are the first and the last page the Passage is on,
	// counted from 1 from the start of the file.
	First int `json:"first,omitempty"`
	Last  int `json:"last,omitempty"`
}

// Passage is a stretch of a Source's own text, as the plugin extracted it:
// never a summary and never a generated answer.
type Passage struct {
	// ID names the Passage within its collection.
	ID string `json:"id"`
	// Source is the id of the Source the text is from.
	Source string `json:"source"`
	// Text is the Source's own text.
	Text string `json:"text"`
	// Location is where the text is in the Source, when the plugin knows.
	Location *Location `json:"location,omitempty"`
}

// ScoredPassage is a Passage a search found, with its score for that query.
// The score belongs to the search, so the passage tool gives none.
type ScoredPassage struct {
	Passage
	// Score says how well the Passage answers the query: higher is better.
	Score float64 `json:"score"`
}

// IndexInput is what the index tool takes.
type IndexInput struct {
	// Collection is the name study chose for the Topic.
	Collection string `json:"collection"`
	// Source is the Source to index.
	Source Source `json:"source"`
}

// IndexResult is what the index tool gives back, at once: what the plugin
// says of the Source now.
type IndexResult struct {
	Source SourceStatus `json:"source"`
}

// StatusInput is what the status tool takes.
type StatusInput struct {
	Collection string `json:"collection"`
}

// StatusResult is what the status tool gives back: every Source the plugin
// has something to say about.
type StatusResult struct {
	Sources []SourceStatus `json:"sources"`
}

// CancelInput is what the cancel tool takes.
type CancelInput struct {
	Collection string `json:"collection"`
	// Source is the id of the Source whose indexing is to stop.
	Source string `json:"source"`
}

// CancelResult is what the cancel tool gives back, once the work has
// stopped.
type CancelResult struct {
	// Stopped says that the Source was being indexed and no longer is.
	Stopped bool `json:"stopped"`
}

// SearchInput is what the search tool takes.
type SearchInput struct {
	Collection string `json:"collection"`
	// Query is what to look for.
	Query string `json:"query"`
	// Limit is the most Passages to give back.
	Limit int `json:"limit"`
	// Sources limits the search to the Sources with these ids. Left out, the
	// whole collection is searched.
	Sources []string `json:"sources,omitempty"`
}

// SearchResult is what the search tool gives back: the Passages found, best
// first.
type SearchResult struct {
	Passages []ScoredPassage `json:"passages"`
	// Notice says, in a sentence or two of the plugin's own for the learner,
	// what this search did less than the plugin was set to, and why: found
	// by keyword alone because an embedding endpoint did not answer, say. It
	// is left out when the search did all it was set to. It is text a
	// plugin wrote, and study treats it as it treats a reason: put on one
	// line, cleaned, and cut to MaxNoticeBytes.
	Notice string `json:"notice,omitempty"`
}

// PassageInput is what the passage tool takes.
type PassageInput struct {
	Collection string `json:"collection"`
	// ID is the id of the Passage, as a search gave it.
	ID string `json:"id"`
}

// PassageResult is what the passage tool gives back.
type PassageResult struct {
	Passage Passage `json:"passage"`
}

// ErrorResult is the result of a tool call the plugin refused or failed:
// the JSON object of a result whose isError is true.
type ErrorResult struct {
	Error ErrorBody `json:"error"`
}

// ErrorBody says what went wrong.
type ErrorBody struct {
	// Code is one of the codes above.
	Code string `json:"code"`
	// Message says what went wrong, for the learner.
	Message string `json:"message"`
}
