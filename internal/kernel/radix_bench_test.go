package kernel_test

// Sorting is benchmarked HERE, at the kernel, rather than through bench/micro,
// because the only interesting question about a radix sort is one of SCALE.
//
// The LSD distribution loop touches one 8-byte key per row per round. Whether
// that is cheap depends entirely on whether the key array fits in cache: at a
// million rows it is 8 MB, which is exactly this machine's L3, and at eight
// million it is 64 MB, which is not close. bench/micro's fixture is a million
// rows and reaches the sort through a Parquet scan, so it can neither separate
// the two regimes nor isolate the sort from the IO — a change that only matters
// out of cache looks like noise there.
//
// No IO, no fixture file, one seeded PRNG: what varies between runs is n.

import (
	"math/rand/v2"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
)

// sortFixture builds the two key shapes that matter.
//
// One float64 key is a plain Sort. A dense int32 partition id in front of a
// float64 is windowSink.segments' exact shape — the partition ids come from
// KeyTable and are therefore dense in [0, nGroups) — and it is what h2o gb8 runs.
func sortFixture(n int, twoKey bool) ([]*data.Column, []kernel.SortSpec) {
	rng := rand.New(rand.NewPCG(7, 11))

	vals := make([]float64, n)
	for i := range vals {
		vals[i] = rng.Float64()
	}
	val := data.NewFixed("v", dtype.Float64, vals, bitmap.AllSet(n))

	if !twoKey {
		return []*data.Column{val}, []kernel.SortSpec{{}}
	}

	gid := make([]int32, n)
	for i := range gid {
		gid[i] = int32(rng.IntN(10_000))
	}
	return []*data.Column{
			data.NewFixed("gid", dtype.Int32, gid, bitmap.AllSet(n)),
			val,
		}, []kernel.SortSpec{{}, {Descending: true}}
}

func BenchmarkArgSortColumns(b *testing.B) {
	// 1<<20 keys are 8 MB — L3-sized on the machine this was written on. 1<<23 are
	// 64 MB, which is not. The pair is the point.
	for _, n := range []int{1 << 20, 1 << 23} {
		for _, shape := range []struct {
			name   string
			twoKey bool
		}{{"1key", false}, {"2key", true}} {
			cols, specs := sortFixture(n, shape.twoKey)
			b.Run(shape.name+"/n="+itoa(n), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if _, err := kernel.ArgSortColumns(cols, specs, n); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
