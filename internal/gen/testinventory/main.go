// Command testinventory counts the test suite's hand-typed expected values, and holds
// them to a ratchet (step 504, the last of context_files/oracle-v2-scope.md).
//
// Since 2026-11-05 a test's expected data comes from a recorded oracle (Polars 2.1.0's
// answers under testdata/oracle), a Go oracle (math/big, strconv, time, a brute force)
// or a differential, never typed by hand. This command parses every *_test.go of the
// module but bench/, docs/examples/, testdata/ and the oracle registries
// (oracle_<group>_test.go, whose literals are queries' parameters, not answers), and
// counts in each Test or Fuzz function the source lines that hold a literal (a
// number, a string, a rune, true or false, and nil among a table's or a
// constructor's values) in an expected position:
//
//   - the right side of an assignment to a want (want…, expect…, golden…, exp, expX),
//     and a want ranged over: for _, want := range []int64{…};
//   - a want field of a composite literal, keyed or by its place in the struct: a case
//     table's;
//   - a number or a non-empty string compared by == or != with a value read from a
//     frame: a reader's call (Get, At, Item, Value, the root package's cells, texts,
//     numbers, i64col, int64Col), or a variable (got…, v, r, val, out, row) assigned
//     what reads a frame, collects a query, or calls a helper that does;
//   - an argument after the first of slices.Equal, reflect.DeepEqual, bytes.Equal,
//     maps.Equal, an Equals, Equal or Cmp method, ursustest.AssertFrameEqual's want, and
//     a want argument of a test helper or closure whose parameter is named as a want;
//   - through a variable or a case's field met in one of these and used nowhere else:
//     assertPivot(t, …, k, tc.x) counts k's value and the table's x values.
//
// In an expected position only the value's data is counted: a literal, a composite's
// elements, a constructor's arguments (ursus.Values, Frame, Lit, time.Date,
// big.NewInt, i128's, a test helper's where the parameter takes data), a conversion's
// operand, a constant expression's operands. Not a method's arguments, a
// computation's (math, strconv, fmt, a Go oracle's helper: naive…, model…, goX), a
// map's keys, a name (a first string argument of several, a parameter named name,
// col or path, Col("x")), a file's name, a size, a seed or a flag (a helper's lone
// integer or Boolean parameter), or a formula with a variable in it.
//
// Not counted, as structure: a want whose name says it expects an error, a plan, a
// type, a shape, an order or a mechanism (wantErr, wantPlan, wantType, wantHeight,
// wantOp…); a want whose every use, within its case table's loop, is an error's or a
// plan's text (strings.Contains, errors.Is) or is compared with a shape, a type, a
// name or an error (Height(), len(), DataType(), Names(), Error(), a variable holding
// one); a want built a row at a time in a loop, a model's; a literal in a failure's
// message; a function named for a refusal (…Refus…, …Reject…) or whose every
// assertion sits under a condition on an error. A line counts once, under its Test
// function.
//
// It is a ratchet, not a proof: it sees expected positions by their names and shapes,
// so a value named otherwise and compared with something it does not take for a
// frame's read, a package-level table, a golden file and oracleDifferences' cells are
// not seen; and a value it sees may be a documented rule's pin or an ursus-only
// mechanism's rather than a pasted answer. baseline.txt holds every function's count.
// -check fails on a function above it or absent from it with a count, and prints the
// lines; a count below passes with a note to rewrite it.
//
//	go run ./internal/gen/testinventory          # the totals
//	go run ./internal/gen/testinventory -list    # file, function, count
//	go run ./internal/gen/testinventory -lines   # each counted line, its literal and position
//	go run ./internal/gen/testinventory -check   # the ratchet: make testinventory
//	go run ./internal/gen/testinventory -write   # rewrite the baseline: make testinventory-write
package main

import (
	"bufio"
	"cmp"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const baselineFile = "internal/gen/testinventory/baseline.txt"

func main() {
	dir := flag.String("dir", ".", "the module's root")
	list := flag.Bool("list", false, "print file, function and count, a function a line")
	lines := flag.Bool("lines", false, "print each counted line: file:line, function, the literal, its position")
	check := flag.Bool("check", false, "fail on a function above its count in the baseline, or new with a count")
	write := flag.Bool("write", false, "rewrite the baseline from the tree")
	flag.Parse()
	inv, err := scan(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "testinventory:", err)
		os.Exit(2)
	}
	w := bufio.NewWriter(os.Stdout)
	defer w.Flush()
	switch {
	case *write:
		if err := os.WriteFile(filepath.Join(*dir, baselineFile), []byte(formatBaseline(inv.counts())), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "testinventory:", err)
			os.Exit(2)
		}
		fmt.Fprintf(w, "testinventory: wrote %s: %d functions, %d lines\n", baselineFile, len(inv.counts()), len(inv.hits))
	case *check:
		b, err := os.ReadFile(filepath.Join(*dir, baselineFile))
		if err != nil {
			fmt.Fprintln(os.Stderr, "testinventory:", err)
			os.Exit(2)
		}
		base, err := parseBaseline(string(b))
		if err != nil {
			fmt.Fprintln(os.Stderr, "testinventory:", baselineFile+":", err)
			os.Exit(2)
		}
		if !ratchet(w, base, inv) {
			w.Flush()
			os.Exit(1)
		}
	case *list:
		for _, c := range inv.counts() {
			fmt.Fprintf(w, "%s\t%s\t%d\n", c.file, c.fn, c.n)
		}
	case *lines:
		for _, h := range inv.hits {
			fmt.Fprintf(w, "%s:%d\t%s\t%s\t%s\n", h.file, h.line, h.fn, h.lit, h.rule)
		}
	default:
		inv.totals(w)
	}
}

// inventory is the scan of a module's tests.
type inventory struct {
	files, registries int // test files scanned, oracle registries skipped
	funcs, regFuncs   int // Test functions in the files scanned, and in the registries
	lines, regLines   int // lines of the files scanned, and of the registries
	hits              []hit
}

type count struct {
	file, fn string
	n        int
}

// counts returns each function's count, sorted by file and function.
func (inv *inventory) counts() []count {
	m := map[[2]string]int{}
	for _, h := range inv.hits {
		m[[2]string{h.file, h.fn}]++
	}
	out := make([]count, 0, len(m))
	for k, n := range m {
		out = append(out, count{k[0], k[1], n})
	}
	slices.SortFunc(out, func(a, b count) int { return cmp.Or(cmp.Compare(a.file, b.file), cmp.Compare(a.fn, b.fn)) })
	return out
}

func (inv *inventory) totals(w io.Writer) {
	cs := inv.counts()
	files := map[string]bool{}
	for _, c := range cs {
		files[c.file] = true
	}
	fmt.Fprintf(w, "testinventory: %d test files, %d Test functions, %d lines; the oracle registries apart (%d files, %d lines)\n",
		inv.files, inv.funcs+inv.regFuncs, inv.lines, inv.registries, inv.regLines)
	fmt.Fprintf(w, "testinventory: %d hand-typed expected-data lines in %d functions of %d files\n",
		len(inv.hits), len(cs), len(files))
}

// skipDir reports whether a directory's tests are left out: the benchmark suite, the
// documentation's examples, a testdata tree, a nested module, a hidden directory.
func skipDir(root, path string, d fs.DirEntry) bool {
	if path == root {
		return false
	}
	name := d.Name()
	rel, _ := filepath.Rel(root, path)
	rel = filepath.ToSlash(rel)
	if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" || name == "vendor" ||
		rel == "bench" || rel == "docs/examples" {
		return true
	}
	if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
		return true
	}
	return false
}

// registry reports whether a test file is an oracle group's registry.
func registry(name string) bool {
	g, ok := strings.CutPrefix(name, "oracle_")
	return ok && strings.HasSuffix(g, "_test.go") && g != "_test.go" && g != "test.go"
}

// scan parses the module's tests under root and counts them.
func scan(root string) (*inventory, error) {
	byDir := map[string][]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDir(root, path, d) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), "_test.go") {
			byDir[filepath.Dir(path)] = append(byDir[filepath.Dir(path)], path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	inv := &inventory{}
	dirs := make([]string, 0, len(byDir))
	for d := range byDir {
		dirs = append(dirs, d)
	}
	slices.Sort(dirs)
	for _, dir := range dirs {
		paths := byDir[dir]
		slices.Sort(paths)
		fset := token.NewFileSet()
		pkg := newPkgInfo()
		files := make([]*ast.File, len(paths))
		for i, p := range paths {
			src, err := os.ReadFile(p)
			if err != nil {
				return nil, err
			}
			f, err := parser.ParseFile(fset, p, src, parser.SkipObjectResolution)
			if err != nil {
				return nil, err
			}
			files[i] = f
			n := strings.Count(string(src), "\n")
			tests := 0
			for _, d := range f.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && strings.HasPrefix(fd.Name.Name, "Test") {
					tests++
				}
			}
			if registry(filepath.Base(p)) {
				inv.registries++
				inv.regLines += n
				inv.regFuncs += tests
				continue
			}
			inv.files++
			inv.lines += n
			inv.funcs += tests
			pkg.learn(f)
		}
		for i, p := range paths {
			if registry(filepath.Base(p)) {
				continue
			}
			rel, _ := filepath.Rel(root, p)
			inv.hits = append(inv.hits, scanFile(fset, filepath.ToSlash(rel), files[i], pkg)...)
		}
	}
	slices.SortFunc(inv.hits, func(a, b hit) int {
		return cmp.Or(cmp.Compare(a.file, b.file), cmp.Compare(a.line, b.line))
	})
	return inv, nil
}

const baselineHeader = `# The hand-typed expected-data lines each test function holds, a function a line:
# file, function, count, as internal/gen/testinventory counts them (step 504). Written
# by go run ./internal/gen/testinventory -write (make testinventory-write). CI's build
# job runs -check (make testinventory), which fails on a function above its count here
# or absent from here with a count; a count that falls passes, and this file is
# rewritten with -write to hold the lower one. Since 2026-11-05 a test's expected data
# is a recorded oracle's, a Go oracle's or a differential's (context_files/
# oracle-v2-scope.md §4): what this file allows is what waves 496 to 503 left, pins of
# documented rules, ursus-only mechanisms, Go-API shapes and the files no wave took,
# and nothing new.
`

func formatBaseline(cs []count) string {
	var b strings.Builder
	b.WriteString(baselineHeader)
	for _, c := range cs {
		fmt.Fprintf(&b, "%s\t%s\t%d\n", c.file, c.fn, c.n)
	}
	return b.String()
}

func parseBaseline(s string) (map[[2]string]int, error) {
	m := map[[2]string]int{}
	for i, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		parts := strings.Split(l, "\t")
		if len(parts) != 3 {
			return nil, fmt.Errorf("line %d: want file<TAB>function<TAB>count, got %q", i+1, l)
		}
		n, err := strconv.Atoi(parts[2])
		if err != nil {
			return nil, fmt.Errorf("line %d: %v", i+1, err)
		}
		m[[2]string{parts[0], parts[1]}] = n
	}
	return m, nil
}

func lines(n int) string { return plural(n, "line", "lines") }

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// ratchet compares the tree with the baseline: it reports and fails on each function
// above its baseline or new with a count, printing its lines, and notes each below.
func ratchet(w io.Writer, base map[[2]string]int, inv *inventory) bool {
	cur := map[[2]string]int{}
	for _, c := range inv.counts() {
		cur[[2]string{c.file, c.fn}] = c.n
	}
	var over, under [][2]string
	for k, n := range cur {
		if n > base[k] {
			over = append(over, k)
		}
	}
	for k, n := range base {
		if cur[k] < n {
			under = append(under, k)
		}
	}
	less := func(a, b [2]string) int { return cmp.Or(cmp.Compare(a[0], b[0]), cmp.Compare(a[1], b[1])) }
	slices.SortFunc(over, less)
	slices.SortFunc(under, less)
	total := 0
	for _, n := range base {
		total += n
	}
	fmt.Fprintf(w, "testinventory: %d hand-typed expected-data lines, %d in the baseline\n", len(inv.hits), total)
	for _, k := range over {
		if b, ok := base[k]; ok {
			fmt.Fprintf(w, "! %s\t%s: %s, %d in the baseline\n", k[0], k[1], lines(cur[k]), b)
		} else {
			fmt.Fprintf(w, "! %s\t%s: %s, a function the baseline has not\n", k[0], k[1], lines(cur[k]))
		}
		for _, h := range inv.hits {
			if h.file == k[0] && h.fn == k[1] {
				fmt.Fprintf(w, "    %s:%d\t%s\t(%s)\n", h.file, h.line, h.lit, h.rule)
			}
		}
	}
	for _, k := range under {
		fmt.Fprintf(w, "  %s\t%s: %s, %d in the baseline: lower than baseline: rewrite with -write\n", k[0], k[1], lines(cur[k]), base[k])
	}
	if len(over) > 0 {
		fmt.Fprintf(w, "testinventory: %s hand-typed expected values the baseline does not allow, marked !: "+
			"a test's expected data is a recorded oracle's (testdata/oracle, make oracle), a Go oracle's or a differential's "+
			"(context_files/oracle-v2-scope.md §4)\n", plural(len(over), "function holds", "functions hold"))
		return false
	}
	if len(under) > 0 {
		fmt.Fprintf(w, "testinventory: ok; %s lower than baseline: rewrite with -write (make testinventory-write)\n",
			plural(len(under), "function", "functions"))
		return true
	}
	fmt.Fprintln(w, "testinventory: ok")
	return true
}
