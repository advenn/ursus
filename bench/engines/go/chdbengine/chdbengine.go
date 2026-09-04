//go:build chdb

// Package chdbengine runs the benchmark suites against chdb-go, ClickHouse
// embedded in a Go process.
//
// It runs the same SQL as the Python chdb engine, with the same two ClickHouse
// accommodations: the inputs are registered as views in a session (rather than
// substituted into the query text, which TPC-H q9's `AS nation` alias makes
// unsafe), and the settings that let ClickHouse accept unaliased joined
// subqueries and correlated subqueries are appended.
//
// Behind the `chdb` build tag because it needs libchdb.so, which is a large
// download and is not vendored. Install it with the upstream script
// (update_libchdb.sh in chdb-io/chdb-go) and then:
//
//	make setup-cgo
//	make bench ENGINES=chdbgo
package chdbengine

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/chdb-io/chdb-go/chdb"

	"ursusbench/engine"
	"ursusbench/sqlsuite"
)

func init() { engine.Register("chdbgo", Build) }

// Settings that let ClickHouse run standard TPC-H text. Neither changes what a
// query means; see the Python side for the same two.
const settings = "joined_subquery_requires_alias = 0, enable_analyzer = 1"

// ClickHouse has no `extract(field FROM value)`, only per-field functions.
var rewrites = []struct {
	pattern *regexp.Regexp
	with    string
}{
	{regexp.MustCompile(`(?i)\bextract\s*\(\s*year\s+from\s+([^)]+)\)`), "toYear($1)"},
	{regexp.MustCompile(`(?i)\bextract\s*\(\s*month\s+from\s+([^)]+)\)`), "toMonth($1)"},
	{regexp.MustCompile(`(?i)\bsubstring\s*\(\s*(\w+)\s+FROM\s+(\d+)\s+FOR\s+(\d+)\s*\)`),
		"substring($1, $2, $3)"},
}

var trailingSemicolon = regexp.MustCompile(`;\s*$`)

// Build loads the query text and returns a closure that opens a session,
// registers the inputs as views, and runs it.
func Build(a engine.Args) (engine.Once, error) {
	raw, err := sqlsuite.Read(a)
	if err != nil {
		return nil, err
	}
	query := dialect(raw)

	format := "Parquet"
	if a.IO == "csv" {
		format = "CSVWithNames"
	}

	return func(ctx context.Context) (engine.Answer, error) {
		session, err := chdb.NewSession()
		if err != nil {
			return nil, err
		}
		defer session.Close()

		for _, table := range a.Inputs() {
			view := fmt.Sprintf("CREATE OR REPLACE VIEW %s AS SELECT * FROM file('%s', %s)",
				table, a.TablePath(table), format)
			result, err := session.Query(view)
			if err != nil {
				return nil, err
			}
			if err := result.Error(); err != nil {
				result.Free()
				return nil, err
			}
			result.Free()
		}

		result, err := session.Query(query, "Arrow")
		if err != nil {
			return nil, err
		}
		if err := result.Error(); err != nil {
			result.Free()
			return nil, err
		}
		// Copy before Free: Buf() is backed by memory the session owns.
		payload := append([]byte(nil), result.Buf()...)
		result.Free()

		return newAnswer(payload)
	}, nil
}

func dialect(sql string) string {
	for _, r := range rewrites {
		sql = r.pattern.ReplaceAllString(sql, r.with)
	}
	body := trailingSemicolon.ReplaceAllString(strings.TrimSpace(sql), "")
	return body + "\nSETTINGS " + settings
}

// answer holds the decoded Arrow table. ClickHouse's Arrow output is the
// random-access file format; older builds emit a stream, so both are tried.
type answer struct {
	records []arrow.Record
	rows    int
}

func newAnswer(payload []byte) (engine.Answer, error) {
	reader, err := ipc.NewFileReader(bytes.NewReader(payload))
	if err == nil {
		defer reader.Close()
		a := &answer{}
		for i := 0; i < reader.NumRecords(); i++ {
			record, err := reader.Record(i)
			if err != nil {
				return nil, err
			}
			record.Retain()
			a.records = append(a.records, record)
			a.rows += int(record.NumRows())
		}
		return a, nil
	}

	stream, err := ipc.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	defer stream.Release()
	a := &answer{}
	for stream.Next() {
		record := stream.Record()
		record.Retain()
		a.records = append(a.records, record)
		a.rows += int(record.NumRows())
	}
	return a, stream.Err()
}

func (a *answer) Rows() int { return a.rows }

func (a *answer) Close() error {
	for _, record := range a.records {
		record.Release()
	}
	a.records = nil
	return nil
}

func (a *answer) WriteFrame(ctx context.Context, path string) error {
	if len(a.records) == 0 {
		return fmt.Errorf("chdb-go: no records to write")
	}
	// Answers in frame mode are at most a few hundred rows, so the batches are
	// concatenated by writing them one after another into the same file.
	return engine.WriteRecords(path, a.records)
}

func (a *answer) Checksum(ctx context.Context, cols []string) (map[string]float64, error) {
	sums := make(map[string]float64, len(cols))
	for _, record := range a.records {
		schema := record.Schema()
		for _, name := range cols {
			indices := schema.FieldIndices(name)
			if len(indices) == 0 {
				return nil, fmt.Errorf("answer has no column %q", name)
			}
			total, err := engine.SumColumn(record.Column(indices[0]))
			if err != nil {
				return nil, fmt.Errorf("column %q: %w", name, err)
			}
			sums[name] += total
		}
	}
	return sums, nil
}
