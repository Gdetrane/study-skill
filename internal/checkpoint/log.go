package checkpoint

import (
	"context"
	"strconv"
	"strings"
)

// Commit is one commit of a repository's history.
type Commit struct {
	Hash string
	// Subject is the commit's subject in UTF-8, cut to SubjectColumns
	// columns, with ".." where it was cut.
	Subject string
}

// History is a repository's HEAD and the commits reachable from it.
type History struct {
	// Head is HEAD's commit; "" when the branch has no commit yet.
	Head string
	// Commits are the newest first, at most the limit asked for.
	Commits []Commit
	// Truncated says the output reached its size bound before the limit, so
	// older commits are missing.
	Truncated bool
}

// SubjectColumns is how much of each subject Log keeps.
const SubjectColumns = 200

// logRecordBytes bounds one record of Log's output: a hash (64 hex digits
// with SHA-256), a NUL, a subject of SubjectColumns columns at up to four
// bytes each, git's ".." and a NUL.
const logRecordBytes = 64 + 1 + 4*SubjectColumns + 2 + 1

// Log reads HEAD and the subjects of at most limit commits reachable from
// it, newest first, by the same rules as every other call here: no program
// the repository's configuration names is run (signatures are not checked,
// no pager, no hooks), and nothing is written to .git. Subjects come in
// UTF-8 whatever the repository's i18n settings, cut to SubjectColumns, and
// the whole output is bounded.
func Log(ctx context.Context, dir string, limit int) (History, error) {
	if limit <= 0 {
		limit = 1
	}
	return logWithin(ctx, dir, limit, limit*logRecordBytes)
}

func logWithin(ctx context.Context, dir string, limit, maxBytes int) (History, error) {
	r, err := open(ctx, dir)
	if err != nil {
		return History{}, err
	}
	defer r.close()
	head, _, err := r.head(ctx)
	if err != nil || head == "" {
		return History{Head: head, Commits: []Commit{}}, err
	}
	h := History{Head: head, Commits: []Commit{}}
	out, err := r.git(ctx, call{readOnly: true, maxOutput: maxBytes, truncated: &h.Truncated},
		"log", "--no-show-signature", "--no-color", "--no-mailmap", "--encoding=UTF-8", "-z",
		"--format=%H%x00%<("+strconv.Itoa(SubjectColumns)+",trunc)%s", "-n", strconv.Itoa(limit), head)
	if err != nil {
		return History{}, err
	}
	if h.Truncated {
		// The last record may be cut short: keep whole ones only.
		if i := strings.LastIndexByte(out, 0); i >= 0 {
			out = out[:i+1]
		}
	}
	fields := splitNUL(out)
	for i := 0; i+1 < len(fields); i += 2 {
		subject := strings.ToValidUTF8(strings.TrimRight(fields[i+1], " "), "�")
		h.Commits = append(h.Commits, Commit{Hash: strings.TrimSpace(fields[i]), Subject: subject})
	}
	return h, nil
}

// Tracked reports which of paths, relative to the top of the repository,
// HEAD's tree holds, as a file or a folder. Nothing is tracked when the
// branch has no commit yet. A listing too long to read whole counts every
// path as tracked, the cautious answer.
func Tracked(ctx context.Context, dir string, paths []string) (map[string]bool, error) {
	got := map[string]bool{}
	if len(paths) == 0 {
		return got, nil
	}
	r, err := open(ctx, dir)
	if err != nil {
		return nil, err
	}
	defer r.close()
	head, _, err := r.head(ctx)
	if err != nil || head == "" {
		return got, err
	}
	var truncated bool
	args := append([]string{"ls-tree", "-z", "--name-only", "--full-tree", head, "--"}, paths...)
	out, err := r.git(ctx, call{readOnly: true, maxOutput: 1 << 20, truncated: &truncated}, args...)
	if err != nil {
		return nil, err
	}
	for _, p := range paths {
		got[p] = truncated
	}
	for _, name := range splitNUL(out) {
		for _, p := range paths {
			if name == p || strings.HasPrefix(name, p+"/") {
				got[p] = true
			}
		}
	}
	return got, nil
}
