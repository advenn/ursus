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
	if v, ok := s.Get(1); !ok || v != 9 {
		t.Errorf("row 1 = %v (valid %v), want 9: the mask lane is null, so the else "+
			"branch, and its payload bit, which says true, must not be trusted", v, ok)
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
	if !sOut.IsValid(2) || acc.Get(2) != "ZZ" {
		t.Error("the null-mask row must take the else branch for strings too")
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
	if !bOut.IsValid(2) || bits.Get(2) {
		t.Error("the null-mask row must take the else branch for bools too")
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
