package spill_test

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"testing"

	"ursus/dtype"
	"ursus/i128"
	"ursus/internal/bitmap"
	"ursus/internal/data"
	"ursus/internal/spill"
)

// roundTrip writes the batches to a temporary file and reads them back.
func roundTrip(t *testing.T, sch *dtype.Schema, in []*data.Batch) []*data.Batch {
	t.Helper()
	path := filepath.Join(t.TempDir(), "run.ursspill")

	w, err := spill.Create(path, sch)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range in {
		if err := w.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := spill.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if !r.Schema().Equal(sch) {
		t.Fatalf("schema round-tripped as %s, want %s", r.Schema(), sch)
	}

	var out []*data.Batch
	for {
		b, err := r.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
}

// render turns a column into a comparable per-row description.
//
// It errors on a type it does not know rather than returning a placeholder. A
// renderer that answers "?" for an unhandled type makes a round-trip test that
// cannot fail — which is the exact way TestMergeEquivalence was found to be
// vacuous in step 7.
func render(t *testing.T, c *data.Column) []string {
	t.Helper()
	n := c.Len()
	out := make([]string, n)
	for i := range n {
		if !c.IsValid(i) {
			out[i] = "null"
			continue
		}
		out[i] = value(t, c, i)
	}
	return out
}

func value(t *testing.T, c *data.Column, i int) string {
	t.Helper()
	dt := c.DType()
	if dt.ID() == dtype.TypeBool && !c.IsPayloadFree() {
		if c.Bools().Get(i) {
			return "true"
		}
		return "false"
	}
	if dt.HasStringStorage() && !c.IsPayloadFree() {
		return "s:" + c.Strings().Get(i)
	}
	if c.IsPayloadFree() {
		return "null"
	}
	switch dt.Physical().ID() {
	case dtype.TypeInt8:
		return fmtv(data.MustValues[int8](c)[i])
	case dtype.TypeInt16:
		return fmtv(data.MustValues[int16](c)[i])
	case dtype.TypeInt32:
		return fmtv(data.MustValues[int32](c)[i])
	case dtype.TypeInt64:
		return fmtv(data.MustValues[int64](c)[i])
	case dtype.TypeUint8:
		return fmtv(data.MustValues[uint8](c)[i])
	case dtype.TypeUint16:
		return fmtv(data.MustValues[uint16](c)[i])
	case dtype.TypeUint32:
		return fmtv(data.MustValues[uint32](c)[i])
	case dtype.TypeUint64:
		return fmtv(data.MustValues[uint64](c)[i])
	case dtype.TypeFloat32:
		// The BIT PATTERN, so NaN and -0.0 are comparable at all.
		return fmtv(math.Float32bits(data.MustValues[float32](c)[i]))
	case dtype.TypeFloat64:
		return fmtv(math.Float64bits(data.MustValues[float64](c)[i]))
	case dtype.TypeInt128:
		v := data.MustValues[i128.Int128](c)[i]
		return fmtv(v.Hi) + "/" + fmtv(v.Lo)
	default:
		t.Fatalf("render has no case for %s — the test cannot prove anything about it", dt)
		return ""
	}
}

func fmtv[T any](v T) string { return fmt.Sprint(v) }

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

func assertSame(t *testing.T, want, got []*data.Batch) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("read back %d batches, wrote %d", len(got), len(want))
	}
	for bi := range want {
		w, g := want[bi], got[bi]
		if w.Rows() != g.Rows() {
			t.Fatalf("batch %d: %d rows, wrote %d", bi, g.Rows(), w.Rows())
		}
		if w.NumCols() != g.NumCols() {
			t.Fatalf("batch %d: %d columns, wrote %d", bi, g.NumCols(), w.NumCols())
		}
		for ci := range w.NumCols() {
			wc, gc := w.Column(ci), g.Column(ci)
			if wc.DType() != gc.DType() {
				t.Errorf("batch %d column %q: type %s, wrote %s",
					bi, wc.Name(), gc.DType(), wc.DType())
			}
			ws, gs := render(t, wc), render(t, gc)
			for i := range ws {
				if ws[i] != gs[i] {
					t.Errorf("batch %d column %q row %d: %s, wrote %s",
						bi, wc.Name(), i, gs[i], ws[i])
				}
			}
		}
	}
}

// TestSpillRoundTripsEveryDtype covers every type a column can hold, INCLUDING
// the ones Parquet refuses on write — Int128, Decimal, Time, Datetime, Duration,
// Enum. Those are the reason this format exists at all, so a round-trip test that
// skipped them would be testing the easy half.
func TestSpillRoundTripsEveryDtype(t *testing.T) {
	valid := func(bits ...bool) bitmap.View {
		b := bitmap.NewBuilder(len(bits))
		for _, v := range bits {
			b.Append(v)
		}
		return b.Finish()
	}

	cols := []*data.Column{
		data.NewBool("b", valid(true, false, true), valid(true, true, false)),
		data.NewFixed("i8", dtype.Int8, []int8{-128, 0, 127}, bitmap.View{}),
		data.NewFixed("i16", dtype.Int16, []int16{-32768, 0, 32767}, bitmap.View{}),
		data.NewFixed("i32", dtype.Int32, []int32{-1, 0, 1}, valid(true, false, true)),
		data.NewFixed("i64", dtype.Int64, []int64{math.MinInt64, 0, math.MaxInt64}, bitmap.View{}),
		data.NewFixed("u8", dtype.Uint8, []uint8{0, 128, 255}, bitmap.View{}),
		data.NewFixed("u16", dtype.Uint16, []uint16{0, 1, 65535}, bitmap.View{}),
		data.NewFixed("u32", dtype.Uint32, []uint32{0, 1, math.MaxUint32}, bitmap.View{}),
		data.NewFixed("u64", dtype.Uint64, []uint64{0, 1, math.MaxUint64}, bitmap.View{}),
		// NaN, -0.0 and +Inf: the values a byte-for-byte format must not normalise.
		data.NewFixed("f32", dtype.Float32,
			[]float32{float32(math.NaN()), float32(math.Copysign(0, -1)), float32(math.Inf(1))},
			bitmap.View{}),
		data.NewFixed("f64", dtype.Float64,
			[]float64{math.NaN(), math.Copysign(0, -1), math.Inf(-1)}, bitmap.View{}),
		data.NewFixed("i128", dtype.Int128,
			[]i128.Int128{i128.FromInt64(-1), {}, {Hi: math.MaxInt64, Lo: math.MaxUint64}},
			bitmap.View{}),
		data.NewFixed("dec", dtype.Decimal(38, 9),
			[]i128.Int128{i128.FromInt64(12345), i128.FromInt64(-1), {}}, bitmap.View{}),
		data.NewString("s", []string{"", "héllo 日本語 🎉", "x"}, valid(true, true, false)),
		data.NewString("bin", []string{"\x00\xff", "", "z"}, bitmap.View{}).
			WithDType(dtype.Binary),
		data.NewFixed("date", dtype.Date, []int32{-1, 0, 20000}, bitmap.View{}),
		data.NewFixed("time", dtype.Time(dtype.Nano), []int64{0, 1, 2}, bitmap.View{}),
		data.NewFixed("dt_naive", dtype.Datetime(dtype.Micro, ""),
			[]int64{-1, 0, 1}, bitmap.View{}),
		data.NewFixed("dt_tz", dtype.Datetime(dtype.Milli, "Europe/Paris"),
			[]int64{-1, 0, 1}, bitmap.View{}),
		data.NewFixed("dur", dtype.Duration(dtype.Second), []int64{-5, 0, 5}, bitmap.View{}),
		data.NewFixed("enum", dtype.Enum("a", "b", "c"),
			[]uint32{0, 1, 2}, bitmap.View{}),
		data.NewNull("nul", dtype.Int64, 3),
		data.NewNull("nul_t", dtype.Null, 3),
	}

	fields := make([]dtype.Field, len(cols))
	for i, c := range cols {
		fields[i] = dtype.Of(c.Name(), c.DType())
	}
	sch, err := dtype.NewSchema(fields...)
	if err != nil {
		t.Fatal(err)
	}
	b, err := data.NewBatch(sch, cols)
	if err != nil {
		t.Fatal(err)
	}

	in := []*data.Batch{b}
	assertSame(t, in, roundTrip(t, sch, in))
}

// TestSpillRoundTripsNullsAndEmpty: the shapes that are easy to get wrong
// because they have no values to compare — an all-null column, a zero-row batch,
// a zero-COLUMN batch that still has rows.
func TestSpillRoundTripsNullsAndEmpty(t *testing.T) {
	sch, err := dtype.NewSchema(
		dtype.Of("v", dtype.Int64),
		dtype.Of("s", dtype.String),
		dtype.Of("b", dtype.Bool),
	)
	if err != nil {
		t.Fatal(err)
	}

	allNull, err := data.NewBatch(sch, []*data.Column{
		data.NewFixed("v", dtype.Int64, []int64{0, 0}, bitmap.Zeros(2)),
		data.NewString("s", []string{"", ""}, bitmap.Zeros(2)),
		data.NewBool("b", bitmap.Zeros(2), bitmap.Zeros(2)),
	})
	if err != nil {
		t.Fatal(err)
	}
	empty, err := data.NewBatch(sch, []*data.Column{
		data.NewFixed("v", dtype.Int64, []int64{}, bitmap.View{}),
		data.NewString("s", nil, bitmap.View{}),
		data.NewBool("b", bitmap.View{}, bitmap.View{}),
	})
	if err != nil {
		t.Fatal(err)
	}

	in := []*data.Batch{allNull, empty, allNull}
	assertSame(t, in, roundTrip(t, sch, in))

	// A frame can legitimately have rows and no columns, after Select() with
	// nothing. NewBatch cannot express that, which is exactly why NewBatchRows
	// exists — and why the format writes the row count separately.
	wide, err := dtype.NewSchema()
	if err != nil {
		t.Fatal(err)
	}
	rowsOnly := data.NewBatchRows(wide, nil, 7)
	got := roundTrip(t, wide, []*data.Batch{rowsOnly})
	if len(got) != 1 || got[0].Rows() != 7 || got[0].NumCols() != 0 {
		t.Errorf("a zero-column batch with rows did not survive: %v", got)
	}
}

// TestSpillRebasesSlicedStrings: a sliced String column keeps the ORIGINAL
// character buffer and re-windows its offsets, so a writer that dumped the buffer
// as it stands would spill every character the column no longer refers to — and
// still read back correctly, which is why this checks the file SIZE as well as
// the values.
func TestSpillRebasesSlicedStrings(t *testing.T) {
	big := make([]string, 200)
	for i := range big {
		big[i] = "a fairly long string that is not worth spilling twice"
	}
	sch, err := dtype.NewSchema(dtype.Of("s", dtype.String))
	if err != nil {
		t.Fatal(err)
	}
	full, err := data.NewBatch(sch, []*data.Column{
		data.NewString("s", big, bitmap.View{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	sliced := full.Slice(190, 10)

	path := filepath.Join(t.TempDir(), "run.ursspill")
	w, err := spill.Create(path, sch)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Write(sliced); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	assertSame(t, []*data.Batch{sliced}, roundTrip(t, sch, []*data.Batch{sliced}))

	whole := int64(len(big)) * int64(len(big[0]))
	if size := fileSize(t, path); size > whole/4 {
		t.Errorf("spilling 10 of 200 rows wrote %d bytes; the whole buffer is %d",
			size, whole)
	}
}

// TestSpillRefusesNestedTypes: List, Array and Struct have no data.Column
// representation, so the format says so rather than writing something it cannot
// read back.
func TestSpillRefusesNestedTypes(t *testing.T) {
	sch, err := dtype.NewSchema(dtype.Of("l", dtype.List(dtype.Int64)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := spill.Create(filepath.Join(t.TempDir(), "x"), sch); err == nil {
		t.Fatal("a List column was accepted")
	}
}

// TestSpillValidityIsNotOversized is the bitmap half of the rebasing story.
//
// TestSpillRebasesSlicedStrings pins the character buffer, and uses an ALL-SET
// validity — so it proves nothing about the validity path, which had the same
// bug: bitmap.View.Buffer() returns the whole backing array when the bit offset
// is zero, and Column.Slice(0, k) produces exactly that. A 100-row slice of an
// 8192-row nullable column wrote 1024 validity bytes instead of 13.
//
// It never produced a wrong answer, because the reader reads n bits — which is
// exactly why only a size assertion can see it.
func TestSpillValidityIsNotOversized(t *testing.T) {
	const total, keep = 8192, 100
	valid := bitmap.NewBuilder(total)
	for i := range total {
		valid.Append(i%3 != 0)
	}
	vals := make([]int8, total) // one byte per row, so validity is not lost in the noise

	sch, err := dtype.NewSchema(dtype.Of("v", dtype.Int8))
	if err != nil {
		t.Fatal(err)
	}
	full, err := data.NewBatch(sch, []*data.Column{
		data.NewFixed("v", dtype.Int8, vals, valid.Finish()),
	})
	if err != nil {
		t.Fatal(err)
	}
	sliced := full.Slice(0, keep) // offset ZERO: the case Buffer() cannot narrow

	path := filepath.Join(t.TempDir(), "run.ursspill")
	w, err := spill.Create(path, sch)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Write(sliced); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	assertSame(t, []*data.Batch{sliced}, roundTrip(t, sch, []*data.Batch{sliced}))

	// 100 values plus 13 validity bytes plus a small header. The old behaviour wrote
	// the whole 1024-byte bitmap, so anything near that is the bug.
	if size := fileSize(t, path); size > 300 {
		t.Errorf("spilling %d of %d rows wrote %d bytes; the validity bitmap for %d "+
			"rows is %d bytes", keep, total, size, keep, (keep+7)/8)
	}
}
