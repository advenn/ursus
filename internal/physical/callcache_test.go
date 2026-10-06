package physical_test

// A compiled call lives as long as the expression it was compiled from, not as long
// as the process.
//
// callCache memoises each Call node's compiled form — a regex, an is_in probe set,
// a list.contains needle — keyed by the node's pointer. Nothing ever removed an
// entry, and an entry's key kept its node alive, so a long-running service grew the
// cache by every Call of every query it ran: the plan is rebuilt per Collect, with
// new nodes, so even one query repeated grows it.

import (
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/physical"
)

// settledCallCacheLen collects garbage until the cache stops shrinking, and returns
// its size. Cleanups run on their own goroutine after a collection, so one GC is
// not enough to see them.
func settledCallCacheLen() int {
	n := physical.CallCacheLen()
	for range 20 {
		runtime.GC()
		time.Sleep(5 * time.Millisecond)
		m := physical.CallCacheLen()
		if m == n && m == 0 {
			break
		}
		n = m
	}
	return n
}

func TestCompiledCallsDoNotOutliveTheirQuery(t *testing.T) {
	const runs = 200
	frame := func() *ursus.LazyFrame {
		return ursus.Frame(ursus.Values("s", []string{"apple", "pear", "fig"}),
			ursus.Values("id", []int64{1, 2, 3}))
	}
	query := func(i int) *ursus.LazyFrame {
		return frame().Filter(ursus.Col("s").Str().Contains(fmt.Sprintf("p|%d", i), false).
			Or(ursus.Col("id").IsIn(int64(100+i), int64(2))))
	}
	collect := func(t *testing.T, lf *ursus.LazyFrame) {
		t.Helper()
		df, err := lf.Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if df.Height() != 2 {
			t.Fatalf("%d rows, want 2", df.Height())
		}
	}

	cases := []struct {
		name string
		run  func(*testing.T)
	}{
		{"a new query each time", func(t *testing.T) {
			for i := range runs {
				collect(t, query(i))
			}
		}},
		{"one query, collected again and again", func(t *testing.T) {
			lf := query(7)
			for range runs {
				collect(t, lf)
			}
		}},
	}
	// The cache still caches: a query that is held keeps its compiled calls, so a
	// fix that simply stopped storing would fail here.
	t.Run("a held query keeps its compiled calls", func(t *testing.T) {
		before := settledCallCacheLen()
		lf := query(9)
		collect(t, lf)
		if held := settledCallCacheLen() - before; held < 2 {
			t.Errorf("a held query's regex and probe set: %d entries cached, want 2", held)
		}
		runtime.KeepAlive(lf)
	})

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := settledCallCacheLen()
			tc.run(t)
			if grown := settledCallCacheLen() - before; grown > runs/10 {
				t.Errorf("the call cache grew by %d entries over %d queries that are gone", grown, runs)
			}
		})
	}
}

// TestAFailedCallLeavesNoEntry: a call whose compilation fails — here a pattern RE2
// rejects, which only compileCall checks — caches its error like any other result,
// and the entry goes when the query does.
func TestAFailedCallLeavesNoEntry(t *testing.T) {
	const runs = 100
	before := settledCallCacheLen()
	for i := range runs {
		_, err := ursus.Frame(ursus.Values("s", []string{"apple"})).
			Filter(ursus.Col("s").Str().Contains(fmt.Sprintf("(%d", i), false)).
			Collect(t.Context())
		if err == nil {
			t.Fatal("an unbalanced pattern was accepted")
		}
	}
	if grown := settledCallCacheLen() - before; grown > runs/10 {
		t.Errorf("the call cache grew by %d entries over %d failed queries", grown, runs)
	}
}
