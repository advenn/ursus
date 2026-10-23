package ursus_test

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

// TestV066UnderAMemoryLimit is the 0.6.6 report's check of the scope's second claim
// (step 287): each item spills or streams where its pieces do. One query an item, over
// 20,000 rows in batches of 64, under a 16 KiB limit:
//
//   - **equal:** the per-row functions, the namespaces', the generators of literal
//     arguments, the folds, List().Eval, the integer bits and the numeric odds stream a
//     batch at a time; the reshapes in a Select spill through the frame operators
//     they are written in; the aggregates spill with the group-by. A group-by over
//     the 2,000 keys runs under 64 KiB: its states sit at 16 KiB's edge, past it one
//     run in twenty under one SIMD width or the race detector, and within it in the
//     others. Each answer equals the one without a limit.
//   - **equal to one thread:** the float moments spill too, but a float aggregate's
//     last bits depend on how many threads merged its partial states, as Sum's, Mean's
//     and Var's already did, and a spilled run is the one-thread run's to the bit. They
//     run under 96 KiB: under 64 their states sat at its edge, past it two runs in a
//     hundred under the race detector (steps 309 and 319); under 96 none in a hundred
//     was, and each still spilled at least sixteen times.
//   - **refused:** what needs more than its pieces can spill, each named in
//     feature-gaps.md. If one of these starts answering, move it up.
func TestV066UnderAMemoryLimit(t *testing.T) {
	const n = 20_000
	k, v, vok, f, s, d := make([]int64, n), make([]int64, n), make([]bool, n), make([]float64, n), make([]string, n), make([]int64, n)
	for i := range n {
		k[i], v[i], vok[i] = int64(i*7919%2000), int64(i%977)-400, i%13 != 0
		f[i] = float64(i%61)/4 + float64(i%7) + 0.1
		s[i] = []string{"pear", "apple pie", "zebra", "fig tree"}[i*31%4]
		d[i] = int64(18000 + i%400)
	}
	c, el := ursus.Col, ursus.Element
	frame := ursus.Frame(ursus.Values("k", k), ursus.ValuesNullable("v", v, vok), ursus.Values("f", f),
		ursus.Values("s", s), ursus.Values("d", d)).WithColumns(c("d").Cast(ursus.Int32).Cast(ursus.Date))
	byK := func(aggs ...ursus.Expr) *ursus.LazyFrame {
		return frame.GroupBy(c("k")).Agg(aggs...).Sort(ursus.Asc(c("k")))
	}
	limited := func(t *testing.T, q *ursus.LazyFrame, limit int64) (*ursus.DataFrame, ursus.MemoryStats, error) {
		var st ursus.MemoryStats
		df, err := q.Collect(t.Context(), ursus.WithBatchSize(64), ursus.WithMemoryLimit(limit),
			ursus.WithSpillDir(t.TempDir()), ursus.WithMemoryStats(&st))
		return df, st, err
	}
	small, groups := int64(16<<10), int64(64<<10)
	// A group-by over the 2,000 keys: its states sit at 16 KiB's edge.
	byKey := map[string]bool{"M1, the bits and odds": true, "M3, unique_counts": true, "M1, spearman by k": true}
	for name, q := range map[string]*ursus.LazyFrame{
		"G1, a literal range": frame.WithColumns(ursus.IntRange(ursus.Lit(int64(0)), ursus.Lit(int64(n)), 1).Alias("i")),
		"G2, a date range a row": frame.WithColumns(ursus.DateRanges(c("d"), c("d").Dt().OffsetBy(ursus.Every("3d")),
			ursus.Every("1d")).List().Len().Alias("r")),
		"F1, cum_sum_horizontal": frame.WithColumns(ursus.CumSumHorizontal(c("v"), c("f")).Alias("cs")),
		"F2, arg_sort":           frame.Select(c("f").ArgSort(false, false).Alias("a")),
		"List().Eval of a rank": frame.WithColumns(ursus.ConcatList(c("v"), c("k")).List().
			Eval(el().Rank(ursus.RankMin, false)).Alias("r")),
		"S, contains_any": frame.WithColumns(c("s").Str().ContainsAny([]string{"pie", "tree"}, false).Alias("m")),
		"D, iso_year":     frame.WithColumns(c("d").Dt().IsoYear().Alias("y")),
		"M1, the bits and odds": byK(c("v").BitwiseXor().Alias("bx"), c("f").NanMin().Alias("nm"),
			c("v").HasNulls().Alias("hn"), c("v").ApproxNUnique().Alias("au")),
		// A Spearman's pairs spill since step 339.
		"M1, spearman by k":  byK(ursus.CorrSpearman(c("f"), c("v")).Alias("sp")),
		"M2, bits":           frame.WithColumns(c("v").Xor(c("k")).Alias("x")),
		"M3, value_counts":   frame.Select(c("s").ValueCounts(true, false)),
		"M3, unique_counts":  frame.Select(c("k").UniqueCounts().Alias("u")),
		"M3, search_sorted":  frame.Select(c("f").SearchSorted(5.0, ursus.SideLeft, false).Alias("ss")),
		"M4, append":         frame.Select(c("v").Append(c("k"), true).Alias("a")),
		"M5, round_sig_figs": frame.WithColumns(c("f").RoundSigFigs(2).Alias("r"), c("f").Cot().Alias("ct")),
	} {
		t.Run(name, func(t *testing.T) {
			want, err := q.Collect(t.Context(), ursus.WithBatchSize(64))
			if err != nil {
				t.Fatal(err)
			}
			limit := small
			if byKey[name] {
				limit = groups
			}
			got, st, err := limited(t, q, limit)
			if err != nil {
				t.Fatalf("under %d KiB: %v", limit>>10, err)
			}
			if limit == groups && st.Spills == 0 {
				t.Fatal("answered without spilling, so it proves nothing")
			}
			ursustest.AssertFrameEqual(t, got, want)
		})
	}
	t.Run("M1, the moments, equal to one thread", func(t *testing.T) {
		q := byK(c("f").Skew(true).Alias("sk"), c("f").Kurtosis(true, true).Alias("ku"), c("f").Entropy(2, true).Alias("en"))
		want, err := q.Collect(t.Context(), ursus.WithBatchSize(64), ursus.WithThreads(1))
		if err != nil {
			t.Fatal(err)
		}
		got, st, err := limited(t, q, 96<<10)
		if err != nil {
			t.Fatalf("under 96 KiB: %v", err)
		}
		if st.Spills == 0 {
			t.Fatal("answered without spilling, so it proves nothing")
		}
		ursustest.AssertFrameEqual(t, got, want)
	})
	for name, tc := range map[string]struct {
		q    *ursus.LazyFrame
		want string
	}{
		// A mode holds its values, a group's, unspilled.
		"M1, mode by k": {byK(c("v").Mode(true).First().Alias("mo")), "per-group state"},
		// A run's number needs the partition, as rank's did before it streamed.
		"M4, rle_id": {frame.WithColumns(c("s").RleID().Alias("r")), "one partition"},
		"M4, rle":    {frame.Select(c("s").Rle().Alias("r")), "one partition"},
		// Each implodes the whole column, as implode always has.
		"M4, sample":   {frame.Select(c("v").SampleN(100, false, false, 7).Alias("a")), "memory limit"},
		"tier 2, hist": {frame.Select(c("f").Hist(ursus.HistBinCount(5)).Alias("h")), "memory limit"},
		// Each row reads its whole prefix.
		"tier 2, cumulative_eval": {frame.Select(c("f").CumulativeEval(el().Sum()).Alias("c")), "cumulative_eval"},
		// The frame's side of the stack is held while Len() counts, and the count, with
		// nothing of its own to spill, refuses; a literal end streams, and so does
		// WithRowIndex.
		"G1, a range to Len() beside text": {frame.WithColumns(ursus.IntRange(ursus.Lit(int64(0)), ursus.Len(), 1).Alias("i")),
			"memory limit"},
	} {
		if _, _, err := limited(t, tc.q, small); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want a refusal naming %q", name, err, tc.want)
		}
	}
}
