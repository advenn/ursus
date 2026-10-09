package ursus_test

// Step 143: a subtree a query uses at more than one place runs once. PDS-H q15
// computed its per-supplier revenue twice, once for the join and once under the max,
// because Resolve copied the one node into both places.

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/plan"
)

// noSharing is the default flags with ShareSubplans off: the query as it ran
// before step 143.
func noSharing() ursus.CollectOption {
	f := plan.DefaultFlags()
	f.ShareSubplans = false
	return ursus.WithOptFlags(f)
}

// TestASharedSubtreeRunsOnce: q15's shape over a source that counts what it reads.
// The group-by is one node with two parents; shared, its input is read once, and
// unshared, twice, with the same answer.
func TestASharedSubtreeRunsOnce(t *testing.T) {
	src := separateBatches(t, 16, 1000)
	q := func() *ursus.LazyFrame {
		byKey := ursus.Scan(src).
			WithColumns(ursus.Col("v").Mod(97).Alias("k")).
			GroupBy(ursus.Col("k")).
			Agg(ursus.Col("v").Sum().Alias("total"))
		best := byKey.GroupBy().Agg(ursus.Col("total").Max().Alias("best"))
		return byKey.Join(best, ursus.JoinHow(ursus.JoinCross)).
			Filter(ursus.Col("total").Eq(ursus.Col("best"))).
			Select(ursus.Col("k"), ursus.Col("total"))
	}

	src.read.Store(0)
	shared, err := q().Collect(t.Context(), ursus.WithThreads(1))
	if err != nil {
		t.Fatal(err)
	}
	if n := src.read.Load(); n != 16 {
		t.Errorf("shared, the source was read %d batches; it has 16", n)
	}

	src.read.Store(0)
	twice, err := q().Collect(t.Context(), ursus.WithThreads(1), noSharing())
	if err != nil {
		t.Fatal(err)
	}
	if n := src.read.Load(); n != 32 {
		t.Errorf("unshared, the source was read %d batches; the subtree runs twice, 32", n)
	}
	if !slices.Equal(readAll[int64](shared, "k"), readAll[int64](twice, "k")) ||
		!slices.Equal(readAll[int64](shared, "total"), readAll[int64](twice, "total")) {
		t.Errorf("sharing changed the answer: %v %v against %v %v",
			readAll[int64](shared, "k"), readAll[int64](shared, "total"),
			readAll[int64](twice, "k"), readAll[int64](twice, "total"))
	}
}

// TestASharedResultSpillsPastTheBudget: a shared sort of 40,000 rows, 320 KB, under
// a 64 KiB limit. It cannot be held, so it is replayed from a spill file, in order,
// at both sites.
//
// From 40 batches of their own, not one frame: the ledger counts an allocation
// whole, so a sort retaining the first view of a single 320 KB buffer counted all
// of it at once, and the peak said nothing about the cache.
func TestASharedResultSpillsPastTheBudget(t *testing.T) {
	sorted := ursus.Scan(separateBatches(t, 40, 1000)).
		Select(ursus.Col("v").Mul(7919).Mod(40_000).Alias("v")).
		Sort(ursus.Asc(ursus.Col("v")))
	q := ursus.Concat([]*ursus.LazyFrame{sorted, sorted.Filter(ursus.Col("v").Lt(10))})

	var st ursus.MemoryStats
	dir := t.TempDir()
	// Batches of 512 rows: an external sort's merge holds a batch per run, and at the
	// default 8,192 rows five runs are 320 KB on their own.
	got, err := q.Collect(t.Context(), ursus.WithThreads(1), ursus.WithMemoryLimit(64<<10),
		ursus.WithBatchSize(512), ursus.WithSpillDir(dir), ursus.WithMemoryStats(&st))
	if err != nil {
		t.Fatal(err)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Errorf("%d spill entries left behind after the query", len(left))
	}
	if st.Spills == 0 {
		t.Error("nothing spilled under a 64 KiB limit")
	}
	// The sort spills on its own too, so the spill count cannot say the cache did.
	// The peak can: held, the shared rows alone are 320 KB.
	t.Logf("peak %d bytes, %d spill files", st.Peak, st.Spills)
	if st.Peak > 160<<10 {
		t.Errorf("the query held %d bytes at its peak under a 64 KiB limit; the shared "+
			"result was held, not spilled", st.Peak)
	}
	vs := readAll[int64](got, "v")
	if len(vs) != 40_010 {
		t.Fatalf("%d rows, want 40,010", len(vs))
	}
	if !slices.IsSorted(vs[:40_000]) || vs[0] != 0 || vs[39_999] != 39_999 {
		t.Error("the first site's replay is not the sorted rows")
	}
	if !slices.Equal(vs[40_000:], []int64{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}) {
		t.Errorf("the second site's replay reads %v", vs[40_000:])
	}
}

// TestASharedSubtreeGivesEverySiteItsColumns: a shared join, one site reading `a`,
// the other `c`, as PDS-H q2 and q11 read different columns of their shared joins.
// Narrowed by each site's own needs, the two copies would differ and the one that
// runs would lack the other's column; each is given the union of what both read.
//
// Before the union, the cache held every column the join produced: q2 and q11 ran
// 70 and 77% slower shared than computed twice, with four times the memory.
func TestASharedSubtreeGivesEverySiteItsColumns(t *testing.T) {
	left := ursus.Frame(
		ursus.Values("k", []int64{1, 2, 3, 4}),
		ursus.Values("a", []int64{10, 20, 30, 40}),
		ursus.Values("b", []string{"w", "x", "y", "z"}),
		ursus.Values("c", []int64{100, 200, 300, 400}),
	)
	right := ursus.Frame(
		ursus.Values("k", []int64{2, 3, 4, 5}),
		ursus.Values("d", []int64{7, 8, 9, 10}),
	)
	joined := left.Join(right, ursus.JoinOn(ursus.Col("k"))).Sort(ursus.Asc(ursus.Col("k")))
	q := ursus.Concat([]*ursus.LazyFrame{
		joined.Select(ursus.Col("k"), ursus.Col("a").Alias("x")),
		joined.Select(ursus.Col("k"), ursus.Col("c").Alias("x")),
	})

	shared, err := q.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	twice, err := q.Collect(t.Context(), noSharing())
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{20, 30, 40, 200, 300, 400}
	if got := readAll[int64](shared, "x"); !slices.Equal(got, want) {
		t.Errorf("shared, x reads %v, want %v", got, want)
	}
	if got := readAll[int64](twice, "x"); !slices.Equal(got, want) {
		t.Errorf("unshared, x reads %v, want %v", got, want)
	}

	p, err := q.Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(p, "CACHE #1") != 2 || strings.Count(p, "projection: [k, a, c]") != 2 {
		t.Errorf("both sites should read the one cached join, with a and c:\n%s", p)
	}
}
