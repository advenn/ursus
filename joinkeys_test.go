package ursus_test

// Every pair of numeric key types, joined and concatenated, against exact rationals.
//
// A join compares its keys in one promoted type, and Concat stacks its columns in
// one. Both must keep every value exactly, or refuse: an Int64 key of 2^53+1 that
// matches a Float64 key of 2^53 is a wrong row, and a Concat that turns it into 2^53
// is a wrong value. The types are every numeric type a key can have; the values are
// each type's own boundaries and the points where a float stops holding every
// integer. The oracle is big.Rat equality, which shares no code with the engine.

import (
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
)

// knownKeyTypeDefects counts the wrong answers per pair.
var knownKeyTypeDefects = map[string]int{}

// numericKeyTypes is every numeric type a key can have. Decimal is left out: its
// keys compare as Decimals, and no pair of them has the question this asks.
var numericKeyTypes = []dtype.DataType{
	dtype.Int8, dtype.Int16, dtype.Int32, dtype.Int64,
	dtype.Uint8, dtype.Uint16, dtype.Uint32, dtype.Uint64, dtype.Int128,
	dtype.Float32, dtype.Float64,
}

// keyValues are the candidate values; each type keeps the ones it holds exactly.
func keyValues() []*big.Rat {
	r := func(s string) *big.Rat {
		v, ok := new(big.Rat).SetString(s)
		if !ok {
			panic(s)
		}
		return v
	}
	return []*big.Rat{
		r("0"), r("1"), r("-1"), r("1/2"), r("127"), r("-128"), r("255"),
		r("16777216"), r("16777217"), r("2147483647"), r("4294967295"),
		r("9007199254740992"), r("9007199254740993"), r("4611686018427387905"),
		r("9223372036854775807"), r("-9223372036854775808"), r("18446744073709551615"),
		r("18446744073709551616"),
	}
}

// holds reports whether dt holds v exactly.
func holds(dt dtype.DataType, v *big.Rat) bool {
	switch dt.ID() {
	case dtype.TypeFloat32:
		_, exact := v.Float32()
		return exact
	case dtype.TypeFloat64:
		_, exact := v.Float64()
		return exact
	}
	if !v.IsInt() {
		return false
	}
	lo, hi := new(big.Int), new(big.Int)
	w := uint(dt.BitWidth())
	if dt.IsSignedInteger() {
		lo.Neg(new(big.Int).Lsh(big.NewInt(1), w-1))
		hi.Sub(new(big.Int).Lsh(big.NewInt(1), w-1), big.NewInt(1))
	} else {
		hi.Sub(new(big.Int).Lsh(big.NewInt(1), w), big.NewInt(1))
	}
	return v.Num().Cmp(lo) >= 0 && v.Num().Cmp(hi) <= 0
}

// keyFrame is one column x of type dt holding vals, and its row number in row.
func keyFrame(t *testing.T, dt dtype.DataType, vals []*big.Rat, row string) *ursus.LazyFrame {
	t.Helper()
	n := len(vals)
	rows := make([]int64, n)
	for i := range rows {
		rows[i] = int64(i)
	}
	all := bitmap.AllSet(n)
	ints := func(f func(*big.Int) any) []any {
		out := make([]any, n)
		for i, v := range vals {
			out[i] = f(v.Num())
		}
		return out
	}
	var col *ursus.Column
	switch dt.ID() {
	case dtype.TypeInt8:
		col = data.NewFixed("x", dt, typed[int8](ints(func(b *big.Int) any { return int8(b.Int64()) })), all)
	case dtype.TypeInt16:
		col = data.NewFixed("x", dt, typed[int16](ints(func(b *big.Int) any { return int16(b.Int64()) })), all)
	case dtype.TypeInt32:
		col = data.NewFixed("x", dt, typed[int32](ints(func(b *big.Int) any { return int32(b.Int64()) })), all)
	case dtype.TypeInt64:
		col = data.NewFixed("x", dt, typed[int64](ints(func(b *big.Int) any { return b.Int64() })), all)
	case dtype.TypeUint8:
		col = data.NewFixed("x", dt, typed[uint8](ints(func(b *big.Int) any { return uint8(b.Uint64()) })), all)
	case dtype.TypeUint16:
		col = data.NewFixed("x", dt, typed[uint16](ints(func(b *big.Int) any { return uint16(b.Uint64()) })), all)
	case dtype.TypeUint32:
		col = data.NewFixed("x", dt, typed[uint32](ints(func(b *big.Int) any { return uint32(b.Uint64()) })), all)
	case dtype.TypeUint64:
		col = data.NewFixed("x", dt, typed[uint64](ints(func(b *big.Int) any { return b.Uint64() })), all)
	case dtype.TypeInt128:
		col = data.NewFixed("x", dt, typed[i128.Int128](ints(func(b *big.Int) any {
			v, ok := i128.Parse(b.String())
			if !ok {
				t.Fatalf("i128.Parse(%s)", b)
			}
			return v
		})), all)
	case dtype.TypeFloat32:
		f := make([]float32, n)
		for i, v := range vals {
			f[i], _ = v.Float32()
		}
		col = data.NewFixed("x", dt, f, all)
	case dtype.TypeFloat64:
		f := make([]float64, n)
		for i, v := range vals {
			f[i], _ = v.Float64()
		}
		col = data.NewFixed("x", dt, f, all)
	default:
		t.Fatalf("no column builder for %s", dt)
	}
	return ursus.Frame(col, ursus.Values(row, rows))
}

func typed[T any](xs []any) []T {
	out := make([]T, len(xs))
	for i, x := range xs {
		out[i] = x.(T)
	}
	return out
}

// exactValue reads a cell rendered by a String cast back as the exact value it is
// at its column's type: a float renders as its shortest round-trip text, which reads
// back as that float only when parsed at its own width.
func exactValue(cell string, dt dtype.DataType) (*big.Rat, bool) {
	if dt.IsFloat() {
		bits := 64
		if dt.ID() == dtype.TypeFloat32 {
			bits = 32
		}
		f, err := strconv.ParseFloat(cell, bits)
		if err != nil {
			return nil, false
		}
		return new(big.Rat).SetFloat64(f), true
	}
	return new(big.Rat).SetString(cell)
}

// exactOrNone reports whether the pair has a type that holds both exactly: every
// integer pair (Int128 absorbs the rest), and an integer with a float when the
// float's mantissa holds the integer type.
func exactOrNone(a, b dtype.DataType) bool {
	if a.IsFloat() == b.IsFloat() {
		return true
	}
	i := a
	if a.IsFloat() {
		i = b
	}
	return i.BitWidth() <= 32
}

func TestJoinKeysCompareExactly(t *testing.T) {
	var names []string
	for id := range int(dtype.TypeIDCount) {
		n := dtype.TypeID(id).String()
		if n == "Uint128" {
			continue // declared in the enum, and no column of it can be built yet
		}
		if (strings.HasPrefix(n, "Int") || strings.HasPrefix(n, "Uint") || strings.HasPrefix(n, "Float")) &&
			!slices.ContainsFunc(numericKeyTypes, func(d dtype.DataType) bool { return d.String() == n }) {
			names = append(names, n)
		}
	}
	if len(names) > 0 {
		t.Fatalf("numeric types missing from numericKeyTypes: %v", names)
	}

	pool := keyValues()
	var pairs, matched, refused int
	for _, lt := range numericKeyTypes {
		for _, rt := range numericKeyTypes {
			pairs++
			var lv, rv []*big.Rat
			for _, v := range pool {
				if holds(lt, v) {
					lv = append(lv, v)
				}
				if holds(rt, v) {
					rv = append(rv, v)
				}
			}
			name := lt.String() + "->" + rt.String()
			var wrong []string

			// The join.
			df, err := keyFrame(t, lt, lv, "l").
				Join(keyFrame(t, rt, rv, "r"), ursus.JoinOn(ursus.Col("x"))).
				Select(ursus.Col("l"), ursus.Col("r")).Collect(t.Context())
			if !exactOrNone(lt, rt) {
				refused++
				switch {
				case err == nil:
					wrong = append(wrong, fmt.Sprintf("join answered %d rows; want a refusal", df.Height()))
				case !errors.Is(err, ursus.ErrType):
					wrong = append(wrong, "join refused with "+kindOf(err)+": "+err.Error())
				}
			} else if err != nil {
				wrong = append(wrong, "join refused: "+err.Error())
			} else {
				got, err := df.Rows[struct{ L, R int64 }]()
				if err != nil {
					t.Fatal(err)
				}
				var gotPairs, wantPairs []string
				for _, p := range got {
					gotPairs = append(gotPairs, fmt.Sprint(p.L, ",", p.R))
				}
				for i, a := range lv {
					for j, b := range rv {
						if a.Cmp(b) == 0 {
							wantPairs = append(wantPairs, fmt.Sprint(i, ",", j))
							matched++
						}
					}
				}
				slices.Sort(gotPairs)
				slices.Sort(wantPairs)
				if !slices.Equal(gotPairs, wantPairs) {
					wrong = append(wrong, fmt.Sprintf("join matched %v, want %v", gotPairs, wantPairs))
				}
			}

			// The concat: every value comes back exactly, or the pair is refused.
			stacked := ursus.Concat([]*ursus.LazyFrame{keyFrame(t, lt, lv, "i"), keyFrame(t, rt, rv, "i")})
			var outType dtype.Field
			if sc, err := stacked.CollectSchema(t.Context()); err == nil {
				outType, _ = sc.ByName("x")
			}
			cf, err := stacked.Select(ursus.Col("x").Cast(ursus.String)).Collect(t.Context())
			switch {
			case !exactOrNone(lt, rt) && err == nil:
				wrong = append(wrong, "concat answered; want a refusal")
			case !exactOrNone(lt, rt) && !errors.Is(err, ursus.ErrType):
				wrong = append(wrong, "concat refused with "+kindOf(err))
			case !exactOrNone(lt, rt):
			case err != nil:
				wrong = append(wrong, "concat refused: "+err.Error())
			default:
				col, err := cf.Column[string]("x")
				if err != nil {
					t.Fatal(err)
				}
				cells := make([]string, col.Len())
				for i := range cells {
					cells[i], _ = col.Get(i)
				}
				for i, want := range append(slices.Clone(lv), rv...) {
					got, ok := exactValue(cells[i], outType.Type)
					if !ok || got.Cmp(want) != 0 {
						wrong = append(wrong, fmt.Sprintf("concat %s came back as %s", want.RatString(), cells[i]))
					}
				}
			}

			n, known := knownKeyTypeDefects[name]
			switch {
			case known && len(wrong) == 0:
				t.Errorf("%s answers correctly now; delete it from knownKeyTypeDefects", name)
			case known && len(wrong) != n:
				t.Errorf("%s: %d wrong, the ratchet says %d: %v", name, len(wrong), n, wrong)
			case !known && len(wrong) > 0:
				t.Errorf("%s: %v", name, wrong)
			}
		}
	}
	if pairs != 121 || matched < 300 || refused != 12 {
		t.Fatalf("%d pairs, %d matches, %d refusals — the sweep has gone vacuous", pairs, matched, refused)
	}
}
