package kernel

import (
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
)

// BenchmarkNUnique reproduces PDS-H q21's shape without a Parquet reader in the way:
// many groups, a handful of distinct values each. That is the case the storage change
// is about, and the whole-query measurement is too slow and too noisy to iterate on.
//
//	groups  perGroup   what it looks like
//	1.5M    4          q21: lineitem by l_orderkey
//	1k      1000       few groups, many values each — the opposite shape
func benchNUnique(b *testing.B, groups, perGroup int) {
	n := groups * perGroup
	gs := make([]int32, n)
	vals := make([]int64, n)
	for i := range n {
		gs[i] = int32(i / perGroup)
		vals[i] = int64(i % perGroup)
	}
	col := data.NewFixed("v", dtype.Int64, vals, bitmap.AllSet(n))

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		var a nuniqueAcc
		a.Reserve(groups)
		if err := a.AddBatch(gs, col); err != nil {
			b.Fatal(err)
		}
		if _, err := a.Finish("n", groups); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkNUniqueManyGroups(b *testing.B) { benchNUnique(b, 300_000, 4) }
func BenchmarkNUniqueFewGroups(b *testing.B)  { benchNUnique(b, 1_000, 1_000) }
