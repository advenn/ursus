// Command svcprobe measures what a Go service feels of ursus: the probe of the 0.8
// scope (context_files/v0.8-scope.md, step 392), each of its items measured with it
// before and after. Build it as the benchmarks are, GOEXPERIMENT=simd GOAMD64=v3, and
// run one mode:
//
//	lat     four small queries at 1,000, 10,000 and 100,000 rows, on all threads and
//	        one: a filter and projection, a group-by's sum and mean, a join to a
//	        100-row dimension and a sort's head of ten; pl_lat.py is Polars' twin
//	conc    a 10,000-row group-by from 1, 4, 16 and 64 goroutines, queries a second
//	conc64  the same from 64 goroutines on one thread each, profiled to $PROF and the
//	        collector's share printed
//	alloc   what each small query allocates on one thread
//	cancel  how soon a query over ten million rows returns once cancelled, and the
//	        goroutines left after: a group-by, a sort, a join, a collect, and h2o's
//	        g1 scanned from Parquet and CSV (run from the repository's root); CASE
//	        picks one by name
//	mem     what a group-by of five million keys leaves held once it is done
//	shared  sixteen queries at once over four million rows, half sorts and half
//	        group-bys of a million keys, each on an engine of its own of $BUDGET MiB
//	        (ARM=own), or all on one (ARM=shared): the peak RSS, spills and time;
//	        MIX=sort or MIX=agg runs sixteen of one, N=n n queries, EACH=1 prints
//	        each query's
//	noop    nothing, for GODEBUG=inittrace=1
package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"runtime/pprof"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/advenn/ursus"
)

var c = ursus.Col

func frame(n int) *ursus.DataFrame {
	k, x, s := make([]int64, n), make([]float64, n), make([]string, n)
	for i := range n {
		r := uint64(i) * 0x9E3779B97F4A7C15
		k[i], x[i], s[i] = int64(r>>40%100), float64(r>>20%10_000)/7, fmt.Sprintf("s%d", r>>50%1000)
	}
	df, err := ursus.Frame(ursus.Values("k", k), ursus.Values("x", x), ursus.Values("s", s)).Collect(context.Background())
	if err != nil {
		panic(err)
	}
	return df
}

func dim() *ursus.DataFrame {
	k, name := make([]int64, 100), make([]string, 100)
	for i := range 100 {
		k[i], name[i] = int64(i), fmt.Sprintf("name%d", i)
	}
	df, _ := ursus.Frame(ursus.Values("k", k), ursus.Values("name", name)).Collect(context.Background())
	return df
}

type query struct {
	name string
	q    func(df, d *ursus.DataFrame) *ursus.LazyFrame
}

var queries = []query{
	{"filter+select", func(df, _ *ursus.DataFrame) *ursus.LazyFrame {
		return df.Lazy().Filter(c("x").Gt(500.0)).Select(c("k"), c("x").Mul(2.0).Alias("y"))
	}},
	{"group-by sum, mean", func(df, _ *ursus.DataFrame) *ursus.LazyFrame {
		return df.Lazy().GroupBy(c("k")).Agg(c("x").Sum().Alias("sum"), c("x").Mean().Alias("mean"))
	}},
	{"join 100-row dim", func(df, d *ursus.DataFrame) *ursus.LazyFrame {
		return df.Lazy().Join(d.Lazy(), ursus.JoinOn(c("k")))
	}},
	{"sort, head 10", func(df, _ *ursus.DataFrame) *ursus.LazyFrame {
		return df.Lazy().Sort(ursus.Asc(c("x"))).Head(10)
	}},
}

func median(ts []time.Duration) time.Duration { slices.Sort(ts); return ts[len(ts)/2] }

func lat() {
	defer func() { fmt.Printf("lat the default engine after: %+v\n", ursus.DefaultEngine().Stats()) }()
	d := dim()
	for _, n := range []int{1_000, 10_000, 100_000} {
		df := frame(n)
		iters := map[int]int{1_000: 2000, 10_000: 500, 100_000: 100}[n]
		for _, q := range queries {
			for _, th := range []int{0, 1} {
				opts := []ursus.CollectOption{}
				if th > 0 {
					opts = append(opts, ursus.WithThreads(th))
				}
				var ts []time.Duration
				for range iters {
					t0 := time.Now()
					if _, err := q.q(df, d).Collect(context.Background(), opts...); err != nil {
						panic(err)
					}
					ts = append(ts, time.Since(t0))
				}
				label := "default"
				if th > 0 {
					label = "1 thread"
				}
				fmt.Printf("lat %7d rows  %-20s %-8s median %8.1f µs\n", n, q.name, label, float64(median(ts).Nanoseconds())/1e3)
			}
		}
	}
}

func conc() {
	df := frame(10_000)
	q := func(opts ...ursus.CollectOption) error {
		_, err := df.Lazy().GroupBy(c("k")).Agg(c("x").Sum().Alias("sum"), c("x").Mean().Alias("mean")).Collect(context.Background(), opts...)
		return err
	}
	for _, th := range []int{0, 1} {
		for _, g := range []int{1, 4, 16, 64} {
			var done atomic.Int64
			var peak atomic.Int64
			stop := make(chan struct{})
			go func() {
				for {
					select {
					case <-stop:
						return
					default:
						if n := int64(runtime.NumGoroutine()); n > peak.Load() {
							peak.Store(n)
						}
						time.Sleep(time.Millisecond)
					}
				}
			}()
			var wg sync.WaitGroup
			t0 := time.Now()
			deadline := t0.Add(2 * time.Second)
			for range g {
				wg.Go(func() {
					opts := []ursus.CollectOption{}
					if th > 0 {
						opts = append(opts, ursus.WithThreads(th))
					}
					for time.Now().Before(deadline) {
						if err := q(opts...); err != nil {
							panic(err)
						}
						done.Add(1)
					}
				})
			}
			wg.Wait()
			close(stop)
			el := time.Since(t0)
			label := "default threads"
			if th > 0 {
				label = "1 thread each"
			}
			fmt.Printf("conc %-15s %3d goroutines: %7.0f queries/s, peak %5d goroutines\n", label, g,
				float64(done.Load())/el.Seconds(), peak.Load())
		}
	}
}

// alloc is what each small query allocates, and how many allocations, on one thread.
func alloc() {
	d := dim()
	for _, n := range []int{1_000, 10_000, 100_000} {
		df := frame(n)
		for _, q := range queries {
			var a, b runtime.MemStats
			const iters = 50
			runtime.GC()
			runtime.ReadMemStats(&a)
			for range iters {
				if _, err := q.q(df, d).Collect(context.Background(), ursus.WithThreads(1)); err != nil {
					panic(err)
				}
			}
			runtime.ReadMemStats(&b)
			fmt.Printf("alloc %7d rows  %-20s %8.1f kB, %6d allocations a query; the input %d kB\n", n, q.name,
				float64(b.TotalAlloc-a.TotalAlloc)/iters/1024, (b.Mallocs-a.Mallocs)/iters, n*(8+8+8+4)/1024)
		}
	}
}

// conc64 profiles 64 goroutines collecting the 10k-row group-by on one thread each.
func conc64() {
	df := frame(10_000)
	runtime.SetMutexProfileFraction(5)
	f, _ := os.Create(os.Getenv("PROF"))
	pprof.StartCPUProfile(f)
	var done atomic.Int64
	var wg sync.WaitGroup
	t0 := time.Now()
	deadline := t0.Add(3 * time.Second)
	for range 64 {
		wg.Go(func() {
			for time.Now().Before(deadline) {
				_, err := df.Lazy().GroupBy(c("k")).Agg(c("x").Sum().Alias("sum"), c("x").Mean().Alias("mean")).
					Collect(context.Background(), ursus.WithThreads(1))
				if err != nil {
					panic(err)
				}
				done.Add(1)
			}
		})
	}
	wg.Wait()
	pprof.StopCPUProfile()
	m, _ := os.Create(os.Getenv("PROF") + ".mutex")
	pprof.Lookup("mutex").WriteTo(m, 0)
	m.Close()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	fmt.Printf("conc64: %.0f queries/s; %d GCs, %.1f%% of CPU in GC, %d MB allocated a second\n",
		float64(done.Load())/time.Since(t0).Seconds(), ms.NumGC, ms.GCCPUFraction*100,
		int(float64(ms.TotalAlloc)/time.Since(t0).Seconds())>>20)
}

func big() *ursus.DataFrame {
	n := 10_000_000
	k, x := make([]int64, n), make([]float64, n)
	for i := range n {
		r := uint64(i) * 0x9E3779B97F4A7C15
		k[i], x[i] = int64(r>>20%5_000_000), float64(r>>30%100_000)
	}
	df, err := ursus.Frame(ursus.Values("k", k), ursus.Values("x", x)).Collect(context.Background())
	if err != nil {
		panic(err)
	}
	return df
}

func cancel() {
	df := big()
	dim := must(ursus.Frame(ursus.Values("k", seq(5_000_000)), ursus.Values("y", seq(5_000_000))).Collect(context.Background()))
	h2o := "bench/data/h2o/n10000000/groupby/g1"
	cases := []struct {
		name string
		lf   func() *ursus.LazyFrame
	}{
		{"group-by of 5M keys", func() *ursus.LazyFrame { return df.Lazy().GroupBy(c("k")).Agg(c("x").Sum()) }},
		{"sort", func() *ursus.LazyFrame { return df.Lazy().Sort(ursus.Asc(c("x")), ursus.Asc(c("k"))) }},
		{"join to 5M keys", func() *ursus.LazyFrame { return df.Lazy().Join(dim.Lazy(), ursus.JoinOn(c("k"))) }},
		{"10M rows collected", func() *ursus.LazyFrame { return df.Lazy().Select(c("k"), c("x").Mul(2.0)) }},
		{"Parquet scan, group-by", func() *ursus.LazyFrame {
			return ursus.ScanParquet(h2o + ".parquet").GroupBy(c("id3")).Agg(c("v1").Sum())
		}},
		{"CSV scan, group-by", func() *ursus.LazyFrame {
			return ursus.ScanCSV(h2o + ".csv").GroupBy(c("id3")).Agg(c("v1").Sum())
		}},
	}
	for _, cs := range cases {
		if only := os.Getenv("CASE"); only != "" && !strings.Contains(cs.name, only) {
			continue
		}
		for _, after := range []time.Duration{10 * time.Millisecond, 50 * time.Millisecond} {
			runtime.GC()
			before := runtime.NumGoroutine()
			ctx, stop := context.WithCancel(context.Background())
			lf := cs.lf()
			done := make(chan error, 1)
			go func() { _, err := lf.Collect(ctx); done <- err }()
			time.Sleep(after)
			tc := time.Now()
			stop()
			err := <-done
			back := time.Since(tc)
			time.Sleep(200 * time.Millisecond)
			fmt.Printf("cancel %-24s at %3d ms: returned %6.1f ms after the cancel (cancelled %v), goroutines %d before, %d after\n",
				cs.name, after.Milliseconds(), float64(back.Microseconds())/1e3, err != nil, before, runtime.NumGoroutine())
		}
	}
}

func seq(n int) []int64 {
	v := make([]int64, n)
	for i := range v {
		v[i] = int64(i)
	}
	return v
}

func must(df *ursus.DataFrame, err error) *ursus.DataFrame {
	if err != nil {
		panic(err)
	}
	return df
}

func heap() (live, mapped uint64) {
	s := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}, {Name: "/memory/classes/total:bytes"}}
	metrics.Read(s)
	return s[0].Value.Uint64(), s[1].Value.Uint64()
}

func rss() string {
	b, _ := os.ReadFile("/proc/self/status")
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "VmRSS") {
			return strings.Join(strings.Fields(l)[1:], " ")
		}
	}
	return "?"
}

func mem() {
	df := big()
	runtime.GC()
	live0, mapped0 := heap()
	fmt.Printf("mem the input held: live %d MB, mapped %d MB, RSS %s\n", live0>>20, mapped0>>20, rss())
	out, err := df.Lazy().GroupBy(c("k")).Agg(c("x").Sum().Alias("sum"), c("x").Mean().Alias("mean")).Collect(context.Background())
	if err != nil {
		panic(err)
	}
	_, mappedPeak := heap()
	out = nil
	_ = out
	runtime.GC()
	live1, mapped1 := heap()
	fmt.Printf("mem after a group-by of 5M keys: mapped at its end %d MB; collected: live %d MB, mapped %d MB, RSS %s\n",
		mappedPeak>>20, live1>>20, mapped1>>20, rss())
	debug.FreeOSMemory()
	live2, mapped2 := heap()
	fmt.Printf("mem after FreeOSMemory: live %d MB, mapped %d MB, RSS %s\n", live2>>20, mapped2>>20, rss())
	runtime.KeepAlive(df)
}

func hwm() string {
	b, _ := os.ReadFile("/proc/self/status")
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "VmHWM") {
			return strings.Join(strings.Fields(l)[1:], " ")
		}
	}
	return "?"
}

func shared() {
	n := 4_000_000
	k, x := make([]int64, n), make([]float64, n)
	for i := range n {
		r := uint64(i) * 0x9E3779B97F4A7C15
		k[i], x[i] = int64(r>>20%1_000_000), float64(r>>30%100_000)
	}
	df := must(ursus.Frame(ursus.Values("k", k), ursus.Values("x", x)).Collect(context.Background()))
	k, x = nil, nil
	budget := int64(256)
	fmt.Sscan(os.Getenv("BUDGET"), &budget)
	one := ursus.NewEngine(ursus.WithEngineMemoryLimit(budget << 20))
	runtime.GC()
	fmt.Printf("shared %s: the input held, RSS %s\n", os.Getenv("ARM"), rss())
	var (
		wg     sync.WaitGroup
		spills atomic.Int64
	)
	queries := 16
	fmt.Sscan(os.Getenv("N"), &queries)
	t0 := time.Now()
	for i := range queries {
		e := one
		if os.Getenv("ARM") == "own" {
			e = ursus.NewEngine(ursus.WithEngineMemoryLimit(budget << 20))
		}
		lf := df.Lazy().Sort(ursus.Asc(c("x")), ursus.Asc(c("k")))
		if mix := os.Getenv("MIX"); mix == "agg" || mix == "" && i%2 == 1 {
			lf = df.Lazy().GroupBy(c("k")).Agg(c("x").Sum())
		}
		wg.Go(func() {
			var st ursus.MemoryStats
			for _, err := range lf.CollectBatches(context.Background(), ursus.WithEngine(e), ursus.WithMemoryStats(&st)) {
				if err != nil {
					panic(err)
				}
			}
			spills.Add(st.Spills)
			if os.Getenv("EACH") != "" {
				fmt.Printf("  query %2d: done at %.2f s, its peak %d MiB, %d files\n",
					i, time.Since(t0).Seconds(), st.Peak>>20, st.Spills)
			}
		})
	}
	if os.Getenv("EACH") != "" {
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			for t := time.NewTicker(250 * time.Millisecond); ; {
				select {
				case <-stop:
					return
				case <-t.C:
					s := one.Stats()
					fmt.Printf("  at %.2f s: %d running, holding %d MiB\n", time.Since(t0).Seconds(), s.Running, s.Held>>20)
				}
			}
		}()
	}
	wg.Wait()
	fmt.Printf("shared %s, %d MiB: %.2f s, %d files spilled, peak RSS %s, the shared engine's peak held %d MiB\n",
		os.Getenv("ARM"), budget, time.Since(t0).Seconds(), spills.Load(), hwm(), one.Stats().Peak>>20)
}

func main() {
	switch os.Args[1] {
	case "lat":
		lat()
	case "conc":
		conc()
	case "cancel":
		cancel()
	case "mem":
		mem()
	case "noop":
	case "conc64":
		conc64()
	case "alloc":
		alloc()
	case "shared":
		shared()
	}
}
