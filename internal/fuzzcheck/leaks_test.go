package fuzzcheck

import (
	"os"
	"testing"
)

// TestLeaksCountsOnlyUrsusGoroutines: the stacks a fuzzing run's false leak printed
// (#96's FuzzArith, recorded from CI), the fuzzing engine's workers and its signal
// watcher, hold no leak (step 546). A goroutine running ursus's code is one, and so
// is one running the standard library's that ursus's code started.
func TestLeaksCountsOnlyUrsusGoroutines(t *testing.T) {
	recorded, err := os.ReadFile("testdata/fuzzarith-stacks.txt")
	if err != nil {
		t.Fatal(err)
	}
	if n := leaks(recorded); n != 0 {
		t.Fatalf("the fuzzing engine's goroutines: %d leaks, want 0", n)
	}
	ursusRuns := "goroutine 99 [chan receive]:\ngithub.com/advenn/ursus/internal/exec.(*pool).worker(0x0)\n" +
		"\t/x/pool.go:1 +0x0\ncreated by github.com/advenn/ursus/internal/exec.newPool in goroutine 26360\n" +
		"\t/x/pool.go:2 +0x0\n"
	ursusStarted := "goroutine 100 [IO wait]:\nio.Copy({0x0, 0x0}, {0x0, 0x0})\n\t/go/src/io/io.go:1 +0x0\n" +
		"created by github.com/advenn/ursus/internal/source.open in goroutine 26360\n\t/x/open.go:3 +0x0\n"
	for want, extra := range map[int]string{1: ursusRuns, 2: ursusRuns + "\n" + ursusStarted} {
		stacks := append(append([]byte{}, recorded...), "\n"+extra...)
		if n := leaks(stacks); n != want {
			t.Errorf("%d leaks, want %d", n, want)
		}
	}
}
