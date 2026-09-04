package csv

import (
	stdcsv "encoding/csv"
	"io"
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
