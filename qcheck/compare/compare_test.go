package compare

import (
	"errors"
	"math"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"

	"github.com/advenn/ursus/internal/uerr"
	"github.com/advenn/ursus/qcheck/ast"
	"github.com/advenn/ursus/qcheck/gen"
	"github.com/advenn/ursus/qcheck/model"
)

// teethSeed is where the local runs start: the self check's smoke seed.
const teethSeed = 20261114

// run runs cases against ursusEngine with the hooks set.
func run(t *testing.T, cases int, c func(*gen.Case, *Answer), i func(model.Config) error) *Report {
	t.Helper()
	return Run(Options{Seed: teethSeed, Cases: cases, Engine: &ursusEngine{}, Corrupt: c, Inject: i})
}

func kinds(rep *Report) map[string]int {
	k := map[string]int{}
	for _, f := range rep.Findings {
		k[f.Kind]++
	}
	return k
}

// TestRunAgainstUrsus runs the whole loop, sqlgen's translation, ursus under
// fuzzcheck in its configuration, FromArrow and the comparator, against ursus at the
// reference through DuckDB's Arrow types: it must find nothing, so a finding against
// DuckDB is never the comparator's own; and the noisy allowance must fire, as ursus
// runs on several threads in some cases.
func TestRunAgainstUrsus(t *testing.T) {
	rep := run(t, 1000, nil, nil)
	t.Log("\n" + rep.Summary())
	for _, f := range rep.Findings {
		if f.Kind != KindVacuous {
			t.Error(f)
		}
	}
	if rep.Compared < 100 {
		t.Errorf("only %d of %d cases compared", rep.Compared, rep.Cases)
	}
	if rep.Allowed[FiredNoisy] == 0 {
		t.Errorf("the noisy allowance never fired: %v", rep.Allowed)
	}
}

// planOf is the plan the runner compares case c under.
func planOf(c *gen.Case) Plan {
	q, bounds := ast.Bounded(c.Query)
	p, err := NewPlan(q, bounds, c.TableMap(), ConfigOf(c).Threads)
	if err != nil {
		panic(err)
	}
	return p
}

// columns are a's columns of the kind: "exact" (neither noisy nor a bound) or
// "noisy".
func columns(a *Answer, p Plan, kind string) []int {
	abs := map[string]bool{}
	for _, b := range p.Bounds {
		abs[b.Abs] = true
	}
	var out []int
	for j, name := range a.Names {
		noisy := p.Bounds[name] != (ast.Bound{})
		var k string
		switch {
		case abs[name]:
			continue
		case noisy:
			k = "noisy"
		default:
			k = "exact"
		}
		if k == kind {
			out = append(out, j)
		}
	}
	return out
}

// firstCell is the first row and column among cols whose cell holds of it.
func firstCell(a *Answer, cols []int, ok func(t model.Type, cell string) bool) (int, int, bool) {
	for _, j := range cols {
		for r := range a.Rows {
			if ok(a.Types[j], a.Cells[j][r]) {
				return r, j, true
			}
		}
	}
	return 0, 0, false
}

func isFloat(t model.Type, c string) bool { return t == model.Float64 && c != "" }

// A break changes an engine's answer, or reports it does not apply to it.
type break_ struct {
	name string
	fn   func(c *gen.Case, p Plan, a *Answer) bool
}

var breaks = []break_{
	{name: "the last row lost", fn: func(c *gen.Case, p Plan, a *Answer) bool {
		if a.IsCount || a.Rows == 0 {
			return false
		}
		a.Rows--
		for j := range a.Cells {
			a.Cells[j] = a.Cells[j][:a.Rows]
		}
		return true
	}},
	{name: "a value made null", fn: func(c *gen.Case, p Plan, a *Answer) bool {
		r, j, ok := firstCell(a, columns(a, p, "exact"), func(t model.Type, c string) bool { return c != "" })
		if ok {
			a.Cells[j][r] = ""
		}
		return ok
	}},
	{name: "a null made a value", fn: func(c *gen.Case, p Plan, a *Answer) bool {
		r, j, ok := firstCell(a, columns(a, p, "exact"), func(t model.Type, c string) bool { return c == "" })
		if !ok {
			return false
		}
		a.Cells[j][r] = map[model.Type]string{model.Bool: "v1", model.Float64: FloatCell(0.25),
			model.String: "vnull"}[a.Types[j]]
		if a.Cells[j][r] == "" {
			a.Cells[j][r] = "v7"
		}
		return true
	}},
	{name: "two rows of an ordered answer swapped", fn: func(c *gen.Case, p Plan, a *Answer) bool {
		if !p.Ordered || a.Rows < 2 {
			return false
		}
		exact := columns(a, p, "exact")
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
	{name: "a float an ulp off", fn: func(c *gen.Case, p Plan, a *Answer) bool {
		r, j, ok := firstCell(a, columns(a, p, "exact"), func(t model.Type, c string) bool {
			return isFloat(t, c) && !math.IsNaN(Float(c)) && !math.IsInf(Float(c), 0)
		})
		if ok {
			f := Float(a.Cells[j][r])
			a.Cells[j][r] = FloatCell(math.Nextafter(f, math.Inf(1)))
		}
		return ok
	}},
	{name: "-0 made +0", fn: func(c *gen.Case, p Plan, a *Answer) bool {
		r, j, ok := firstCell(a, columns(a, p, "exact"), func(t model.Type, c string) bool {
			return isFloat(t, c) && Float(c) == 0
		})
		if ok {
			a.Cells[j][r] = FloatCell(math.Copysign(0, -Float(a.Cells[j][r])))
		}
		return ok
	}},
	{name: "NaN made a number", fn: func(c *gen.Case, p Plan, a *Answer) bool {
		r, j, ok := firstCell(a, columns(a, p, "exact"), func(t model.Type, c string) bool { return c == nanCell })
		if ok {
			a.Cells[j][r] = FloatCell(math.Inf(1))
		}
		return ok
	}},
	{name: "text edited", fn: func(c *gen.Case, p Plan, a *Answer) bool {
		r, j, ok := firstCell(a, columns(a, p, "exact"), func(t model.Type, c string) bool {
			return t == model.String && c != ""
		})
		if ok {
			a.Cells[j][r] += "x"
		}
		return ok
	}},
	{name: "an integer one more", fn: func(c *gen.Case, p Plan, a *Answer) bool {
		r, j, ok := firstCell(a, columns(a, p, "exact"), func(t model.Type, c string) bool {
			return (t.IsInt() || t == model.Date) && c != ""
		})
		if ok {
			v, _ := new(big.Int).SetString(a.Cells[j][r][1:], 10)
			a.Cells[j][r] = "v" + v.Add(v, big.NewInt(1)).String()
		}
		return ok
	}},
	{name: "a Count one more", fn: func(c *gen.Case, p Plan, a *Answer) bool {
		if a.IsCount {
			a.Count++
		}
		return a.IsCount
	}},
	{name: "a noisy cell 1e-6 relative + 1e-3 off", fn: func(c *gen.Case, p Plan, a *Answer) bool {
		r, j, ok := firstCell(a, columns(a, p, "noisy"), func(t model.Type, c string) bool {
			return c != "" && !math.IsNaN(Float(c)) && !math.IsInf(Float(c), 0) && math.Abs(Float(c)) < 1e300
		})
		if !ok {
			return false
		}
		b := p.Bounds[a.Names[j]]
		s := Float(cell(a, b.Abs, r))
		nc := cell(a, b.N, r)
		if nc == "" {
			return false
		}
		n, err := strconv.ParseFloat(nc[1:], 64)
		if err != nil || math.IsInf(s, 0) || math.IsNaN(s) || s > math.MaxFloat64/4 {
			return false
		}
		f := Float(a.Cells[j][r])
		tol := 2 * n * eps * s // a Sum's; a Mean's is less
		if b.Op == ast.Mean {
			tol = 2*eps*s + 2*ulp(math.Abs(f)*2)
		}
		if math.Abs(f)*1e-6+1e-3 <= 2*tol {
			return false // within the bound, as it should be
		}
		a.Cells[j][r] = FloatCell(f*(1+1e-6) + 1e-3)
		return true
	}},
	{name: "a noisy cell an ulp off, ursus on one thread", fn: func(c *gen.Case, p Plan, a *Answer) bool {
		if p.Threads {
			return false
		}
		r, j, ok := firstCell(a, columns(a, p, "noisy"), func(t model.Type, c string) bool {
			return c != "" && !math.IsNaN(Float(c)) && !math.IsInf(Float(c), 0)
		})
		if ok {
			a.Cells[j][r] = FloatCell(math.Nextafter(Float(a.Cells[j][r]), math.Inf(1)))
		}
		return ok
	}},
}

// TestComparatorTeeth breaks the engine's answers, each break over the run's
// cases where it applies, and holds the comparator to finding every one, a
// mismatch in that case and in no other.
func TestComparatorTeeth(t *testing.T) {
	for _, br := range breaks {
		t.Run(strings.ReplaceAll(br.name, " ", "_"), func(t *testing.T) {
			applied := map[uint64]bool{}
			rep := run(t, 150, func(c *gen.Case, a *Answer) {
				if br.fn(c, planOf(c), a) {
					applied[c.Seed] = true
				}
			}, nil)
			found := map[uint64]bool{}
			for _, f := range rep.Findings {
				if f.Kind == KindMismatch {
					found[f.Seed] = true
				} else if f.Kind != KindVacuous {
					t.Errorf("a %s finding: %v", f.Kind, f)
				}
			}
			t.Logf("applied in %d of %d cases; found %d; allowances %v", len(applied), rep.Cases, len(found), rep.Allowed)
			if len(applied) == 0 {
				t.Fatalf("the break applied to no case")
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

// TestRunTeeth holds the runner to failing a case whose ursus run returns
// ErrInternal or panics, whatever it answered: every ursus run is inside
// fuzzcheck.Run.
func TestRunTeeth(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  func() error
		want string
	}{
		{"ErrInternal", func() error { return uerr.Internalf("a broken invariant") }, "internal"},
		{"a panic", func() error { panic(errors.New("a bug")) }, "panic"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hit := 0
			rep := run(t, 40, nil, func(cfg model.Config) error {
				if cfg.Batch == 7 || cfg.Batch == 3 {
					hit++
					return tc.err()
				}
				return nil
			})
			k := kinds(rep)
			t.Logf("injected %d times; findings %v", hit, k)
			if hit == 0 || k[KindFuzzcheck] != hit {
				t.Fatalf("injected %d times, %d fuzzcheck findings", hit, k[KindFuzzcheck])
			}
			for _, f := range rep.Findings {
				if f.Kind == KindFuzzcheck && !strings.Contains(strings.ToLower(f.Detail), tc.want) {
					t.Errorf("a fuzzcheck finding that does not say %q: %s", tc.want, f.Detail)
				}
			}
		})
	}
}

// TestFromArrowReadsWhatItCanHoldExactly reads every type from each Arrow type
// DuckDB may export it as, the cells of generated tables sent through toArrow,
// and holds them to FromUrsus's cells of the same values.
func TestFromArrowReadsWhatItCanHoldExactly(t *testing.T) {
	for seed := uint64(1); seed <= 40; seed++ {
		c := gen.NewCase(seed)
		e := &ursusEngine{}
		_ = e.Load(c.Tables)
		q := &ast.Query{Root: &ast.Scan{Table: c.Tables[0].Name}}
		schema, recs, err := e.Answer(t.Context(), q, nil)
		if err != nil {
			t.Fatal(err)
		}
		info, _ := ast.Schema(q.Root, c.TableMap())
		a, err := FromArrow(schema, recs, info.Cols)
		if err != nil {
			t.Fatal(err)
		}
		// The table read back directly, by its own Arrow types.
		direct, err := FromArrow(c.Tables[0].Rec.Schema(), []arrow.RecordBatch{c.Tables[0].Rec}, info.Cols)
		if err != nil {
			t.Fatal(err)
		}
		if v := Compare(direct, a, Plan{Ordered: true}); v.Diff != "" {
			t.Fatalf("seed %d: %s", seed, v.Diff)
		}
	}
}
