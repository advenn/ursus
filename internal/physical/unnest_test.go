package physical

import (
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
)

// TestUnnestMasksFieldsOfANullStruct is why unnestOp ANDs the struct's validity into
// each field instead of trusting the field.
//
// No struct ursus can currently READ is inconsistent this way — the Parquet reader
// marks a field null wherever its group is absent, because a definition level below
// the group's is below the field's too. But nothing ENFORCES it: data.NewStruct does
// not check, and Slice, takeStruct and concatStruct carry whatever they are handed.
//
// So the column is built by hand here, with a perfectly valid 99 sitting under a null
// struct. Without the mask the 99 comes out; with it, the row is null. That is the
// difference between a rule that is applied and one that happens to hold.
func TestUnnestMasksFieldsOfANullStruct(t *testing.T) {
	valid := bitmap.NewBuilder(3)
	valid.Append(true)
	valid.Append(false) // the struct is ABSENT here...
	valid.Append(true)

	age := data.NewFixed("age", dtype.Int64, []int64{7, 99, 8}, bitmap.AllSet(3))
	//                                             ^^ ...but the field says 99

	person := data.NewStruct("person", []*data.Column{age}, valid.Finish())
	in := data.NewBatchRows(
		dtype.MustSchema(dtype.Field{Name: "person", Type: person.DType(), Nullable: true}),
		[]*data.Column{person}, 3)

	out := dtype.MustSchema(dtype.Field{Name: "age", Type: dtype.Int64, Nullable: true})
	op := &unnestOp{schema: out, cols: []int{0}}

	got, err := op.Apply(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	col := got.Column(0)
	if col.Len() != 3 {
		t.Fatalf("got %d rows, want 3", col.Len())
	}
	for i, want := range []bool{true, false, true} {
		if col.IsValid(i) != want {
			t.Errorf("row %d: age valid = %v, want %v — a field of an absent struct "+
				"is absent, whatever the field itself claims", i, col.IsValid(i), want)
		}
	}
}

// TestUnnestKeepsAnUnmaskedFieldUnmasked: the mask is skipped when the struct has no
// nulls, which keeps a field's no-storage validity — the form downstream kernels take
// their fast path on — from being replaced by a full bitmap for nothing.
func TestUnnestKeepsAnUnmaskedFieldUnmasked(t *testing.T) {
	age := data.NewFixed("age", dtype.Int64, []int64{1, 2}, bitmap.AllSet(2))
	person := data.NewStruct("person", []*data.Column{age}, bitmap.AllSet(2))
	in := data.NewBatchRows(
		dtype.MustSchema(dtype.Field{Name: "person", Type: person.DType(), Nullable: true}),
		[]*data.Column{person}, 2)

	out := dtype.MustSchema(dtype.Field{Name: "age", Type: dtype.Int64, Nullable: true})
	op := &unnestOp{schema: out, cols: []int{0}}

	got, err := op.Apply(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Column(0) != age {
		t.Error("a field under an all-valid struct should be handed through untouched")
	}
}
