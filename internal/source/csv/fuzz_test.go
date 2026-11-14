package csv

import (
	"bytes"
	"context"
	stdcsv "encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/fuzzcheck"
	_ "github.com/advenn/ursus/internal/fuzzcheck/invariants"
	"github.com/advenn/ursus/internal/source"
)

// The CSV reader's fuzz targets (step 509, steps 445 and 446 of
// context_files/v0.7.2-plan.md): its number parsers against strconv, its inference
// against its parsers, its scanner against encoding/csv, its vector field ends
// against a byte loop, and its parallel path against its serial one, each inside
// fuzzcheck's limits. Several set a package variable, splitBlockSize or
// vectorFields, so no test of this package runs in parallel with them.

// fuzzLimits bounds one call: ten seconds, and 64 MiB plus 1 KiB a byte of input, as
// readfuzz bounds a read.
func fuzzLimits(n int) fuzzcheck.Limits {
	return fuzzcheck.Limits{Wall: 10 * time.Second, Alloc: 64<<20 + 1024*int64(n)}
}

// sameNumError: both nil, or both errors of the same kind, ErrSyntax or ErrRange.
func sameNumError(a, b error) bool {
	return (a == nil) == (b == nil) && errors.Is(a, strconv.ErrRange) == errors.Is(b, strconv.ErrRange) &&
		errors.Is(a, strconv.ErrSyntax) == errors.Is(b, strconv.ErrSyntax)
}

// FuzzParseNumbers: parseInt and parseUint, at each width, are strconv.ParseInt's
// and strconv.ParseUint's value and error kind.
func FuzzParseNumbers(f *testing.F) {
	for _, s := range []string{
		"0", "-0", "+0", "7", "-7", "+7", "127", "128", "-128", "-129", "255", "256", "32767", "-32769",
		"2147483647", "2147483648", "4294967295", "4294967296", "999999999999999999", "1000000000000000000",
		"9223372036854775807", "9223372036854775808", "-9223372036854775808", "-9223372036854775809",
		"18446744073709551615", "18446744073709551616", "0000000000000000000000001", "", "+", "-", "1_000",
		"0x10", " 1", "1 ", "1.0", "1e3", "١", "-+1", "--1", "99999999999999999999x",
	} {
		for bits := range uint8(4) {
			f.Add([]byte(s), bits)
		}
	}
	f.Fuzz(func(t *testing.T, b []byte, bits uint8) {
		w := []int{8, 16, 32, 64}[bits%4]
		fuzzcheck.Run(t, fuzzLimits(len(b)), func() error {
			v, err := parseInt(b, w)
			rv, rerr := strconv.ParseInt(string(b), 10, w)
			if v != rv || !sameNumError(err, rerr) {
				t.Fatalf("parseInt(%q, %d) = %d, %v; strconv: %d, %v", b, w, v, err, rv, rerr)
			}
			u, err := parseUint(b, w)
			ru, rerr := strconv.ParseUint(string(b), 10, w)
			if u != ru || !sameNumError(err, rerr) {
				t.Fatalf("parseUint(%q, %d) = %d, %v; strconv: %d, %v", b, w, u, err, ru, rerr)
			}
			return nil
		})
	})
}

// widensTo is every type a column of k's fields can be read as: k's own, and, for a
// number, the types the inference lattice widens it to beside another number. A word,
// inf or nan, is a float beside a number.
func widensTo(k inferred) []dtype.DataType {
	switch k {
	case infInt:
		return []dtype.DataType{dtype.Int64, dtype.Int128, dtype.Float64}
	case infInt128:
		return []dtype.DataType{dtype.Int128, dtype.Float64}
	case infWord:
		return []dtype.DataType{dtype.Float64}
	case infString, infUnknown:
		return nil
	}
	return []dtype.DataType{k.dtype()}
}

// FuzzClassifyAgreesWithParse: a field classify calls a kind, the builder of each
// type that kind can be read as reads, as the inference lattice promises (infer.go);
// text it calls a String is read as one unless it is not UTF-8 and the read is not
// lossy; and a field the Bool, Int64, Int128 or Float64 builder reads, classify calls
// that kind or a narrower number. Under options from opts: dates tried, a decimal
// comma, lossy text, null values, and no inference.
func FuzzClassifyAgreesWithParse(f *testing.F) {
	for _, s := range []string{
		"", "NA", "true", "FALSE", "tRuE", "1", "0", "-0", "+5", "1.5", "1,5", ".5", "5.", "1e3", "1E-3",
		"inf", "-Infinity", "NaN", "-nan", "9223372036854775807", "9223372036854775808",
		"170141183460469231731687303715884105727", "170141183460469231731687303715884105728",
		"18446744073709551615", "2024-01-02", "02/01/2024", "2024-01-02 03:04:05", "2024-01-02T03:04:05Z",
		"2024-01-02T03:04:05+09:00", "03:04:05", "3:04", "24:00", "2023-02-29", "x", "1_000", "0x1p3",
		"\xff\xfe", "h\xc3\xa9", "1,000.5", "1.000,5", " 1",
	} {
		for _, opts := range []uint8{0, 1, 2, 3, 4, 8, 16} {
			f.Add([]byte(s), opts)
		}
	}
	f.Fuzz(func(t *testing.T, field []byte, opts uint8) {
		o := DefaultOptions()
		o.TryParseDates = opts&1 != 0
		o.DecimalComma = opts&2 != 0
		o.Lossy = opts&4 != 0
		if opts&8 != 0 {
			o.NullValues = []string{"NA", "null"}
		}
		o.NoInference = opts&16 != 0
		isNull := (&Source{opts: o}).nullTest()
		reads := func(dt dtype.DataType) error {
			b, err := newBuilder(dt, o)
			if err != nil {
				t.Fatalf("newBuilder(%s): %v", dt, err)
			}
			return b.appendField(field)
		}
		fuzzcheck.Run(t, fuzzLimits(len(field)), func() error {
			k := classify(field, isNull, o)
			if k == infUnknown && len(field) > 0 && !isNull(field) {
				t.Fatalf("classify(%q) found nothing in a field that is not null", field)
			}
			for _, dt := range widensTo(k) {
				if err := reads(dt); err != nil {
					t.Fatalf("classify(%q) = %s, and the %s builder refuses it: %v", field, k.dtype(), dt, err)
				}
			}
			if k == infString {
				if err := reads(dtype.String); (err == nil) != (o.Lossy || utf8.Valid(field)) {
					t.Fatalf("classify(%q) = String, and the String builder: %v", field, err)
				}
			}
			if k == infUnknown || o.NoInference {
				return nil
			}
			narrow := map[dtype.DataType][]inferred{
				dtype.Bool:    {infBool},
				dtype.Int64:   {infInt},
				dtype.Int128:  {infInt, infInt128},
				dtype.Float64: {infInt, infInt128, infFloat, infWord},
			}
			for dt, kinds := range narrow {
				if reads(dt) == nil && !inferredIn(k, kinds) {
					t.Fatalf("the %s builder reads %q, and classify calls it %s", dt, field, k.dtype())
				}
			}
			return nil
		})
	})
}

func inferredIn(k inferred, ks []inferred) bool {
	for _, x := range ks {
		if x == k {
			return true
		}
	}
	return false
}

// scannerSeeds are the encoding/csv differential's corpus (scanner_diff_test.go), the
// malformed quotes both refuse, and the shapes a line end, a byte-order mark and a
// stray quote make.
var scannerSeeds = []string{
	"a,b,c\n1,2,3\n", "a,b,c\n1,2,3", "a,b,c\r\n1,2,3\r\n", "a,,c\n,,\n", "a,b,\n1,2,\n", "a\nb\nc\n",
	"a,b\n\n1,2\n\n\n3,4\n", `"a","b"` + "\n" + `"1","2"` + "\n", `"a,b",c` + "\n", "\"a\nb\",c\n1,2\n",
	"\"a\r\nb\",c\n", `"say ""hi""",c` + "\n", `""""` + "\n", `"",""` + "\n", `a,"b,c",d` + "\n", `"abc"`,
	"a\rb,c\n", "  a  ,b\t\n", "héllo,wörld\n日本,語\n", strings.Repeat("x,", 40) + "y\n",
	`"abc` + "\n", `a,"bc`, `"ab"cd,e` + "\n", `a"b,c` + "\n", `a,b"c"` + "\n", `  "a",b` + "\n",
	"a,b\r", "a,b\n1,2\r", `"a","b"` + "\r", "a\n\r", "a,b\r\r\n1,2\n", "\"a\"\rb\n", "\r", "\r\n", "\n\r\n",
	"\xef\xbb\xbfa,b\n1,2\n", "\xef\xbb\xbf", "\xef\xbb", "a\x00b,c\n", "\xff,\xfe\n", "a;b\n1;2\n",
	"\"a\"\"\"\n", "\"\"\"\"\"\",x\n", "a,\"\"\n", "\"a\r\r\nb\",c\n",
}

// sizedScanner is newScannerFrom through a buffer of size bytes, to refill mid-record.
func sizedScanner(r io.Reader, o Options, size int) *scanner {
	sc := newScannerFrom(r, o)
	sc.buf = make([]byte, size)
	return sc
}

// trickle reads its bytes in pieces of 1 to 7, a read of none every third time, as
// a pipe or a socket may give them.
type trickle struct {
	b    []byte
	step int
}

func (r *trickle) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	r.step++
	if r.step%3 == 0 {
		return 0, nil
	}
	n := copy(p[:min(len(p), 1+r.step%7)], r.b)
	r.b = r.b[n:]
	return n, nil
}

// scanned is a scanner's records and its error's text, "" for none.
type scanned struct {
	recs [][]string
	err  string
}

func scanWith(sc *scanner) scanned {
	recs, err := drain(sc)
	s := scanned{recs: recs}
	if err != nil {
		s.err = err.Error()
	}
	return s
}

// scanEveryWay scans data whole, as the parallel path scans a block, through a buffer
// of size bytes, and through that buffer a few bytes a read; each must agree.
func scanEveryWay(t *testing.T, data []byte, o Options, size int) scanned {
	t.Helper()
	whole := scanWith(newScannerOver(data, o))
	for _, way := range []struct {
		name string
		r    io.Reader
	}{
		{"through a buffer", bytes.NewReader(data)},
		{"a few bytes a read", &trickle{b: data}},
	} {
		got := scanWith(sizedScanner(way.r, o, size))
		if !equal(got.recs, whole.recs) || got.err != whole.err {
			t.Fatalf("scanned whole: %q, error %q\n%s of %d bytes: %q, error %q\ninput %q",
				whole.recs, whole.err, way.name, size, got.recs, got.err, data)
		}
	}
	return whole
}

// encodingCSVComma is whether encoding/csv takes sep as its Comma, and reads it as
// the byte it is: an ASCII byte that is not a line end, a quote or NUL.
func encodingCSVComma(sep byte) bool {
	return sep != 0 && sep < utf8.RuneSelf && sep != '\r' && sep != '\n' && sep != '"'
}

// deviatesFromEncodingCSV is the one place the scanner and encoding/csv are allowed to
// differ, each case by Polars' read_csv: encoding/csv refuses a quote inside an
// unquoted field, ErrBareQuote, where ursus reads it as data, as Polars 2.1 reads
// a,b"c" and   "a",b (split.go: "the scanner reads a quote in the middle of an
// unquoted field as data").
func deviatesFromEncodingCSV(err error) bool {
	var pe *stdcsv.ParseError
	return errors.As(err, &pe) && errors.Is(pe.Err, stdcsv.ErrBareQuote)
}

// FuzzScannerMatchesEncodingCSV: any bytes, split by the scanner with the separator
// sep where encoding/csv takes it and else a comma, through a buffer of 1 to 512
// bytes, whole, and a few bytes a read, are encoding/csv's records, field for field,
// or both refuse them, but where deviatesFromEncodingCSV allows otherwise. A
// byte-order mark at a stream's start is dropped, and the rest scanned as without it.
func FuzzScannerMatchesEncodingCSV(f *testing.F) {
	for i, s := range scannerSeeds {
		f.Add([]byte(s), uint16(i*37), byte(','))
	}
	f.Add([]byte("a;b\n1;\"2;3\"\n"), uint16(5), byte(';'))
	f.Add([]byte("a\tb\n\"x\ty\"\t2\n"), uint16(64), byte('\t'))
	f.Fuzz(func(t *testing.T, data []byte, size uint16, sep byte) {
		if !encodingCSVComma(sep) {
			sep = ','
		}
		o := DefaultOptions()
		o.Separator = sep
		o = o.normalise()
		fuzzcheck.Run(t, fuzzLimits(len(data)), func() error {
			got := scanEveryWay(t, data, o, 1+int(size)%512)
			if rest, ok := bytes.CutPrefix(data, []byte{0xEF, 0xBB, 0xBF}); ok {
				started := scanWith(newScanner(bytes.NewReader(data), o))
				without := scanWith(newScannerFrom(bytes.NewReader(rest), o))
				if !equal(started.recs, without.recs) || started.err != without.err {
					t.Fatalf("with its byte-order mark: %q, %q; without: %q, %q", started.recs, started.err, without.recs, without.err)
				}
			}
			std := stdcsv.NewReader(bytes.NewReader(data))
			std.Comma, std.FieldsPerRecord = rune(sep), -1
			want, err := std.ReadAll()
			if deviatesFromEncodingCSV(err) {
				return nil
			}
			if (err != nil) != (got.err != "") {
				t.Fatalf("encoding/csv: %v; the scanner: %q", err, got.err)
			}
			if err == nil && !equal(got.recs, want) {
				t.Fatalf("the scanner: %q\nencoding/csv: %q", got.recs, want)
			}
			return nil
		})
	})
}

// FuzzEndsMask: endsMask over sixty-four bytes from any offset marks each separator
// and newline, as a loop a byte at a time marks them: the vector compare where the
// simd experiment builds one, the loop itself where it does not.
func FuzzEndsMask(f *testing.F) {
	f.Add([]byte(strings.Repeat("a,b\n", 20)), byte(','), uint8(0))
	f.Add([]byte(strings.Repeat("\x00\xff\n;", 33)), byte(';'), uint8(3))
	f.Add([]byte(strings.Repeat(",", 64)), byte(','), uint8(0))
	f.Add([]byte(strings.Repeat("\n", 70)), byte('\n'), uint8(6))
	f.Add([]byte("short"), byte(0), uint8(1))
	f.Fuzz(func(t *testing.T, b []byte, sep byte, off uint8) {
		if len(b) < 64 {
			b = append(b, make([]byte, 64-len(b))...)
		}
		at := int(off) % (len(b) - 63)
		fuzzcheck.Run(t, fuzzLimits(len(b)), func() error {
			var ref uint64
			for k, c := range b[at : at+64] {
				if c == sep || c == '\n' {
					ref |= 1 << k
				}
			}
			if got := endsMask(b[at:], sep); got != ref {
				t.Fatalf("endsMask(%q, %q) = %064b; a byte at a time: %064b", b[at:at+64], sep, got, ref)
			}
			return nil
		})
	})
}

// splitOptions are FuzzSplitMatchesSerial's options, a bit of opts each: no header, a
// comment prefix, no quoting, ragged lines truncated, errors ignored, every column a
// String, ';' for a separator, and dates tried.
func splitOptions(opts uint8) Options {
	o := DefaultOptions()
	o.HasHeader = opts&1 == 0
	if opts&2 != 0 {
		o.Comment = []byte("#")
	}
	o.NoQuote = opts&4 != 0
	o.TruncateRaggedLines = opts&8 != 0
	o.IgnoreErrors = opts&16 != 0
	o.NoInference = opts&32 != 0
	if opts&64 != 0 {
		o.Separator = ';'
	}
	o.TryParseDates = opts&128 != 0
	return o
}

// cellText is a cell's value as text, exactly: a String's quoted, a Bool's, and a
// fixed-width value's bytes, whatever its type.
func cellText(c *data.Column, i int) string {
	if !c.IsValid(i) {
		return "∅"
	}
	switch c.DType().ID() {
	case dtype.TypeString, dtype.TypeCategorical:
		return strconv.Quote(c.Strings().Get(i))
	case dtype.TypeBool:
		return strconv.FormatBool(c.Bools().Get(i))
	}
	raw := c.RawFixed()
	w := len(raw) / c.Len()
	return fmt.Sprintf("%x", raw[i*w:(i+1)*w])
}

// readRows reads src on threads, five rows a batch, into a line a row, its schema
// first, or stops at its first error.
func readRows(ctx context.Context, src *Source, threads int) ([]string, error) {
	bs, err := src.Open(ctx, source.ScanSpec{Threads: threads, BatchSize: 5})
	if err != nil {
		return nil, err
	}
	defer bs.Close()
	rows := []string{bs.Schema().String()}
	for {
		b, err := bs.Next(ctx)
		if errors.Is(err, io.EOF) {
			return rows, nil
		}
		if err != nil {
			return rows, err
		}
		for i := range b.Rows() {
			var line []string
			for _, c := range b.Columns() {
				line = append(line, cellText(c, i))
			}
			rows = append(rows, strings.Join(line, "|"))
		}
	}
}

// FuzzSplitMatchesSerial: any bytes read as CSV on four threads, cut into blocks of 1
// to 64 bytes so that a block's end falls everywhere, speculatively and exactly, are
// the serial read's rows, or its error, the same text naming the same row and line,
// after rows of the file's start. Under options from opts (splitOptions).
func FuzzSplitMatchesSerial(f *testing.F) {
	for i, s := range append(scannerSeeds,
		"a,b,c\n1,x,2.5\n# not a comment\n\"q\"\"\",,\n3,\"multi\nline\",4\n",
		"d,t\n2024-01-02,03:04:05\n02/01/2024,3:04\n",
		"a,b\n1,2\n3\n4,5,6\n,\n\n7,8\n",
		"#c\na,b\n#c2\n1,2\n#c3\n3,4\n") {
		f.Add([]byte(s), uint8(i), uint8(i*53))
	}
	f.Fuzz(func(t *testing.T, b []byte, block, opts uint8) {
		defer func(n int) { splitBlockSize = n }(splitBlockSize)
		splitBlockSize = 1 + int(block)%64
		o := splitOptions(opts)
		open := func(context.Context) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }
		fuzzcheck.Run(t, fuzzLimits(len(b)), func() error {
			serial, serr := readRows(t.Context(), New(open, "fuzz.csv", o), 1)
			par, perr := readRows(t.Context(), New(open, "fuzz.csv", o), 4)
			if fmt.Sprint(serr) != fmt.Sprint(perr) {
				t.Fatalf("blocks of %d: the error differs\nserial:   %v\nparallel: %v\ninput %q", splitBlockSize, serr, perr, b)
			}
			short, long := par, serial
			if serr != nil && len(short) > len(long) {
				short, long = long, short
			}
			if serr == nil && len(short) != len(long) || strings.Join(long[:min(len(long), len(short))], "\n") != strings.Join(short, "\n") {
				t.Fatalf("blocks of %d: the rows differ\nserial:   %q\nparallel: %q\ninput %q", splitBlockSize, serial, par, b)
			}
			return nil
		})
	})
}
