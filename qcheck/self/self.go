// Package self checks qcheck's random queries against themselves (step 512, plan
// step 454, H6 P1b): each query must give one answer at every configuration.
//
// The reference run is one thread, batches of 8192 rows, WithMemoryLimit(0) and the
// default flags. Each case runs it twice, which must agree to the bit, and then a
// few variants drawn in turn from model.Pairwise, a covering array of threads {1, 4,
// 8}, batch sizes {1, 2, 3, 7, 64, 1024, 65536}, the optimizer on or off with
// WithVerify, and 1 against k source records; one variant runs twice as well.
// Answers compare bit-exact: in order where the query promises one, as multisets
// where it does not. The one allowance is the float-threads rule: a float Sum or
// Mean of a group-by on several threads may differ from the one-thread answer
// within the error model of the critic's correction to 437, |Δ| ≤ 2·n·eps·Σ|x|.
// Each time it fires is counted, and a run that should fire it and does not fails as
// stale. A mismatch is bisected to the fewest factors that cause it.
//
// Every run is inside fuzzcheck.Run, so ErrInternal, a panic, a hang, an allocation
// past the bound or a goroutine left behind fails the case whatever it answered.
// Each frame operation's output is counted at the reference, and a run where an
// operation nearly always answers no rows, or is always refused, fails as vacuous.
package self

import (
	"context"
	"fmt"
	"math"
	"math/bits"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/fuzzcheck"
	_ "github.com/advenn/ursus/internal/fuzzcheck/invariants"
	"github.com/advenn/ursus/qcheck/ast"
	"github.com/advenn/ursus/qcheck/build"
	"github.com/advenn/ursus/qcheck/gen"
	"github.com/advenn/ursus/qcheck/model"
)

// Options are a run's.
type Options struct {
	Seed     uint64        // the first case's seed; case i's is Seed+i
	Cases    int           // stop after this many cases; 0 is no limit
	Duration time.Duration // stop starting cases after this long; 0 is no limit
	Variants int           // variants a case runs; 0 is 3
	// Limits bound each run: its Wall is how long one query may take before the
	// watchdog ends the process, 0 for a minute.
	Limits fuzzcheck.Limits
	// Progress, if set, is told of each case as it finishes.
	Progress func(c *gen.Case, r *Report)
}

// Kinds of finding.
const (
	KindMismatch  = "mismatch"   // a variant's answer differs from the reference's
	KindRerun     = "rerun"      // one configuration, two answers
	KindFuzzcheck = "fuzzcheck"  // ErrInternal, a panic, a leak, the allocation bound
	KindSchema    = "schema"     // ursus's schema differs from the AST's types
	KindGenerator = "generator"  // qcheck drew an ill-formed query, its own bug
	KindVacuous   = "vacuous"    // an operation nearly always empty, or always refused
	KindStale     = "stale-rule" // the float-threads rule stopped firing
)

// Finding is one thing wrong.
type Finding struct {
	Kind    string
	Seed    uint64
	Case    string // the case: tables and query, as Go
	Ref     model.Config
	Variant model.Config
	Factors model.Factor // the fewest factors that reproduce a mismatch
	Detail  string
}

func (f Finding) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "FINDING %s, seed %d\n", f.Kind, f.Seed)
	switch {
	case f.Kind == KindMismatch || f.Kind == KindRerun:
		fmt.Fprintf(&b, "  reference: %v\n  variant:   %v\n", f.Ref, f.Variant)
	case f.Variant != (model.Config{}):
		fmt.Fprintf(&b, "  run under: %v\n", f.Variant)
	}
	if f.Factors != 0 {
		fmt.Fprintf(&b, "  caused by: %v (the reference with only that changed)\n", f.Factors)
	}
	if f.Detail != "" {
		b.WriteString(indent(f.Detail, "  ") + "\n")
	}
	if f.Case != "" {
		b.WriteString(indent(f.Case, "  ") + "\n")
	}
	return b.String()
}

func indent(s, by string) string { return by + strings.ReplaceAll(s, "\n", "\n"+by) }

// corrupt, set by a test, changes an answer once it is read: TestSelfTeeth's
// wrong answer. run is how many times the case ran cfg before.
var corrupt func(c *gen.Case, cfg model.Config, run int, a *Answer)

// inject, set by a test, runs inside fuzzcheck.Run after the query, and what it
// returns is the run's error: TestSelfTeeth's ErrInternal and panic.
var inject func(cfg model.Config) error

// Run checks cases from opts.Seed on, until opts.Cases or opts.Duration.
func Run(opts Options) *Report {
	if opts.Variants <= 0 {
		opts.Variants = 3
	}
	if opts.Limits.Wall <= 0 {
		opts.Limits.Wall = time.Minute
	}
	opts.Limits.API = true
	rep := newReport()
	warmUp()
	start := time.Now()
	pairs := model.Pairwise()
	for i := 0; opts.Cases == 0 || i < opts.Cases; i++ {
		if opts.Duration > 0 && time.Since(start) >= opts.Duration {
			break
		}
		seed := opts.Seed + uint64(i)
		c, err := newCase(seed)
		if err != "" {
			rep.add(Finding{Kind: KindGenerator, Seed: seed, Detail: err})
			continue
		}
		var variants []model.Config
		for v := range opts.Variants {
			cfg := pairs[(int(seed%uint64(len(pairs)))*opts.Variants+v)%len(pairs)]
			if cfg.Records == 0 {
				cfg.Records = c.Records
			}
			variants = append(variants, cfg)
		}
		(&caseRun{c: c, opts: opts, rep: rep, runs: map[model.Config]int{}}).check(variants)
		rep.Cases++
		if opts.Progress != nil {
			opts.Progress(c, rep)
		}
	}
	rep.Elapsed = time.Since(start)
	rep.finish()
	return rep
}

// newCase draws seed's case, and reports a generator's panic or ill-formed query.
func newCase(seed uint64) (c *gen.Case, failure string) {
	defer func() {
		if v := recover(); v != nil {
			failure = fmt.Sprintf("the generator panicked: %v", v)
		}
	}()
	c = gen.NewCase(seed)
	if err := ast.Check(c.Query, c.TableMap()); err != nil {
		return nil, fmt.Sprintf("an ill-formed query: %v\n%s", err, c)
	}
	return c, ""
}

// warmUp runs one query before any is judged, so that what ursus starts once per
// process is not taken for a goroutine a query left behind.
func warmUp() {
	t := gen.Table(1, "w", 100)
	_, _ = ursus.ScanArrowRecords(t.Rec).GroupBy(ursus.Col(t.Cols[0].Name)).Agg(ursus.Len()).
		Collect(context.Background(), ursus.WithThreads(4), ursus.WithMemoryLimit(0))
}

// caseRun is one case's runs.
type caseRun struct {
	c      *gen.Case
	opts   Options
	rep    *Report
	q      *ast.Query
	bounds map[string]ast.Bound
	info   *ast.Info
	runs   map[model.Config]int
	failed bool // fuzzcheck failed a run: the case stops
}

func (cr *caseRun) check(variants []model.Config) {
	cr.q, cr.bounds = ast.Bounded(cr.c.Query)
	info, err := ast.Schema(cr.q.Root, cr.c.TableMap())
	if err != nil {
		cr.finding(Finding{Kind: KindGenerator, Detail: "the bounded query is ill-formed: " + err.Error()})
		return
	}
	cr.info = info
	ref := model.Reference()

	cr.checkSchema()
	if cr.failed {
		return
	}
	want := cr.run(ref)
	if cr.failed {
		return
	}
	again := cr.run(ref)
	if cr.failed {
		return
	}
	cr.rep.countQuery(cr.c.Query, want)
	if d := identical(want, again, true); d != "" {
		cr.finding(Finding{Kind: KindRerun, Ref: ref, Variant: ref, Detail: d})
		return
	}
	cr.countOps(ref)
	if cr.failed {
		return
	}
	for i, v := range variants {
		got := cr.run(v)
		if cr.failed {
			return
		}
		cmp := cr.comparison(ref, v)
		verdict := cmp.compare(want, got)
		cr.rep.Comparisons++
		if cmp.threads && len(cr.bounds) > 0 && want.Err == nil && want.Rows > 0 {
			cr.rep.Eligible++
			if verdict.fired > 0 {
				cr.rep.Fired++
				cr.rep.FiredCells += verdict.fired
			}
		}
		if verdict.diff != "" {
			factors, detail := cr.bisect(want, ref, v)
			if cr.failed {
				return
			}
			cr.finding(Finding{Kind: KindMismatch, Ref: ref, Variant: v, Factors: factors,
				Detail: verdict.diff + detail})
			return
		}
		if i == 0 {
			// The same variant again, which must agree with itself to the bit.
			again := cr.run(v)
			if cr.failed {
				return
			}
			if d := identical(got, again, true); d != "" {
				cr.finding(Finding{Kind: KindRerun, Ref: v, Variant: v, Detail: d})
				return
			}
		}
	}
	cr.canary()
}

// canaryThreads is the configuration a canary runs on beside the reference: eight
// workers, each dealt every eighth batch of seven rows.
var canaryThreads = model.Config{Threads: 8, Batch: 7, Records: 1}

// canaryEvery is how often a case runs the canary: every eighth.
const canaryEvery = 8

// canary checks that the float-threads rule still has a reason to exist. Every
// eighth case it sums 4,096 floats of five magnitudes, drawn from the case's seed,
// keyless, on eight threads in batches of seven rows, and compares the sum with the
// reference's. The workers' partial sums are merged in another order than one
// thread adds the values, so the last bits differ almost always (feature-gaps §1);
// a run whose canaries never differ fails the rule as stale.
func (cr *caseRun) canary() {
	if cr.c.Seed%canaryEvery != 0 {
		return
	}
	r := rand.New(rand.NewPCG(cr.c.Seed, 0xca7a7))
	b := array.NewRecordBuilder(memory.NewGoAllocator(), arrow.NewSchema([]arrow.Field{
		{Name: "x", Type: arrow.PrimitiveTypes.Float64, Nullable: true}}, nil))
	defer b.Release()
	for range 4096 {
		b.Field(0).(*array.Float64Builder).Append(r.NormFloat64() * math.Pow(10, float64(r.IntN(5))))
	}
	rec := b.NewRecordBatch()
	defer rec.Release()
	t := &model.Table{Name: "canary", Rows: 4096, Rec: rec,
		Cols: []model.Column{{Name: "x", Type: model.Float64, Profile: model.Wild}}}

	sum, err := ast.NewAgg(ast.Sum, &ast.Col{Name: "x", T: model.Float64})
	if err != nil {
		panic(err)
	}
	q, bounds := ast.Bounded(&ast.Query{Root: &ast.GroupBy{In: &ast.Scan{Table: t.Name},
		Aggs: []ast.Named{{Name: "canary", E: sum}}}})
	tables := map[string][]arrow.RecordBatch{t.Name: {rec}}
	ref := model.Reference()
	want := cr.runOn(ref, q, tables)
	if cr.failed {
		return
	}
	got := cr.runOn(canaryThreads, q, tables)
	if cr.failed {
		return
	}
	v := comparison{ordered: true, bounds: bounds, threads: true}.compare(want, got)
	cr.rep.Canaries++
	if v.fired > 0 {
		cr.rep.CanariesFired++
	}
	if v.diff != "" {
		cr.finding(Finding{Kind: KindMismatch, Ref: ref, Variant: canaryThreads, Factors: model.FThreads | model.FBatch,
			Detail: "the canary, a keyless Sum of 4,096 floats drawn from the case's seed: " + v.diff})
	}
}

// comparison is how a run under cfg compares with one under ref.
func (cr *caseRun) comparison(ref, cfg model.Config) comparison {
	return comparison{ordered: cr.info.Ordered, bounds: cr.bounds, threads: ref.Threads != cfg.Threads}
}

// bisect finds the fewest factors of v that, changed alone in the reference, give a
// mismatch: each single factor first, then pairs, and so on.
func (cr *caseRun) bisect(want *Answer, ref, v model.Config) (model.Factor, string) {
	diff := v.Diff(ref)
	var subsets []model.Factor
	for s := model.Factor(1); s <= diff; s++ {
		if s&^diff == 0 {
			subsets = append(subsets, s)
		}
	}
	slices.SortStableFunc(subsets, func(a, b model.Factor) int {
		return bits.OnesCount8(uint8(a)) - bits.OnesCount8(uint8(b))
	})
	for _, s := range subsets {
		cfg := ref.With(v, s)
		got := cr.run(cfg)
		if cr.failed {
			return 0, ""
		}
		if d := cr.comparison(ref, cfg).compare(want, got).diff; d != "" {
			if cfg != v {
				line, _, _ := strings.Cut(d, "\n")
				return s, fmt.Sprintf("\nreproduced by %v alone: %s", cfg, line)
			}
			return s, ""
		}
	}
	return diff, "\n(no subset of its factors reproduced it alone: flaky?)"
}

// run runs the query under cfg inside fuzzcheck.Run.
func (cr *caseRun) run(cfg model.Config) *Answer {
	return cr.runFrame(cfg, cr.q, true)
}

func (cr *caseRun) recs(cfg model.Config) map[string][]arrow.RecordBatch {
	return build.RecordsOf(cr.c.Tables, cfg.Records)
}

// runFrame runs q under cfg; judged says the answer counts as a run of the case,
// which the test hook may corrupt.
func (cr *caseRun) runFrame(cfg model.Config, q *ast.Query, judged bool) *Answer {
	a := cr.runOn(cfg, q, nil)
	if judged && !cr.failed {
		if corrupt != nil {
			corrupt(cr.c, cfg, cr.runs[cfg], a)
		}
		cr.runs[cfg]++
	}
	return a
}

// runOn runs q under cfg over tables, or over the case's tables, cut as cfg says,
// when tables is nil.
func (cr *caseRun) runOn(cfg model.Config, q *ast.Query, tables map[string][]arrow.RecordBatch) *Answer {
	cr.rep.Runs++
	var a *Answer
	var readErr error
	name := fmt.Sprintf("qcheck seed %d %v", cr.c.Seed, cfg)
	failure := guarded(name, cr.opts.Limits, func() error {
		recs := tables
		if recs == nil {
			recs = cr.recs(cfg)
		}
		lf := build.Frame(q.Root, recs)
		ctx := context.Background()
		if q.Count {
			n, err := lf.Count(ctx, build.Options(cfg)...)
			a = &Answer{Err: err, Count: n}
			return err
		}
		df, err := lf.Collect(ctx, build.Options(cfg)...)
		if err != nil {
			a = &Answer{Err: err}
			return err
		}
		a, readErr = read(df)
		if inject != nil {
			return inject(cfg)
		}
		return nil
	})
	if failure != "" {
		cr.finding(Finding{Kind: KindFuzzcheck, Variant: cfg, Detail: failure})
		cr.failed = true
		return &Answer{Err: fmt.Errorf("fuzzcheck")}
	}
	if readErr != nil {
		cr.finding(Finding{Kind: KindGenerator, Variant: cfg, Detail: readErr.Error()})
		cr.failed = true
		return &Answer{Err: readErr}
	}
	return a
}

// checkSchema compares ursus's schema of the query with the AST's types.
func (cr *caseRun) checkSchema() {
	var got *ursus.Schema
	var err error
	failure := guarded(fmt.Sprintf("qcheck seed %d schema", cr.c.Seed), cr.opts.Limits, func() error {
		got, err = build.Frame(cr.q.Root, cr.recs(model.Reference())).CollectSchema(context.Background())
		return err
	})
	switch {
	case failure != "":
		cr.finding(Finding{Kind: KindFuzzcheck, Detail: failure})
		cr.failed = true
		return
	case err != nil:
		// A refusal at planning: counted with the reference's answer.
		return
	}
	var want, have []string
	for _, c := range cr.info.Cols {
		want = append(want, c.Name+" "+c.Type.String())
	}
	for i := range got.Len() {
		f := got.Field(i)
		have = append(have, f.Name+" "+build.TypeOf(f.Type).String())
	}
	if !slices.Equal(want, have) {
		cr.finding(Finding{Kind: KindSchema, Detail: fmt.Sprintf("ursus's schema %v\nthe AST's       %v", have, want)})
	}
}

// countOps counts each frame operation's rows at the reference, for the
// anti-vacuity checks: a Count of each node, and of its first input.
func (cr *caseRun) countOps(ref model.Config) {
	counts := map[ast.Frame]int64{}
	refused := map[ast.Frame]bool{}
	count := func(f ast.Frame) (int64, bool) {
		if n, ok := counts[f]; ok {
			return n, !refused[f]
		}
		a := cr.runFrame(ref, &ast.Query{Root: f, Count: true}, false)
		counts[f], refused[f] = a.Count, a.Err != nil
		return a.Count, a.Err == nil
	}
	ast.Walk(cr.q.Root, func(f ast.Frame) {
		if cr.failed {
			return
		}
		if _, ok := f.(*ast.Scan); ok {
			return
		}
		in, inOK := count(f.Inputs()[0])
		out, ok := count(f)
		cr.rep.countOp(f.Op(), inOK && in > 0, ok, out > 0)
	})
}

func (cr *caseRun) finding(f Finding) {
	f.Seed = cr.c.Seed
	f.Case = cr.c.String()
	cr.rep.add(f)
}

// tb is the fuzzcheck.TB a run reports to: it keeps what failed, and FailNow
// unwinds to guarded.
type tb struct {
	name string
	errs []string
}

type failNow struct{}

func (t *tb) Helper()                         {}
func (t *tb) Name() string                    { return t.name }
func (t *tb) Errorf(format string, a ...any)  { t.errs = append(t.errs, fmt.Sprintf(format, a...)) }
func (t *tb) Logf(format string, args ...any) {}
func (t *tb) FailNow()                        { panic(failNow{}) }

// guarded runs fn inside fuzzcheck.Run and returns what it failed, or "".
func guarded(name string, lim fuzzcheck.Limits, fn func() error) (failure string) {
	t := &tb{name: name}
	defer func() {
		if v := recover(); v != nil {
			if _, ok := v.(failNow); !ok {
				panic(v)
			}
		}
		failure = strings.Join(t.errs, "\n")
	}()
	fuzzcheck.Run(t, lim, fn)
	return ""
}
