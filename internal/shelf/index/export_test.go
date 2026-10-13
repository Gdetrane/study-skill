package index

// What the tests of this package need of its inside.

// FormatOneSchema is what a collection's file held in Format 1, and
// ApplicationID what marks a file as an index of Shelf's: for a test that
// makes a file as a Shelf from before vectors left one.
var (
	FormatOneSchema = schema
	ApplicationID   = applicationID
)

// SetRunLength sets how many Passages' vectors one row holds, so that a
// test's few Passages are several runs, and returns what puts it back.
func SetRunLength(n int) (restore func()) {
	was := runLength
	runLength = n
	return func() { runLength = was }
}
