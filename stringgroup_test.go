package ursus_test

import (
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestAStringKeyGroupsAsTheEncodedKeysDo: a group-by on one String or Binary key
// keys each value's own bytes, read in place, with a null that is never the empty
// string (step 205), where it encoded every key first. A second key, the same in
// every row, makes the same groups through the encoded path. They must agree:
// serially; on eight threads, over a few groups, which the workers' merge folds into
// one table, and over enough for the partitioned fold; in first appearance under
// MaintainOrder; and frozen and spilled under a budget.
//
// The partitioned fold routes the null by its fixed hash and the empty string by
// its own, which here land in different partitions; the merge always puts the two
// in one table.
func TestAStringKeyGroupsAsTheEncodedKeysDo(t *testing.T) {
	const n = 120_000
	frame := func(distinct int) *ursus.LazyFrame {
		rng := rand.New(rand.NewPCG(205, uint64(distinct)))
		k, kok := make([]string, n), make([]bool, n)
		v, one := make([]int64, n), make([]int64, n)
		for i := range n {
			switch r := rng.IntN(100); {
			case r < 5: // a null
			case r < 10:
				k[i], kok[i] = "", true // the empty string, which is not a null
			default:
				k[i], kok[i] = fmt.Sprintf("key-%d", rng.IntN(distinct)), true
			}
			v[i] = int64(i)
		}
		return ursus.Frame(ursus.ValuesNullable("k", k, kok), ursus.Values("v", v), ursus.Values("one", one))
	}
	c := ursus.Col
	aggs := func() []ursus.Expr {
		return []ursus.Expr{c("v").Sum().Alias("s"), ursus.Len().Alias("n"), c("v").Min().Alias("lo")}
	}
	for _, key := range []struct {
		name string
		k    ursus.Expr
	}{{"String", c("k")}, {"Binary", c("k").Cast(ursus.Binary).Alias("k")}} {
		raw := func(distinct int, ordered bool) *ursus.LazyFrame {
			g := frame(distinct).GroupBy(key.k)
			if ordered {
				g = g.MaintainOrder()
			}
			return g.Agg(aggs()...)
		}
		encoded := func(distinct int, ordered bool) *ursus.LazyFrame {
			g := frame(distinct).GroupBy(key.k, c("one"))
			if ordered {
				g = g.MaintainOrder()
			}
			return g.Agg(aggs()...).Drop("one")
		}
		for _, run := range []struct {
			name     string
			distinct int
			ordered  bool
			opts     []ursus.CollectOption
		}{
			{"one thread", 40_000, false, []ursus.CollectOption{ursus.WithThreads(1)}},
			{"eight threads, merged", 300, false, []ursus.CollectOption{ursus.WithThreads(8)}},
			{"eight threads, folded by partition", 40_000, false, []ursus.CollectOption{ursus.WithThreads(8)}},
			{"in first appearance", 40_000, true, []ursus.CollectOption{ursus.WithThreads(1)}},
			{"spilled", 40_000, false, []ursus.CollectOption{ursus.WithThreads(1),
				ursus.WithMemoryLimit(256 << 10), ursus.WithSpillDir(t.TempDir())}},
			{"spilled, in first appearance", 40_000, true, []ursus.CollectOption{ursus.WithThreads(1),
				ursus.WithMemoryLimit(256 << 10), ursus.WithSpillDir(t.TempDir())}},
		} {
			name := key.name + ", " + run.name
			opts := append([]ursus.CollectOption{ursus.WithBatchSize(1024)}, run.opts...)
			var stats ursus.MemoryStats
			got, err := raw(run.distinct, run.ordered).Collect(t.Context(), append(opts, ursus.WithMemoryStats(&stats))...)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			want, err := encoded(run.distinct, run.ordered).Collect(t.Context(), opts...)
			if err != nil {
				t.Fatalf("%s, encoded: %v", name, err)
			}
			if got.Height() < run.distinct*3/4 {
				t.Fatalf("%s: %d groups; the fixture has too few", name, got.Height())
			}
			if run.ordered {
				ursustest.AssertFrameEqual(t, got, want)
			} else if !sameRows(t, got, want) {
				t.Fatalf("%s: %d groups, encoded %d, or they differ", name, got.Height(), want.Height())
			}
			if spilled := len(run.opts) > 1; spilled && stats.Spills == 0 {
				t.Fatalf("%s: nothing spilled", name)
			}
		}
	}
}
