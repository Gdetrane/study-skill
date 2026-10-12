// Package pdf reads the text of a PDF, page by page.
//
// The reader is PDFium, compiled to WebAssembly and run inside this program
// by wazero: nothing has to be installed beside study-shelf, the same file
// gives the same text on every computer, and a file is read by code that
// can reach no file, no network and no more memory than it is given. The
// prototype that chose it over pdftotext is recorded in ADR-0012.
//
// A book is someone else's file. Whatever is wrong with it, a page that does
// not parse or a font that maps its letters to nothing, is an error here or
// a page without text, and never a crash of Shelf.
package pdf

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/klippa-app/go-pdfium"
	pdfiumerrors "github.com/klippa-app/go-pdfium/errors"
	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
)

// What can be wrong with a file. The messages are for whoever reads Shelf's
// own diagnostics; Shelf says each to the learner in its own words.
var (
	// ErrNotPDF: the file does not begin as a PDF does.
	ErrNotPDF = errors.New("the file is not a PDF")
	// ErrProtected: the file cannot be opened without a password.
	ErrProtected = errors.New("the PDF is protected by a password")
	// ErrDamaged: the file is a PDF that cannot be read: it is cut short, or
	// what it holds is not what a PDF holds.
	ErrDamaged = errors.New("the PDF is damaged")
	// ErrTooMuch: the file holds more text than Shelf takes from one file.
	ErrTooMuch = errors.New("the PDF holds too much text")
)

const (
	// memoryPages is the most memory PDFium can have for one file, in
	// WebAssembly pages of 64 KiB: 1 GiB. The largest of the books the
	// prototype read, 131 MB with a drawing on most pages, was read within a
	// quarter of it.
	memoryPages = 16384
	// MostText is the most text one file may hold, in bytes of UTF-8: some
	// twenty-five books of a thousand pages. More is no book, and would all
	// be held in memory before it is cut into Passages.
	MostText = 64 << 20
	// headerWithin is how far into a file its "%PDF-" may stand: PDFium
	// looks that far for it.
	headerWithin = 1024
)

// engine is PDFium, compiled for this computer when the first file is read,
// which takes a few seconds, and kept for as long as the program runs: a
// Shelf that only searches never pays for it. It gives out one instance at a
// time. PDFium's memory is the largest thing Shelf holds, and two books at
// once would hold it twice.
var engine struct {
	once sync.Once
	pool pdfium.Pool
	err  error
}

// compiled returns PDFium, compiling it first when no file was read yet.
func compiled() (pdfium.Pool, error) {
	engine.once.Do(func() {
		engine.pool, engine.err = webassembly.Init(webassembly.Config{
			MinIdle: 0, MaxIdle: 0, MaxTotal: 1,
			// PDFium is given no folder: it reads the one file through the
			// function Read hands it, and can open nothing by a name.
			FSConfig: wazero.NewFSConfig(),
			// Standard output carries MCP and nothing else (the contract),
			// and nothing PDFium prints is for the learner.
			Stdout: io.Discard,
			Stderr: io.Discard,
			RuntimeConfig: wazero.NewRuntimeConfig().
				// PDFium's module needs exception handling, which it leaves
				// setjmp and longjmp to.
				WithCoreFeatures(api.CoreFeaturesV2 | experimental.CoreFeaturesExceptionHandling).
				WithMemoryLimitPages(memoryPages).
				// So that a file that is being read can be stopped.
				WithCloseOnContextDone(true),
		})
		if engine.err != nil {
			engine.err = fmt.Errorf("the PDF reader could not be started: %w", engine.err)
		}
	})
	return engine.pool, engine.err
}

// Read reads the PDF in file, which holds size bytes, and calls page for
// each of its pages in order: the page's number from 1, how many pages the
// file has, and the page's text as PDFium extracted it, which is empty for a
// page that shows no text. An error from page stops the reading and is
// returned.
//
// Read stops when ctx is done, in the middle of a page too, and returns
// ctx's error. One file is read at a time: a second Read waits for the first
// to end, or for its own ctx.
func Read(ctx context.Context, file io.ReadSeeker, size int64, page func(number, of int, text string) error) (err error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("reading the file: %w", err)
	}
	head := make([]byte, headerWithin)
	n, err := io.ReadFull(file, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return fmt.Errorf("reading the file: %w", err)
	}
	if !hasHeader(head[:n]) {
		return ErrNotPDF
	}

	// Compiling PDFium cannot be stopped, so it is not waited for by a
	// reading that is: it goes on for the next file.
	ready := make(chan struct{})
	go func() {
		defer close(ready)
		_, _ = compiled()
	}()
	select {
	case <-ready:
	case <-ctx.Done():
		return ctx.Err()
	}
	pool, err := compiled()
	if err != nil {
		return err
	}
	// This waits for the file that is being read, if one is.
	instance, err := pool.GetInstanceWithContext(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("the PDF reader could not be started: %w", err)
	}
	// PDFium runs until it is done with what it was asked. Killing the
	// instance is what stops it in the middle of a page.
	finished, watched := make(chan struct{}), make(chan struct{})
	killed := false
	go func() {
		defer close(watched)
		select {
		case <-ctx.Done():
			killed = true
			_ = instance.Kill()
		case <-finished:
		}
	}()
	defer func() {
		close(finished)
		<-watched
		if !killed {
			_ = instance.Close()
		}
		// Whatever went wrong while the file was being stopped is the
		// stopping, and nothing about the file. A panic in the reader is an
		// error like any other: no file takes Shelf down.
		if p := recover(); p != nil {
			err = fmt.Errorf("%w: the reader gave up (%v)", ErrDamaged, p)
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		}
	}()

	source := &source{ReadSeeker: file}
	doc, err := instance.OpenDocument(&requests.OpenDocument{FileReader: source, FileReaderSize: size})
	switch {
	case err == nil:
	case source.err != nil:
		return fmt.Errorf("reading the file: %w", source.err)
	case errors.Is(err, pdfiumerrors.ErrPassword):
		return ErrProtected
	case errors.Is(err, pdfiumerrors.ErrSecurity):
		return fmt.Errorf("%w, in a way this reader does not know", ErrProtected)
	default:
		return fmt.Errorf("%w: %v", ErrDamaged, err)
	}
	defer func() {
		if ctx.Err() == nil {
			_, _ = instance.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc.Document})
		}
	}()
	pages := func() (int, error) {
		count, err := instance.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: doc.Document})
		if err != nil {
			return 0, err
		}
		return count.PageCount, nil
	}
	count, err := pages()
	if err != nil || count < 1 {
		return fmt.Errorf("%w: it has no pages", ErrDamaged)
	}

	unreadable, total := 0, 0
	for i := range count {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		text, err := pageText(instance, doc.Document, i)
		if err != nil {
			if source.err != nil {
				return fmt.Errorf("reading the file: %w", source.err)
			}
			// A page PDFium cannot make out is a page without text, as long
			// as PDFium itself still answers: one broken page does not cost
			// the learner the book. When it no longer answers, the pages
			// after this one would all be lost without a word.
			if _, alive := pages(); alive != nil {
				return fmt.Errorf("%w: the reader gave up on page %d of %d", ErrDamaged, i+1, count)
			}
			unreadable++
			text = ""
		}
		if total += len(text); total > MostText {
			return ErrTooMuch
		}
		if err := page(i+1, count, text); err != nil {
			return err
		}
	}
	if unreadable == count {
		return fmt.Errorf("%w: none of its %d pages can be read", ErrDamaged, count)
	}
	return nil
}

// HasHeader reports whether file begins as a PDF does. It is how a caller
// tells a file that is no PDF from one that is, before it does anything
// that costs as much as the file is long.
func HasHeader(file io.ReaderAt) (bool, error) {
	head := make([]byte, headerWithin)
	n, err := file.ReadAt(head, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	return hasHeader(head[:n]), nil
}

func hasHeader(head []byte) bool { return bytes.Contains(head, []byte("%PDF-")) }

// pageText returns the text of the page at index, counted from 0.
func pageText(instance pdfium.Pdfium, doc references.FPDF_DOCUMENT, index int) (string, error) {
	loaded, err := instance.FPDF_LoadPage(&requests.FPDF_LoadPage{Document: doc, Index: index})
	if err != nil {
		return "", err
	}
	defer func() { _, _ = instance.FPDF_ClosePage(&requests.FPDF_ClosePage{Page: loaded.Page}) }()
	text, err := instance.FPDFText_LoadPage(&requests.FPDFText_LoadPage{Page: requests.Page{ByReference: &loaded.Page}})
	if err != nil {
		return "", err
	}
	defer func() { _, _ = instance.FPDFText_ClosePage(&requests.FPDFText_ClosePage{TextPage: text.TextPage}) }()
	chars, err := instance.FPDFText_CountChars(&requests.FPDFText_CountChars{TextPage: text.TextPage})
	if err != nil || chars.Count <= 0 {
		return "", err
	}
	// All of the page in one call: asked for one character at a time, a book
	// is two million calls into WebAssembly.
	got, err := instance.FPDFText_GetText(&requests.FPDFText_GetText{TextPage: text.TextPage, StartIndex: 0, Count: chars.Count})
	if err != nil {
		return "", err
	}
	return got.Text, nil
}

// source is the file PDFium reads from. It remembers the first error of the
// file itself, which PDFium would report as a file that is no PDF.
type source struct {
	io.ReadSeeker
	err error
}

func (s *source) Read(p []byte) (int, error) {
	n, err := s.ReadSeeker.Read(p)
	if err != nil && !errors.Is(err, io.EOF) && s.err == nil {
		s.err = err
	}
	return n, err
}

func (s *source) Seek(offset int64, whence int) (int64, error) {
	at, err := s.ReadSeeker.Seek(offset, whence)
	if err != nil && s.err == nil {
		s.err = err
	}
	return at, err
}
