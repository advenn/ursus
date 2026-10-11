package ursus_test

import (
	"bytes"
	"fmt"
	"slices"
	"testing"

	"github.com/advenn/ursus"
)

// TestStringConjunctsOverParquetAnswerAsInMemory: a Parquet reader handed string
// conjuncts evaluates them on each row group's dictionary and drops the rows that
// fail, ahead of the Filter (step 193). Every answer must be the in-memory frame's,
// on one thread and on four.
//
// The last case is the one the design turns on. The Filter applies its conjuncts in
// turn, so the strict cast of `code` only ever sees the rows whose kind is "num",
// and none of them holds "x". The dictionary of `code` holds "x", so evaluating
// the cast there fails, and the reader must keep the rows rather than fail the
// query.
func TestStringConjunctsOverParquetAnswerAsInMemory(t *testing.T) {
	const n = 20_000
	kinds := []string{"num", "word", "AIR", "AIR REG", "MAIL", "TRUCK", ""}
	kind, valid, code, id := make([]string, n), make([]bool, n), make([]string, n), make([]int64, n)
	for i := range n {
		kind[i], valid[i], id[i] = kinds[i%len(kinds)], i%11 != 5, int64(i)
		code[i] = fmt.Sprint(i % 9)
		if kind[i] == "word" {
			code[i] = "x"
		}
	}
	frame := ursus.Frame(ursus.ValuesNullable("kind", kind, valid), ursus.Values("code", code),
		ursus.Values("id", id))
	var pq bytes.Buffer
	if err := frame.WriteParquet(t.Context(), &pq, ursus.WithRowGroupRows(3_000)); err != nil {
		t.Fatal(err)
	}
	k, c := ursus.Col("kind"), ursus.Col("code")
	for _, q := range []struct {
		name string
		pred ursus.Expr
	}{
		{"==", k.Eq("AIR")},
		{"!=", k.Ne("MAIL")},
		{"is in", k.IsIn("AIR", "AIR REG")},
		{"not is in", k.IsIn("num", "word").Not()},
		{"starts with", k.Str().StartsWith("AIR")},
		{"contains", k.Str().Contains("R", true)},
		{"a regexp", k.Str().Contains("^A.*G$", false)},
		{"is null", k.IsNull()},
		{"is not null", k.IsNotNull()},
		{"two columns", k.Eq("num").And(c.Ne("3"))},
		{"a strict cast that fails on a value an earlier conjunct drops",
			k.Eq("num").And(c.Cast(ursus.Int64).Gt(4))},
	} {
		want := ids(t, frame.Filter(q.pred))
		for _, threads := range []int{1, 4} {
			got := ids(t, ursus.ScanParquetBytes(pq.Bytes(), "t.parquet").Filter(q.pred), ursus.WithThreads(threads))
			if !slices.Equal(got, want) {
				t.Fatalf("%s, %d threads: %d rows from Parquet, %d in memory", q.name, threads, len(got), len(want))
			}
		}
	}
}

// ids collects lf's id column.
func ids(t *testing.T, lf *ursus.LazyFrame, opts ...ursus.CollectOption) []int64 {
	t.Helper()
	df, err := lf.Select(ursus.Col("id")).Collect(t.Context(), append(opts, ursus.WithBatchSize(1_000))...)
	if err != nil {
		t.Fatal(err)
	}
	s, err := df.Column[int64]("id")
	if err != nil {
		t.Fatal(err)
	}
	return s.Values()
}
