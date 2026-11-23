//go:build !ursus07

package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/advenn/ursus"
)

// The modes that need 0.8's Engine. Built with -tags ursus07, engine07.go stands in for
// this file, so the probe's other modes build against a 0.7 tree and time it: bench.yml's
// probe job does that for its baseline (step 536).

func engineStats() string { return fmt.Sprintf("%+v", ursus.DefaultEngine().Stats()) }

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
