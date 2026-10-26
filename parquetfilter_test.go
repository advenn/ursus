package ursus_test

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/file"

	"github.com/advenn/ursus"
)

// TestAParquetFilterReadsTheRestAtItsKeptRows: a text's conjunct pushed to a Parquet
// scan is judged on its dictionary, and since step 377 the reader reads the batch's
// other columns at the rows it keeps, where it read them whole and took the kept
// rows. Every scalar type Parquet holds is read beside it, with and without nulls,
// converted and not, from dictionary pages and from the PLAIN pages a column falls
// back to past its dictionary's megabyte, and a list, a struct, a Boolean and a
// decimal, whose readers read whole and take; filters keep some rows, none and all; on
// one thread and four, in batches of 1000 and the default. The answer is the filter's
// over the whole file read.
func TestAParquetFilterReadsTheRestAtItsKeptRows(t *testing.T) {
	c := ursus.Col
	ctx := t.Context()
	const n = 300_000
	id := make([]int64, n)
	price := make([]float64, n)
	disc := make([]float64, n)
	discOK := make([]bool, n)
	qty := make([]int32, n)
	dayn := make([]int32, n)
	tsn := make([]int64, n)
	durn := make([]int64, n)
	decn := make([]float64, n)
	mode := make([]string, n)
	modeOK := make([]bool, n)
	txt := make([]string, n)
	flag := make([]bool, n)
	u8 := make([]uint8, n)
	u8OK := make([]bool, n)
	i16 := make([]int16, n)
	f32 := make([]float32, n)
	modes := []string{"AIR", "MAIL", "SHIP", "RAIL", "TRUCK", "FOB", "REG AIR"}
	for i := range n {
		h := uint64(i) * 0x9E3779B97F4A7C15
		id[i] = int64(i)
		price[i] = float64(h>>11) / (1 << 40)
		disc[i], discOK[i] = float64(h%11)/100, i%17 != 0
		qty[i] = int32(1 + h%50)
		dayn[i] = int32(8000 + h%2500)
		tsn[i] = int64(h >> 20)
		durn[i] = int64(h%86400) * 1e9
		decn[i] = float64(h%400) * 0.25
		mode[i], modeOK[i] = modes[h%7], i%13 != 5
		txt[i] = fmt.Sprintf("comment-%d", h%1_000_003)
		flag[i] = h%3 == 0
		u8[i], u8OK[i] = uint8(h), i%19 != 2
		i16[i] = int16(h)
		f32[i] = float32(h%1000) / 1000
	}
	src := ursus.Frame(ursus.Values("id", id), ursus.Values("price", price),
		ursus.ValuesNullable("disc", disc, discOK), ursus.Values("qty", qty), ursus.Values("dayn", dayn),
		ursus.Values("tsn", tsn), ursus.Values("durn", durn), ursus.Values("decn", decn),
		ursus.ValuesNullable("mode", mode, modeOK), ursus.Values("txt", txt), ursus.Values("flag", flag),
		ursus.ValuesNullable("u8", u8, u8OK), ursus.Values("i16", i16), ursus.Values("f32", f32)).
		WithColumns(c("dayn").Cast(ursus.Date).Alias("day"),
			c("tsn").Cast(ursus.Datetime(ursus.Micro, "")).Alias("ts"),
			c("durn").Cast(ursus.TimeOf(ursus.Nano)).Alias("tod"),
			c("decn").Cast(ursus.Decimal(12, 2)).Alias("dec"),
			ursus.ConcatList(c("id"), c("id").Add(ursus.Lit(int64(1)))).Alias("lst"),
			ursus.Struct(c("qty"), c("mode")).Alias("st")).
		Drop("dayn", "tsn", "durn", "decn")
	path := filepath.Join(t.TempDir(), "t.parquet")
	if err := src.SinkParquet(ctx, path, ursus.WithRowGroupRows(200_000)); err != nil {
		t.Fatal(err)
	}
	// The premise: some column's chunk is in dictionary pages and then PLAIN ones.
	if !fallsBackToPlain(t, path) {
		t.Fatal("no column chunk falls back from its dictionary to PLAIN pages")
	}

	full, err := ursus.ScanParquet(path).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	day := func(y int, m time.Month, d int) ursus.Expr {
		return ursus.Lit(time.Date(y, m, d, 0, 0, 0, 0, time.UTC)).Cast(ursus.Date)
	}
	filters := []struct {
		name string
		p    ursus.Expr
	}{
		{"a text's value", c("mode").Eq(ursus.Lit("AIR"))},
		{"a text's values and two numbers", c("qty").Lt(ursus.Lit(int32(24))).
			And(c("disc").Ge(ursus.Lit(0.05))).And(c("mode").IsIn(ursus.Lit([]string{"AIR", "MAIL"})))},
		{"a text's null", c("mode").IsNull()},
		{"a text present and a date's month", c("mode").IsNotNull().
			And(c("day").IsBetween(day(1993, 1, 1), day(1993, 2, 1), ursus.ClosedLeft))},
		{"a text of PLAIN pages", c("txt").Str().Contains(ursus.Lit("99"), true)},
		{"two texts", c("txt").Str().StartsWith(ursus.Lit("comment-1")).And(c("mode").Ne(ursus.Lit("SHIP")))},
		{"no row", c("mode").Eq(ursus.Lit("NONE"))},
		{"every row", c("mode").IsNull().Or(c("mode").IsNotNull())},
		{"a date's month alone", c("day").IsBetween(day(1993, 1, 1), day(1993, 2, 1), ursus.ClosedLeft)},
		{"a float between alone", c("disc").IsBetween(ursus.Lit(0.05), ursus.Lit(0.07), ursus.ClosedBoth)},
	}
	for _, f := range filters {
		want, err := full.Lazy().Filter(f.p).Collect(ctx)
		if err != nil {
			t.Fatalf("%s: %v", f.name, err)
		}
		if f.name != "every row" && f.name != "no row" && (want.Height() == 0 || want.Height() == n) {
			t.Fatalf("%s keeps %d rows of %d: no test of keeping some", f.name, want.Height(), n)
		}
		for _, threads := range []int{1, 4} {
			for _, size := range []int{0, 1000} {
				opts := []ursus.CollectOption{ursus.WithThreads(threads)}
				if size > 0 {
					opts = append(opts, ursus.WithBatchSize(size))
				}
				got, err := ursus.ScanParquet(path).Filter(f.p).Collect(ctx, opts...)
				if err != nil {
					t.Fatalf("%s on %d threads in batches of %d: %v", f.name, threads, size, err)
				}
				sameFrame(t, fmt.Sprintf("%s on %d threads in batches of %d", f.name, threads, size), got, want)
			}
		}
	}
}

// fallsBackToPlain reports whether a column chunk of path holds data pages of its
// dictionary's indices and PLAIN data pages both, by the pages' own encodings: the
// footer's list names PLAIN for a dictionary page too.
func fallsBackToPlain(t *testing.T, path string) bool {
	t.Helper()
	r, err := file.OpenParquetFile(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for g := range r.NumRowGroups() {
		rg := r.RowGroup(g)
		for i := range rg.NumColumns() {
			pr, err := rg.GetColumnPageReader(i)
			if err != nil {
				t.Fatal(err)
			}
			dict, plain := false, false
			for pr.Next() {
				dp, ok := pr.Page().(file.DataPage)
				if !ok {
					continue
				}
				switch parquet.Encoding(dp.Encoding()) {
				case parquet.Encodings.PlainDict, parquet.Encodings.RLEDict:
					dict = true
				case parquet.Encodings.Plain:
					plain = true
				}
			}
			pr.Close()
			if dict && plain {
				return true
			}
		}
	}
	return false
}
