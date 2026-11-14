package model

import (
	"fmt"
	"strings"
)

// Config is how a query runs: the factors step 454 varies. Every run, the reference
// and each variant, is under WithMemoryLimit(0): the default budget's group-by
// switches to serial at a batch that scheduling picks (WithMemoryLimit's doc), so it
// is never compared bit-exact twice, and a limit of the caller's own makes every
// group-by serial (aggmerge.go's aggWorkers), which would hide the threads.
type Config struct {
	Threads int  // WithThreads
	Batch   int  // WithBatchSize
	OptOff  bool // WithOptFlags(NoOptFlags()); otherwise every rule, as by default
	Verify  bool // WithVerify: the optimizer asserts each rule kept the schema
	Records int  // each table handed to ScanArrowRecords as this many records
}

// Reference is the configuration every variant is compared against: one thread,
// batches of 8192 rows, default flags, each table one record.
func Reference() Config { return Config{Threads: 1, Batch: 8192, Records: 1} }

// The levels each factor of a variant takes (step 454).
var (
	ThreadLevels  = []int{1, 4, 8}
	BatchLevels   = []int{1, 2, 3, 7, 64, 1024, 65536}
	OptLevels     = []bool{false, true} // optimizer on, off
	RecordsLevels = []int{1, 0}         // 1 record, or k: 0 means 2 to 5, drawn per case
)

// Factor names one way a variant differs from the reference.
type Factor uint8

const (
	FThreads Factor = 1 << iota
	FBatch
	FOptimizer
	FVerify
	FRecords
)

// Factors in the order bisection tries them.
var Factors = []Factor{FThreads, FBatch, FOptimizer, FVerify, FRecords}

func (f Factor) String() string {
	var parts []string
	for _, one := range Factors {
		if f&one == 0 {
			continue
		}
		switch one {
		case FThreads:
			parts = append(parts, "threads")
		case FBatch:
			parts = append(parts, "batch")
		case FOptimizer:
			parts = append(parts, "optimizer")
		case FVerify:
			parts = append(parts, "verify")
		case FRecords:
			parts = append(parts, "records")
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, "+")
}

// Diff is the factors in which c differs from o.
func (c Config) Diff(o Config) Factor {
	var f Factor
	if c.Threads != o.Threads {
		f |= FThreads
	}
	if c.Batch != o.Batch {
		f |= FBatch
	}
	if c.OptOff != o.OptOff {
		f |= FOptimizer
	}
	if c.Verify != o.Verify {
		f |= FVerify
	}
	if c.Records != o.Records {
		f |= FRecords
	}
	return f
}

// With is c with the factors in f taken from o.
func (c Config) With(o Config, f Factor) Config {
	if f&FThreads != 0 {
		c.Threads = o.Threads
	}
	if f&FBatch != 0 {
		c.Batch = o.Batch
	}
	if f&FOptimizer != 0 {
		c.OptOff = o.OptOff
	}
	if f&FVerify != 0 {
		c.Verify = o.Verify
	}
	if f&FRecords != 0 {
		c.Records = o.Records
	}
	return c
}

func (c Config) String() string {
	opt := "optimizer on"
	if c.OptOff {
		opt = "optimizer off"
	}
	if c.Verify {
		opt += "+verify"
	}
	return fmt.Sprintf("{threads %d, batch %d, %s, %d record(s)}", c.Threads, c.Batch, opt, c.Records)
}

// Pairwise is a covering array of the variant factors: every pair of levels of any
// two factors (threads × batch, threads × optimizer, … records × optimizer) is in
// some row. Each case runs a few consecutive rows, so a run covers every pair after
// len(Pairwise())/rows-per-case cases. Each row keeps WithVerify on, as the
// optimizer's variants run with it; Records 0 is drawn per case as 2 to 5.
func Pairwise() []Config {
	type lv struct{ t, b, o, r int }
	nt, nb, no, nr := len(ThreadLevels), len(BatchLevels), len(OptLevels), len(RecordsLevels)
	// Greedy: start from threads × batch, the largest pair, and pick the optimizer
	// and records levels of each row to cover the most pairs not yet covered.
	covered := map[[4]int]bool{} // {factor a, level a, factor b, level b}, a < b
	key := func(fa, la, fb, lb int) [4]int { return [4]int{fa, la, fb, lb} }
	var rows []lv
	for t := range nt {
		for b := range nb {
			best, bestGain := lv{t, b, 0, 0}, -1
			for o := range no {
				for r := range nr {
					gain := 0
					for _, k := range [][4]int{key(0, t, 2, o), key(0, t, 3, r), key(1, b, 2, o),
						key(1, b, 3, r), key(2, o, 3, r)} {
						if !covered[k] {
							gain++
						}
					}
					if gain > bestGain {
						best, bestGain = lv{t, b, o, r}, gain
					}
				}
			}
			rows = append(rows, best)
			for _, k := range [][4]int{key(0, best.t, 1, best.b), key(0, best.t, 2, best.o),
				key(0, best.t, 3, best.r), key(1, best.b, 2, best.o), key(1, best.b, 3, best.r),
				key(2, best.o, 3, best.r)} {
				covered[k] = true
			}
		}
	}
	out := make([]Config, len(rows))
	for i, r := range rows {
		out[i] = Config{Threads: ThreadLevels[r.t], Batch: BatchLevels[r.b], OptOff: OptLevels[r.o],
			Verify: true, Records: RecordsLevels[r.r]}
	}
	return out
}
