package ursus_test

// Step 183, scope item R1's second half: a backward as-of join streams both sides,
// holding a row a by-key, where it buffered the right side.

import (
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
)

// TestBackwardAsOfStreamingAgreesWithAScan: random sorted sides with ties, by-keys
// with nulls, exact matches on and off, and a tolerance, at batch sizes small enough
// to cross batches and to compact the by-keys' rows many times, against each left
// row's match found by scanning the right side.
func TestBackwardAsOfStreamingAgreesWithAScan(t *testing.T) {
	rng := rand.New(rand.NewPCG(183, 1))
	side := func(n int, maxStep int64, byNull int) (k []int64, by []string, byOK []bool, tag []string) {
		k, by, byOK, tag = make([]int64, n), make([]string, n), make([]bool, n), make([]string, n)
		v := int64(0)
		for i := range n {
			v += rng.Int64N(maxStep)
			k[i], by[i], byOK[i] = v, string(rune('a'+rng.IntN(5))), rng.IntN(byNull) != 0
			tag[i] = by[i] + "#" + string(rune('A'+i%26)) + string(rune('A'+i/26%26))
		}
		return
	}
	lk, lby, lok, _ := side(300, 7, 20)
	rk, rby, rok, rtag := side(900, 3, 15)
	left := ursus.Frame(ursus.Values("t", lk), ursus.ValuesNullable("g", lby, lok))
	right := ursus.Frame(ursus.Values("t", rk), ursus.ValuesNullable("g", rby, rok), ursus.Values("tag", rtag))

	for _, tc := range []struct {
		name  string
		exact bool
		tol   int64 // in ticks, 0 for none
		byKey bool
	}{
		{"by, exact", true, 0, true},
		{"by, strictly before", false, 0, true},
		{"by, a tolerance of 4", true, 4, true},
		{"no by", true, 0, false},
		{"no by, strictly before, a tolerance of 2", false, 2, false},
	} {
		// The reference: the last right row at or before each left key, in its by-bucket.
		want := make([]string, len(lk))
		for i := range lk {
			want[i] = "∅"
			if tc.byKey && !lok[i] {
				continue
			}
			for j := range rk {
				if rk[j] > lk[i] || (!tc.exact && rk[j] == lk[i]) {
					break
				}
				if tc.byKey && (!rok[j] || rby[j] != lby[i]) {
					continue
				}
				if tc.tol > 0 && lk[i]-rk[j] > tc.tol {
					want[i] = "∅"
					continue
				}
				want[i] = rtag[j]
			}
		}
		opts := []ursus.AsOfOption{ursus.AsOfOn(ursus.Col("t")), ursus.AsOfAllowExactMatches(tc.exact)}
		if tc.byKey {
			opts = append(opts, ursus.AsOfBy(ursus.Col("g")))
		}
		if tc.tol > 0 {
			// The key is an Int64; a tolerance needs an instant, so this case runs on
			// the key read as nanoseconds.
			l := left.Select(ursus.Col("t").Cast(ursus.Duration(ursus.Nano)).Alias("t"), ursus.Col("g"))
			r := right.Select(ursus.Col("t").Cast(ursus.Duration(ursus.Nano)).Alias("t"), ursus.Col("g"), ursus.Col("tag"))
			opts = append(opts, ursus.AsOfTolerance(ursus.FromDuration(time.Duration(tc.tol))))
			check(t, tc.name, l.JoinAsOf(r, opts...), want)
			continue
		}
		check(t, tc.name, left.JoinAsOf(right, opts...), want)
	}
}

func check(t *testing.T, name string, lf *ursus.LazyFrame, want []string) {
	t.Helper()
	for _, bs := range []int{1, 4, 64, 4096} {
		df, err := lf.Collect(t.Context(), ursus.WithBatchSize(bs))
		if err != nil {
			t.Fatalf("%s, batch %d: %v", name, bs, err)
		}
		tags, err := df.Column[string]("tag")
		if err != nil {
			t.Fatal(err)
		}
		for i := range want {
			got, ok := tags.Get(i)
			if !ok {
				got = "∅"
			}
			if got != want[i] {
				t.Fatalf("%s, batch %d, left row %d: %q, want %q", name, bs, i, got, want[i])
			}
		}
	}
}

// TestBackwardAsOfHoldsARowAByKey: a right side of 300,000 rows over ten by-keys
// joins with a peak of kilobytes, where the buffered join held the right side, about
// 7 MB here. Forward and nearest too, since step 194: the by-keys interleave, so
// what they read ahead to find a by-key's next row is a few rows.
func TestBackwardAsOfHoldsARowAByKey(t *testing.T) {
	const n = 300_000
	rk, rv := make([]int64, n), make([]int64, n)
	rg := make([]string, n)
	for i := range n {
		rk[i], rv[i], rg[i] = int64(i), int64(i)*10, string(rune('a'+i%10))
	}
	lk, lg := make([]int64, 1000), make([]string, 1000)
	for i := range lk {
		lk[i], lg[i] = int64(i*300), string(rune('a'+i%10))
	}
	for _, strategy := range []ursus.AsOfStrategy{ursus.AsOfBackward, ursus.AsOfForward, ursus.AsOfNearest} {
		var st ursus.MemoryStats
		df, err := ursus.Frame(ursus.Values("t", lk), ursus.Values("g", lg)).
			JoinAsOf(ursus.Frame(ursus.Values("t", rk), ursus.Values("g", rg), ursus.Values("v", rv)),
				ursus.AsOfOn(ursus.Col("t")), ursus.AsOfBy(ursus.Col("g")), ursus.AsOfStrategyOpt(strategy)).
			Collect(t.Context(), ursus.WithMemoryStats(&st), ursus.WithBatchSize(4096))
		if err != nil {
			t.Fatal(err)
		}
		if df.Height() != 1000 {
			t.Fatalf("%v: %d rows", strategy, df.Height())
		}
		if st.Peak > 1<<20 {
			t.Errorf("%v: a peak of %d bytes, for a right side of %d", strategy, st.Peak, n*24)
		}
		t.Logf("%v: a peak of %d bytes", strategy, st.Peak)
	}
}

// TestBackwardAsOfStillRefusesAnUnsortedRightSide: past the left side's last key, as
// the buffered join read the right side whole and refused it.
func TestBackwardAsOfStillRefusesAnUnsortedRightSide(t *testing.T) {
	left := ursus.Frame(ursus.Values("t", []int64{1, 2}))
	// With batches of two, matching the left side reads 0 1 and 50 60, and stops at
	// 50; only reading the rest finds 40 after 60.
	right := ursus.Frame(ursus.Values("t", []int64{0, 1, 50, 60, 40, 70}), ursus.Values("v", []int64{1, 2, 3, 4, 5, 6}))
	_, err := left.JoinAsOf(right, ursus.AsOfOn(ursus.Col("t"))).Collect(t.Context(), ursus.WithBatchSize(2))
	if err == nil || !strings.Contains(err.Error(), "the right side is not sorted") {
		t.Fatalf("an unsorted right side past the left keys: %v", err)
	}
}

// TestForwardAndNearestAsOfStreamingAgreeWithAScan: step 194, v0.6.1 item O3. As
// the backward test, for forward and nearest, against each left row's match found by
// scanning: the first right row at or after its key, or after it, in its by-bucket,
// and for nearest the closer of that and the backward one, a tie going backward; a
// tolerance applied to the row chosen.
//
// The left side has a by-key the right side never holds, which reads the right side
// to its end, and one the right side holds rarely, which reads far ahead; and keys
// tie within each side and across them. At batch sizes down to one row, so reading
// ahead crosses batches and compacts the queues many times.
func TestForwardAndNearestAsOfStreamingAgreeWithAScan(t *testing.T) {
	rng := rand.New(rand.NewPCG(194, 1))
	side := func(n int, maxStep int64, byNull int, bys string) (k []int64, by []string, byOK []bool, tag []string) {
		k, by, byOK, tag = make([]int64, n), make([]string, n), make([]bool, n), make([]string, n)
		v := int64(0)
		for i := range n {
			v += rng.Int64N(maxStep)
			b := bys[rng.IntN(len(bys))]
			if b == 'r' && rng.IntN(40) != 0 {
				b = 'a' // r, rare
			}
			k[i], by[i], byOK[i] = v, string(rune(b)), rng.IntN(byNull) != 0
			tag[i] = by[i] + "#" + string(rune('A'+i%26)) + string(rune('A'+i/26%26))
		}
		return
	}
	lk, lby, lok, _ := side(300, 7, 20, "abcdrz") // z: never on the right
	rk, rby, rok, rtag := side(900, 3, 15, "abcdr")
	left := ursus.Frame(ursus.Values("t", lk), ursus.ValuesNullable("g", lby, lok))
	right := ursus.Frame(ursus.Values("t", rk), ursus.ValuesNullable("g", rby, rok), ursus.Values("tag", rtag))

	abs := func(v int64) int64 {
		if v < 0 {
			return -v
		}
		return v
	}
	for _, tc := range []struct {
		name     string
		strategy ursus.AsOfStrategy
		exact    bool
		tol      int64
		byKey    bool
	}{
		{"forward, by, exact", ursus.AsOfForward, true, 0, true},
		{"forward, by, strictly after", ursus.AsOfForward, false, 0, true},
		{"forward, by, a tolerance of 4", ursus.AsOfForward, true, 4, true},
		{"forward, no by, strictly after", ursus.AsOfForward, false, 0, false},
		{"nearest, by, exact", ursus.AsOfNearest, true, 0, true},
		{"nearest, by, strictly", ursus.AsOfNearest, false, 0, true},
		{"nearest, by, a tolerance of 2", ursus.AsOfNearest, true, 2, true},
		{"nearest, no by", ursus.AsOfNearest, true, 0, false},
	} {
		want := make([]string, len(lk))
		for i := range lk {
			want[i] = "∅"
			if tc.byKey && !lok[i] {
				continue
			}
			back, fwd := -1, -1
			for j := range rk {
				if tc.byKey && (!rok[j] || rby[j] != lby[i]) {
					continue
				}
				if rk[j] < lk[i] || (tc.exact && rk[j] == lk[i]) {
					back = j // the last at or before
				}
				if fwd < 0 && (rk[j] > lk[i] || (tc.exact && rk[j] == lk[i])) {
					fwd = j // the first at or after
				}
			}
			pick := fwd
			if tc.strategy == ursus.AsOfNearest && back >= 0 && (fwd < 0 || abs(lk[i]-rk[back]) <= abs(rk[fwd]-lk[i])) {
				pick = back
			}
			if pick < 0 || (tc.tol > 0 && abs(rk[pick]-lk[i]) > tc.tol) {
				continue
			}
			want[i] = rtag[pick]
		}
		opts := []ursus.AsOfOption{ursus.AsOfOn(ursus.Col("t")), ursus.AsOfAllowExactMatches(tc.exact),
			ursus.AsOfStrategyOpt(tc.strategy)}
		if tc.byKey {
			opts = append(opts, ursus.AsOfBy(ursus.Col("g")))
		}
		if tc.tol > 0 {
			l := left.Select(ursus.Col("t").Cast(ursus.Duration(ursus.Nano)).Alias("t"), ursus.Col("g"))
			r := right.Select(ursus.Col("t").Cast(ursus.Duration(ursus.Nano)).Alias("t"), ursus.Col("g"), ursus.Col("tag"))
			opts = append(opts, ursus.AsOfTolerance(ursus.FromDuration(time.Duration(tc.tol))))
			check(t, tc.name, l.JoinAsOf(r, opts...), want)
			continue
		}
		check(t, tc.name, left.JoinAsOf(right, opts...), want)
	}
}
