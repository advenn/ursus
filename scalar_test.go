package ursus_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestAnAggregateInARowContextIsAScalar: since step 250 an aggregate in Select,
// WithColumns and Filter is one value for the frame, broadcast to every row, and a
// Select of aggregates and literals alone is one row, as in Polars. Each answer here
// is Polars 1.44.1's on the same frame, written as CSV by Polars itself. A window in
// a Filter is computed beneath it, SQL's QUALIFY.
func TestAnAggregateInARowContextIsAScalar(t *testing.T) {
	c := ursus.Col
	lf := ursus.Frame(ursus.Values("g", []int64{1, 1, 2, 2, 2}), ursus.Values("x", []float64{1, 4, 2, 8, 5}),
		ursus.Values("y", []float64{3, 1, 4, 1, 5}))
	cases := []struct {
		q      *ursus.LazyFrame
		polars string
	}{
		{lf.Select(c("x").Sum(), c("y")), "x,y\n20.0,3.0\n20.0,1.0\n20.0,4.0\n20.0,1.0\n20.0,5.0\n"},
		{lf.Select(c("x").Sum(), c("y").Mean().Alias("m")), "x,m\n20.0,2.8\n"},
		{lf.Select(c("x").Sum(), ursus.Lit(int64(7)).Alias("l")), "x,l\n20.0,7\n"},
		{lf.WithColumns(c("x").Sub(c("x").Mean()).Alias("cx")),
			"g,x,y,cx\n1,1.0,3.0,-3.0\n1,4.0,1.0,0.0\n2,2.0,4.0,-2.0\n2,8.0,1.0,4.0\n2,5.0,5.0,1.0\n"},
		{lf.Filter(c("x").Gt(c("x").Mean())), "g,x,y\n2,8.0,1.0\n2,5.0,5.0\n"},
		{lf.Filter(c("x").Sum().Over(c("g")).Gt(10)), "g,x,y\n2,2.0,4.0\n2,8.0,1.0\n2,5.0,5.0\n"},
		{lf.Select(c("x").Sum().Add(c("y").Max()).Mul(2).Alias("s")), "s\n50.0\n"},
		{lf.Select(c("x").Mul(c("x").Sum()).Alias("p")), "p\n20.0\n80.0\n40.0\n160.0\n100.0\n"},
		{lf.Select(ursus.All().Sum().Suffix("_s")), "g_s,x_s,y_s\n8,20.0,14.0\n"},
		{lf.Select(c("g"), c("x", "y").Mean()), "g,x,y\n1,4.0,2.8\n1,4.0,2.8\n2,4.0,2.8\n2,4.0,2.8\n2,4.0,2.8\n"},
		{lf.WithColumns(c("x").Div(c("x").Sum()).Alias("share")),
			"g,x,y,share\n1,1.0,3.0,0.05\n1,4.0,1.0,0.2\n2,2.0,4.0,0.1\n2,8.0,1.0,0.4\n2,5.0,5.0,0.25\n"},
		{lf.Select(c("x").Max().Sub(c("x").Min()).Alias("range"), c("y").Std(1).Alias("sd")),
			"range,sd\n7.0,1.7888543819998317\n"},
	}
	for i, tc := range cases {
		var got bytes.Buffer
		if err := tc.q.WriteCSV(t.Context(), &got); err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if got.String() != tc.polars {
			t.Errorf("case %d:\n got %q\nwant %q (Polars)", i, got.String(), tc.polars)
		}
	}
}

// TestABroadcastAggregateStreamsPastItsLimit: the broadcast is the aggregate over a
// window with no partition, so it streams past a memory limit as that window does
// (steps 197, 242): centring a column, a median broadcast, and a filter above the
// mean, under a tenth of the input, give the in-memory answer, spill, and leave the
// spill directory empty.
func TestABroadcastAggregateStreamsPastItsLimit(t *testing.T) {
	frame := sortedFrame(40, 500)
	in, err := frame.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	limit := in.Batch().NBytes() / 10
	c := ursus.Col
	for name, q := range map[string]*ursus.LazyFrame{
		"centred and a median": frame.WithColumns(c("x").Sub(c("x").Mean()).Alias("cx"), c("v").Median().Alias("med")),
		"above the mean":       frame.Filter(c("x").Gt(c("x").Mean())),
	} {
		want, err := q.Collect(t.Context(), ursus.WithBatchSize(500))
		if err != nil {
			t.Fatal(err)
		}
		var stats ursus.MemoryStats
		dir := t.TempDir()
		got, err := q.Collect(t.Context(), ursus.WithBatchSize(500), ursus.WithMemoryLimit(limit),
			ursus.WithSpillDir(dir), ursus.WithMemoryStats(&stats))
		if err != nil {
			t.Fatalf("%s under %d bytes: %v", name, limit, err)
		}
		if stats.Spills == 0 {
			t.Errorf("%s: nothing spilled", name)
		}
		ursustest.AssertFrameEqual(t, got, want)
		if left, _ := os.ReadDir(dir); len(left) > 0 {
			t.Errorf("%s: %d entries left in the spill directory", name, len(left))
		}
	}
}
