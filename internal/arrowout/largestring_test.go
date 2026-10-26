package arrowout_test

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/arrowout"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
)

// textArray is an exported String, LargeString, Binary or LargeBinary's rows.
func textArray(t *testing.T, a arrow.Array) []string {
	t.Helper()
	out := make([]string, a.Len())
	for i := range out {
		switch v := a.(type) {
		case *array.String:
			out[i] = v.Value(i)
		case *array.LargeString:
			out[i] = v.Value(i)
		case *array.Binary:
			out[i] = string(v.Value(i))
		case *array.LargeBinary:
			out[i] = string(v.Value(i))
		default:
			t.Fatalf("exported as %T", a)
		}
	}
	return out
}

// TestTextPastItsOffsetsExportsLarge: a String column of 64-bit offsets (step 368),
// past MaxStringBytes, lowered to 64, is a LargeString, a Binary a LargeBinary, and
// a list or struct of one says so in its type and its record's schema; a slice of
// one that fits is a String, its offsets rebased; RecordLarge makes every text large.
func TestTextPastItsOffsetsExportsLarge(t *testing.T) {
	defer func(old int64) { data.MaxStringBytes = old }(data.MaxStringBytes)
	data.MaxStringBytes = 64

	vals := make([]string, 30)
	for i := range vals {
		vals[i] = fmt.Sprintf("row %02d", i) // 180 bytes
	}
	wide := data.NewString("s", vals, bitmap.View{})
	if wide.RawOffsets64() == nil {
		t.Fatal("a column past the limit has 32-bit offsets")
	}

	same := func(what string, a arrow.Array, want []string) {
		t.Helper()
		got := textArray(t, a)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: %q, want %q", what, got, want)
		}
	}
	column := func(c *data.Column) arrow.Array {
		t.Helper()
		a, err := arrowout.Column(c)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(a.Release)
		return a
	}

	t.Run("past the limit, large", func(t *testing.T) {
		a := column(wide)
		if a.DataType().ID() != arrow.LARGE_STRING {
			t.Fatalf("exported as %s", a.DataType())
		}
		same("the column", a, vals)
		b := column(wide.WithDType(dtype.Binary))
		if b.DataType().ID() != arrow.LARGE_BINARY {
			t.Fatalf("Binary exported as %s", b.DataType())
		}
		same("as Binary", b, vals)
		same("a slice past it", column(wide.Slice(10, 20)), vals[10:30])
	})

	t.Run("a slice that fits, rebased", func(t *testing.T) {
		a := column(wide.Slice(20, 5))
		if a.DataType().ID() != arrow.STRING {
			t.Fatalf("exported as %s", a.DataType())
		}
		if o := a.(*array.String).ValueOffsets(); o[0] != 0 {
			t.Errorf("offsets from %d, not rebased", o[0])
		}
		same("the slice", a, vals[20:25])
	})

	t.Run("nested", func(t *testing.T) {
		list := data.NewList("l", []int32{0, 10, 10, 30}, wide, bitmap.AllSet(3))
		l := column(list)
		lt, ok := l.DataType().(*arrow.ListType)
		if !ok || lt.Elem().ID() != arrow.LARGE_STRING {
			t.Fatalf("a list of it exported as %s", l.DataType())
		}
		same("the list's elements", l.(*array.List).ListValues(), vals)
		st := column(data.NewStruct("st", []*data.Column{wide}, bitmap.AllSet(30)))
		if f := st.DataType().(*arrow.StructType).Field(0); f.Type.ID() != arrow.LARGE_STRING {
			t.Fatalf("a struct of it exported as %s", st.DataType())
		}
		same("the struct's field", st.(*array.Struct).Field(0), vals)

		sch := dtype.MustSchema(dtype.Of("s", dtype.String), dtype.Of("l", list.DType()))
		b, err := data.NewBatch(sch, []*data.Column{wide.Slice(0, 3), list})
		if err != nil {
			t.Fatal(err)
		}
		rec, err := arrowout.Record(b)
		if err != nil {
			t.Fatal(err)
		}
		defer rec.Release()
		if got := rec.Schema().Field(0).Type.ID(); got != arrow.STRING {
			t.Errorf("a slice that fits in a record: %s", rec.Schema().Field(0).Type)
		}
		if !arrow.TypeEqual(rec.Schema().Field(1).Type, l.DataType()) {
			t.Errorf("the record's schema says %s for a column of %s",
				rec.Schema().Field(1).Type, l.DataType())
		}
	})

	t.Run("RecordLarge", func(t *testing.T) {
		narrow := data.NewString("s", vals[:4], bitmap.View{})
		nulls := data.NewNull("n", dtype.Binary, 4)
		sch := dtype.MustSchema(dtype.Of("s", dtype.String), dtype.Of("n", dtype.Binary))
		b, err := data.NewBatch(sch, []*data.Column{narrow, nulls})
		if err != nil {
			t.Fatal(err)
		}
		rec, err := arrowout.RecordLarge(b)
		if err != nil {
			t.Fatal(err)
		}
		defer rec.Release()
		if s, n := rec.Column(0).DataType().ID(), rec.Column(1).DataType().ID(); s != arrow.LARGE_STRING || n != arrow.LARGE_BINARY {
			t.Fatalf("exported as %s and %s", rec.Column(0).DataType(), rec.Column(1).DataType())
		}
		same("a 32-bit column, widened", rec.Column(0), vals[:4])
		if rec.Column(1).NullN() != 4 {
			t.Errorf("%d nulls of 4", rec.Column(1).NullN())
		}
	})
}

// TestTextPastTwoGiB builds a real String column of 3 GiB, its offsets 64-bit, past
// any int32: two copies of a 1.5 GiB column of 1 KiB rows concatenated, and reads
// rows on both sides of 2^31, slices, takes and exports it (step 368). It holds about
// 4.6 GB, so it runs only with URSUS_BIG_STRINGS=1, in CI's bigstrings job;
// URSUS_BIG_STRINGS=small runs it at 3 MiB under a limit of 2 MiB, to check the test.
func TestTextPastTwoGiB(t *testing.T) {
	mode := os.Getenv("URSUS_BIG_STRINGS")
	if mode == "" {
		t.Skip("URSUS_BIG_STRINGS is not set: it holds about 4.6 GB")
	}
	if mode == "small" {
		defer func(old int64) { data.MaxStringBytes = old }(data.MaxStringBytes)
		data.MaxStringBytes = 2 << 20
	}
	const width = 1 << 10
	limit := data.MaxStringBytes + 1 // 2^31, where an int32 offset wraps
	half := int(limit*3/4) / width   // rows of each copy, 1.5 GiB of them
	row := func(i int) string {
		r := i % half
		return fmt.Sprintf("%08d", r) + strings.Repeat(string(rune('a'+r%26)), width-8)
	}
	offs := make([]int32, half+1)
	chars := make([]byte, half*width)
	for r := range half {
		offs[r+1] = int32((r + 1) * width)
		copy(chars[r*width:], row(r))
	}
	c := data.NewStringOwned("s", offs, chars, bitmap.View{})
	n := 2 * half
	wide := data.ConcatStrings("s", dtype.String, []*data.Column{c, c}, bitmap.AllSet(n))
	if wide.RawOffsets64() == nil {
		t.Fatal("3 GiB of text with 32-bit offsets")
	}
	o := data.Reinterpret[int64](wide.RawOffsets64())
	if o[n] != int64(n)*width || o[n] <= limit {
		t.Fatalf("the last offset is %d, want %d, past %d", o[n], int64(n)*width, limit)
	}
	past := int(limit / width) // the first row past 2^31
	rows := []int{0, half - 1, half, past - 1, past, n - 1}
	a := wide.Strings()
	for _, i := range rows {
		if got := a.Get(i); got != row(i) {
			t.Fatalf("row %d is %.12q…, want %.12q…", i, got, row(i))
		}
	}

	t.Run("a slice across 2^31", func(t *testing.T) {
		s := wide.Slice(past-3, 6)
		for j := range 6 {
			if got := s.Strings().Get(j); got != row(past-3+j) {
				t.Fatalf("slice row %d is %.12q…", j, got)
			}
		}
		arr, err := arrowout.Column(s)
		if err != nil {
			t.Fatal(err)
		}
		defer arr.Release()
		if arr.DataType().ID() != arrow.STRING {
			t.Fatalf("a slice of 6 KiB exported as %s", arr.DataType())
		}
		for j, v := range textArray(t, arr) {
			if v != row(past-3+j) {
				t.Fatalf("exported slice row %d is %.12q…", j, v)
			}
		}
	})

	t.Run("a take across 2^31", func(t *testing.T) {
		sel := make([]int32, len(rows))
		for j, i := range rows {
			sel[j] = int32(i)
		}
		got := data.TakeStrings(wide, sel, bitmap.View{})
		if got.RawOffsets() == nil {
			t.Error("a take of 6 KiB with 64-bit offsets")
		}
		for j, i := range rows {
			if v := got.Strings().Get(j); v != row(i) {
				t.Fatalf("taken row %d is %.12q…, want row %d", j, v, i)
			}
		}
	})

	t.Run("exported as a LargeString", func(t *testing.T) {
		arr, err := arrowout.Column(wide)
		if err != nil {
			t.Fatal(err)
		}
		defer arr.Release()
		ls, ok := arr.(*array.LargeString)
		if !ok {
			t.Fatalf("exported as %s", arr.DataType())
		}
		for _, i := range rows {
			if got := ls.Value(i); got != row(i) {
				t.Fatalf("exported row %d is %.12q…", i, got)
			}
		}
	})
}
