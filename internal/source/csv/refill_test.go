package csv

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"testing"

	"github.com/advenn/ursus/internal/fuzzcheck"
)

// steady serves b n bytes a read, as a peer that sends slowly does: a refill then
// brings n bytes, not a buffer's worth. It is readfuzz's scheduled reader with one
// size.
type steady struct {
	b []byte
	n int
}

func (r *steady) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	k := copy(p[:min(len(p), r.n)], r.b)
	r.b = r.b[k:]
	return k, nil
}

// pick is a random byte of alphabet.
func pick(r *rand.Rand, alphabet string) byte { return alphabet[r.IntN(len(alphabet))] }

// longShapes are the ways a record grows long: each writes one of about n bytes, read
// under the options set gives.
var longShapes = []struct {
	name  string
	set   func(*Options)
	write func(r *rand.Rand, n int) []byte
}{
	{"a quoted field", nil, func(r *rand.Rand, n int) []byte {
		b := []byte{'"'}
		for len(b) < n {
			c := pick(r, "abcxyz019 ,;\n\r\"")
			if c == '"' {
				b = append(b, '"') // doubled: a quote in a quoted field
			}
			b = append(b, c)
		}
		return append(b, "\",x\n"...)
	}},
	{"an unquoted field", nil, func(r *rand.Rand, n int) []byte {
		var b []byte
		for len(b) < n {
			b = append(b, pick(r, "abcxyz019 .-_\r"))
		}
		return append(b, ",x\n"...)
	}},
	{"many fields", nil, func(r *rand.Rand, n int) []byte {
		var b []byte
		for len(b) < n {
			if len(b) > 0 {
				b = append(b, ',')
			}
			if r.IntN(3) > 0 {
				for range r.IntN(20) {
					b = append(b, pick(r, "abc019 "))
				}
				continue
			}
			b = append(b, '"')
			for range r.IntN(20) {
				c := pick(r, "pq7 ,\n\"")
				if c == '"' {
					b = append(b, '"')
				}
				b = append(b, c)
			}
			b = append(b, '"')
		}
		return append(b, "\r\n"...)
	}},
	// A quote that does not start a field is data (split.go), so a count of quotes
	// says a quoted field is open where none is.
	{"quotes inside unquoted fields", nil, func(r *rand.Rand, n int) []byte {
		var b []byte
		for len(b) < n {
			if len(b) > 0 {
				b = append(b, ',')
			}
			b = append(b, pick(r, "abc"))
			for range r.IntN(12) {
				b = append(b, pick(r, "ab\""))
			}
		}
		return append(b, '\n')
	}},
	{"a comment line", func(o *Options) { o.Comment = []byte("#") }, func(r *rand.Rand, n int) []byte {
		b := []byte{'#'}
		for len(b) < n {
			b = append(b, pick(r, "abc ,\"\r"))
		}
		return append(b, '\n')
	}},
	{"a comment of two bytes", func(o *Options) { o.Comment = []byte("//") }, func(r *rand.Rand, n int) []byte {
		b := []byte("//")
		for len(b) < n {
			b = append(b, pick(r, "abc ,\"/\r"))
		}
		return append(b, '\n')
	}},
}

// TestALongRecordInSmallReadsIsParsedInLinearTime: a record of 256 KiB and one of 1
// MiB, of each shape a record grows long in, followed by a short one, arriving 1 KiB
// a read, are the records the bytes read whole are; parse looks at no more than four
// times the input's bytes to find them, and seekEnd at each byte once at most.
// readRecord parsed a record from its first byte after each refill, and a refill
// brings what one read gives, so parse looked at 129.5 times the input at 256 KiB and
// 513.5 times at 1 MiB: quadratic (step 509 §3, step 515). A count, not a clock, as
// step 495's test.
func TestALongRecordInSmallReadsIsParsedInLinearTime(t *testing.T) {
	r := rand.New(rand.NewPCG(515, 1))
	for _, n := range []int{256 << 10, 1 << 20} {
		for _, shape := range longShapes {
			o := DefaultOptions()
			if shape.set != nil {
				shape.set(&o)
			}
			o = o.normalise()
			in := append(shape.write(r, n), "1,2\n"...)
			what := fmt.Sprintf("%s of %d KiB", shape.name, n>>10)
			whole := scanWith(newScannerOver(in, o))
			if whole.err != "" || len(whole.recs) == 0 {
				t.Fatalf("%s: read whole, %d records and error %q", what, len(whole.recs), whole.err)
			}
			sc := newScannerFrom(&steady{b: in, n: 1 << 10}, o)
			got := scanWith(sc)
			if !equal(got.recs, whole.recs) || got.err != whole.err {
				t.Fatalf("%s, 1 KiB a read: %d records and error %q; read whole, %d and %q",
					what, len(got.recs), got.err, len(whole.recs), whole.err)
			}
			if sc.parsed > 4*len(in) {
				t.Errorf("%s, 1 KiB a read: parse looked at %d bytes, %.1f times the input's %d",
					what, sc.parsed, float64(sc.parsed)/float64(len(in)), len(in))
			}
			if sc.followed > len(in) {
				t.Errorf("%s, 1 KiB a read: seekEnd followed %d bytes of the input's %d", what, sc.followed, len(in))
			}
			t.Logf("%s: parse looked at %.2f times the input, seekEnd followed %.2f",
				what, float64(sc.parsed)/float64(len(in)), float64(sc.followed)/float64(len(in)))
		}
	}
}

// stalling serves b in reads of 1 to 64 bytes, and then fails every read: a peer that
// has sent its records and waits.
type stalling struct {
	b []byte
	r *rand.Rand
}

var errStalled = errors.New("the peer has sent nothing more")

func (s *stalling) Read(p []byte) (int, error) {
	if len(s.b) == 0 {
		return 0, errStalled
	}
	k := copy(p[:min(len(p), 1+s.r.IntN(64))], s.b)
	s.b = s.b[k:]
	return k, nil
}

// mixedRecords writes count records under o, of every shape parse reads: blank lines,
// comments, unquoted fields with a quote or a \r in them, quoted ones with separators,
// line ends and doubled quotes, fields of up to 3 KiB, and LF and CRLF line ends.
func mixedRecords(r *rand.Rand, o Options, count int) []byte {
	var b []byte
	bare := func() {
		n := r.IntN(8)
		if r.IntN(10) == 0 {
			n = 500 + r.IntN(2500)
		}
		for i := range n {
			c := pick(r, "abc019 .\"'\r")
			if c == o.Separator || i == 0 && !o.NoQuote && c == o.Quote {
				c = 'z'
			}
			b = append(b, c)
		}
	}
	quoted := func() {
		n := r.IntN(10)
		if r.IntN(8) == 0 {
			n = 500 + r.IntN(2500)
		}
		b = append(b, o.Quote)
		for range n {
			c := pick(r, "pq7 \n\r\"';,\t")
			if c == o.Quote {
				b = append(b, c)
			}
			b = append(b, c)
		}
		b = append(b, o.Quote)
	}
	for range count {
		switch k := r.IntN(14); {
		case k == 0:
			b = append(b, '\n')
		case k == 1:
			b = append(b, "\r\n"...)
		case k == 2 && len(o.Comment) > 0:
			b = append(b, o.Comment...)
			for range r.IntN(40) {
				b = append(b, pick(r, "abc ,\"'\r"))
			}
			b = append(b, '\n')
		default:
			for f := range 1 + r.IntN(6) {
				if f > 0 {
					b = append(b, o.Separator)
				}
				if !o.NoQuote && r.IntN(3) == 0 {
					quoted()
				} else {
					bare()
				}
			}
			if r.IntN(3) == 0 {
				b = append(b, '\r')
			}
			b = append(b, '\n')
		}
	}
	return b
}

// TestARecordWhoseEndHasArrivedIsReturnedWithoutAnotherRead: a stream that sends its
// records, a few bytes a read, and then waits, which a read failing stands for, has
// every record it sent returned before that read, the records the bytes read whole
// are, whatever shape the last one has. Holding back a record whose end has arrived
// would leave a service reading a live stream waiting on a peer that waits on it.
func TestARecordWhoseEndHasArrivedIsReturnedWithoutAnotherRead(t *testing.T) {
	r := rand.New(rand.NewPCG(515, 2))
	for _, set := range []func(*Options){
		func(*Options) {},
		func(o *Options) { o.Separator, o.Quote, o.Comment = ';', '\'', []byte("#") },
		func(o *Options) { o.Separator, o.Comment = '\t', []byte("//") },
		func(o *Options) { o.NoQuote, o.Comment = true, []byte("#!") },
	} {
		o := DefaultOptions()
		set(&o)
		o = o.normalise()
		in := mixedRecords(r, o, 120)
		whole := newScannerOver(in, o)
		var recs [][]string
		var ends []int
		for whole.Next() {
			rec := make([]string, whole.NumFields())
			for i := range rec {
				rec[i] = string(whole.Field(i))
			}
			recs, ends = append(recs, rec), append(ends, whole.beg)
		}
		if err := whole.Err(); err != nil {
			t.Fatalf("separator %q, quote %q: read whole: %v", o.Separator, o.Quote, err)
		}
		for k, end := range ends {
			got, err := drain(newScannerFrom(&stalling{b: in[:end], r: r}, o))
			if !errors.Is(err, errStalled) || !equal(got, recs[:k+1]) {
				t.Fatalf("separator %q, quote %q, sent to the end of record %d: %d records, error %v; read whole %d",
					o.Separator, o.Quote, k+1, len(got), err, k+1)
			}
		}
	}
}

// FuzzRecordEndMatchesParse: any bytes, under any separator and quote, quoting on or
// off and a comment of up to three bytes, held a few more at a time as reads bring
// them, record after record: seekEnd says parse is to be asked exactly where parse
// finds the record's end, at that end, or an error in it, but while the bytes held
// are fewer than the comment's, where parse is asked at each read (step 515). Had it
// said no where parse finds a record, a record whose end has arrived would wait on a
// read; yes where parse finds none, the doubling alone would ask parse after.
func FuzzRecordEndMatchesParse(f *testing.F) {
	for i, s := range scannerSeeds {
		f.Add([]byte(s), byte(','), byte('"'), uint8(i<<1), []byte(nil)) // quoting on, a step of 1 to 32
	}
	f.Add([]byte("#a\"b\n1,2\n# c\r\n"), byte(','), byte('"'), uint8(2), []byte("#"))
	f.Add([]byte("//x\n/y,z\n\"//\"\n//"), byte(';'), byte('\''), uint8(4), []byte("//"))
	f.Add([]byte("a\nb\n\"c\nd\"\n"), byte('\n'), byte('"'), uint8(6), []byte(nil))
	f.Add([]byte("\r\r\n\r\r\r\n\r\n\r"), byte(','), byte('\r'), uint8(8), []byte(nil))
	f.Add([]byte("a,,b\n,,,\n,a,\n"), byte(','), byte(','), uint8(10), []byte(nil))
	f.Add([]byte("a\"b,\"c\n\"d\n"), byte(','), byte('"'), uint8(1), []byte(nil))
	f.Add([]byte("\"x\"\r,\"y\"\ry\n"), byte('\r'), byte('"'), uint8(0), []byte("\"x"))
	f.Fuzz(func(t *testing.T, data []byte, sep, quote byte, opts uint8, comment []byte) {
		data, comment = data[:min(len(data), 4<<10)], comment[:min(len(comment), 3)]
		o := DefaultOptions()
		o.Separator, o.Quote, o.NoQuote, o.Comment = sep, quote, opts&1 != 0, comment
		step := 1 + int(opts>>1)%32
		fuzzcheck.Run(t, fuzzLimits(len(data)), func() error {
			s := newScannerOver(data, o)
			for start := 0; start < len(data); {
				s.beg, s.endsTo, s.seek = start, 0, recordEnd{}
				for held := start; ; {
					held = min(held+step, len(data))
					b := data[start:held]
					n, _, ok, err := s.parse(b, false)
					asked, early := s.seekEnd(b), len(b) < len(comment)
					switch {
					case !asked && (ok || err != nil):
						t.Fatalf("%q from byte %d: parse found %d bytes, error %v; seekEnd did not ask it", b, start, n, err)
					case asked && !early && !ok && err == nil:
						t.Fatalf("%q from byte %d: seekEnd asked parse at byte %d, which found no record", b, start, s.seek.at)
					case asked && !early && ok && n != s.seek.at:
						t.Fatalf("%q from byte %d: parse ended the record at byte %d, seekEnd at %d", b, start, n, s.seek.at)
					}
					if err != nil || ok && start+n == len(data) {
						return nil
					}
					if ok {
						start += n
						break
					}
					if held == len(data) {
						return nil
					}
				}
			}
			return nil
		})
	})
}

// BenchmarkALongQuotedFieldInSmallReads is step 509's case, a quoted field of 2 MiB
// arriving 1 KiB a read: of one byte repeated, as step 509 timed it, 0.27 s a read
// before step 515, and of longShapes' bytes, a doubled quote or a line end in every
// few. One case, not a suite.
func BenchmarkALongQuotedFieldInSmallReads(b *testing.B) {
	o := DefaultOptions().normalise()
	plain := append([]byte{'"'}, bytes.Repeat([]byte{'x'}, 2<<20)...)
	for _, field := range []struct {
		name string
		in   []byte
	}{
		{"plain", append(plain, "\",x\n1,2\n"...)},
		{"mixed", append(longShapes[0].write(rand.New(rand.NewPCG(515, 3)), 2<<20), "1,2\n"...)},
	} {
		b.Run(field.name, func(b *testing.B) {
			b.SetBytes(int64(len(field.in)))
			for b.Loop() {
				sc := newScannerFrom(&steady{b: field.in, n: 1 << 10}, o)
				for sc.Next() {
				}
				if err := sc.Err(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
