package main

import (
	"bytes"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Each source below marks the lines the classifier must count with a "// counted"
// comment, and every other line must not be: the expected answer is read from the
// source itself, not typed beside it.

const wantTable = `package x_test

import (
	"slices"
	"testing"

	"github.com/advenn/ursus"
)

func TestTable(t *testing.T) {
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want []string
	}{
		{"a", ursus.Frame(ursus.Values("x", []int64{1, 2})), []string{"1", "2"}}, // counted
		{"b", ursus.Frame(ursus.Values("x", []int64{3})),
			[]string{"3"}}, // counted
	} {
		if got := texts(t, tc.q); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
	want := ursus.Frame(ursus.Values("y", []float64{1.5, 2.5})) // counted
	_ = want
	if v, ok := frame(t).Column("x").Get(0); !ok || v != 7 { // counted
		t.Error("x")
	}
}
`

const errorSubstring = `package x_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

func TestErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		q    *ursus.LazyFrame
		want string
	}{
		{"a", ursus.Frame(ursus.Values("x", []int64{1})), "cannot reshape 6 values"},
		{"b", ursus.Frame(ursus.Values("x", []int64{2})), "not defined"},
	} {
		if _, err := tc.q.Collect(t.Context()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want an error naming %q", tc.name, err, tc.want)
		}
	}
	wantErr := "no column"
	_ = wantErr
	// An assertion on a value, so that the function is not one of errors alone.
	if got := texts(t, ursus.Frame(ursus.Values("x", []int64{1}))); !slices.Equal(got, other(t)) {
		t.Error("a value")
	}
}

func TestEvenAWantTableRefuses(t *testing.T) {
	want := []int64{1, 2, 3}
	_ = want
}
`

const heightCheck = `package x_test

import "testing"

func TestShape(t *testing.T) {
	df := collect(t)
	if got := df.Height(); got != 3 {
		t.Errorf("height %d", got)
	}
	if df.Width() != 2 || len(df.Columns()) != 2 {
		t.Error("width")
	}
	wantHeight := 4
	if df.Height() != wantHeight {
		t.Error("height")
	}
	want := 5
	if got := df.Height(); got != want {
		t.Error("height again")
	}
	for i := range 10 {
		if r := i; r == 3 {
			t.Log("an index")
		}
	}
}
`

const dtypeCheck = `package x_test

import (
	"testing"

	"github.com/advenn/ursus"
)

func TestTypes(t *testing.T) {
	for _, tc := range []struct {
		dt   ursus.DataType
		want string
	}{
		{ursus.Int64, "Int64"},
		{ursus.Float64, "Float64"},
	} {
		if got := tc.dt.String(); got != tc.want {
			t.Errorf("%s", got)
		}
	}
	want := ursus.Int64
	if got := collect(t).Schema().Field(0).Type; got != want {
		t.Error("type")
	}
	if got := collect(t).Schema().String(); got != "{x: Int64}" {
		t.Error("schema")
	}
}
`

// counted returns the lines the classifier counts in src, and the lines src marks.
func counted(t *testing.T, src string) (got, marked map[int]bool) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x_test.go", src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	pkg := newPkgInfo()
	pkg.learn(f)
	got = map[int]bool{}
	for _, h := range scanFile(fset, "x_test.go", f, pkg) {
		got[h.line] = true
	}
	marked = map[int]bool{}
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			if strings.TrimSpace(strings.TrimPrefix(c.Text, "//")) == "counted" {
				marked[fset.Position(c.Pos()).Line] = true
			}
		}
	}
	return got, marked
}

func assertCounted(t *testing.T, src string) {
	t.Helper()
	got, marked := counted(t, src)
	if !maps.Equal(got, marked) {
		t.Errorf("counted lines %v, marked %v", slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(marked)))
	}
}

// TestAWantTableIsCounted: a case table's want field, a want variable and a value
// read from a frame compared with a literal are each counted, a line once; the case's
// name and its input are not.
func TestAWantTableIsCounted(t *testing.T) {
	got, marked := counted(t, wantTable)
	if len(marked) == 0 || !maps.Equal(got, marked) {
		t.Errorf("counted lines %v, marked %v", slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(marked)))
	}
}

// TestAnErrorSubstringIsNotCounted: a want whose every use is strings.Contains on an
// error is a refusal's phrase, as is a wantErr; a function named for a refusal is
// left out whole.
func TestAnErrorSubstringIsNotCounted(t *testing.T) { assertCounted(t, errorSubstring) }

// TestAHeightCheckIsNotCounted: an integer compared with Height(), Width() or len(),
// directly or through a variable, and a loop's index, are shapes.
func TestAHeightCheckIsNotCounted(t *testing.T) { assertCounted(t, heightCheck) }

// TestADtypeIsNotCounted: a type's name compared with a case's type, a dtype value
// and a schema's text are types.
func TestADtypeIsNotCounted(t *testing.T) { assertCounted(t, dtypeCheck) }

// module writes files under a new directory with a go.mod, and returns it.
func module(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	files["go.mod"] = "module example.com/x\n\ngo 1.27\n"
	for name, src := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestAnOracleRegistryIsSkipped: oracle_<group>_test.go's literals are a query's
// parameters; the same table in another file is counted. So are bench/, testdata/
// and docs/examples/.
func TestAnOracleRegistryIsSkipped(t *testing.T) {
	files := map[string]string{
		"oracle_numodds_test.go":        wantTable,
		"oracle_x_test.go":              wantTable,
		"table_test.go":                 wantTable,
		"bench/b_test.go":               wantTable,
		"testdata/t_test.go":            wantTable,
		"docs/examples/e_test.go":       wantTable,
		"internal/deeper/table_test.go": strings.Replace(wantTable, "TestTable", "TestDeeper", 1),
	}
	inv, err := scan(module(t, files))
	if err != nil {
		t.Fatal(err)
	}
	got, marked := counted(t, wantTable)
	byFile := map[string]int{}
	for _, h := range inv.hits {
		byFile[h.file]++
	}
	if want := map[string]int{"table_test.go": len(marked), "internal/deeper/table_test.go": len(marked)}; !maps.Equal(byFile, want) || !maps.Equal(got, marked) {
		t.Errorf("lines by file %v, want %v", byFile, want)
	}
	registries := 0
	for name := range files {
		if registry(filepath.Base(name)) && !strings.Contains(name, "/") {
			registries++
		}
	}
	if inv.registries != registries {
		t.Errorf("%d registries skipped, want %d", inv.registries, registries)
	}
	if registry("oracle_test.go") {
		t.Error("oracle_test.go, the loader, is not a registry")
	}
}

// inventoryOf scans one file of src as the module's.
func inventoryOf(t *testing.T, src string) *inventory {
	t.Helper()
	inv, err := scan(module(t, map[string]string{"x_test.go": src}))
	if err != nil {
		t.Fatal(err)
	}
	return inv
}

// risen is wantTable with one more hand-typed value in its test function.
var risen = strings.Replace(wantTable, "\t_ = want\n", "\t_ = want\n\twant2 := []int64{9, 8} // counted\n\t_ = want2\n", 1)

func ratchetOf(t *testing.T, baseline, tree string) (bool, string) {
	t.Helper()
	base, err := parseBaseline(formatBaseline(inventoryOf(t, baseline).counts()))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	ok := ratchet(&out, base, inventoryOf(t, tree))
	return ok, out.String()
}

// TestARatchetRiseFails: a function holding more hand-typed lines than its baseline
// fails the check, which prints the function and its lines.
func TestARatchetRiseFails(t *testing.T) {
	if risen == wantTable {
		t.Fatal("the rise was not written")
	}
	ok, out := ratchetOf(t, wantTable, risen)
	if ok || !strings.Contains(out, "! x_test.go\tTestTable") || !strings.Contains(out, "x_test.go:") {
		t.Errorf("a rise passed, or did not name its function and lines:\n%s", out)
	}
}

// TestANewFunctionFails: a function the baseline has not, with a count, fails.
func TestANewFunctionFails(t *testing.T) {
	tree := wantTable + strings.ReplaceAll(wantTable[strings.Index(wantTable, "func TestTable"):], "TestTable", "TestAnother")
	ok, out := ratchetOf(t, wantTable, tree)
	if ok || !strings.Contains(out, "TestAnother") || !strings.Contains(out, "a function the baseline has not") {
		t.Errorf("a new function passed:\n%s", out)
	}
}

// TestAFallPasses: fewer lines than the baseline pass, with a note to rewrite it; the
// same pass silently.
func TestAFallPasses(t *testing.T) {
	ok, out := ratchetOf(t, risen, wantTable)
	if !ok || !strings.Contains(out, "lower than baseline: rewrite with -write") {
		t.Errorf("a fall failed, or said nothing:\n%s", out)
	}
	ok, out = ratchetOf(t, wantTable, wantTable)
	if !ok || strings.Contains(out, "lower than baseline") {
		t.Errorf("no change:\n%s", out)
	}
}

// TestTheBaselineRoundTrips: -write's file is what -check reads.
func TestTheBaselineRoundTrips(t *testing.T) {
	cs := inventoryOf(t, risen).counts()
	m, err := parseBaseline(formatBaseline(cs))
	if err != nil {
		t.Fatal(err)
	}
	back := map[[2]string]int{}
	for _, c := range cs {
		back[[2]string{c.file, c.fn}] = c.n
	}
	if !maps.Equal(m, back) {
		t.Errorf("%v, want %v", m, back)
	}
}

// TestTheTreeHoldsToItsBaseline is the ratchet over this module, as CI's build job
// runs it: no function holds more hand-typed expected values than baseline.txt
// allows. The scan is deterministic, as a check must be: twice, the same lines.
func TestTheTreeHoldsToItsBaseline(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, baselineFile)); err != nil {
		t.Skip("not in the module:", err)
	}
	a, err := scan(root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(a.hits, b.hits) {
		t.Error("two scans of the tree differ")
	}
	src, err := os.ReadFile(filepath.Join(root, baselineFile))
	if err != nil {
		t.Fatal(err)
	}
	base, err := parseBaseline(string(src))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if !ratchet(&out, base, a) {
		t.Errorf("go run ./internal/gen/testinventory -check:\n%s", out.String())
	}
}
