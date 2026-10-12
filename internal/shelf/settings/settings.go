// Package settings reads Shelf's settings file: which endpoint gives Shelf
// its vectors, which Recipe or which settings of a model's own it is asked
// with, the key it takes if it takes one, and an endpoint that reranks.
//
// The file is the learner's. It is shelf.toml in Lamplight's configuration
// folder, beside the Knowledge base plugin registry and outside the Study
// home, where the learner's agent writes: nothing the agent writes decides
// where Shelf sends a book's text, or a key. Nothing is read from Shelf's
// command line, which shows in the process list, nor from its environment,
// of which study hands a plugin a few variables and no more. The
// environment says only where the configuration folder is.
//
// A file that cannot be used is an error, never a Shelf that quietly
// searches by keyword alone: the learner who wrote an endpoint into it
// would not know. No file at all is Shelf as it is without settings.
package settings

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/mordor-forge/lamplight/v2/internal/shelf/cut"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/endpoint"
)

const (
	// Format is the format of the settings file this Shelf reads, which the
	// file names in its first setting. A file in a newer format is refused.
	Format = 1
	// FileName is the file's name in Lamplight's configuration folder.
	FileName = "shelf.toml"
	// maxBytes is how large a settings file may be.
	maxBytes = 1 << 20
)

// Settings are what the settings file says.
type Settings struct {
	// Path is where the file is, or would be. It is empty when Shelf's
	// environment names no configuration folder. Found says that there is a
	// file there.
	Path  string
	Found bool
	// Embedding is the endpoint that gives Shelf its vectors, and nil when
	// the file names none: Shelf then searches by keyword alone.
	Embedding *endpoint.Embedding
	// Reranking is the endpoint that orders a search's Passages, and nil
	// when the file names none.
	Reranking *endpoint.Reranking
	// PassageSize is about how long a Passage is, in characters, and 0
	// when the file does not say.
	PassageSize int
}

// Recipe is a named set of Shelf's settings for one embedding model: what
// the endpoint is asked with, short of where it is. A setting the file
// gives beside a Recipe's name takes the place of the Recipe's.
type Recipe struct {
	// Model is the model's name, as the endpoint is asked for it.
	Model string
	// Dimensions is how many numbers Shelf keeps of each vector, and
	// SendDimensions whether the endpoint is asked for that many.
	Dimensions     int
	SendDimensions bool
	// Query and Document say how the model is told that a text is a
	// question, or a stretch of a document.
	Query, Document endpoint.Marking
	// PassageSize is about how long a Passage may be for the model, in
	// characters.
	PassageSize int
}

// recipes are the Recipes this Shelf has.
//
// EmbeddingGemma 2 wants a question and a document marked as such, and the
// two Recipes for it differ in who does the marking, since servers differ:
//
//   - embeddinggemma-2 is for a server that embeds a text as it is sent,
//     as llama.cpp, Ollama and vLLM do. Shelf writes the model's own marks
//     around each text, and cuts the vectors itself.
//   - embeddinggemma-2-prompts is for a server that applies the model's
//     prompts itself, by name: the request names the prompt and the size,
//     and the texts go as they are. Marked by Shelf as well, a text would be
//     marked twice there.
//
// 512 numbers of the model's 768: on four books and 300 questions the right
// Passage was found as often with 512 as with 768, and a third less is kept,
// read and compared for every search (docs/shelf.md).
var recipes = map[string]Recipe{
	"embeddinggemma-2": {
		Model: "google/embeddinggemma-2", Dimensions: 512,
		Query:       endpoint.Marking{Text: "task: search result | query: {text}"},
		Document:    endpoint.Marking{Text: "title: {title} | text: {text}"},
		PassageSize: cut.DefaultSize,
	},
	"embeddinggemma-2-prompts": {
		Model: "google/embeddinggemma-2", Dimensions: 512, SendDimensions: true,
		Query:       endpoint.Marking{Fields: map[string]any{"prompt": "query"}},
		Document:    endpoint.Marking{Fields: map[string]any{"prompt": "document"}},
		PassageSize: cut.DefaultSize,
	},
}

// Recipes returns the names of the Recipes this Shelf has, in order.
func Recipes() []string {
	names := slices.Collect(maps.Keys(recipes))
	sort.Strings(names)
	return names
}

// RecipeNamed returns the Recipe of that name.
func RecipeNamed(name string) (Recipe, bool) {
	r, ok := recipes[name]
	r.Query.Fields, r.Document.Fields = maps.Clone(r.Query.Fields), maps.Clone(r.Document.Fields)
	return r, ok
}

// Error is why the settings file cannot be used, in words for the learner.
// It never repeats a value of the file, which may be the key.
type Error struct{ Path, Reason string }

func (e *Error) Error() string {
	return "Shelf's settings file, " + e.Path + ", " + e.Reason
}

// Path is where the settings file is, in the environment getenv reads:
// shelf.toml in the folder lamplight of the configuration folder, which is
// XDG_CONFIG_HOME when that is an absolute path and .config in the home
// folder otherwise. It is empty when neither names a folder.
func Path(getenv func(string) string) string {
	if dir := getenv("XDG_CONFIG_HOME"); filepath.IsAbs(dir) {
		return filepath.Join(dir, "lamplight", FileName)
	}
	if home := getenv("HOME"); filepath.IsAbs(home) {
		return filepath.Join(home, ".config", "lamplight", FileName)
	}
	return ""
}

// Load reads the settings file of the environment getenv reads. No file is
// no settings, and no error. dataFolder is the folder Shelf keeps its index
// in: a settings file inside it, or reached through it, is refused.
func Load(getenv func(string) string, dataFolder string) (Settings, error) {
	path := Path(getenv)
	if path == "" {
		return Settings{}, nil
	}
	return Read(path, dataFolder)
}

// Read reads the settings file at path, as Load does.
func Read(path, dataFolder string) (Settings, error) {
	s := Settings{Path: path}
	fail := func(format string, args ...any) (Settings, error) {
		return Settings{Path: path}, &Error{Path: path, Reason: fmt.Sprintf(format, args...)}
	}
	unreadable := func(err error) (Settings, error) {
		return fail("cannot be read (%v). Shelf does not start without the settings you wrote: make the file readable, or "+
			"move it away to search by keyword alone", cause(err))
	}
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return s, nil
	case err != nil:
		return unreadable(err)
	case !info.Mode().IsRegular():
		return fail("is not a file. Shelf's settings are a file of that name, in TOML")
	case info.Size() > maxBytes:
		return fail("is larger than %d MB, which no settings file is", maxBytes>>20)
	}
	// The folder Shelf keeps its index in holds data, and the contract has
	// a plugin load no settings from it. It is more than a formality: the
	// learner may have let their agent's sandbox write in that folder, so
	// that study source index can run there, and a settings file found
	// through it would let whoever writes an index say where Shelf sends a
	// book's text and its key, from outside the sandbox.
	if dataFolder != "" {
		switch through, err := reachedThrough(path, dataFolder); {
		case err != nil:
			return unreadable(err)
		case through:
			return fail("is inside the folder Shelf keeps its index in, or is reached through it. That folder holds data, " +
				"which more than you may write: keep the settings in a folder that is yours alone, and have no link lead " +
				"from there into Shelf's folder")
		}
	}
	file, err := os.Open(path)
	if err != nil {
		return unreadable(err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	switch {
	case err != nil:
		return unreadable(err)
	case len(data) > maxBytes:
		return fail("is larger than %d MB, which no settings file is", maxBytes>>20)
	}
	parsed, reason := parse(string(data))
	if reason != "" {
		return fail("%s", reason)
	}
	parsed.Path, parsed.Found = path, true
	return parsed, nil
}

// cause is an operating system error without the path it names.
func cause(err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
}

// reachedThrough reports whether the file at path is inside folder, or is
// reached through it: whether any folder a name is looked up in, on the way
// to the file, is that folder. It follows the path one name at a time and
// each link as it meets it, and tells folders apart by what they are, not
// by what they are called.
//
// Nobody but the learner writes the folders on the way to their settings,
// short of the one this looks for, so the file may be opened by its path
// afterwards: a path that does not pass through Shelf's folder holds no
// name that whoever writes there could change.
func reachedThrough(path, folder string) (bool, error) {
	target, err := os.Stat(folder)
	if err != nil {
		// A folder that is not there holds no settings file.
		return false, nil
	}
	const mostLinks = 40
	// The names as they are written, a ".." among them taken where it
	// stands: tidying a path by its letters would take ".." after a link for
	// the folder the link is in, which is not where the system goes.
	rest := strings.Split(path, string(filepath.Separator))
	at, links := string(filepath.Separator), 0
	for len(rest) > 0 {
		name := rest[0]
		rest = rest[1:]
		if name == "" || name == "." {
			continue
		}
		// The folder this name is looked up in, ".." like any other.
		here, err := os.Stat(at)
		if err != nil {
			return false, err
		}
		if os.SameFile(here, target) {
			return true, nil
		}
		if name == ".." {
			// at holds no link, so its parent by name is its parent.
			at = filepath.Dir(at)
			continue
		}
		next := filepath.Join(at, name)
		info, err := os.Lstat(next)
		if err != nil {
			return false, err
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			at = next
			continue
		}
		if links++; links > mostLinks {
			return false, errors.New("too many links on the way to it")
		}
		leads, err := os.Readlink(next)
		if err != nil {
			return false, err
		}
		if filepath.IsAbs(leads) {
			at = string(filepath.Separator)
		}
		rest = append(strings.Split(leads, string(filepath.Separator)), rest...)
	}
	return false, nil
}

// table is one table of the file, with the name it is spoken of by.
type table struct {
	name   string
	values map[string]any
}

// parse reads the file's text. It returns what is wrong with it, in words
// that hold no value of the file, or nothing.
func parse(text string) (Settings, string) {
	var root map[string]any
	if _, err := toml.Decode(text, &root); err != nil {
		// Never the parser's own words: they can repeat what a line holds,
		// and a line may hold the key.
		var bad toml.ParseError
		where := "is not valid TOML"
		if errors.As(err, &bad) {
			where = "is not valid TOML at line " + strconv.Itoa(bad.Position.Line)
		}
		return Settings{}, where + ": each line is a name, an equals sign and a value, and a value that is text stands " +
			"between double quotes"
	}
	top := table{"", root}
	// The format before anything else: what a newer format holds is not
	// this Shelf's to judge.
	format, ok, reason := top.integer("format")
	switch {
	case reason != "":
		return Settings{}, reason
	case !ok:
		return Settings{}, "does not say which format it is in: begin it with the line format = " + strconv.Itoa(Format)
	case format < 1:
		return Settings{}, "names a format that is none: the format is a whole number, and this Shelf reads format = " + strconv.Itoa(Format)
	case format > Format:
		return Settings{}, fmt.Sprintf("is in format %d, which a newer Shelf reads, and this one reads format %d: update "+
			"Lamplight, which Shelf is part of", format, Format)
	}
	if unknown := top.unknown("format", "embedding", "reranking"); unknown != "" {
		return Settings{}, "holds " + unknown + ", which this Shelf does not know. It knows format, [embedding] and [reranking]"
	}
	var out Settings
	if section, there, reason := top.table("embedding"); reason != "" {
		return Settings{}, reason
	} else if there {
		embedding, size, reason := embedding(section)
		if reason != "" {
			return Settings{}, reason
		}
		out.Embedding, out.PassageSize = &embedding, size
	}
	if section, there, reason := top.table("reranking"); reason != "" {
		return Settings{}, reason
	} else if there {
		reranking, reason := reranking(section)
		if reason != "" {
			return Settings{}, reason
		}
		out.Reranking = &reranking
	}
	return out, ""
}

// embedding reads the [embedding] table: a Recipe's settings, each replaced
// by the one the table gives itself.
func embedding(t table) (endpoint.Embedding, int, string) {
	var e endpoint.Embedding
	if unknown := t.unknown("endpoint", "key", "recipe", "model", "dimensions", "send_dimensions", "query", "document",
		"query_request", "document_request", "passage_size", "batch"); unknown != "" {
		return e, 0, "holds " + unknown + ", which this Shelf does not know"
	}
	var recipe Recipe
	name, named, reason := t.text("recipe")
	if reason != "" {
		return e, 0, reason
	}
	if named {
		var ok bool
		if recipe, ok = RecipeNamed(name); !ok {
			return e, 0, "names a Recipe this Shelf does not have. It has " + strings.Join(Recipes(), " and ") + ". For " +
				"another model, leave recipe out and give the model's own settings: model, and what the model needs of " +
				"dimensions, query and document"
		}
	}
	e.Model, e.Dimensions, e.SendDimensions = recipe.Model, recipe.Dimensions, recipe.SendDimensions
	e.Query, e.Document = recipe.Query, recipe.Document
	size := recipe.PassageSize

	var given bool
	if e.Address, given, reason = t.text("endpoint"); reason != "" {
		return e, 0, reason
	} else if !given {
		return e, 0, "has an [embedding] table that names no endpoint: give the address the endpoint serves the OpenAI API " +
			"at, such as endpoint = \"http://127.0.0.1:8080/v1\", or take the table out to search by keyword alone"
	}
	if e.Key, _, reason = t.text("key"); reason != "" {
		return e, 0, reason
	}
	if model, given, reason := t.text("model"); reason != "" {
		return e, 0, reason
	} else if given {
		e.Model = model
	}
	if n, given, reason := t.integer("dimensions"); reason != "" {
		return e, 0, reason
	} else if given {
		if n < 0 || n > endpoint.MaxDimensions {
			return e, 0, fmt.Sprintf("has an [embedding] dimensions that is not a number from 1 to %d", endpoint.MaxDimensions)
		}
		e.Dimensions = int(n)
	}
	if send, given, reason := t.boolean("send_dimensions"); reason != "" {
		return e, 0, reason
	} else if given {
		e.SendDimensions = send
	}
	// How a text is marked: by text Shelf writes around it, or by fields of
	// the request. Either of the table's own takes the place of the
	// Recipe's, and the two ways are never both said.
	texts, fields := false, false
	for _, which := range []struct {
		name string
		into *endpoint.Marking
	}{{"query", &e.Query}, {"document", &e.Document}} {
		if text, given, reason := t.text(which.name); reason != "" {
			return e, 0, reason
		} else if given {
			which.into.Text, texts = text, true
		}
		if request, given, reason := t.fields(which.name + "_request"); reason != "" {
			return e, 0, reason
		} else if given {
			which.into.Fields, fields = request, true
		}
	}
	recipeTexts := recipe.Query.Text != "" || recipe.Document.Text != ""
	recipeFields := len(recipe.Query.Fields) > 0 || len(recipe.Document.Fields) > 0
	switch {
	case texts && fields:
		return e, 0, "says both what text Shelf writes around a question and a document (query, document) and what the " +
			"request tells the endpoint to write itself (query_request, document_request): an endpoint takes one of the " +
			"two, or a text would be marked twice"
	case named && fields && recipeTexts, named && texts && recipeFields:
		other := "leave recipe out and give the model's own settings"
		if sibling := strings.TrimSuffix(name, "-prompts"); sibling != name && recipeFields {
			other = "use the Recipe " + sibling + ", or " + other
		} else if _, has := recipes[name+"-prompts"]; has && recipeTexts {
			other = "use the Recipe " + name + "-prompts, or " + other
		}
		return e, 0, "names a Recipe that marks a question and a document in one way, and settings that mark them in the " +
			"other: Shelf writes text around them (query, document), or the request tells the endpoint to (query_request, " +
			"document_request), never both. To change the way, " + other
	}
	if n, given, reason := t.integer("passage_size"); reason != "" {
		return e, 0, reason
	} else if given {
		if n < cut.MinSize || n > cut.MaxSize {
			return e, 0, fmt.Sprintf("has an [embedding] passage_size that is not a number of characters from %d to %d", cut.MinSize, cut.MaxSize)
		}
		size = int(n)
	}
	if n, given, reason := t.integer("batch"); reason != "" {
		return e, 0, reason
	} else if given {
		if n < 1 || n > endpoint.MaxBatch {
			return e, 0, fmt.Sprintf("has an [embedding] batch that is not a number from 1 to %d", endpoint.MaxBatch)
		}
		e.Batch = int(n)
	}
	if e.Model == "" {
		return e, 0, "has an [embedding] table that names neither a recipe nor a model: give recipe = \"" + Recipes()[0] +
			"\", or the model's own settings, model first"
	}
	if err := e.Check(); err != nil {
		return e, 0, "has an [embedding] table that Shelf cannot use: " + err.Error()
	}
	return e, size, ""
}

// reranking reads the [reranking] table.
func reranking(t table) (endpoint.Reranking, string) {
	var r endpoint.Reranking
	if unknown := t.unknown("endpoint", "key", "model", "candidates"); unknown != "" {
		return r, "holds " + unknown + ", which this Shelf does not know"
	}
	var given bool
	var reason string
	if r.Address, given, reason = t.text("endpoint"); reason != "" {
		return r, reason
	} else if !given {
		return r, "has a [reranking] table that names no endpoint: give the address the endpoint serves its API at, such as " +
			"endpoint = \"http://127.0.0.1:8081/v1\", or take the table out"
	}
	if r.Key, _, reason = t.text("key"); reason != "" {
		return r, reason
	}
	if r.Model, _, reason = t.text("model"); reason != "" {
		return r, reason
	}
	if n, given, reason := t.integer("candidates"); reason != "" {
		return r, reason
	} else if given {
		if n < 2 || n > endpoint.MaxCandidates {
			return r, fmt.Sprintf("has a [reranking] candidates that is not a number from 2 to %d", endpoint.MaxCandidates)
		}
		r.Candidates = int(n)
	}
	if err := r.Check(); err != nil {
		return r, "has a [reranking] table that Shelf cannot use: " + err.Error()
	}
	return r, ""
}

// spoken is a setting's name as the learner finds it in the file.
func (t table) spoken(key string) string {
	if t.name == "" {
		return key
	}
	return "[" + t.name + "] " + key
}

// unknown names a setting of the table that is none of known, or nothing.
// A name is the learner's own word and no value, so it is repeated, cut
// short and quoted.
func (t table) unknown(known ...string) string {
	var names []string
	for key := range t.values {
		if !slices.Contains(known, key) {
			names = append(names, key)
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	name := names[0]
	if len(name) > 40 {
		name = name[:40] + "…"
	}
	what := "the setting " + strconv.QuoteToASCII(name)
	if t.name != "" {
		what += " in [" + t.name + "]"
	}
	return what
}

func (t table) text(key string) (string, bool, string) {
	value, there := t.values[key]
	if !there {
		return "", false, ""
	}
	text, ok := value.(string)
	if !ok {
		return "", false, "has a " + t.spoken(key) + " that is not text between double quotes"
	}
	return text, true, ""
}

func (t table) integer(key string) (int64, bool, string) {
	value, there := t.values[key]
	if !there {
		return 0, false, ""
	}
	n, ok := value.(int64)
	if !ok {
		return 0, false, "has a " + t.spoken(key) + " that is not a whole number"
	}
	return n, true, ""
}

func (t table) boolean(key string) (bool, bool, string) {
	value, there := t.values[key]
	if !there {
		return false, false, ""
	}
	b, ok := value.(bool)
	if !ok {
		return false, false, "has a " + t.spoken(key) + " that is not true or false"
	}
	return b, true, ""
}

func (t table) table(key string) (table, bool, string) {
	value, there := t.values[key]
	if !there {
		return table{}, false, ""
	}
	values, ok := value.(map[string]any)
	if !ok {
		return table{}, false, "has a " + t.spoken(key) + " that is not a table: write [" + key + "] on a line of its own, and its " +
			"settings below it"
	}
	return table{key, values}, true, ""
}

// fields reads a table of fields to add to a request.
func (t table) fields(key string) (map[string]any, bool, string) {
	inner, there, reason := t.table(key)
	if !there || reason != "" {
		if reason != "" {
			reason = "has a " + t.spoken(key) + " that is not a table of fields, such as " + key + " = { prompt = \"query\" }"
		}
		return nil, false, reason
	}
	return inner.values, true, ""
}
