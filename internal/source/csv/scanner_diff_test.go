package csv

import (
	stdcsv "encoding/csv"
	"fmt"
	"io"
	"math/rand/v2"
	"strings"
	"testing"
)

// TestScannerMatchesEncodingCSV is what licenses a hand-written CSV scanner.
//
// The argument for not using encoding/csv is in scanner.go and it is structural,
// not a benchmark: Read materialises every field of every row, which makes
// projection pushdown impossible, forces a row-major intermediate, and discards
// the raw bytes an error message needs. None of that makes hand-rolled quote
// handling any less dangerous, so the risk is paid down the same way the SIMD
// kernels pay theirs — an independent implementation checks every case.
//
// The deviations below are DELIBERATE and are excluded from the corpus rather than
// hidden, so the exception list is itself reviewable:
//
//   - A multi-byte Comment prefix. encoding/csv's Comment is a single rune.
//   - A record-size bound. encoding/csv will happily buffer a whole file into one
//     unterminated quoted field.
//   - Field slices alias the buffer and are valid only until the next Next.
//
// Everything else must agree field for field.
func TestScannerMatchesEncodingCSV(t *testing.T) {
	inputs := []struct {
		name string
		in   string
	}{
		{"simple", "a,b,c\n1,2,3\n"},
		{"no trailing newline", "a,b,c\n1,2,3"},
		{"crlf", "a,b,c\r\n1,2,3\r\n"},
		{"empty fields", "a,,c\n,,\n"},
		{"trailing separator", "a,b,\n1,2,\n"},
		{"single column", "a\nb\nc\n"},
		{"blank lines between records", "a,b\n\n1,2\n\n\n3,4\n"},
		{"quoted simple", `"a","b"` + "\n" + `"1","2"` + "\n"},
		{"quoted with separator", `"a,b",c` + "\n"},
		{"quoted with newline", "\"a\nb\",c\n1,2\n"},
		{"quoted with crlf inside", "\"a\r\nb\",c\n"},
		{"doubled quotes", `"say ""hi""",c` + "\n"},
		{"doubled quotes only", `""""` + "\n"},
		{"quoted empty", `"",""` + "\n"},
		{"mixed quoted and bare", `a,"b,c",d` + "\n"},
		{"quote at end of file", `"abc"`},
		{"lone cr is data", "a\rb,c\n"},
		{"whitespace preserved", "  a  ,b\t\n"},
		{"unicode", "héllo,wörld\n日本,語\n"},
		{"many columns", strings.Repeat("x,", 40) + "y\n"},
		{"long field", strings.Repeat("z", 5000) + ",b\n"},
		{"long quoted field", `"` + strings.Repeat("z", 5000) + `",b` + "\n"},
		{"record spanning many buffer fills", strings.Repeat("a,b,c,d,e\n", 5000)},
	}

	for _, tc := range inputs {
		t.Run(tc.name, func(t *testing.T) {
			want, wantErr := readAllStd(tc.in)
			got, gotErr := readAllScanner(t, tc.in, DefaultOptions())

			if (wantErr != nil) != (gotErr != nil) {
				t.Fatalf("error disagreement: encoding/csv=%v, ursus=%v", wantErr, gotErr)
			}
			if wantErr != nil {
				return
			}
			if len(got) != len(want) {
				t.Fatalf("record count = %d, want %d\ngot:  %q\nwant: %q",
					len(got), len(want), got, want)
			}
			for i := range want {
				if len(got[i]) != len(want[i]) {
					t.Fatalf("record %d: %d fields, want %d\ngot:  %q\nwant: %q",
						i, len(got[i]), len(want[i]), got[i], want[i])
				}
				for j := range want[i] {
					if got[i][j] != want[i][j] {
						t.Errorf("record %d field %d = %q, want %q", i, j, got[i][j], want[i][j])
					}
				}
			}
		})
	}
}

// TestScannerRejectsMalformedQuotes covers the cases where BOTH implementations
// must fail. A scanner that silently accepts them would pass the corpus above by
// never being asked.
func TestScannerRejectsMalformedQuotes(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"unterminated quote", `"abc` + "\n"},
		{"unterminated quote at eof", `a,"bc`},
		{"text after closing quote", `"ab"cd,e` + "\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := readAllScanner(t, c.in, DefaultOptions()); err == nil {
				t.Error("expected an error")
			}
			if _, err := readAllStd(c.in); err == nil {
				t.Error("encoding/csv accepted it; the corpus assumption is wrong")
			}
		})
	}
}

// TestScannerBufferBoundaries forces a refill in the middle of every construct
// that has state, by shrinking the buffer to a few bytes.
//
// This is the case a scanner that suspends and resumes a quote-state machine gets
// wrong, and it is invisible at the default 64 KiB buffer because the fixtures a
// human writes never reach it.
func TestScannerBufferBoundaries(t *testing.T) {
	inputs := []string{
		"a,b,c\n1,2,3\n",
		`"quoted,field",b` + "\n",
		"\"multi\nline\",b\n",
		`"doubled""quote",b` + "\n",
		"a,b\r\nc,d\r\n",
		strings.Repeat("aaaa,bbbb\n", 200),
	}
	for _, in := range inputs {
		want, err := readAllStd(in)
		if err != nil {
			t.Fatalf("corpus entry is not valid CSV: %v", err)
		}
		// Every buffer size from "smaller than any field" upward.
		for _, size := range []int{1, 2, 3, 5, 7, 8, 16, 31} {
			sc := newScannerSized(strings.NewReader(in), DefaultOptions().normalise(), size)
			got, err := drain(sc)
			if err != nil {
				t.Fatalf("buffer %d bytes: %v\ninput: %q", size, err, in)
			}
			if !equal(got, want) {
				t.Fatalf("buffer %d bytes disagreed with encoding/csv\ngot:  %q\nwant: %q\ninput: %q",
					size, got, want, in)
			}
		}
	}
}

// TestScannerCommentsAndSkips covers the features encoding/csv cannot express, so
// they are checked against hand-written expectations instead.
func TestScannerCommentsAndSkips(t *testing.T) {
	o := DefaultOptions()
	o.Comment = []byte("//")

	got, err := readAllScanner(t, "// a banner\na,b\n// another\n1,2\n", o)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"a", "b"}, {"1", "2"}}
	if !equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestScannerLineNumbersArePhysical: a field with an embedded newline advances the
// physical line by more than one, while the record number advances by exactly one.
// Error messages quote both, and reporting a logical row as a line number points
// the reader at the wrong place in their file.
func TestScannerLineNumbersArePhysical(t *testing.T) {
	sc := newScanner(strings.NewReader("a,b\n\"x\ny\",c\nlast,z\n"), DefaultOptions().normalise())

	var lines, recs []int
	for sc.Next() {
		lines = append(lines, sc.Line())
		recs = append(recs, sc.Record())
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	// Record 2 spans lines 2 and 3, so record 3 starts on line 4.
	wantLines := []int{1, 3, 4}
	wantRecs := []int{1, 2, 3}
	for i := range wantLines {
		if i >= len(lines) {
			t.Fatalf("got %d records, want %d", len(lines), len(wantLines))
		}
		if lines[i] != wantLines[i] || recs[i] != wantRecs[i] {
			t.Errorf("record %d: line %d rec %d, want line %d rec %d",
				i, lines[i], recs[i], wantLines[i], wantRecs[i])
		}
	}
}

// TestScannerBoundsRecordSize: an unterminated quote must be an error, not an
// out-of-memory kill. Without the bound, one stray quote makes the rest of a 10 GB
// file a single record.
func TestScannerBoundsRecordSize(t *testing.T) {
	o := DefaultOptions()
	o.MaxRecordSize = 1024

	in := `"` + strings.Repeat("x", 100_000)
	_, err := readAllScanner(t, in, o)
	if err == nil {
		t.Fatal("expected a record-size error")
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Errorf("error should name the limit: %v", err)
	}
}

// --- helpers -------------------------------------------------------------------

func newScannerSized(r io.Reader, o Options, size int) *scanner {
	sc := newScanner(r, o)
	sc.buf = make([]byte, size)
	return sc
}

func readAllStd(in string) ([][]string, error) {
	r := stdcsv.NewReader(strings.NewReader(in))
	r.FieldsPerRecord = -1 // ragged input is the scanner's caller's problem, not the splitter's
	return r.ReadAll()
}

func readAllScanner(t *testing.T, in string, o Options) ([][]string, error) {
	t.Helper()
	return drain(newScanner(strings.NewReader(in), o.normalise()))
}

func drain(sc *scanner) ([][]string, error) {
	var out [][]string
	for sc.Next() {
		rec := make([]string, sc.NumFields())
		for i := range rec {
			rec[i] = string(sc.Field(i))
		}
		out = append(out, rec)
	}
	return out, sc.Err()
}

func equal(a, b [][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if len(a[i]) != len(b[i]) {
			return false
		}
		for j := range a[i] {
			if a[i][j] != b[i][j] {
				return false
			}
		}
	}
	return true
}

// TestALongQuotedFieldIsNormalisedAndCounted: a quoted field longer than shortField
// is searched for its line endings and carriage returns, where a short one is looked
// at a byte at a time (step 211). Fields either side of the threshold, each holding
// a CRLF and a LF: the CRLF reads as LF, as encoding/csv reads it, and the records
// after start on their physical lines.
func TestALongQuotedFieldIsNormalisedAndCounted(t *testing.T) {
	for _, n := range []int{shortField - 4, shortField + 8, 3 * shortField} {
		pad := strings.Repeat("x", n)
		in := "a,b\n\"" + pad + "\r\nmid\nend\",1\nlast,2\n"
		sc := newScanner(strings.NewReader(in), DefaultOptions().normalise())
		var lines []int
		var fields []string
		for sc.Next() {
			lines = append(lines, sc.Line())
			fields = append(fields, string(sc.Field(0)))
		}
		if err := sc.Err(); err != nil {
			t.Fatal(err)
		}
		if want := pad + "\nmid\nend"; len(fields) != 3 || fields[1] != want {
			t.Fatalf("a field of %d: %q, want %q", n, fields, want)
		}
		// The field spans lines 2 to 4, so the last record is on line 5.
		if lines[2] != 5 {
			t.Errorf("a field of %d: the record after it on line %d, want 5", n, lines[2])
		}
	}
}

// TestFieldEndsFoundSixtyFourBytesAtATime: an unquoted field's end, found in a mask
// of sixty-four bytes since step 373, splits as encoding/csv splits, over records of
// unquoted fields 0 to 150 bytes long, quoted ones holding separators, newlines and
// doubled quotes between them, and CRLF line ends: through buffers either side of
// sixty-four bytes and larger, refilled mid-mask, by reads of a few bytes as a pipe
// gives them, over a whole block as the parallel path scans one, and with a
// separator other than the comma.
func TestFieldEndsFoundSixtyFourBytesAtATime(t *testing.T) {
	r := rand.New(rand.NewPCG(37, 3))
	for _, sep := range []byte{',', ';', '\t'} {
		var sb strings.Builder
		for range 600 {
			for f := range 1 + r.IntN(14) {
				if f > 0 {
					sb.WriteByte(sep)
				}
				switch k := r.IntN(10); {
				case k < 7:
					n := r.IntN(30)
					if r.IntN(8) == 0 {
						n = 64 + r.IntN(90)
					}
					for range n {
						sb.WriteByte("abcxyz0129 .-_/é"[r.IntN(16)])
					}
				case k < 9:
					sb.WriteByte('"')
					for range r.IntN(80) {
						switch c := r.IntN(20); c {
						case 0:
							sb.WriteString(`""`)
						case 1:
							sb.WriteByte('\n')
						case 2:
							sb.WriteByte(sep)
						default:
							sb.WriteByte("pqrs789 "[c%8])
						}
					}
					sb.WriteByte('"')
				}
			}
			if r.IntN(4) == 0 {
				sb.WriteByte('\r')
			}
			sb.WriteByte('\n')
		}
		in := sb.String()
		std := stdcsv.NewReader(strings.NewReader(in))
		std.Comma, std.FieldsPerRecord = rune(sep), -1
		want, err := std.ReadAll()
		if err != nil {
			t.Fatalf("separator %q: the corpus is not CSV: %v", sep, err)
		}
		o := DefaultOptions()
		o.Separator = sep
		o = o.normalise()
		for _, size := range []int{63, 64, 65, 100, 127, 128, 129, 200, 300, 1000, 4096, 0} {
			for _, piecemeal := range []bool{false, true} {
				var sc *scanner
				switch {
				case size == 0 && piecemeal:
					continue
				case size == 0:
					sc = newScannerOver([]byte(in), o)
				case piecemeal:
					sc = newScannerSized(&pieces{in, r}, o, size)
				default:
					sc = newScannerSized(strings.NewReader(in), o, size)
				}
				what := fmt.Sprintf("separator %q, buffer %d, read piecemeal %v", sep, size, piecemeal)
				got, err := drain(sc)
				if err != nil {
					t.Fatalf("%s: %v", what, err)
				}
				if !equal(got, want) {
					for i := range min(len(got), len(want)) {
						if !equal(got[i:i+1], want[i:i+1]) {
							t.Fatalf("%s: record %d is %q, want %q", what, i, got[i], want[i])
						}
					}
					t.Fatalf("%s: %d records, want %d", what, len(got), len(want))
				}
			}
		}
	}
}

// pieces reads its input 1 to 100 bytes at a time, as a pipe or a socket may give
// it: a refill then brings fewer bytes than a mask covers.
type pieces struct {
	s string
	r *rand.Rand
}

func (p *pieces) Read(b []byte) (int, error) {
	if len(p.s) == 0 {
		return 0, io.EOF
	}
	n := copy(b[:min(len(b), 1+p.r.IntN(100))], p.s)
	p.s = p.s[n:]
	return n, nil
}
