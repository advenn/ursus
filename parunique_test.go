package ursus_test

import (
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/advenn/ursus"
)

// TestUniqueOnThreadsMatchesOneThread is step 362's: a unique on several threads
// keeps one thread's rows, each key's first, in their order, for every form of key,
// an integer, a string, a float, several columns, nulls among them; of keys few, which
// one goroutine inserts, and many, which the threads do, past 65,536; in batches the
// threads split and in batches too few to; past a memory limit; and stopped early.
func TestUniqueOnThreadsMatchesOneThread(t *testing.T) {
	const n = 150_000
	rng := rand.New(rand.NewPCG(362, 1))
	k := make([]int64, n)
	many := make([]int64, n)
	small := make([]int32, n)
	u8 := make([]uint8, n)
	f := make([]float64, n)
	s := make([]string, n)
	ms := make([]string, n)
	mf := make([]float64, n)
	b := make([]bool, n)
	seq := make([]int64, n)
	kOK, fOK, sOK := make([]bool, n), make([]bool, n), make([]bool, n)
	for i := range n {
		k[i] = rng.Int64N(3000) - 1500
		many[i] = rng.Int64N(n)
		small[i] = int32(rng.IntN(70))
		u8[i] = uint8(rng.IntN(5))
		f[i] = float64(rng.IntN(500)) / 4
		s[i] = fmt.Sprintf("s%d", rng.IntN(2000))
		ms[i] = fmt.Sprintf("m%d", many[i])
		mf[i] = float64(many[i]) / 8
		if i%17 == 0 {
			s[i] = ""
		}
		b[i] = rng.IntN(2) == 0
		seq[i] = int64(i)
		kOK[i], fOK[i], sOK[i] = i%7 != 3, i%11 != 5, i%5 != 1
	}
	for i := 0; i < n; i += 1000 {
		f[i], f[i+1], f[i+2] = math.NaN(), math.Copysign(0, -1), 0
	}
	frame := func() *ursus.LazyFrame {
		return ursus.Frame(
			ursus.ValuesNullable("k", k, kOK),
			ursus.ValuesNullable("many", many, kOK),
			ursus.Values("small", small),
			ursus.Values("u8", u8),
			ursus.ValuesNullable("f", f, fOK),
			ursus.ValuesNullable("s", s, sOK),
			ursus.ValuesNullable("ms", ms, sOK),
			ursus.ValuesNullable("mf", mf, fOK),
			ursus.Values("b", b),
			ursus.Values("seq", seq),
		)
	}
	c := ursus.Col
	cases := []struct {
		name string
		q    func(*ursus.LazyFrame) *ursus.LazyFrame
	}{
		{"an integer, nulls", func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Unique("k") }},
		{"an integer of many, nulls", func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Unique("many") }},
		{"an Int32", func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Unique("small") }},
		{"a float, NaN and both zeros", func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Unique("f") }},
		{"a string, nulls and empties", func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Unique("s") }},
		{"a string of many, nulls", func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Unique("ms") }},
		{"a float of many, nulls", func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Unique("mf") }},
		{"two columns of many", func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Unique("many", "u8") }},
		{"a Categorical", func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.WithColumns(c("s").Cast(ursus.Categorical)).Unique("s")
		}},
		{"a date", func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.WithColumns(c("small").Cast(ursus.Date)).Unique("small")
		}},
		{"three columns", func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Unique("u8", "b", "small") }},
		{"a Boolean", func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Unique("b") }},
		{"every column", func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.Select(c("u8"), c("b"), c("small")).Unique()
		}},
		{"the first ten", func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Unique("many").Head(10) }},
	}
	for _, tc := range cases {
		want, err := tc.q(frame()).Collect(t.Context(), ursus.WithThreads(1))
		if err != nil {
			t.Fatal(err)
		}
		for _, threads := range []int{4, 7} {
			for _, batch := range []int{8192, 1000} {
				got, err := tc.q(frame()).Collect(t.Context(), ursus.WithThreads(threads), ursus.WithBatchSize(batch))
				if err != nil {
					t.Fatal(err)
				}
				sameUnique(t, fmt.Sprintf("%s on %d threads, batches of %d", tc.name, threads, batch), got, want)
			}
		}
		// A limit each set outgrows, and one that a deep spill of many keys does not
		// take seconds under.
		limit := int64(16 << 10)
		if want.Height() > 20_000 {
			limit = 256 << 10
		}
		var stats ursus.MemoryStats
		got, err := tc.q(frame()).Collect(t.Context(), ursus.WithThreads(4),
			ursus.WithMemoryLimit(limit), ursus.WithSpillDir(t.TempDir()), ursus.WithMemoryStats(&stats))
		if err != nil {
			t.Fatal(err)
		}
		if stats.Spills == 0 && want.Height() > 1000 {
			t.Errorf("%s: %d keys never spilled under %d bytes", tc.name, want.Height(), limit)
		}
		sameUnique(t, tc.name+", spilled", got, want)
	}

	// The rows kept are each key's first, as the definition has them, a null a key.
	df, err := frame().Unique("k").Collect(t.Context(), ursus.WithThreads(4))
	if err != nil {
		t.Fatal(err)
	}
	type key struct {
		v  int64
		ok bool
	}
	seen := map[key]bool{}
	var first []int64
	for i := range n {
		kk := key{k[i], kOK[i]}
		if !kOK[i] {
			kk.v = 0
		}
		if !seen[kk] {
			seen[kk] = true
			first = append(first, int64(i))
		}
	}
	got, err := df.Column[int64]("seq")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Values(), first) {
		t.Errorf("Unique(k) kept %d rows, not each key's first of %d", len(got.Values()), len(first))
	}
}

// sameUnique is a frame's Equals, which a frame of 150,000 rows is checked by at a
// hundredth of AssertFrameEqual's cost.
func sameUnique(t *testing.T, what string, got, want *ursus.DataFrame) {
	t.Helper()
	ok, err := got.Equals(t.Context(), want, true)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Errorf("%s: %d rows differ from one thread's %d\n got:\n%s\nwant:\n%s",
			what, got.Height(), want.Height(), got, want)
	}
}
