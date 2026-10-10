package kernel

// Step 164: Kleene logic a word at a time, and is_in over an integer column's own
// values. Each against what it replaced: the row-by-row truth table, and InSet.

import (
	"fmt"
	"math"
	"math/rand/v2"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/expr"
)

// kleeneByRow is the three-valued truth table, one row at a time, as kleene was
// before step 164: ok is whether the answer is known.
func kleeneByRow(op expr.BinaryOp, a, aok, b, bok bool) (v, ok bool) {
	a, b = a && aok, b && bok
	switch op {
	case expr.OpAnd:
		switch {
		case aok && bok:
			return a && b, true
		case (aok && !a) || (bok && !b):
			return false, true
		}
		return false, false
	case expr.OpOr:
		switch {
		case aok && bok:
			return a || b, true
		case (aok && a) || (bok && b):
			return true, true
		}
		return false, false
	default:
		return a != b, aok && bok
	}
}

// boolColumn is n random Bools, one in nulls null (none when nulls is 0), with a
// payload bit set under some nulls, which no answer may read.
func boolColumn(rng *rand.Rand, n, nulls int) *data.Column {
	bits, valid := bitmap.NewBuilder(n), bitmap.NewBuilder(n)
	for range n {
		bits.Append(rng.IntN(2) == 0)
		valid.Append(nulls == 0 || rng.IntN(nulls) != 0)
	}
	v := valid.Finish()
	if nulls == 0 {
		v = bitmap.AllSet(n)
	}
	return data.NewBool("b", bits.Finish(), v)
}

func TestKleeneAnswersAsTheTruthTable(t *testing.T) {
	rng := rand.New(rand.NewPCG(164, 1))
	const n = 1_000
	nullBool, err := NullColumn("n", dtype.Bool, n)
	if err != nil {
		t.Fatal(err)
	}
	sides := map[string]func() *data.Column{
		"no nulls":     func() *data.Column { return boolColumn(rng, n, 0) },
		"nulls":        func() *data.Column { return boolColumn(rng, n, 4) },
		"mostly nulls": func() *data.Column { return boolColumn(rng, n, 2) },
		// Sliced at an odd offset, so every word straddles two of the source's.
		"sliced":               func() *data.Column { return boolColumn(rng, n+37, 3).Slice(37, n) },
		"true":                 func() *data.Column { return data.NewBool("s", bitmap.AllSet(1), bitmap.AllSet(1)) },
		"false":                func() *data.Column { return data.NewBool("s", bitmap.Zeros(1), bitmap.AllSet(1)) },
		"null":                 func() *data.Column { return data.NewBool("s", bitmap.AllSet(1), bitmap.Zeros(1)) },
		"all null, no payload": func() *data.Column { return data.NewNull("p", dtype.Bool, n) },
		"all null":             func() *data.Column { return nullBool },
	}
	for _, op := range []expr.BinaryOp{expr.OpAnd, expr.OpOr, expr.OpXor} {
		for ln, l := range sides {
			for rn, r := range sides {
				lc, rc := l(), r()
				got, err := kleene(op, "k", lc, rc, n)
				if err != nil {
					t.Fatal(err)
				}
				if got.Len() != n {
					t.Fatalf("%s %s %s: %d rows, want %d", ln, op, rn, got.Len(), n)
				}
				at := func(c *data.Column, i int) (bool, bool) {
					if c.Len() == 1 {
						i = 0
					}
					if !c.IsValid(i) {
						return false, false
					}
					return c.Bools().Get(i), true
				}
				for i := range n {
					a, aok := at(lc, i)
					b, bok := at(rc, i)
					wv, wok := kleeneByRow(op, a, aok, b, bok)
					gok := got.IsValid(i)
					if gok != wok || (wok && got.Bools().Get(i) != wv) {
						t.Fatalf("%s %s %s, row %d: (%v,%v) %s (%v,%v) gave (%v,%v), want (%v,%v)",
							ln, op, rn, i, a, aok, op, b, bok, got.Bools().Get(i), gok, wv, wok)
					}
				}
			}
		}
	}
}

// intColumnOf is vals as a column of dt, one in nulls null, from int64s wrapped to
// dt's width.
func intColumnOf(dt dtype.DataType, vals []int64, nulls int, rng *rand.Rand) *data.Column {
	n := len(vals)
	valid := bitmap.NewBuilder(n)
	for range n {
		valid.Append(nulls == 0 || rng.IntN(nulls) != 0)
	}
	v := valid.Finish()
	conv := func(f func(int64)) {
		for _, x := range vals {
			f(x)
		}
	}
	switch dt.Physical().ID() {
	case dtype.TypeInt8:
		var out []int8
		conv(func(x int64) { out = append(out, int8(x)) })
		return data.NewFixed("k", dt, out, v)
	case dtype.TypeInt16:
		var out []int16
		conv(func(x int64) { out = append(out, int16(x)) })
		return data.NewFixed("k", dt, out, v)
	case dtype.TypeInt32:
		var out []int32
		conv(func(x int64) { out = append(out, int32(x)) })
		return data.NewFixed("k", dt, out, v)
	case dtype.TypeInt64:
		return data.NewFixed("k", dt, vals, v)
	case dtype.TypeUint8:
		var out []uint8
		conv(func(x int64) { out = append(out, uint8(x)) })
		return data.NewFixed("k", dt, out, v)
	case dtype.TypeUint16:
		var out []uint16
		conv(func(x int64) { out = append(out, uint16(x)) })
		return data.NewFixed("k", dt, out, v)
	case dtype.TypeUint32:
		var out []uint32
		conv(func(x int64) { out = append(out, uint32(x)) })
		return data.NewFixed("k", dt, out, v)
	default:
		var out []uint64
		conv(func(x int64) { out = append(out, uint64(x)) })
		return data.NewFixed("k", dt, out, v)
	}
}

var intKeyTypes = []dtype.DataType{dtype.Int8, dtype.Int16, dtype.Int32, dtype.Int64,
	dtype.Uint8, dtype.Uint16, dtype.Uint32, dtype.Uint64, dtype.Date,
	dtype.Datetime(dtype.Micro, "")}

var intEdges = []int64{0, -1, 1, math.MinInt64, math.MaxInt64, math.MinInt32, math.MaxInt32,
	math.MaxUint32, -128, 127, 255, 1 << 32, -1 << 31}

// encodedSet is set's members encoded as InSet's set is, one key each.
func encodedSet(t *testing.T, c *data.Column) map[string]struct{} {
	t.Helper()
	enc, err := NewGroupKeyEncoder("is_in", []*data.Column{c})
	if err != nil {
		t.Fatal(err)
	}
	set := map[string]struct{}{}
	for i := range c.Len() {
		set[string(enc.Encode(i))] = struct{}{}
	}
	return set
}

// TestIntMembersDecodeWhatTheEncoderWrote: every width's extremes, encoded as is_in
// encodes its set, come back as IntKeys widens the same values.
func TestIntMembersDecodeWhatTheEncoderWrote(t *testing.T) {
	rng := rand.New(rand.NewPCG(164, 2))
	for _, dt := range intKeyTypes {
		c := intColumnOf(dt, intEdges, 0, rng)
		got, ok := IntMembers(encodedSet(t, c), dt)
		if !ok {
			t.Fatalf("%s: the set was not read as integers", dt)
		}
		var scratch []int64
		want := map[int64]bool{}
		for _, v := range IntKeys(c, &scratch) {
			want[v] = true
		}
		if len(got) != len(want) {
			t.Fatalf("%s: %d members, want %d", dt, len(got), len(want))
		}
		for _, v := range got {
			if !want[v] {
				t.Fatalf("%s: decoded %d, which no value widens to", dt, v)
			}
		}
	}
	if _, ok := IntMembers(map[string]struct{}{"\x01ab": {}}, dtype.Int32); ok {
		t.Error("a key of the wrong width was read as an Int32")
	}
	if _, ok := IntMembers(map[string]struct{}{}, dtype.Float64); ok {
		t.Error("a Float64 set was read as integers")
	}
}

// TestInIntsAnswersAsInSet: random columns of every integer width, with nulls,
// against sets of a few members and of many, which take InInts' two paths.
func TestInIntsAnswersAsInSet(t *testing.T) {
	rng := rand.New(rand.NewPCG(164, 3))
	for _, dt := range intKeyTypes {
		vals := make([]int64, 2_000)
		for i := range vals {
			if rng.IntN(20) == 0 {
				vals[i] = intEdges[rng.IntN(len(intEdges))]
			} else {
				vals[i] = int64(rng.IntN(60)) - 30
			}
		}
		col := intColumnOf(dt, vals, 7, rng)
		for _, size := range []int{0, 3, 8, 9, 40} {
			mv := make([]int64, size)
			for i := range mv {
				mv[i] = int64(rng.IntN(60)) - 30
			}
			mv = append(mv, intEdges[:min(size, len(intEdges))]...)
			set := encodedSet(t, intColumnOf(dt, mv, 0, rng))
			members, ok := IntMembers(set, dt)
			if !ok {
				t.Fatalf("%s: the set was not read as integers", dt)
			}
			var lookup map[int64]struct{}
			if len(members) > 8 {
				lookup = map[int64]struct{}{}
				for _, m := range members {
					lookup[m] = struct{}{}
				}
			}
			got := InInts("k", col, members, lookup)
			want, err := InSet("k", col, set)
			if err != nil {
				t.Fatal(err)
			}
			name := fmt.Sprintf("%s, %d members", dt, len(members))
			for i := range col.Len() {
				if got.IsValid(i) != want.IsValid(i) ||
					(want.IsValid(i) && got.Bools().Get(i) != want.Bools().Get(i)) {
					t.Fatalf("%s, row %d: %v, InSet says %v", name, i, got.Bools().Get(i), want.Bools().Get(i))
				}
			}
		}
	}
}
