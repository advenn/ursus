package ursus_test

// Step 385: a Head(k) after a Sort keeps a heap of k for a small k and sorts every row
// for a large one, k log k ln(n/k) past a third of n. Either way its rows are the
// sort's first k, in its order: keys with ties, NaN, −0 and nulls, ascending and
// descending, nulls first and last, text; k from one row to past n; on one thread
// and four, in batches of 997, and under a memory limit that spills.

import (
	"fmt"
	"math"
	"slices"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

func TestALargeTopKIsTheSortsFirstRows(t *testing.T) {
	const n = 60_000
	f, fOK := make([]float64, n), make([]bool, n)
	g := make([]int64, n)
	s := make([]string, n)
	id := make([]int64, n)
	for i := range n {
		h := uint64(i) * 0x9E3779B97F4A7C15 >> 11
		f[i], fOK[i] = float64(h%5000)/8, h%37 != 0
		switch h % 101 {
		case 0:
			f[i] = math.NaN()
		case 1:
			f[i] = math.Copysign(0, -1)
		}
		g[i] = int64(h % 300)
		s[i] = fmt.Sprintf("s%04d", h%2000)
		id[i] = int64(i)
	}
	frame := ursus.Frame(ursus.ValuesNullable("f", f, fOK), ursus.Values("g", g), ursus.Values("s", s),
		ursus.Values("id", id))
	c := ursus.Col
	orders := []struct {
		name string
		keys []ursus.SortKey
	}{
		{"a float ascending", []ursus.SortKey{ursus.Asc(c("f"))}},
		{"a float descending, nulls last", []ursus.SortKey{ursus.Desc(c("f")).NullsLast()}},
		{"an integer and text", []ursus.SortKey{ursus.Asc(c("g")), ursus.Desc(c("s"))}},
		{"text, then a float nulls last", []ursus.SortKey{ursus.Asc(c("s")), ursus.Asc(c("f")).NullsLast()}},
	}
	for _, o := range orders {
		all, err := frame.Sort(o.keys...).Collect(t.Context(), ursus.WithThreads(1))
		if err != nil {
			t.Fatal(err)
		}
		// k up to 300 keeps a heap; from 1,000 the rule sorts every row.
		for _, k := range []int{1, 30, 300, 1000, 5000, 30000, n - 1, n, n + 9} {
			want, err := all.Head(t.Context(), k)
			if err != nil {
				t.Fatal(err)
			}
			for _, threads := range []int{1, 4} {
				got, err := frame.Sort(o.keys...).Head(k).
					Collect(t.Context(), ursus.WithThreads(threads), ursus.WithBatchSize(997))
				if err != nil {
					t.Fatal(err)
				}
				sameIDs(t, fmt.Sprintf("%s, k %d, %d threads", o.name, k, threads), got, want)
			}
		}
		// Past a memory limit a large k's best rows spill as runs, as a sort's do.
		var stats ursus.MemoryStats
		want, err := all.Head(t.Context(), 30000)
		if err != nil {
			t.Fatal(err)
		}
		got, err := frame.Sort(o.keys...).Head(30000).Collect(t.Context(), ursus.WithThreads(1),
			ursus.WithBatchSize(997), ursus.WithMemoryLimit(256<<10), ursus.WithSpillDir(t.TempDir()),
			ursus.WithMemoryStats(&stats))
		if err != nil {
			t.Fatalf("%s under 256 KiB: %v", o.name, err)
		}
		if stats.Spills == 0 {
			t.Errorf("%s: nothing spilled under 256 KiB", o.name)
		}
		sameIDs(t, o.name+", spilled", got, want)
	}
	// The rows themselves, once, beyond their ids.
	want, err := frame.Sort(orders[0].keys...).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if want, err = want.Head(t.Context(), 5000); err != nil {
		t.Fatal(err)
	}
	got, err := frame.Sort(orders[0].keys...).Head(5000).Collect(t.Context(), ursus.WithThreads(4))
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, got, want)
}

// sameIDs checks got holds want's rows in want's order, by their unique ids.
func sameIDs(t *testing.T, what string, got, want *ursus.DataFrame) {
	t.Helper()
	g, err := got.Column[int64]("id")
	if err != nil {
		t.Fatal(err)
	}
	w, err := want.Column[int64]("id")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(g.Values(), w.Values()) {
		t.Fatalf("%s: %d rows, want %d, or in another order", what, g.Len(), w.Len())
	}
}
