package ursus

import (
	"context"
	"strings"
	"time"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/exec"
	"github.com/advenn/ursus/internal/physical"
	"github.com/advenn/ursus/internal/uerr"
)

// Profile runs the query as Collect does, and answers its result and a frame of how
// each node of its plan ran: Polars' LazyFrame.profile (step 369).
//
// The profile has a row a node, in the order Explain prints the plan, after a first
// row, "optimization", for the time the plan took to resolve and optimize:
//
//   - node: the node as Explain names it, indented two spaces a level;
//   - start, end: microseconds since the query began, of the node's first work and
//     its last, as Polars' start and end; null for a node that did none;
//   - rows, batches: what the node handed on;
//   - own: microseconds of the node's own work, summed over the threads that did it.
//     A batch transform's, a filter's or a projection's, is its time; an operator's,
//     a scan's, a sort's or a join's, its time less what it waited on its inputs.
//     Where an operator works on threads beside its inputs, as a parallel
//     aggregation does, only what it did beyond that wait is its own;
//   - peak_memory: the most its operators held at once, in bytes, as the memory
//     budget counts them; 0 for a node that holds nothing across batches.
//
// A profiled query runs each operator in a wrapper that reads the clock, so it takes
// a little longer; Collect builds none.
func (lf *LazyFrame) Profile(ctx context.Context, opts ...CollectOption) (df, profile *DataFrame, err error) {
	defer uerr.Catch(&err, "profile")
	start := time.Now()
	cfg := newCollectCfg(opts)
	cfg.profiler = physical.NewProfiler(start, cfg.budget)
	root, err := lf.compile(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	b, err := exec.Collect(ctx, root, cfg.budget.Result())
	cfg.report()
	if err != nil {
		return nil, nil, err
	}
	profile, err = profileFrame(cfg.profiler.Rows())
	if err != nil {
		return nil, nil, err
	}
	return &DataFrame{batch: b}, profile, nil
}

// profileFrame is Profile's frame of rows.
func profileFrame(rows []physical.ProfRow) (*DataFrame, error) {
	n := len(rows)
	node := make([]string, n)
	start, end, own := make([]uint64, n), make([]uint64, n), make([]uint64, n)
	nrows, batches, peak := make([]int64, n), make([]int64, n), make([]int64, n)
	ran := bitmap.NewBuilder(n)
	for i, r := range rows {
		node[i] = strings.Repeat("  ", r.Depth) + r.Label
		if r.Ran {
			start[i], end[i] = uint64(r.Start/1000), uint64(r.End/1000)
		}
		ran.Append(r.Ran)
		own[i] = uint64(r.Own / 1000)
		nrows[i], batches[i], peak[i] = r.Rows, r.Batches, r.Peak
	}
	valid := ran.Finish()
	return frameOfColumns([]*data.Column{
		data.NewString("node", node, bitmap.View{}),
		data.NewFixed("start", dtype.Uint64, start, valid),
		data.NewFixed("end", dtype.Uint64, end, valid),
		data.NewFixed("rows", dtype.Int64, nrows, bitmap.View{}),
		data.NewFixed("batches", dtype.Int64, batches, bitmap.View{}),
		data.NewFixed("own", dtype.Uint64, own, bitmap.View{}),
		data.NewFixed("peak_memory", dtype.Int64, peak, bitmap.View{}),
	}, n)
}
