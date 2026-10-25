package ursus_test

import (
	"fmt"
	"math"
	"math/rand/v2"
	"testing"

	"github.com/advenn/ursus"
)

// TestSortOnThreadsMatchesOneThread is step 361's: a sort on four threads gives one
// thread's frame, ties in their input order, whether it radix-sorts or merges,
// takes the top rows, spills, or reads batches of a power of two's rows or of any
// other count.
func TestSortOnThreadsMatchesOneThread(t *testing.T) {
	const n = 150_000
	rng := rand.New(rand.NewPCG(361, 3))
	k := make([]int64, n)
	f := make([]float64, n)
	s := make([]string, n)
	seq := make([]int64, n)
	kOK, fOK, sOK := make([]bool, n), make([]bool, n), make([]bool, n)
	for i := range n {
		k[i] = rng.Int64N(50)
		f[i] = float64(rng.IntN(40)) - 20
		s[i] = fmt.Sprintf("s%02d", rng.IntN(30))
		seq[i] = int64(n - i)
		kOK[i], fOK[i], sOK[i] = i%7 != 3, i%11 != 5, i%5 != 1
	}
	f[10], f[20], f[30], f[n/2] = math.NaN(), math.Inf(-1), math.Inf(1), math.Copysign(0, -1)
	frame := func() *ursus.LazyFrame {
		return ursus.Frame(
			ursus.ValuesNullable("k", k, kOK),
			ursus.ValuesNullable("f", f, fOK),
			ursus.ValuesNullable("s", s, sOK),
			ursus.Values("seq", seq),
		)
	}
	c := ursus.Col
	sorts := map[string]func(*ursus.LazyFrame) *ursus.LazyFrame{
		"k": func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Sort(ursus.Asc(c("k"))) },
		"f descending, nulls last": func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.Sort(ursus.Desc(c("f")).NullsLast())
		},
		"s, then k descending": func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.Sort(ursus.Asc(c("s")), ursus.Desc(c("k")))
		},
		"seq, distinct": func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Sort(ursus.Asc(c("seq"))) },
		"the top 5000 of k": func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.Sort(ursus.Asc(c("k"))).Head(5000)
		},
	}
	for name, sort := range sorts {
		for _, batch := range []int{8192, 1000, 70_000} {
			want, err := sort(frame()).Collect(t.Context(), ursus.WithThreads(1), ursus.WithBatchSize(batch))
			if err != nil {
				t.Fatal(err)
			}
			got, err := sort(frame()).Collect(t.Context(), ursus.WithThreads(4), ursus.WithBatchSize(batch))
			if err != nil {
				t.Fatal(err)
			}
			sameFrame(t, fmt.Sprintf("%s, batches of %d", name, batch), got, want)
		}
		var stats ursus.MemoryStats
		got, err := sort(frame()).Collect(t.Context(), ursus.WithThreads(4),
			ursus.WithMemoryLimit(1<<20), ursus.WithSpillDir(t.TempDir()), ursus.WithMemoryStats(&stats))
		if err != nil {
			t.Fatal(err)
		}
		want, err := sort(frame()).Collect(t.Context(), ursus.WithThreads(1))
		if err != nil {
			t.Fatal(err)
		}
		if stats.Spills == 0 && name != "the top 5000 of k" {
			t.Errorf("%s never spilled, so it proves nothing", name)
		}
		sameFrame(t, name+", spilled", got, want)
	}
}

// sameFrame is AssertFrameEqual at a hundredth of its cost, for frames of 150,000
// rows: seq is distinct, so equal frames are one order of the rows.
func sameFrame(t *testing.T, what string, got, want *ursus.DataFrame) {
	t.Helper()
	ok, err := got.Equals(t.Context(), want, true)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Errorf("%s: the frame differs from one thread's\n got:\n%s\nwant:\n%s", what, got, want)
	}
}
