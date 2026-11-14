// Package gen draws qcheck's cases from a seed: the tables, as Arrow records, and a
// well-typed query over them. A case's seed alone rebuilds it, tables and query,
// on any machine: every draw is from a PCG seeded by it.
package gen

import (
	"fmt"
	"math"
	"math/rand/v2"
	"strings"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/advenn/ursus/qcheck/model"
)

// The sizes step 453 lists: 0 to 3000 rows, and the batch's edges.
var edgeSizes = []int{8191, 8192, 8193}

// NullRates are the null rates a column draws from.
var NullRates = []float64{0, 0.1, 0.5, 1}

// size draws a table's row count: none, a few, hundreds, thousands, or one of the
// 8192-row batch's edges.
func size(r *rand.Rand, max int) int {
	var n int
	switch p := r.IntN(100); {
	case p < 5:
		n = 0
	case p < 20:
		n = 1 + r.IntN(10)
	case p < 50:
		n = 11 + r.IntN(290)
	case p < 88:
		n = 301 + r.IntN(2700)
	default:
		n = edgeSizes[r.IntN(len(edgeSizes))]
	}
	return min(n, max)
}

var typeTags = map[model.Type]string{
	model.Int64: "i64", model.Int32: "i32", model.Float64: "f64",
	model.String: "str", model.Bool: "bool", model.Date: "date",
}

// Table draws a table named name with at most maxRows rows. Its columns are two to
// six of the base types, an integer always among them, so that a group-by has a
// key.
func Table(seed uint64, name string, maxRows int) *model.Table {
	r := rand.New(rand.NewPCG(splitmix(seed), splitmix(seed^0x5eed7ab1e)))
	t := &model.Table{Name: name, Seed: seed, Rows: size(r, maxRows)}
	ncols := 2 + r.IntN(5)
	for i := range ncols {
		typ := model.Base[r.IntN(len(model.Base))]
		if i == 0 {
			typ = []model.Type{model.Int64, model.Int32}[r.IntN(2)]
		}
		c := model.Column{
			Name:     fmt.Sprintf("%s%c", typeTags[typ], 'a'+i),
			Type:     typ,
			Profile:  model.Profile(r.IntN(3)),
			NullRate: NullRates[r.IntN(len(NullRates))],
		}
		if r.IntN(3) == 0 {
			// Mostly not null, so keys and sums have values.
			c.NullRate = 0
		}
		if c.Profile == model.Tame && r.IntN(2) == 0 {
			c.Card = []int{1, 2, 3, 5, 8, 13, 100}[r.IntN(7)]
		}
		t.Cols = append(t.Cols, c)
	}
	t.Rec = record(r, t)
	return t
}

// record draws t's rows.
func record(r *rand.Rand, t *model.Table) arrow.RecordBatch {
	fields := make([]arrow.Field, len(t.Cols))
	for i, c := range t.Cols {
		fields[i] = arrow.Field{Name: c.Name, Type: ArrowType(c.Type), Nullable: true}
	}
	b := array.NewRecordBuilder(memory.NewGoAllocator(), arrow.NewSchema(fields, nil))
	defer b.Release()
	for i, c := range t.Cols {
		v := values{r: r, c: c}
		v.pool()
		fb := b.Field(i)
		for range t.Rows {
			if c.NullRate > 0 && r.Float64() < c.NullRate {
				fb.AppendNull()
				continue
			}
			switch fb := fb.(type) {
			case *array.Int64Builder:
				fb.Append(v.int64())
			case *array.Int32Builder:
				fb.Append(int32(v.int32()))
			case *array.Float64Builder:
				fb.Append(v.float64())
			case *array.StringBuilder:
				fb.Append(v.string())
			case *array.BooleanBuilder:
				fb.Append(v.bool())
			case *array.Date32Builder:
				fb.Append(arrow.Date32(v.date()))
			}
		}
	}
	return b.NewRecordBatch()
}

// ArrowType is the Arrow type a base type is generated as.
func ArrowType(t model.Type) arrow.DataType {
	switch t {
	case model.Int64:
		return arrow.PrimitiveTypes.Int64
	case model.Int32:
		return arrow.PrimitiveTypes.Int32
	case model.Float64:
		return arrow.PrimitiveTypes.Float64
	case model.String:
		return arrow.BinaryTypes.String
	case model.Bool:
		return arrow.FixedWidthTypes.Boolean
	case model.Date:
		return arrow.FixedWidthTypes.Date32
	}
	panic("gen: no Arrow type for " + t.String())
}

// values draws one column's values by its profile.
type values struct {
	r    *rand.Rand
	c    model.Column
	ints []int64   // a tame column's distinct values, when Card bounds them
	strs []string  // likewise
	flts []float64 // likewise
	bias float64   // a Boolean's chance of true
}

func (v *values) pool() {
	switch v.c.Profile {
	case model.Tame:
		v.bias = 0.5
	case model.Wild:
		v.bias = 0.9
	default:
		v.bias = float64(v.r.IntN(2)) // all true or all false
	}
	if v.c.Card == 0 {
		return
	}
	for range v.c.Card {
		v.ints = append(v.ints, int64(v.r.IntN(41))-20)
		v.flts = append(v.flts, float64(v.r.IntN(81)-40)/4)
		v.strs = append(v.strs, tameWords[v.r.IntN(len(tameWords))])
	}
}

var tameWords = []string{"", "a", "b", "c", "ab", "ba", "abc", "x", "y", "zz", "A", "B"}

// The extremes each type draws from under the extreme profile.
var (
	extremeInt64 = []int64{math.MinInt64, math.MaxInt64, math.MinInt64 + 1, math.MaxInt64 - 1, 0, -1, 1,
		math.MinInt32, math.MaxInt32, 1 << 53, -(1 << 53), 1<<53 + 1}
	extremeInt32 = []int64{math.MinInt32, math.MaxInt32, math.MinInt32 + 1, math.MaxInt32 - 1, 0, -1, 1,
		math.MinInt16, math.MaxInt16, math.MinInt8, math.MaxInt8}
	extremeFloat = []float64{math.NaN(), 0, math.Copysign(0, -1), math.Inf(1), math.Inf(-1),
		math.MaxFloat64, -math.MaxFloat64, math.SmallestNonzeroFloat64, -math.SmallestNonzeroFloat64,
		2.2250738585072014e-308, 1 << 53, 1<<53 + 2, -(1 << 63), 1 << 63, 0.1, 1e308, -1e308, 1, -1}
	extremeDate = []int64{math.MinInt32, math.MaxInt32, 0, -1, 1, -719162, 2932896, 2932897, -719163}
	unicode     = []string{"Zoë", "日本語", "🐻", "é", "\u0000", "a\u0000b", "‮abc", "ß", "İ",
		"\U0010FFFF", "�", " ", "\t\n", "Ω≈ç√", "🏳️‍🌈"}
)

func (v *values) pick(n int) bool { return v.r.IntN(100) < n }

func (v *values) int64() int64 {
	switch {
	case v.ints != nil:
		return v.ints[v.r.IntN(len(v.ints))]
	case v.c.Profile == model.Tame:
		return int64(v.r.IntN(201)) - 100
	case v.c.Profile == model.Extreme && v.pick(50):
		return extremeInt64[v.r.IntN(len(extremeInt64))]
	case v.pick(50):
		return int64(v.r.Uint64())
	}
	return int64(v.r.IntN(2_000_001)) - 1_000_000
}

func (v *values) int32() int64 {
	switch {
	case v.ints != nil:
		return v.ints[v.r.IntN(len(v.ints))]
	case v.c.Profile == model.Tame:
		return int64(v.r.IntN(201)) - 100
	case v.c.Profile == model.Extreme && v.pick(50):
		return extremeInt32[v.r.IntN(len(extremeInt32))]
	case v.pick(50):
		return int64(int32(v.r.Uint32()))
	}
	return int64(v.r.IntN(200_001)) - 100_000
}

func (v *values) float64() float64 {
	switch {
	case v.flts != nil:
		return v.flts[v.r.IntN(len(v.flts))]
	case v.c.Profile == model.Tame:
		// Quarters: their sums are exact, so their bits never move with threads.
		return float64(v.r.IntN(801)-400) / 4
	case v.c.Profile == model.Extreme && v.pick(40):
		return extremeFloat[v.r.IntN(len(extremeFloat))]
	case v.pick(10):
		// Any finite bits.
		for {
			if f := math.Float64frombits(v.r.Uint64()); !math.IsNaN(f) && !math.IsInf(f, 0) {
				return f
			}
		}
	}
	return v.r.NormFloat64() * math.Pow(10, float64(v.r.IntN(16)-5))
}

func (v *values) string() string {
	switch {
	case v.strs != nil:
		return v.strs[v.r.IntN(len(v.strs))]
	case v.c.Profile == model.Tame:
		return tameWords[v.r.IntN(len(tameWords))]
	case v.c.Profile == model.Extreme && v.pick(30):
		return unicode[v.r.IntN(len(unicode))]
	case v.c.Profile == model.Extreme && v.pick(5):
		// Long text, past any inline or small-string layout.
		return strings.Repeat(string(rune('a'+v.r.IntN(26))), 1000+v.r.IntN(4000))
	case v.pick(5):
		// A number as text, for the casts from String.
		return fmt.Sprint(v.r.IntN(2001) - 1000)
	}
	n := v.r.IntN(21)
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(32 + v.r.IntN(95))
	}
	return string(b)
}

func (v *values) bool() bool { return v.r.Float64() < v.bias }

func (v *values) date() int64 {
	switch {
	case v.ints != nil:
		return 19000 + v.ints[v.r.IntN(len(v.ints))]
	case v.c.Profile == model.Tame:
		return 18262 + int64(v.r.IntN(731)) // 2020 and 2021
	case v.c.Profile == model.Extreme && v.pick(40):
		return extremeDate[v.r.IntN(len(extremeDate))]
	}
	return int64(v.r.IntN(2_000_001)) - 1_000_000
}
