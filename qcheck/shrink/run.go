package shrink

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/fuzzcheck"
	_ "github.com/advenn/ursus/internal/fuzzcheck/invariants"
	"github.com/advenn/ursus/qcheck/ast"
	"github.com/advenn/ursus/qcheck/build"
	"github.com/advenn/ursus/qcheck/compare"
	"github.com/advenn/ursus/qcheck/model"
	"github.com/advenn/ursus/qcheck/sqlgen"
)

// state is a case being shrunk: what a step changes. Its tables are never changed in
// place: a step that changes one makes another.
type state struct {
	tables []*Table
	q      *ast.Query
	ref    model.Config
	cfg    model.Config
}

func (s *state) clone() *state {
	c := *s
	c.tables = slices.Clone(s.tables)
	return &c
}

func (s *state) tableMap() (map[string]*model.Table, error) {
	m := make(map[string]*model.Table, len(s.tables))
	for _, t := range s.tables {
		mt, err := t.Model()
		if err != nil {
			return nil, err
		}
		m[t.Name] = mt
	}
	return m, nil
}

func (s *state) models() ([]*model.Table, error) {
	out := make([]*model.Table, len(s.tables))
	for i, t := range s.tables {
		m, err := t.Model()
		if err != nil {
			return nil, err
		}
		out[i] = m
	}
	return out, nil
}

// outcome is one check of a case: how it failed, a zero Signature where it passed,
// or why it is no case at all.
type outcome struct {
	sig     Signature
	detail  string
	invalid string
}

// corrupt, set by a test, changes ursus's answer under cfg once it is read:
// TestShrinkerReachesMinimum's known bug.
var corrupt func(tables map[string]*model.Table, q *ast.Query, cfg model.Config, a *compare.Answer)

// checker runs a case's candidates, each query inside fuzzcheck.Run.
type checker struct {
	mode   string
	engine compare.Engine
	lim    fuzzcheck.Limits
	// joinBound bounds an inner or left join's matches, counted on the base tables:
	// a step that makes keys alike must not make a join of millions of rows.
	joinBound int
	queries   int
}

func newChecker(mode string, engine compare.Engine, lim fuzzcheck.Limits) *checker {
	if lim.Wall <= 0 {
		lim.Wall = time.Minute
	}
	lim.API = true
	return &checker{mode: mode, engine: engine, lim: lim, joinBound: maxJoinRows}
}

// maxJoinRows is gen.MaxJoinRows: the generator draws no join past it.
const maxJoinRows = 10_000

// check runs s once.
func (k *checker) check(s *state) outcome {
	tables, err := s.tableMap()
	if err != nil {
		return outcome{invalid: err.Error()}
	}
	if err := ast.Check(s.q, tables); err != nil {
		return outcome{invalid: err.Error()}
	}
	if n := joinMatches(s.q, tables); n > k.joinBound {
		return outcome{invalid: fmt.Sprintf("a join of %d matches, past %d", n, k.joinBound)}
	}
	bq, bounds := ast.Bounded(s.q)
	info, err := ast.Schema(bq.Root, tables)
	if err != nil {
		return outcome{invalid: "the bounded query: " + err.Error()}
	}
	run := func(cfg model.Config) (*compare.Answer, Signature, string) {
		a, failure, readErr := k.ursus(s, bq, info, cfg)
		switch {
		case failure != "":
			return nil, fuzzSignature(failure), failure
		case readErr != nil:
			return nil, Signature{Kind: KindSchema, Class: class(readErr.Error())}, readErr.Error()
		}
		if corrupt != nil && a.Err == nil {
			corrupt(tables, s.q, cfg, a)
		}
		return a, Signature{}, ""
	}
	cellSig := func(kind string, v compare.Verdict) Signature {
		sig := Signature{Kind: kind, What: v.What}
		if v.What == compare.DiffCell {
			sig.Op = producer(bq.Root, v.Column, tables)
			if c := info.Col(v.Column); c != nil {
				sig.Type = c.Type.String()
			}
		}
		return sig
	}
	switch k.mode {
	case ModeSchema:
		return k.schema(s, bq, info)
	case ModeRun:
		_, sig, detail := run(s.cfg)
		return outcome{sig: sig, detail: detail}
	case ModeRerun:
		a, sig, detail := run(s.cfg)
		if !sig.Zero() {
			return outcome{sig: sig, detail: detail}
		}
		b, sig, detail := run(s.cfg)
		if !sig.Zero() {
			return outcome{sig: sig, detail: detail}
		}
		if (a.Err == nil) != (b.Err == nil) {
			return outcome{sig: Signature{Kind: KindRerun, What: "refusal"}, detail: fmt.Sprintf("%v, then %v", a.Err, b.Err)}
		}
		if a.Err != nil {
			return outcome{}
		}
		v := compare.Compare(a, b, compare.Plan{Ordered: true, Want: "first run", Got: "second run"})
		if v.Diff == "" {
			return outcome{}
		}
		return outcome{sig: cellSig(KindRerun, v), detail: v.Diff}
	case ModeDifferential:
		want, sig, detail := run(s.ref)
		if !sig.Zero() {
			return outcome{sig: sig, detail: "the reference: " + detail}
		}
		got, sig, detail := run(s.cfg)
		if !sig.Zero() {
			return outcome{sig: sig, detail: detail}
		}
		switch {
		case want.Err != nil && got.Err != nil:
			return outcome{}
		case want.Err != nil:
			return outcome{sig: Signature{Kind: KindRefusal, What: "reference", Class: errKind(want.Err)},
				detail: "the reference refused, " + firstLine(want.Err) + "; the variant answered"}
		case got.Err != nil:
			return outcome{sig: Signature{Kind: KindRefusal, What: "variant", Class: errKind(got.Err)},
				detail: "the reference answered; the variant refused, " + firstLine(got.Err)}
		}
		v := compare.Compare(want, got, compare.Plan{Ordered: info.Ordered, Bounds: bounds,
			Threads: s.ref.Threads != s.cfg.Threads, Want: "reference", Got: "variant"})
		if v.Diff == "" {
			return outcome{}
		}
		return outcome{sig: cellSig(KindMismatch, v), detail: v.Diff}
	case ModeDuck:
		return k.duck(s, bq, bounds, info, tables, run, cellSig)
	}
	return outcome{invalid: "a case of mode " + k.mode}
}

func (k *checker) duck(s *state, bq *ast.Query, bounds map[string]ast.Bound, info *ast.Info,
	tables map[string]*model.Table, run func(model.Config) (*compare.Answer, Signature, string),
	cellSig func(string, compare.Verdict) Signature) outcome {
	if k.engine == nil {
		return outcome{invalid: "a DuckDB case needs DuckDB: qcheck built with -tags duckdb,duckdb_arrow"}
	}
	sq, err := sqlgen.Translate(bq, tables)
	var ex *sqlgen.Excluded
	switch {
	case errors.As(err, &ex):
		return outcome{invalid: "sqlgen excludes it: " + ex.Error()}
	case err != nil:
		return outcome{invalid: "sqlgen: " + err.Error()}
	}
	got, sig, detail := run(s.cfg)
	if !sig.Zero() {
		return outcome{sig: sig, detail: detail}
	}
	models, err := s.models()
	if err != nil {
		return outcome{invalid: err.Error()}
	}
	want, err := k.engineAnswer(bq, sq, info, models)
	if err != nil {
		return outcome{sig: Signature{Kind: KindSQL, Class: class(err.Error())}, detail: "DuckDB: " + err.Error()}
	}
	if got.Err != nil {
		return outcome{sig: Signature{Kind: KindRefusal, What: "ursus", Class: errKind(got.Err)},
			detail: "ursus refused, " + firstLine(got.Err) + "; DuckDB answered"}
	}
	v := compare.Compare(want, got, compare.Plan{Ordered: info.Ordered, Bounds: bounds, Threads: s.cfg.Threads > 1})
	if v.Diff == "" {
		return outcome{}
	}
	return outcome{sig: cellSig(KindMismatch, v), detail: v.Diff}
}

// engineAnswer is DuckDB's answer, as compare's runner reads it.
func (k *checker) engineAnswer(q *ast.Query, sq *sqlgen.SQL, info *ast.Info, models []*model.Table) (*compare.Answer, error) {
	if err := k.engine.Load(models); err != nil {
		return nil, fmt.Errorf("loading the tables: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	schema, recs, err := k.engine.Answer(ctx, q, sq)
	defer func() {
		for _, r := range recs {
			r.Release()
		}
	}()
	if err != nil {
		return nil, err
	}
	if q.Count {
		a, err := compare.FromArrow(schema, recs, []ast.Column{{Name: "n", Type: model.Int64}})
		if err != nil {
			return nil, err
		}
		if a.Rows != 1 || a.Cells[0][0] == "" {
			return nil, fmt.Errorf("a Count of %d rows", a.Rows)
		}
		var n int64
		if _, err := fmt.Sscan(a.Cells[0][0][1:], &n); err != nil {
			return nil, err
		}
		return &compare.Answer{IsCount: true, Count: n}, nil
	}
	return compare.FromArrow(schema, recs, info.Cols)
}

// ursus runs q, a bounded query whose frame info describes, under cfg inside
// fuzzcheck.Run, and returns its answer, what fuzzcheck failed, or why its frame is
// not the query's.
func (k *checker) ursus(s *state, q *ast.Query, info *ast.Info, cfg model.Config) (*compare.Answer, string, error) {
	k.queries++
	models, err := s.models()
	if err != nil {
		return nil, "", err
	}
	var a *compare.Answer
	var readErr error
	failure := guarded(fmt.Sprintf("qcheck shrink %v", cfg), k.lim, func() error {
		lf := build.Frame(q.Root, build.RecordsOf(models, cfg.Records))
		ctx := context.Background()
		if q.Count {
			n, err := lf.Count(ctx, build.Options(cfg)...)
			a = &compare.Answer{Err: err, IsCount: true, Count: n}
			return err
		}
		df, err := lf.Collect(ctx, build.Options(cfg)...)
		if err != nil {
			a = &compare.Answer{Err: err}
			return err
		}
		a, readErr = compare.FromUrsus(df, info.Cols)
		return nil
	})
	if failure != "" {
		return nil, failure, nil
	}
	return a, "", readErr
}

// schema compares ursus's CollectSchema of q with the AST's types.
func (k *checker) schema(s *state, q *ast.Query, info *ast.Info) outcome {
	models, err := s.models()
	if err != nil {
		return outcome{invalid: err.Error()}
	}
	var got *ursus.Schema
	var serr error
	failure := guarded("qcheck shrink schema", k.lim, func() error {
		got, serr = build.Frame(q.Root, build.RecordsOf(models, 1)).CollectSchema(context.Background())
		return serr
	})
	switch {
	case failure != "":
		return outcome{sig: fuzzSignature(failure), detail: failure}
	case serr != nil:
		return outcome{}
	}
	var want, have []string
	for _, c := range info.Cols {
		want = append(want, c.Name+" "+c.Type.String())
	}
	for i := range got.Len() {
		f := got.Field(i)
		have = append(have, f.Name+" "+build.TypeOf(f.Type).String())
	}
	if slices.Equal(want, have) {
		return outcome{}
	}
	return outcome{sig: Signature{Kind: KindSchema}, detail: fmt.Sprintf("ursus's schema %v\nthe AST's       %v", have, want)}
}

func firstLine(err error) string {
	s, _, _ := strings.Cut(err.Error(), "\n")
	return s
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

// joinMatches is the most matches any inner or left join of q has, counted on the
// base tables its keys come from, a null matching none, NaN matching NaN and -0
// matching +0, as gen counts them; 0 where a key is computed.
func joinMatches(q *ast.Query, tables map[string]*model.Table) int {
	most := 0
	ast.Walk(q.Root, func(f ast.Frame) {
		j, ok := f.(*ast.Join)
		if !ok || (j.How != ast.Inner && j.How != ast.Left) {
			return
		}
		l, err := ast.Schema(j.L, tables)
		if err != nil {
			return
		}
		r, err := ast.Schema(j.R, tables)
		if err != nil {
			return
		}
		lc, rc := l.Col(j.LeftOn), r.Col(j.RightOn)
		if lc == nil || rc == nil || lc.Origin == "" || rc.Origin == "" {
			return
		}
		lk, rk := keyCounts(lc.Origin, tables), keyCounts(rc.Origin, tables)
		n := 0
		for key, c := range lk {
			n += c * rk[key]
		}
		most = max(most, n)
	})
	return most
}

func keyCounts(origin string, tables map[string]*model.Table) map[string]int {
	tname, cname, _ := strings.Cut(origin, ".")
	t := tables[tname]
	out := map[string]int{}
	if t == nil {
		return out
	}
	idx := slices.IndexFunc(t.Cols, func(c model.Column) bool { return c.Name == cname })
	if idx < 0 {
		return out
	}
	col := t.Rec.Column(idx)
	for i := range col.Len() {
		if col.IsNull(i) {
			continue
		}
		out[keyOf(col, i)]++
	}
	return out
}

func keyOf(col arrow.Array, i int) string {
	switch a := col.(type) {
	case *array.Int64:
		return fmt.Sprint(a.Value(i))
	case *array.Int32:
		return fmt.Sprint(a.Value(i))
	case *array.Float64:
		f := a.Value(i)
		switch {
		case math.IsNaN(f):
			return "NaN"
		case f == 0:
			return "0"
		}
		return fmt.Sprint(math.Float64bits(f))
	case *array.String:
		return a.Value(i)
	}
	return col.ValueStr(i)
}
