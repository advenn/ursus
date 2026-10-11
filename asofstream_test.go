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
// 7 MB here.
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
	var st ursus.MemoryStats
	df, err := ursus.Frame(ursus.Values("t", lk), ursus.Values("g", lg)).
		JoinAsOf(ursus.Frame(ursus.Values("t", rk), ursus.Values("g", rg), ursus.Values("v", rv)),
			ursus.AsOfOn(ursus.Col("t")), ursus.AsOfBy(ursus.Col("g"))).
		Collect(t.Context(), ursus.WithMemoryStats(&st), ursus.WithBatchSize(4096))
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 1000 {
		t.Fatalf("%d rows", df.Height())
	}
	if st.Peak > 1<<20 {
		t.Errorf("a peak of %d bytes, for a right side of %d", st.Peak, n*24)
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
