package kernel_test

import (
	"math"
	"math/rand/v2"
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

// TestSelectAnswersAsTheRowModel holds Select to a model of each row, over 600
// generated masks and branches (step 503, where three tests' typed rows were): a row
// whose mask is valid and true takes a's value, any other takes b's — a false one,
// and a null one whatever its payload bit says, since Arrow does not guarantee a
// null slot's value bit is zero and the generator sets it at random — and the row is
// null exactly where the branch it takes is, a null in the branch not taken
// irrelevant. Each of the mask, a and b is the frame's length or one row,
// broadcast; the branches are Int64, Int8, Float64 (NaN, the infinities and -0
// among them), String or Bool, or a branch of no payload at all, data.NewNull.
func TestSelectAnswersAsTheRowModel(t *testing.T) {
	rng := rand.New(rand.NewPCG(503, 311))
	kinds := []dtype.DataType{dtype.Int64, dtype.Int8, dtype.Float64, dtype.String, dtype.Bool}
	took := map[string]int{}
	for trial := range 600 {
		dt := kinds[trial%len(kinds)]
		n := 1 + rng.IntN(70)
		length := func() int {
			if rng.IntN(4) == 0 {
				return 1
			}
			return n
		}
		ml, al, bl := length(), length(), length()
		mvals, mvalid := randomBools(rng, ml, 0.5), randomBools(rng, ml, 0.7)
		a, b := selectBranch(rng, "a", dt, al), selectBranch(rng, "b", dt, bl)
		out, err := kernel.Select("out", mask(mvals, mvalid), a.col, b.col)
		if err != nil {
			t.Fatalf("trial %d: %v", trial, err)
		}
		rows := max(ml, al, bl)
		if out.Len() != rows || out.DType() != dt {
			t.Fatalf("trial %d: %d rows of %s, want %d of %s", trial, out.Len(), out.DType(), rows, dt)
		}
		at := func(length, i int) int { return min(i, length-1) }
		for i := range rows {
			mi := at(ml, i)
			want, wantOK := b.at(at(bl, i))
			switch {
			case mvalid[mi] && mvals[mi]:
				want, wantOK = a.at(at(al, i))
				took["a"]++
			case mvalid[mi]:
				took["b, the mask false"]++
			default:
				took["b, the mask null"]++
				if mvals[mi] {
					took["b, the mask null over a true bit"]++
				}
			}
			got, gotOK := selectCell(t, out, i)
			if gotOK != wantOK || (wantOK && !sameCell(got, want)) {
				t.Fatalf("trial %d (%s, %d rows) row %d: got %v (valid %v), the model %v (valid %v)",
					trial, dt, rows, i, got, gotOK, want, wantOK)
			}
		}
	}
	for _, k := range []string{"a", "b, the mask false", "b, the mask null", "b, the mask null over a true bit"} {
		if took[k] == 0 {
			t.Errorf("no row took %s; the sweep has gone vacuous", k)
		}
	}
}

func randomBools(rng *rand.Rand, n int, p float64) []bool {
	out := make([]bool, n)
	for i := range out {
		out[i] = rng.Float64() < p
	}
	return out
}

// selectInput is a generated branch and its model: each row's value, or not valid.
type selectInput struct {
	col   *data.Column
	vals  []any
	valid []bool
}

func (s selectInput) at(i int) (any, bool) { return s.vals[i], s.valid[i] }

// selectBranch is n generated rows of dt, a fifth of them null; one branch in eight
// is a payload-free column of nulls.
func selectBranch(rng *rand.Rand, name string, dt dtype.DataType, n int) selectInput {
	in := selectInput{vals: make([]any, n), valid: randomBools(rng, n, 0.8)}
	if rng.IntN(8) == 0 {
		in.valid = make([]bool, n)
		in.col = data.NewNull(name, dt, n)
		return in
	}
	v := bitmap.NewBuilder(n)
	for _, ok := range in.valid {
		v.Append(ok)
	}
	switch dt {
	case dtype.Int64:
		vals := make([]int64, n)
		for i := range vals {
			vals[i] = rng.Int64() >> rng.IntN(64)
			in.vals[i] = vals[i]
		}
		in.col = data.NewFixed(name, dt, vals, v.Finish())
	case dtype.Int8:
		vals := make([]int8, n)
		for i := range vals {
			vals[i] = int8(rng.Uint32())
			in.vals[i] = vals[i]
		}
		in.col = data.NewFixed(name, dt, vals, v.Finish())
	case dtype.Float64:
		edges := []float64{math.NaN(), math.Inf(1), math.Inf(-1), math.Copysign(0, -1), 0}
		vals := make([]float64, n)
		for i := range vals {
			vals[i] = rng.NormFloat64() * 1e3
			if rng.IntN(5) == 0 {
				vals[i] = edges[rng.IntN(len(edges))]
			}
			in.vals[i] = vals[i]
		}
		in.col = data.NewFixed(name, dt, vals, v.Finish())
	case dtype.String:
		vals := make([]string, n)
		for i := range vals {
			b := make([]byte, rng.IntN(6))
			for j := range b {
				b[j] = "ab\x00é"[rng.IntN(5)]
			}
			vals[i] = string(b)
			in.vals[i] = vals[i]
		}
		in.col = data.NewString(name, vals, v.Finish())
	case dtype.Bool:
		bits := bitmap.NewBuilder(n)
		for i := range n {
			x := rng.IntN(2) == 0
			bits.Append(x)
			in.vals[i] = x
		}
		in.col = data.NewBool(name, bits.Finish(), v.Finish())
	}
	return in
}

// selectCell reads row i of Select's answer.
func selectCell(t *testing.T, c *data.Column, i int) (any, bool) {
	t.Helper()
	if !c.IsValid(i) {
		return nil, false
	}
	switch c.DType() {
	case dtype.Int64:
		return mustValues[int64](t, c)[i], true
	case dtype.Int8:
		return mustValues[int8](t, c)[i], true
	case dtype.Float64:
		return mustValues[float64](t, c)[i], true
	case dtype.String:
		return c.Strings().Get(i), true
	case dtype.Bool:
		return c.Bools().Get(i), true
	}
	t.Fatalf("selectCell does not read %s", c.DType())
	return nil, false
}

// sameCell is equality, a float's by its bits: NaN is NaN and -0 is not 0.
func sameCell(a, b any) bool {
	if x, ok := a.(float64); ok {
		y, ok := b.(float64)
		return ok && math.Float64bits(x) == math.Float64bits(y)
	}
	return a == b
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
