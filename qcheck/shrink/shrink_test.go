package shrink

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/advenn/ursus/qcheck/ast"
	"github.com/advenn/ursus/qcheck/build"
	"github.com/advenn/ursus/qcheck/compare"
	"github.com/advenn/ursus/qcheck/model"
	"github.com/advenn/ursus/qcheck/sqlgen"
)

// The known bug TestShrinkerReachesMinimum plants: a wrong answer, one row too many
// or a Count one more, under any configuration but the reference, whenever a Filter
// reads an Int64 column of a table that holds the value 7. Its minimal case is one
// table of one row and one column, the 7, under one operation, the Filter.
const planted = 7

func plantBug(t *testing.T) {
	t.Helper()
	corrupt = func(tables map[string]*model.Table, q *ast.Query, cfg model.Config, a *compare.Answer) {
		if cfg == model.Reference() || !filterMeetsPlanted(tables, q) {
			return
		}
		if a.IsCount {
			a.Count++
			return
		}
		a.Rows++
		for j := range a.Cells {
			a.Cells[j] = append(a.Cells[j], "")
		}
	}
	t.Cleanup(func() { corrupt = nil })
}

// filterMeetsPlanted is whether a Filter of q reads an Int64 column of a base table
// that holds the planted value.
func filterMeetsPlanted(tables map[string]*model.Table, q *ast.Query) bool {
	found := false
	ast.Walk(q.Root, func(f ast.Frame) {
		flt, ok := f.(*ast.Filter)
		if !ok {
			return
		}
		in, err := ast.Schema(flt.In, tables)
		if err != nil {
			return
		}
		for _, name := range ast.Reads(flt.Pred) {
			if c := in.Col(name); c != nil && c.Type == model.Int64 && c.Origin != "" && holds(tables, c.Origin, planted) {
				found = true
			}
		}
	})
	return found
}

func holds(tables map[string]*model.Table, origin string, v int64) bool {
	tname, cname, _ := strings.Cut(origin, ".")
	t := tables[tname]
	idx := slices.IndexFunc(t.Cols, func(c model.Column) bool { return c.Name == cname })
	col := t.Rec.Column(idx).(*array.Int64)
	for i := range col.Len() {
		if col.IsValid(i) && col.Value(i) == v {
			return true
		}
	}
	return false
}

// plantedCases are the first n generated cases from seed on that the planted bug
// fails, big ones: hundreds of rows or more, two operations or more, and no join,
// whose right side's columns a shrink step cannot rename back.
func plantedCases(t *testing.T, seed uint64, n int, mode string) []*Case {
	t.Helper()
	var out []*Case
	pairs := model.Pairwise()
	for s := seed; len(out) < n && s < seed+5000; s++ {
		cfg := pairs[s%uint64(len(pairs))]
		c := FromSeed(SourceSelf, s, mode, model.Reference(), cfg)
		if mode == ModeDuck {
			c.Source = SourceDuck
		}
		if cfg.Records == 0 {
			c.Config.Records = 3
		}
		size := c.Size()
		joins := false
		ast.Walk(c.Query.Root, func(f ast.Frame) {
			if _, ok := f.(*ast.Join); ok {
				joins = true
			}
		})
		tables, err := c.TableMap()
		if err != nil {
			t.Fatal(err)
		}
		if joins || size.Rows < 300 || size.Ops < 2 || !filterMeetsPlanted(tables, c.Query) {
			continue
		}
		out = append(out, c)
	}
	if len(out) < n {
		t.Fatalf("%d cases the planted bug fails from seed %d, want %d", len(out), seed, n)
	}
	return out
}

// minimal fails unless res's case is the planted bug's minimum: one table, one row,
// one column, an Int64 holding 7, one operation, a Filter that reads it, and one
// configuration factor.
func minimal(t *testing.T, from *Case, res *Result) {
	t.Helper()
	c := res.Case
	size := c.Size()
	desc := fmt.Sprintf("seed %d, from %v to %v in %d runs:\n%s", from.Seed, from.Size(), size, res.Runs, c.Query.Go())
	if size.Tables != 1 || size.Rows != 1 || size.Columns != 1 || size.Ops != 1 || size.Factors != 1 {
		t.Fatalf("not minimal: %s", desc)
	}
	col := c.Tables[0].Cols[0]
	if col.Type != model.Int64 || col.Values[0].Null || col.Values[0].I != planted {
		t.Fatalf("the one value is %+v of %s, not the planted %d: %s", col.Values[0], col.Type, planted, desc)
	}
	f, ok := c.Query.Root.(*ast.Filter)
	if !ok || !slices.Contains(ast.Reads(f.Pred), col.Name) {
		t.Fatalf("the one operation is not a Filter reading %s: %s", col.Name, desc)
	}
	if res.Reproduced != 3 {
		t.Fatalf("the shrunk case failed %d of 3 times again: %s", res.Reproduced, desc)
	}
	t.Logf("%s", desc)
}

// TestShrinkerReachesMinimum plants a known minimal bug through the test hook and
// shrinks big random cases it fails: each must come out as its minimum, one
// operation, one row, one column, the value 7, under one factor. Through the self
// check's differential, and through package compare's against an engine, ursus at
// the reference standing in for DuckDB, which the laptop never links.
func TestShrinkerReachesMinimum(t *testing.T) {
	plantBug(t)
	t.Run("differential", func(t *testing.T) {
		for _, c := range plantedCases(t, 514_000, 3, ModeDifferential) {
			res, err := Shrink(c, Options{})
			if err != nil {
				t.Fatalf("seed %d: %v", c.Seed, err)
			}
			minimal(t, c, res)
			if want := (Signature{Kind: KindMismatch, What: compare.DiffRows}); res.Case.Signature != want &&
				res.Case.Signature != (Signature{Kind: KindMismatch, What: compare.DiffCount}) {
				t.Fatalf("seed %d: signature %v", c.Seed, res.Case.Signature)
			}
		}
	})
	t.Run("duck", func(t *testing.T) {
		for _, c := range plantedCases(t, 515_000, 2, ModeDuck) {
			res, err := Shrink(c, Options{Engine: &ursusEngine{}})
			if err != nil {
				t.Fatalf("seed %d: %v", c.Seed, err)
			}
			minimal(t, c, res)
			if _, err := EmitSQL(res.Case); err != nil {
				t.Fatalf("seed %d: the SQL: %v", c.Seed, err)
			}
		}
	})
}

// TestShrinkingIsDeterministic: a seed's case shrinks to one case, to the byte,
// every time.
func TestShrinkingIsDeterministic(t *testing.T) {
	plantBug(t)
	c := plantedCases(t, 516_000, 1, ModeDifferential)[0]
	var files [][]byte
	for range 2 {
		res, err := Shrink(c, Options{})
		if err != nil {
			t.Fatal(err)
		}
		res.Case.Shrunk.Seconds = 0
		b, err := Marshal(res.Case)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, b)
	}
	if !bytes.Equal(files[0], files[1]) {
		t.Fatalf("two shrinks of seed %d:\n%s\n%s", c.Seed, files[0], files[1])
	}
}

// TestAPassingCaseIsNotShrunk: a case that does not fail is said to, and one whose
// file names another signature than it fails with is refused.
func TestAPassingCaseIsNotShrunk(t *testing.T) {
	c := FromSeed(SourceSelf, 514_000, ModeDifferential, model.Reference(), model.Pairwise()[3])
	if _, err := Shrink(c, Options{}); err != ErrPasses {
		t.Fatalf("a case that passes: %v", err)
	}
	plantBug(t)
	c = plantedCases(t, 514_000, 1, ModeDifferential)[0]
	c.Signature = Signature{Kind: KindMismatch, What: compare.DiffCell, Op: "Min", Type: "Float64"}
	if _, err := Shrink(c, Options{}); err == nil || !strings.Contains(err.Error(), "its file says") {
		t.Fatalf("a case of another signature: %v", err)
	}
}

// ursusEngine stands in for DuckDB on the laptop: it answers the query's tree in
// ursus at the reference and hands it over as Arrow, as package duck's tests' does.
type ursusEngine struct{ tables []*model.Table }

func (e *ursusEngine) Load(tables []*model.Table) error { e.tables = tables; return nil }

func (e *ursusEngine) Answer(ctx context.Context, q *ast.Query, s *sqlgen.SQL) (*arrow.Schema, []arrow.RecordBatch, error) {
	lf := build.Frame(q.Root, build.RecordsOf(e.tables, 1))
	opts := build.Options(model.Reference())
	if q.Count {
		n, err := lf.Count(ctx, opts...)
		if err != nil {
			return nil, nil, err
		}
		b := array.NewInt64Builder(memory.NewGoAllocator())
		defer b.Release()
		b.Append(n)
		arr := b.NewArray()
		defer arr.Release()
		schema := arrow.NewSchema([]arrow.Field{{Name: "n", Type: arrow.PrimitiveTypes.Int64}}, nil)
		return schema, []arrow.RecordBatch{array.NewRecordBatch(schema, []arrow.Array{arr}, 1)}, nil
	}
	df, err := lf.Collect(ctx, opts...)
	if err != nil {
		return nil, nil, err
	}
	rec, err := df.Record()
	if err != nil {
		return nil, nil, err
	}
	return rec.Schema(), []arrow.RecordBatch{rec}, nil
}

func (e *ursusEngine) Counts(ctx context.Context, q *ast.Query, s *sqlgen.SQL) ([]int64, error) {
	return make([]int64, len(s.Nodes)), nil
}
