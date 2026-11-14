package gen

import (
	"bytes"
	"context"
	"go/parser"
	"go/token"
	"slices"
	"testing"

	"github.com/apache/arrow-go/v18/arrow/ipc"

	"github.com/advenn/ursus/qcheck/ast"
	"github.com/advenn/ursus/qcheck/build"
	"github.com/advenn/ursus/qcheck/model"
)

// recordBytes is t's record as an Arrow IPC stream, which compares two tables to
// the bit.
func recordBytes(t *testing.T, tb *model.Table) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := ipc.NewWriter(&buf, ipc.WithSchema(tb.Rec.Schema()))
	if err := w.Write(tb.Rec); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestACaseIsItsSeed: a seed alone rebuilds a case, its tables to the bit and its
// query to the character, and two seeds draw two cases.
func TestACaseIsItsSeed(t *testing.T) {
	for seed := uint64(1); seed <= 50; seed++ {
		a, b := NewCase(seed), NewCase(seed)
		if a.String() != b.String() {
			t.Fatalf("seed %d: two cases:\n%s\n%s", seed, a, b)
		}
		for i := range a.Tables {
			if !bytes.Equal(recordBytes(t, a.Tables[i]), recordBytes(t, b.Tables[i])) {
				t.Fatalf("seed %d: table %s differs between two draws", seed, a.Tables[i].Name)
			}
		}
	}
	if NewCase(1).String() == NewCase(2).String() {
		t.Fatal("seeds 1 and 2 draw one case")
	}
}

// TestTheTablesHoldStep453sShapes: over many seeds the tables hold each base
// type, each null rate, each profile, empty tables and the batch's edges, and
// every value a profile names is the type its column declares.
func TestTheTablesHoldStep453sShapes(t *testing.T) {
	types, rates, profiles, sizes := map[model.Type]bool{}, map[float64]bool{}, map[model.Profile]bool{}, map[int]bool{}
	for seed := uint64(1); seed <= 400; seed++ {
		tb := Table(seed, "t", 8193)
		sizes[tb.Rows] = true
		if int(tb.Rec.NumRows()) != tb.Rows || int(tb.Rec.NumCols()) != len(tb.Cols) {
			t.Fatalf("seed %d: a record of %d rows and %d columns for %v", seed, tb.Rec.NumRows(), tb.Rec.NumCols(), tb)
		}
		for i, c := range tb.Cols {
			types[c.Type], rates[c.NullRate], profiles[c.Profile] = true, true, true
			if !arrowTypeIs(tb.Rec.Column(i).DataType(), c.Type) {
				t.Fatalf("seed %d: column %s is %s", seed, c.Name, tb.Rec.Column(i).DataType())
			}
			if c.NullRate == 1 && tb.Rec.Column(i).NullN() != tb.Rows {
				t.Fatalf("seed %d: column %s of null rate 1 has values", seed, c.Name)
			}
		}
	}
	for _, typ := range model.Base {
		if !types[typ] {
			t.Errorf("no column of %s", typ)
		}
	}
	for _, r := range NullRates {
		if !rates[r] {
			t.Errorf("no column of null rate %v", r)
		}
	}
	for p := model.Tame; p <= model.Extreme; p++ {
		if !profiles[p] {
			t.Errorf("no column of profile %v", p)
		}
	}
	for _, n := range append([]int{0}, edgeSizes...) {
		if !sizes[n] {
			t.Errorf("no table of %d rows", n)
		}
	}
}

func arrowTypeIs(dt interface{ String() string }, t model.Type) bool {
	return dt.String() == ArrowType(t).String()
}

// TestQueriesAreWellTypedAndUrsusAgrees: every query a seed draws checks, its
// bounded twin too, and ursus's CollectSchema of it is the AST's: the names and
// the types the AST computed from its leaves, a differential of the AST's type
// rules against ursus's.
func TestQueriesAreWellTypedAndUrsusAgrees(t *testing.T) {
	ops := map[string]bool{}
	for seed := uint64(1); seed <= 300; seed++ {
		c := NewCase(seed)
		tables := c.TableMap()
		if err := ast.Check(c.Query, tables); err != nil {
			t.Fatalf("seed %d: %v\n%s", seed, err, c)
		}
		q, _ := ast.Bounded(c.Query)
		info, err := ast.Schema(q.Root, tables)
		if err != nil {
			t.Fatalf("seed %d: the bounded query: %v\n%s", seed, err, c)
		}
		lf := build.Frame(q.Root, build.RecordsOf(c.Tables, 1))
		s, err := lf.CollectSchema(context.Background())
		if err != nil {
			t.Fatalf("seed %d: ursus refuses the query: %v\n%s", seed, err, c)
		}
		var want, got []string
		for _, col := range info.Cols {
			want = append(want, col.Name+" "+col.Type.String())
		}
		for i := range s.Len() {
			got = append(got, s.Field(i).Name+" "+build.TypeOf(s.Field(i).Type).String())
		}
		if !slices.Equal(want, got) {
			t.Fatalf("seed %d: ursus's schema %v, the AST's %v\n%s", seed, got, want, c)
		}
		for _, op := range c.Query.Ops() {
			ops[op] = true
		}
	}
	for _, op := range []string{"Filter", "Select", "WithColumns", "GroupBy", "Sort", "Head", "Slice",
		"JoinInner", "JoinLeft", "JoinSemi", "JoinAnti", "Count"} {
		if !ops[op] {
			t.Errorf("no query of %s in 300 seeds", op)
		}
	}
}

// TestAQueryPrintsAsGo: a query's Go is Go, statements a test can hold, so a finding
// reads as the code that reproduces it.
func TestAQueryPrintsAsGo(t *testing.T) {
	for seed := uint64(1); seed <= 200; seed++ {
		c := NewCase(seed)
		src := "package p\nfunc f() {\n" + c.Query.Go() + "\n}\n"
		if _, err := parser.ParseFile(token.NewFileSet(), "q.go", src, 0); err != nil {
			t.Fatalf("seed %d: %v\n%s", seed, err, src)
		}
	}
}
