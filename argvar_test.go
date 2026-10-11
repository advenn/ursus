package ursus_test

// Step 176: what step 175 found. Var and Std over large near-equal values, against
// Polars 1.44.1's answers from the bench's environment; and ArgMin and ArgMax, rebuilt
// on typed keys, against a brute force.

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/advenn/ursus"
)

func TestVarOfLargeNearEqualValuesAnswersAsPolars(t *testing.T) {
	c := ursus.Col
	for _, tc := range []struct {
		name string
		vals []float64
		ddof int
		v, s float64
	}{
		{"near 1e9, ddof 1", []float64{1e9 + 1, 1e9 + 2, 1e9 + 4}, 1, 2.3333333333333357, 1.5275252316519474},
		{"near 1e9, ddof 0", []float64{1e9 + 1, 1e9 + 2, 1e9 + 4}, 0, 1.5555555555555571, math.Sqrt(1.5555555555555571)},
		{"quarter seconds near an epoch", []float64{1.7e9 + 0.25, 1.7e9 + 0.5, 1.7e9 + 1.0, 1.7e9 + 0.75}, 1,
			0.10416666666666667, math.Sqrt(0.10416666666666667)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			df, err := ursus.Frame(ursus.Values("x", tc.vals)).GroupBy().
				Agg(c("x").Var(tc.ddof).Alias("v"), c("x").Std(tc.ddof).Alias("s")).Collect(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			v, _ := floats175(t, df, "v")
			s, _ := floats175(t, df, "s")
			if !near175(v[0], tc.v) || !near175(s[0], tc.s) {
				t.Errorf("var %.17g std %.17g, want %.17g and %.17g", v[0], s[0], tc.v, tc.s)
			}
		})
	}
}

// TestVarAndArgExtremaAgreeWithABruteForce: many groups of large near-equal values
// over many batches, on one worker and four, and under a budget that spills, against
// answers computed here: Var by two passes, ArgMin and ArgMax by a scan. The arg
// columns have ties, nulls and NaNs.
func TestVarAndArgExtremaAgreeWithABruteForce(t *testing.T) {
	const rows, groups = 30_000, 1_000
	rng := rand.New(rand.NewPCG(176, 1))
	g, x, by := make([]int64, rows), make([]float64, rows), make([]float64, rows)
	text := make([]string, rows)
	ok := make([]bool, rows)
	for i := range rows {
		g[i] = rng.Int64N(groups)
		x[i] = 1e9 + float64(rng.IntN(1000))/8
		by[i] = float64(rng.IntN(10))
		if rng.IntN(50) == 0 {
			by[i] = math.NaN()
		}
		text[i] = string(rune('a' + rng.IntN(8)))
		ok[i] = rng.IntN(9) != 0
	}
	type want struct {
		xs               []float64
		n                uint64
		lo, hi, slo, shi uint64
		loV, hiV         float64
		sloV, shiV       string
		has              bool
	}
	ws := make([]want, groups)
	// NaN is the greatest value, as ursus orders it; the fixture has no infinity, so
	// +Inf stands for it.
	key := func(f float64) float64 {
		if math.IsNaN(f) {
			return math.Inf(1)
		}
		return f
	}
	for i := range rows {
		w := &ws[g[i]]
		w.xs = append(w.xs, x[i])
		pos := w.n
		w.n++
		if !ok[i] {
			continue
		}
		k := by[i]
		switch {
		case !w.has:
			w.lo, w.hi, w.loV, w.hiV = pos, pos, k, k
			w.slo, w.shi, w.sloV, w.shiV = pos, pos, text[i], text[i]
			w.has = true
		default:
			if key(k) < key(w.loV) {
				w.lo, w.loV = pos, k
			}
			if key(w.hiV) < key(k) {
				w.hi, w.hiV = pos, k
			}
			if text[i] < w.sloV {
				w.slo, w.sloV = pos, text[i]
			}
			if text[i] > w.shiV {
				w.shi, w.shiV = pos, text[i]
			}
		}
	}
	c := ursus.Col
	frame := ursus.Frame(ursus.Values("g", g), ursus.Values("x", x),
		ursus.ValuesNullable("by", by, ok), ursus.ValuesNullable("s", text, ok))
	var spilled ursus.MemoryStats
	for _, run := range []struct {
		name string
		opts []ursus.CollectOption
	}{
		{"one worker", []ursus.CollectOption{ursus.WithThreads(1), ursus.WithBatchSize(700)}},
		{"four workers", []ursus.CollectOption{ursus.WithThreads(4), ursus.WithBatchSize(700)}},
		{"spilling", []ursus.CollectOption{ursus.WithThreads(2), ursus.WithBatchSize(700),
			ursus.WithMemoryLimit(128 << 10), ursus.WithSpillDir(t.TempDir()), ursus.WithMemoryStats(&spilled)}},
	} {
		t.Run(run.name, func(t *testing.T) {
			// Var alone, so it may run on every worker; the arg aggregates refuse to.
			df, err := frame.GroupBy(c("g")).Agg(c("x").Var(1).Alias("v")).
				Sort(ursus.Asc(c("g"))).Collect(t.Context(), run.opts...)
			if err != nil {
				t.Fatal(err)
			}
			v, _ := floats175(t, df, "v")
			for i := range groups {
				xs := ws[i].xs
				n := float64(len(xs))
				var m, ss float64
				for _, e := range xs {
					m += e - 1e9
				}
				m /= n
				for _, e := range xs {
					ss += (e - 1e9 - m) * (e - 1e9 - m)
				}
				if want := ss / (n - 1); math.Abs(v[i]-want) > 1e-9*want {
					t.Fatalf("group %d: var %.17g, want %.17g", i, v[i], want)
				}
			}

			df, err = frame.GroupBy(c("g")).Agg(
				c("by").ArgMin().Alias("lo"), c("by").ArgMax().Alias("hi"),
				c("s").ArgMin().Alias("slo"), c("s").ArgMax().Alias("shi"),
			).Sort(ursus.Asc(c("g"))).Collect(t.Context(), run.opts...)
			if err != nil {
				t.Fatal(err)
			}
			for _, col := range []struct {
				name string
				want func(want) uint64
			}{
				{"lo", func(w want) uint64 { return w.lo }}, {"hi", func(w want) uint64 { return w.hi }},
				{"slo", func(w want) uint64 { return w.slo }}, {"shi", func(w want) uint64 { return w.shi }},
			} {
				got, err := df.Column[uint64](col.name)
				if err != nil {
					t.Fatal(err)
				}
				for i := range groups {
					v, valid := got.Get(i)
					if valid != ws[i].has || (valid && v != col.want(ws[i])) {
						t.Fatalf("group %d: %s %d (valid %v), want %d (valid %v)",
							i, col.name, v, valid, col.want(ws[i]), ws[i].has)
					}
				}
			}
		})
	}
	if spilled.Spills == 0 {
		t.Errorf("the spilling run wrote no file; its budget no longer forces a spill")
	}
}
