package dtype

import (
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"
)

// internedCount is how many payloads the intern table holds.
func internedCount() int {
	internMu.RLock()
	defer internMu.RUnlock()
	return len(interned)
}

// collectInterned runs the collector until the table holds at most want entries,
// or a few seconds pass: a cleanup runs after the cycle that frees its object.
func collectInterned(want int) int {
	deadline := time.Now().Add(5 * time.Second)
	for {
		runtime.GC()
		n := internedCount()
		if n <= want || time.Now().After(deadline) {
			return n
		}
		time.Sleep(time.Millisecond)
	}
}

// TestInternedTypesAreCollected (step 427): the intern table only grew, a payload
// for every struct, list, enum and zone ever built, so a service reading files of
// varied schemas held each one's type forever. A type nobody holds is collected, and
// its entry with it: ten rounds of 10,000 of each, dropped, leave the table as it
// was and the heap within 16 MB of where the first round left it. One that is held
// stays the one payload, == to itself rebuilt.
func TestInternedTypesAreCollected(t *testing.T) {
	before := collectInterned(0)
	kept := Struct(Field{Name: "kept", Type: Enum("a", "b")})
	round := func(r int) {
		for i := range 10_000 {
			s := strconv.Itoa(r*10_000 + i)
			_ = Struct(Field{Name: "f" + s, Type: Int64}, Field{Name: "g", Type: List(Enum(s, "x"))})
			_ = Datetime(Micro, "Zone/"+s)
		}
	}
	heap := func() int64 {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		return int64(ms.HeapAlloc)
	}
	round(0)
	collectInterned(before + 2)
	first := heap()
	for r := 1; r < 10; r++ {
		round(r)
	}
	if n := collectInterned(before + 2); n > before+2 {
		t.Errorf("after 400,000 types dropped: %d entries, want the %d held", n, before+2)
	}
	// The bound leaves room for the table's own map, which Go never shrinks: a round's
	// entries can outnumber the first's, as cleanups run when they run, and its buckets
	// stay. That alone read 2.1 MB once on CI (step 438), against 2 MB. Held for good,
	// the types grew it 118.8 MB, and their keys alone 51.
	if grew := heap() - first; grew > 16<<20 {
		t.Errorf("nine rounds more grew the heap %d bytes, want under 16 MB", grew)
	}
	if again := Struct(Field{Name: "kept", Type: Enum("a", "b")}); again != kept {
		t.Error("a type held across the collection rebuilt as another payload")
	}
	runtime.KeepAlive(kept)
}

// TestInternedTypesRebuiltWhileCollected: goroutines rebuilding the same types while
// the collector frees them always agree on one payload while any of them holds it.
func TestInternedTypesRebuiltWhileCollected(t *testing.T) {
	var wg sync.WaitGroup
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				runtime.GC()
			}
		}
	}()
	for g := range 4 {
		wg.Go(func() {
			for i := range 2_000 {
				s := strconv.Itoa(i % 50)
				a := Struct(Field{Name: s, Type: Enum(s, "y")})
				b := Struct(Field{Name: s, Type: Enum(s, "y")})
				if a != b {
					t.Errorf("goroutine %d, round %d: one struct built twice is two payloads", g, i)
					return
				}
				if a == Struct(Field{Name: s + "z", Type: Enum(s, "y")}) {
					t.Errorf("goroutine %d, round %d: two structs are one payload", g, i)
					return
				}
			}
		})
	}
	wg.Wait()
	close(stop)
}
