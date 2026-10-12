package shelf

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/plugincontract"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/cut"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/index"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/pdf"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/web"
)

// job is one Source being indexed.
type job struct {
	// source is the Source as index was given it.
	source plugincontract.Source
	// ctx is done once the work is told to stop, which stop does.
	ctx  context.Context
	stop context.CancelFunc
	// done is closed when the work has ended, and stopped then says that it
	// ended because it was told to, with nothing of it in the index.
	done    chan struct{}
	stopped bool
	// done64 is how much of the work is done, from 0 to 1, as the bits of a
	// float64, once known is set: a file's pages are counted, and a web
	// page is fetched or not.
	done64 atomic.Uint64
	known  atomic.Bool
}

// indexes reports whether the job is indexing the content that is asked
// for: the same file content, or the same address.
func (j *job) indexes(asked plugincontract.Source) bool {
	if j.source.Kind != asked.Kind {
		return false
	}
	if asked.Kind == plugincontract.SourceURL {
		return j.source.URL == asked.URL
	}
	return j.source.Hash == asked.Hash
}

func (j *job) progress() (float64, bool) {
	return math.Float64frombits(j.done64.Load()), j.known.Load()
}

func (j *job) advance(to float64) {
	j.done64.Store(math.Float64bits(to))
	j.known.Store(true)
}

// problem is what is wrong with a Source, in words for the learner: the
// reason status gives for it.
type problem struct{ reason string }

func (p *problem) Error() string { return p.reason }

func wrong(format string, args ...any) error {
	return &problem{reason: fmt.Sprintf(format, args...)}
}

// work indexes one Source, after index has answered. Whatever it comes to
// is in the index before the job leaves the list of what is being indexed:
// the Source's new content, in the place of what was there, or the reason it
// could not be indexed, beside what was there. Told to stop, it leaves the
// index as it was.
func (s *Shelf) work(k key, in plugincontract.IndexInput, j *job) {
	ctx := j.ctx
	err := s.run(ctx, k, in, j)
	stopped := err != nil && ctx.Err() != nil
	unwritten := ""
	if err != nil && !stopped {
		reason := reasonOf(err)
		// Told to the index, where a copy of Shelf started later finds it:
		// the learner reads the reason after study source index has exited.
		// An index that cannot be told is why the Source failed, or will
		// say so itself; the reason is then kept here for as long as this
		// copy runs.
		told, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		c, cerr := s.collection(told, k.collection, true)
		if cerr == nil {
			cerr = c.Fail(told, index.Source{ID: k.source, Kind: in.Source.Kind, Title: in.Source.Title}, reason)
		}
		cancel()
		if cerr != nil {
			unwritten = reason
		}
	}
	s.mu.Lock()
	j.stopped = stopped
	if unwritten != "" {
		s.unwritten[k] = unwritten
	}
	if s.jobs[k] == j {
		delete(s.jobs, k)
	}
	s.mu.Unlock()
	j.stop()
	close(j.done)
}

// run reads a Source, cuts it into Passages and puts them in the index. It
// returns nil once the new content is there, and otherwise why it is not.
func (s *Shelf) run(ctx context.Context, k key, in plugincontract.IndexInput, j *job) error {
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return errStopped
	}
	c, err := s.collection(ctx, k.collection, true)
	if err != nil {
		return err
	}
	// Indexed again, the Source is no longer the one whose last indexing
	// failed, whatever becomes of this one.
	if err := c.Recover(ctx, k.source); err != nil {
		return err
	}

	var pages []cut.Page
	source := index.Source{ID: k.source, Kind: in.Source.Kind, Title: in.Source.Title}
	switch in.Source.Kind {
	case plugincontract.SourceFile:
		pages, source.Hash, err = s.readFile(ctx, in.Source.Path, j)
	case plugincontract.SourceURL:
		source.Address = in.Source.URL
		pages, source.Hash, err = s.fetchPage(ctx, in.Source.URL)
	}
	if err != nil {
		return err
	}
	var passages []index.Passage
	for _, p := range cut.Passages(pages, s.settings.PassageSize) {
		passages = append(passages, index.Passage{Text: p.Text, First: p.First, Last: p.Last})
	}
	if len(passages) == 0 {
		return wrong("there is no text in it")
	}
	if ctx.Err() != nil {
		return errStopped
	}
	// From here on nothing stops it: the write is one short transaction,
	// and a cancel that arrives now is told, truthfully, that it stopped
	// nothing.
	return c.Replace(context.WithoutCancel(ctx), source, passages)
}

// readFile reads the PDF at path and returns the text of its pages, cleaned,
// and the content hash of the bytes that were read.
func (s *Shelf) readFile(ctx context.Context, path string, j *job) ([]cut.Page, string, error) {
	// Opened without waiting: a path that names a pipe would otherwise hold
	// the work until someone wrote to it.
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, "", wrong("the file is not there: it was moved or removed since it was added")
	case errors.Is(err, fs.ErrPermission):
		return nil, "", wrong("the file cannot be read: Shelf is not allowed to open it")
	case err != nil:
		return nil, "", wrong("the file cannot be read: %v", cause(err))
	}
	defer file.Close()
	info, err := file.Stat()
	switch {
	case err != nil:
		return nil, "", wrong("the file cannot be read: %v", cause(err))
	case info.IsDir():
		return nil, "", wrong("it is a folder, not a file")
	case !info.Mode().IsRegular():
		return nil, "", wrong("it is not a file")
	}
	if ok, err := pdf.HasHeader(file); err != nil {
		return nil, "", wrong("the file cannot be read: %v", cause(err))
	} else if !ok {
		return nil, "", notPDF()
	}

	ctx, cancel := context.WithTimeout(ctx, s.settings.FileTime)
	defer cancel()
	slow := func(err error) error {
		// The file's own time is over, not the work's.
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() == context.DeadlineExceeded {
			return wrong("reading the file took longer than %s, the most Shelf gives one file", s.settings.FileTime)
		}
		return err
	}
	// The hash is of the bytes Shelf read, which may no longer be the ones
	// study hashed. It is taken before the pages are read and again after:
	// a file that changed in between is one whose pages belong to no hash.
	// The reader is given as many bytes as were hashed, and no other count.
	before, size, err := hashOf(ctx, file)
	if err != nil {
		return nil, "", slow(err)
	}
	var pages []cut.Page
	text := false
	err = pdf.Read(ctx, file, size, func(number, of int, raw string) error {
		page := cut.Page{Number: number, Text: cut.Clean(raw)}
		text = text || page.Text != ""
		pages = append(pages, page)
		// The last of the work is the writing of the index.
		j.advance(0.95 * float64(number) / float64(of))
		return nil
	})
	switch {
	case err == nil:
	case errors.Is(err, pdf.ErrNotPDF):
		return nil, "", notPDF()
	case errors.Is(err, pdf.ErrProtected):
		return nil, "", wrong("the file is protected by a password, and Shelf cannot open it. Save a copy without the password and add that")
	case errors.Is(err, pdf.ErrDamaged):
		return nil, "", wrong("the file is damaged: it is a PDF that cannot be read to its end. Download or export it again")
	case errors.Is(err, pdf.ErrTooMuch):
		return nil, "", wrong("the file holds more than %d MB of text, the most Shelf takes from one file", pdf.MostText>>20)
	default:
		return nil, "", slow(err)
	}
	after, _, err := hashOf(ctx, file)
	if err != nil {
		return nil, "", slow(err)
	}
	if after != before {
		return nil, "", wrong("the file changed while it was being read. Index it again")
	}
	if !text {
		return nil, "", wrong("the file has no text layer: its pages are pictures, as in a scanned book, and Shelf reads text. " +
			"Run it through a program that recognises the text (OCR), and add the result")
	}
	return pages, before, nil
}

func notPDF() error {
	return wrong("the file is not a PDF: Shelf reads PDF files and web pages")
}

// hashOf returns the content hash of a file, read from its start to its
// end, and how many bytes that was.
func hashOf(ctx context.Context, file *os.File) (hash string, size int64, err error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", 0, wrong("the file cannot be read: %v", cause(err))
	}
	sum := sha256.New()
	buffer := make([]byte, 1<<20)
	for {
		if ctx.Err() != nil {
			return "", 0, ctx.Err()
		}
		n, err := file.Read(buffer)
		sum.Write(buffer[:n])
		size += int64(n)
		if errors.Is(err, io.EOF) {
			return "sha256:" + hex.EncodeToString(sum.Sum(nil)), size, nil
		}
		if err != nil {
			return "", 0, wrong("the file cannot be read: %v", cause(err))
		}
	}
}

// fetchPage fetches a web page and returns its text, cleaned, as the one
// page a Source without pages is, and the content hash of what was fetched.
func (s *Shelf) fetchPage(ctx context.Context, address string) ([]cut.Page, string, error) {
	page, err := s.fetcher.Fetch(ctx, address)
	if err != nil {
		return nil, "", err
	}
	text := cut.Clean(page.Text)
	if text == "" {
		return nil, "", wrong("the page has no text to read")
	}
	return []cut.Page{{Text: text}}, page.Hash, nil
}

// cause is an operating system error without the path it names: the path
// is the learner's, and the reason is read beside the Source it is about.
func cause(err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
}

// reasonOf is why a Source could not be indexed, as status says it.
func reasonOf(err error) string {
	var p *problem
	var fetch *web.Problem
	reason := ""
	switch {
	case errors.As(err, &p):
		reason = p.reason
	case errors.As(err, &fetch):
		reason = fetch.Reason
	default:
		// Something of Shelf's own, the index most likely, which words its
		// errors for the learner.
		reason = cause(err).Error()
	}
	if reason = strings.TrimSpace(reason); reason == "" {
		reason = "the Source could not be indexed"
	}
	return plugincontract.Cut(reason, plugincontract.MaxTextBytes)
}
