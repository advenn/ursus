package kernel_test

// Comparing two Binary columns.
//
// dispatchCompare gated its offset-and-character path on IsString(), which asks
// whether a value is TEXT. The question it needed was whether the value is STORED as
// offsets and characters, which is HasStringStorage() — and the gap between the two
// is exactly Binary. So a Binary column sorted (valueComparator has always used the
// storage predicate) while comparing two of them refused, for all eight operators,
// after the planner had already promised a Bool.
//
// The matrix in internal/physical checks the TYPES. This checks the answers, and the
// fixture is chosen so a plausible wrong implementation gives a different one: bytes
// above 0x7f, which a signed byte comparison orders below the empty string, and an
// embedded NUL, which a C-style comparison would treat as a terminator.

import (
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
)

func binCol(name string, vals []string) *data.Column {
	return data.NewString(name, vals, bitmap.AllSet(len(vals))).WithDType(dtype.Binary)
}

func TestBinaryColumnsCompare(t *testing.T) {
	// row 0: equal. row 1: left sorts first. row 2: 0xff vs 0x01 — a SIGNED byte
	// comparison would answer the other way. row 3: identical up to an embedded NUL.
	l := binCol("l", []string{"ab", "ab\x00", "\xff", "a\x00b"})
	r := binCol("r", []string{"ab", "abz", "\x01", "a\x00c"})

	for _, c := range []struct {
		op   expr.BinaryOp
		want []bool
	}{
		{expr.OpEq, []bool{true, false, false, false}},
		{expr.OpNe, []bool{false, true, true, true}},
		{expr.OpLt, []bool{false, true, false, true}},
		{expr.OpGt, []bool{false, false, true, false}},
		{expr.OpLe, []bool{true, true, false, true}},
		{expr.OpGe, []bool{true, false, true, false}},
	} {
		t.Run(c.op.String(), func(t *testing.T) {
			got, err := kernel.Binary(c.op, "o", dtype.Bool, l, r)
			if err != nil {
				t.Fatalf("comparing two Binary columns: %v", err)
			}
			bits := got.Bools()
			for i, want := range c.want {
				if !got.IsValid(i) {
					t.Errorf("row %d is null", i)
					continue
				}
				if bits.Get(i) != want {
					t.Errorf("row %d: %q %s %q = %v, want %v",
						i, l.Strings().Get(i), c.op, r.Strings().Get(i), bits.Get(i), want)
				}
			}
		})
	}
}

// TestBinaryAndStringDoNotCompareAcross: the fix widened a STORAGE predicate, and it
// must not have widened the type rules with it. Binary and String share a layout and
// are different types, and Promote refuses the pair — so this stays a refusal.
func TestBinaryAndStringDoNotCompareAcross(t *testing.T) {
	if _, err := expr.ResolveBinary(expr.OpEq, dtype.Binary, dtype.String); err == nil {
		t.Error("Binary and String were promoted to a common type")
	}
}
