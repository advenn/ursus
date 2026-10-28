package ursus_test

// Step 403: an engine's hook is told of each of its queries as it ends, its duration,
// rows, peak, spills, threads and error, with the query's context (C3 of
// v0.8-scope.md).

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/plan"
)

type routeKey struct{}

// heard is a hook's reports, with the route its context carried.
type heard struct {
	mu   sync.Mutex
	got  []ursus.QueryStats
	from []any
}

func (h *heard) hook(ctx context.Context, s ursus.QueryStats) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.got, h.from = append(h.got, s), append(h.from, ctx.Value(routeKey{}))
}

// since returns the reports after the first n.
func (h *heard) since(n int) ([]ursus.QueryStats, []any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]ursus.QueryStats(nil), h.got[n:]...), append([]any(nil), h.from[n:]...)
}

func (h *heard) len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.got)
}

// TestEveryQueryIsReportedOnce reflects over *LazyFrame for every method that runs a
// query, as TestEveryEntryPointRecovers does, and runs each on DefaultEngine with a
// hook: on a frame of five rows, one report with the request's context, no error and
// the rows; on a plan that panics as it is resolved, one report with the error the
// method returned, an internal one. A method that reads the schema alone runs no
// query and reports none, and Describe, which reads it first, none of that plan.
func TestEveryQueryIsReportedOnce(t *testing.T) {
	var h heard
	defer ursus.SetDefaultEngine(ursus.NewEngine(ursus.WithEngineQueryHook(h.hook)))()
	ctx := context.WithValue(t.Context(), routeKey{}, "/report")
	good := panicFrame()
	bad := ursus.FromPlan(&plan.Project{Input: panicFrame().Plan(),
		Exprs: []expr.Node{&expr.Alias{Name: "x"}}})
	errType := reflect.TypeFor[error]()
	arg := func(in reflect.Type) reflect.Value {
		switch in {
		case reflect.TypeFor[context.Context]():
			return reflect.ValueOf(ctx)
		case reflect.TypeFor[io.Writer]():
			return reflect.ValueOf(io.Writer(io.Discard))
		case reflect.TypeFor[string]():
			return reflect.ValueOf(filepath.Join(t.TempDir(), "out"))
		case reflect.TypeFor[int]():
			return reflect.ValueOf(2)
		case reflect.TypeFor[ursus.DatabaseWriter]():
			db, _ := writeDB(t)
			return reflect.ValueOf(ursus.DatabaseWriter(db))
		}
		t.Fatalf("no argument for a %s", in)
		return reflect.Value{}
	}
	run := func(lf *ursus.LazyFrame, name string) (error, bool) {
		fn := reflect.ValueOf(lf).MethodByName(name)
		ft := fn.Type()
		isErr := ft.NumOut() > 0 && ft.Out(ft.NumOut()-1) == errType
		isSeq := ft.NumOut() == 1 && ft.Out(0).Kind() == reflect.Func && ft.Out(0).NumIn() == 1 &&
			ft.Out(0).In(0).NumIn() == 2 && ft.Out(0).In(0).In(1) == errType
		if (!isErr && !isSeq) || name == "Err" {
			return nil, false
		}
		var args []reflect.Value
		for j := range ft.NumIn() {
			if ft.IsVariadic() && j == ft.NumIn()-1 {
				if ft.In(j).Elem() == reflect.TypeFor[ursus.DatabaseWriteOption]() {
					args = append(args, reflect.ValueOf(ursus.WithDialect(ursus.DialectSQLite)))
				}
				break
			}
			args = append(args, arg(ft.In(j)))
		}
		out := fn.Call(args)
		if isErr {
			err, _ := out[len(out)-1].Interface().(error)
			return err, true
		}
		var err error
		yield := reflect.MakeFunc(out[0].Type().In(0), func(in []reflect.Value) []reflect.Value {
			if e, _ := in[1].Interface().(error); e != nil && err == nil {
				err = e
			}
			return []reflect.Value{reflect.ValueOf(true)}
		})
		out[0].Call([]reflect.Value{yield})
		return err, true
	}

	schemaOnly := map[string]bool{"CollectSchema": true, "Columns": true, "DTypes": true, "Explain": true, "Width": true}
	var swept []string
	typ := reflect.TypeFor[*ursus.LazyFrame]()
	for i := range typ.NumMethod() {
		name := typ.Method(i).Name
		n := h.len()
		err, ran := run(good, name)
		if !ran {
			continue
		}
		swept = append(swept, name)
		got, from := h.since(n)
		if schemaOnly[name] {
			if _, _ = run(bad, name); len(got) != 0 || h.len() != n {
				t.Errorf("%s, which runs no query, reported %+v", name, got)
			}
			continue
		}
		if len(got) != 1 || err != nil || got[0].Err != nil || got[0].Rows == 0 || from[0] != "/report" {
			t.Errorf("%s: %d reports %+v from %v, its error %v: want one, of rows and no error, from /report",
				name, len(got), got, from, err)
		}
		n = h.len()
		err, _ = run(bad, name)
		got, _ = h.since(n)
		if name == "Describe" {
			if len(got) != 0 || err == nil {
				t.Errorf("Describe of a plan that panics in its schema: %+v, %v", got, err)
			}
			continue
		}
		var e *ursus.Error
		if len(got) != 1 || err == nil || got[0].Err == nil || got[0].Err.Error() != err.Error() ||
			!errors.As(got[0].Err, &e) || e.Kind != ursus.KindInternal {
			t.Errorf("%s of a plan that panics: %d reports %+v, its error %v: want one, of that internal error",
				name, len(got), got, err)
		}
	}
	if len(swept) < 15 {
		t.Errorf("swept only %d entry points: %v", len(swept), swept)
	}
}

// TestAHookIsToldWhatAQueryDid: on an engine of four threads, a group-by of 1,000 rows
// into 10 groups is 10 rows on one thread with a peak and no spills; a sort of
// 200,000 rows under 256 KiB of its own spills and runs on four; a stream broken off
// after a batch is reported once, its rows the batch's; a query cancelled and one of
// an unknown column report their errors; a partitioned write its rows.
func TestAHookIsToldWhatAQueryDid(t *testing.T) {
	var h heard
	e := ursus.NewEngine(ursus.WithEngineThreads(4), ursus.WithEngineQueryHook(h.hook))
	on := ursus.WithEngine(e)
	ctx := t.Context()
	c := ursus.Col
	one := func(t *testing.T, n int) ursus.QueryStats {
		t.Helper()
		got, _ := h.since(n)
		if len(got) != 1 {
			t.Fatalf("%d reports, want one: %+v", len(got), got)
		}
		return got[0]
	}

	n := h.len()
	if _, err := engineFrame(t, 1_000, 10).Lazy().GroupBy(c("k")).Agg(c("v").Sum()).Collect(ctx, on); err != nil {
		t.Fatal(err)
	}
	if s := one(t, n); s.Op != "collect" || s.Rows != 10 || s.Threads != 1 || s.Peak <= 0 || s.Spills != 0 ||
		s.Duration <= 0 || s.Duration > time.Minute || s.Err != nil {
		t.Errorf("a small group-by: %+v", s)
	}

	n = h.len()
	big := engineFrame(t, 200_000, 1_000).Lazy().Sort(ursus.Asc(c("v")))
	if _, err := big.Collect(ctx, on, ursus.WithMemoryLimit(256<<10), ursus.WithSpillDir(t.TempDir())); err != nil {
		t.Fatal(err)
	}
	if s := one(t, n); s.Rows != 200_000 || s.Threads != 4 || s.Spills == 0 || s.Err != nil {
		t.Errorf("a sort under 256 KiB: %+v", s)
	}

	n = h.len()
	var first int
	for b, err := range big.CollectBatches(ctx, on) {
		if err != nil {
			t.Fatal(err)
		}
		first = b.Height()
		break
	}
	if s := one(t, n); s.Op != "collect_batches" || s.Rows != int64(first) || s.Err != nil {
		t.Errorf("a stream broken off after %d rows: %+v", first, s)
	}

	n = h.len()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := big.Collect(cancelled, on); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled query: %v", err)
	}
	if s := one(t, n); !errors.Is(s.Err, context.Canceled) {
		t.Errorf("a cancelled query: %+v", s)
	}

	n = h.len()
	_, err := engineFrame(t, 10, 2).Lazy().Select(c("nope")).Collect(ctx, on)
	var ue *ursus.Error
	if s := one(t, n); err == nil || !errors.As(s.Err, &ue) || ue.Kind != ursus.KindSchema || s.Rows != 0 {
		t.Errorf("an unknown column: %+v", s)
	}

	n = h.len()
	dir := filepath.Join(t.TempDir(), "parts")
	if err := engineFrame(t, 1_000, 10).Lazy().SinkParquet(ctx, dir, ursus.WithPartitionBy("k"), on); err != nil {
		t.Fatal(err)
	}
	if s := one(t, n); s.Op != "sink_parquet" || s.Rows != 1_000 || s.Err != nil {
		t.Errorf("a partitioned write: %+v", s)
	}

	n = h.len()
	if _, err := engineFrame(t, 10, 2).Lazy().Collect(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.since(n); len(got) != 0 {
		t.Errorf("a query on another engine reported to this one: %+v", got)
	}
}
