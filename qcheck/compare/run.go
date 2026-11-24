package compare

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	"github.com/apache/arrow-go/v18/arrow"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/fuzzcheck"
	_ "github.com/advenn/ursus/internal/fuzzcheck/invariants"
	"github.com/advenn/ursus/qcheck/ast"
	"github.com/advenn/ursus/qcheck/build"
	"github.com/advenn/ursus/qcheck/gen"
	"github.com/advenn/ursus/qcheck/model"
	"github.com/advenn/ursus/qcheck/sqlgen"
)

// Engine answers a case's query in the engine ursus is checked against: DuckDB,
// through package duck, or a test's.
type Engine interface {
	// Load makes the case's tables the engine's, replacing the last case's.
	Load(tables []*model.Table) error
	// Answer is the answer of q, whose translation is s, as Arrow.
	Answer(ctx context.Context, q *ast.Query, s *sqlgen.SQL) (*arrow.Schema, []arrow.RecordBatch, error)
	// Counts are the rows of each of s.Nodes' frames.
	Counts(ctx context.Context, q *ast.Query, s *sqlgen.SQL) ([]int64, error)
}

// Options are a run's.
type Options struct {
	Seed     uint64        // the first case's seed; case i's is Seed+i
	Cases    int           // stop after this many cases; 0 is no limit
	Duration time.Duration // stop starting cases after this long; 0 is no limit
	Engine   Engine
	// Core draws the core generator's cases (gen.NewCoreCase), step 512's types
	// and operations alone, each seed the case it was before step 539.
	Core bool
	// Limits bound each ursus run: its Wall is how long one query may take before the
	// watchdog ends the process, 0 for a minute.
	Limits fuzzcheck.Limits
	// Timeout bounds each engine query; 0 is a minute.
	Timeout time.Duration
	// Progress, if set, is told of each case as it finishes.
	Progress func(c *gen.Case, r *Report)

	// Corrupt, set by a test, changes the engine's answer once it is read: the
	// comparator's teeth.
	Corrupt func(c *gen.Case, a *Answer)
	// Inject, set by a test, runs inside fuzzcheck.Run after ursus's query, and what
	// it returns is the run's error: the runner's teeth, an ErrInternal or a panic.
	Inject func(cfg model.Config) error
}

// Kinds of finding.
const (
	KindMismatch  = "mismatch"  // the answers differ
	KindRefused   = "refused"   // ursus refused, the engine answered
	KindFuzzcheck = "fuzzcheck" // ErrInternal, a panic, a hang, the allocation bound, a leak
	KindSQL       = "sql"       // the engine refused the SQL: sqlgen's bug, or the engine's
	KindSchema    = "schema"    // an answer's columns are not the query's
	KindGenerator = "generator" // qcheck drew an ill-formed query, or sqlgen failed on one
	KindVacuous   = "vacuous"   // an operation nearly always empty
)

// Finding is one thing wrong.
type Finding struct {
	Kind   string
	Seed   uint64
	Config model.Config // ursus's
	Case   string       // the tables and the query, as Go
	SQL    string
	Detail string
	Core   bool // the case is the core generator's
}

func (f Finding) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "FINDING %s, seed %d\n", f.Kind, f.Seed)
	if f.Config != (model.Config{}) {
		fmt.Fprintf(&b, "  ursus ran under: %v\n", f.Config)
	}
	if f.Detail != "" {
		b.WriteString(indent(f.Detail, "  ") + "\n")
	}
	if f.Case != "" {
		b.WriteString(indent(f.Case, "  ") + "\n")
	}
	if f.SQL != "" {
		b.WriteString("  SQL:\n" + indent(f.SQL, "    ") + "\n")
	}
	return b.String()
}

func indent(s, by string) string { return by + strings.ReplaceAll(s, "\n", "\n"+by) }

// Run checks cases from opts.Seed on, until opts.Cases or opts.Duration.
func Run(opts Options) *Report {
	if opts.Limits.Wall <= 0 {
		opts.Limits.Wall = time.Minute
	}
	if opts.Timeout <= 0 {
		opts.Timeout = time.Minute
	}
	opts.Limits.API = true
	rep := newReport()
	warmUp()
	start := time.Now()
	for i := 0; opts.Cases == 0 || i < opts.Cases; i++ {
		if opts.Duration > 0 && time.Since(start) >= opts.Duration {
			break
		}
		seed := opts.Seed + uint64(i)
		c, failure := newCase(seed, opts.Core)
		if failure != "" {
			rep.add(Finding{Kind: KindGenerator, Seed: seed, Detail: failure})
			continue
		}
		(&caseRun{c: c, cfg: ConfigOf(c), opts: opts, rep: rep}).check()
		rep.Cases++
		if opts.Progress != nil {
			opts.Progress(c, rep)
		}
	}
	rep.Elapsed = time.Since(start)
	rep.finish()
	return rep
}

// ConfigOf is the configuration ursus runs case c under: a row of the covering
// array the self check draws its variants from, picked by the case's seed, so
// threads, batch sizes, the optimizer with WithVerify and the records a table is
// read in all meet DuckDB over a run. Every run is under WithMemoryLimit(0), as the
// self check's are.
func ConfigOf(c *gen.Case) model.Config {
	pairs := model.Pairwise()
	cfg := pairs[rand.New(rand.NewPCG(c.Seed, 0xd0c)).IntN(len(pairs))]
	if cfg.Records == 0 {
		cfg.Records = c.Records
	}
	return cfg
}

// newCase draws seed's case, and reports a generator's panic or ill-formed query.
func newCase(seed uint64, core bool) (c *gen.Case, failure string) {
	defer func() {
		if v := recover(); v != nil {
			failure = fmt.Sprintf("the generator panicked: %v", v)
		}
	}()
	c = gen.CaseOf(seed, core)
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

type caseRun struct {
	c    *gen.Case
	cfg  model.Config
	opts Options
	rep  *Report
}

func (cr *caseRun) finding(kind, sql, detail string) {
	cr.rep.add(Finding{Kind: kind, Seed: cr.c.Seed, Config: cr.cfg, Case: cr.c.String(), SQL: sql, Detail: detail,
		Core: cr.c.Core})
}

func (cr *caseRun) check() {
	tables := cr.c.TableMap()
	q, bounds := ast.Bounded(cr.c.Query)
	info, err := ast.Schema(q.Root, tables)
	if err != nil {
		cr.finding(KindGenerator, "", "the bounded query is ill-formed: "+err.Error())
		return
	}
	s, err := sqlgen.Translate(q, tables)
	var ex *sqlgen.Excluded
	switch {
	case errors.As(err, &ex):
		cr.rep.Excluded[ex.Rule]++
		return
	case err != nil:
		cr.finding(KindGenerator, "", "sqlgen: "+err.Error())
		return
	}
	for id, n := range s.Fired {
		cr.rep.Fired[id] += n
	}
	got, failure, schemaErr := cr.ursus(q, info)
	switch {
	case failure != "":
		cr.finding(KindFuzzcheck, s.Text, failure)
		return
	case schemaErr != nil:
		cr.finding(KindSchema, s.Text, schemaErr.Error())
		return
	}
	want, err := cr.engine(q, s, info)
	if err != nil {
		if got.Err != nil && errors.Is(got.Err, ursus.ErrValue) {
			// Both refused (step 543): ursus with ErrValue, a value its type cannot
			// hold, a Date or a year past int32's, and DuckDB with its own error, as
			// its translation computes the same value and its cast to that type
			// refuses it alike. Counted, not compared.
			cr.rep.BothRefused++
			return
		}
		cr.finding(KindSQL, s.Text, "DuckDB: "+err.Error())
		return
	}
	if cr.opts.Corrupt != nil {
		cr.opts.Corrupt(cr.c, want)
	}
	cr.rep.Compared++
	cr.rep.countQuery(q, want)
	if got.Err != nil {
		cr.finding(KindRefused, s.Text, fmt.Sprintf("ursus refused, %v\nDuckDB answered %s", firstLine(got.Err), want))
		return
	}
	plan, err := NewPlan(q, bounds, tables, cr.cfg.Threads)
	if err != nil {
		cr.finding(KindGenerator, s.Text, err.Error())
		return
	}
	v := Compare(want, got, plan)
	for k, n := range v.Fired {
		cr.rep.Allowed[k] += n
	}
	if v.Diff != "" {
		cr.finding(KindMismatch, s.Text, v.Diff+"\nDuckDB answered "+want.String()+"\nursus answered "+got.String())
		return
	}
	cr.countOps(q, s)
}

// ursus runs q under the case's configuration inside fuzzcheck.Run, and returns
// its answer, what fuzzcheck failed, or why its frame is not the query's.
func (cr *caseRun) ursus(q *ast.Query, info *ast.Info) (*Answer, string, error) {
	cr.rep.Runs++
	var a *Answer
	var readErr error
	failure := guarded(fmt.Sprintf("qcheck duck seed %d %v", cr.c.Seed, cr.cfg), cr.opts.Limits, func() error {
		lf := build.Frame(q.Root, build.RecordsOf(cr.c.Tables, cr.cfg.Records))
		ctx := context.Background()
		if q.Count {
			n, err := lf.Count(ctx, build.Options(cr.cfg)...)
			a = &Answer{Err: err, IsCount: true, Count: n}
			return err
		}
		df, err := lf.Collect(ctx, build.Options(cr.cfg)...)
		if err != nil {
			a = &Answer{Err: err}
			return err
		}
		a, readErr = FromUrsus(df, info.Cols)
		if cr.opts.Inject != nil {
			return cr.opts.Inject(cr.cfg)
		}
		return nil
	})
	if failure != "" {
		return nil, failure, nil
	}
	return a, "", readErr
}

// engine is the engine's answer to q.
func (cr *caseRun) engine(q *ast.Query, s *sqlgen.SQL, info *ast.Info) (*Answer, error) {
	if err := cr.opts.Engine.Load(cr.c.Tables); err != nil {
		return nil, fmt.Errorf("loading the tables: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), cr.opts.Timeout)
	defer cancel()
	schema, recs, err := cr.opts.Engine.Answer(ctx, q, s)
	defer func() {
		for _, r := range recs {
			r.Release()
		}
	}()
	if err != nil {
		return nil, err
	}
	if q.Count {
		n, err := countOf(recs)
		return &Answer{IsCount: true, Count: n}, err
	}
	return FromArrow(schema, recs, info.Cols)
}

func countOf(recs []arrow.RecordBatch) (int64, error) {
	if len(recs) == 0 {
		return 0, fmt.Errorf("a Count of no rows")
	}
	a, err := FromArrow(recs[0].Schema(), recs, []ast.Column{{Name: "n", Type: model.Int64}})
	if err != nil {
		return 0, err
	}
	if a.Rows != 1 || a.Cells[0][0] == "" {
		return 0, fmt.Errorf("a Count of %d rows", a.Rows)
	}
	return strconv.ParseInt(a.Cells[0][0][1:], 10, 64)
}

// countOps counts each frame operation's rows for the anti-vacuity checks, from the
// engine's counts of every node: they agree with ursus's wherever the answers do.
func (cr *caseRun) countOps(q *ast.Query, s *sqlgen.SQL) {
	ctx, cancel := context.WithTimeout(context.Background(), cr.opts.Timeout)
	defer cancel()
	counts, err := cr.opts.Engine.Counts(ctx, q, s)
	if err != nil {
		cr.finding(KindSQL, s.Counts, "DuckDB, counting each node's rows: "+err.Error())
		return
	}
	n := map[ast.Frame]int64{}
	for i, f := range s.Nodes {
		n[f] = counts[i]
	}
	ast.Walk(q.Root, func(f ast.Frame) {
		if _, ok := f.(*ast.Scan); ok {
			return
		}
		cr.rep.countOp(f.Op(), n[f.Inputs()[0]] > 0, n[f] > 0)
	})
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
