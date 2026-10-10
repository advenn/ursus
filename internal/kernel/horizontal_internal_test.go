package kernel

import (
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
)

// TestStructOfSpreadsALiteralOperand: a horizontal call's literal operand is one row
// long and read at every row; struct makes it a field as long as the batch.
func TestStructOfSpreadsALiteralOperand(t *testing.T) {
	a := data.NewFixed("a", dtype.Int64, []int64{1, 2, 3}, bitmap.AllSet(3))
	lit := data.NewString("tag", []string{"t"}, bitmap.AllSet(1))
	out, err := expr.ResolveHorizontal(expr.FnStructOf, []dtype.Field{
		{Name: "a", Type: dtype.Int64}, {Name: "tag", Type: dtype.String}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := HorizontalCall(expr.FnStructOf, "a", out, []*data.Column{a, lit})
	if err != nil {
		t.Fatal(err)
	}
	tag, ok := got.Field("tag")
	if !ok || got.Len() != 3 || tag.Len() != 3 || tag.Strings().Get(2) != "t" {
		t.Fatalf("the literal field is %d rows long in a struct of %d", tag.Len(), got.Len())
	}
}
