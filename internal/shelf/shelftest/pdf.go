// Package shelftest is what tests of Shelf share: PDFs written for a test,
// and the study-shelf program, built once for a test binary.
//
// No test of Shelf reads a real book. A PDF here is a few objects written
// out by hand: pages of plain text in Helvetica, which every reader knows
// without a font file, and as much of the standard encryption as a test
// needs to meet a file that asks for a password.
//
// Nothing but tests imports this package.
package shelftest

import (
	"bytes"
	"crypto/md5"
	"crypto/rc4"
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"testing"
)

// PDF describes a PDF to write.
type PDF struct {
	// Pages are the pages, each a list of paragraphs. A page with no
	// paragraphs is empty.
	Pages [][]string
	// Pictures draws each page as a grey rectangle and no text: what a
	// scanned book is to a program that reads text.
	Pictures bool
	// Password, when set, is the password the file cannot be opened without.
	Password string
	// Restricted encrypts the file under an owner's password alone: it opens
	// without one, as most protected books a learner has bought do.
	Restricted bool
}

// lineWidth is how many characters a line of a page holds, at most.
const lineWidth = 88

// WritePDF writes, at path, a PDF that holds the pages given, each a list of
// paragraphs. It is a plugintest.Target's WriteFile.
func WritePDF(t *testing.T, path string, pages [][]string) {
	t.Helper()
	if err := os.WriteFile(path, PDF{Pages: pages}.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Bytes returns the file.
func (p PDF) Bytes() []byte {
	// Objects 1 to 3 are the catalog, the page tree and the font. Each page
	// is two objects after them, the page and its content, and the
	// encryption dictionary comes last.
	objects := make([][]byte, 3+2*len(p.Pages))
	var kids []string
	for i := range p.Pages {
		kids = append(kids, fmt.Sprintf("%d 0 R", 4+2*i))
	}
	objects[0] = []byte("<< /Type /Catalog /Pages 2 0 R >>")
	objects[1] = fmt.Appendf(nil, "<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(p.Pages))
	objects[2] = []byte("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>")

	id := md5.Sum(fmt.Append(nil, p.Pages, p.Pictures, p.Password, p.Restricted))
	var key []byte
	trailer := ""
	if p.Password != "" || p.Restricted {
		owner, user := "an owner's password", p.Password
		var encrypt []byte
		key, encrypt = encryption(owner, user, id[:])
		objects = append(objects, encrypt)
		trailer = fmt.Sprintf(" /Encrypt %d 0 R", len(objects))
	}
	for i, paragraphs := range p.Pages {
		content := []byte(text(paragraphs))
		if p.Pictures {
			content = []byte("0.6 g 72 72 468 648 re f\n")
		}
		number := 5 + 2*i
		if key != nil {
			content = encrypted(key, number, content)
		}
		objects[3+2*i] = fmt.Appendf(nil, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] "+
			"/Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", number)
		objects[4+2*i] = append(append(fmt.Appendf(nil, "<< /Length %d >>\nstream\n", len(content)), content...), "\nendstream"...)
	}

	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n%\xE2\xE3\xCF\xD3\n")
	offsets := make([]int, len(objects))
	for i, object := range objects {
		offsets[i] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R%s /ID [<%x> <%x>] >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, trailer, id, id, xref)
	return out.Bytes()
}

// text is the content of a page that shows paragraphs: lines of at most
// lineWidth characters, set small enough for all of them to be on the page.
func text(paragraphs []string) string {
	var lines []string
	for i, paragraph := range paragraphs {
		if i > 0 {
			lines = append(lines, "")
		}
		line := ""
		for _, word := range strings.Fields(paragraph) {
			if line != "" && len(line)+1+len(word) > lineWidth {
				lines = append(lines, line)
				line = ""
			}
			if line != "" {
				line += " "
			}
			line += word
		}
		lines = append(lines, line)
	}
	size := 10.0
	if len(lines) > 54 {
		size = 540.0 / float64(len(lines))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "BT\n/F1 %.2f Tf\n%.2f TL\n72 730 Td\n", size, size*1.2)
	for _, line := range lines {
		if line != "" {
			fmt.Fprintf(&b, "(%s) Tj\n", escaped(line))
		}
		b.WriteString("T*\n")
	}
	b.WriteString("ET\n")
	return b.String()
}

// escaped writes a line as a PDF string in the page's encoding, which holds
// the characters of Latin-1. Any other becomes a question mark.
func escaped(line string) string {
	var b strings.Builder
	for _, r := range line {
		switch {
		case r == '(' || r == ')' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r > 0xFF:
			b.WriteByte('?')
		case r > 0x7E:
			fmt.Fprintf(&b, "\\%03o", r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// padding is what the standard security handler fills a password up with.
var padding = []byte{
	0x28, 0xBF, 0x4E, 0x5E, 0x4E, 0x75, 0x8A, 0x41, 0x64, 0x00, 0x4E, 0x56, 0xFF, 0xFA, 0x01, 0x08,
	0x2E, 0x2E, 0x00, 0xB6, 0xD0, 0x68, 0x3E, 0x80, 0x2F, 0x0C, 0xA9, 0xFE, 0x64, 0x53, 0x69, 0x7A,
}

func padded(password string) []byte {
	return append([]byte(password), padding...)[:32]
}

func rc4With(key, data []byte) []byte {
	cipher, err := rc4.NewCipher(key)
	if err != nil {
		panic(err)
	}
	out := make([]byte, len(data))
	cipher.XORKeyStream(out, data)
	return out
}

// encryption returns the key a file is encrypted with and its encryption
// dictionary: the standard security handler of PDF 1.4, revision 2, with a
// key of 40 bits. It is the oldest and simplest there is, which every reader
// still opens, and the few lines of it are algorithms 3.2 to 3.4 of the PDF
// reference.
func encryption(owner, user string, id []byte) (key, dictionary []byte) {
	const permissions = int32(-44) // everything but changing the file
	ownerKey := md5.Sum(padded(owner))
	o := rc4With(ownerKey[:5], padded(user))
	h := md5.New()
	h.Write(padded(user))
	h.Write(o)
	_ = binary.Write(h, binary.LittleEndian, permissions)
	h.Write(id)
	key = h.Sum(nil)[:5]
	u := rc4With(key, padding)
	return key, fmt.Appendf(nil, "<< /Filter /Standard /V 1 /R 2 /O <%x> /U <%x> /P %d >>", o, u, permissions)
}

// encrypted encrypts the content of the object with that number.
func encrypted(key []byte, number int, content []byte) []byte {
	h := md5.New()
	h.Write(key)
	h.Write([]byte{byte(number), byte(number >> 8), byte(number >> 16), 0, 0})
	return rc4With(h.Sum(nil)[:len(key)+5], content)
}
