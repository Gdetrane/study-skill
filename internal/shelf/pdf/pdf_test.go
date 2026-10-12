package pdf_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mordor-forge/lamplight/v2/internal/shelf/pdf"
	"github.com/mordor-forge/lamplight/v2/internal/shelf/shelftest"
)

// read reads a PDF and returns the text of its pages.
func read(t *testing.T, ctx context.Context, file []byte) ([]string, error) {
	t.Helper()
	var pages []string
	err := pdf.Read(ctx, bytes.NewReader(file), int64(len(file)), func(number, of int, text string) error {
		if number != len(pages)+1 {
			t.Errorf("page %d of %d came after page %d", number, of, len(pages))
		}
		pages = append(pages, text)
		return nil
	})
	return pages, err
}

func words(text string) string { return strings.Join(strings.Fields(text), " ") }

func TestAPDFIsReadPageByPage(t *testing.T) {
	book := shelftest.PDF{Pages: [][]string{
		{"The first page opens the book.", "Its second paragraph is about elimination, which produces a triangular system."},
		{},
		{"The third page follows a page that is empty (and has brackets \\ too)."},
	}}
	pages, err := read(t, context.Background(), book.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 3 {
		t.Fatalf("read %d pages, want 3: %q", len(pages), pages)
	}
	if got, want := words(pages[0]), "The first page opens the book. Its second paragraph is about elimination, which produces a triangular system."; got != want {
		t.Errorf("page 1 = %q, want %q", got, want)
	}
	if strings.TrimSpace(pages[1]) != "" {
		t.Errorf("page 2 = %q, want no text", pages[1])
	}
	if got, want := words(pages[2]), "The third page follows a page that is empty (and has brackets \\ too)."; got != want {
		t.Errorf("page 3 = %q, want %q", got, want)
	}
}

// A paragraph longer than a line is read with its words in order, whatever
// the lines it was set in.
func TestAPageOfManyLinesKeepsItsWordsInOrder(t *testing.T) {
	var sentences []string
	for n := range 60 {
		sentences = append(sentences, fmt.Sprintf("Sentence %d of a long paragraph says something of its own about row %d.", n+1, n*7))
	}
	paragraph := strings.Join(sentences, " ")
	pages, err := read(t, context.Background(), shelftest.PDF{Pages: [][]string{{paragraph}}}.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if got := words(pages[0]); got != paragraph {
		t.Errorf("the page reads\n%s\nwant\n%s", got, paragraph)
	}
}

func TestWhatIsWrongWithAFileIsSaid(t *testing.T) {
	good := shelftest.PDF{Pages: [][]string{{"A page of a book that is whole."}, {"And its second page."}}}.Bytes()
	for name, tc := range map[string]struct {
		file []byte
		want error
	}{
		"plain text":                  {[]byte("Elimination produces an upper triangular system.\n"), pdf.ErrNotPDF},
		"an empty file":               {nil, pdf.ErrNotPDF},
		"a picture":                   {[]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), pdf.ErrNotPDF},
		"a header and nothing else":   {[]byte("%PDF-1.7\n"), pdf.ErrDamaged},
		"a header before other bytes": {append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("not what a PDF holds\n"), 200)...), pdf.ErrDamaged},
		"a file cut short":            {good[:len(good)/3], pdf.ErrDamaged},
		"a password":                  {shelftest.PDF{Pages: [][]string{{"Nobody reads this without the password."}}, Password: "open sesame"}.Bytes(), pdf.ErrProtected},
	} {
		t.Run(name, func(t *testing.T) {
			pages, err := read(t, context.Background(), tc.file)
			if !errors.Is(err, tc.want) {
				t.Errorf("reading it gave %v and %d pages, want the error %v", err, len(pages), tc.want)
			}
		})
	}
}

// A book that is protected against changes, and opens without a password,
// is read like any other.
func TestAFileThatOpensWithoutItsPasswordIsRead(t *testing.T) {
	book := shelftest.PDF{Pages: [][]string{{"This book may be read and not changed."}}, Restricted: true}
	pages, err := read(t, context.Background(), book.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if got := words(strings.Join(pages, " ")); got != "This book may be read and not changed." {
		t.Errorf("the book reads %q", got)
	}
}

// A scanned book has pages and no text: every page is read, as empty.
func TestPagesThatArePicturesHaveNoText(t *testing.T) {
	pages, err := read(t, context.Background(), shelftest.PDF{Pages: [][]string{{"unseen"}, {"unseen"}}, Pictures: true}.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 || strings.TrimSpace(strings.Join(pages, "")) != "" {
		t.Errorf("read %q, want two pages without text", pages)
	}
}

func TestReadingStopsWhenItIsToldTo(t *testing.T) {
	var book shelftest.PDF
	for n := range 300 {
		book.Pages = append(book.Pages, []string{fmt.Sprintf("Page %d of a book that is long enough to be stopped halfway.", n+1)})
	}
	file := book.Bytes()
	// Once to its end, so that the reader is compiled before the clock
	// starts: that takes seconds, and is no part of stopping.
	if pages, err := read(t, context.Background(), file); err != nil || len(pages) != 300 {
		t.Fatalf("the book is not read: %v, %d pages", err, len(pages))
	}

	// Stopped by the caller, between two pages.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	seen := 0
	began := time.Now()
	err := pdf.Read(ctx, bytes.NewReader(file), int64(len(file)), func(number, of int, text string) error {
		if seen++; number == 20 {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) || seen > 21 {
		t.Errorf("a reading stopped at page 20 gave %v after %d pages, want it to stop there", err, seen)
	}
	if took := time.Since(began); took > 20*time.Second {
		t.Errorf("stopping took %s", took)
	}

	// By what the caller makes of a page.
	stop := errors.New("enough")
	if err := pdf.Read(context.Background(), bytes.NewReader(file), int64(len(file)), func(int, int, string) error { return stop }); !errors.Is(err, stop) {
		t.Errorf("a reading whose page failed gave %v, want that failure", err)
	}

	// Before it began.
	if pages, err := read(t, ctx, file); !errors.Is(err, context.Canceled) || len(pages) != 0 {
		t.Errorf("a reading that was stopped before it began gave %v and %d pages", err, len(pages))
	}
	// And the reader reads the next file as if nothing had happened.
	if pages, err := read(t, context.Background(), file); err != nil || len(pages) != 300 {
		t.Errorf("after a reading was stopped, the next gave %v and %d pages, want 300", err, len(pages))
	}
}

// One file is read at a time, and a reading that waits for its turn gives up
// when it is told to.
func TestASecondReadingWaitsForItsTurn(t *testing.T) {
	file := shelftest.PDF{Pages: [][]string{{"One page."}, {"Another."}}}.Bytes()
	inside, release := make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- pdf.Read(context.Background(), bytes.NewReader(file), int64(len(file)), func(number, _ int, _ string) error {
			if number == 1 {
				close(inside)
				<-release
			}
			return nil
		})
	}()
	<-inside
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := read(t, ctx, file); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a reading that could not have its turn gave %v, want it to give up when its time was over", err)
	}
	close(release)
	if err := <-first; err != nil {
		t.Errorf("the first reading: %v", err)
	}
}
