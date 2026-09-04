package kernel_test

import (
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
)

// mask builds a Boolean column from values and validity.
func mask(vals, valid []bool) *data.Column {
	b := bitmap.NewBuilder(len(vals))
	v := bitmap.NewBuilder(len(vals))
	for i := range vals {
		b.Append(vals[i])
		v.Append(valid == nil || valid[i])
	}
	return data.NewBool("m", b.Finish(), v.Finish())
}

func i64(name string, vals []int64, valid []bool) *data.Column {
	v := bitmap.NewBuilder(len(vals))
	for i := range vals {
		v.Append(valid == nil || valid[i])
	}
	return data.NewFixed(name, dtype.Int64, vals, v.Finish())
}

func TestSelectMerges(t *testing.T) {
	out, err := kernel.Select("out",
		mask([]bool{true, false, true}, nil),
		i64("a", []int64{1, 2, 3}, nil),
		i64("b", []int64{10, 20, 30}, nil))
	if err != nil {
		t.Fatal(err)
	}
	s, err := data.TypedColumn[int64](out)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []int64{1, 20, 3} {
		if v, ok := s.Get(i); !ok || v != want {
			t.Errorf("row %d = %v (valid %v), want %d", i, v, ok, want)
		}
	}
	if out.Name() != "out" {
		t.Errorf("name = %q, want %q", out.Name(), "out")
	}
}

// TestSelectNullMaskTakesNeitherBranch is the decision most likely to be got
// backwards. A null predicate is not false: it must not fall through to Otherwise.
//
// This is SelectionFromMask's rule — "a row survives iff its predicate is VALID AND
// TRUE" — applied to a merge instead of a filter, so Filter and When agree about
// what a missing predicate means.
func TestSelectNullMaskTakesNeitherBranch(t *testing.T) {
	out, err := kernel.Select("out",
		mask([]bool{true, false, false}, []bool{true, true, false}),
		i64("a", []int64{1, 1, 1}, nil),
		i64("b", []int64{9, 9, 9}, nil))
	if err != nil {
		t.Fatal(err)
	}
	s, _ := data.TypedColumn[int64](out)

	if v, ok := s.Get(1); !ok || v != 9 {
		t.Errorf("a FALSE mask must take the else branch: got %v (valid %v)", v, ok)
	}
	if v, ok := s.Get(2); ok {
		t.Errorf("a NULL mask must produce null, got %v — treating null as false "+
			"would make Otherwise silently absorb missing data", v)
	}
}

// TestSelectIgnoresMaskPayloadUnderANull: Arrow does not guarantee that a null
// slot's value bit is zero, so a kernel that reads the payload without ANDing the
// validity first will take the wrong branch. Here the null lane's value bit is 1.
func TestSelectIgnoresMaskPayloadUnderANull(t *testing.T) {
	bits := bitmap.NewBuilder(2)
	bits.Append(false)
	bits.Append(true) // payload says "true"...
	valid := bitmap.NewBuilder(2)
	valid.Append(true)
	valid.Append(false) // ...but the lane is null
	m := data.NewBool("m", bits.Finish(), valid.Finish())

	out, err := kernel.Select("out", m,
		i64("a", []int64{1, 1}, nil),
		i64("b", []int64{9, 9}, nil))
	if err != nil {
		t.Fatal(err)
	}
	s, _ := data.TypedColumn[int64](out)
	if v, ok := s.Get(1); ok {
		t.Errorf("row 1 = %v, want null: the mask lane is null and its payload bit "+
			"must not be trusted", v)
	}
}

// TestSelectPropagatesBranchNulls: a null reached through the taken branch is null;
// a null sitting in the branch that was NOT taken is irrelevant.
func TestSelectPropagatesBranchNulls(t *testing.T) {
	out, err := kernel.Select("out",
		mask([]bool{true, false}, nil),
		i64("a", []int64{0, 7}, []bool{false, true}), // a[0] null, and a[0] IS taken
		i64("b", []int64{7, 0}, []bool{true, false})) // b[1] null, and b[1] IS taken
	if err != nil {
		t.Fatal(err)
	}
	s, _ := data.TypedColumn[int64](out)
	if _, ok := s.Get(0); ok {
		t.Error("row 0 takes a, which is null there")
	}
	if _, ok := s.Get(1); ok {
		t.Error("row 1 takes b, which is null there")
	}

	// The mirror: the same nulls in the branches that are NOT taken must not show.
	out2, err := kernel.Select("out",
		mask([]bool{false, true}, nil),
		i64("a", []int64{0, 7}, []bool{false, true}),
		i64("b", []int64{7, 0}, []bool{true, false}))
	if err != nil {
		t.Fatal(err)
	}
	s2, _ := data.TypedColumn[int64](out2)
	for i, want := range []int64{7, 7} {
		if v, ok := s2.Get(i); !ok || v != want {
			t.Errorf("row %d = %v (valid %v), want %d — the null is in the untaken branch",
				i, v, ok, want)
		}
	}
}

// TestSelectBroadcastsLengthOneBranches: litColumn produces length-1 columns and
// evalColumn's broadcast runs at the operator boundary, AFTER Eval returns. So
// `When(c).Then(Lit(1)).Otherwise(Lit(0))` reaches this kernel as a length-n mask
// and two length-1 branches, and repeating them is this kernel's job.
func TestSelectBroadcastsLengthOneBranches(t *testing.T) {
	out, err := kernel.Select("out",
		mask([]bool{true, false, true, false}, nil),
		i64("a", []int64{1}, nil),
		i64("b", []int64{0}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if out.Len() != 4 {
		t.Fatalf("length = %d, want 4", out.Len())
	}
	s, _ := data.TypedColumn[int64](out)
	for i, want := range []int64{1, 0, 1, 0} {
		if v, ok := s.Get(i); !ok || v != want {
			t.Errorf("row %d = %v (valid %v), want %d", i, v, ok, want)
		}
	}

	// A length-1 MASK broadcasts too, which is what Lit(true) as a predicate needs.
	out2, err := kernel.Select("out",
		mask([]bool{true}, nil),
		i64("a", []int64{1, 2, 3}, nil),
		i64("b", []int64{9, 9, 9}, nil))
	if err != nil {
		t.Fatal(err)
	}
	s2, _ := data.TypedColumn[int64](out2)
	for i, want := range []int64{1, 2, 3} {
		if v, ok := s2.Get(i); !ok || v != want {
			t.Errorf("broadcast mask row %d = %v, want %d", i, v, want)
		}
	}
}

// TestSelectAcceptsAPayloadFreeNullBranch: Lit(nil) becomes data.NewNull, which
// carries no buffer. data.Values refuses it and StringAccessor.Get panics on it, so
// a branch arriving in that shape must be routed through NullColumn first. Without
// that, `Otherwise(Lit(nil))` fails or panics rather than producing nulls.
func TestSelectAcceptsAPayloadFreeNullBranch(t *testing.T) {
	for _, dt := range []dtype.DataType{dtype.Int64, dtype.String, dtype.Bool, dtype.Float64} {
		t.Run(dt.String(), func(t *testing.T) {
			var present *data.Column
			switch dt.ID() {
			case dtype.TypeString:
				present = data.NewString("a", []string{"x", "y"}, bitmap.AllSet(2))
			case dtype.TypeBool:
				b := bitmap.NewBuilder(2)
				b.Append(true)
				b.Append(true)
				present = data.NewBool("a", b.Finish(), bitmap.AllSet(2))
			case dtype.TypeFloat64:
				present = data.NewFixed("a", dt, []float64{1, 2}, bitmap.AllSet(2))
			default:
				present = i64("a", []int64{1, 2}, nil)
			}

			out, err := kernel.Select("out",
				mask([]bool{true, false}, nil),
				present,
				data.NewNull("b", dt, 2))
			if err != nil {
				t.Fatalf("payload-free null branch: %v", err)
			}
			if out.IsValid(1) {
				t.Error("row 1 takes the null branch and must be null")
			}
			if !out.IsValid(0) {
				t.Error("row 0 takes the present branch and must be valid")
			}
		})
	}
}

func TestSelectOverStringsAndBools(t *testing.T) {
	m := mask([]bool{true, false, true}, []bool{true, true, false})

	sOut, err := kernel.Select("out", m,
		data.NewString("a", []string{"aa", "bb", "cc"}, bitmap.AllSet(3)),
		data.NewString("b", []string{"XX", "YY", "ZZ"}, bitmap.AllSet(3)))
	if err != nil {
		t.Fatal(err)
	}
	acc := sOut.Strings()
	if acc.Get(0) != "aa" || acc.Get(1) != "YY" {
		t.Errorf("strings = %q, %q; want \"aa\", \"YY\"", acc.Get(0), acc.Get(1))
	}
	if sOut.IsValid(2) {
		t.Error("the null-mask row must be null for strings too")
	}

	tt := bitmap.NewBuilder(3)
	ff := bitmap.NewBuilder(3)
	for range 3 {
		tt.Append(true)
		ff.Append(false)
	}
	bOut, err := kernel.Select("out", m,
		data.NewBool("a", tt.Finish(), bitmap.AllSet(3)),
		data.NewBool("b", ff.Finish(), bitmap.AllSet(3)))
	if err != nil {
		t.Fatal(err)
	}
	bits := bOut.Bools()
	if !bits.Get(0) || bits.Get(1) {
		t.Errorf("bools = %v, %v; want true, false", bits.Get(0), bits.Get(1))
	}
	if bOut.IsValid(2) {
		t.Error("the null-mask row must be null for bools too")
	}
}

func TestSelectRefusals(t *testing.T) {
	ok := i64("a", []int64{1, 2}, nil)

	if _, err := kernel.Select("out", ok, ok, ok); err == nil {
		t.Error("a non-Bool mask must be refused")
	}
	if _, err := kernel.Select("out", mask([]bool{true, true}, nil),
		ok, data.NewFixed("b", dtype.Int32, []int32{1, 2}, bitmap.AllSet(2))); err == nil {
		t.Error("branches of different types must be refused — the evaluator casts first")
	}
	if _, err := kernel.Select("out", mask([]bool{true, true}, nil),
		ok, i64("b", []int64{1, 2, 3}, nil)); err == nil {
		t.Error("mismatched non-broadcast lengths must be refused")
	}
}
