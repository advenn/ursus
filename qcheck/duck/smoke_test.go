package duck

import (
	"math"
	"math/big"
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus/qcheck/ast"
	"github.com/advenn/ursus/qcheck/compare"
	"github.com/advenn/ursus/qcheck/gen"
)

// smokeSeed and smokeCases are the smoke run every qcheck-duckdb job affords.
const (
	smokeSeed  = 20261115
	smokeCases = 400
)

// TestDuckSmoke runs a fixed stretch of cases against DuckDB with each generator,
// which must find nothing and cover every operation. Nine in ten of the core
// generator's cases are compared, and three in four of the wide generator's
// (step 539): an X- rule excludes a query DuckDB's dialect is not made to mirror,
// about one in six of them.
func TestDuckSmoke(t *testing.T) {
	d := open(t)
	for _, core := range []bool{false, true} {
		rep := compare.Run(compare.Options{Seed: smokeSeed, Cases: smokeCases, Engine: d, Core: core})
		t.Logf("core %v:\n%s", core, rep.Summary())
		for _, f := range rep.Findings {
			t.Error(f)
		}
		for _, op := range []string{"Filter", "Select", "WithColumns", "GroupBy", "GroupBy()", "Sort", "Head", "Slice",
			"JoinInner", "JoinLeft", "JoinSemi", "JoinAnti", "Count"} {
			if s := rep.Ops[op]; s == nil || s.Seen == 0 {
				t.Errorf("core %v: no query compared ran %s", core, op)
			}
		}
		least := smokeCases * 3 / 4
		if core {
			least = smokeCases * 9 / 10
		}
		if rep.Compared < least {
			t.Errorf("core %v: %d of %d cases compared", core, rep.Compared, rep.Cases)
		}
	}
}

// exactCols are a's columns that compare exactly under p.
func exactCols(a *compare.Answer, p compare.Plan) []int {
	abs := map[string]bool{}
	for _, b := range p.Bounds {
		abs[b.Abs] = true
	}
	var out []int
	for j, name := range a.Names {
		if !abs[name] && p.Bounds[name] == (ast.Bound{}) {
			out = append(out, j)
		}
	}
	return out
}

// firstWhere is the first row and column among cols whose cell holds of ok.
func firstWhere(a *compare.Answer, cols []int, ok func(j int, cell string) bool) (int, int, bool) {
	for _, j := range cols {
		for r := range a.Rows {
			if ok(j, a.Cells[j][r]) {
				return r, j, true
			}
		}
	}
	return 0, 0, false
}

// duckBreaks change DuckDB's answer, or report they do not apply to it.
var duckBreaks = []struct {
	name string
	fn   func(p compare.Plan, a *compare.Answer) bool
}{
	{"the last row lost", func(p compare.Plan, a *compare.Answer) bool {
		if a.IsCount || a.Rows == 0 {
			return false
		}
		a.Rows--
		for j := range a.Cells {
			a.Cells[j] = a.Cells[j][:a.Rows]
		}
		return true
	}},
	{"a value made null", func(p compare.Plan, a *compare.Answer) bool {
		r, j, ok := firstWhere(a, exactCols(a, p), func(j int, c string) bool { return c != "" })
		if ok {
			a.Cells[j][r] = ""
		}
		return ok
	}},
	{"text edited", func(p compare.Plan, a *compare.Answer) bool {
		r, j, ok := firstWhere(a, exactCols(a, p), func(j int, c string) bool { return c != "" && a.Types[j].String() == "String" })
		if ok {
			a.Cells[j][r] += "x"
		}
		return ok
	}},
	{"an integer one more", func(p compare.Plan, a *compare.Answer) bool {
		r, j, ok := firstWhere(a, exactCols(a, p), func(j int, c string) bool {
			return c != "" && (a.Types[j].IsInt() || a.Types[j].String() == "Date")
		})
		if ok {
			v, _ := new(big.Int).SetString(a.Cells[j][r][1:], 10)
			a.Cells[j][r] = "v" + v.Add(v, big.NewInt(1)).String()
		}
		return ok
	}},
	{"a float an ulp off", func(p compare.Plan, a *compare.Answer) bool {
		r, j, ok := firstWhere(a, exactCols(a, p), func(j int, c string) bool {
			return c != "" && a.Types[j].String() == "Float64" && !math.IsNaN(compare.Float(c)) && !math.IsInf(compare.Float(c), 0)
		})
		if ok {
			a.Cells[j][r] = compare.FloatCell(math.Nextafter(compare.Float(a.Cells[j][r]), math.Inf(1)))
		}
		return ok
	}},
	{"two rows of an ordered answer swapped", func(p compare.Plan, a *compare.Answer) bool {
		if !p.Ordered || a.Rows < 2 {
			return false
		}
		exact := exactCols(a, p)
		for r := 1; r < a.Rows; r++ {
			if slices.ContainsFunc(exact, func(j int) bool { return a.Cells[j][0] != a.Cells[j][r] }) {
				for j := range a.Cells {
					a.Cells[j][0], a.Cells[j][r] = a.Cells[j][r], a.Cells[j][0]
				}
				return true
			}
		}
		return false
	}},
	{"a Count one more", func(p compare.Plan, a *compare.Answer) bool {
		if a.IsCount {
			a.Count++
		}
		return a.IsCount
	}},
}

// TestDuckTeeth breaks DuckDB's real answers, each break over the smoke seed's
// cases where it applies, and holds the run to a mismatch in every case broken and
// in no other.
func TestDuckTeeth(t *testing.T) {
	d := open(t)
	for _, br := range duckBreaks {
		t.Run(strings.ReplaceAll(br.name, " ", "_"), func(t *testing.T) {
			applied := map[uint64]bool{}
			rep := compare.Run(compare.Options{Seed: smokeSeed, Cases: 120, Engine: d,
				Corrupt: func(c *gen.Case, a *compare.Answer) {
					q, bounds := ast.Bounded(c.Query)
					p, err := compare.NewPlan(q, bounds, c.TableMap(), compare.ConfigOf(c).Threads)
					if err != nil {
						t.Fatal(err)
					}
					if br.fn(p, a) {
						applied[c.Seed] = true
					}
				}})
			found := map[uint64]bool{}
			for _, f := range rep.Findings {
				if f.Kind == compare.KindMismatch {
					found[f.Seed] = true
				} else if f.Kind != compare.KindVacuous {
					t.Errorf("a %s finding: %v", f.Kind, f)
				}
			}
			t.Logf("applied in %d of %d cases; found %d", len(applied), rep.Cases, len(found))
			if len(applied) == 0 {
				t.Fatal("the break applied to no case")
			}
			for seed := range applied {
				if !found[seed] {
					t.Errorf("seed %d: the break was not found", seed)
				}
			}
			for seed := range found {
				if !applied[seed] {
					t.Errorf("seed %d: a mismatch where nothing was broken", seed)
				}
			}
		})
	}
}

// foundSeeds are the cases whose findings the record lists (step-513-as-built.md
// §6), each run alone: every one must now agree.
var foundSeeds = []uint64{
	804000458,                                             // an Int8 FloorDiv by 243: gen's literal outside its type
	804000121,                                             // TRY_CAST's null dropped in a comparison
	804002697, 804006513, 804011153, 804013588, 804018440, // an integer Mean rounded twice
}

// TestDuckFound runs the cases the qcheck-duckdb job found, each alone. Each was
// found before step 539, so its case is the core generator's.
func TestDuckFound(t *testing.T) {
	d := open(t)
	for _, seed := range foundSeeds {
		rep := compare.Run(compare.Options{Seed: seed, Cases: 1, Engine: d, Core: true})
		for _, f := range rep.Findings {
			if f.Kind != compare.KindVacuous {
				t.Error(f)
			}
		}
		if rep.Compared != 1 {
			t.Errorf("seed %d: not compared", seed)
		}
	}
}
