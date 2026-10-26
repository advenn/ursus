package csv

import (
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/source"
)

// TestParseIntIsStrconvs: the one-pass integer parses of step 373 answer as
// strconv.ParseInt and ParseUint do in base 10, value and error, at every width:
// the edges of each width and of the one pass's 18 and 19 digits, signs, leading
// zeros, text that is no number, and half a million random integers.
func TestParseIntIsStrconvs(t *testing.T) {
	check := func(s string) {
		t.Helper()
		for _, bits := range []int{8, 16, 32, 64} {
			gi, gerr := parseInt([]byte(s), bits)
			wi, werr := strconv.ParseInt(s, 10, bits)
			if gi != wi || fmt.Sprint(gerr) != fmt.Sprint(werr) {
				t.Fatalf("parseInt(%q, %d) = %d, %v; strconv %d, %v", s, bits, gi, gerr, wi, werr)
			}
			gu, gerr := parseUint([]byte(s), bits)
			wu, werr := strconv.ParseUint(s, 10, bits)
			if gu != wu || fmt.Sprint(gerr) != fmt.Sprint(werr) {
				t.Fatalf("parseUint(%q, %d) = %d, %v; strconv %d, %v", s, bits, gu, gerr, wu, werr)
			}
		}
	}
	for _, s := range []string{
		"", "0", "-0", "+0", "-", "+", "+-1", "--1", "1-", " 1", "1 ", "1.0", "1e3", "0x10", "1_000",
		"127", "128", "-128", "-129", "255", "256", "32767", "32768", "-32768", "-32769", "65535",
		"65536", "2147483647", "2147483648", "-2147483648", "-2147483649", "4294967295",
		"4294967296", "999999999999999999", "-999999999999999999", "1000000000000000000",
		"9223372036854775807", "9223372036854775808", "-9223372036854775808",
		"-9223372036854775809", "18446744073709551615", "18446744073709551616",
		"9999999999999999999", "99999999999999999999", "000000000000000000000000042",
		"-00000000000000000000127", "+00255", "١٢", "1\x00",
	} {
		check(s)
	}
	r := rand.New(rand.NewPCG(3, 73))
	for range 500_000 {
		var b strings.Builder
		switch r.IntN(4) {
		case 0:
			b.WriteByte('-')
		case 1:
			b.WriteByte('+')
		}
		for range r.IntN(22) {
			b.WriteByte(byte('0' + r.IntN(10)))
		}
		if r.IntN(50) == 0 {
			b.WriteByte("x .-/:"[r.IntN(6)])
		}
		check(b.String())
	}
}

// TestABatchWithNoNullHasNoBitmap: a column parsed with no null in it says so
// without a bitmap since step 373, and one with a null has its nulls where the file
// has them; for an integer, a float, a text and a Boolean, with nulls first and last
// in a batch, side by side, and alone amid valid values. On one thread a batch is parsed by itself; on four a
// block is parsed and sliced into batches, which keep the block's bitmap, so there a
// column no row of which is null is the one that has none.
func TestABatchWithNoNullHasNoBitmap(t *testing.T) {
	const rows = 2000
	empty := map[int]bool{300: true, 350: true, 351: true, 399: true, 1234: true, 1999: true}
	var sb strings.Builder
	// i, f, s and b are empty in the rows of empty; id, g, t and c never are.
	sb.WriteString("id,i,f,s,b,g,t,c\n")
	for k := range rows {
		never := fmt.Sprintf("%d.5,u%d,%v", k, k, k%3 == 0)
		if empty[k] {
			fmt.Fprintf(&sb, "%d,,,,,%s\n", k, never)
			continue
		}
		fmt.Fprintf(&sb, "%d,%d,%d.25,t%d,%v,%s\n", k, k*3, k, k, k%2 == 0, never)
	}
	path := filepath.Join(t.TempDir(), "t.csv")
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, threads := range []int{1, 4} {
		bs, err := FromFile(path, DefaultOptions()).Open(t.Context(),
			source.ScanSpec{Threads: threads, BatchSize: 100})
		if err != nil {
			t.Fatal(err)
		}
		at, nulls := 0, 0
		for {
			b, err := bs.Next(t.Context())
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			ids := data.MustValues[int64](b.Column(0))
			for k, id := range ids {
				if int(id) != at+k {
					t.Fatalf("%d threads: row %d has id %d", threads, at+k, id)
				}
			}
			has := false
			for k := range b.Rows() {
				has = has || empty[at+k]
			}
			for j, c := range b.Columns() {
				never := j == 0 || j > 4
				none := c.Validity().IsAllSet()
				switch {
				case never && !none:
					t.Errorf("%d threads: rows %d on, %s, never null: a bitmap", threads, at, c.Name())
				case !never && has && none:
					t.Errorf("%d threads: rows %d on, %s: a null, and no bitmap", threads, at, c.Name())
				case !never && !has && !none && threads == 1:
					t.Errorf("%d threads: rows %d on, %s: no null, and a bitmap", threads, at, c.Name())
				}
				for k := range b.Rows() {
					if c.IsValid(k) == (!never && empty[at+k]) {
						t.Errorf("%d threads: row %d, %s: valid %v", threads, at+k, c.Name(), c.IsValid(k))
					}
				}
				nulls += c.NullCount()
			}
			if v := data.MustValues[int64](b.Column(1)); b.Rows() > 0 && !empty[at] && v[0] != int64(3*at) {
				t.Errorf("%d threads: row %d, i: %d", threads, at, v[0])
			}
			at += b.Rows()
		}
		bs.Close()
		if at != rows || nulls != 4*len(empty) {
			t.Errorf("%d threads: %d rows, %d nulls; want %d, %d", threads, at, nulls, rows, 4*len(empty))
		}
	}
}
