package ursus_test

// Step 425: a batch of a frame's strings is charged as the text its rows cover, where
// it was charged as all the frame's text.

import (
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestARangeToLenBesideTextUnderALimit: a range to Len() beside a frame holding text
// keeps a batch of the frame in hand while Len() counts. A 64-row batch was charged
// as the whole column's text, 135 KB, and the count refused under 16 KiB; it answers,
// equal to the run without a limit. A sort holding all the batches holds all the text
// and still spills.
func TestARangeToLenBesideTextUnderALimit(t *testing.T) {
	const n = 20_000
	k, s := make([]int64, n), make([]string, n)
	for i := range n {
		k[i] = int64(i * 7919 % 2000)
		s[i] = []string{"pear", "apple pie", "zebra", "fig tree"}[i*31%4] + string(rune('a'+i%26))
	}
	frame := ursus.Frame(ursus.Values("k", k), ursus.Values("s", s))
	limited := func(q *ursus.LazyFrame) (*ursus.DataFrame, ursus.MemoryStats, error) {
		var st ursus.MemoryStats
		df, err := q.Collect(t.Context(), ursus.WithBatchSize(64), ursus.WithMemoryLimit(16<<10),
			ursus.WithSpillDir(t.TempDir()), ursus.WithMemoryStats(&st))
		return df, st, err
	}

	q := frame.WithColumns(ursus.IntRange(ursus.Lit(int64(0)), ursus.Len(), 1).Alias("i"))
	want, err := q.Collect(t.Context(), ursus.WithBatchSize(64))
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := limited(q)
	if err != nil {
		t.Fatalf("a range to Len() beside text, under 16 KiB: %v", err)
	}
	ursustest.AssertFrameEqual(t, got, want)

	sorted := frame.Sort(ursus.Asc(ursus.Col("s")), ursus.Asc(ursus.Col("k")))
	want, err = sorted.Collect(t.Context(), ursus.WithBatchSize(64))
	if err != nil {
		t.Fatal(err)
	}
	got, st, err := limited(sorted)
	if err != nil {
		t.Fatalf("a sort of the text, under 16 KiB: %v", err)
	}
	if st.Spills == 0 {
		t.Errorf("a sort of the text, under 16 KiB: peak %d bytes and no spill", st.Peak)
	}
	ursustest.AssertFrameEqual(t, got, want)
}
