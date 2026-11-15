package duck

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus/internal/fuzzcheck"
	"github.com/advenn/ursus/qcheck/ast"
	"github.com/advenn/ursus/qcheck/compare"
	"github.com/advenn/ursus/qcheck/model"
	"github.com/advenn/ursus/qcheck/shrink"
	"github.com/advenn/ursus/qcheck/sqlgen"
)

// TestCorpusAgainstDuckDB replays the corpus's DuckDB cases, which package shrink's
// TestCorpus leaves to a build that links DuckDB, under the same ratchet.
func TestCorpusAgainstDuckDB(t *testing.T) {
	d := open(t)
	cases, _, err := shrink.LoadCorpus("../testdata/corpus")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, c := range cases {
		if c.Mode != shrink.ModeDuck {
			continue
		}
		n++
		r, err := shrink.Replay(c, d, fuzzcheck.Limits{})
		if err != nil {
			t.Errorf("%s: %v", c.Name(), err)
			continue
		}
		if err := shrink.Judge(c, r); err != nil {
			t.Error(err)
		}
	}
	t.Logf("%d DuckDB case(s) replayed", n)
}

// cut is c with each table held as its first rows.
func cut(t *testing.T, c *shrink.Case, rows int) *shrink.Case {
	t.Helper()
	for i, tb := range c.Tables {
		m, err := tb.Model()
		if err != nil {
			t.Fatal(err)
		}
		n := min(rows, m.Rows)
		sliced := *m
		sliced.Rows, sliced.Rec = n, m.Rec.NewSlice(0, int64(n))
		h, err := shrink.FromModel(&sliced)
		if err != nil {
			t.Fatal(err)
		}
		c.Tables[i] = h
	}
	return c
}

// TestEmittedSQLRunsInDuckDB runs the script EmitSQL writes for a case, statement by
// statement, in a database of its own, and its answer must be the answer of the
// case's SQL over the tables duck.Load registers: the script holds the case.
func TestEmittedSQLRunsInDuckDB(t *testing.T) {
	d := open(t)
	ran := 0
	for seed := uint64(1); seed <= 60; seed++ {
		c := cut(t, shrink.FromSeed(shrink.SourceDuck, seed, shrink.ModeDuck, model.Reference(), model.Reference()), 12)
		tables, err := c.TableMap()
		if err != nil {
			t.Fatal(err)
		}
		bq, _ := ast.Bounded(c.Query)
		sq, err := sqlgen.Translate(bq, tables)
		var ex *sqlgen.Excluded
		if errors.As(err, &ex) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		info, err := ast.Schema(bq.Root, tables)
		if err != nil {
			t.Fatal(err)
		}
		models, err := c.Models()
		if err != nil {
			t.Fatal(err)
		}
		want := answerOf(t, d, bq, info, func(ctx context.Context) error { return d.Load(models) }, sq.Text)

		script, err := shrink.EmitSQL(c)
		if err != nil {
			t.Fatal(err)
		}
		fresh, err := Open()
		if err != nil {
			t.Fatal(err)
		}
		stmts := strings.Split(strings.TrimSpace(script), ";\n")
		last := strings.TrimSuffix(stmts[len(stmts)-1], ";")
		got := answerOf(t, fresh, bq, info, func(ctx context.Context) error {
			for _, s := range stmts[:len(stmts)-1] {
				if err := fresh.Exec(ctx, stripComments(s)); err != nil {
					return err
				}
			}
			return nil
		}, stripComments(last))
		fresh.Close()
		if v := compare.Compare(want, got, compare.Plan{Ordered: info.Ordered, Want: "Load", Got: "the script"}); v.Diff != "" {
			t.Fatalf("seed %d: the emitted script answers otherwise: %s\n%s", seed, v.Diff, script)
		}
		ran++
	}
	if ran < 30 {
		t.Fatalf("%d scripts ran", ran)
	}
}

// answerOf runs setup, then query, and reads its answer.
func answerOf(t *testing.T, d *DB, q *ast.Query, info *ast.Info, setup func(context.Context) error, query string) *compare.Answer {
	t.Helper()
	ctx := context.Background()
	if err := setup(ctx); err != nil {
		t.Fatal(err)
	}
	schema, recs, err := d.Query(ctx, query)
	if err != nil {
		t.Fatalf("%v\n%s", err, query)
	}
	defer func() {
		for _, r := range recs {
			r.Release()
		}
	}()
	cols := info.Cols
	if q.Count {
		cols = []ast.Column{{Name: "n", Type: model.Int64}}
	}
	a, err := compare.FromArrow(schema, recs, cols)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// stripComments drops a statement's leading comment lines.
func stripComments(s string) string {
	var keep []string
	for _, line := range strings.Split(s, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") {
			keep = append(keep, line)
		}
	}
	return strings.TrimSpace(strings.Join(keep, "\n"))
}
