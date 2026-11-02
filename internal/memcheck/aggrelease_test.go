package memcheck_test

import (
	"runtime"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/internal/physical"
)

// TestAGroupByLetsGoOfEachAggregateAsItFinishes (step 432): a group-by building its
// answer finishes its aggregates one at a time, and lets go of each one's state as
// its column is made (step 131). A median holds every value of every group: over a
// million rows of two a key, a slice a group. Read after two collections, once the
// keys are copied and again once the median's column is made, before the sum's and
// the implode's are, the live heap has fallen by at least 8 MB: 15.8 MB at step
// 432, its state less its answer's 4 MB. Kept, the state would have stayed and the
// answer added to it. Step 131 recorded this unmeasured.
func TestAGroupByLetsGoOfEachAggregateAsItFinishes(t *testing.T) {
	const n = 1_000_000
	k, v := make([]int64, n), make([]float64, n)
	for i := range n {
		k[i], v[i] = int64(i/2), float64(i%1013)+0.25
	}
	df, err := ursus.Frame(ursus.Values("k", k), ursus.Values("v", v)).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	c := ursus.Col
	q := df.Lazy().GroupBy(c("k")).Agg(c("v").Median().Alias("m"), c("v").Sum().Alias("s"), c("v").Implode().Alias("vs"))

	var keysCopied uint64
	at := map[int]uint64{}
	kernel.ConcatColumnDone = func() {
		if len(at) > 0 {
			return // Collect assembling the result, after the aggregates
		}
		runtime.GC()
		runtime.GC()
		keysCopied = liveNow() // the last before an aggregate finishes is the answer's keys
	}
	physical.AggColumnDone = func(i int) {
		runtime.GC()
		runtime.GC()
		at[i] = liveNow()
	}
	defer func() { kernel.ConcatColumnDone, physical.AggColumnDone = nil, nil }()
	got, err := q.Collect(t.Context(), ursus.WithThreads(1), ursus.WithMemoryLimit(0))
	if err != nil {
		t.Fatal(err)
	}
	if got.Height() != n/2 {
		t.Fatalf("%d groups, want %d", got.Height(), n/2)
	}
	runtime.KeepAlive(df)
	if keysCopied == 0 || len(at) != 3 {
		t.Fatalf("read after the keys %v, and after %d aggregates of 3", keysCopied != 0, len(at))
	}
	fell := int64(keysCopied) - int64(at[0])
	if testing.Verbose() {
		t.Logf("after the keys %.1f MB; after the median %.1f, the sum %.1f, the implode %.1f MB",
			float64(keysCopied)/mb, float64(at[0])/mb, float64(at[1])/mb, float64(at[2])/mb)
	}
	if fell < 8*mb {
		t.Errorf("the live heap fell %.1f MB as the median's column was made, want at least 8 MB", float64(fell)/mb)
	}
}
