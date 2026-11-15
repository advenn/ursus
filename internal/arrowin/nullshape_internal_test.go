package arrowin

import (
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

// TestShapeNullsCopiesOnlyThePathToANullOfNoBuffer (step 522): where no Null array of
// no buffer is in an array, shapeNulls returns its data itself and allocates nothing,
// so every other array is validated as it was; where one is, it copies the arrays on
// the path to it, the Null array holding the nil buffer, and shares every other.
func TestShapeNullsCopiesOnlyThePathToANullOfNoBuffer(t *testing.T) {
	mem := memory.NewGoAllocator()
	build := func(dt arrow.DataType, js string) *array.Data {
		a, _, err := array.FromJSON(mem, dt, strings.NewReader(js))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(a.Release)
		return a.Data().(*array.Data)
	}
	z := arrow.Field{Name: "z", Type: arrow.Null, Nullable: true}
	y := arrow.Field{Name: "y", Type: arrow.PrimitiveTypes.Int64, Nullable: true}
	for _, d := range []*array.Data{
		build(arrow.PrimitiveTypes.Int64, `[1, null, 3]`),
		build(arrow.ListOf(arrow.BinaryTypes.String), `[["a"], null, []]`),
		build(arrow.Null, `[null, null]`), // arrow-go's builder gives it the nil buffer
		build(arrow.StructOf(z, y), `[{"z": null, "y": 1}, null]`),
	} {
		if got := shapeNulls(d); got != d {
			t.Errorf("%s with no Null array of no buffer was copied", d.DataType())
		}
		if n := testing.AllocsPerRun(20, func() { shapeNulls(d) }); n != 0 {
			t.Errorf("%s with no Null array of no buffer: %v allocations", d.DataType(), n)
		}
	}

	ints := build(arrow.PrimitiveTypes.Int64, `[1, 2]`)
	empty := array.NewData(arrow.Null, 2, nil, nil, 2, 0)
	defer empty.Release()
	st := array.NewData(arrow.StructOf(y, z), 2, []*memory.Buffer{nil}, []arrow.ArrayData{ints, empty}, 0, 0)
	defer st.Release()
	got := shapeNulls(st)
	defer got.Release()
	switch {
	case got == st:
		t.Fatal("a struct of a Null field of no buffer was not copied")
	case got.Children()[0] != arrow.ArrayData(ints):
		t.Error("the struct's Int64 field was copied, and only the path to the Null field is")
	case len(got.Children()[1].Buffers()) != 1 || got.Children()[1].Buffers()[0] != nil:
		t.Errorf("the Null field holds %d buffers, not the nil buffer", len(got.Children()[1].Buffers()))
	case got.Len() != st.Len() || got.NullN() != st.NullN() || got.Offset() != st.Offset() ||
		got.Buffers()[0] != st.Buffers()[0] || got.DataType() != st.DataType():
		t.Error("the struct's copy is not the struct")
	}
	if err := validate(array.MakeFromData(got)); err != nil {
		t.Errorf("the copy: %v", err)
	}
}
