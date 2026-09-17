package arrowin_test

// Where Arrow is looser than ursus, built by hand, because arrow-go's builders never
// produce most of these shapes and other producers routinely do.

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unsafe"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/arrowin"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
)

// bytesOf is a Go slice's memory as an Arrow buffer, with no allocator.
func bytesOf[T any](vals []T) *memory.Buffer {
	if len(vals) == 0 {
		return memory.NewBufferBytes(nil)
	}
	var z T
	n := len(vals) * int(unsafe.Sizeof(z))
	return memory.NewBufferBytes(unsafe.Slice((*byte)(unsafe.Pointer(&vals[0])), n))
}

// bitsOf packs a validity pattern.
func bitsOf(valid ...bool) *memory.Buffer {
	b := make([]byte, (len(valid)+7)/8)
	for i, v := range valid {
		if v {
			b[i/8] |= 1 << (i % 8)
		}
	}
	return memory.NewBufferBytes(b)
}

func nulls(valid ...bool) int {
	n := 0
	for _, v := range valid {
		if !v {
			n++
		}
	}
	return n
}

func make1(t *testing.T, d *array.Data) arrow.Array {
	t.Helper()
	defer d.Release()
	return keep(t, array.MakeFromData(d))
}

func int64s(t *testing.T, vals []int64, valid ...bool) arrow.Array {
	t.Helper()
	var vb *memory.Buffer
	if valid != nil {
		vb = bitsOf(valid...)
	}
	return make1(t, array.NewData(i64, len(vals), []*memory.Buffer{vb, bytesOf(vals)}, nil, nulls(valid...), 0))
}

func convert(t *testing.T, arr arrow.Array, dt dtype.DataType) *data.Column {
	t.Helper()
	col, err := arrowin.Column("c", arr, dt, 0, arr.Len())
	if err != nil {
		t.Fatal(err)
	}
	return col
}

// TestANullListOwnsNothing: Arrow lets a null list row cover elements — pyarrow's
// ListArray.from_arrays with a mask builds exactly this — and ursus's listReduce
// credits a row with every element in its range, so list.sum over a null row would
// answer a number.
func TestANullListOwnsNothing(t *testing.T) {
	child := int64s(t, []int64{1, 2, 100, 200, 300, 3})
	valid := []bool{true, false, true}
	list := make1(t, array.NewData(arrow.ListOf(i64), 3,
		[]*memory.Buffer{bitsOf(valid...), bytesOf([]int32{0, 2, 5, 6})},
		[]arrow.ArrayData{child.Data()}, 1, 0))
	if err := array.Validate(list); err != nil {
		t.Fatalf("the fixture is not legal Arrow, so it proves nothing: %v", err)
	}

	col := convert(t, list, dtype.List(dtype.Int64))
	if s, e, _ := col.Lists().Get(1); s != e {
		t.Errorf("the null row covers elements [%d, %d)", s, e)
	}
	if n := col.Child().Len(); n != 3 {
		t.Errorf("the child holds %d elements, want 3 — the null row's were copied", n)
	}
	got, _ := data.Values[int64](col.Child())
	if fmt.Sprint(got) != "[1 2 3]" {
		t.Errorf("elements %v, want [1 2 3]", got)
	}
}

// TestAFixedSizeListNullRowOwnsNothing: a fixed-size list's null row ALWAYS covers
// size elements, by construction.
func TestAFixedSizeListNullRowOwnsNothing(t *testing.T) {
	child := int64s(t, []int64{1, 2, 9, 9, 3, 4})
	valid := []bool{true, false, true}
	list := make1(t, array.NewData(arrow.FixedSizeListOf(2, i64), 3,
		[]*memory.Buffer{bitsOf(valid...)}, []arrow.ArrayData{child.Data()}, 1, 0))

	col := convert(t, list, dtype.List(dtype.Int64))
	if s, e, _ := col.Lists().Get(1); s != e || col.Child().Len() != 4 {
		t.Errorf("the null row covers [%d, %d) of %d elements", s, e, col.Child().Len())
	}
}

// TestANullStructHidesItsFields: arrow-go's StructBuilder.AppendValues and pyarrow's
// StructArray.from_arrays both leave a null struct's fields valid, and struct.field
// hands back the field's own validity.
func TestANullStructHidesItsFields(t *testing.T) {
	age := int64s(t, []int64{30, 40, 50})
	inner := make1(t, array.NewData(arrow.StructOf(arrow.Field{Name: "age", Type: i64}), 3,
		[]*memory.Buffer{nil}, []arrow.ArrayData{age.Data()}, 0, 0))
	name := fromJSON(t, memory.NewGoAllocator(), str, `["ann", "bo", "cy"]`)
	valid := []bool{true, false, true}
	outer := make1(t, array.NewData(arrow.StructOf(
		arrow.Field{Name: "inner", Type: inner.DataType()},
		arrow.Field{Name: "name", Type: str}), 3,
		[]*memory.Buffer{bitsOf(valid...)},
		[]arrow.ArrayData{inner.Data(), name.Data()}, 1, 0))

	dt, err := arrowin.Type(outer.DataType(), arrow.Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	col := convert(t, outer, dt)

	in, _ := col.Field("inner")
	ag, _ := in.Field("age")
	nm, _ := col.Field("name")
	for _, c := range []*data.Column{in, ag, nm} {
		if c.IsValid(1) {
			t.Errorf("field %q is valid beneath a null struct", c.Name())
		}
		if !c.IsValid(0) || !c.IsValid(2) {
			t.Errorf("field %q lost a valid row", c.Name())
		}
	}
	if v, _ := data.Values[int64](ag); v[1] != 0 {
		t.Errorf("the hidden age is %d, want the canonical zero", v[1])
	}
	if s := nm.Strings().Get(1); s != "" {
		t.Errorf("the hidden name is %q, want empty", s)
	}
}

// TestNullSlotsAreCanonical: what a producer leaves in a null slot is never copied.
func TestNullSlotsAreCanonical(t *testing.T) {
	valid := []bool{true, false, true}

	ints := int64s(t, []int64{7, 999, 9}, valid...)
	if v, _ := data.Values[int64](convert(t, ints, dtype.Int64)); fmt.Sprint(v) != "[7 0 9]" {
		t.Errorf("int64 values %v, want [7 0 9]", v)
	}

	strs := make1(t, array.NewData(str, 3,
		[]*memory.Buffer{bitsOf(valid...), bytesOf([]int32{0, 1, 8, 9}), memory.NewBufferBytes([]byte("aGARBAGEc"))},
		nil, 1, 0))
	sc := convert(t, strs, dtype.String)
	if s := sc.Strings().Get(1); s != "" || len(sc.RawChars()) != 2 {
		t.Errorf("the null string reads %q and the column holds %d bytes, want \"\" and 2",
			s, len(sc.RawChars()))
	}

	bools := make1(t, array.NewData(arrow.FixedWidthTypes.Boolean, 3,
		[]*memory.Buffer{bitsOf(valid...), bitsOf(true, true, true)}, nil, 1, 0))
	if bc := convert(t, bools, dtype.Bool); bc.Bools().Get(1) {
		t.Error("the null bool's payload bit is set")
	}
}

// TestADictionaryIndexIsReadOnlyForValidRows: a null slot's index is unspecified.
func TestADictionaryIndexIsReadOnlyForValidRows(t *testing.T) {
	mem := memory.NewGoAllocator()
	dict := fromJSON(t, mem, str, `["a", "b"]`)
	idx := make1(t, array.NewData(arrow.PrimitiveTypes.Int8, 3,
		[]*memory.Buffer{bitsOf(true, false, true), bytesOf([]int8{0, 99, 1})}, nil, 1, 0))
	dt := &arrow.DictionaryType{IndexType: arrow.PrimitiveTypes.Int8, ValueType: str}
	d := keep(t, array.NewDictionaryArray(dt, idx, dict))

	col := convert(t, d, dtype.String)
	if col.IsValid(1) || col.Strings().Get(0) != "a" || col.Strings().Get(2) != "b" {
		t.Errorf("decoded %v", col)
	}
}

// refused converts arr and requires an error of kind want, never a panic.
func refused(t *testing.T, label string, arr arrow.Array, dt dtype.DataType, want error, says string) {
	t.Helper()
	var err error
	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Errorf("%s: panicked instead of refusing: %v", label, p)
			}
		}()
		_, err = arrowin.Column("c", arr, dt, 0, arr.Len())
	}()
	if err == nil {
		t.Errorf("%s: accepted", label)
		return
	}
	if !errors.Is(err, want) || !strings.Contains(err.Error(), says) {
		t.Errorf("%s: want a %v error saying %q, got %v", label, want, says, err)
	}
}

func TestMalformedDataIsRefused(t *testing.T) {
	mem := memory.NewGoAllocator()
	child := int64s(t, []int64{1, 2, 3, 4})

	// First and last offsets in range, so only a row-by-row check sees the middle.
	refused(t, "list offsets that decrease",
		make1(t, array.NewData(arrow.ListOf(i64), 3,
			[]*memory.Buffer{nil, bytesOf([]int32{0, 3, 1, 4})}, []arrow.ArrayData{child.Data()}, 0, 0)),
		dtype.List(dtype.Int64), uerr.ErrValue, "covers elements [3, 1)")

	refused(t, "string offsets that decrease",
		make1(t, array.NewData(str, 3,
			[]*memory.Buffer{nil, bytesOf([]int32{0, 3, 1, 4}), memory.NewBufferBytes([]byte("abcd"))}, nil, 0, 0)),
		dtype.String, uerr.ErrValue, "offsets decrease")

	dict := fromJSON(t, mem, str, `["a", "b"]`)
	idx := int64s(t, []int64{0, 7})
	refused(t, "a dictionary index past the dictionary on a valid row",
		keep(t, array.NewDictionaryArray(&arrow.DictionaryType{IndexType: i64, ValueType: str}, idx, dict)),
		dtype.String, uerr.ErrValue, "outside a dictionary of 2 values")

	var h arrow.ViewHeader
	h.SetString("a string longer than twelve")
	h.SetIndexOffset(3, 0)
	refused(t, "a view pointing at a data buffer that does not exist",
		make1(t, array.NewData(arrow.BinaryTypes.StringView, 1,
			[]*memory.Buffer{nil, bytesOf([]arrow.ViewHeader{h}), memory.NewBufferBytes(make([]byte, 64))}, nil, 0, 0)),
		dtype.String, uerr.ErrValue, "outside its data buffers")

	refused(t, "a list view past its child",
		make1(t, array.NewData(arrow.ListViewOf(i64), 2,
			[]*memory.Buffer{nil, bytesOf([]int32{0, 3}), bytesOf([]int32{2, 2})}, []arrow.ArrayData{child.Data()}, 0, 0)),
		dtype.List(dtype.Int64), uerr.ErrValue, "")
}

// TestTimeIsWrappedIntoTheDay: arrow-go never range-checks a time, and every ursus
// kernel assumes [0, 24h) — the invariant step 57 established and Cast enforces.
func TestTimeIsWrappedIntoTheDay(t *testing.T) {
	valid := []bool{true, true, true, false}
	t32 := make1(t, array.NewData(arrow.FixedWidthTypes.Time32ms, 4,
		[]*memory.Buffer{bitsOf(valid...), bytesOf([]int32{-1, 86_400_000, 90_000_000, -5})}, nil, 1, 0))
	v, _ := data.Values[int64](convert(t, t32, dtype.Time(dtype.Milli)))
	if fmt.Sprint(v) != "[86399999 0 3600000 0]" {
		t.Errorf("time32[ms] became %v", v)
	}

	t64 := make1(t, array.NewData(arrow.FixedWidthTypes.Time64ns, 1,
		[]*memory.Buffer{nil, bytesOf([]int64{-86_400_000_000_001})}, nil, 0, 0))
	if v, _ := data.Values[int64](convert(t, t64, dtype.Time(dtype.Nano))); v[0] != 86_399_999_999_999 {
		t.Errorf("time64[ns] became %d", v[0])
	}
}

func TestDate64MustBeWholeDays(t *testing.T) {
	const day = 86_400_000
	d64 := func(vals []int64, valid ...bool) arrow.Array {
		var vb *memory.Buffer
		if valid != nil {
			vb = bitsOf(valid...)
		}
		return make1(t, array.NewData(arrow.FixedWidthTypes.Date64, len(vals),
			[]*memory.Buffer{vb, bytesOf(vals)}, nil, nulls(valid...), 0))
	}

	refused(t, "a time of day", d64([]int64{day + 1}), dtype.Date, uerr.ErrValue, "whole number of days")
	refused(t, "past int32 days", d64([]int64{day * (1 << 31)}), dtype.Date, uerr.ErrValue, "outside the range")

	// A null slot holds whatever it holds, and must not be judged.
	col := convert(t, d64([]int64{2 * day, 12345}, true, false), dtype.Date)
	if v, _ := data.Values[int32](col); fmt.Sprint(v) != "[2 0]" {
		t.Errorf("date64 with a null became %v", v)
	}
}

// TestListSpansAreCheckedBeforeAllocating uses a Null child, which costs nothing
// however long it claims to be, so both halves run on any machine.
func TestListSpansAreCheckedBeforeAllocating(t *testing.T) {
	const big = 3_000_000_000
	nullChild := keep(t, array.NewNull(big+10))

	refused(t, "a list of three billion elements",
		make1(t, array.NewData(arrow.LargeListOf(arrow.Null), 1,
			[]*memory.Buffer{nil, bytesOf([]int64{0, big})}, []arrow.ArrayData{nullChild.Data()}, 0, 0)),
		dtype.List(dtype.Null), uerr.ErrValue, "2^31 elements")

	// The SPAN is what must fit, not the absolute offsets: five elements three
	// billion into the child are an ordinary slice of a large list.
	far := make1(t, array.NewData(arrow.LargeListOf(arrow.Null), 2,
		[]*memory.Buffer{nil, bytesOf([]int64{big, big + 2, big + 5})}, []arrow.ArrayData{nullChild.Data()}, 0, 0))
	col := convert(t, far, dtype.List(dtype.Null))
	if s, e, _ := col.Lists().Get(1); col.Child().Len() != 5 || s != 2 || e != 5 {
		t.Errorf("a far slice became %d elements with row 1 at [%d, %d)", col.Child().Len(), s, e)
	}
}
