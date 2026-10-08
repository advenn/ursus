package kernel_test

import (
	"runtime"
	"runtime/metrics"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/kernel"
)

func liveHeap() int64 {
	s := []metrics.Sample{{Name: "/gc/heap/live:bytes"}}
	metrics.Read(s)
	return int64(s[0].Value.Uint64())
}

// TestAccumulatorsGrowByDoubling: every accumulator, grown to a million groups ten
// thousand at a time, as a group-by's batches grow it.
//
// Reserve appended one group at a time, so a large accumulator grew by append's
// quarter steps and allocated about five times what it ended up holding. In h2o gb10
// the sum and the count were 12% of everything the query allocated (step 132). They
// double now: about twice.
//
// And NBytes counts what is held, capacity and not length. Doubling can leave half
// of an array unused, and a budget counting only the used half would miss it.
//
// Not parallel, so no other test allocates while it reads.
func TestAccumulatorsGrowByDoubling(t *testing.T) {
	cases := []struct {
		op     expr.AggOp
		dt     dtype.DataType
		params expr.AggParams
	}{
		{op: expr.AggSum, dt: dtype.Float64},
		{op: expr.AggSum, dt: dtype.Int64},
		{op: expr.AggCount, dt: dtype.Float64},
		{op: expr.AggLen, dt: dtype.Float64},
		{op: expr.AggNUnique, dt: dtype.Int64},
		{op: expr.AggMean, dt: dtype.Float64},
		{op: expr.AggMean, dt: dtype.Int64},
		{op: expr.AggMin, dt: dtype.Float64},
		{op: expr.AggMax, dt: dtype.String},
		{op: expr.AggMax, dt: dtype.Bool},
		{op: expr.AggFirst, dt: dtype.Float64},
		{op: expr.AggVar, dt: dtype.Float64},
		{op: expr.AggProduct, dt: dtype.Float64},
		{op: expr.AggArgMax, dt: dtype.Float64},
		{op: expr.AggMedian, dt: dtype.Float64},
		{op: expr.AggImplode, dt: dtype.Int64},
		{op: expr.AggTopK, dt: dtype.Int64, params: expr.AggParams{K: 3}},
	}
	for _, c := range cases {
		name := c.op.String() + "(" + c.dt.String() + ")"
		bind, err := expr.ResolveAggBinding(c.op, c.dt)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		runtime.GC()
		base, before := liveHeap(), allocatedBytes()
		a, err := kernel.NewAccumulator(c.op, c.dt, bind, c.params)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for n := 10_000; n <= 1_000_000; n += 10_000 {
			a.Reserve(n)
		}
		allocated := int64(allocatedBytes() - before)
		runtime.GC()
		held, counted := liveHeap()-base, a.NBytes()
		runtime.KeepAlive(a)
		t.Logf("%-20s allocated %6.1f MB, holds %6.1f MB, counts %6.1f MB", name,
			float64(allocated)/(1<<20), float64(held)/(1<<20), float64(counted)/(1<<20))
		if allocated > counted*5/2 {
			t.Errorf("%s: growing to a million groups allocated %d bytes to hold %d", name, allocated, counted)
		}
		if counted < held*9/10 {
			t.Errorf("%s: NBytes is %d, and it holds %d", name, counted, held)
		}
	}
}
