package physical

import (
	"runtime"
	"runtime/metrics"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/plan"
)

// TestAJoinBuildAllocatesAboutTwiceWhatItHolds: a million distinct keys, in batches
// of 8,192, into an inner join's build side, against the state it counts: its key
// table, and rowKey and counts, one entry a row and a key.
//
// rowKey and counts grew by append, which past 256 elements adds a quarter at a
// time and allocates about five times the final size: about 0.8 GB of h2o j5's
// profile (step 137). They now double, as the key table's own arrays do (step 133),
// and everything the build grows allocates about twice what it ends up holding.
//
// The batches are built first, and the join keeps them: only what the build grows is
// measured. Not parallel, so no other test allocates while it reads.
func TestAJoinBuildAllocatesAboutTwiceWhatItHolds(t *testing.T) {
	const n, batch = 1 << 20, 8192
	s := joinFixture(t, plan.JoinInner)
	batches := make([]*data.Batch, 0, n/batch)
	for start := 0; start < n; start += batch {
		ks, vs := make([]int64, batch), make([]int64, batch)
		for i := range ks {
			ks[i], vs[i] = int64(start+i)*7919%(1<<40), int64(i)
		}
		b, err := data.NewBatch(s.right, []*data.Column{
			data.NewFixed("k", dtype.Int64, ks, bitmap.AllSet(batch)),
			data.NewFixed("rv", dtype.Int64, vs, bitmap.AllSet(batch)),
		})
		if err != nil {
			t.Fatal(err)
		}
		batches = append(batches, b)
	}
	read := func() uint64 {
		m := []metrics.Sample{{Name: "/gc/heap/allocs:bytes"}}
		metrics.Read(m)
		return m[0].Value.Uint64()
	}
	runtime.GC()
	before := read()
	for _, b := range batches {
		if err := s.Consume(t.Context(), b); err != nil {
			t.Fatal(err)
		}
	}
	allocated := int64(read() - before)
	held := s.stateBytes
	t.Logf("allocated %.1f MB to hold %.1f MB: %.2f", float64(allocated)/(1<<20),
		float64(held)/(1<<20), float64(allocated)/float64(held))
	if s.ids.Len() != n || len(s.rowKey) != n {
		t.Fatalf("%d keys and %d rows, want %d of each", s.ids.Len(), len(s.rowKey), n)
	}
	if allocated > held*2 {
		t.Errorf("building on %d keys allocated %d bytes to hold %d", n, allocated, held)
	}
}
