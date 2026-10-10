package kernel

import (
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
)

// TestWithFieldsKeepsANullStruct: the struct's own validity is the answer's, so a null
// struct stays null whatever its fields are set to, and a literal operand is spread
// over every row (step 171).
func TestWithFieldsKeepsANullStruct(t *testing.T) {
	valid := bitmap.NewBuilder(3)
	valid.Append(true)
	valid.Append(false)
	valid.Append(true)
	s := data.NewStruct("p", []*data.Column{
		data.NewFixed("age", dtype.Int64, []int64{30, 40, 50}, bitmap.AllSet(3)),
	}, valid.Finish())
	tag := data.NewString("tag", []string{"t"}, bitmap.AllSet(1)) // a literal
	age := data.NewFixed("age", dtype.Int64, []int64{1, 2, 3}, bitmap.AllSet(3))
	out, err := expr.ResolveHorizontal(expr.FnStructWithFields, []dtype.Field{
		{Name: "p", Type: s.DType()}, {Name: "age", Type: dtype.Int64}, {Name: "tag", Type: dtype.String}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := HorizontalCall(expr.FnStructWithFields, "p", out, []*data.Column{s, age, tag})
	if err != nil {
		t.Fatal(err)
	}
	if got.IsValid(1) || !got.IsValid(0) || !got.IsValid(2) {
		t.Fatalf("validity %v %v %v, want the struct's: true false true", got.IsValid(0), got.IsValid(1), got.IsValid(2))
	}
	a, _ := got.Field("age")
	tg, _ := got.Field("tag")
	if v := data.MustValues[int64](a); v[0] != 1 || v[2] != 3 {
		t.Fatalf("age %v, want the operand's", v)
	}
	if tg.Len() != 3 || tg.Strings().Get(2) != "t" {
		t.Fatalf("a literal operand was not spread: %d rows", tg.Len())
	}
}
