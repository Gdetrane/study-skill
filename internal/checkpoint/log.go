package checkpoint

import (
	"context"
	"strconv"
	"strings"
)

// Commit is one commit of a repository's history.
type Commit struct {
	Hash    string
	Subject string
}

// History is a repository's HEAD and the commits reachable from it.
type History struct {
	// Head is HEAD's commit; "" when the branch has no commit yet.
	Head string
	// Commits are the newest first, at most the limit asked for.
	Commits []Commit
}

// Log reads HEAD and the subjects of at most limit commits reachable from
// it, newest first, by the same rules as every other call here: no program
// the repository's configuration names is run (signatures are not checked,
// no pager, no hooks), and nothing is written to .git.
func Log(ctx context.Context, dir string, limit int) (History, error) {
	r, err := open(ctx, dir)
	if err != nil {
		return History{}, err
	}
	defer r.close()
	head, _, err := r.head(ctx)
	if err != nil || head == "" {
		return History{Head: head, Commits: []Commit{}}, err
	}
	if limit <= 0 {
		limit = 1
	}
	out, err := r.git(ctx, call{readOnly: true}, "log", "--no-show-signature", "--no-color", "--no-mailmap",
		"-z", "--format=%H%x00%s", "-n", strconv.Itoa(limit), head)
	if err != nil {
		return History{}, err
	}
	h := History{Head: head, Commits: []Commit{}}
	fields := splitNUL(out)
	for i := 0; i+1 < len(fields); i += 2 {
		h.Commits = append(h.Commits, Commit{Hash: strings.TrimSpace(fields[i]), Subject: fields[i+1]})
	}
	return h, nil
}
