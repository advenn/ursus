package ursus_test

import (
	"cmp"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
)

// Step 523: an aggregate that picks one of several values its order calls equal, -0.0
// and +0.0 or NaNs of different payloads, answers the same bits at every thread count,
// batch size and spill, and Min and Max answer the bits a stable Sort puts first and
// last. qcheck's seed 802018528 found Min answering +0 on four threads where one
// thread answered -0: a parallel group-by merged its workers' partial minimums in
// worker order, not row order.
//
// The expected answers are differentials and Go oracles over generated frames, never
// typed: oracle 1 is the same query on one thread in batches of 8192; oracle 2 is
// each group's values sorted stably in Go by the documented order, and ursus's own
// Col(v).DropNulls().Sort().First() and .Last().

// The values the generated columns draw from: the two zeros, NaNs of five payloads
// each, and two numbers. Every NaN's bits are distinct, and a quiet NaN's payload
// survives a load and a store, so whichever row an aggregate picks shows in its bits.
var (
	pickNaN64 = []uint64{
		0x7FF8000000000001, // math.NaN()
		0xFFF8000000000000, // what x86 arithmetic makes, its sign set
		0x7FF8DEADBEEF0001,
		0xFFFC000000000005,
		0x7FF4000000000003, // signalling
	}
	pickNaN32 = []uint32{0x7FC00000, 0xFFC00000, 0x7FC0BEEF, 0xFFE00003, 0x7FA00001}
)

func pickAtoms64() []float64 {
	out := []float64{math.Copysign(0, -1), 0, -1.5, 2.5}
	for _, b := range pickNaN64 {
		out = append(out, math.Float64frombits(b))
	}
	return out
}

func pickAtoms32() []float32 {
	out := []float32{float32(math.Copysign(0, -1)), 0, -1.5, 2.5}
	for _, b := range pickNaN32 {
		out = append(out, math.Float32frombits(b))
	}
	return out
}

// pickData is a generated frame: a key g; v64 and v32, which draw from a subset of
// the atoms chosen per group, with nulls; z64 and z32, zeros of either sign and no
// null, which a keyless aggregate reads whole; n64, NaNs only; and by, the atoms, for
// the by-aggregates.
type pickData struct {
	g             []int64
	v64, z64, n64 []float64
	by            []float64
	v32, z32      []float32
	v64ok, v32ok  []bool
}

func genPicks(seed uint64, rows, groups int) *pickData {
	r := rand.New(rand.NewPCG(seed, seed^0x5eed))
	a64, a32 := pickAtoms64(), pickAtoms32()
	subset := func(n int) [][]int {
		out := make([][]int, groups)
		for g := range out {
			for len(out[g]) == 0 {
				for i := range n {
					if r.IntN(2) == 0 {
						out[g] = append(out[g], i)
					}
				}
			}
		}
		return out
	}
	s64, s32, sby := subset(len(a64)), subset(len(a32)), subset(len(a64))
	d := &pickData{}
	for range rows {
		g := r.IntN(groups)
		d.g = append(d.g, int64(g))
		d.v64 = append(d.v64, a64[s64[g][r.IntN(len(s64[g]))]])
		d.v64ok = append(d.v64ok, r.IntN(8) != 0)
		d.v32 = append(d.v32, a32[s32[g][r.IntN(len(s32[g]))]])
		d.v32ok = append(d.v32ok, r.IntN(8) != 0)
		d.by = append(d.by, a64[sby[g][r.IntN(len(sby[g]))]])
		d.z64 = append(d.z64, a64[r.IntN(2)])
		d.z32 = append(d.z32, a32[r.IntN(2)])
		d.n64 = append(d.n64, a64[4+r.IntN(len(pickNaN64))])
	}
	return d
}

func (d *pickData) frame() *ursus.LazyFrame {
	return ursus.Frame(
		ursus.Values("g", d.g),
		ursus.ValuesNullable("v64", d.v64, d.v64ok),
		ursus.ValuesNullable("v32", d.v32, d.v32ok),
		ursus.Values("z64", d.z64),
		ursus.Values("z32", d.z32),
		ursus.Values("n64", d.n64),
		ursus.Values("by", d.by),
	)
}

// pickCols are the columns each aggregate below reads.
var pickCols = []string{"v64", "v32", "z64", "z32", "n64"}

// orderFreeAggs are the aggregates that pick a value by the total order and that a
// parallel group-by runs on every worker: none is order-dependent, so the whole list
// stays parallel.
func orderFreeAggs() []ursus.Expr {
	c := ursus.Col
	var out []ursus.Expr
	for _, v := range pickCols {
		out = append(out,
			c(v).Min().Alias("min "+v),
			c(v).Max().Alias("max "+v),
			c(v).NanMin().Alias("nanmin "+v),
			c(v).NanMax().Alias("nanmax "+v),
			c(v).TopK(1).Alias("top1 "+v),
			c(v).TopK(3).Alias("top3 "+v),
			c(v).BottomK(2).Alias("bottom2 "+v),
			c(v).TopKBy(c("by"), 2).Alias("topby2 "+v),
			c(v).BottomKBy(c("by"), 1).Alias("bottomby1 "+v),
			c(v).Median().Alias("median "+v),
		)
	}
	for _, v := range []string{"v64", "z64", "v32"} {
		for _, in := range []struct {
			name   string
			interp ursus.Interpolation
		}{{"lower", ursus.InterpLower}, {"higher", ursus.InterpHigher},
			{"nearest", ursus.InterpNearest}, {"midpoint", ursus.InterpMidpoint},
			{"linear", ursus.InterpLinear}} {
			out = append(out, c(v).Quantile(0.25, in.interp).Alias("q25"+in.name+" "+v))
		}
	}
	// A filtered input: the rows an accumulator sees are not the batch's.
	out = append(out,
		c("v64").Filter(c("z64").Gt(c("by"))).Min().Alias("filtered min v64"),
		c("v64").Filter(c("z64").Lt(c("by"))).Max().Alias("filtered max v64"))
	return out
}

// orderAggs are the aggregates that pick by position, ArgMin and its kin: order-
// dependent, so they keep a group-by serial, and are here to show it.
func orderAggs() []ursus.Expr {
	c := ursus.Col
	var out []ursus.Expr
	for _, v := range pickCols {
		out = append(out,
			c(v).ArgMin().Alias("argmin "+v),
			c(v).ArgMax().Alias("argmax "+v),
			c(v).MinBy(c("by")).Alias("minby "+v),
			c(v).MaxBy(c("by")).Alias("maxby "+v),
		)
	}
	return out
}

// sortAggs are oracle 2 in ursus's own terms: each group's values sorted, stably,
// first and last.
func sortAggs() []ursus.Expr {
	c := ursus.Col
	var out []ursus.Expr
	for _, v := range pickCols {
		out = append(out,
			c(v).DropNulls().Sort().First().Alias("sortfirst "+v),
			c(v).DropNulls().Sort().Last().Alias("sortlast "+v))
	}
	return out
}

type pickConfig struct {
	threads, batch int
	ordered        bool
	limit          int64 // a memory limit, which spills; 0 is none
}

func (c pickConfig) String() string {
	return fmt.Sprintf("threads %d batch %d ordered %v limit %d", c.threads, c.batch, c.ordered, c.limit)
}

// pickReference is oracle 1's configuration.
var pickReference = pickConfig{threads: 1, batch: 8192}

// pickVariants are the configurations every answer is held to the reference's in:
// threads {1, 4, 8} by batch sizes {1, 2, 3, 7, 1024}, with and without
// MaintainOrder.
func pickVariants() []pickConfig {
	var out []pickConfig
	for _, th := range []int{1, 4, 8} {
		for _, b := range []int{1, 2, 3, 7, 1024} {
			for _, o := range []bool{false, true} {
				out = append(out, pickConfig{threads: th, batch: b, ordered: o})
			}
		}
	}
	return out
}

// pickQuery is aggs grouped by keys, or keyless, its rows in key order.
func pickQuery(lf *ursus.LazyFrame, keys []string, ordered bool, aggs []ursus.Expr) *ursus.LazyFrame {
	ks := make([]ursus.Expr, len(keys))
	sk := make([]ursus.SortKey, len(keys))
	for i, k := range keys {
		ks[i], sk[i] = ursus.Col(k), ursus.Asc(ursus.Col(k))
	}
	gb := lf.GroupBy(ks...)
	if ordered {
		gb = gb.MaintainOrder()
	}
	q := gb.Agg(aggs...)
	if len(keys) > 0 {
		q = q.Sort(sk...)
	}
	return q
}

func collectPicks(t *testing.T, q *ursus.LazyFrame, c pickConfig) *ursus.DataFrame {
	t.Helper()
	opts := []ursus.CollectOption{ursus.WithThreads(c.threads), ursus.WithBatchSize(c.batch), ursus.WithVerify()}
	var stats ursus.MemoryStats
	if c.limit > 0 {
		opts = append(opts, ursus.WithMemoryLimit(c.limit), ursus.WithSpillDir(t.TempDir()), ursus.WithMemoryStats(&stats))
	}
	df, err := q.Collect(t.Context(), opts...)
	if err != nil {
		t.Fatalf("%v: %v", c, err)
	}
	if c.limit > 0 && stats.Spills == 0 {
		t.Fatalf("%v: nothing spilled, so the spill proves nothing", c)
	}
	return df
}

// cellBits is a cell as its bits: a float's whole word, a list's elements', a null as
// null.
func cellBits(c *ursus.Column, i int) string {
	if !c.IsValid(i) {
		return "null"
	}
	switch c.DType().ID() {
	case dtype.TypeList:
		l := c.Lists()
		lo, hi, _ := l.Get(i)
		parts := make([]string, 0, hi-lo)
		for j := lo; j < hi; j++ {
			parts = append(parts, cellBits(l.Child(), int(j)))
		}
		return "[" + strings.Join(parts, " ") + "]"
	case dtype.TypeFloat64:
		return fmt.Sprintf("%016x", math.Float64bits(data.MustValues[float64](c)[i]))
	case dtype.TypeFloat32:
		return fmt.Sprintf("%08x", math.Float32bits(data.MustValues[float32](c)[i]))
	case dtype.TypeInt64:
		return fmt.Sprint(data.MustValues[int64](c)[i])
	case dtype.TypeUint32:
		return fmt.Sprint(data.MustValues[uint32](c)[i])
	case dtype.TypeUint64:
		return fmt.Sprint(data.MustValues[uint64](c)[i])
	}
	panic(fmt.Sprintf("cellBits: a %s column", c.DType()))
}

// samePickBits holds got to want cell by cell, by bits, and reports each column's
// first difference.
func samePickBits(t *testing.T, what string, got, want *ursus.DataFrame) {
	t.Helper()
	if got.Height() != want.Height() || !slices.Equal(got.Columns(), want.Columns()) {
		t.Fatalf("%s: %d rows of %v, want %d of %v", what, got.Height(), got.Columns(), want.Height(), want.Columns())
	}
	for j, name := range want.Columns() {
		g, w := got.ColumnAt(j), want.ColumnAt(j)
		for i := range want.Height() {
			if gb, wb := cellBits(g, i), cellBits(w, i); gb != wb {
				t.Errorf("%s: %q row %d is %s, the reference's %s", what, name, i, gb, wb)
				break
			}
		}
	}
}

// TestAFloatPickIsTheSameAtEveryThreadCount is oracle 1: every pick, keyed and
// keyless, bit for bit the reference's in every configuration.
func TestAFloatPickIsTheSameAtEveryThreadCount(t *testing.T) {
	for _, seed := range []uint64{523, 802018528} {
		lf := genPicks(seed, 1500, 41).frame()
		for _, shape := range []struct {
			name string
			keys []string
			aggs []ursus.Expr
		}{
			{"keyed", []string{"g"}, orderFreeAggs()},
			{"keyless", nil, orderFreeAggs()},
			{"keyed by position", []string{"g"}, orderAggs()},
		} {
			ref := collectPicks(t, pickQuery(lf, shape.keys, false, shape.aggs), pickReference)
			for _, c := range pickVariants() {
				got := collectPicks(t, pickQuery(lf, shape.keys, c.ordered, shape.aggs), c)
				samePickBits(t, fmt.Sprintf("seed %d %s %v", seed, shape.name, c), got, ref)
			}
		}
	}
}

// TestAFloatPickIsTheSameSpilled is oracle 1 under a memory limit, where the group-by
// is serial and spills: the quantiles' values to disk, and with many groups the keys.
func TestAFloatPickIsTheSameSpilled(t *testing.T) {
	for _, shape := range []struct {
		name         string
		rows, groups int
		limit        int64 // low enough to spill, high enough for the hottest group
	}{{"few groups", 1500, 41, 64 << 10}, {"many groups", 6000, 2000, 512 << 10}} {
		lf := genPicks(523, shape.rows, shape.groups).frame()
		q := func(ordered bool) *ursus.LazyFrame {
			return pickQuery(lf, []string{"g"}, ordered, orderFreeAggs())
		}
		ref := collectPicks(t, q(false), pickReference)
		for _, c := range []pickConfig{
			{threads: 4, batch: 7},
			{threads: 1, batch: 64},
			{threads: 8, batch: 1024, ordered: true},
		} {
			c.limit = shape.limit
			samePickBits(t, fmt.Sprintf("%s %v", shape.name, c), collectPicks(t, q(c.ordered), c), ref)
		}
	}
}

// TestAFloatPickIsTheSameFoldedPartitioned is oracle 1 over enough groups that the
// parallel group-by folds its workers a partition each (aggmerge.go's
// foldPartitioned), not one into another.
//
// And a float key folded so: fk is g as a float, but from row 6000 on every
// thousandth group's rows are zeros of either sign, one group, and the next
// thousandth's NaNs, another, rows 6000 and 6001 the first of each. Row 6000 is in a
// batch dealt to worker 1 at both configurations below, so worker 0, folded first,
// holds a later row of each.
func TestAFloatPickIsTheSameFoldedPartitioned(t *testing.T) {
	d := genPicks(5230, 160_000, 80_000)
	fk := make([]float64, len(d.g))
	for i, g := range d.g {
		switch {
		case i == 6000 || i > 6001 && g%1000 == 0:
			fk[i] = d.z64[i]
		case i == 6001 || i > 6001 && g%1000 == 1:
			fk[i] = d.n64[i]
		default:
			fk[i] = float64(g)
		}
	}
	lf := ursus.Frame(ursus.Values("g", d.g), ursus.Values("fk", fk),
		ursus.ValuesNullable("v64", d.v64, d.v64ok), ursus.ValuesNullable("v32", d.v32, d.v32ok),
		ursus.Values("z64", d.z64), ursus.Values("z32", d.z32), ursus.Values("n64", d.n64),
		ursus.Values("by", d.by))
	for _, shape := range []struct {
		key  string
		aggs []ursus.Expr
	}{{"g", orderFreeAggs()[:40]}, {"fk", []ursus.Expr{ursus.Len().Alias("n"), ursus.Col("v64").Min().Alias("min v64")}}} {
		q := pickQuery(lf, []string{shape.key}, false, shape.aggs)
		ref := collectPicks(t, q, pickReference)
		for _, c := range []pickConfig{{threads: 4, batch: 1024}, {threads: 8, batch: 4096}} {
			samePickBits(t, fmt.Sprintf("partitioned by %s %v", shape.key, c), collectPicks(t, q, c), ref)
		}
	}
}

// orderKey64 is the documented total order as a key: NaN above everything, every NaN
// alike, -0.0 equal to +0.0. The oracle's own, written from the doc, not the kernel's.
func orderKey64(x float64) (int, float64) {
	switch {
	case math.IsNaN(x):
		return 1, 0
	case x == 0:
		return 0, 0
	}
	return 0, x
}

func compareOrder64(x, y float64) int {
	xa, xb := orderKey64(x)
	ya, yb := orderKey64(y)
	if c := cmp.Compare(xa, ya); c != 0 {
		return c
	}
	return cmp.Compare(xb, yb)
}

// TestFloatMinAndMaxAreTheStableSortsEnds is oracle 2: the reference's Min and Max
// are each group's values sorted stably, first and last, in bits, as Go sorts them
// and as ursus's Sort does; NanMin is the first by the order that puts NaN first;
// and ArgMin and ArgMax are the earliest row of the least or greatest.
func TestFloatMinAndMaxAreTheStableSortsEnds(t *testing.T) {
	d := genPicks(523, 1500, 41)
	lf := d.frame()
	ref := collectPicks(t, pickQuery(lf, []string{"g"}, false, append(orderFreeAggs(), orderAggs()...)), pickReference)
	sorted := collectPicks(t, pickQuery(lf, []string{"g"}, false, sortAggs()), pickReference)
	keys := data.MustValues[int64](ref.ColumnAt(0))

	// Each column's values as float64, for the order, and each cell's bits at its own
	// width: a Float32 NaN widened and narrowed again may not keep its bits.
	type oracleCol struct {
		order []float64
		bits  []string
		ok    []bool
	}
	of64 := func(v []float64, ok []bool) oracleCol {
		c := oracleCol{order: v, ok: ok}
		for _, x := range v {
			c.bits = append(c.bits, fmt.Sprintf("%016x", math.Float64bits(x)))
		}
		return c
	}
	of32 := func(v []float32, ok []bool) oracleCol {
		c := oracleCol{ok: ok}
		for _, x := range v {
			c.order = append(c.order, float64(x))
			c.bits = append(c.bits, fmt.Sprintf("%08x", math.Float32bits(x)))
		}
		return c
	}
	cols := map[string]oracleCol{
		"v64": of64(d.v64, d.v64ok), "v32": of32(d.v32, d.v32ok),
		"z64": of64(d.z64, nil), "z32": of32(d.z32, nil), "n64": of64(d.n64, nil),
	}
	col := func(df *ursus.DataFrame, name string) *ursus.Column {
		i := slices.Index(df.Columns(), name)
		if i < 0 {
			t.Fatalf("no column %q", name)
		}
		return df.ColumnAt(i)
	}
	for _, v := range pickCols {
		oc := cols[v]
		for r, k := range keys {
			// The group's non-null values in row order, their bits, and their rows in
			// the group, nulls counted.
			var in []float64
			var bits []string
			var at []int
			n := 0
			for i, g := range d.g {
				if g != k {
					continue
				}
				if oc.ok == nil || oc.ok[i] {
					in, bits, at = append(in, oc.order[i]), append(bits, oc.bits[i]), append(at, n)
				}
				n++
			}
			// The oracle's cells, none for a group of no value, whose cells are null.
			want := map[string]string{}
			if len(in) > 0 {
				idx := make([]int, len(in))
				for i := range idx {
					idx[i] = i
				}
				slices.SortStableFunc(idx, func(a, b int) int { return compareOrder64(in[a], in[b]) })
				first, last := idx[0], idx[len(idx)-1]
				want["min "], want["max "] = bits[first], bits[last]
				// ArgMin and ArgMax: the earliest of the least, and of the greatest.
				want["argmin "] = fmt.Sprint(at[first])
				for _, i := range idx {
					if compareOrder64(in[i], in[last]) == 0 {
						want["argmax "] = fmt.Sprint(at[i])
						break
					}
				}
				// NanMin: the first NaN, or the least.
				want["nanmin "] = bits[first]
				for i, x := range in {
					if math.IsNaN(x) {
						want["nanmin "] = bits[i]
						break
					}
				}
			}
			for _, agg := range []string{"min ", "max ", "nanmin ", "argmin ", "argmax "} {
				c := col(ref, agg+v)
				w, has := want[agg]
				switch got := cellBits(c, r); {
				case !has && c.IsValid(r):
					t.Errorf("group %d: %s%s is %s, of no value", k, agg, v, got)
				case has && got != w:
					t.Errorf("group %d: %s%s is %s, the Go oracle's %s", k, agg, v, got, w)
				}
			}
			for agg, sortAgg := range map[string]string{"min ": "sortfirst ", "max ": "sortlast "} {
				if got, w := cellBits(col(ref, agg+v), r), cellBits(col(sorted, sortAgg+v), r); got != w {
					t.Errorf("group %d: %s%s is %s, %s%s %s", k, agg, v, got, sortAgg, v, w)
				}
			}
		}
	}
}

// TestFloatMinOfAJoinedGroupIsTheSameOnFourThreads is qcheck's seed 802018528's
// shape, a join on a float key and a group-by of the right side's key, over this
// file's frames, on the configuration that reproduced it, four threads in batches of
// three. The seed's own case runs in qcheck/self's TestFoundSeedsFindNothing.
func TestFloatMinOfAJoinedGroupIsTheSameOnFourThreads(t *testing.T) {
	lf := genPicks(802018528, 184, 23).frame()
	q := func(ordered bool) *ursus.LazyFrame {
		joined := lf.Join(lf, ursus.JoinOn(ursus.Col("z64")), ursus.JoinHow(ursus.JoinInner),
			ursus.JoinMaintainOrder(ursus.JoinOrderLeft))
		return pickQuery(joined, []string{"g_right"}, ordered, []ursus.Expr{
			ursus.Len().Alias("n"), ursus.Col("v64").Min().Alias("min v64"),
			ursus.Col("v64").Max().Alias("max v64")})
	}
	ref := collectPicks(t, q(false), pickReference)
	samePickBits(t, "threads 4 batch 3", collectPicks(t, q(false), pickConfig{threads: 4, batch: 3}), ref)
}

// TestAFloatGroupKeyIsTheSameAtEveryThreadCount: a group of a float key holds rows
// whose keys the order calls equal, -0.0 and +0.0, or NaNs, and its key is its first
// row's at every thread count, as one thread answers it.
func TestAFloatGroupKeyIsTheSameAtEveryThreadCount(t *testing.T) {
	d := genPicks(523, 1500, 41)
	lf := d.frame()
	for _, key := range []string{"z64", "z32", "n64", "v64"} {
		q := func(ordered bool) *ursus.LazyFrame {
			return pickQuery(lf, []string{key}, ordered, []ursus.Expr{ursus.Len().Alias("n")})
		}
		ref := collectPicks(t, q(false), pickReference)
		for _, c := range pickVariants() {
			samePickBits(t, fmt.Sprintf("key %s %v", key, c), collectPicks(t, q(c.ordered), c), ref)
		}
		// The Go oracle: each key the bits of the first row of its group, the rows
		// grouped by the documented order, every NaN one value and -0 one with +0.
		if key != "z64" && key != "n64" {
			continue
		}
		group := func(x float64) [2]float64 { a, b := orderKey64(x); return [2]float64{float64(a), b} }
		first := map[[2]float64]string{}
		for _, x := range map[string][]float64{"z64": d.z64, "n64": d.n64}[key] {
			if _, ok := first[group(x)]; !ok {
				first[group(x)] = fmt.Sprintf("%016x", math.Float64bits(x))
			}
		}
		keys := data.MustValues[float64](ref.ColumnAt(0))
		if len(keys) != len(first) {
			t.Fatalf("key %s: %d groups, the oracle's %d", key, len(keys), len(first))
		}
		for r, x := range keys {
			if got, want := cellBits(ref.ColumnAt(0), r), first[group(x)]; got != want {
				t.Errorf("key %s: group %d's key is %s, its first row's %s", key, r, got, want)
			}
		}
	}
}

// TestAFloatSumOfZerosIsTheSameAtEveryThreadCount: the aggregates built on a float
// sum keep a zero's sign at every thread count. A sum starts at +0 and so is never
// -0, and a product's sign is its factors', in any order. qcheck's seed 807010171
// looked like a Mean's -0 against +0; it was the same query's Max, a pick, and this
// holds the sums to the bit over zeros of either sign. Over NaNs of several payloads
// their answer is NaN at every thread count, but which payload is the arithmetic's
// order's, as the last bits of a sum are (step 512's float-threads rule), so there a
// NaN is compared as a NaN.
func TestAFloatSumOfZerosIsTheSameAtEveryThreadCount(t *testing.T) {
	c := ursus.Col
	var aggs []ursus.Expr
	for _, v := range []string{"z64", "z32", "n64"} {
		aggs = append(aggs,
			c(v).Sum().Alias("sum "+v), c(v).Mean().Alias("mean "+v),
			c(v).Var(1).Alias("var "+v), c(v).Std(0).Alias("std "+v),
			c(v).Product().Alias("product "+v))
	}
	lf := genPicks(523, 1500, 41).frame()
	for _, keys := range [][]string{{"g"}, nil} {
		ref := collectPicks(t, pickQuery(lf, keys, false, aggs), pickReference)
		for _, cf := range pickVariants() {
			got := collectPicks(t, pickQuery(lf, keys, cf.ordered, aggs), cf)
			sameSumBits(t, fmt.Sprintf("keys %v %v", keys, cf), got, ref)
		}
	}
}

// sameSumBits is samePickBits with a NaN of any payload one NaN, in the columns of
// the NaN-holding inputs.
func sameSumBits(t *testing.T, what string, got, want *ursus.DataFrame) {
	t.Helper()
	if got.Height() != want.Height() || !slices.Equal(got.Columns(), want.Columns()) {
		t.Fatalf("%s: %d rows of %v, want %d of %v", what, got.Height(), got.Columns(), want.Height(), want.Columns())
	}
	for j, name := range want.Columns() {
		g, w := got.ColumnAt(j), want.ColumnAt(j)
		nan := strings.HasSuffix(name, "n64")
		for i := range want.Height() {
			gb, wb := cellBits(g, i), cellBits(w, i)
			if nan && isNaNCell(g, i) && isNaNCell(w, i) {
				continue
			}
			if gb != wb {
				t.Errorf("%s: %q row %d is %s, the reference's %s", what, name, i, gb, wb)
				break
			}
		}
	}
}

// isNaNCell reports whether a Float64 cell is a NaN.
func isNaNCell(c *ursus.Column, i int) bool {
	return c.IsValid(i) && c.DType().ID() == dtype.TypeFloat64 && math.IsNaN(data.MustValues[float64](c)[i])
}
