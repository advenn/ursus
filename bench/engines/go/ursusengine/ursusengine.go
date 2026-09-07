// Package ursusengine runs the benchmark suites against ursus.
//
// Everything here goes through the public API only. That is not a stylistic
// choice: `module ursus` is reached by a replace directive from a separate
// module, so ursus/internal/... is not importable even if we wanted it. The
// upside is that these queries are exactly what a user of the library would
// write, which is the only thing worth timing.
package ursusengine

import (
	"context"
	"fmt"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"

	"ursusbench/engine"
)

// Scan opens one input table as a LazyFrame.
type Scan func(table string) *ursus.LazyFrame

// Query builds a lazy plan for one benchmark query.
type Query func(scan Scan) *ursus.LazyFrame

// The name here is the engine's key in config/engines.toml, not the import
// path. A module rename that rewrites this string unregisters the engine: the
// driver asks for "ursus", the runner answers "unknown engine", and the subject
// of the whole suite turns into 22 ERR cells. It has happened once already.
func init() { engine.Register("ursus", Build) }

// Build resolves the query and returns a closure that runs it once.
func Build(a engine.Args) (engine.Once, error) {
	var registry map[string]Query
	switch a.Suite {
	case "pdsh":
		registry = pdshQueries
	case "h2o":
		registry = h2oQueries
	default:
		return nil, engine.Unsupported("ursus: unknown suite %q", a.Suite)
	}

	query, ok := registry[a.Query]
	if !ok {
		return nil, engine.Unsupported("ursus: %s/%s not ported", a.Suite, a.Query)
	}

	var opts []ursus.CollectOption
	if a.Threads > 0 {
		opts = append(opts, ursus.WithThreads(a.Threads))
	}

	scan := func(table string) *ursus.LazyFrame { return scanTable(a, table) }

	return func(ctx context.Context) (engine.Answer, error) {
		// The plan is rebuilt every iteration: for a lazy engine, planning and
		// reading file metadata are part of the query, and reusing a LazyFrame
		// across iterations would hide both.
		// WithMemoryStats is passed HERE and deliberately not stored on the answer:
		// the answer reuses opts for its checksum aggregation, and that second,
		// far smaller Collect would overwrite the query's figure with its own.
		var mem ursus.MemoryStats
		df, err := query(scan).Collect(ctx, append(opts, ursus.WithMemoryStats(&mem))...)
		if err != nil {
			return nil, err
		}
		// Read after Collect returns. WithMemoryStats writes at the END of the
		// query, so reading it earlier gives a partial figure — which is the shape
		// of wrong answer that looks entirely plausible.
		return &answer{frame: df, opts: opts, accounted: mem.Peak}, nil
	}, nil
}

func scanTable(a engine.Args, table string) *ursus.LazyFrame {
	path := a.TablePath(table)
	if a.IO != "csv" {
		return ursus.ScanParquet(path)
	}
	// ursus's CSV inference covers Bool, Int64, Float64 and String but not
	// dates, so the temporal columns are declared. Every other engine gets the
	// equivalent (polars try_parse_dates, pandas parse_dates); leaving them as
	// strings here would make the date predicates unpushable and would be
	// measuring the wrong thing.
	if overrides := csvDateOverrides(a.Suite, table); len(overrides) > 0 {
		return ursus.ScanCSV(path, ursus.WithSchemaOverrides(overrides))
	}
	return ursus.ScanCSV(path)
}

func csvDateOverrides(suite, table string) map[string]dtype.DataType {
	if suite != "pdsh" {
		return nil
	}
	switch table {
	case "lineitem":
		return map[string]dtype.DataType{
			"l_shipdate":    ursus.Date,
			"l_commitdate":  ursus.Date,
			"l_receiptdate": ursus.Date,
		}
	case "orders":
		return map[string]dtype.DataType{"o_orderdate": ursus.Date}
	default:
		return nil
	}
}

// answer adapts a collected ursus DataFrame to the harness contract.
type answer struct {
	frame *ursus.DataFrame
	opts  []ursus.CollectOption

	// accounted is the query's own peak retention, captured before the checksum
	// aggregation below could overwrite it. See where it is set.
	accounted int64
}

func (a *answer) Rows() int { return a.frame.Height() }

// AccountedBytes implements engine.Accounted: what ursus believes it retained,
// against the VmHWM the harness reports beside it.
func (a *answer) AccountedBytes() int64 { return a.accounted }

func (a *answer) WriteFrame(ctx context.Context, path string) error {
	return a.frame.Lazy().SinkParquet(ctx, path)
}

// Checksum sums the requested columns with ursus itself, on the already
// materialised answer. Cheaper and more faithful than walking the frame cell by
// cell through the public accessors, and it runs outside the timed region.
func (a *answer) Checksum(ctx context.Context, cols []string) (map[string]float64, error) {
	sums := make(map[string]float64, len(cols))
	if a.frame.Height() == 0 {
		return sums, nil
	}

	exprs := make([]ursus.Expr, 0, len(cols))
	for _, name := range cols {
		// Cast first: an integer Sum in ursus widens to Int128, and the
		// checksum contract is float64 everywhere.
		exprs = append(exprs, ursus.Col(name).Cast(ursus.Float64).Sum().Alias("sum_"+name))
	}

	totals, err := a.frame.Lazy().GroupBy().Agg(exprs...).Collect(ctx, a.opts...)
	if err != nil {
		return nil, fmt.Errorf("checksum: %w", err)
	}
	if totals.Height() == 0 {
		return sums, nil
	}

	for _, name := range cols {
		value, ok, err := totals.At[float64](0, "sum_"+name)
		if err != nil {
			return nil, fmt.Errorf("checksum %s: %w", name, err)
		}
		if ok {
			sums[name] = value
		}
	}
	return sums, nil
}
