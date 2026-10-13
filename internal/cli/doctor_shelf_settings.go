package cli

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/mordor-forge/lamplight/v2/internal/core"
	"github.com/mordor-forge/lamplight/v2/internal/pluginregistry"
	"github.com/mordor-forge/lamplight/v2/internal/xdg"
)

// shelfSettingsName is the name of Shelf's settings file, in Lamplight's
// configuration folder. study reads nothing of the file, and links nothing
// of Shelf, so the name is written here a second time: an end-to-end test
// has the built Shelf read its settings through the file the built study
// warns of.
const shelfSettingsName = "shelf.toml"

// shelfSettingsPath is where Shelf looks for its settings in the
// environment getenv reads, and "" when that names no configuration folder.
func shelfSettingsPath(getenv func(string) string) string {
	base, err := xdg.ConfigHome.Dir(getenv)
	if err != nil {
		return ""
	}
	return filepath.Join(base, "lamplight", shelfSettingsName)
}

// diagnoseShelfSettings is the doctor's Finding about Shelf's settings file,
// when there is something to say of it: that it is in the Study home, or is
// reached through it, a link to a file there say.
//
// The file says where Shelf sends a book's text and with which key. Shelf
// refuses one in the folders it keeps its index in, and cannot refuse this:
// it does not know where the Study home is. study does, and whoever can
// write the Study home, an agent in its sandbox among them, would write
// Shelf's settings through such a file, while every finding of Shelf's own
// test says its endpoints are all right.
//
// It is decided as the registry decides it of a plugin's program, by file
// identity and one hop at a time (pluginregistry.Guard), and a path that
// could not be followed is said to be that, never taken for one outside: a
// file below a folder study may not look into among them. A file that is
// not there is nothing to say anything of: the link that is there and leads
// nowhere yet is.
func (a *app) diagnoseShelfSettings() (core.Finding, bool) {
	f := core.Finding{Name: "shelf_settings", Status: core.FindingWarn}
	path := shelfSettingsPath(a.opts.Getenv)
	if path == "" {
		return f, false
	}
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		// No such file. One that may be there and cannot be looked at is
		// followed below, and said to be that.
		return f, false
	}
	c, err := core.Open(a.opts)
	if err != nil {
		// The Study home cannot be found, and the doctor has said why.
		return f, false
	}
	guard := pluginregistry.NewGuard(c.PluginRegistryEnv())
	defer guard.Close()
	switch err := guard.Follow(path); {
	case err == nil:
		return f, false
	case pluginregistry.IsInside(err):
		f.Message = "Shelf's settings file, " + path + ", is in the Study home or is reached through it. Whoever can write " +
			"the Study home, your agent among them, can change there where Shelf sends a book's text, and with which key"
		f.Fix = "keep " + path + " as a file of its own, outside the Study home and with no link into it"
	default:
		f.Message = "study could not follow Shelf's settings file, " + path + ", to see that it stays outside the Study " +
			"home, where your agent could change it: " + err.Error()
	}
	return f, true
}
