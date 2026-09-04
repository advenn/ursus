// Package qframeengine runs the h2o group-by queries against tobgu/qframe.
//
// qframe is the other pure-Go dataframe library people reach for, and the
// faster of the two. Like gota it is eager and CSV-only, and its built-in
// aggregations are a short list that differs by column type: float columns get
// sum/avg/max/min, integer columns get only sum/max/min. That last asymmetry is
// why gb4 (mean of three columns, two of them integers) reads v1 and v2 as
// floats — it is the only way qframe can express the query, and it is noted in
// the report rather than hidden.
//
// Anything needing a median, a standard deviation, a window or a join is out of
// reach, which is most of both suites.
package qframeengine

import (
	"context"
	"os"

	"github.com/tobgu/qframe"
	"github.com/tobgu/qframe/config/csv"
	"github.com/tobgu/qframe/config/groupby"

	"ursusbench/engine"
)

func init() { engine.Register("qframe", Build) }

type query struct {
	by []string
	// aggregations, in output order. `fn` is a qframe built-in id.
	aggs []aggregation
	// types forces a CSV column to a type qframe can aggregate the way the
	// query needs. Empty means "let qframe infer".
	types map[string]string
	// read lists the columns to parse; everything else is dropped at load.
	read []string
}

type aggregation struct{ fn, column, as string }

var queries = map[string]query{
	"gb1": {
		by:   []string{"id1"},
		aggs: []aggregation{{"sum", "v1", "v1"}},
		read: []string{"id1", "v1"},
	},
	"gb2": {
		by:   []string{"id1", "id2"},
		aggs: []aggregation{{"sum", "v1", "v1"}},
		read: []string{"id1", "id2", "v1"},
	},
	"gb3": {
		by:   []string{"id3"},
		aggs: []aggregation{{"sum", "v1", "v1"}, {"avg", "v3", "v3"}},
		read: []string{"id3", "v1", "v3"},
	},
	"gb4": {
		by:   []string{"id4"},
		aggs: []aggregation{{"avg", "v1", "v1"}, {"avg", "v2", "v2"}, {"avg", "v3", "v3"}},
		// qframe has no avg for integer columns, so v1 and v2 are parsed as
		// floats. Same values, different storage; noted in the report.
		types: map[string]string{"v1": "float", "v2": "float"},
		read:  []string{"id4", "v1", "v2", "v3"},
	},
	"gb5": {
		by: []string{"id6"},
		aggs: []aggregation{
			{"sum", "v1", "v1"}, {"sum", "v2", "v2"}, {"sum", "v3", "v3"},
		},
		read: []string{"id6", "v1", "v2", "v3"},
	},
}

// Build resolves the query, or explains why qframe cannot run it.
func Build(a engine.Args) (engine.Once, error) {
	if a.Suite != "h2o" {
		return nil, engine.Unsupported(
			"qframe has no join API; only the h2o basic group-by queries fit")
	}
	q, ok := queries[a.Query]
	if !ok {
		return nil, engine.Unsupported(
			"qframe: %s needs a median, a standard deviation, a window or a join, "+
				"none of which qframe has", a.Query)
	}
	if a.IO != "csv" {
		return nil, engine.Unsupported(
			"qframe has no Parquet reader; run with IO=csv to include it")
	}

	path := a.TablePath("g1")

	return func(ctx context.Context) (engine.Answer, error) {
		frame, err := load(path, q)
		if err != nil {
			return nil, err
		}

		aggs := make([]qframe.Aggregation, len(q.aggs))
		for i, a := range q.aggs {
			aggs[i] = qframe.Aggregation{Fn: a.fn, Column: a.column, As: a.as}
		}
		result := frame.GroupBy(groupby.Columns(q.by...)).Aggregate(aggs...)
		if result.Err != nil {
			return nil, result.Err
		}
		return collect(result, q)
	}, nil
}

func load(path string, q query) (qframe.QFrame, error) {
	handle, err := os.Open(path)
	if err != nil {
		return qframe.QFrame{}, err
	}
	defer handle.Close()

	options := []csv.ConfigFunc{}
	if len(q.types) > 0 {
		options = append(options, csv.Types(q.types))
	}
	frame := qframe.ReadCSV(handle, options...)
	if frame.Err != nil {
		return frame, frame.Err
	}
	return frame.Select(q.read...), nil
}

func collect(result qframe.QFrame, q query) (engine.Answer, error) {
	answer := &engine.SimpleAnswer{Columns: map[string]any{}}

	for _, name := range q.by {
		answer.Names = append(answer.Names, name)
		if view, err := result.StringView(name); err == nil {
			values := make([]string, view.Len())
			for i := range values {
				if s := view.ItemAt(i); s != nil {
					values[i] = *s
				}
			}
			answer.Columns[name] = values
			continue
		}
		view, err := result.IntView(name)
		if err != nil {
			return nil, err
		}
		values := make([]int64, view.Len())
		for i := range values {
			values[i] = int64(view.ItemAt(i))
		}
		answer.Columns[name] = values
	}

	for _, a := range q.aggs {
		answer.Names = append(answer.Names, a.as)
		if view, err := result.FloatView(a.as); err == nil {
			values := make([]float64, view.Len())
			for i := range values {
				values[i] = view.ItemAt(i)
			}
			answer.Columns[a.as] = values
			continue
		}
		view, err := result.IntView(a.as)
		if err != nil {
			return nil, err
		}
		values := make([]int64, view.Len())
		for i := range values {
			values[i] = int64(view.ItemAt(i))
		}
		answer.Columns[a.as] = values
	}
	return answer, nil
}
