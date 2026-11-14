package self

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus/internal/uerr"
	"github.com/advenn/ursus/qcheck/gen"
	"github.com/advenn/ursus/qcheck/model"
)

// smokeSeed and smokeCases are the smoke test's run: about ten seconds on the
// laptop, and every ci.yml run.
const (
	smokeSeed  = 20261114
	smokeCases = 400
)

// TestSelfSmoke runs a fixed stretch of cases, which must find nothing, fire the
// float-threads rule, and cover every operation.
func TestSelfSmoke(t *testing.T) {
	rep := Run(Options{Seed: smokeSeed, Cases: smokeCases})
	t.Log("\n" + rep.Summary())
	for _, f := range rep.Findings {
		t.Error(f)
	}
	if rep.Fired == 0 || rep.CanariesFired == 0 {
		t.Errorf("the float-threads rule fired in %d of %d comparisons and %d of %d canaries",
			rep.Fired, rep.Eligible, rep.CanariesFired, rep.Canaries)
	}
	for _, op := range []string{"Filter", "Select", "WithColumns", "GroupBy", "GroupBy()", "Sort", "Head", "Slice",
		"JoinInner", "JoinLeft", "JoinSemi", "JoinAnti", "Count"} {
		if s := rep.Ops[op]; s == nil || s.Seen == 0 {
			t.Errorf("no query ran %s", op)
		}
	}
}

// teeth runs cases from seed with the hooks set, and returns the report.
func teeth(t *testing.T, cases int, c func(*gen.Case, model.Config, int, *Answer), i func(model.Config) error) *Report {
	t.Helper()
	corrupt, inject = c, i
	defer func() { corrupt, inject = nil, nil }()
	return Run(Options{Seed: smokeSeed, Cases: cases})
}

// kinds counts a report's findings by kind, and the factors each mismatch was
// bisected to.
func kinds(rep *Report) (map[string]int, map[model.Factor]int) {
	k, f := map[string]int{}, map[model.Factor]int{}
	for _, x := range rep.Findings {
		k[x.Kind]++
		if x.Kind == KindMismatch {
			f[x.Factors]++
		}
	}
	return k, f
}

// bites logs what a broken run found, for the record.
func bites(t *testing.T, rep *Report) (map[string]int, map[model.Factor]int) {
	t.Helper()
	k, f := kinds(rep)
	t.Logf("%d cases: findings %v, mismatches bisected to %v", rep.Cases, k, f)
	return k, f
}

// exactCol is the first column of a that bounds nothing and is bounded by
// nothing, or -1.
func exactCol(a *Answer) int {
	for j, name := range a.Names {
		if !strings.Contains(name, "__") && !slices.Contains(a.Names, name+"__abs") {
			return j
		}
	}
	return -1
}

// TestSelfTeeth breaks answers through the test hooks, each in one configuration
// factor, and the self check must find each break and bisect it to that factor.
func TestSelfTeeth(t *testing.T) {
	const cases = 60

	t.Run("a wrong cell on eight threads", func(t *testing.T) {
		rep := teeth(t, cases, func(_ *gen.Case, cfg model.Config, _ int, a *Answer) {
			if j := exactCol(a); cfg.Threads == 8 && a.Err == nil && a.Rows > 0 && j >= 0 {
				a.Cells[j][a.Rows-1] += "!"
			}
		}, nil)
		k, f := bites(t, rep)
		if k[KindMismatch] == 0 || f[model.FThreads] != k[KindMismatch] {
			t.Fatalf("want every finding a mismatch of threads; got %v, factors %v", k, f)
		}
	})

	t.Run("two rows swapped in batches of one row", func(t *testing.T) {
		rep := teeth(t, cases, func(_ *gen.Case, cfg model.Config, _ int, a *Answer) {
			if cfg.Batch != 1 || a.Err != nil || a.Rows < 2 {
				return
			}
			for _, col := range a.Cells {
				col[0], col[1] = col[1], col[0]
			}
		}, nil)
		k, f := bites(t, rep)
		// A swap is no difference where the answer's order is not promised, or its
		// first two rows are equal: some of the cases are found, each by batch.
		if k[KindMismatch] == 0 || f[model.FBatch] != k[KindMismatch] {
			t.Fatalf("want mismatches of the batch size; got %v, factors %v", k, f)
		}
	})

	t.Run("a row lost with the optimizer off", func(t *testing.T) {
		rep := teeth(t, cases, func(_ *gen.Case, cfg model.Config, _ int, a *Answer) {
			if cfg.OptOff && a.Err == nil && a.Rows > 0 {
				a.Rows--
				for j := range a.Cells {
					a.Cells[j] = a.Cells[j][:a.Rows]
				}
			}
		}, nil)
		k, f := bites(t, rep)
		if k[KindMismatch] == 0 || f[model.FOptimizer] != k[KindMismatch] {
			t.Fatalf("want mismatches of the optimizer; got %v, factors %v", k, f)
		}
	})

	t.Run("a count off by one from several records", func(t *testing.T) {
		rep := teeth(t, cases*3, func(_ *gen.Case, cfg model.Config, _ int, a *Answer) {
			if cfg.Records > 1 && a.Err == nil && a.Names == nil {
				a.Count++
			}
		}, nil)
		k, f := bites(t, rep)
		if k[KindMismatch] == 0 || f[model.FRecords] != k[KindMismatch] {
			t.Fatalf("want mismatches of the records; got %v, factors %v", k, f)
		}
	})

	t.Run("a refusal under WithVerify", func(t *testing.T) {
		rep := teeth(t, cases, func(_ *gen.Case, cfg model.Config, _ int, a *Answer) {
			if cfg.Verify && cfg.Threads == 1 && a.Err == nil {
				a.Err = errors.New("refused")
			}
		}, nil)
		k, f := bites(t, rep)
		if k[KindMismatch] == 0 || f[model.FVerify] != k[KindMismatch] {
			t.Fatalf("want mismatches of verify; got %v, factors %v", k, f)
		}
	})

	t.Run("the reference twice, two answers", func(t *testing.T) {
		rep := teeth(t, cases, func(_ *gen.Case, cfg model.Config, run int, a *Answer) {
			if j := exactCol(a); cfg == model.Reference() && run == 1 && a.Err == nil && a.Rows > 0 && j >= 0 {
				a.Cells[j][0] += "!"
			}
		}, nil)
		if k, _ := bites(t, rep); k[KindRerun] == 0 || len(rep.Findings) != k[KindRerun] {
			t.Fatalf("want every finding a rerun; got %v", k)
		}
	})

	t.Run("a noisy cell past its bound on several threads", func(t *testing.T) {
		rep := teeth(t, cases*6, func(_ *gen.Case, cfg model.Config, _ int, a *Answer) {
			noisy(a, cfg.Threads > 1, func(f float64) float64 { return f*(1+1e-6) + 1e-3 })
		}, nil)
		k, f := bites(t, rep)
		if k[KindMismatch] == 0 || f[model.FThreads] != k[KindMismatch] {
			t.Fatalf("want mismatches of threads; got %v, factors %v", k, f)
		}
	})

	t.Run("a noisy cell an ulp off on one thread", func(t *testing.T) {
		// The rule allows the threads alone: an ulp in other batches on one thread
		// is a mismatch.
		rep := teeth(t, cases*6, func(_ *gen.Case, cfg model.Config, _ int, a *Answer) {
			noisy(a, cfg.Threads == 1 && cfg.Batch != 8192, func(f float64) float64 { return math.Nextafter(f, math.Inf(1)) })
		}, nil)
		k, f := bites(t, rep)
		if k[KindMismatch] == 0 || f[model.FBatch] != k[KindMismatch] {
			t.Fatalf("want mismatches of the batch size; got %v, factors %v", k, f)
		}
	})

	t.Run("ErrInternal in batches of three rows", func(t *testing.T) {
		rep := teeth(t, cases, nil, func(cfg model.Config) error {
			if cfg.Batch == 3 {
				return fmt.Errorf("injected: %w", uerr.ErrInternal)
			}
			return nil
		})
		if k, _ := bites(t, rep); k[KindFuzzcheck] == 0 || len(rep.Findings) != k[KindFuzzcheck] {
			t.Fatalf("want every finding fuzzcheck's; got %v", k)
		}
		if !strings.Contains(rep.Findings[0].Detail, "internal error") {
			t.Fatalf("want fuzzcheck's internal error; got %s", rep.Findings[0].Detail)
		}
	})

	t.Run("a panic in batches of two rows", func(t *testing.T) {
		rep := teeth(t, cases, nil, func(cfg model.Config) error {
			if cfg.Batch == 2 {
				panic("injected")
			}
			return nil
		})
		if k, _ := bites(t, rep); k[KindFuzzcheck] == 0 || len(rep.Findings) != k[KindFuzzcheck] {
			t.Fatalf("want every finding fuzzcheck's; got %v", k)
		}
		if !strings.Contains(rep.Findings[0].Detail, "a panic reached the caller") {
			t.Fatalf("want fuzzcheck's escaped panic; got %s", rep.Findings[0].Detail)
		}
	})
}

// noisy applies f to every non-null noisy cell of a, when on.
func noisy(a *Answer, on bool, f func(float64) float64) {
	if !on || a.Err != nil {
		return
	}
	for j, name := range a.Names {
		if !slices.Contains(a.Names, name+"__abs") {
			continue
		}
		for r, c := range a.Cells[j] {
			if c != "" && c != nanCell {
				v := f(float(c))
				a.Cells[j][r] = "v" + string(bitsOf(v))
			}
		}
	}
}

func bitsOf(f float64) []byte {
	b := make([]byte, 8)
	u := math.Float64bits(f)
	for i := range b {
		b[i] = byte(u >> (8 * i))
	}
	return b
}

// TestAStaleRuleAndAVacuousOpFail: the run-wide checks. A rule that had its chances
// and never fired is stale; an operation that answered rows in under a twentieth of
// its runs over input with rows, or was refused every time, is vacuous.
func TestAStaleRuleAndAVacuousOpFail(t *testing.T) {
	r := newReport()
	r.Canaries, r.CanariesFired = MinCanaries, 0
	for range MinSeen {
		r.countOp("Filter", true, true, false)
		r.countOp("Sort", false, false, false)
		r.countOp("Head", true, true, true)
	}
	r.finish()
	k, _ := kinds(r)
	if k[KindStale] != 1 || k[KindVacuous] != 2 {
		t.Fatalf("want one stale rule and two vacuous operations; got %v:\n%s", k, r)
	}

	ok := newReport()
	ok.Canaries, ok.CanariesFired = MinCanaries, 1
	for range MinSeen {
		ok.countOp("Filter", true, true, true)
	}
	ok.finish()
	if ok.Failed() {
		t.Fatalf("a rule that fired and an operation with rows failed:\n%s", ok)
	}
}
