package model

import "testing"

// TestPairwiseCoversEveryPair: every pair of levels of any two variant factors is in
// some row of the covering array, every row has WithVerify on, and none is the
// reference.
func TestPairwiseCoversEveryPair(t *testing.T) {
	rows := Pairwise()
	levels := func(c Config) [4]int {
		var out [4]int
		for i, v := range ThreadLevels {
			if v == c.Threads {
				out[0] = i
			}
		}
		for i, v := range BatchLevels {
			if v == c.Batch {
				out[1] = i
			}
		}
		for i, v := range OptLevels {
			if v == c.OptOff {
				out[2] = i
			}
		}
		for i, v := range RecordsLevels {
			if v == c.Records {
				out[3] = i
			}
		}
		return out
	}
	sizes := [4]int{len(ThreadLevels), len(BatchLevels), len(OptLevels), len(RecordsLevels)}
	covered := map[[4]int]bool{}
	for _, r := range rows {
		if !r.Verify || r == Reference() {
			t.Fatalf("row %v: every variant runs WithVerify and differs from the reference", r)
		}
		l := levels(r)
		for a := range 4 {
			for b := a + 1; b < 4; b++ {
				covered[[4]int{a, l[a], b, l[b]}] = true
			}
		}
	}
	for a := range 4 {
		for b := a + 1; b < 4; b++ {
			for la := range sizes[a] {
				for lb := range sizes[b] {
					if !covered[[4]int{a, la, b, lb}] {
						t.Errorf("factors %d and %d: levels %d and %d are in no row", a, b, la, lb)
					}
				}
			}
		}
	}
}

// TestBisectionTakesFactorsOneByOne: With takes only the factors named, and Diff
// names each that differs.
func TestBisectionTakesFactorsOneByOne(t *testing.T) {
	ref := Reference()
	v := Config{Threads: 8, Batch: 3, OptOff: true, Verify: true, Records: 4}
	if d := v.Diff(ref); d != FThreads|FBatch|FOptimizer|FVerify|FRecords {
		t.Fatalf("Diff is %v", d)
	}
	for _, f := range Factors {
		got := ref.With(v, f)
		if got.Diff(ref) != f || got.Diff(v) != v.Diff(ref)&^f {
			t.Errorf("%v: With gives %v", f, got)
		}
	}
}
