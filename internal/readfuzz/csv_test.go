package readfuzz_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zlib"
	"github.com/klauspost/compress/zstd"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/fuzzcheck"
	"github.com/advenn/ursus/ursustest"
)

// csvSeeds are the CSV targets' seed corpus: the shapes the reader's own tests name,
// and one of them compressed each way the reader knows a stream by its first bytes.
// testdata/fuzz/FuzzScanCSV adds the texts Polars 2.1's recorded CSV answers read
// (testdata/oracle/csvread.json, csvskip.json and csvdates.json) and the ones its
// writer wrote (csvwrite.json), each with its options.
func csvSeeds(f *testing.F) [][]byte {
	plain := []string{
		"",
		"a,b,c\n1,2,3\n4,5,6\n",
		"a,b\n1,x\n,\n3,\n",
		"id,price,when,day,flag\n1,1.5,2024-01-02 03:04:05,2024-01-02,true\n2,-0.25,2024-02-29 23:59:59,2024-02-29,false\n",
		"a,b\r\n1,2\r\n3,4\r\n",
		"\xef\xbb\xbfa,b\n1,2\n",
		"name,text\n1,\"quoted, with a comma\"\n2,\"a \"\"quote\"\" inside\"\n3,\"two\nlines\"\n",
		"a,b,c\n1,2\n3,4,5,6\n",
		"a\n\n\n1\n",
		"a,a,b\n1,2,3\n",
		"x\n9223372036854775807\n-9223372036854775808\n9223372036854775808\n",
		"f\n1e308\n1e309\nNaN\ninf\n-0.0\n",
		"t\n12:34:56\n24:00:00\n",
		"s\n\xff\xfe\n\xc3\x28\n",
		"a,b\n\"unterminated,1\n",
		"x^2,b\n1,2\n",
		"a,b\n1,2\r",
		"a\n1\n\r",
		"x\n-nan\n1.5\n",
		"d\n1e99999999999999999999\n0e9223372036854775808\n",
		"# banner\na;b\n1,5;2\n",
	}
	seeds := make([][]byte, 0, len(plain)+3)
	for _, s := range plain {
		seeds = append(seeds, []byte(s))
	}
	text := []byte(plain[3])
	return append(seeds, compress(f, text, 1), compress(f, text, 2), compress(f, text, 3))
}

// compress is b as a gzip (1), a zlib (2) or a Zstandard (3) stream, kind's low two
// bits, or b itself (0).
func compress(f testing.TB, b []byte, kind uint32) []byte {
	var buf bytes.Buffer
	var w io.WriteCloser
	switch kind % 4 {
	case 0:
		return b
	case 1:
		w = gzip.NewWriter(&buf)
	case 2:
		w = zlib.NewWriter(&buf)
	case 3:
		enc, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
		if err != nil {
			f.Fatal(err)
		}
		defer enc.Close()
		return enc.EncodeAll(b, nil)
	}
	if _, err := w.Write(b); err != nil {
		f.Fatal(err)
	}
	if err := w.Close(); err != nil {
		f.Fatal(err)
	}
	return buf.Bytes()
}

// csvOptions decodes FuzzScanCSV's options from opts, a few bits each:
//
//	bits 0-1   the stream gzip, zlib or Zstandard, or none (compress)
//	bit 2      the separator the byte in bits 24-31, any byte
//	bit 3      no quoting                  bit 4      ' for the quote
//	bit 5      # for a comment prefix      bit 6      no header
//	bits 7-8   SkipRows 0 to 3             bits 9-10  SkipLines 0 to 3
//	bit 11     NA a null value             bit 12     a decimal comma
//	bit 13     dates tried                 bit 14     ragged lines truncated
//	bit 15     errors ignored              bits 16-17 MaxRecordSize 64, 1 KiB or the default
//	bits 18-19 InferRows 0, 1 or 100       bit 20     text read lossily
//	bits 21-23 the batch size, 1 to 7
func csvOptions(opts uint32) (o []ursus.CSVOption, batch int) {
	bit := func(i uint) bool { return opts>>i&1 != 0 }
	two := func(i uint) int { return int(opts >> i & 3) }
	if bit(2) {
		o = append(o, ursus.WithSeparator(byte(opts>>24)))
	}
	if bit(3) {
		o = append(o, ursus.WithQuoting(false))
	}
	if bit(4) {
		o = append(o, ursus.WithQuote('\''))
	}
	if bit(5) {
		o = append(o, ursus.WithComment("#"))
	}
	if bit(6) {
		o = append(o, ursus.WithHasHeader(false))
	}
	if n := two(7); n > 0 {
		o = append(o, ursus.WithSkipRows(n))
	}
	if n := two(9); n > 0 {
		o = append(o, ursus.WithSkipLines(n))
	}
	if bit(11) {
		o = append(o, ursus.WithNullValues("NA"))
	}
	if bit(12) {
		o = append(o, ursus.WithDecimalComma(true))
	}
	if bit(13) {
		o = append(o, ursus.WithTryParseDates(true))
	}
	if bit(14) {
		o = append(o, ursus.WithTruncateRaggedLines(true))
	}
	if bit(15) {
		o = append(o, ursus.WithIgnoreErrors(true))
	}
	if n := two(16); n == 1 || n == 2 {
		o = append(o, ursus.WithMaxRecordSize([]int{0, 64, 1024}[n]))
	}
	if n := two(18); n > 0 {
		o = append(o, ursus.WithInferRows([]int{100, 0, 1, 100}[n]))
	}
	if bit(20) {
		o = append(o, ursus.WithEncoding(ursus.UTF8Lossy))
	}
	return o, 1 + int(opts>>21&7)%7
}

// sameError: both nil, or both errors of the same text.
func sameError(a, b error) bool { return fmt.Sprint(a) == fmt.Sprint(b) }

// FuzzScanCSV: any bytes, read as a CSV file through the public API under options from
// opts (csvOptions), are a frame or an error, inside fuzzcheck's limits, each read.
// And the reads agree: on one thread and on four, with the verifier on, the same
// frame or the same error; where the frame is read, CollectSchema is its schema,
// Count its height, Head(1) its first row and a Select of its last column that column.
func FuzzScanCSV(f *testing.F) {
	seeds := csvSeeds(f)
	for _, b := range seeds {
		f.Add(b, uint32(0))
	}
	for i, opts := range []uint32{1, 2, 3, 1<<2 | ';'<<24, 1 << 3, 1 << 4, 1<<5 | 1<<6, 2<<7 | 1<<11,
		3 << 9, 1<<12 | 1<<2 | ';'<<24, 1<<13 | 1<<15, 1 << 14, 1 << 16, 2 << 16, 1 << 18, 2<<18 | 1<<20,
		6 << 21, 1<<2 | '\t'<<24, 1<<2 | 0xff<<24, 1<<2 | '"'<<24, 1<<2 | '\n'<<24} {
		f.Add(seeds[(3+i)%len(seeds)], opts)
	}
	f.Fuzz(func(t *testing.T, raw []byte, opts uint32) {
		b := compress(t, raw, opts)
		n, ok := inflated(b)
		if !ok {
			t.Skip("decompresses past maxInflated")
		}
		o, batch := csvOptions(opts)
		lf := ursus.ScanCSVReader(b, "fuzz.csv", o...)
		read := func(fn func() error) { fuzzcheck.Run(t, limits(n), fn) }
		ctx := t.Context()

		var schema *ursus.Schema
		var serr error
		read(func() error { schema, serr = lf.CollectSchema(ctx); return serr })
		var one, four *ursus.DataFrame
		var err1, err4 error
		read(func() error {
			one, err1 = lf.Collect(ctx, ursus.WithThreads(1), ursus.WithVerify(), ursus.WithBatchSize(batch))
			return err1
		})
		read(func() error {
			four, err4 = lf.Collect(ctx, ursus.WithThreads(4), ursus.WithVerify(), ursus.WithBatchSize(batch))
			return err4
		})
		if !sameError(err1, err4) {
			t.Fatalf("on one thread: %v\non four: %v", err1, err4)
		}
		if err1 != nil {
			return
		}
		ursustest.AssertFrameEqual(t, four, one)
		if serr != nil {
			t.Fatalf("the frame is read, and its schema is not: %v", serr)
		}
		ursustest.AssertSchemaEqual(t, schema, one.Schema(), ursustest.CheckNullability())

		var count int64
		var cerr error
		read(func() error { count, cerr = lf.Count(ctx); return cerr })
		if cerr != nil || count != int64(one.Height()) {
			t.Fatalf("Count = %d, %v; the frame has %d rows", count, cerr, one.Height())
		}
		var head *ursus.DataFrame
		var herr error
		read(func() error { head, herr = lf.Head(1).Collect(ctx); return herr })
		if herr != nil {
			t.Fatalf("Head(1): %v", herr)
		}
		first, err := one.Head(ctx, 1)
		if err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, head, first)
		if one.Width() == 0 {
			return
		}
		name := one.Columns()[one.Width()-1]
		var sel *ursus.DataFrame
		var selErr error
		read(func() error { sel, selErr = lf.Select(ursus.Col(name)).Collect(ctx); return selErr })
		if selErr != nil {
			t.Fatalf("Select(%q): %v", name, selErr)
		}
		col, err := one.Select(ctx, ursus.Col(name))
		if err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, sel, col)
	})
}

// scheduled serves b in reads sized by schedule's bytes in turn, the low seven bits
// the size: 0 an empty read, no bytes and no error, at most 99 in a row, which the
// streams' reader allows (internal/source.Decompressed fails the hundredth); and the
// top bit, on the read that ends b, its bytes and io.EOF together.
type scheduled struct {
	b        []byte
	schedule []byte
	next     int
	empty    int
}

func (r *scheduled) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	s := byte(1)
	if len(r.schedule) > 0 {
		s = r.schedule[r.next%len(r.schedule)]
		r.next++
	}
	size := int(s & 0x7f)
	if size == 0 && r.empty < 99 {
		r.empty++
		return 0, nil
	}
	r.empty = 0
	n := copy(p[:min(len(p), max(size, 1))], r.b)
	r.b = r.b[n:]
	if len(r.b) == 0 && s&0x80 != 0 {
		return n, io.EOF
	}
	return n, nil
}

// FuzzScanCSVChunked: any bytes, read through ScanCSVFrom from a stream whose reads
// schedule sizes, empty ones and a last one with io.EOF among them, are what
// ScanCSVReader reads from the bytes whole: the same frame, or the same error.
func FuzzScanCSVChunked(f *testing.F) {
	schedules := [][]byte{nil, {0}, {1}, {0, 1}, {3, 0, 0, 5}, {0x81}, {0x7f}, {2, 0x83}, {0, 0, 0, 0, 0, 0, 0, 7}, {64, 0, 1}}
	for i, b := range csvSeeds(f) {
		f.Add(b, schedules[i%len(schedules)])
	}
	f.Add([]byte("a,b\n1,2\n3,4\n"), bytes.Repeat([]byte{0}, 120))
	f.Fuzz(func(t *testing.T, b, schedule []byte) {
		n, ok := inflated(b)
		if !ok {
			t.Skip("decompresses past maxInflated")
		}
		ctx := t.Context()
		var whole, chunked *ursus.DataFrame
		var werr, cerr error
		fuzzcheck.Run(t, limits(n), func() error {
			whole, werr = ursus.ScanCSVReader(b, "fuzz.csv").Collect(ctx)
			return werr
		})
		fuzzcheck.Run(t, limits(n), func() error {
			chunked, cerr = ursus.ScanCSVFrom([]ursus.CSVFile{{
				Name: "fuzz.csv",
				Open: func(context.Context) (io.ReadCloser, error) {
					return io.NopCloser(&scheduled{b: b, schedule: schedule}), nil
				},
			}}).Collect(ctx)
			return cerr
		})
		if !sameError(werr, cerr) {
			t.Fatalf("read whole: %v\nread in chunks: %v", werr, cerr)
		}
		if werr == nil {
			ursustest.AssertFrameEqual(t, chunked, whole)
		}
	})
}
