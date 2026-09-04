package ursus_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	arrowpq "github.com/apache/arrow-go/v18/parquet"

	"ursus"
	"ursus/dtype"
	"ursus/i128"
	"ursus/internal/bitmap"
	"ursus/internal/data"
	"ursus/internal/plan"
	"ursus/internal/source/memsrc"
	"ursus/internal/source/parquet"
	"ursus/internal/uerr"
	"ursus/ursustest"
)

// TestParquetRoundTrip is the strongest correctness check available for a reader:
// write a frame, read it back, and require it to be identical — values, nulls,
// dtypes and column order.
//
// Every supported type appears, and every column has nulls in DIFFERENT positions.
// Nulls are the whole difficulty of a Parquet reader: values come back packed with
// no gaps where the nulls are, so a reader that writes values[i] to row i shifts
// everything after the first null. Identical null positions across columns would
// let a consistent shift look correct.
func TestParquetRoundTrip(t *testing.T) {
	const n = 7
	valid := func(pattern string) bitmap.View {
		b := bitmap.NewBuilder(n)
		for i := range n {
			b.Append(pattern[i] == '1')
		}
		return b.Finish()
	}

	fields := []dtype.Field{
		{Name: "b", Type: dtype.Bool, Nullable: true},
		{Name: "i8", Type: dtype.Int8, Nullable: true},
		{Name: "i16", Type: dtype.Int16, Nullable: true},
		{Name: "i32", Type: dtype.Int32, Nullable: true},
		{Name: "i64", Type: dtype.Int64, Nullable: true},
		{Name: "u8", Type: dtype.Uint8, Nullable: true},
		{Name: "u32", Type: dtype.Uint32, Nullable: true},
		{Name: "u64", Type: dtype.Uint64, Nullable: true},
		{Name: "f32", Type: dtype.Float32, Nullable: true},
		{Name: "f64", Type: dtype.Float64, Nullable: true},
		{Name: "s", Type: dtype.String, Nullable: true},
		{Name: "dec", Type: dtype.Decimal(10, 2), Nullable: true},
		{Name: "allnull", Type: dtype.Int64, Nullable: true},
	}
	schema, err := dtype.NewSchema(fields...)
	if err != nil {
		t.Fatal(err)
	}

	boolVals := bitmap.NewBuilder(n)
	for _, v := range []bool{true, false, true, true, false, false, true} {
		boolVals.Append(v)
	}

	cols := []*data.Column{
		data.NewBool("b", boolVals.Finish(), valid("1011010")),
		data.NewFixed("i8", dtype.Int8, []int8{-128, 0, 1, 2, 3, 4, 127}, valid("1101101")),
		data.NewFixed("i16", dtype.Int16, []int16{-32768, 0, 1, 2, 3, 4, 32767}, valid("0111110")),
		data.NewFixed("i32", dtype.Int32, []int32{-2147483648, 0, 1, 2, 3, 4, 2147483647}, valid("1111111")),
		data.NewFixed("i64", dtype.Int64,
			[]int64{-9223372036854775808, 0, 1, 2, 3, 4, 9223372036854775807}, valid("1010101")),
		data.NewFixed("u8", dtype.Uint8, []uint8{0, 1, 2, 3, 4, 5, 255}, valid("0101010")),
		data.NewFixed("u32", dtype.Uint32, []uint32{0, 1, 2, 3, 4, 5, 4294967295}, valid("1110001")),
		// Above 2^63, which is where a uint64 written as an int64 wraps negative.
		data.NewFixed("u64", dtype.Uint64,
			[]uint64{0, 1, 2, 3, 4, 5, 18446744073709551615}, valid("1000011")),
		data.NewFixed("f32", dtype.Float32, []float32{-1.5, 0, 0.25, 1, 2, 3, 1e30}, valid("1111011")),
		data.NewFixed("f64", dtype.Float64, []float64{-1.5, 0, 0.25, 1, 2, 3, 1e300}, valid("0011111")),
		data.NewString("s", []string{"", "a", "with,comma", "üñí", "x", "y", "z"}, valid("1101011")),
		data.NewFixed("dec", dtype.Decimal(10, 2),
			[]i128.Int128{
				i128.FromInt64(1234), i128.FromInt64(-5), i128.FromInt64(0),
				i128.FromInt64(10000), i128.FromInt64(1), i128.FromInt64(-99999999),
				i128.FromInt64(99999999),
			}, valid("1011101")),
		data.NewFixed("allnull", dtype.Int64, make([]int64, n), valid("0000000")),
	}

	b, err := data.NewBatch(schema, cols)
	if err != nil {
		t.Fatal(err)
	}
	memSrc, err := memsrc.New(schema, b)
	if err != nil {
		t.Fatal(err)
	}
	src := ursus.Scan(memSrc)

	want, err := src.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "rt.parquet")
	if err := src.SinkParquet(t.Context(), path); err != nil {
		t.Fatal(err)
	}

	got, err := ursus.ScanParquet(path).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != want.String() {
		t.Errorf("round trip changed the data\ngot:\n%s\nwant:\n%s", got, want)
	}
	if !got.Schema().Equal(want.Schema()) {
		t.Errorf("round trip changed the schema\ngot:  %s\nwant: %s", got.Schema(), want.Schema())
	}
}

// pqNumbers writes a file with one row group per chunk of `perGroup` rows, so
// pruning has something to prune.
func pqNumbers(t *testing.T, rows, perGroup int) string {
	t.Helper()

	vals := make([]int64, rows)
	words := make([]string, rows)
	for i := range rows {
		vals[i] = int64(i)
		words[i] = "w" + strconv.Itoa(i)
	}
	path := filepath.Join(t.TempDir(), "n.parquet")
	err := ursus.Frame(ursus.Values("n", vals), ursus.Values("w", words)).
		SinkParquet(t.Context(), path, ursus.WithRowGroupRows(perGroup))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// TestParquetPruningIsSound is the test that matters most for the pruner, because
// the failure mode is not an error — it is missing rows.
//
// Pruning must never change the answer. Every query runs with pruning on and off
// and must return identical results.
func TestParquetPruningIsSound(t *testing.T) {
	path := pqNumbers(t, 500, 50)

	queries := []struct {
		name string
		f    func(*ursus.LazyFrame) *ursus.LazyFrame
	}{
		{"gt", func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.Filter(ursus.Col("n").Gt(400))
		}},
		{"lt", func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.Filter(ursus.Col("n").Lt(50))
		}},
		{"eq", func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.Filter(ursus.Col("n").Eq(123))
		}},
		{"ne", func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.Filter(ursus.Col("n").Ne(123))
		}},
		{"ge and le", func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.Filter(ursus.Col("n").Ge(100)).Filter(ursus.Col("n").Le(200))
		}},
		{"literal on the left", func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.Filter(ursus.Lit(400).Lt(ursus.Col("n")))
		}},
		{"matches nothing", func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.Filter(ursus.Col("n").Gt(100000))
		}},
		{"matches everything", func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.Filter(ursus.Col("n").Ge(0))
		}},
		{"string comparison", func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.Filter(ursus.Col("w").Gt("w4"))
		}},
		{"is not null", func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.Filter(ursus.Col("n").IsNotNull())
		}},
		{"is null", func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.Filter(ursus.Col("n").IsNull())
		}},
		{"filter then project", func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.Filter(ursus.Col("n").Gt(400)).Select(ursus.Col("w"))
		}},
		{"filter then aggregate", func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.Filter(ursus.Col("n").Gt(400)).
				GroupBy(ursus.Lit(1).Alias("k")).Agg(ursus.Len().Alias("n_rows"))
		}},
	}

	for _, q := range queries {
		t.Run(q.name, func(t *testing.T) {
			on, err := q.f(ursus.ScanParquet(path)).Collect(t.Context(), ursus.WithVerify())
			if err != nil {
				t.Fatalf("pruning on: %v", err)
			}
			off, err := q.f(ursus.ScanParquet(path, ursus.WithPruning(false))).
				Collect(t.Context(), ursus.WithOptFlags(plan.NoFlags()))
			if err != nil {
				t.Fatalf("pruning off: %v", err)
			}
			if on.String() != off.String() {
				t.Errorf("pruning changed the result\nwith:\n%s\nwithout:\n%s", on, off)
			}
		})
	}
}

// TestParquetPruningActuallySkips proves the pruner does something, not merely
// nothing safely. A predicate matching only the last row group must read far fewer
// rows than the file holds.
//
// Row counts are the observable: with an inexact pushdown the Filter above the scan
// still runs, so the final answer is identical either way. What differs is how much
// was read, which is what Count over an unfiltered frame reveals.
func TestParquetPruningActuallySkips(t *testing.T) {
	path := pqNumbers(t, 500, 50)

	// Rows 450-499 are in the last row group of ten.
	df, err := ursus.ScanParquet(path).
		Filter(ursus.Col("n").Ge(450)).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 50 {
		t.Fatalf("got %d rows, want 50\n%s", df.Height(), df)
	}

	// The counter is the only thing that can see pruning happen. An inexact
	// pushdown leaves the Filter in place, so a pruner that skips NOTHING returns
	// exactly these same 50 rows — the difference is only in what was read.
	src := parquet.New([]parquet.Opener{fileOpener(t, path)}, path, parquet.DefaultOptions())
	if _, err := ursus.Scan(src).Filter(ursus.Col("n").Ge(450)).
		Collect(t.Context(), ursus.WithVerify()); err != nil {
		t.Fatal(err)
	}
	read, skipped := src.RowGroupStats()
	if skipped != 9 || read != 1 {
		t.Errorf("read %d row groups and skipped %d, want 1 read and 9 skipped — "+
			"only the last group can hold n >= 450", read, skipped)
	}

	// The plan must show the conjunct reaching the scan AND the Filter surviving:
	// statistics prove a group cannot match, never that a row does.
	p, err := ursus.ScanParquet(path).Filter(ursus.Col("n").Ge(450)).Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p, "predicate: [") {
		t.Errorf("the conjunct did not reach the scan:\n%s", p)
	}
	if !strings.Contains(p, "FILTER") {
		t.Errorf("an INEXACT pushdown must keep the Filter above the scan:\n%s", p)
	}
}

// TestParquetTruncatedStatsAreNotTrusted covers arrow-go trap number three.
//
// ApplyStatSizeLimits drops an oversized min or max INDEPENDENTLY, while the read
// path sets hasMinMax = IsSetMax() || IsSetMin(). A column holding one 5 KB string
// therefore reads back as HasMinMax() == true with Max == "", and a pruner that
// trusts that pair decides `w > "z"` cannot match and skips a group full of
// matches.
//
// The defence is the min > max sanity check. This test writes exactly that file.
func TestParquetTruncatedStatsAreNotTrusted(t *testing.T) {
	const rows = 200
	words := make([]string, rows)
	for i := range rows {
		words[i] = "zz" + strconv.Itoa(i)
	}
	// One value far past the 4096-byte default stats limit, so its bound is dropped.
	words[rows-1] = strings.Repeat("z", 5000)

	path := filepath.Join(t.TempDir(), "big.parquet")
	if err := ursus.Frame(ursus.Values("w", words)).
		SinkParquet(t.Context(), path, ursus.WithRowGroupRows(rows)); err != nil {
		t.Fatal(err)
	}

	on, err := ursus.ScanParquet(path).Filter(ursus.Col("w").Gt("z")).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	off, err := ursus.ScanParquet(path, ursus.WithPruning(false)).
		Filter(ursus.Col("w").Gt("z")).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if on.Height() != off.Height() {
		t.Errorf("a truncated statistic caused %d rows to be skipped (%d vs %d)",
			off.Height()-on.Height(), on.Height(), off.Height())
	}
	if on.Height() == 0 {
		t.Error("the fixture should match every row; it proves nothing otherwise")
	}
}

// TestParquetAllNullRowGroupIsPrunable covers arrow-go trap number two.
//
// Statistics().NumValues() is the NON-NULL count, so the natural-looking
// "NullCount == NumValues means every row is null" is always false and would
// silently never prune. The row count lives on the column chunk.
func TestParquetAllNullRowGroupIsPrunable(t *testing.T) {
	const n = 100
	vals := make([]int64, n)
	valid := make([]bool, n)
	for i := range n {
		vals[i] = int64(i)
		valid[i] = i >= n/2 // the first half is null
	}
	path := filepath.Join(t.TempDir(), "nulls.parquet")
	if err := ursus.Frame(ursus.ValuesNullable("n", vals, valid)).
		SinkParquet(t.Context(), path, ursus.WithRowGroupRows(n/2)); err != nil {
		t.Fatal(err)
	}

	on, err := ursus.ScanParquet(path).Filter(ursus.Col("n").Gt(0)).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	off, err := ursus.ScanParquet(path, ursus.WithPruning(false)).
		Filter(ursus.Col("n").Gt(0)).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if on.String() != off.String() {
		t.Errorf("pruning an all-null row group changed the result\nwith:\n%s\nwithout:\n%s",
			on, off)
	}
}

// TestParquetProjectionSkipsColumns: an unselected column must never be opened.
// On a wide file this is the difference between reading two columns and a hundred.
func TestParquetProjectionSkipsColumns(t *testing.T) {
	path := pqNumbers(t, 100, 100)

	df, err := ursus.ScanParquet(path).Select(ursus.Col("w")).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if df.Width() != 1 || df.Columns()[0] != "w" {
		t.Errorf("got columns %v, want [w]", df.Columns())
	}

	p, err := ursus.ScanParquet(path).Select(ursus.Col("w")).Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p, "projection: [w] (1/2 cols)") {
		t.Errorf("the projection did not reach the scan:\n%s", p)
	}
}

// TestParquetBatchSizeInvariance: batch size must never change the answer, and for
// Parquet it also exercises reading across row-group boundaries at odd offsets.
func TestParquetBatchSizeInvariance(t *testing.T) {
	path := pqNumbers(t, 137, 40) // neither divides the other

	// AssertFrameEqual rather than df.String(): this filter keeps ~86 rows and
	// String renders ten of them, so the comparison was checking an eighth of the
	// result and calling it invariance.
	var want *ursus.DataFrame
	for _, size := range []int{1, 7, 40, 41, 137, 4096} {
		df, err := ursus.ScanParquet(path).
			Filter(ursus.Col("n").Gt(50)).
			Collect(t.Context(), ursus.WithBatchSize(size), ursus.WithVerify())
		if err != nil {
			t.Fatalf("batch size %d: %v", size, err)
		}
		if want == nil {
			want = df
			continue
		}
		ursustest.AssertFrameEqual(t, df, want)
	}
}

// TestParquetLimitStopsEarly: Head(n) must not read the whole file.
func TestParquetLimitStopsEarly(t *testing.T) {
	path := pqNumbers(t, 1000, 100)

	df, err := ursus.ScanParquet(path).Head(5).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 5 {
		t.Errorf("got %d rows, want 5\n%s", df.Height(), df)
	}
	p, err := ursus.ScanParquet(path).Head(5).Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p, "max rows: 5") {
		t.Errorf("the limit did not reach the scan:\n%s", p)
	}
}

// TestParquetMultipleFiles reads a partitioned dataset as one frame.
func TestParquetMultipleFiles(t *testing.T) {
	dir := t.TempDir()
	for i := range 3 {
		vals := []int64{int64(i*2 + 1), int64(i*2 + 2)}
		p := filepath.Join(dir, "part-"+strconv.Itoa(i)+".parquet")
		if err := ursus.Frame(ursus.Values("n", vals)).SinkParquet(t.Context(), p); err != nil {
			t.Fatal(err)
		}
	}

	df, err := ursus.ScanParquetGlob(filepath.Join(dir, "part-*.parquet")).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 6 {
		t.Fatalf("got %d rows, want 6\n%s", df.Height(), df)
	}
	for i := range 6 {
		v, _, err := df.At[int64](i, "n")
		if err != nil {
			t.Fatal(err)
		}
		if v != int64(i+1) {
			t.Errorf("row %d = %d, want %d — parts must read in name order\n%s",
				i, v, i+1, df)
		}
	}
}

// TestParquetRefusesUnsupported checks that the refusals name the column and are
// user-facing errors rather than reported ursus bugs.
func TestParquetRefusesUnsupported(t *testing.T) {
	// Writing an unsupported type is the reachable half; a nested or oddly-encoded
	// FILE needs a writer ursus does not have, and the refusal for those lives in
	// toDataType, covered by TestParquetTypeRefusals in the parquet package.
	//
	// The type here is Duration, which has no Parquet logical type at all —
	// arrow-go's IntervalLogicalType.toThrift panics — rather than Datetime, which
	// step 14 made writable. Step 6 hit the same thing from the CSV side and recorded
	// the lesson: a refusal test pinned to whichever type happens to be unwritable
	// this month breaks every time the format grows.
	err := ursus.Frame(ursus.Values("x", []int64{1, 2})).
		Select(ursus.Col("x").Cast(dtype.Duration(dtype.Micro))).
		SinkParquet(t.Context(), filepath.Join(t.TempDir(), "x.parquet"))
	if err == nil {
		t.Fatal("expected a refusal for an unwritable type")
	}
	if !errors.Is(err, uerr.ErrUnsupported) {
		t.Errorf("kind should be Unsupported: %v", err)
	}
	if !strings.Contains(err.Error(), `"x"`) {
		t.Errorf("the error should name the column: %v", err)
	}
	if errors.Is(err, uerr.ErrInternal) {
		t.Errorf("a missing feature is not an ursus bug: %v", err)
	}
}

// TestSinkParquetIsAtomic: a Parquet file without its footer is unreadable rather
// than merely truncated, so a failed sink must leave nothing behind.
func TestSinkParquetIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.parquet")

	// A failing strict CAST, not an unwritable type. Atomicity is a property of a
	// failed sink whatever made it fail, and pinning it to a type is what made this
	// test break when step 14 made Datetime writable — the second time the suite has
	// learned that, after step 6.
	err := ursus.Frame(ursus.Values("x", []int64{1, 2000})).
		Select(ursus.Col("x").Cast(dtype.Int8)).
		SinkParquet(t.Context(), path, ursus.WithVerify())
	if err == nil {
		t.Fatal("expected the sink to fail")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("a failed sink left files behind: %v", names)
	}
}

// TestParquetStreamsWithoutMaterialising: the reader and writer together must hold
// a row group, not the result. This is "larger than RAM" for Parquet.
func TestParquetStreamsWithoutMaterialising(t *testing.T) {
	const rows = 200_000
	in := pqNumbers(t, rows, 8192)
	out := filepath.Join(t.TempDir(), "out.parquet")

	before := heapInUse()
	if err := ursus.ScanParquet(in).SinkParquet(t.Context(), out,
		ursus.WithRowGroupRows(8192)); err != nil {
		t.Fatal(err)
	}
	growth := int64(heapInUse()) - int64(before)

	// Loose on purpose: this catches "accumulated every batch", not an allocation
	// regression. The whole result is tens of megabytes.
	if growth > 32<<20 {
		t.Errorf("heap grew by %d bytes streaming %d rows; something is accumulating",
			growth, rows)
	}

	n, err := ursus.ScanParquet(out).Count(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if n != rows {
		t.Errorf("wrote %d rows, want %d", n, rows)
	}
}

// TestParquetIsLazy: building a frame over a missing file must not touch the disk.
func TestParquetIsLazy(t *testing.T) {
	lf := ursus.ScanParquet(filepath.Join(t.TempDir(), "nope.parquet"))
	if lf.Err() != nil {
		t.Fatalf("building the frame must not open the file: %v", lf.Err())
	}
	if _, err := lf.Collect(t.Context()); err == nil {
		t.Error("collecting a missing file should fail")
	}
}

// TestCSVToParquet is the end-to-end shape the library exists for: read one format,
// transform, write another, without either side fitting in memory.
func TestCSVToParquet(t *testing.T) {
	csvPath := writeFile(t, "sales.csv", salesCSV)
	pqPath := filepath.Join(t.TempDir(), "sales.parquet")

	err := ursus.ScanCSV(csvPath).
		Filter(ursus.Col("qty").Gt(1)).
		Select(ursus.Col("region"), ursus.Col("qty")).
		SinkParquet(t.Context(), pqPath)
	if err != nil {
		t.Fatal(err)
	}

	df, err := ursus.ScanParquet(pqPath).
		GroupBy(ursus.Col("region")).
		Agg(ursus.Len().Alias("n")).
		Sort(ursus.Asc(ursus.Col("region"))).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 3 { // apac, eu, us
		t.Errorf("got %d groups, want 3\n%s", df.Height(), df)
	}
}

// fileOpener mirrors the unexported opener in parquet.go, so a test can build a
// Source directly and read its counters.
func fileOpener(t *testing.T, path string) parquet.Opener {
	t.Helper()
	return func() (arrowpq.ReaderAtSeeker, io.Closer, error) {
		f, err := os.Open(path)
		if err != nil {
			return nil, nil, err
		}
		return f, f, nil
	}
}

// TestTemporalParquetRoundTrip: the .dt namespace shipped in step 6 and was
// unreachable from Parquet in either direction until step 14 — the writer refused
// TIME and TIMESTAMP and the reader refused them back.
//
// The assertion is the one TestTemporalCSVRoundTrip makes: the writer's output must
// be readable by its own reader. No WithSchema here, because Parquet carries its
// schema in the footer where CSV has to be told.
func TestTemporalParquetRoundTrip(t *testing.T) {
	for _, c := range []struct {
		name string
		dt   dtype.DataType
	}{
		{"datetime_ms_naive", dtype.Datetime(dtype.Milli, "")},
		{"datetime_us_naive", dtype.Datetime(dtype.Micro, "")},
		{"datetime_ns_utc", dtype.Datetime(dtype.Nano, "UTC")},
		{"time_ms", dtype.Time(dtype.Milli)}, // the one Parquet puts on INT32
		{"time_us", dtype.Time(dtype.Micro)},
		{"time_ns", dtype.Time(dtype.Nano)},
		{"date", dtype.Date},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "t.parquet")
			// A null in the middle, because def-level expansion is where a temporal
			// reader would go wrong without anyone noticing the values shifted.
			want := ursus.Frame(ursus.ValuesNullable("t",
				[]int64{0, 1, 86_400_000, 0, -1},
				[]bool{true, true, true, false, true})).
				Select(ursus.Col("t").CastLossy(c.dt))

			if err := want.SinkParquet(t.Context(), path); err != nil {
				t.Fatal(err)
			}
			got, err := ursus.ScanParquet(path).Collect(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			ref, err := want.Collect(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if !got.Schema().Equal(ref.Schema()) {
				t.Fatalf("schema changed: %s -> %s", ref.Schema(), got.Schema())
			}
			ursustest.AssertFrameEqual(t, got, ref)
		})
	}
}

// TestParquetInt128RoundTrip: every integer Sum outputs Int128, so before step 14 a
// group-by could not sink its own most common output.
//
// The read-back TYPE is asserted to be Decimal(38, 0), not Int128 — that asymmetry is
// real and is the price of Parquet having no other 128-bit integer. Asserting it
// keeps it a documented decision rather than a surprise.
func TestParquetInt128RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "i128.parquet")
	src := ursus.Frame(
		ursus.Values("g", []string{"a", "a", "b"}),
		ursus.Values("v", []int64{1 << 62, 1 << 62, -5}),
	)
	if err := src.GroupBy(ursus.Col("g")).MaintainOrder().
		Agg(ursus.Col("v").Sum().Alias("s")).
		SinkParquet(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	got, err := ursus.ScanParquet(path).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got.Height() != 2 {
		t.Fatalf("%d rows, want 2", got.Height())
	}
	if f := got.Schema().Field(1); f.Type.ID() != dtype.TypeDecimal {
		t.Errorf("the sum read back as %s; Parquet's only 128-bit integer is "+
			"DECIMAL(38,0), so Decimal is expected", f.Type)
	}
	// 2^62 + 2^62 = 2^63, which does not fit in an Int64 — the value that makes
	// Int128 the accumulator type in the first place.
	v, _, err := got.At[i128.Int128](0, "s")
	if err != nil {
		t.Fatal(err)
	}
	if v.String() != "9223372036854775808" {
		t.Errorf("sum = %s, want 9223372036854775808 (2^63)", v)
	}
}

// TestParquetRefusesSecondResolution: arrow-go's createTimeUnit PANICS for anything
// but milli, micro and nano, and dtype.Second is ursus's zero value — so this is a
// guard against taking the process down, not a taste refusal.
func TestParquetRefusesSecondResolution(t *testing.T) {
	for _, dt := range []dtype.DataType{
		dtype.Datetime(dtype.Second, ""),
		dtype.Time(dtype.Second),
	} {
		err := ursus.Frame(ursus.Values("x", []int64{1})).
			Select(ursus.Col("x").CastLossy(dt)).
			SinkParquet(t.Context(), filepath.Join(t.TempDir(), "x.parquet"))
		if err == nil {
			t.Fatalf("%s was accepted; arrow-go panics on a second resolution", dt)
		}
		if !strings.Contains(err.Error(), "second resolution") {
			t.Errorf("the message should name the cause:\n%v", err)
		}
	}
}
