package ursus_test

// Batch-size and thread invariance for the five operators that had neither.
//
// The standing rule in this project is that an operator's answer must not depend on
// how the query happened to be chunked or scheduled — batching is an execution
// detail, not something the user asked for. Roughly twenty operators have a
// `{1, 2, 3, 7, 8192} × threads` sweep pinning that. These five had NOTHING:
// asof_test.go, tempgroup_test.go and topk_test.go contained zero WithBatchSize and
// zero WithThreads; MergeSorted has no dedicated file; and Distinct was thread-swept
// but never batch-swept.
//
// JoinAsOf is the one that matters most. It is an ordered streaming join carrying a
// cursor across batches, which is the canonical shape for a per-batch state leak —
// exactly the defect class that batching sweeps exist to catch.

import (
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// assertInvariant collects q at a reference configuration and then across the
// matrix, requiring identical frames every time.
//
// The anti-vacuity floor is not decorative: an operator that returns nothing agrees
// with itself at every batch size, so a sweep over an empty result asserts nothing
// at all. minRows is what the caller believes the query produces.
func assertInvariant(t *testing.T, minRows int, q func() *ursus.LazyFrame) {
	t.Helper()

	want, err := q().Collect(t.Context(), ursus.WithBatchSize(8192), ursus.WithThreads(1))
	if err != nil {
		t.Fatal(err)
	}
	if want.Height() < minRows {
		t.Fatalf("the query produced %d rows, want at least %d — a sweep over an "+
			"empty result proves nothing", want.Height(), minRows)
	}

	for _, bs := range []int{1, 2, 3, 7, 8192} {
		for _, th := range []int{1, 2, 4} {
			got, err := q().Collect(t.Context(),
				ursus.WithBatchSize(bs), ursus.WithThreads(th), ursus.WithVerify())
			if err != nil {
				t.Fatalf("batch %d threads %d: %v", bs, th, err)
			}
			ursustest.AssertFrameEqual(t, got, want, ursustest.CheckNullability())
		}
	}
}

// TestAsOfIsBatchSizeInvariant. asOfBuildSink buffers the right side and the left
// streams past it with a cursor; a cursor reset or carried per batch is invisible to
// a single-batch test and wrong everywhere else.
func TestAsOfIsBatchSizeInvariant(t *testing.T) {
	lk := []int64{0, 5, 10, 15, 20, 25, 100}
	rk := []int64{3, 5, 9, 16, 16, 21}

	for _, c := range []struct {
		name string
		opts []ursus.AsOfOption
	}{
		{"backward", []ursus.AsOfOption{ursus.AsOfOn(ursus.Col("k"))}},
		{"forward", []ursus.AsOfOption{ursus.AsOfOn(ursus.Col("k")),
			ursus.AsOfStrategyOpt(ursus.AsOfForward)}},
		{"nearest", []ursus.AsOfOption{ursus.AsOfOn(ursus.Col("k")),
			ursus.AsOfStrategyOpt(ursus.AsOfNearest)}},
	} {
		t.Run(c.name, func(t *testing.T) {
			assertInvariant(t, len(lk), func() *ursus.LazyFrame {
				return tickFrame(t, "lv", lk, nil).
					JoinAsOf(tickFrame(t, "rv", rk, nil), c.opts...)
			})
		})
	}
}

// TestMergeSortedIsBatchSizeInvariant. A streaming k-way merge whose entire job is
// cross-batch ordering, and it had no sweep at all.
func TestMergeSortedIsBatchSizeInvariant(t *testing.T) {
	a := func() *ursus.LazyFrame {
		return ursus.Frame(
			ursus.Values("k", []int64{1, 3, 5, 7, 9, 11}),
			ursus.Values("v", []int64{10, 30, 50, 70, 90, 110}))
	}
	b := func() *ursus.LazyFrame {
		return ursus.Frame(
			ursus.Values("k", []int64{2, 4, 6, 8, 10, 12}),
			ursus.Values("v", []int64{20, 40, 60, 80, 100, 120}))
	}
	assertInvariant(t, 12, func() *ursus.LazyFrame { return a().MergeSorted(b(), "k") })
}

// TestTopKIsBatchSizeInvariant. A top-k sink holding a PER-BATCH heap instead of a
// global one is the classic defect, and it is invisible at one batch.
func TestTopKIsBatchSizeInvariant(t *testing.T) {
	src := func() *ursus.LazyFrame {
		k := make([]int64, 40)
		v := make([]string, 40)
		for i := range k {
			k[i] = int64((i * 17) % 40)
			v[i] = string(rune('a' + i%7))
		}
		return ursus.Frame(ursus.Values("k", k), ursus.Values("v", v))
	}
	t.Run("top", func(t *testing.T) {
		assertInvariant(t, 5, func() *ursus.LazyFrame {
			return src().TopK(5, ursus.Desc(ursus.Col("k")))
		})
	})
	t.Run("bottom", func(t *testing.T) {
		assertInvariant(t, 5, func() *ursus.LazyFrame {
			return src().BottomK(5, ursus.Asc(ursus.Col("k")))
		})
	})
}

// TestDistinctIsBatchSizeInvariant. Distinct was thread-swept in parallel_test.go
// but pinned at one batch size, and "first row per key" is precisely a CROSS-BATCH
// claim: a `seen` map rebuilt per batch keeps the first row of every batch.
func TestDistinctIsBatchSizeInvariant(t *testing.T) {
	src := func() *ursus.LazyFrame {
		k := make([]int64, 60)
		v := make([]int64, 60)
		for i := range k {
			k[i] = int64(i % 9) // every key recurs across batch boundaries
			v[i] = int64(i)
		}
		return ursus.Frame(ursus.Values("k", k), ursus.Values("v", v))
	}
	t.Run("subset", func(t *testing.T) {
		assertInvariant(t, 9, func() *ursus.LazyFrame { return src().Unique("k") })
	})
	t.Run("whole row", func(t *testing.T) {
		assertInvariant(t, 60, func() *ursus.LazyFrame { return src().Unique() })
	})
}

// TestTemporalGroupIsBatchSizeInvariant covers both of TemporalGroup's forms.
// Windows straddle batch boundaries by construction, so a grid rebuilt per batch
// produces different windows at every chunking.
func TestTemporalGroupIsBatchSizeInvariant(t *testing.T) {
	stamps := []string{
		"2024-01-01T00:10:00", "2024-01-01T00:50:00", "2024-01-01T01:05:00",
		"2024-01-01T01:55:00", "2024-01-01T02:30:00", "2024-01-01T03:10:00",
		"2024-01-01T03:45:00", "2024-01-01T04:20:00",
	}
	t.Run("group_by_dynamic", func(t *testing.T) {
		assertInvariant(t, 4, func() *ursus.LazyFrame {
			return hourly(t, stamps...).
				GroupByDynamic(ursus.Col("ts"), ursus.DynamicOptions{
					Every: ursus.Every("1h"),
				}).Agg(ursus.Len().Alias("n"))
		})
	})
	t.Run("rolling", func(t *testing.T) {
		assertInvariant(t, 4, func() *ursus.LazyFrame {
			return hourly(t, stamps...).
				Rolling(ursus.Col("ts"), ursus.RollingOptions{
					Period: ursus.Every("1h"),
				}).Agg(ursus.Len().Alias("n"))
		})
	})
}
