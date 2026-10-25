package csv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/source"
)

// The parallel path (step 144) must read every input exactly as the serial one
// does: the same rows, the same values, the same nulls, and the same error, naming
// the same row and line. These tests read each input both ways, with block sizes
// from one byte up, so that a block boundary falls everywhere it can.

func textOpener(s string) Opener {
	return func(context.Context) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(s)), nil }
}

// readAllRows reads src into one line per row, or stops at the first error.
func readAllRows(t *testing.T, src *Source, threads int) ([]string, error) {
	t.Helper()
	bs, err := src.Open(t.Context(), source.ScanSpec{Threads: threads, BatchSize: 5})
	if err != nil {
		return nil, err
	}
	defer bs.Close()
	var rows []string
	for {
		b, err := bs.Next(t.Context())
		if errors.Is(err, io.EOF) {
			return rows, nil
		}
		if err != nil {
			return rows, err
		}
		for i := range b.Rows() {
			var line []string
			for _, c := range b.Columns() {
				line = append(line, render(t, c, i))
			}
			rows = append(rows, strings.Join(line, "|"))
		}
	}
}

func render(t *testing.T, c *data.Column, i int) string {
	t.Helper()
	if !c.IsValid(i) {
		return "∅"
	}
	switch c.DType().ID() {
	case dtype.TypeString:
		s, _ := data.TypedColumn[string](c)
		v, _ := s.Get(i)
		return fmt.Sprintf("%q", v)
	case dtype.TypeInt64:
		s, _ := data.TypedColumn[int64](c)
		v, _ := s.Get(i)
		return fmt.Sprint(v)
	case dtype.TypeFloat64:
		s, _ := data.TypedColumn[float64](c)
		v, _ := s.Get(i)
		return fmt.Sprint(v)
	}
	t.Fatalf("render: %s", c.DType())
	return ""
}

var blockSizes = []int{1, 3, 17, 64, 1 << 20}

// sameBothWays reads opens with o serially and on four threads, at every block
// size, and requires the same rows and the same error.
func sameBothWays(t *testing.T, name string, opens []Opener, o Options) {
	t.Helper()
	serial, serr := readAllRows(t, NewMulti(opens, name, o), 1)
	defer func(size int) { splitBlockSize = size }(splitBlockSize)
	for _, size := range blockSizes {
		splitBlockSize = size
		par, perr := readAllRows(t, NewMulti(opens, name, o), 4)
		if fmt.Sprint(serr) != fmt.Sprint(perr) {
			t.Fatalf("%s, blocks of %d: the error differs\nserial:   %v\nparallel: %v", name, size, serr, perr)
		}
		if serr != nil {
			// Before an error, the parallel path delivers whole blocks, and the serial
			// one full batches: either can deliver more. Both must be the file's rows
			// from its start, so the shorter is a prefix of the longer.
			short, long := par, serial
			if len(short) > len(long) {
				short, long = long, short
			}
			if strings.Join(long[:len(short)], "\n") != strings.Join(short, "\n") {
				t.Fatalf("%s, blocks of %d: before the error, the rows delivered differ "+
					"(%d parallel, %d serial)", name, size, len(par), len(serial))
			}
			continue
		}
		if strings.Join(serial, "\n") != strings.Join(par, "\n") {
			for i := range max(len(serial), len(par)) {
				var s, p string
				if i < len(serial) {
					s = serial[i]
				}
				if i < len(par) {
					p = par[i]
				}
				if s != p {
					t.Fatalf("%s, blocks of %d: row %d differs\nserial:   %s\nparallel: %s\n(%d rows serial, %d parallel)",
						name, size, i, s, p, len(serial), len(par))
				}
			}
		}
	}
}

// allStrings is a schema of n String columns, so the values compared are the
// scanner's field text exactly.
func allStrings(names ...string) *dtype.Schema {
	fields := make([]dtype.Field, len(names))
	for i, n := range names {
		fields[i] = dtype.Of(n, dtype.String)
	}
	return dtype.MustSchema(fields...)
}

// randomCSV writes rows of three fields, each one of the shapes a block boundary
// could misread: quoted with a separator, a newline, a CRLF or a doubled quote in
// it, empty, quoted empty, a quote inside an unquoted field, and blank and comment
// lines between records.
func randomCSV(rng *rand.Rand, rows int, comments bool) string {
	var b strings.Builder
	b.WriteString("a,b,c\n")
	field := func() string {
		switch rng.IntN(11) {
		case 0:
			return ""
		case 1:
			return `""`
		case 2:
			return `"x,y"`
		case 3:
			return "\"line\nbreak\""
		case 4:
			return "\"crlf\r\ninside\""
		case 5:
			return `"say ""hi"""`
		case 6:
			return `ab"cd`
		case 7:
			return `a""b`
		case 8:
			return `"q"`
		default:
			return fmt.Sprintf("w%d", rng.IntN(1000))
		}
	}
	for i := range rows {
		if rng.IntN(13) == 0 {
			b.WriteString("\n")
		}
		if comments && rng.IntN(9) == 0 {
			b.WriteString(`# a comment with "quotes, and a ""doubled"" one` + "\n")
		}
		b.WriteString(field() + "," + field() + "," + field())
		if i < rows-1 || rng.IntN(2) == 0 {
			if rng.IntN(4) == 0 {
				b.WriteString("\r\n")
			} else {
				b.WriteString("\n")
			}
		}
	}
	return b.String()
}

func TestParallelReadsAsSerialDoes(t *testing.T) {
	rng := rand.New(rand.NewPCG(144, 1))
	o := DefaultOptions()
	o.Schema = allStrings("a", "b", "c")
	for i := range 40 {
		text := randomCSV(rng, 1+rng.IntN(60), false)
		sameBothWays(t, fmt.Sprintf("random %d", i), []Opener{textOpener(text)}, o)
	}

	withComments := o
	withComments.Comment = []byte("#")
	for i := range 20 {
		text := randomCSV(rng, 1+rng.IntN(60), true)
		sameBothWays(t, fmt.Sprintf("commented %d", i), []Opener{textOpener(text)}, withComments)
	}

	oneCol := DefaultOptions()
	oneCol.Schema = allStrings("a")
	sameBothWays(t, "one column, blank lines are nulls", []Opener{
		textOpener("a\nx\n\ny\n\n\nz\n"),
	}, oneCol)

	// A byte-order mark is a mark only at the start of a stream. A field that begins
	// with those three bytes, mid-file, is where a block can begin.
	bom := "\xef\xbb\xbf"
	sameBothWays(t, "a field that begins with a byte-order mark", []Opener{
		textOpener("a,b,c\n1,2,3\n" + bom + "x,y,z\n" + bom + "\"q\",r,s\n4,5,6\n"),
	}, o)

	// Since step 357 a blank line in a wider file is a record of nulls too, and a
	// short record is padded, as Polars reads them.
	sameBothWays(t, "three columns, blank lines and short records", []Opener{
		textOpener("a,b,c\n\n1,2,3\n\n\n4,5\r\n\r\n6\n\"x\n\ny\",7,8\n\n"),
	}, o)
	// Quoted line ends have the splitter cut blocks exactly, counting their rows, so
	// that the bad value's row is the blank lines' count too.
	ints := o
	ints.Schema = dtype.MustSchema(dtype.Of("a", dtype.Int64), dtype.Of("b", dtype.String))
	sameBothWays(t, "a bad value after blank lines, in two columns", []Opener{
		textOpener("a,b\n1,2\n\n\n3,4\n" + strings.Repeat("5,\"6\n7\"\n\n", 40) + "x,7\n8,9\n"),
	}, ints)
	read := o
	read.Schema = dtype.MustSchema(dtype.Of("a", dtype.Int64), dtype.Of("b", dtype.Float64), dtype.Of("c", dtype.String))
	read.IgnoreErrors, read.DecimalComma, read.Lossy, read.Separator = true, true, true, ';'
	sameBothWays(t, "ignore_errors, a decimal comma and lossy text", []Opener{
		textOpener("a;b;c\n1;1,5;x\nq;2,5;\xff\n3;3.5;\xe2\x82A\n4\n"),
	}, read)

	ragged := o
	ragged.TruncateRaggedLines = true
	sameBothWays(t, "ragged, truncated", []Opener{
		textOpener("a,b,c\n1,2\n3,4,5,6\n7,8,9\n\"x\ny\"\n"),
	}, ragged)

	projected := o
	projected.Schema = allStrings("a", "b", "c")
	sameBothWays(t, "several streams, one reordered, one empty", []Opener{
		textOpener("a,b,c\n1,\"2\n2\",3\n4,5,6\n"),
		textOpener("c,a,b\n9,7,8\n\"z\",x,y"),
		textOpener("a,b,c\n"),
		textOpener("a,b,c\n10,11,12\n"),
	}, projected)
}

// TestParallelMeasuresARecordAsSerialDoes: a record at MaxRecordSize, either side
// of it, with its line end and without, in the middle of the input and at its end.
// A block's scanner reads the block in place since step 188, with no buffer for the
// limit to bound, so it measures each record; the serial scanner's buffer fits a
// record and its line end, and a last record without one only if a byte is left.
func TestParallelMeasuresARecordAsSerialDoes(t *testing.T) {
	o := DefaultOptions()
	o.Schema = allStrings("a", "b")
	o.MaxRecordSize = 64
	for _, n := range []int{62, 63, 64, 65} {
		// "1," and the field: n bytes with the line end, n-1 without.
		rec := "1," + strings.Repeat("z", n-3)
		sameBothWays(t, fmt.Sprintf("%d bytes, mid-input", n), []Opener{
			textOpener("a,b\n2,y\n" + rec + "\n3,x\n"),
		}, o)
		sameBothWays(t, fmt.Sprintf("%d bytes, last, with its line end", n), []Opener{
			textOpener("a,b\n2,y\n" + rec + "\n"),
		}, o)
		sameBothWays(t, fmt.Sprintf("%d bytes, last, without", n-1), []Opener{
			textOpener("a,b\n2,y\n" + rec),
		}, o)
		sameBothWays(t, fmt.Sprintf("%d bytes, ending CRLF", n+1), []Opener{
			textOpener("a,b\r\n2,y\r\n" + rec + "\r\n3,x\r\n"),
		}, o)
		// And the limit itself, which the comparison cannot see if both paths move
		// it: 64 bytes with the line end are read, and 65 refused.
		for _, text := range []string{"a,b\n2,y\n" + rec + "\n3,x\n", "a,b\n2,y\n" + rec} {
			_, err := readAllRows(t, NewMulti([]Opener{textOpener(text)}, "limit", o), 4)
			if (err != nil) != (n > 64) {
				t.Errorf("a %d-byte record: error %v", n, err)
			}
		}
	}
}

// TestParallelErrorsAsSerialDoes: an input the serial path refuses is refused the
// same way, naming the same row and line, wherever the blocks fall.
func TestParallelErrorsAsSerialDoes(t *testing.T) {
	typed := DefaultOptions()
	typed.Schema = dtype.MustSchema(dtype.Of("a", dtype.Int64), dtype.Of("b", dtype.String))

	var good strings.Builder
	good.WriteString("a,b\n")
	for i := range 200 {
		fmt.Fprintf(&good, "%d,\"x\ny\"\n", i)
	}
	for _, c := range []struct {
		name string
		text string
		o    Options
	}{
		{"a bad value deep in", good.String() + "oops,z\n" + good.String()[4:], typed},
		{"a ragged row", good.String() + "1,2,3\n", typed},
		{"an unterminated quote", good.String() + "5,\"never closed\n6,x\n", typed},
		{"a character after a closing quote", good.String() + "5,\"ab\"c\n6,x\n", typed},
		{"a record past MaxRecordSize", good.String() + "5,\"" + strings.Repeat("z", 3000) + "\"\n",
			func() Options { o := typed; o.MaxRecordSize = 1024; return o }()},
		{"a bad value after comment lines holding quotes",
			"a,b\n1,x\n# skip, \"this\n2,y\n# and, \"\"this\"\"\n3,z\nbad,w\n4,v\n",
			func() Options { o := typed; o.Comment = []byte("#"); return o }()},
		{"a bad value after blank lines, in one column", "a\n1\n\n2\n\n\n3\nzz\n4\n",
			func() Options {
				o := typed
				o.Schema = dtype.MustSchema(dtype.Of("a", dtype.Int64))
				return o
			}()},
		{"a null in a non-nullable column", "a,b\n1,x\n,y\n",
			func() Options {
				o := typed
				o.Schema = dtype.MustSchema(dtype.NotNull("a", dtype.Int64), dtype.Of("b", dtype.String))
				return o
			}()},
	} {
		sameBothWays(t, c.name, []Opener{textOpener(c.text)}, c.o)
		if _, err := readAllRows(t, NewMulti([]Opener{textOpener(c.text)}, c.name, c.o), 4); err == nil {
			t.Errorf("%s: read without an error", c.name)
		}
	}
}

// TestTheSplitterCutsWellFormedQuotingWithoutStopping: quotes inside unquoted
// fields, doubled quotes, quoted newlines, and comment lines holding quotes, cut at
// every block size. The splitter stops only on what it will not follow, and handing
// a well-formed file to the serial path would cost its parallelism, silently: the
// answer would still be right, so only this test can see it.
func TestTheSplitterCutsWellFormedQuotingWithoutStopping(t *testing.T) {
	var text strings.Builder
	for i := range 50 {
		fmt.Fprintf(&text, "ab\"cd,\"say \"\"hi\"\"\",\"line\nbreak\",a\"\"b,%d\n", i)
		text.WriteString(`# a comment, "unclosed and "doubled"" quotes` + "\n")
	}
	defer func(size int) { splitBlockSize = size }(splitBlockSize)
	for _, size := range blockSizes {
		splitBlockSize = size
		sp := &splitter{r: strings.NewReader(text.String()), sep: ',', quote: '"',
			comment: []byte("#"), maxRec: 1 << 20, width: 5}
		rows := 0
		for {
			blk, ok, err := sp.next()
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				break
			}
			rows += blk.rows
		}
		if sp.stopped {
			t.Errorf("blocks of %d: the splitter stopped on well-formed input", size)
		}
		if rows != 50 {
			t.Errorf("blocks of %d: %d rows cut, want 50", size, rows)
		}
	}
}

// TestSpeculativeCutsHoldOnOrdinaryFiles: step 199 cuts blocks after the first line
// ending past their size, and a block that does not end cleanly sends the rest of
// the stream back to be cut exactly. That is correct either way; what this pins is
// that it is fast: a file of quoted fields holding no line ending is read without a
// single fallback, and one quoted line ending, past the first blocks, costs exactly
// one, and reads as the serial path does.
func TestSpeculativeCutsHoldOnOrdinaryFiles(t *testing.T) {
	o := DefaultOptions()
	o.Schema = allStrings("a", "b", "c")
	defer func(size int) { splitBlockSize = size }(splitBlockSize)
	splitBlockSize = 4 << 10
	var plain strings.Builder
	plain.WriteString("a,b,c\n")
	for i := range 20_000 {
		fmt.Fprintf(&plain, "\"id%03d\",%d,\"x, %d\"\n", i%997, i, i*7)
	}
	resumed := func(text string) int {
		t.Helper()
		bs, err := NewMulti([]Opener{textOpener(text)}, "spec", o).Open(t.Context(), source.ScanSpec{Threads: 4})
		if err != nil {
			t.Fatal(err)
		}
		defer bs.Close()
		r := bs.(*reader)
		n := 0
		for {
			b, err := bs.Next(t.Context())
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			n += b.Rows()
			if r.split != nil && r.split.resumed > 0 {
				return r.split.resumed
			}
		}
		return 0
	}
	if got := resumed(plain.String()); got != 0 {
		t.Errorf("a file of quoted fields with no line ending in one fell back %d times", got)
	}
	// A quoted line ending 300 KB in: one fallback, and the rows as the serial path's.
	text := plain.String()
	at := strings.Index(text[300<<10:], "\"x, ") + 300<<10
	odd := text[:at] + "\"x,\nwrapped\"" + text[at+len("\"x, "):]
	if got := resumed(odd); got != 1 {
		t.Errorf("a quoted line ending fell back %d times, want 1", got)
	}
	sameBothWays(t, "a quoted line ending past the first blocks", []Opener{textOpener(odd)}, o)
}
