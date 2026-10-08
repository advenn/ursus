package memcheck_test

// A query's live heap against what its memory budget counts (step 129).
//
// The budget only holds if what the operators keep is what they charge. Step 128
// found the opposite at full size: Collect kept a join's build side alive while it
// assembled the result, and the result and a join's build side were each held twice
// while they were concatenated, none of it charged, so a join of two ten-million-row
// tables was killed under a 4 GB container. These tests measure the same things on
// a few million rows, in seconds, and fail if they come back.
//
// # How
//
// Two readings of the live heap, above where it stood before the query:
//
//   - Sampled every 200µs, with the collector made to run often (GOGC=10). That
//     follows an operator's state, which lasts, though a sample can also count the
//     floating garbage a collection allocates while it marks: the same Collect read
//     145 MB in one run and 111 MB in the next. So its bounds are wide.
//   - Exact, at every column a concatenation copies: kernel.ConcatColumnDone runs a
//     full collection there and reads the result. A copy takes milliseconds, too fast
//     for the samples to land in, and it is where a doubled result or a build side
//     held twice is largest.
//
// The inputs are built first, are part of the baseline, and are kept reachable until
// the reading ends. Results are streamed and dropped except where Collect is the
// subject.
//
// The bounds are wide on purpose: they are for large uncounted state, a second copy
// of something, which moves a ratio by most of a whole; not for the last ten percent.

import (
	"fmt"
	"math/rand/v2"
	"os"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/kernel"
)

func liveNow() uint64 {
	s := []metrics.Sample{{Name: "/gc/heap/live:bytes"}}
	metrics.Read(s)
	return s[0].Value.Uint64()
}

// liveDuring runs f while sampling the live heap, and returns the most it rose above
// where it stood before f.
//
// keep is every input f reads, kept reachable until f returns. Without it the
// collector frees an input as soon as nothing will read it again, which for a query
// is once its scan is closed, and the live heap then falls below the baseline the
// input was part of: this test's first version read a doubled result as a single
// one, by exactly the size of its input.
func liveDuring(f func(), keep ...any) (sampled, atColumns int64) {
	runtime.GC()
	base := liveNow()
	var peak, exact atomic.Uint64
	note := func(at *atomic.Uint64, v uint64) {
		for {
			p := at.Load()
			if v <= p || at.CompareAndSwap(p, v) {
				return
			}
		}
	}
	kernel.ConcatColumnDone = func() {
		runtime.GC()
		v := liveNow()
		note(&exact, v)
		note(&peak, v)
	}
	defer func() { kernel.ConcatColumnDone = nil }()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		tick := time.NewTicker(200 * time.Microsecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				note(&peak, liveNow())
			}
		}
	})
	f()
	close(stop)
	wg.Wait()
	runtime.KeepAlive(keep)
	above := func(v uint64) int64 { return max(0, int64(v)-int64(base)) }
	return above(peak.Load()), above(exact.Load())
}

const mb = 1 << 20

// frame is an in-memory frame of n rows: key column k, holding keyOf(i), and cols
// Float64 columns v0, v1, ….
func frame(t *testing.T, n, cols int, keyOf func(int) int64) *ursus.LazyFrame {
	t.Helper()
	rng := rand.New(rand.NewPCG(129, uint64(n*cols)))
	ks := make([]int64, n)
	for i := range ks {
		ks[i] = keyOf(i)
	}
	vals := []*ursus.Column{ursus.Values("k", ks)}
	for c := range cols {
		v := make([]float64, n)
		for i := range v {
			v[i] = rng.Float64()
		}
		vals = append(vals, ursus.Values(fmt.Sprintf("v%d", c), v))
	}
	df, err := ursus.Frame(vals...).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return df.Lazy()
}

func stream(t *testing.T, lf *ursus.LazyFrame, opts ...ursus.CollectOption) {
	t.Helper()
	for _, err := range lf.CollectBatches(t.Context(), opts...) {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func report(t *testing.T, name string, live int64, st ursus.MemoryStats) {
	t.Helper()
	if os.Getenv("URSUS_MEMCHECK_VERBOSE") != "" {
		fmt.Printf("%-28s live +%6.1f MB, counted %6.1f MB, spills %d\n",
			name, float64(live)/mb, float64(st.Peak)/mb, st.Spills)
	}
}

func TestQueriesHoldWhatTheBudgetCounts(t *testing.T) {
	defer debug.SetGCPercent(debug.SetGCPercent(10))
	c := ursus.Col
	const n = 1 << 21
	rng := rand.New(rand.NewPCG(129, 0))
	build := frame(t, n, 4, func(i int) int64 { return int64(i) })
	probe := frame(t, n, 1, func(int) int64 { return rng.Int64N(n) })
	limit := ursus.WithMemoryLimit(1 << 30)
	// An in-memory frame's batches are views of its own buffers, so a join building
	// from one retains nothing new, and nothing a concatenation could hold twice. A
	// filter that keeps nearly every row copies them, as a file's scan would: the case
	// that was killed (step 128) read CSV.
	copied := build.Filter(c("k").Mod(1000).Ne(999))

	t.Run("a join holds what it charges", func(t *testing.T) {
		var st ursus.MemoryStats
		live, atFreeze := liveDuring(func() {
			stream(t, probe.Join(copied, ursus.JoinOn(c("k"))), limit, ursus.WithMemoryStats(&st))
		}, build, probe)
		report(t, t.Name(), live, st)
		report(t, t.Name()+" (freezing)", atFreeze, st)
		if live > st.Peak*13/10+16*mb {
			t.Errorf("the live heap rose %d MB; the join charged a peak of %d MB", live/mb, st.Peak/mb)
		}
		// When the build side freezes, its batches are concatenated into one: the
		// moment it was held twice, uncharged, until step 128. Held once, that is
		// what the join charges and one column more.
		if atFreeze > st.Peak+24*mb {
			t.Errorf("freezing the build side held %d MB; the join charged a peak of %d MB",
				atFreeze/mb, st.Peak/mb)
		}
	})
	t.Run("a group-by holds what it charges", func(t *testing.T) {
		var st ursus.MemoryStats
		live, _ := liveDuring(func() {
			stream(t, build.GroupBy(c("k")).Agg(c("v0").Sum()), limit, ursus.WithMemoryStats(&st))
		}, build)
		report(t, t.Name(), live, st)
		if live > st.Peak*13/10+16*mb {
			t.Errorf("the live heap rose %d MB; the group-by charged a peak of %d MB", live/mb, st.Peak/mb)
		}
	})
	t.Run("a join under a small budget spills and stays near it", func(t *testing.T) {
		var st ursus.MemoryStats
		const small = 16 * mb
		live, _ := liveDuring(func() {
			stream(t, probe.Join(copied, ursus.JoinOn(c("k"))), ursus.WithMemoryLimit(small), ursus.WithMemoryStats(&st))
		}, build, probe)
		report(t, t.Name(), live, st)
		if st.Spills == 0 {
			t.Fatal("nothing spilled")
		}
		// Held whole, the join's state is about 185 MB; spilled, a few times the
		// budget, sampled with the filter's floating garbage.
		if live > 5*small+16*mb {
			t.Errorf("the live heap rose %d MB under a %d MB budget", live/mb, int64(small)/mb)
		}
	})
}

func TestCollectHoldsTheResultOnce(t *testing.T) {
	defer debug.SetGCPercent(debug.SetGCPercent(10))
	c := ursus.Col
	const n = 1 << 21
	rng := rand.New(rand.NewPCG(129, 1))
	wide := frame(t, n, 6, func(i int) int64 { return int64(i) })
	limit := ursus.WithMemoryLimit(1 << 30)

	// A filter keeping three rows in four copies them into new buffers, so the
	// result is new memory and nothing below it is held: what Collect itself holds.
	//
	// Under the default budget too, where Collect counts its result (step 130) in an
	// account that kept every batch alive until the result was whole (step 131).
	for _, b := range []struct {
		name string
		opts []ursus.CollectOption
	}{
		{"under a limit", []ursus.CollectOption{limit}},
		{"under the default budget", nil},
	} {
		t.Run("a result is not held twice while it is assembled, "+b.name, func(t *testing.T) {
			var df *ursus.DataFrame
			var st ursus.MemoryStats
			_, live := liveDuring(func() {
				var err error
				df, err = wide.Filter(c("k").Mod(4).Ne(0)).Collect(t.Context(),
					append(b.opts, ursus.WithMemoryStats(&st))...)
				if err != nil {
					t.Fatal(err)
				}
			}, wide)
			result := int64(df.Height()) * 7 * 8
			report(t, t.Name(), live, st)
			// Held once is the result and one column more, 1.14 of it here; held twice
			// is 2.
			if live > result*13/10+8*mb {
				t.Errorf("assembling a %d MB result held %d MB", result/mb, live/mb)
			}
			runtime.KeepAlive(df)
		})
	}
	// Four probe rows a build key, so the result outweighs the join's own state.
	t.Run("a join's state is not held while its result is assembled", func(t *testing.T) {
		build := frame(t, n/4, 1, func(i int) int64 { return int64(i) })
		probe := frame(t, n, 6, func(int) int64 { return rng.Int64N(n / 4) })
		var df *ursus.DataFrame
		var st ursus.MemoryStats
		_, live := liveDuring(func() {
			var err error
			df, err = probe.Join(build, ursus.JoinOn(c("k"))).Collect(t.Context(), limit, ursus.WithMemoryStats(&st))
			if err != nil {
				t.Fatal(err)
			}
		}, build, probe)
		result := int64(df.Height()) * 8 * 8
		report(t, t.Name(), live, st)
		// The result and one column more, about 1.13 of it; the join's table on top
		// of that is another quarter, and the result held twice is 2.
		if live > result*12/10+12*mb {
			t.Errorf("assembling a %d MB result held %d MB; the join had charged %d MB",
				result/mb, live/mb, st.Peak/mb)
		}
		runtime.KeepAlive(df)
	})
}

// TestAGroupByHoldsItsAnswerOnce: a group-by with one group per row, h2o gb10's
// shape, reached a 3 GB container's cap (step 128's record). Its answer is about its
// input's size, and it held its keys twice while it assembled it, with the key table
// alive beside both: a concatenation of every key part, kept until the last column
// was copied (step 131).
//
// Three String keys and three Int64, and a sum and a count, as gb10 has. Streamed,
// so what is read is the group-by's own.
func TestAGroupByHoldsItsAnswerOnce(t *testing.T) {
	defer debug.SetGCPercent(debug.SetGCPercent(10))
	c := ursus.Col
	const n = 1 << 21
	var cols []*ursus.Column
	for k, name := range []string{"id1", "id2", "id3"} {
		v := make([]string, n)
		for i := range v {
			v[i] = fmt.Sprintf("id%07d", (i*7+k)%n)
		}
		cols = append(cols, ursus.Values(name, v))
	}
	for _, name := range []string{"id4", "id5", "id6"} {
		v := make([]int64, n)
		for i := range v {
			v[i] = int64(i)
		}
		cols = append(cols, ursus.Values(name, v))
	}
	cols = append(cols, ursus.Values("v3", make([]float64, n)))
	df, err := ursus.Frame(cols...).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// A filter keeping nearly every row copies them, as a file's scan does.
	q := df.Lazy().Filter(c("id4").Mod(1000).Ne(999)).
		GroupBy(c("id1"), c("id2"), c("id3"), c("id4"), c("id5"), c("id6")).
		Agg(c("v3").Sum(), ursus.Len())

	// The answer's size: what its batches point into, each allocation once. They are
	// slices of the one answer the group-by built.
	answer := map[data.BufferID]int64{}
	var st ursus.MemoryStats
	live, atColumns := liveDuring(func() {
		for out, err := range q.CollectBatches(t.Context(), ursus.WithMemoryLimit(1<<30),
			ursus.WithMemoryStats(&st)) {
			if err != nil {
				t.Fatal(err)
			}
			for _, col := range out.Batch().Columns() {
				for id, size := range col.Buffers() {
					answer[id] = size
				}
			}
		}
	}, df)
	var result int64
	for _, size := range answer {
		result += size
	}
	report(t, t.Name(), live, st)
	report(t, t.Name()+" (assembling)", atColumns, st)
	if os.Getenv("URSUS_MEMCHECK_VERBOSE") != "" {
		fmt.Printf("%-28s %6.1f MB\n", "  its answer", float64(result)/mb)
	}

	// While it assembles the answer it holds the answer, one column of key parts not
	// yet let go, and the accumulators: 1.08 of the answer here. Its keys twice were
	// 3.2, with the key table beside them.
	if atColumns > result*13/10+16*mb {
		t.Errorf("assembling a %d MB answer held %d MB", result/mb, atColumns/mb)
	}
	// Sampled, the most is now while it aggregates, at 1.31 of what it charges here,
	// against the one-key group-by's 1.17. That phase is not what this test is for,
	// and step 131 did not change it; the bound is for a second copy of something.
	if live > st.Peak*15/10+16*mb {
		t.Errorf("the live heap rose %d MB; the group-by charged a peak of %d MB", live/mb, st.Peak/mb)
	}
}
