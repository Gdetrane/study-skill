package core

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mordor-forge/lamplight/v2/internal/pluginclient"
	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
)

// Searching a Topic's Sources, and reading again the Passages a quote is
// checked against (ADR-0012).
//
// Both ask the Topic's Knowledge base plugin, and both are reached by an MCP
// tool, evidence_search and evidence_record. Neither has the plugin index or
// read a Source: a search reads the plugin's index, and so does giving a
// Passage again. What they may ask is fixed by tests in internal/mcpserver,
// which call every tool against plugins that record what they are asked, and
// read this package for where a Connection's search and passage tools are
// called: in searchPassages and readPassage below, and nowhere else.
//
// The plugin is started for one of them and stopped again before it
// returns, as for everything else. All of it has one deadline
// (PluginEvidenceTimeout): reading the registry, starting the plugin, and
// every answer. An agent waits for the tool, and the learner for the agent.
//
// What a search may return is what the Topic records:
//
//   - Only Passages of Sources the Topic has, by their ids. A Topic that is
//     removed and another made later under its id share a collection, and
//     the removed Topic's Sources are still in it.
//   - Only Passages of content the Topic records. The plugin's status tool
//     gives the hash of what it holds for each Source, and a Source is
//     searched only when the plugin says it is indexed, with the Source's
//     own hash when it is a file: a plugin that breaks the contract may hold
//     another file's text under the Source's id.
//
// So the plugin is asked what it holds first, and then to search the Sources
// that passed, by their ids. Asked the other way round, the Passages of the
// Sources that did not pass would have taken places among the best, and the
// search would have returned fewer than there are to find. A plugin that
// answers with a Passage of a Source it was not asked for breaks the
// contract, and the client refuses its whole answer.

const (
	// PluginEvidenceTimeout is how long a search of a Topic's Sources gives
	// the Topic's Knowledge base plugin, and so does recording Evidence with
	// its Passages: reading the registry, starting the plugin or reaching
	// it, and its answers, all of it together.
	//
	// It is less than a minute because the agent's tool call waits for it:
	// many MCP clients give a call up after sixty seconds, and study must
	// have stopped the plugin and said what is wrong before that. It is more
	// than the thirty seconds a plugin has to start because a plugin that
	// needs most of those still has a search to answer, which may have a
	// model embed the query and another reorder what was found. Nothing here
	// has the two seconds of status: that is for showing where the learner
	// is, and this is work the agent asked for.
	PluginEvidenceTimeout = plugincontract.EvidenceTime
	// DefaultPassageLimit is how many Passages a search returns unless it
	// is told otherwise, and MaxPassageLimit the most it can be told.
	DefaultPassageLimit = 5
	MaxPassageLimit     = plugincontract.MaxSearchLimit
)

// PassageQuery says what to search a Topic's Sources for.
type PassageQuery struct {
	Topic string
	// Query is what to look for, in words.
	Query string
	// Limit is the most Passages to return. Zero is DefaultPassageLimit, and
	// more than MaxPassageLimit is MaxPassageLimit.
	Limit int
	// Sources are the ids of the Sources to search. Empty is every Source of
	// the Topic.
	Sources []string
}

// FoundPassage is a Passage a search found: a stretch of a Source's own
// text, as the Topic's Knowledge base plugin extracted it.
type FoundPassage struct {
	// ID is the Passage's id, which Evidence quoted from it is recorded
	// with. It is the plugin's, and this computer's: it names the Passage
	// for as long as its Source is not indexed anew.
	ID string `json:"id"`
	// Source is the id of the Source the text is from, and Title its title.
	Source string `json:"source"`
	Title  string `json:"title"`
	// Text is the Source's own text. It is the plugin's, as it gave it:
	// print it as text from a file.
	Text string `json:"text"`
	// Location is where the text is in the Source, when the plugin knows:
	// "p. 46" or "pp. 46–47", counted from the start of the file.
	Location string `json:"location,omitempty"`
	// Score says how well the Passage answers the query: higher is better.
	// It means nothing outside this search.
	Score float64 `json:"score"`
}

// PassageSearch is the result of SearchEvidence.
type PassageSearch struct {
	Topic string `json:"topic"`
	Query string `json:"query"`
	// Plugin is the name of the Topic's Knowledge base plugin.
	Plugin string `json:"plugin"`
	// Passages are the Passages found, best first.
	Passages []FoundPassage `json:"passages"`
	// Searched are the ids of the Sources that were searched: the ones the
	// plugin holds indexed on this computer, as the Topic records them.
	Searched []string `json:"searched"`
	// LeftOut are the Sources that were not searched, each with its index
	// state and why.
	LeftOut []IndexedSource `json:"left_out,omitempty"`
	// Note says, when Sources were left out or there was nothing to search,
	// that it is so.
	Note string `json:"note,omitempty"`
	// Action is the command that has what was left out indexed.
	Action string `json:"action,omitempty"`
}

// SearchEvidence searches a Topic's Sources for the Passages that answer a
// query, best first: the Source's own text, to quote Evidence from. It asks
// the Topic's Knowledge base plugin, which is started for it and stopped
// again, and has PluginEvidenceTimeout for all of it.
//
// It returns only Passages of the Topic's Sources that the plugin holds
// indexed on this computer as the Topic records them. The others are left
// out, and the result says which, why, and the command that has them
// indexed. A Topic without Sources, and one of whose Sources none is
// indexed, are searched for nothing and say so: neither is an error. It
// records nothing and changes nothing.
//
// It fails with failed_precondition when the Topic names no plugin, when the
// plugin is not registered on this computer, cannot be started, does not
// speak the contract or does not answer in time; with newer_format when it
// speaks a newer contract; with not_found for a Source the Topic does not
// have; and with canceled when it is stopped.
func (c *Core) SearchEvidence(ctx context.Context, q PassageQuery) (PassageSearch, error) {
	if err := checkTopicID(q.Topic); err != nil {
		return PassageSearch{}, err
	}
	query, err := cleanQuery(q.Query)
	if err != nil {
		return PassageSearch{}, err
	}
	limit := q.Limit
	switch {
	case limit < 0:
		return PassageSearch{}, invalidf("the limit must be positive")
	case limit == 0:
		limit = DefaultPassageLimit
	case limit > MaxPassageLimit:
		limit = MaxPassageLimit
	}
	for _, id := range q.Sources {
		if !sourceIDPattern.MatchString(id) {
			return PassageSearch{}, invalidf("%q is not a Source id: study source list %s shows them", id, q.Topic)
		}
	}
	if err := ctx.Err(); err != nil {
		return PassageSearch{}, err
	}
	home, topic, err := c.openTopicFolder(q.Topic)
	if err != nil {
		return PassageSearch{}, err
	}
	defer home.Close()
	defer topic.Close()
	kb, err := topicKnowledgeBase(&topicView{root: topic}, q.Topic)
	if err != nil {
		return PassageSearch{}, err
	}
	yourself := "read its Sources yourself (study source list " + q.Topic + " shows where they are)"
	name, ok := pluginOf(kb)
	if !ok {
		return PassageSearch{}, &Error{Code: CodeFailedPrecondition, Message: "Topic " + q.Topic + " names no Knowledge base " +
			"plugin, so there is nothing to search: " + yourself + ". To have it searched, name a plugin with study topic " +
			"update " + q.Topic + " --knowledge-base <name>: study knowledge-base list shows the ones this computer has"}
	}
	h, err := readHistory(topic, q.Topic)
	if err != nil {
		return PassageSearch{}, err
	}
	k := replayHistory(h).knowledge()
	for _, id := range q.Sources {
		if _, known := k.sources[id]; !known {
			return PassageSearch{}, unknownSource(q.Topic, id)
		}
	}
	// The Sources to search, in the order they were added, each once.
	var sources []Source
	for _, id := range k.order {
		if len(q.Sources) == 0 || slices.Contains(q.Sources, id) {
			sources = append(sources, *k.sources[id])
		}
	}
	res := PassageSearch{Topic: q.Topic, Query: query, Plugin: name, Passages: []FoundPassage{}, Searched: []string{}}
	if len(sources) == 0 {
		// Nothing to ask anyone about.
		res.Note = "Topic " + q.Topic + " has no Sources yet, so there is nothing to search: add one with study source add " +
			q.Topic + " --file <path>"
		return res, nil
	}

	p, err := c.startTopicPlugin(ctx, q.Topic, name, "answer a search of the Topic's Sources",
		"Meanwhile the Topic works as one without a Knowledge base: "+yourself)
	if err != nil {
		return PassageSearch{}, err
	}
	defer p.stop()
	held, leftOut, toIndex, err := p.indexed(ctx, sources)
	if err != nil {
		return PassageSearch{}, err
	}
	res.LeftOut = leftOut
	titles := map[string]string{}
	for _, src := range held {
		res.Searched = append(res.Searched, src.ID)
		titles[src.ID] = src.Title
	}
	if len(held) > 0 {
		found, err := p.searchPassages(ctx, query, limit, res.Searched)
		if err != nil {
			return PassageSearch{}, err
		}
		for _, passage := range found {
			title, searched := titles[passage.Source]
			if !searched {
				// The client refuses an answer that holds a Passage of a
				// Source the search was not limited to. Were one to get
				// here all the same, it is not the Topic's to return.
				continue
			}
			res.Passages = append(res.Passages, FoundPassage{ID: passage.ID, Source: passage.Source, Title: title,
				Text: passage.Text, Location: placeOf(passage.Location), Score: passage.Score})
		}
	}
	switch n := len(leftOut); {
	case len(held) == 0 && n == 1:
		res.Note = "nothing was searched: Knowledge base plugin " + name + " does not hold the Source indexed on this " +
			"computer as the Topic records it"
	case len(held) == 0:
		res.Note = "nothing was searched: Knowledge base plugin " + name + " holds none of the " + strconv.Itoa(n) +
			" Sources indexed on this computer as the Topic records them"
	case n == 1:
		res.Note = "1 of the " + strconv.Itoa(len(sources)) + " Sources was not searched: Knowledge base plugin " + name +
			" does not hold it indexed on this computer as the Topic records it"
	case n > 1:
		res.Note = strconv.Itoa(n) + " of the " + strconv.Itoa(len(sources)) + " Sources were not searched: Knowledge base " +
			"plugin " + name + " does not hold them indexed on this computer as the Topic records them"
	}
	if toIndex {
		res.Action = "study source index " + q.Topic
	}
	return res, nil
}

// cleanQuery checks what a search looks for: text a plugin can be handed,
// as the contract has a query. It may span lines; other control characters,
// and the ones that change the direction text is shown in, are refused.
func cleanQuery(s string) (string, error) {
	if !utf8.ValidString(s) {
		return "", invalidf("the query is not valid UTF-8 text")
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "", invalidf("say what to search the Topic's Sources for")
	}
	for _, r := range s {
		if unicode.IsControl(r) && r != '\n' && r != '\t' || unicode.Is(unicode.Bidi_Control, r) {
			return "", invalidf("the query contains a control character")
		}
	}
	if len(s) > plugincontract.MaxQueryBytes {
		return "", invalidf("the query is longer than %d bytes: ask for what the Lesson needs, in a sentence", plugincontract.MaxQueryBytes)
	}
	return s, nil
}

// placeOf is where Passages are in their Source, as Evidence records a
// location: "p. 46", or "pp. 46–47" from the first page of the first to the
// last page of the last. It is nothing unless the plugin gave every one of
// them its pages: a location of a kind this version does not know says
// nothing here, and a web page has none.
func placeOf(locations ...*plugincontract.Location) string {
	first, last := 0, 0
	for _, l := range locations {
		if l == nil || l.Kind != plugincontract.LocationPages || l.First < 1 || l.Last < l.First {
			return ""
		}
		if first == 0 || l.First < first {
			first = l.First
		}
		last = max(last, l.Last)
	}
	switch {
	case first == 0:
		return ""
	case first == last:
		return "p. " + strconv.Itoa(first)
	}
	return "pp. " + strconv.Itoa(first) + "–" + strconv.Itoa(last)
}

// topicPlugin is a Topic's Knowledge base plugin, started to be searched or
// to give Passages again, with the one deadline all of that has.
type topicPlugin struct {
	conn *pluginclient.Connection
	// name is the plugin's name, topic the Topic's id, and collection the
	// name study gives the plugin for the Topic's Sources.
	name, topic, collection string
	// deadline is when the plugin's time is over, and limit how long that
	// time was, counted from when study set out to find the plugin.
	deadline time.Time
	limit    time.Duration
	// doing says what the plugin was started for, and otherwise what to do
	// when it cannot be had: for the errors.
	doing, otherwise string
	// late says that the plugin missed the deadline.
	late bool
}

// startTopicPlugin starts the Knowledge base plugin a Topic names, or
// reaches it, to search the Topic's Sources or to give Passages again. The
// caller stops it. Reading the registry, starting the plugin and whatever
// it is asked afterwards share one deadline (PluginOptions.EvidenceTimeout).
//
// doing says what the plugin is started for, as in "did not <doing> in
// time", and otherwise, a sentence, what to do when the plugin cannot be
// had. It fails with failed_precondition when the plugin is not registered
// on this computer, cannot be started, does not speak the contract or does
// not answer in time, with newer_format when it speaks a newer contract,
// and with canceled when the command was stopped first.
func (c *Core) startTopicPlugin(ctx context.Context, topicID, name, doing, otherwise string) (*topicPlugin, error) {
	limit := c.plugins.EvidenceTimeout
	if limit <= 0 {
		limit = PluginEvidenceTimeout
	}
	home, err := c.realHome()
	if err != nil {
		return nil, err
	}
	p := &topicPlugin{name: name, topic: topicID, collection: collectionName(home, topicID), limit: limit, doing: doing,
		otherwise: otherwise}
	list, began, err := c.registryInTime(ctx, limit)
	p.deadline = began.Add(limit)
	switch {
	case ctx.Err() != nil:
		return nil, &Error{Code: CodeCanceled, Err: ctx.Err(), Message: "the command was stopped before Knowledge base plugin " +
			name + " was started"}
	case err != nil:
		// The registry could not be read, in time or at all: no plugin was
		// started, and none is to blame.
		return nil, p.meanwhile(err)
	}
	plugin, err := registered(list, name)
	if err != nil {
		return nil, &Error{Code: CodeFailedPrecondition, Err: err, Message: "Topic " + topicID + " names the Knowledge base " +
			"plugin " + name + ", which is not registered on this computer: register it with study knowledge-base add " +
			name + " -- <command>, or name another with study topic update " + topicID + " --knowledge-base <name>. " + otherwise}
	}
	if p.conn, err = c.startPlugin(ctx, plugin, began, pluginUse{within: limit}); err != nil {
		return nil, p.failed(ctx, err)
	}
	return p, nil
}

// stop stops the plugin: at once when it missed its deadline, since it was
// given its time already.
func (p *topicPlugin) stop() {
	if p.late {
		p.conn.GiveUp()
	}
	p.conn.Close()
}

// asking returns the context a call to the plugin is made with: the
// caller's, which ends at the plugin's deadline at the latest.
func (p *topicPlugin) asking(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithDeadline(ctx, p.deadline)
}

// failed explains why the plugin could not be had, or did not answer.
//
// A plugin that ran out of its time is said so in the same words wherever
// it was when the time ran out. What the plugin refused of one call is that
// call's own error, and a command that was stopped says so itself: both are
// returned as they are. Anything else is the plugin's failure, to which is
// added what to do meanwhile.
func (p *topicPlugin) failed(ctx context.Context, err error) error {
	switch code := CodeOf(err); {
	case ctx.Err() != nil:
		return err
	case !time.Now().Before(p.deadline):
		p.late = true
		return &Error{Code: CodeFailedPrecondition, Err: err, Message: "Knowledge base plugin " + p.name + " did not " + p.doing +
			" within " + p.limit.String() + ", starting it included, so study stopped it. study knowledge-base check " + p.name +
			" shows whether it starts and what it says. " + p.otherwise}
	case code == CodeFailedPrecondition || code == CodeNewerFormat:
		return p.meanwhile(err)
	}
	return err
}

// meanwhile is an error with what to do while it lasts.
func (p *topicPlugin) meanwhile(err error) error {
	return &Error{Code: CodeOf(err), Err: err, Message: strings.TrimSuffix(err.Error(), ".") + ". " + p.otherwise}
}

// indexed asks the plugin what it holds of the Topic's Sources, and says
// which of sources it holds indexed as the Topic records them (held), which
// it does not (leftOut, each with its state and why), and whether study
// source index would ask the plugin for one of those (toIndex).
//
// Indexed is what a listing of the Sources calls indexed: the plugin says
// so, and for a file the hash of what it holds is the Source's own.
func (p *topicPlugin) indexed(ctx context.Context, sources []Source) (held []Source, leftOut []IndexedSource, toIndex bool, err error) {
	asking, cancel := p.asking(ctx)
	defer cancel()
	said, err := p.conn.Status(asking, p.collection)
	if err != nil {
		return nil, nil, false, p.failed(ctx, err)
	}
	contract := p.conn.Contract()
	for _, src := range sources {
		index, wanted := indexOf(src, statusOf(said, src.ID), contract, p.name)
		if index.State == IndexIndexed {
			held = append(held, src)
			continue
		}
		index.Reason = shorten(index.Reason, maxIndexTextBytes)
		leftOut = append(leftOut, IndexedSource{ID: src.ID, Title: src.Title, SourceIndex: index})
		// A Source that failed is tried again by whoever runs the command,
		// when the plugin indexes Sources of its kind at all.
		toIndex = toIndex || wanted || index.State == IndexFailed && slices.Contains(contract.Sources, src.Kind)
	}
	return held, leftOut, toIndex, nil
}

// searchPassages asks the plugin for the Passages of some of the Topic's
// Sources that answer a query. It is the one place study searches a
// plugin.
func (p *topicPlugin) searchPassages(ctx context.Context, query string, limit int, sources []string) ([]plugincontract.ScoredPassage, error) {
	asking, cancel := p.asking(ctx)
	defer cancel()
	found, err := p.conn.Search(asking, p.collection, query, limit, sources)
	if err != nil {
		return nil, p.failed(ctx, err)
	}
	return found, nil
}

// readPassage asks the plugin for one Passage again, by the id a search
// gave. It is the one place study does.
func (p *topicPlugin) readPassage(ctx context.Context, id string) (plugincontract.Passage, error) {
	asking, cancel := p.asking(ctx)
	defer cancel()
	passage, err := p.conn.Passage(asking, p.collection, id)
	if err != nil {
		return plugincontract.Passage{}, p.failed(ctx, err)
	}
	return passage, nil
}

// quotedPassages reads again the Passages Evidence is to be quoted from, in
// the order given: each must be a Passage of the Source, and the plugin must
// hold the Source indexed as the Topic records it.
//
// The second is what makes a Passage's text the Source's: a plugin that
// breaks the contract may hold another file's text under the Source's id,
// and says so through the hash of what it holds. The first keeps out the
// Passages of a Topic that was removed, which share the collection of the
// one made later under its id.
func (p *topicPlugin) quotedPassages(ctx context.Context, src Source, ids []string) ([]plugincontract.Passage, error) {
	const unchecked = "or record the Evidence without a Passage, which is then not checked"
	held, leftOut, _, err := p.indexed(ctx, []Source{src})
	if err != nil {
		return nil, err
	}
	if len(held) == 0 {
		state := leftOut[0].SourceIndex
		why := strings.ReplaceAll(state.State, "_", " ")
		if state.Reason != "" {
			why += ": " + state.Reason
		}
		return nil, &Error{Code: CodeFailedPrecondition, Message: "Knowledge base plugin " + p.name + " does not hold Source " +
			src.ID + " indexed on this computer as the Topic records it (" + why + "), so a quote cannot be checked against " +
			"its Passages: run study source index " + p.topic + " " + src.ID + " and search again, " + unchecked}
	}
	passages := make([]plugincontract.Passage, 0, len(ids))
	for _, id := range ids {
		passage, err := p.readPassage(ctx, id)
		switch {
		case CodeOf(err) == CodeNotFound:
			return nil, &Error{Code: CodeNotFound, Err: err, Message: "Knowledge base plugin " + p.name + " has no Passage " + id +
				" for Topic " + p.topic + ": a Passage's id is the one a search gave, and names nothing once its Source is " +
				"indexed anew. Search again with study evidence search " + p.topic + " <query>, " + unchecked}
		case err != nil:
			return nil, err
		case passage.Source != src.ID:
			return nil, invalidf("Passage %s is of Source %s, and the Evidence names Source %s: a quote is checked against "+
				"Passages of the Source it is from", id, passage.Source, src.ID)
		}
		passages = append(passages, passage)
	}
	return passages, nil
}
