package core

import (
	"context"
	"crypto/sha256"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/pluginclient"
	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
)

// What a Topic's Knowledge base plugin has indexed (ADR-0012).
//
// A Topic names a plugin, and the plugin indexes the Topic's Sources.
// Whether a Source is indexed is the plugin's to say, on this computer: it
// records no Event and is in no file of the Topic, so study asks each time.
//
// Three things ask. askPlugins is for status and for a Topic's Sources: it
// asks the plugin's status tool and nothing else, has a couple of seconds
// for all of it, and shows the Sources' state as not known when that is
// missed, so that nothing waits for a plugin. An MCP tool reaches it. A
// search, and Evidence recorded with its Passage, ask it too
// (topicPlugin.indexed, in evidence_search.go), with the time a search has,
// and decide what is indexed and what to do about the rest by the same
// rule, which is indexAsk's. An MCP tool reaches that as well.
// IndexSources is study source index, the one thing that asks a plugin to
// index. It is CLI-only, like study check (ADR-0009): a plugin that indexes
// reads the files it is handed, the MCP server runs outside the agent's
// sandbox, and a tool that indexed would have it read any file the agent
// named as a Source.

// Index states of a Source: what the Topic's Knowledge base plugin says of
// it on this computer.
const (
	// IndexIndexed: the plugin holds the Source's content, and a search
	// finds it.
	IndexIndexed = "indexed"
	// IndexIndexing: the plugin is indexing it now.
	IndexIndexing = "indexing"
	// IndexFailed: the plugin's last indexing of it failed.
	IndexFailed = "failed"
	// IndexOutdated: the plugin holds the Source's content, and a search
	// finds it, but the plugin would index it otherwise now, since its
	// settings changed: the reason says what indexing it again would add,
	// and study source index asks for it again.
	IndexOutdated = "outdated"
	// IndexNotIndexed: the plugin does not hold the Source's content.
	IndexNotIndexed = "not_indexed"
	// IndexUnknown: the plugin could not be asked, or did not say in time.
	IndexUnknown = "unknown"
)

const (
	// PluginGlanceTimeout is how long status and a Topic's Sources give a
	// Topic's Knowledge base plugin to say what it has indexed: reading the
	// registry, starting the plugin or reaching it, and its answers, all of
	// it together. A plugin has thirty seconds to start when it is asked to
	// index (pluginclient.DefaultStartTimeout). Nothing that shows where the
	// learner is may wait that long, so here a plugin that takes longer is
	// stopped, and its Sources are shown as not known.
	PluginGlanceTimeout = plugincontract.GlanceTime
	// PluginGlanceGrace is how long a command that was only asked what it
	// has indexed has to exit once its input is closed, and again once it
	// is signalled. It had nothing to finish.
	PluginGlanceGrace = plugincontract.GlanceGrace
	// DefaultIndexPoll is how often study source index asks a plugin how
	// far it is with a Source, once the first answers came.
	DefaultIndexPoll = time.Second
	// DefaultIndexCancelTimeout is how long a plugin has to stop indexing a
	// Source once study source index was stopped: its cancel tool answers
	// when the work has stopped.
	DefaultIndexCancelTimeout = 10 * time.Second
	// maxIndexTextBytes is how much of a plugin's reason, and of what is
	// wrong with a plugin, status and a Topic's Sources repeat. study source
	// index gives a reason whole.
	maxIndexTextBytes = 300
)

// SourceIndex is what a Topic's Knowledge base plugin says of one Source on
// this computer.
type SourceIndex struct {
	// State is indexed, indexing, failed, not_indexed or unknown.
	State string `json:"state"`
	// Progress is how much of the indexing is done, from 0 to 1, with
	// indexing and when the plugin can tell.
	Progress *float64 `json:"progress,omitempty"`
	// Reason says why, when there is something to say: with failed, in the
	// plugin's words, which study cleaned; with not_indexed, when the
	// plugin does not index Sources of this kind, or holds other content
	// for the Source than the Topic records.
	Reason string `json:"reason,omitempty"`
	// Retry is the command that has a failed Source tried again, when
	// trying again can come to anything: the plugin indexes Sources of its
	// kind, and a file is on this computer, is the one that was added and
	// can be read. A failure is no next action, so this is beside the
	// Source and not the Topic's action.
	Retry string `json:"retry,omitempty"`
}

// IndexedSource is one Source of a Topic with what the Topic's Knowledge
// base plugin says of it.
type IndexedSource struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	SourceIndex
}

// Indexing is what a Topic's Knowledge base plugin says, on this computer,
// of the Topic's Sources. It is asked each time, and recorded nowhere.
type Indexing struct {
	// Plugin is the plugin's name, as the Topic records it.
	Plugin string `json:"plugin"`
	// Problem says why the plugin could not say what it has indexed: the
	// Topic names no plugin this computer has, the plugin does not start or
	// does not speak the contract, or it did not answer in time. The Topic
	// then works as one without a Knowledge base, and every Source's state
	// is unknown.
	Problem string `json:"problem,omitempty"`
	// Action is the one command to run next: what puts Problem right, or
	// study source index when a Source is not indexed and can be. It is
	// empty when there is nothing to do, and when all that is not indexed
	// has failed: a failure is shown with its reason, and tried again by
	// whoever runs study source index for it. For a Source whose file is
	// not on this computer, or is another now, indexing is no action
	// either: status then names study source list, which says what to do
	// about each such file.
	Action string `json:"action,omitempty"`
	// Sources are the Topic's Sources, in the order they were added, each
	// with its state. A listing of a Topic's Sources gives each state with
	// its Source instead.
	Sources []IndexedSource `json:"sources,omitempty"`
}

// collectionSuffixBytes is how much of a hash a collection's name ends
// with: 160 bits, which are 32 characters.
const collectionSuffixBytes = 20

// collectionName is the name study gives a Knowledge base plugin for the
// collection that holds a Topic's Sources.
//
// A plugin keeps its index in a folder that is one for the plugin on this
// computer, not one for each Study home, and a plugin reached by URL may
// serve several computers. So the Topic's id alone would not do: two Study
// homes that each hold a Topic linear-algebra would share a collection, and
// a search in one would find the other's books. The name therefore ends
// with a hash of where the Study home really is and of the Topic's id.
// The path is one the agent does not choose: it writes inside the Study
// home, and cannot make another folder be found at the path of this one.
//
// The name is the Topic's id, cut to fit, a hyphen, and 32 characters of
// that hash: always lowercase letters, digits and single hyphens, 64
// characters at most, as the contract wants a collection's name. Two names
// are the same only when their hashes are, since every name ends with one:
// so no two Topics share a name, whatever their ids were cut to, and none
// shares one with a Topic of another Study home. The same Study home and
// Topic give the same name on every run. A Study home that is moved gives
// another: its Sources are then indexed again.
func collectionName(realHome, topicID string) string {
	sum := sha256.Sum256([]byte("lamplight collection\x00" + realHome + "\x00" + topicID))
	suffix := strings.ToLower(idEncoding.EncodeToString(sum[:collectionSuffixBytes]))
	// A folder of the Study home is a Topic whatever it is called, so its
	// name need not be one a Topic can be given.
	prefix := topicID
	if validateTopicID(prefix) != nil {
		prefix = "topic"
	}
	if most := plugincontract.MaxNameBytes - len(suffix) - 1; len(prefix) > most {
		prefix = strings.TrimRight(prefix[:most], "-")
	}
	return prefix + "-" + suffix
}

// realHome is where the Study home really is: its path with every symbolic
// link followed.
func (c *Core) realHome() (string, error) {
	home, err := filepath.EvalSymlinks(c.home)
	if err != nil {
		return "", &Error{Code: CodeFailedPrecondition, Err: err, Message: "study cannot tell where the Study home, " + c.home +
			", really is (" + pathReasonOf(err) + "), so it cannot name the Topic to a Knowledge base plugin"}
	}
	return home, nil
}

// pathReasonOf is an operating system error without the path it names.
func pathReasonOf(err error) string {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err.Error()
	}
	return err.Error()
}

// kindsOfSource names the kinds of Source in a sentence.
var kindsOfSource = map[string]string{SourceFile: "files", SourceURL: "web pages"}

// indexOf is the index state of a Source, given what its Topic's plugin
// said of it, which is nothing when the plugin did not list it, and whether
// study source index is the next thing to do for it (wanted).
//
// It is for a Source the plugin has not tried, or holds other content of. A
// Source whose indexing failed is shown as failed, with the plugin's reason,
// and is no next action: the plugin tried, and trying again helps only once
// what the reason names is put right. A scan without text would otherwise
// have study source index as the one thing to do for ever, and every Session
// would run it. Whoever runs study source index has it tried again all the
// same.
func indexOf(src Source, said *plugincontract.SourceStatus, contract plugincontract.Contract, plugin string) (index SourceIndex, wanted bool) {
	can := slices.Contains(contract.Sources, src.Kind)
	switch {
	case said == nil && !can:
		return SourceIndex{State: IndexNotIndexed, Reason: "Knowledge base plugin " + plugin + " does not index " +
			kindsOfSource[src.Kind]}, false
	case said == nil:
		return SourceIndex{State: IndexNotIndexed}, true
	case said.State == plugincontract.StateIndexing:
		return SourceIndex{State: IndexIndexing, Progress: said.Progress}, false
	case said.State == plugincontract.StateFailed:
		return SourceIndex{State: IndexFailed, Reason: said.Reason}, false
	case src.Kind == SourceFile && said.Hash != src.Hash:
		// A file Source is its content: what the plugin read was another
		// file's, or this one's after it changed.
		return SourceIndex{State: IndexNotIndexed, Reason: "Knowledge base plugin " + plugin + " holds other content for it " +
			"than the Source's: the file was another when the plugin read it"}, can
	case said.State == plugincontract.StateOutdated:
		// Found as it is, and to be indexed again: what the plugin is
		// asked for like a Source it has not indexed.
		return SourceIndex{State: IndexOutdated, Reason: said.Reason}, can
	}
	return SourceIndex{State: IndexIndexed}, false
}

// shorten cuts text to at most max bytes, at the end of a character, and
// says so with an ellipsis, which is within the max.
func shorten(text string, max int) string {
	const ellipsis = "…"
	if len(text) <= max {
		return text
	}
	return plugincontract.Cut(text, max-len(ellipsis)) + ellipsis
}

// indexAsk is one Topic of which status, or a listing of its Sources, asks
// what its Knowledge base plugin has indexed.
type indexAsk struct {
	topic   string
	kb      *KnowledgeBase
	sources []Source
	// fileState, when it is not nil, says of a file Source whether its file
	// is on this computer and is the one that was added: SourceOK,
	// SourceMissing, SourceChanged or sourceUnreadable, or nothing when it
	// could not tell in time. It is asked only about a Source the plugin has
	// not indexed.
	fileState func(ctx context.Context, src Source) string
	// held says that a Source is not indexed and cannot be handed to the
	// plugin as things are: its file is missing, or is another now.
	held bool
	// indexing is the answer: every Source's state is unknown until the
	// plugin said otherwise.
	indexing *Indexing
}

func newIndexAsk(topic string, kb *KnowledgeBase, sources []Source) *indexAsk {
	a := &indexAsk{topic: topic, kb: kb, sources: sources, indexing: &Indexing{Plugin: kb.Plugin}}
	for _, src := range sources {
		a.indexing.Sources = append(a.indexing.Sources, IndexedSource{ID: src.ID, Title: src.Title,
			SourceIndex: SourceIndex{State: IndexUnknown}})
	}
	return a
}

// unanswered records why the plugin did not say what it has indexed, and
// the command that puts it right.
func (a *indexAsk) unanswered(problem, action string) {
	a.indexing.Problem, a.indexing.Action = shorten(problem, 2*maxIndexTextBytes), action
}

// Why a file Source that is not indexed is not one to index as things are.
const (
	reasonFileMissing    = "its file is not on this computer"
	reasonFileChanged    = "the file here is no longer the one added"
	reasonFileUnreadable = "its file cannot be read"
)

// sourceUnreadable is what an indexAsk's fileState says of a file that is
// where it was found, and cannot be opened to read.
const sourceUnreadable = "unreadable"

// fileStateAt is the state of a file Source for whoever would hand it to a
// plugin, given where it was found and in which state: a file that is there
// and cannot be opened is none to hand over.
func fileStateAt(path, state string) string {
	if state != SourceOK {
		return state
	}
	f, err := os.Open(path)
	if err != nil {
		return sourceUnreadable
	}
	_ = f.Close()
	return state
}

// fileStateIn returns what an indexAsk asks to learn whether a file Source's
// file is on this computer and is the one that was added: a Source the
// plugin has not indexed is looked for as a listing of the Sources looks for
// it, within the time the asking has. Where it is found is not written
// down: whoever asks changes nothing.
func (c *Core) fileStateIn(home *os.Root, topicID string) func(ctx context.Context, src Source) string {
	var l *locator
	return func(ctx context.Context, src Source) string {
		if l == nil {
			l = c.newLocator(ctx, home, topicID)
		}
		path, state, err := l.locate(src)
		if err != nil {
			return ""
		}
		return fileStateAt(path, state)
	}
}

// answered records what the plugin said of the Topic's Sources.
//
// study source index is the next action only for a Source it would hand to
// the plugin. A file that is missing, or has other content now, is skipped
// by it (fileToHandOver), so such a Source says why it is not indexed
// instead, and what puts that right is the file's to say.
func (a *indexAsk) answered(ctx context.Context, plugin string, contract plugincontract.Contract, said []plugincontract.SourceStatus) {
	byID := map[string]*plugincontract.SourceStatus{}
	for i := range said {
		byID[said[i].ID] = &said[i]
	}
	toIndex := false
	for i, src := range a.sources {
		index, wanted := indexOf(src, byID[src.ID], contract, plugin)
		// Whether study source index would hand the Source over, and so
		// whether trying a failed one again can come to anything.
		handed := slices.Contains(contract.Sources, src.Kind) && validateTopicID(a.topic) == nil
		if src.Kind == SourceFile && a.fileState != nil && (wanted || index.State == IndexFailed) {
			why := map[string]string{SourceMissing: reasonFileMissing, SourceChanged: reasonFileChanged,
				sourceUnreadable: reasonFileUnreadable}[a.fileState(ctx, src)]
			if why != "" {
				// A Source that failed keeps the plugin's reason, and so does
				// one that is outdated, which is still found: the file says
				// the rest in a listing.
				if a.held, handed = true, false; wanted {
					if wanted = false; index.State != IndexOutdated {
						index.Reason = why
					}
				}
			}
		}
		if index.State == IndexFailed && handed {
			index.Retry = "study source index " + a.topic + " " + src.ID
		}
		index.Reason = shorten(index.Reason, maxIndexTextBytes)
		a.indexing.Sources[i].SourceIndex = index
		toIndex = toIndex || wanted
	}
	// A folder whose name is no Topic's id is a Topic all the same, and one
	// no command can name.
	if toIndex && validateTopicID(a.topic) == nil {
		a.indexing.Action = "study source index " + a.topic
	}
}

// unusable says why this computer cannot use the plugin registered under
// name, given a reading of the registry and what went wrong with it, and
// the command that puts it right. It returns the plugin and no problem when
// the registry has it and study uses it.
func unusable(list PluginList, readErr error, name string) (plugin Plugin, problem, action string) {
	if readErr != nil {
		return Plugin{}, readErr.Error(), "study knowledge-base list"
	}
	plugin, err := registered(list, name)
	switch {
	case err != nil:
		return Plugin{}, "no Knowledge base plugin named " + name + " is registered on this computer",
			"study knowledge-base add " + name + " -- <command>"
	case plugin.Problem != "":
		return Plugin{}, "Knowledge base plugin " + name + " is registered on this computer, and study does not use it: " +
			plugin.Problem, "study knowledge-base list"
	}
	return plugin, "", ""
}

// askPlugins says, for each Topic that names a Knowledge base plugin, what
// the plugin has indexed of the Topic's Sources: it fills in the indexing
// of every ask.
//
// It never waits for a plugin. Reading the registry, starting each plugin
// and its answers share one deadline (PluginOptions.GlanceTimeout), plugins
// are asked side by side, and a plugin that misses the deadline is stopped:
// the Sources of its Topics are then not known, with the command that says
// what is wrong. So are those of a Topic whose plugin is not registered on
// this computer, does not start, or does not speak the contract. A Topic
// without Sources has nothing to ask, and its plugin is not started.
//
// A plugin is asked its status tool and nothing else: an MCP tool reaches
// this, and no MCP tool may have a plugin index. Nothing is written but a
// command plugin's own folder, which is made when it is not there. It fails
// only when the command was stopped.
func (c *Core) askPlugins(ctx context.Context, asks []*indexAsk) error {
	limit := c.plugins.GlanceTimeout
	if limit <= 0 {
		limit = PluginGlanceTimeout
	}
	byPlugin := map[string][]*indexAsk{}
	var names []string
	for _, a := range asks {
		name, ok := pluginOf(a.kb)
		if !ok {
			a.unanswered("topic.toml of "+a.topic+" records a Knowledge base plugin without a name a plugin can have",
				"study topic update "+a.topic+" --knowledge-base <name>")
			continue
		}
		if byPlugin[name] == nil {
			names = append(names, name)
		}
		byPlugin[name] = append(byPlugin[name], a)
	}
	if len(names) == 0 {
		// Nothing to ask anyone: nothing here was stopped, either.
		return nil
	}
	list, began, readErr := c.registryInTime(ctx, limit)
	if ctx.Err() != nil {
		return glanceStopped(ctx)
	}
	deadline := began.Add(limit)
	var wg sync.WaitGroup
	for _, name := range names {
		group := byPlugin[name]
		plugin, problem, action := unusable(list, readErr, name)
		switch {
		case problem != "":
			for _, a := range group {
				a.unanswered(problem, action)
			}
		case !slices.ContainsFunc(group, func(a *indexAsk) bool { return len(a.sources) > 0 }):
			// Nothing to ask it about.
		default:
			wg.Add(1)
			go func() {
				defer wg.Done()
				c.askPlugin(ctx, plugin, began, deadline, group)
			}()
		}
	}
	wg.Wait()
	if ctx.Err() != nil {
		return glanceStopped(ctx)
	}
	return nil
}

// glanceStopped is the error for a command that was stopped while it asked
// what is indexed.
func glanceStopped(ctx context.Context) error {
	if !stopped(ctx) {
		return ctx.Err()
	}
	return &Error{Code: CodeCanceled, Err: ctx.Err(), Message: "the command was stopped before every Knowledge base plugin " +
		"had said what it has indexed"}
}

// askPlugin starts one plugin, asks it what it has indexed of each Topic
// that names it, and stops it again, all before deadline.
func (c *Core) askPlugin(ctx context.Context, plugin Plugin, began, deadline time.Time, asks []*indexAsk) {
	// answered says that the plugin said what it has indexed of a Topic.
	answered := false
	// failed says why the plugin did not answer about the Topics from
	// asks[from] on. One that ran out of time is said so in the same words
	// wherever it was when the time ran out.
	//
	// The Topics that name a plugin share its time, and are asked one after
	// the other. A plugin that answered about one works: for the Topics the
	// time did not reach, the command that says what is wrong with a plugin
	// would say that nothing is, so they are given the one that asks about
	// that Topic alone, with all of the time.
	late := false
	failed := func(from int, err error) {
		late = ctx.Err() == nil && !time.Now().Before(deadline)
		for _, a := range asks[from:] {
			switch {
			case len(a.sources) == 0:
			case late && answered:
				a.unanswered("Knowledge base plugin "+plugin.Name+" had "+deadline.Sub(began).String()+" to say what it has "+
					"indexed of every Topic that names it, and they were over before it said so of this one",
					"study source list "+a.topic)
			case late:
				a.unanswered("Knowledge base plugin "+plugin.Name+" did not say what it has indexed within "+
					deadline.Sub(began).String()+", and study did not wait for it", "study knowledge-base check "+plugin.Name)
			default:
				a.unanswered(err.Error(), "study knowledge-base check "+plugin.Name)
			}
		}
	}
	home, err := c.realHome()
	if err != nil {
		failed(0, err)
		return
	}
	conn, err := c.startPlugin(ctx, plugin, began, pluginUse{within: deadline.Sub(began), grace: PluginGlanceGrace})
	if err != nil {
		failed(0, err)
		return
	}
	defer func() {
		if late {
			// It had its time: it is not given more to leave.
			conn.GiveUp()
		}
		conn.Close()
	}()
	for i, a := range asks {
		if len(a.sources) == 0 {
			continue
		}
		// The plugin had until the deadline to start, and has what is left
		// of that to answer.
		asking, cancel := context.WithDeadline(ctx, deadline)
		said, err := conn.Status(asking, collectionName(home, a.topic))
		if err != nil {
			cancel()
			failed(i, err)
			return
		}
		a.answered(asking, plugin.Name, conn.Contract(), said)
		cancel()
		answered = true
	}
}

// pluginNote says, of the plugin a Topic was just made to name, what keeps
// this computer from using it and how to put that right, or nothing. It
// looks in the registry, and starts no plugin.
func (c *Core) pluginNote(ctx context.Context, topicID, name string) string {
	limit := c.plugins.GlanceTimeout
	if limit <= 0 {
		limit = PluginGlanceTimeout
	}
	list, _, readErr := c.registryInTime(ctx, limit)
	if ctx.Err() != nil {
		return ""
	}
	_, problem, action := unusable(list, readErr, name)
	if problem == "" {
		return ""
	}
	return shorten(problem, 2*maxIndexTextBytes) + ". Until that is put right, " + topicID + " works on this computer as a Topic " +
		"without a Knowledge base. Run: " + action
}

// IndexSpec says what study source index is asked to do.
type IndexSpec struct {
	Topic string
	// Sources are the ids of the Sources to index. Empty is every Source
	// of the Topic.
	Sources []string
	// Again has Sources that are indexed already asked for again: a web
	// page is then fetched again. A file whose content is indexed is left
	// as it is by the plugin, as the contract says.
	Again bool
	// DryRun starts the plugin, asks what it has indexed, and returns what
	// would be asked of it, without asking it to index anything.
	DryRun bool
	// Progress, if set, is told when the indexing of each Source starts and
	// ends, and every ProgressEvery while it goes on. It is never called
	// concurrently.
	Progress func(IndexProgress)
	// ProgressEvery is how often a Source that is being indexed is
	// reported. Zero means DefaultProgressEvery.
	ProgressEvery time.Duration
}

// IndexProgress reports one Source of a running study source index.
type IndexProgress struct {
	Source string
	Title  string
	// Number counts the Sources the plugin is asked to index from 1, out of
	// Total.
	Number, Total int
	// State is ProgressStarted, ProgressRunning, ProgressFinished or
	// ProgressStopping.
	State string
	// Progress is how much of the Source is indexed, from 0 to 1, when the
	// plugin can tell.
	Progress *float64
	// Elapsed is how long the plugin has been indexing the Source.
	Elapsed time.Duration
	// Outcome is the Source's index state once the indexing ended.
	Outcome string
}

// ProgressStopping reports a Source of study source index that the plugin
// was indexing when the command was stopped: the plugin is being told to
// stop, and has a few seconds to say that it did. The command says so at
// once, since nothing else is printed until the plugin answered.
const ProgressStopping = "stopping"

// IndexOutcome is one Source after study source index, or in a dry run
// before it.
type IndexOutcome struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Path is where a file Source is on this computer, when it was found,
	// and URL a web page's address: what a plugin is handed, and reads or
	// fetches. It is shown so that whoever runs the command sees which
	// files it has a plugin read.
	Path string `json:"path,omitempty"`
	URL  string `json:"url,omitempty"`
	SourceIndex
	// Asked says that the plugin was asked to index the Source, or in a dry
	// run that it would be.
	Asked bool `json:"asked,omitempty"`
	// Skipped says why the plugin was not asked to index a Source that is
	// not indexed: its file is not on this computer or is no longer the one
	// added, or the plugin does not index Sources of its kind.
	Skipped string `json:"skipped,omitempty"`
}

// IndexResult is the result of IndexSources.
type IndexResult struct {
	Topic string `json:"topic"`
	// Plugin is the name of the Topic's Knowledge base plugin.
	Plugin string `json:"plugin"`
	// Collection is the name study gives the plugin for this Topic's
	// Sources: the Topic's id and a hash of where the Study home is.
	Collection string `json:"collection"`
	// Sources are the Sources that were named, or all of the Topic's, in
	// the order they were added, each as the plugin has it now.
	Sources []IndexOutcome `json:"sources"`
	DryRun  bool           `json:"dry_run,omitempty"`
}

// IndexSources asks a Topic's Knowledge base plugin to index the Topic's
// Sources that are not indexed, one after the other, and follows each until
// the plugin says it is indexed or has failed. It is study source index.
//
// It is the one thing that asks a plugin to index, and it is for the
// command line alone: the agent runs it in its own shell, where its sandbox
// applies to what a command plugin reads and fetches, or the learner runs
// it. No MCP tool reaches it (ADR-0012).
//
// A file goes to the plugin with its real path on this computer and its
// content hash, and only when the file there is the one the Topic records: a
// file that is missing, or that has other content now, is skipped, with why
// (fileToHandOver). A URL goes as it is. What goes wrong with a Source is no
// error of the command: the Source's state is failed, with the plugin's
// reason. It records no Event and changes nothing in the Topic: what is
// indexed is the plugin's to say on this computer.
//
// It fails with failed_precondition when the Topic names no plugin, when
// the plugin is not registered on this computer, cannot be started, does not
// speak the contract or stops answering, and when its folder cannot be
// written in, as inside a sandbox that allows the Study home alone; with
// newer_format when the plugin speaks a newer contract; and with not_found
// for a Source the Topic does not have.
//
// When it is stopped, it tells the plugin to stop indexing the Source it
// was indexing, with a context of its own since the command's is cancelled
// by then, and reports canceled. What was indexed before stays, and running
// it again carries on. A command plugin is stopped before it returns, so
// nothing indexes behind a command that has exited.
func (c *Core) IndexSources(ctx context.Context, spec IndexSpec) (IndexResult, error) {
	if err := checkTopicID(spec.Topic); err != nil {
		return IndexResult{}, err
	}
	for _, id := range spec.Sources {
		if !sourceIDPattern.MatchString(id) {
			return IndexResult{}, invalidf("%q is not a Source id: study source list %s shows them", id, spec.Topic)
		}
	}
	if err := ctx.Err(); err != nil {
		return IndexResult{}, err
	}
	home, topic, err := c.openTopicFolder(spec.Topic)
	if err != nil {
		return IndexResult{}, err
	}
	defer home.Close()
	defer topic.Close()
	kb, err := topicKnowledgeBase(&topicView{root: topic}, spec.Topic)
	if err != nil {
		return IndexResult{}, err
	}
	name, ok := pluginOf(kb)
	if !ok {
		return IndexResult{}, &Error{Code: CodeFailedPrecondition, Message: "Topic " + spec.Topic + " names no Knowledge base " +
			"plugin, so nothing indexes its Sources: name one with study topic update " + spec.Topic +
			" --knowledge-base <name>. study knowledge-base list shows the plugins this computer has"}
	}
	h, err := readHistory(topic, spec.Topic)
	if err != nil {
		return IndexResult{}, err
	}
	k := replayHistory(h).knowledge()
	for _, id := range spec.Sources {
		if _, known := k.sources[id]; !known {
			return IndexResult{}, unknownSource(spec.Topic, id)
		}
	}
	// The Sources in the order they were added, each once, with where its
	// file is on this computer.
	var sources []SourceStatus
	l := c.newLocator(ctx, home, spec.Topic)
	for _, id := range k.order {
		if len(spec.Sources) > 0 && !slices.Contains(spec.Sources, id) {
			continue
		}
		found := SourceStatus{Source: *k.sources[id]}
		if found.Kind == SourceFile {
			if found.Path, found.State, err = l.locate(found.Source); err != nil {
				return IndexResult{}, err
			}
		}
		sources = append(sources, found)
	}
	l.save()
	realHome, err := c.realHome()
	if err != nil {
		return IndexResult{}, err
	}

	plugin, began, err := c.pluginInTime(ctx, name)
	if CodeOf(err) == CodeNotFound {
		return IndexResult{}, &Error{Code: CodeFailedPrecondition, Err: err, Message: "Topic " + spec.Topic + " names the " +
			"Knowledge base plugin " + name + ", which is not registered on this computer: register it with study " +
			"knowledge-base add " + name + " -- <command>, or name another with study topic update " + spec.Topic +
			" --knowledge-base <name>"}
	}
	if err != nil {
		return IndexResult{}, err
	}
	conn, err := c.startPlugin(ctx, plugin, began, pluginUse{indexes: true})
	if err != nil {
		return IndexResult{}, err
	}
	defer conn.Close()

	run := &indexRun{c: c, conn: conn, plugin: plugin, topic: spec.Topic, collection: collectionName(realHome, spec.Topic)}
	result, err := run.indexSources(ctx, spec, sources)
	if err != nil {
		return IndexResult{}, run.ended(ctx, err)
	}
	return result, nil
}

// indexRun is one run of study source index, once its plugin has started.
type indexRun struct {
	c          *Core
	conn       *pluginclient.Connection
	plugin     Plugin
	topic      string
	collection string
	// asked says that the run got as far as asking the plugin what it has
	// indexed: a run that ends before that left nothing to tell of.
	asked bool
	// underWay is the id of the Source this run asked the plugin to index
	// and has not seen come to an end: what the plugin is told to stop
	// indexing when the run ends early.
	underWay string
	// heard says that the plugin answered the request to index underWay. A
	// request that was stopped on its way may reach the plugin all the same,
	// after it was told to stop.
	heard bool
	// stopping, when it is not nil, reports that the plugin is being told
	// to stop indexing underWay.
	stopping func()
}

// beforeIndexAsked, when a test sets it, is called with a Source's id after
// study decided to ask the plugin to index it, having read its file, and
// before it asks: where a file can change between the two.
var beforeIndexAsked atomic.Pointer[func(source string)]

// indexSources decides which of sources the plugin is asked to index, asks
// for each in turn, and follows it to its end. Each Source comes with where
// its file is on this computer and whether that file is the one that was
// added.
func (r *indexRun) indexSources(ctx context.Context, spec IndexSpec, sources []SourceStatus) (IndexResult, error) {
	result := IndexResult{Topic: r.topic, Plugin: r.plugin.Name, Collection: r.collection, DryRun: spec.DryRun,
		Sources: []IndexOutcome{}}
	said, err := r.conn.Status(ctx, r.collection)
	if err != nil {
		return IndexResult{}, err
	}
	r.asked = true
	contract := r.conn.Contract()
	var toAsk []int
	for i, src := range sources {
		listed := statusOf(said, src.ID)
		index, _ := indexOf(src.Source, listed, contract, r.plugin.Name)
		out := IndexOutcome{ID: src.ID, Title: src.Title, Path: src.Path, URL: src.URL, SourceIndex: index}
		switch {
		case index.State == IndexIndexed && !spec.Again:
			// Nothing to do.
		case !slices.Contains(contract.Sources, src.Kind):
			out.Skipped = "Knowledge base plugin " + r.plugin.Name + " does not index " + kindsOfSource[src.Kind]
			if listed == nil {
				// The reason says the same.
				out.Reason = ""
			}
		case src.Kind == SourceFile && (spec.DryRun || src.State != SourceOK):
			// The plugin is handed where the file really is, and that is
			// what is shown. A dry run reads every file now, to say what
			// the real run would do with it.
			if sources[i].Path, out.Skipped, err = r.c.fileToHandOver(ctx, r.topic, src); err != nil {
				return IndexResult{}, err
			}
			out.Path, out.Asked = sources[i].Path, out.Skipped == ""
		case src.Kind == SourceFile:
			// Read just before it is handed over, below: by then the files
			// before it are indexed, which takes minutes, and a file inside
			// the Study home is one the agent can exchange meanwhile.
			out.Asked = true
		default:
			out.Asked = true
		}
		if out.Asked {
			toAsk = append(toAsk, i)
		}
		result.Sources = append(result.Sources, out)
	}
	if spec.DryRun {
		return result, nil
	}
	every := spec.ProgressEvery
	if every <= 0 {
		every = DefaultProgressEvery
	}
	for n, i := range toAsk {
		if sources[i].Kind == SourceFile {
			// The file is read, whole, as the last thing before the plugin
			// is asked: what is handed over is where the file really is
			// now, and only a file that is the Source's.
			out := &result.Sources[i]
			if sources[i].Path, out.Skipped, err = r.c.fileToHandOver(ctx, r.topic, sources[i]); err != nil {
				return IndexResult{}, err
			}
			if out.Path = sources[i].Path; out.Skipped != "" {
				out.Asked = false
				continue
			}
		}
		src := sources[i]
		report := func(state string, began time.Time, index SourceIndex) {
			if spec.Progress == nil {
				return
			}
			p := IndexProgress{Source: src.ID, Title: src.Title, Number: n + 1, Total: len(toAsk), State: state, Progress: index.Progress}
			if !began.IsZero() {
				p.Elapsed = time.Since(began)
			}
			if state == ProgressFinished {
				p.Outcome = index.State
			}
			spec.Progress(p)
		}
		report(ProgressStarted, time.Time{}, SourceIndex{})
		began := time.Now()
		r.stopping = func() { report(ProgressStopping, began, SourceIndex{}) }
		index, err := r.askToIndex(ctx, src, spec.Again)
		if err != nil {
			return IndexResult{}, err
		}
		if index.State == IndexIndexing {
			if index, err = r.follow(ctx, src.Source, every, func(now SourceIndex) { report(ProgressRunning, began, now) }); err != nil {
				return IndexResult{}, err
			}
		}
		r.underWay, r.stopping = "", nil
		result.Sources[i].SourceIndex = index
		report(ProgressFinished, began, index)
	}
	return result, nil
}

// statusOf is what a plugin's answer says of the Source with this id, or
// nil when it lists none.
func statusOf(said []plugincontract.SourceStatus, id string) *plugincontract.SourceStatus {
	for i := range said {
		if said[i].ID == id {
			return &said[i]
		}
	}
	return nil
}

// fileToHandOver returns where a file Source is, to hand to a plugin, or
// why it is not handed over (skipped).
//
// A Source is its content: a plugin is told the hash of the content study
// read, so only the file the Topic records is handed over, and a file that
// has other content now is another Source. The file is read once more for
// it, whole, however lately it was found: finding it trusts a size and a
// time that have not changed.
//
// The path is the file's real one, with every symbolic link followed, and
// the content is read at that path. A Source can be a link, in a Topic or
// anywhere, and a plugin reads what it is handed, a moment later and
// perhaps outside any sandbox: handed the link, it would read wherever the
// link leads by then. And whoever runs the command sees which file that is.
// When the file cannot be handed over, the path is the one it was found by.
func (c *Core) fileToHandOver(ctx context.Context, topicID string, src SourceStatus) (path, skipped string, err error) {
	const addIt = "restore it, or add the file as it is now as a new Source with study source add"
	switch src.State {
	case SourceMissing:
		return "", "its file is not on this computer: say where it is with study source update " + topicID + " " + src.ID +
			" --path <file>", nil
	case SourceChanged:
		return src.Path, "the file at " + src.Path + " has other content now than was added, so it is not this Source: " + addIt, nil
	}
	real, err := filepath.EvalSymlinks(src.Path)
	if err != nil {
		return src.Path, "its file, " + src.Path + ", cannot be read (" + pathReasonOf(err) + ")", nil
	}
	hash, _, _, err := hashFile(ctx, real)
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "", "", err
	case err != nil:
		return src.Path, "its file, " + src.Path + ", cannot be read (" + pathReasonOf(err) + ")", nil
	case hash != src.Hash:
		return src.Path, "the file at " + src.Path + " has other content now than was added, so it is not this Source: " + addIt, nil
	}
	return real, "", nil
}

// askToIndex asks the plugin to index one Source, and returns what the
// plugin says of it at once. It is the one place study asks a plugin to
// index.
//
// What the plugin refuses, or what study cannot send it as the contract
// shapes it, is that Source's failure and the reason for it: the run goes
// on with the next. A plugin that does not answer, or not as the contract
// has it, ends the run: it would do the same for the next Source, and what
// it holds for this one is not known.
func (r *indexRun) askToIndex(ctx context.Context, src SourceStatus, again bool) (SourceIndex, error) {
	source := plugincontract.Source{ID: src.ID, Title: src.Title, Kind: src.Kind}
	if src.Kind == SourceFile {
		source.Path, source.Hash = src.Path, src.Hash
	} else {
		source.URL, source.Refetch = src.URL, again
	}
	if hook := beforeIndexAsked.Load(); hook != nil {
		(*hook)(src.ID)
	}
	// From here on the plugin may be indexing it, whatever comes of the
	// call: one that was stopped may have reached the plugin all the same.
	r.underWay, r.heard = src.ID, false
	said, err := r.conn.Index(ctx, r.collection, source)
	var about *pluginclient.Error
	switch {
	case err == nil:
		r.heard = true
		return r.outcome(src.Source, &said), nil
	case ctx.Err() == nil && !r.conn.Ended() && errors.As(err, &about) && about.AboutTheCall:
		// The plugin answered, and is indexing nothing for it.
		r.underWay = ""
		return SourceIndex{State: IndexFailed, Reason: err.Error()}, nil
	}
	return SourceIndex{}, err
}

// outcome is a Source's index state as the plugin gives it to a run that
// asked for it. A Source the plugin no longer lists was stopped by someone
// else.
func (r *indexRun) outcome(src Source, said *plugincontract.SourceStatus) SourceIndex {
	if said == nil {
		return SourceIndex{State: IndexNotIndexed, Reason: "Knowledge base plugin " + r.plugin.Name + " stopped indexing it " +
			"and says nothing of it: its indexing was stopped from elsewhere"}
	}
	index, _ := indexOf(src, said, r.conn.Contract(), r.plugin.Name)
	if index.State == IndexNotIndexed {
		index.Reason = "Knowledge base plugin " + r.plugin.Name + " read other content than the Source's: its file changed " +
			"while it was indexed. Run study source index " + r.topic + " " + src.ID + " again"
	}
	return index
}

// follow asks the plugin how far it is with a Source until it is no longer
// indexing it, and returns what it came to. It asks often at first, since
// a short Source is indexed in a moment, and then every IndexPoll.
func (r *indexRun) follow(ctx context.Context, src Source, every time.Duration, running func(SourceIndex)) (SourceIndex, error) {
	poll := r.c.plugins.IndexPoll
	if poll <= 0 {
		poll = DefaultIndexPoll
	}
	wait := min(20*time.Millisecond, poll)
	reported := time.Now()
	for {
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return SourceIndex{}, ctx.Err()
		case <-timer.C:
		}
		said, err := r.conn.Status(ctx, r.collection)
		if err != nil {
			return SourceIndex{}, err
		}
		index := r.outcome(src, statusOf(said, src.ID))
		if index.State != IndexIndexing {
			return index, nil
		}
		if time.Since(reported) >= every {
			reported = time.Now()
			running(index)
		}
		wait = min(2*wait, poll)
	}
}

// ended explains a run that did not reach its end, and first has the plugin
// stop what the run had it indexing: no work goes on behind a command that
// has exited.
//
// The command's own context is cancelled by then, when the command was
// stopped, and a call made with it asks the plugin nothing: so the plugin
// is told with a context of its own, which lasts as long as a plugin has to
// stop.
func (r *indexRun) ended(ctx context.Context, cause error) error {
	if !r.asked && !stopped(ctx) {
		// The plugin did not get as far as saying what it has indexed.
		return cause
	}
	told := "The plugin was indexing nothing for it"
	if r.underWay != "" {
		limit := r.c.plugins.CancelTimeout
		if limit <= 0 {
			limit = DefaultIndexCancelTimeout
		}
		if r.stopping != nil {
			r.stopping()
		}
		own, cancel := context.WithTimeout(context.WithoutCancel(ctx), limit)
		stopped, err := r.conn.Cancel(own, r.collection, r.underWay)
		cancel()
		switch {
		case err == nil && stopped:
			told = "Knowledge base plugin " + r.plugin.Name + " was told to stop indexing Source " + r.underWay + ", and stopped"
		case err == nil && !r.heard && len(r.plugin.Command) == 0:
			// The request to index was on its way when the command was
			// stopped, and a service may take it up after saying this.
			told = "Knowledge base plugin " + r.plugin.Name + " was told to stop indexing Source " + r.underWay +
				", and said it was not indexing it. It had not answered the request to index it, so the service may take " +
				"it up yet: study source list " + r.topic + " shows whether it did"
		case err == nil && !r.heard:
			told = "Knowledge base plugin " + r.plugin.Name + " was told to stop indexing Source " + r.underWay +
				" before it had answered the request to index it, so study stops its program, and whatever it began with it"
		case err == nil:
			told = "Knowledge base plugin " + r.plugin.Name + " was told to stop indexing Source " + r.underWay +
				", and was not indexing it any more"
		case len(r.plugin.Command) > 0:
			// Its program is stopped when this command returns, and its
			// indexing with it.
			told = "Knowledge base plugin " + r.plugin.Name + " did not say that it stopped indexing Source " + r.underWay +
				", so study stops its program, and its indexing with it"
		default:
			told = "study could not tell Knowledge base plugin " + r.plugin.Name + " to stop indexing Source " + r.underWay +
				" (" + err.Error() + "): the service may still be indexing it"
		}
	}
	after := told + ". What is indexed stays indexed, and study source index " + r.topic + " carries on from there"
	if stopped(ctx) {
		return &Error{Code: CodeCanceled, Err: ctx.Err(), Message: "study source index was stopped before it finished. " + after}
	}
	return &Error{Code: CodeOf(cause), Err: cause, Message: strings.TrimSuffix(cause.Error(), ".") + ". " + after}
}
