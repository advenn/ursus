package ursus_test

// Step 426: a join's deepest split names what holds the memory over the query's limit,
// where it blamed one key's rows for what the query's other operators held.

import (
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestAlignedFramesUnderASmallLimitBlameNoKey: step 307's three frames aligned, 20,000
// keys each held once but a few, under 64 KiB, were refused "a single join key has
// more build rows than the limit"; the memory was the other join's. Refused, it names
// the other operators; answered, it is the answer without a limit.
func TestAlignedFramesUnderASmallLimitBlameNoKey(t *testing.T) {
	const n = 20_000
	frameOf := func(name string, step, dup int) *ursus.LazyFrame {
		var ids, vals []int64
		for i := 0; i < n; i += step {
			ids, vals = append(ids, int64((i*7919)%n)), append(vals, int64(i))
			if dup > 0 && i%dup == 0 {
				ids, vals = append(ids, int64((i*7919)%n)), append(vals, int64(-i))
			}
		}
		return ursus.Frame(ursus.Values("id", ids), ursus.Values(name, vals))
	}
	q := ursus.Concat([]*ursus.LazyFrame{frameOf("x", 1, 0), frameOf("y", 2, 5), frameOf("z", 3, 0)},
		ursus.WithConcatMode(ursus.ConcatAlign))
	got, err := q.Collect(t.Context(), ursus.WithBatchSize(64), ursus.WithMemoryLimit(64<<10),
		ursus.WithSpillDir(t.TempDir()))
	if err != nil {
		if !errors.Is(err, ursus.ErrResource) || strings.Contains(err.Error(), "single join key") ||
			!strings.Contains(err.Error(), "other operators") {
			t.Fatalf("under 64 KiB: %v, want a refusal naming the other operators", err)
		}
		t.Log(err)
		return
	}
	want, err := q.Collect(t.Context(), ursus.WithBatchSize(64))
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, got, want)
}
