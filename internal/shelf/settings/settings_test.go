package settings_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mordor-forge/lamplight/v2/internal/shelf/endpoint"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/settings"
)

// secret stands in every file below where a key would: nothing Shelf says
// of a file may repeat it.
const secret = "sk-settings-test-91c3e7a04b"

// computer is a learner's configuration folder and the folder Shelf keeps
// its index in, both temporary.
type computer struct {
	home, config, data string
}

func newComputer(t *testing.T) computer {
	t.Helper()
	base := t.TempDir()
	if real, err := filepath.EvalSymlinks(base); err == nil {
		base = real
	}
	c := computer{home: filepath.Join(base, "home")}
	c.config = filepath.Join(c.home, ".config", "lamplight")
	c.data = filepath.Join(c.home, ".local", "state", "lamplight", "knowledge-base-plugins", "shelf")
	for _, dir := range []string{c.config, c.data} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func (c computer) getenv(key string) string {
	return map[string]string{"HOME": c.home}[key]
}

func (c computer) write(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(c.config, settings.FileName)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// load reads the computer's settings file, as study-shelf does when it
// starts.
func (c computer) load() (settings.Settings, error) {
	return settings.Load(c.getenv, c.data)
}

func TestWhereTheSettingsFileIs(t *testing.T) {
	for name, tc := range map[string]struct {
		env  map[string]string
		want string
	}{
		"in the home folder's configuration": {map[string]string{"HOME": "/home/ada"}, "/home/ada/.config/lamplight/shelf.toml"},
		"in the configuration folder":        {map[string]string{"HOME": "/home/ada", "XDG_CONFIG_HOME": "/etc/ada"}, "/etc/ada/lamplight/shelf.toml"},
		"a relative folder is none":          {map[string]string{"HOME": "/home/ada", "XDG_CONFIG_HOME": "config"}, "/home/ada/.config/lamplight/shelf.toml"},
		"no folder is named":                 {map[string]string{"HOME": "ada"}, ""},
		"nothing is set":                     {nil, ""},
	} {
		if got := settings.Path(func(key string) string { return tc.env[key] }); got != tc.want {
			t.Errorf("%s: Path = %q, want %q", name, got, tc.want)
		}
	}
}

// No file is no settings: Shelf searches by keyword, as it does without
// any of this. So is an environment that names no configuration folder.
func TestNoFileIsNoSettings(t *testing.T) {
	c := newComputer(t)
	got, err := c.load()
	if err != nil || got.Found || got.Embedding != nil || got.Reranking != nil || got.PassageSize != 0 {
		t.Errorf("Load() = %+v, %v; want nothing, and where the file would be", got, err)
	}
	if got.Path != filepath.Join(c.config, "shelf.toml") {
		t.Errorf("the file would be at %q", got.Path)
	}
	nowhere, err := settings.Load(func(string) string { return "" }, c.data)
	if err != nil || nowhere.Path != "" || nowhere.Embedding != nil {
		t.Errorf("with no configuration folder, Load() = %+v, %v", nowhere, err)
	}
	// A file that says its format and nothing else is no settings either.
	c.write(t, "format = 1\n")
	if got, err := c.load(); err != nil || !got.Found || got.Embedding != nil || got.Reranking != nil {
		t.Errorf("a file with a format alone: %+v, %v", got, err)
	}
}

// The Recipe for a server that embeds a text as it is sent: Shelf writes
// the model's marks itself, and asks for no size.
func TestTheRecipeForEmbeddingGemma2(t *testing.T) {
	c := newComputer(t)
	c.write(t, `# Shelf's settings
format = 1

[embedding]
endpoint = "http://127.0.0.1:8080/v1"
recipe = "embeddinggemma-2"
key = "`+secret+`"

[reranking]
endpoint = "http://127.0.0.1:8081"
model = "qwen3-reranker-0.6b"
`)
	got, err := c.load()
	if err != nil {
		t.Fatal(err)
	}
	e := got.Embedding
	if e == nil || e.Address != "http://127.0.0.1:8080/v1" || e.Key != secret || e.Model != "google/embeddinggemma-2" ||
		e.Dimensions != 512 || e.SendDimensions || e.Batch != 0 {
		t.Fatalf("the embedding endpoint is %+v", e)
	}
	if e.Query.Text != "task: search result | query: {text}" || e.Document.Text != "title: {title} | text: {text}" ||
		len(e.Query.Fields)+len(e.Document.Fields) != 0 {
		t.Errorf("a question is marked %+v and a document %+v", e.Query, e.Document)
	}
	if got.PassageSize != 1500 {
		t.Errorf("a Passage is about %d characters, want the Recipe's 1500", got.PassageSize)
	}
	if r := got.Reranking; r == nil || r.Address != "http://127.0.0.1:8081" || r.Model != "qwen3-reranker-0.6b" || r.Key != "" || r.Candidates != 0 {
		t.Errorf("the reranking endpoint is %+v", r)
	}
	// What was read makes an embedder and a reranker.
	if _, err := endpoint.NewEmbedder(*got.Embedding); err != nil {
		t.Errorf("NewEmbedder: %v", err)
	}
	if _, err := endpoint.NewReranker(*got.Reranking); err != nil {
		t.Errorf("NewReranker: %v", err)
	}
}

// The Recipe for a server that applies the model's prompts itself: the
// request names the prompt and the size, and Shelf writes nothing around a
// text.
func TestTheRecipeForAServerThatMarksTheTextsItself(t *testing.T) {
	c := newComputer(t)
	c.write(t, "format = 1\n[embedding]\nendpoint = \"https://embeddings.example/v1\"\nrecipe = \"embeddinggemma-2-prompts\"\n")
	got, err := c.load()
	if err != nil {
		t.Fatal(err)
	}
	e := got.Embedding
	// Eight Passages in a request: the server this Recipe was held to
	// refuses more.
	if e.Model != "google/embeddinggemma-2" || e.Dimensions != 512 || !e.SendDimensions || e.Batch != 8 {
		t.Errorf("the embedding endpoint is %+v", e)
	}
	// A batch the learner gives beside the Recipe takes its place.
	c.write(t, "format = 1\n[embedding]\nendpoint = \"https://embeddings.example/v1\"\nrecipe = \"embeddinggemma-2-prompts\"\nbatch = 32\n")
	if own, err := c.load(); err != nil || own.Embedding.Batch != 32 {
		t.Errorf("with a batch of the learner's own, the settings are %+v, %v", own.Embedding, err)
	}
	if e.Query.Text != "" || e.Document.Text != "" || fmt.Sprint(e.Query.Fields, e.Document.Fields) != "map[prompt:query] map[prompt:document]" {
		t.Errorf("a question is marked %+v and a document %+v", e.Query, e.Document)
	}
	if fmt.Sprint(settings.Recipes()) != "[embeddinggemma-2 embeddinggemma-2-prompts]" {
		t.Errorf("Shelf has the Recipes %v", settings.Recipes())
	}
	// A Recipe that is handed out is the caller's own: changing it changes
	// no other.
	e.Query.Fields["prompt"] = "changed"
	if again, _ := settings.RecipeNamed("embeddinggemma-2-prompts"); again.Query.Fields["prompt"] != "query" {
		t.Errorf("a Recipe changed under its name: %+v", again.Query)
	}
}

// Any other model works through settings of its own, and a setting beside a
// Recipe's name takes the place of the Recipe's.
func TestSettingsOfTheLearnersOwn(t *testing.T) {
	c := newComputer(t)
	c.write(t, `format = 1
[embedding]
endpoint = "http://127.0.0.1:11434/v1"
model = "nomic-embed-text"
dimensions = 768
query = "search_query: {text}"
document = "search_document: {text}"
passage_size = 900
batch = 8
`)
	got, err := c.load()
	if err != nil {
		t.Fatal(err)
	}
	if e := got.Embedding; e.Model != "nomic-embed-text" || e.Dimensions != 768 || e.SendDimensions || e.Batch != 8 ||
		e.Query.Text != "search_query: {text}" || e.Document.Text != "search_document: {text}" || got.PassageSize != 900 {
		t.Errorf("the embedding endpoint is %+v, with Passages of %d", e, got.PassageSize)
	}

	c.write(t, `format = 1
[embedding]
endpoint = "http://127.0.0.1:11434/v1"
recipe = "embeddinggemma-2"
model = "embeddinggemma2:latest"
dimensions = 768
`)
	if got, err = c.load(); err != nil {
		t.Fatal(err)
	}
	if e := got.Embedding; e.Model != "embeddinggemma2:latest" || e.Dimensions != 768 || e.Query.Text != "task: search result | query: {text}" {
		t.Errorf("beside a Recipe's name, the settings are %+v", e)
	}

	// Fields of a request, for a server that wants another than prompt.
	c.write(t, `format = 1
[embedding]
endpoint = "https://embeddings.example/v1"
model = "some-model"
dimensions = 256
send_dimensions = true
query_request = { input_type = "query", truncate = true }

[embedding.document_request]
input_type = "passage"
`)
	if got, err = c.load(); err != nil {
		t.Fatal(err)
	}
	if e := got.Embedding; !e.SendDimensions || e.Dimensions != 256 ||
		fmt.Sprint(e.Query.Fields, e.Document.Fields) != "map[input_type:query truncate:true] map[input_type:passage]" {
		t.Errorf("the embedding endpoint is %+v", e)
	}
}

// A file Shelf cannot use is said, with what is wrong and never with what
// the file holds: a line of it may be the key.
func TestAFileThatCannotBeUsedSaysWhy(t *testing.T) {
	embedding := "format = 1\n[embedding]\nkey = \"" + secret + "\"\n"
	for name, tc := range map[string]struct {
		text string
		want []string
	}{
		"a line that is no TOML":      {"format = 1\n[embedding]\nkey = \"" + secret + "\nendpoint = \"http://127.0.0.1:8080/v1\"\n", []string{"is not valid TOML at line 3"}},
		"a key without quotes":        {"format = 1\n[embedding]\nendpoint = \"http://127.0.0.1:8080/v1\"\nkey = " + secret + "\n", []string{"is not valid TOML at line 4", "between double quotes"}},
		"no format":                   {"[embedding]\nendpoint = \"http://127.0.0.1:8080/v1\"\nrecipe = \"embeddinggemma-2\"\n", []string{"does not say which format", "format = 1"}},
		"a newer format":              {"format = 2\n[embedding]\nwhat_a_newer_shelf_knows = true\n[of_tomorrow]\nx = 1\n", []string{"is in format 2", "this one reads format 1", "update Lamplight"}},
		"a format that is none":       {"format = 0\n", []string{"names a format that is none"}},
		"a format that is text":       {"format = \"1\"\n", []string{"format that is not a whole number"}},
		"a table Shelf does not know": {"format = 1\n[embeding]\nendpoint = \"x\"\n", []string{`the setting "embeding"`, "does not know"}},
		"a setting it does not know":  {embedding + "endpiont = \"http://127.0.0.1:8080/v1\"\n", []string{`the setting "endpiont" in [embedding]`}},
		"a Recipe it does not have":   {embedding + "endpoint = \"http://127.0.0.1:8080/v1\"\nrecipe = \"" + secret + "\"\n", []string{"a Recipe this Shelf does not have", "embeddinggemma-2 and embeddinggemma-2-prompts", "leave recipe out"}},
		"no endpoint":                 {embedding + "recipe = \"embeddinggemma-2\"\n", []string{"names no endpoint", "search by keyword alone"}},
		"no model":                    {embedding + "endpoint = \"http://127.0.0.1:8080/v1\"\n", []string{"neither a recipe nor a model"}},
		"an endpoint with a password": {"format = 1\n[embedding]\nendpoint = \"http://ada:" + secret + "@127.0.0.1/v1\"\nmodel = \"m\"\n", []string{"a key goes in key"}},
		"an endpoint that is a file":  {embedding + "endpoint = \"file:///etc/passwd\"\nmodel = \"m\"\n", []string{"not an http or https address"}},
		"a size that is text":         {embedding + "endpoint = \"http://127.0.0.1:8080/v1\"\nmodel = \"m\"\ndimensions = \"512\"\n", []string{"[embedding] dimensions that is not a whole number"}},
		"a size that is too large":    {embedding + "endpoint = \"http://127.0.0.1:8080/v1\"\nmodel = \"m\"\ndimensions = 100000\n", []string{"dimensions that is not a number from 1 to 16384"}},
		"a model without its size":    {embedding + "endpoint = \"http://127.0.0.1:8080/v1\"\nmodel = \"m\"\n", []string{"dimensions is missing", "768 for many"}},
		"a size of none":              {embedding + "endpoint = \"http://127.0.0.1:8080/v1\"\nrecipe = \"embeddinggemma-2\"\ndimensions = 0\n", []string{"dimensions that is not a number from 1 to 16384"}},
		"a switch that is a number":   {embedding + "endpoint = \"http://127.0.0.1:8080/v1\"\nmodel = \"m\"\nsend_dimensions = 1\n", []string{"send_dimensions that is not true or false"}},
		"a mark without the text":     {embedding + "endpoint = \"http://127.0.0.1:8080/v1\"\nmodel = \"m\"\nquery = \"query: \"\n", []string{"holds {text} 0 times"}},
		"fields that are text":        {embedding + "endpoint = \"http://127.0.0.1:8080/v1\"\nmodel = \"m\"\nquery_request = \"query\"\n", []string{"query_request that is not a table of fields"}},
		"a field that is Shelf's":     {embedding + "endpoint = \"http://127.0.0.1:8080/v1\"\nmodel = \"m\"\nquery_request = { model = \"other\" }\n", []string{"which is Shelf's to send"}},
		"both ways of marking": {embedding + "endpoint = \"http://127.0.0.1:8080/v1\"\nmodel = \"m\"\nquery = \"q: {text}\"\ndocument_request = { prompt = \"document\" }\n",
			[]string{"an endpoint takes one of the", "marked twice"}},
		"a Recipe, marked the other way": {embedding + "endpoint = \"http://127.0.0.1:8080/v1\"\nrecipe = \"embeddinggemma-2\"\nquery_request = { prompt = \"query\" }\n",
			[]string{"never both", "use the Recipe embeddinggemma-2-prompts"}},
		"the other Recipe, marked the other way": {embedding + "endpoint = \"http://127.0.0.1:8080/v1\"\nrecipe = \"embeddinggemma-2-prompts\"\ndocument = \"d: {text}\"\n",
			[]string{"never both", "use the Recipe embeddinggemma-2,"}},
		"Passages too short":           {embedding + "endpoint = \"http://127.0.0.1:8080/v1\"\nmodel = \"m\"\npassage_size = 10\n", []string{"passage_size that is not a number of characters from 200 to 5000"}},
		"too large a batch":            {embedding + "endpoint = \"http://127.0.0.1:8080/v1\"\nmodel = \"m\"\nbatch = 0\n", []string{"batch that is not a number from 1 to 256"}},
		"a table that is a value":      {"format = 1\nembedding = \"" + secret + "\"\n", []string{"embedding that is not a table"}},
		"reranking without a model":    {"format = 1\n[reranking]\nendpoint = \"http://127.0.0.1:8081/v1\"\nkey = \"" + secret + "\"\n", []string{"[reranking] table that Shelf cannot use", "model is missing"}},
		"reranking nowhere":            {"format = 1\n[reranking]\nmodel = \"m\"\nkey = \"" + secret + "\"\n", []string{"[reranking] table that names no endpoint"}},
		"too few candidates":           {"format = 1\n[reranking]\nendpoint = \"http://127.0.0.1:8081/v1\"\nmodel = \"m\"\ncandidates = 1\n", []string{"candidates that is not a number from 2 to 100"}},
		"a reranking setting too many": {"format = 1\n[reranking]\nendpoint = \"http://127.0.0.1:8081/v1\"\nmodel = \"m\"\ntop_n = 5\n", []string{`the setting "top_n" in [reranking]`}},
	} {
		t.Run(name, func(t *testing.T) {
			c := newComputer(t)
			path := c.write(t, tc.text)
			got, err := c.load()
			var failed *settings.Error
			if !errors.As(err, &failed) {
				t.Fatalf("Load() = %+v, %v; want it refused", got, err)
			}
			if got.Embedding != nil || got.Reranking != nil {
				t.Errorf("a file that is refused still gave %+v", got)
			}
			said := err.Error()
			for _, word := range append(tc.want, "Shelf's settings file, "+path) {
				if !strings.Contains(said, word) {
					t.Errorf("it does not say %q: %s", word, said)
				}
			}
			if strings.Contains(said, secret) {
				t.Errorf("it repeats what the file holds, which may be the key: %s", said)
			}
		})
	}
}

func TestWhatIsNoFileIsRefused(t *testing.T) {
	c := newComputer(t)
	path := filepath.Join(c.config, settings.FileName)
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := c.load(); err == nil || !strings.Contains(err.Error(), "is not a file") {
		t.Errorf("a folder under the file's name: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	c.write(t, "format = 1\n# "+strings.Repeat("a long comment ", 80000))
	if _, err := c.load(); err == nil || !strings.Contains(err.Error(), "is larger than 1 MB") {
		t.Errorf("a file of more than a megabyte: %v", err)
	}
	if os.Geteuid() == 0 {
		return // root reads what nobody may
	}
	c.write(t, "format = 1\n")
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := c.load(); err == nil || !strings.Contains(err.Error(), "cannot be read (permission denied)") ||
		!strings.Contains(err.Error(), "search by keyword alone") {
		t.Errorf("a file that may not be read: %v", err)
	}
}

// The folder Shelf keeps its index in holds data. A learner may let their
// agent's sandbox write there, so that study source index can run inside
// it, and a settings file found there would then be the agent's to write:
// the endpoint Shelf sends a book's text to, and the key with it. So a
// settings file inside that folder, or reached through it, is refused,
// however it comes to be there.
func TestASettingsFileInTheIndexFolderIsRefused(t *testing.T) {
	const text = "format = 1\n[embedding]\nendpoint = \"http://127.0.0.1:8080/v1\"\nrecipe = \"embeddinggemma-2\"\n"
	refused := func(t *testing.T, got settings.Settings, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "is inside the folder Shelf keeps its index in, or the folder of the plugins' folders above it, or is reached through one of them") {
			t.Errorf("Load() = %+v, %v; want the file refused for where it is", got, err)
		}
		if got.Embedding != nil {
			t.Errorf("the refused file's endpoint was read all the same: %+v", got.Embedding)
		}
	}
	link := func(t *testing.T, target, name string) {
		t.Helper()
		if err := os.Symlink(target, name); err != nil {
			t.Fatal(err)
		}
	}
	write := func(t *testing.T, path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("the configuration folder is inside it", func(t *testing.T) {
		c := newComputer(t)
		write(t, filepath.Join(c.data, "config", "lamplight", "shelf.toml"))
		got, err := settings.Load(func(key string) string {
			return map[string]string{"HOME": c.home, "XDG_CONFIG_HOME": filepath.Join(c.data, "config")}[key]
		}, c.data)
		refused(t, got, err)
	})
	t.Run("the file is a link into it", func(t *testing.T) {
		c := newComputer(t)
		write(t, filepath.Join(c.data, "planted.toml"))
		link(t, filepath.Join(c.data, "planted.toml"), filepath.Join(c.config, "shelf.toml"))
		got, err := c.load()
		refused(t, got, err)
	})
	t.Run("the folder is a link into it, by another name", func(t *testing.T) {
		c := newComputer(t)
		write(t, filepath.Join(c.data, "conf", "shelf.toml"))
		if err := os.Remove(c.config); err != nil {
			t.Fatal(err)
		}
		// Through a second name of the index folder: what a folder is
		// called decides nothing.
		alias := filepath.Join(c.home, "alias")
		link(t, c.data, alias)
		link(t, filepath.Join(alias, "conf"), c.config)
		got, err := c.load()
		refused(t, got, err)
	})
	t.Run("it is reached through a link kept there", func(t *testing.T) {
		c := newComputer(t)
		// The file itself is outside the index folder. The link that leads
		// to it is inside, where whoever writes an index can point it
		// elsewhere.
		elsewhere := filepath.Join(c.home, "elsewhere")
		write(t, filepath.Join(elsewhere, "shelf.toml"))
		link(t, elsewhere, filepath.Join(c.data, "leads-out"))
		if err := os.Remove(c.config); err != nil {
			t.Fatal(err)
		}
		link(t, filepath.Join(c.data, "leads-out"), c.config)
		got, err := c.load()
		refused(t, got, err)
	})
	t.Run("the way to it climbs out of a link into it", func(t *testing.T) {
		c := newComputer(t)
		// Read by its letters, the link leads to a folder in the home
		// folder. The system goes into the index folder first, and up from
		// there: the file is inside it.
		write(t, filepath.Join(c.data, "conf", "shelf.toml"))
		if err := os.Mkdir(filepath.Join(c.data, "deep"), 0o700); err != nil {
			t.Fatal(err)
		}
		link(t, filepath.Join(c.data, "deep"), filepath.Join(c.home, "a"))
		if err := os.Remove(c.config); err != nil {
			t.Fatal(err)
		}
		link(t, filepath.Join(c.home, "a")+"/../conf", c.config)
		got, err := c.load()
		refused(t, got, err)
	})
	t.Run("a link elsewhere is the learner's own business", func(t *testing.T) {
		c := newComputer(t)
		dotfiles := filepath.Join(c.home, "dotfiles", "lamplight")
		write(t, filepath.Join(dotfiles, "shelf.toml"))
		if err := os.Remove(c.config); err != nil {
			t.Fatal(err)
		}
		link(t, filepath.Join("..", "dotfiles", "lamplight"), c.config)
		got, err := c.load()
		if err != nil || got.Embedding == nil || got.Embedding.Model != "google/embeddinggemma-2" {
			t.Errorf("settings kept in a folder of the learner's own, by a link: %+v, %v", got, err)
		}
	})
	t.Run("an index folder that is not there holds nothing", func(t *testing.T) {
		c := newComputer(t)
		c.write(t, text)
		got, err := settings.Load(c.getenv, filepath.Join(c.home, "not-made"))
		if err != nil || got.Embedding == nil {
			t.Errorf("Load() = %+v, %v", got, err)
		}
	})
	// The folder above Shelf's holds every plugin's folder, and is the one
	// the contract's page has the learner let a sandbox write in.
	plugins := func(c computer) string { return filepath.Dir(c.data) }
	t.Run("the file is a link into the plugins' folder, beside Shelf's", func(t *testing.T) {
		c := newComputer(t)
		write(t, filepath.Join(plugins(c), "planted.toml"))
		link(t, filepath.Join(plugins(c), "planted.toml"), filepath.Join(c.config, "shelf.toml"))
		got, err := c.load()
		refused(t, got, err)
	})
	t.Run("the file is a link into another plugin's folder", func(t *testing.T) {
		c := newComputer(t)
		write(t, filepath.Join(plugins(c), "other", "shelf.toml"))
		link(t, filepath.Join(plugins(c), "other", "shelf.toml"), filepath.Join(c.config, "shelf.toml"))
		got, err := c.load()
		refused(t, got, err)
	})
	t.Run("a folder above an index folder of another name is not looked for", func(t *testing.T) {
		// Started by hand with a folder elsewhere, Shelf guards that folder:
		// the folder above it is then anything, the home folder perhaps.
		c := newComputer(t)
		c.write(t, text)
		data := filepath.Join(c.home, "shelf-data")
		if err := os.Mkdir(data, 0o700); err != nil {
			t.Fatal(err)
		}
		got, err := settings.Load(c.getenv, data)
		if err != nil || got.Embedding == nil {
			t.Errorf("Load() = %+v, %v", got, err)
		}
	})
	t.Run("the file has a second name in the index folder", func(t *testing.T) {
		c := newComputer(t)
		path := c.write(t, text)
		// The file is where it should be, and whoever writes the index
		// folder writes it all the same, under its other name.
		if err := os.Link(path, filepath.Join(c.data, "second-name.toml")); err != nil {
			t.Skipf("no second name can be made here: %v", err)
		}
		got, err := c.load()
		if err == nil || !strings.Contains(err.Error(), "has a second name elsewhere (a hard link)") || got.Embedding != nil {
			t.Errorf("Load() = %+v, %v; want the file refused for its second name", got, err)
		}
	})
	t.Run("an index folder that cannot be looked at decides nothing", func(t *testing.T) {
		if os.Getuid() == 0 {
			t.Skip("root looks at every folder")
		}
		c := newComputer(t)
		c.write(t, text)
		// Whether the settings are reached through Shelf's folder cannot be
		// told when the folder cannot be looked at: that is no "it is not".
		if err := os.Chmod(plugins(c), 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(plugins(c), 0o700) })
		got, err := c.load()
		if err == nil || !strings.Contains(err.Error(), "cannot be read (permission denied)") || got.Embedding != nil {
			t.Errorf("Load() = %+v, %v; want the file refused: where it is could not be decided", got, err)
		}
	})
}

// A key need not stand in the settings file: a table can name a file that
// holds the key and nothing else. The settings then hold no secret, and
// whatever keeps a learner's secrets writes one file for one key. The file
// is named, and is beside the settings file, in whichever folder that is.
func TestAKeyInAFileOfItsOwn(t *testing.T) {
	c := newComputer(t)
	beside := filepath.Join(c.config, "embedding.key")
	elsewhere := filepath.Join(c.config, "reranking.key")
	// As a program writes it, with a line's end, and as an editor does on
	// another system.
	for path, text := range map[string]string{beside: secret + "\n", elsewhere: "  " + secret + "-2\r\n"} {
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c.write(t, "format = 1\n[embedding]\nendpoint = \"http://127.0.0.1:8080/v1\"\nrecipe = \"embeddinggemma-2\"\n"+
		"key_file = \"embedding.key\"\n[reranking]\nendpoint = \"http://127.0.0.1:8081/v1\"\nmodel = \"m\"\nkey_file = \"reranking.key\"\n")
	got, err := c.load()
	if err != nil || got.Embedding == nil || got.Reranking == nil || got.Embedding.Key != secret || got.Reranking.Key != secret+"-2" {
		t.Fatalf("Load = %+v, %v; want each endpoint's key from its file", got, err)
	}
	// Without a key file the key is the one in the settings file, as ever.
	c.write(t, "format = 1\n[embedding]\nendpoint = \"http://127.0.0.1:8080/v1\"\nrecipe = \"embeddinggemma-2\"\nkey = \""+secret+"\"\n")
	if got, err := c.load(); err != nil || got.Embedding.Key != secret {
		t.Errorf("Load with a key in the settings file = %+v, %v", got, err)
	}
	// The settings file reached by a link, as a learner has it who keeps
	// their settings in one folder: the key file is beside the link, in the
	// configuration folder, and not where the link leads.
	kept := filepath.Join(c.home, "dotfiles", "shelf.toml")
	if err := os.MkdirAll(filepath.Dir(kept), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kept, []byte("format = 1\n[embedding]\nendpoint = \"http://127.0.0.1:8080/v1\"\nrecipe = \"embeddinggemma-2\"\n"+
		"key_file = \"embedding.key\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(c.config, settings.FileName)
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(kept, link); err != nil {
		t.Fatal(err)
	}
	if got, err := c.load(); err != nil || got.Embedding.Key != secret {
		t.Errorf("Load of settings behind a link = %+v, %v; want the key from the file beside the link", got, err)
	}
}

// A key file Shelf cannot use stops it, as a settings file does: the learner
// who named one would not know that no key is sent. The reason names the
// file and never holds what is in it.
func TestAKeyFileThatCannotBeUsedSaysWhy(t *testing.T) {
	table := "format = 1\n[embedding]\nendpoint = \"http://127.0.0.1:8080/v1\"\nrecipe = \"embeddinggemma-2\"\n"
	for name, tc := range map[string]struct {
		// file is what the key file holds, and "" for none; setting is what
		// the table says of the key.
		file, setting string
		want          []string
	}{
		"a key and a key file":       {secret, "key = \"" + secret + "\"\nkey_file = \"the.key\"\n", []string{"has both a [embedding] key and a [embedding] key_file", "not both"}},
		"a key file that is no text": {secret, "key_file = 7\n", []string{"[embedding] key_file that is not text"}},
		"a key file without a name":  {secret, "key_file = \"  \"\n", []string{"[embedding] key_file that names no file"}},
		"a home folder by its sign":  {secret, "key_file = \"~/the.key\"\n", []string{"is not the name of a file beside the settings file"}},
		"a path below the folder":    {secret, "key_file = \"keys/the.key\"\n", []string{"is not the name of a file beside the settings file"}},
		"a path out of the folder":   {secret, "key_file = \"../the.key\"\n", []string{"is not the name of a file beside the settings file", "link it there"}},
		"a whole path":               {secret, "key_file = \"/etc/hostname\"\n", []string{"is not the name of a file beside the settings file"}},
		"the folder above":           {secret, "key_file = \"..\"\n", []string{"is not the name of a file beside the settings file"}},
		// A name is written into what Shelf says, on a terminal.
		"a name of two lines":          {"", "key_file = \"the\\nkey\"\n", []string{"is not the name of a file beside the settings file"}},
		"a name a terminal obeys":      {"", "key_file = \"the\\u001b[2Jkey\"\n", []string{"is not the name of a file beside the settings file"}},
		"a file that is not there":     {"", "key_file = \"the.key\"\n", []string{"names a key file for [embedding], ", "the.key, that is not there"}},
		"an empty file":                {"\n", "key_file = \"the.key\"\n", []string{"the.key, that is empty"}},
		"two words":                    {secret + " " + secret, "key_file = \"the.key\"\n", []string{"holds more than a key", "one word"}},
		"two lines":                    {secret + "\n" + secret + "\n", "key_file = \"the.key\"\n", []string{"holds more than a key"}},
		"what a terminal obeys":        {secret + "\x1b[2J", "key_file = \"the.key\"\n", []string{"holds more than a key"}},
		"bytes that are no text":       {secret + "\xff\xfe", "key_file = \"the.key\"\n", []string{"holds more than a key"}},
		"more than any key":            {strings.Repeat(secret, 200), "key_file = \"the.key\"\n", []string{"is larger than 4096 bytes"}},
		"a key no request can carry":   {secret + "\u00e9", "key_file = \"the.key\"\n", []string{"names a key file for [embedding] that Shelf cannot use", "one word of printable characters"}},
		"the folder itself":            {"", "key_file = \".\"\n", []string{"is not the name of a file beside the settings file"}},
		"a key file for the reranker":  {"", "[reranking]\nendpoint = \"http://127.0.0.1:8081/v1\"\nmodel = \"m\"\nkey_file = \"other.key\"\n", []string{"names a key file for [reranking], ", "other.key, that is not there"}},
		"a setting of another's table": {secret, "passage_size = 900\n[reranking]\nendpoint = \"http://127.0.0.1:8081/v1\"\nkey = \"" + secret + "\"\nkey_file = \"the.key\"\n", []string{"has both a [reranking] key and a [reranking] key_file"}},
	} {
		t.Run(name, func(t *testing.T) {
			c := newComputer(t)
			if tc.file != "" {
				if err := os.WriteFile(filepath.Join(c.config, "the.key"), []byte(tc.file), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			path := c.write(t, table+tc.setting)
			_, err := c.load()
			var refused *settings.Error
			if !errors.As(err, &refused) || refused.Path != path {
				t.Fatalf("Load = %v; want the settings file refused", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the reason does not say %q: %v", want, err)
				}
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "sk-") {
				t.Errorf("the reason holds the key: %v", err)
			}
			if strings.ContainsFunc(err.Error(), func(r rune) bool { return r < ' ' || r == 0x7f }) {
				t.Errorf("the reason holds what a terminal obeys, or a line's end: %q", err)
			}
		})
	}
}

// A key file is as much of a setting as the settings file, and is held to
// what that is held to: no second name, and nowhere in the folders that hold
// data, which the learner may have let their agent's sandbox write in.
func TestAKeyFileIsAFileOfItsOwnOutsideTheIndexFolders(t *testing.T) {
	text := func(keyFile string) string {
		return "format = 1\n[embedding]\nendpoint = \"http://127.0.0.1:8080/v1\"\nrecipe = \"embeddinggemma-2\"\nkey_file = \"" + keyFile + "\"\n"
	}
	refused := func(t *testing.T, c computer, want string) {
		t.Helper()
		_, err := c.load()
		var why *settings.Error
		if !errors.As(err, &why) || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), secret) {
			t.Errorf("Load = %v; want the key file refused, since it %s", err, want)
		}
	}
	key := []byte(secret + "\n")
	t.Run("by a link to a file in the folder of the plugins' folders", func(t *testing.T) {
		c := newComputer(t)
		inside := filepath.Join(filepath.Dir(c.data), "the.key")
		if err := os.WriteFile(inside, key, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(inside, filepath.Join(c.config, "the.key")); err != nil {
			t.Fatal(err)
		}
		c.write(t, text("the.key"))
		refused(t, c, "is inside the folder Shelf keeps its index in")
	})
	t.Run("by a link to a file in the folder Shelf keeps its index in", func(t *testing.T) {
		c := newComputer(t)
		if err := os.WriteFile(filepath.Join(c.data, "the.key"), key, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(c.data, "the.key"), filepath.Join(c.config, "the.key")); err != nil {
			t.Fatal(err)
		}
		c.write(t, text("the.key"))
		refused(t, c, "is inside the folder Shelf keeps its index in")
	})
	t.Run("with a second name", func(t *testing.T) {
		c := newComputer(t)
		beside := filepath.Join(c.config, "the.key")
		if err := os.WriteFile(beside, key, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(beside, filepath.Join(c.home, "another-name")); err != nil {
			t.Skipf("no hard links here: %v", err)
		}
		c.write(t, text("the.key"))
		refused(t, c, "has a second name elsewhere (a hard link)")
	})
	// A learner who keeps the key elsewhere links it into the folder: that
	// is their own doing, and the file a link of theirs leads to is read.
	t.Run("by a link to a file elsewhere, which is the learner's own", func(t *testing.T) {
		c := newComputer(t)
		kept := filepath.Join(c.home, "secrets", "the.key")
		if err := os.MkdirAll(filepath.Dir(kept), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(kept, key, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(kept, filepath.Join(c.config, "the.key")); err != nil {
			t.Fatal(err)
		}
		c.write(t, text("the.key"))
		if got, err := c.load(); err != nil || got.Embedding.Key != secret {
			t.Errorf("Load = %+v, %v; want the key from the file the link leads to", got, err)
		}
		// Named by its path, the same file is refused: settings that
		// could name any file would have Shelf read it, and send it.
		c.write(t, text(kept))
		refused(t, c, "is not the name of a file beside the settings file")
	})
}
