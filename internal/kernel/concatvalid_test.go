package kernel

import (
	"slices"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
)

// TestConcatSaysAllValid: columns with every row valid concatenate to one that says
// so, with no bitmap, which lets each kernel after a Collect of several batches read
// no bit a row (step 321); one null among the parts keeps its bit, in its place.
func TestConcatSaysAllValid(t *testing.T) {
	sch := dtype.MustSchema(dtype.Of("i", dtype.Int64), dtype.Of("s", dtype.String))
	batch := func(valid bitmap.View, vals ...int64) *data.Batch {
		strs := make([]string, len(vals))
		b, err := data.NewBatch(sch, []*data.Column{
			data.NewFixed("i", dtype.Int64, vals, valid),
			data.NewString("s", strs, valid),
		})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	built := bitmap.NewBuilder(2)
	built.Append(true)
	built.Append(true)
	every := built.Finish() // every bit set, and not saying so
	out, err := Concat(sch, []*data.Batch{batch(bitmap.AllSet(2), 1, 2), batch(bitmap.AllSet(3), 3, 4, 5)})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range out.Columns() {
		if !c.Validity().IsAllSet() {
			t.Errorf("%s: two batches with every row valid concatenated to a built bitmap", c.Name())
		}
	}
	nulls := bitmap.NewBuilder(2)
	nulls.Append(true)
	nulls.Append(false)
	out, err = Concat(sch, []*data.Batch{batch(every, 1, 2), batch(bitmap.AllSet(1), 3), batch(nulls.Finish(), 4, 5)})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range out.Columns() {
		got := make([]bool, c.Len())
		for i := range got {
			got[i] = c.Validity().Get(i)
		}
		if want := []bool{true, true, true, true, false}; c.Validity().IsAllSet() || !slices.Equal(got, want) {
			t.Errorf("%s: the validity is %v, want %v", c.Name(), got, want)
		}
	}
}
