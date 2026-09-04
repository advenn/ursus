// Package gotaengine runs the h2o group-by queries against go-gota/gota.
//
// gota is the best-known pure-Go dataframe library. It is fully eager, reads
// CSV and nothing else — no Parquet reader exists — and its aggregation surface
// is a fixed list of seven functions applied to named columns. That is enough
// for the six basic h2o group-by queries and nothing else in either suite: no
// expression language means no `max(v1) - min(v2)`, no window means no top-n
// within a group, and PDS-H is out of reach entirely.
//
// It is here to answer the question a Go user actually asks — "what happens if I
// just use gota?" — with a measurement rather than an opinion.
package gotaengine

import (
	"context"
	"fmt"
	"os"

	"github.com/go-gota/gota/dataframe"
	"github.com/go-gota/gota/series"

	"ursusbench/engine"
)

func init() { engine.Register("gota", Build) }

// query describes one group-by in the vocabulary gota has: group columns, and a
// list of (aggregation, column) pairs.
type query struct {
	by    []string
	aggs  []dataframe.AggregationType
	cols  []string
	names []string // output names, positionally matched to cols
}

var queries = map[string]query{
	"gb1": {by: []string{"id1"}, aggs: agg(sum), cols: []string{"v1"}, names: []string{"v1"}},
	"gb2": {by: []string{"id1", "id2"}, aggs: agg(sum), cols: []string{"v1"},
		names: []string{"v1"}},
	"gb3": {by: []string{"id3"}, aggs: agg(sum, mean), cols: []string{"v1", "v3"},
		names: []string{"v1", "v3"}},
	"gb4": {by: []string{"id4"}, aggs: agg(mean, mean, mean),
		cols: []string{"v1", "v2", "v3"}, names: []string{"v1", "v2", "v3"}},
	"gb5": {by: []string{"id6"}, aggs: agg(sum, sum, sum),
		cols: []string{"v1", "v2", "v3"}, names: []string{"v1", "v2", "v3"}},
	"gb6": {by: []string{"id4", "id5"}, aggs: agg(median, std), cols: []string{"v3", "v3"},
		names: []string{"median_v3", "sd_v3"}},
}

const (
	sum    = dataframe.Aggregation_SUM
	mean   = dataframe.Aggregation_MEAN
	median = dataframe.Aggregation_MEDIAN
	std    = dataframe.Aggregation_STD
)

func agg(types ...dataframe.AggregationType) []dataframe.AggregationType { return types }

// columns lists what each query reads, so gota is not charged for parsing the
// whole CSV when it only needs three columns of it.
var columns = map[string][]string{
	"gb1": {"id1", "v1"},
	"gb2": {"id1", "id2", "v1"},
	"gb3": {"id3", "v1", "v3"},
	"gb4": {"id4", "v1", "v2", "v3"},
	"gb5": {"id6", "v1", "v2", "v3"},
	"gb6": {"id4", "id5", "v3"},
}

// Build resolves the query, or explains why gota cannot run it.
func Build(a engine.Args) (engine.Once, error) {
	if a.Suite != "h2o" {
		return nil, engine.Unsupported(
			"gota has no join or expression API; only the h2o basic group-by queries fit")
	}
	q, ok := queries[a.Query]
	if !ok {
		return nil, engine.Unsupported(
			"gota: %s needs a window, a join or arithmetic over aggregates, none of "+
				"which gota has", a.Query)
	}
	if a.IO != "csv" {
		return nil, engine.Unsupported(
			"gota has no Parquet reader; run with IO=csv to include it")
	}

	path := a.TablePath("g1")
	wanted := columns[a.Query]

	return func(ctx context.Context) (engine.Answer, error) {
		frame, err := load(path, wanted)
		if err != nil {
			return nil, err
		}
		grouped := frame.GroupBy(q.by...)
		if grouped == nil {
			return nil, fmt.Errorf("gota: group by %v produced no groups", q.by)
		}
		result := grouped.Aggregation(q.aggs, q.cols)
		if result.Err != nil {
			return nil, result.Err
		}
		return collect(result, q)
	}, nil
}

func load(path string, wanted []string) (dataframe.DataFrame, error) {
	handle, err := os.Open(path)
	if err != nil {
		return dataframe.DataFrame{}, err
	}
	defer handle.Close()

	frame := dataframe.ReadCSV(handle, dataframe.DetectTypes(true))
	if frame.Err != nil {
		return frame, frame.Err
	}
	projected := frame.Select(wanted)
	if projected.Err != nil {
		return projected, projected.Err
	}
	return projected, nil
}

// collect pulls the result into plain slices. gota names an aggregated column
// `<col>_<AGG>`, so the output is renamed to the contract names here.
func collect(result dataframe.DataFrame, q query) (engine.Answer, error) {
	answer := &engine.SimpleAnswer{Columns: map[string]any{}}

	for _, name := range q.by {
		column := result.Col(name)
		if column.Err != nil {
			return nil, column.Err
		}
		answer.Names = append(answer.Names, name)
		if column.Type() == series.String {
			answer.Columns[name] = column.Records()
			continue
		}
		values := column.Float()
		ints := make([]int64, len(values))
		for i, v := range values {
			ints[i] = int64(v)
		}
		answer.Columns[name] = ints
	}

	produced := result.Names()
	for i, name := range q.names {
		source := aggregatedName(produced, q.cols[i], q.aggs[i])
		if source == "" {
			return nil, fmt.Errorf("gota produced %v, expected an aggregate of %q",
				produced, q.cols[i])
		}
		column := result.Col(source)
		if column.Err != nil {
			return nil, column.Err
		}
		answer.Names = append(answer.Names, name)
		answer.Columns[name] = column.Float()
	}
	return answer, nil
}

func aggregatedName(produced []string, column string, kind dataframe.AggregationType) string {
	want := column + "_" + kind.String()
	for _, name := range produced {
		if name == want {
			return name
		}
	}
	return ""
}
