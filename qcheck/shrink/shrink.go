package shrink

import (
	"fmt"
	"math"
	"math/bits"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/advenn/ursus/internal/fuzzcheck"
	"github.com/advenn/ursus/qcheck/ast"
	"github.com/advenn/ursus/qcheck/compare"
	"github.com/advenn/ursus/qcheck/model"
)

// The budget of a shrink: plan step 455's.
const (
	DefaultRuns = 2000
	DefaultTime = 120 * time.Second
)

// Options are a shrink's.
type Options struct {
	// MaxRuns bounds the candidates run: each runs its query in the case's
	// configurations, the reference and the failing one in ModeDifferential. 0 is
	// DefaultRuns.
	MaxRuns int
	// MaxTime bounds the time: no candidate starts after it. 0 is DefaultTime.
	MaxTime time.Duration
	// Engine is DuckDB, for a ModeDuck case.
	Engine compare.Engine
	// Limits bound each query, as the finders' do: a Wall of a minute when 0.
	Limits fuzzcheck.Limits
	// Log, if set, is told of each step kept.
	Log func(format string, args ...any)
}

// Result is a shrink's outcome.
type Result struct {
	Case *Case
	// Detail is how the shrunk case fails: the comparison's first difference, or
	// what fuzzcheck failed.
	Detail string
	// Steps are the steps each pass kept.
	Steps map[string]int
	// Runs are the candidates run, Queries the queries they ran.
	Runs, Queries int
	Elapsed       time.Duration
	// Spent is whether the budget ran out before the passes did.
	Spent bool
	// Reproduced is how many of three runs of the shrunk case failed alike.
	Reproduced int
}

// ErrPasses is a case that does not fail: there is nothing to shrink.
var ErrPasses = fmt.Errorf("the case does not fail")

// Shrink shrinks c to the smallest case that fails with its signature, or with the
// signature its first run learns where c has none. c is not changed.
func Shrink(c *Case, opts Options) (*Result, error) {
	if opts.MaxRuns <= 0 {
		opts.MaxRuns = DefaultRuns
	}
	if opts.MaxTime <= 0 {
		opts.MaxTime = DefaultTime
	}
	if c.Signature.Kind == KindHang {
		return nil, fmt.Errorf("a hang ends the process (fuzzcheck's watchdog), so it is not shrunk in one")
	}
	sh := &shrinker{opts: opts, k: newChecker(c.Mode, opts.Engine, opts.Limits), start: time.Now(),
		memo: map[string]outcome{}, ids: map[*Table]int{}, steps: map[string]int{}}
	ref := c.Reference
	if ref == (model.Config{}) {
		ref = model.Reference()
	}
	st := &state{tables: c.Tables, q: c.Query, ref: ref, cfg: c.Config}
	tables, err := st.tableMap()
	if err != nil {
		return nil, err
	}
	sh.k.joinBound = max(maxJoinRows, joinMatches(st.q, tables))
	first := sh.k.check(st)
	sh.runs++
	switch {
	case first.invalid != "":
		return nil, fmt.Errorf("the case is not one: %s", first.invalid)
	case first.sig.Zero():
		return nil, ErrPasses
	case !c.Signature.Zero() && first.sig != c.Signature:
		return nil, fmt.Errorf("the case fails as %v; its file says %v", first.sig, c.Signature)
	case first.sig.Kind == KindHang:
		return nil, fmt.Errorf("a hang is not shrunk")
	}
	sh.sig, sh.detail = first.sig, first.detail
	sh.memo[sh.key(st)] = first
	from := c.Size()

	passes := []struct {
		name string
		fn   func(**state) bool
	}{
		{"factors", sh.passFactors},
		{"rows", sh.passRows},
		{"ops", sh.passOps},
		{"parts", sh.passParts},
		{"exprs", sh.passExprs},
		{"columns", sh.passColumns},
		{"values", sh.passValues},
	}
	for !sh.spent() {
		progress := false
		for _, p := range passes {
			if p.fn(&st) {
				progress = true
			}
			if sh.spent() {
				break
			}
		}
		if !progress {
			break
		}
	}

	out := &Case{Source: c.Source, Mode: c.Mode, Seed: c.Seed, Open: c.Open, Note: c.Note, Signature: sh.sig,
		Reference: st.ref, Config: st.cfg, Query: st.q}
	if out.Mode != ModeDifferential {
		out.Reference = model.Reference()
	}
	for _, t := range st.tables {
		out.Tables = append(out.Tables, t)
	}
	res := &Result{Case: out, Detail: sh.detail, Steps: sh.steps, Runs: sh.runs, Queries: sh.k.queries,
		Spent: sh.spent()}
	// Three runs more, uncounted: a shrunk case that fails only sometimes is said so.
	for range 3 {
		if o := sh.k.check(st); o.sig == sh.sig {
			res.Reproduced++
		}
	}
	res.Elapsed = time.Since(sh.start)
	out.Shrunk = &Provenance{From: from, To: out.Size(), Runs: res.Runs, Seconds: round(res.Elapsed.Seconds()),
		Spent: res.Spent}
	return res, nil
}

func round(s float64) float64 { return math.Round(s*10) / 10 }

// shrinker is one shrink's state.
type shrinker struct {
	opts   Options
	k      *checker
	start  time.Time
	sig    Signature
	detail string
	runs   int
	memo   map[string]outcome
	ids    map[*Table]int
	steps  map[string]int
}

func (sh *shrinker) spent() bool {
	return sh.runs >= sh.opts.MaxRuns || time.Since(sh.start) >= sh.opts.MaxTime
}

// key names a candidate for the memo: its query's Go, its tables, its
// configurations.
func (sh *shrinker) key(s *state) string {
	var b strings.Builder
	b.WriteString(s.q.Go())
	for _, t := range s.tables {
		id, ok := sh.ids[t]
		if !ok {
			id = len(sh.ids) + 1
			sh.ids[t] = id
		}
		b.WriteString("|" + strconv.Itoa(id))
	}
	fmt.Fprintf(&b, "|%v|%v", s.ref, s.cfg)
	return b.String()
}

// try runs a candidate, and keeps it as *cur where it costs less and fails with the
// case's signature.
func (sh *shrinker) try(cur **state, cand *state, pass string) bool {
	if !less(cost(cand), cost(*cur)) || sh.spent() {
		return false
	}
	key := sh.key(cand)
	o, ok := sh.memo[key]
	if !ok {
		o = sh.k.check(cand)
		if o.invalid == "" {
			sh.runs++
		}
		sh.memo[key] = o
	}
	if o.invalid != "" || o.sig != sh.sig {
		return false
	}
	*cur = cand
	sh.detail = o.detail
	sh.steps[pass]++
	if sh.opts.Log != nil {
		sh.opts.Log("shrink: %s kept: %v", pass, sizeOf(cand))
	}
	return true
}

func sizeOf(s *state) Size {
	c := &Case{Tables: s.tables, Query: s.q, Config: s.cfg}
	return c.Size()
}

// costT is a case's cost, compared in order: a step is kept only where it lowers
// it, which ends the passes.
type costT [10]int

func less(a, b costT) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// cost is s's cost: its operations, its expression nodes, its tables, rows and
// columns, its configuration's factors and their levels, the columns its query
// reads, its literals' and its values' complexity.
func cost(s *state) costT {
	var c costT
	reads := map[string]bool{}
	ast.Walk(s.q.Root, func(f ast.Frame) {
		if _, ok := f.(*ast.Scan); !ok {
			c[0]++
		}
		for _, e := range slotExprs(f) {
			c[1] += exprNodes(e)
			c[8] += litCost(e)
			for _, name := range ast.Reads(e) {
				reads[name] = true
			}
		}
	})
	c[2] = len(s.tables)
	for _, t := range s.tables {
		c[3] += t.Rows
		c[4] += len(t.Cols)
		c[9] += tableCost(t)
	}
	c[5] = factorCount(s.cfg.Diff(model.Reference()))
	c[6] = s.cfg.Threads + s.cfg.Records
	c[7] = len(reads)
	return c
}

// exprNodes counts e's nodes.
func exprNodes(e ast.Expr) int {
	n := 1
	for _, k := range e.Kids() {
		n += exprNodes(k)
	}
	return n
}

func litCost(e ast.Expr) int {
	n := 0
	if l, ok := e.(*ast.Lit); ok {
		n += valueCost(l.T, litValue(l))
	}
	for _, k := range e.Kids() {
		n += litCost(k)
	}
	return n
}

// tableCost is a table's values' complexity: each value's, and each column's
// distinct values. A generated table costs as its rows would.
func tableCost(t *Table) int {
	if t.Generated() {
		// Not yet held: as costly as any held table of as many rows, and more.
		return math.MaxInt32
	}
	n := 0
	for _, c := range t.Cols {
		distinct := map[Value]bool{}
		for _, v := range c.Values {
			n += valueCost(c.Type, v)
			if !v.Null {
				distinct[v] = true
			}
		}
		n += len(distinct)
	}
	return n
}

// valueCost is how complex a value is: 0 for a null, a zero, false and empty
// text; more for more bits, a sign, more text.
func valueCost(t model.Type, v Value) int {
	if v.Null {
		return 0
	}
	if !slices.Contains(model.Base, t) && t != model.Int8 && t != model.Int16 {
		return wideCost(t, v)
	}
	switch t {
	case model.Int8, model.Int16, model.Int32, model.Int64, model.Date:
		return intCost(v.I)
	case model.Float64:
		f := v.F
		switch {
		case f == 0 && !math.Signbit(f):
			return 0
		case f == 0:
			return 1
		case math.IsNaN(f) || math.IsInf(f, 0):
			return 2
		case f == math.Trunc(f) && math.Abs(f) < 1<<62:
			return 3 + intCost(int64(f))
		}
		return 200
	case model.String:
		n := 2 * len(v.S)
		for i := 0; i < len(v.S); i++ {
			if v.S[i] < 0x20 || v.S[i] > 0x7e {
				n++
			}
		}
		return n
	case model.Bool:
		if v.B {
			return 1
		}
		return 0
	}
	return 0
}

func intCost(i int64) int {
	if i < 0 {
		return 2*bits.Len64(uint64(-i)) + 1
	}
	return 2 * bits.Len64(uint64(i))
}
