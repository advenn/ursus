package ursus_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/advenn/ursus"
)

// TestASpilledOuterJoinEmitsItsLastFlush: a full or right join emits the build rows
// no probe row matched, in a flush after the probe. Where the join had also spilled,
// the flush's last call dropped the table, to give the replay the whole budget, and
// then emitted its last rows from it: a nil build side, a panic.
//
// It was there from the first commit, and reached only when a level both kept build
// rows resident and spilled others, at limits that depend on what everything in the
// budget weighs: with 500 build keys, between 2.5 and 3.2 KiB. The chunked key arena
// moved that to where TestMemoryLimitIsNotASemanticKnob runs (step 133).
//
// So a sweep: two build sides, 20 keys matched, every limit from 2 KiB to 512 KiB in
// steps of a quarter. A limit too small for one key's rows may refuse, as documented;
// none may panic, or answer differently.
func TestASpilledOuterJoinEmitsItsLastFlush(t *testing.T) {
	probe := memFrame(t, 6000, 256, 20)
	for _, keys := range []int{500, 3000} {
		build := memFrame(t, 6000, 256, keys)
		for _, kind := range []ursus.JoinKind{ursus.JoinFull, ursus.JoinRight} {
			// Sorted on every column, with no limit, the answer is one frame whatever
			// order a limit gave it, and its three Int64 columns are compared value by
			// value: ursustest.AssertFrameEqual takes 170 ms over these 18,000 rows,
			// and the sweep compares about seventy times.
			sorted := func(df *ursus.DataFrame) *ursus.DataFrame {
				t.Helper()
				out, err := df.Lazy().Sort(ursus.Asc(ursus.Col("k2")), ursus.Asc(ursus.Col("k")),
					ursus.Asc(ursus.Col("seq"))).Collect(t.Context(), ursus.WithMemoryLimit(0))
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			all, err := joinShape(probe, build, kind).Collect(t.Context(), ursus.WithBatchSize(256))
			if err != nil {
				t.Fatal(err)
			}
			want := sorted(all)
			var spilled, answered int64
			for limit := int64(2 << 10); limit <= 512<<10; limit = limit * 5 / 4 {
				var st ursus.MemoryStats
				got, err := joinShape(probe, build, kind).Collect(t.Context(), ursus.WithBatchSize(256),
					ursus.WithMemoryLimit(limit), ursus.WithSpillDir(t.TempDir()), ursus.WithMemoryStats(&st))
				if errors.Is(err, ursus.ErrResource) {
					continue
				}
				if err != nil {
					t.Fatalf("%v join over %d build keys, limit %d: %v", kind, keys, limit, err)
				}
				answered++
				spilled += st.Spills
				if diff := sameInts(sorted(got), want, "k2", "k", "seq"); diff != "" {
					t.Fatalf("%v join over %d build keys, limit %d: %s", kind, keys, limit, diff)
				}
			}
			if answered < 20 || spilled == 0 {
				t.Errorf("%v join over %d build keys: %d limits answered, %d spill files; the sweep "+
					"is not reaching the spilled flush", kind, keys, answered, spilled)
			}
		}
	}
}

// sameInts compares the named Int64 columns of two frames, nulls included, and says
// where they first differ.
func sameInts(got, want *ursus.DataFrame, names ...string) string {
	if got.Height() != want.Height() {
		return fmt.Sprintf("%d rows, want %d", got.Height(), want.Height())
	}
	for _, name := range names {
		g, err := got.Column[int64](name)
		if err != nil {
			return err.Error()
		}
		w, err := want.Column[int64](name)
		if err != nil {
			return err.Error()
		}
		for i := range g.Len() {
			gv, gok := g.Get(i)
			wv, wok := w.Get(i)
			if gok != wok || gv != wv {
				return fmt.Sprintf("row %d of %s is %d (valid %v), want %d (valid %v)", i, name, gv, gok, wv, wok)
			}
		}
	}
	return ""
}
