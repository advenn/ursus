package compare

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/advenn/ursus/qcheck/ast"
	"github.com/advenn/ursus/qcheck/model"
)

// FiredNoisy is the comparator's one allowance, as Verdict.Fired names it: a noisy
// cell within its bound, ursus on several threads.
const FiredNoisy = "noisy"

// Plan is how two answers to one query compare.
type Plan struct {
	Ordered bool
	// Bounds are the noisy columns' bounds (ast.Bounded).
	Bounds map[string]ast.Bound
	// Threads is whether ursus ran on several threads: a noisy cell may then differ
	// within its bound. Otherwise it compares exactly.
	Threads bool
}

// NewPlan is the plan of q, a bounded query, whose noisy columns bounds names,
// for ursus run on threads threads.
func NewPlan(q *ast.Query, bounds map[string]ast.Bound, tables map[string]*model.Table, threads int) (Plan, error) {
	info, err := ast.Schema(q.Root, tables)
	if err != nil {
		return Plan{}, err
	}
	return Plan{Ordered: info.Ordered, Bounds: bounds, Threads: threads > 1}, nil
}

// Verdict is one comparison's outcome.
type Verdict struct {
	Diff  string         // "" when the answers agree
	Fired map[string]int // the allowances that fired, by kind
}

const eps = 0x1p-52

// Compare compares ursus's answer, got, against the engine's, want. Neither may be
// a refusal: the runner reports those.
func Compare(want, got *Answer, p Plan) Verdict {
	v := Verdict{Fired: map[string]int{}}
	if want.IsCount != got.IsCount {
		v.Diff = "one answer is a Count and the other a frame"
		return v
	}
	if want.IsCount {
		if want.Count != got.Count {
			v.Diff = fmt.Sprintf("Count: ursus %d, DuckDB %d", got.Count, want.Count)
		}
		return v
	}
	if !slices.Equal(want.Names, got.Names) {
		v.Diff = fmt.Sprintf("columns: ursus %v, DuckDB %v", got.Names, want.Names)
		return v
	}
	if want.Rows != got.Rows {
		v.Diff = fmt.Sprintf("rows: ursus %d, DuckDB %d", got.Rows, want.Rows)
		return v
	}
	var exact, loose []int
	skip := map[string]bool{}
	for _, b := range p.Bounds {
		skip[b.Abs] = true // a bound's own sum is noisy, and only bounds
	}
	for j, name := range want.Names {
		switch {
		case skip[name]:
		case p.Bounds[name] != (ast.Bound{}):
			loose = append(loose, j)
		default:
			exact = append(exact, j)
		}
	}
	wi, gi := identity(want.Rows), identity(got.Rows)
	if !p.Ordered {
		wi, gi = canonicalOrder(want, exact, loose), canonicalOrder(got, exact, loose)
	}
	for r := range want.Rows {
		for _, j := range exact {
			if want.Cells[j][wi[r]] != got.Cells[j][gi[r]] {
				v.Diff = rowDiff(want, got, wi[r], gi[r], j, p.Ordered)
				return v
			}
		}
		for _, j := range loose {
			ok, fired := p.within(want, got, wi[r], gi[r], j)
			if !ok {
				v.Diff = rowDiff(want, got, wi[r], gi[r], j, p.Ordered)
				return v
			}
			if fired != "" {
				v.Fired[fired]++
			}
		}
	}
	return v
}

func identity(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

// canonicalOrder is a's rows sorted by their exact cells, then their loose ones as
// floats: two answers equal as multisets line up row for row.
func canonicalOrder(a *Answer, exact, loose []int) []int {
	idx := identity(a.Rows)
	slices.SortStableFunc(idx, func(x, y int) int {
		for _, j := range exact {
			if c := strings.Compare(a.Cells[j][x], a.Cells[j][y]); c != 0 {
				return c
			}
		}
		for _, j := range loose {
			fx, fy := a.Cells[j][x], a.Cells[j][y]
			if fx == "" || fy == "" {
				if c := strings.Compare(fx, fy); c != 0 {
					return c
				}
				continue
			}
			if c := cmpFloat(Float(fx), Float(fy)); c != 0 {
				return c
			}
		}
		return 0
	})
	return idx
}

func cmpFloat(a, b float64) int {
	switch {
	case math.IsNaN(a) && math.IsNaN(b):
		return 0
	case math.IsNaN(a):
		return 1
	case math.IsNaN(b):
		return -1
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// within is whether a loose cell agrees: exactly, or within its allowance, which
// it names when it fired.
func (p Plan) within(want, got *Answer, wr, gr, j int) (ok bool, fired string) {
	w, g := want.Cells[j][wr], got.Cells[j][gr]
	if w == g {
		return true, ""
	}
	if w == "" || g == "" {
		return false, ""
	}
	fw, fg := Float(w), Float(g)
	name := want.Names[j]
	if !p.Threads {
		return false, ""
	}
	b := p.Bounds[name]
	absW, absG := cell(want, b.Abs, wr), cell(got, b.Abs, gr)
	nW := cell(want, b.N, wr)
	if absW == "" || absG == "" || nW == "" {
		return false, ""
	}
	s := math.Max(Float(absW), Float(absG))
	n, err := strconv.ParseFloat(nW[1:], 64)
	if err != nil {
		return false, ""
	}
	switch {
	case math.IsNaN(s):
		// A NaN among the values: the answer is NaN in any order.
		return math.IsNaN(fw) && math.IsNaN(fg), ""
	case math.IsInf(s, 1) || s > math.MaxFloat64/4:
		// An infinity among the values, or partial sums that can overflow: which
		// infinity, or NaN, depends on the order of the additions.
		return true, FiredNoisy
	case math.IsNaN(fw) || math.IsNaN(fg) || math.IsInf(fw, 0) || math.IsInf(fg, 0):
		return false, ""
	}
	var tol float64
	if b.Op == ast.Sum {
		tol = 2 * n * eps * s
	} else {
		tol = 2*eps*s + 2*ulp(math.Max(math.Abs(fw), math.Abs(fg)))
	}
	if math.Abs(fw-fg) <= tol {
		return true, FiredNoisy
	}
	return false, ""
}

func ulp(x float64) float64 { return math.Nextafter(x, math.Inf(1)) - x }

func cell(a *Answer, name string, r int) string {
	j := slices.Index(a.Names, name)
	if j < 0 {
		return ""
	}
	return a.Cells[j][r]
}

// rowDiff describes a differing row: the column, and both rows.
func rowDiff(want, got *Answer, wr, gr, j int, ordered bool) string {
	how := "row"
	if !ordered {
		how = "row, in the rows' canonical order,"
	}
	return fmt.Sprintf("%s %d differs in column %q: ursus %s, DuckDB %s\n  DuckDB: %s\n  ursus:  %s", how, wr,
		want.Names[j], show(got.Cells[j][gr], got.Types[j]), show(want.Cells[j][wr], want.Types[j]),
		want.Row(wr), got.Row(gr))
}
