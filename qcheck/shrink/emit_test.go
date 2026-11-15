package shrink

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/advenn/ursus/qcheck/ast"
	"github.com/advenn/ursus/qcheck/compare"
	"github.com/advenn/ursus/qcheck/model"
)

// small is seed's generated case with each table cut to its first rows: what an
// emitter writes, every column type and value among them, without thousands of
// rows to compile.
func small(t *testing.T, source string, seed uint64, mode string, cfg model.Config, sig Signature) *Case {
	t.Helper()
	c := FromSeed(source, seed, mode, model.Reference(), cfg)
	c.Signature = sig
	for i, tb := range c.Tables {
		h, err := tb.Held()
		if err != nil {
			t.Fatal(err)
		}
		rows := make([]int, min(h.Rows, 5))
		for r := range rows {
			rows[r] = r
		}
		c.Tables[i] = selectRows(h, rows)
	}
	tables, err := c.TableMap()
	if err != nil {
		t.Fatal(err)
	}
	if err := ast.Check(c.Query, tables); err != nil {
		t.Fatal(err)
	}
	return c
}

// emitted are cases of every mode and every branch the emitter has: the planted
// bug's shrunk cases, a Count and a frame, ordered and not; a rerun; a run failing
// ErrInternal, the allocation bound and a leak; a schema finding; DuckDB's; a
// noisy column dropped between thread counts; tables read in several records; and
// generated tables of every column type, their extremes among them.
func emitted(t *testing.T) []*Case {
	t.Helper()
	var out []*Case
	plantBug(t)
	for _, c := range plantedCases(t, 514_000, 3, ModeDifferential) {
		res, err := Shrink(c, Options{})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, res.Case)
	}
	corrupt = nil
	mis := Signature{Kind: KindMismatch, What: compare.DiffCell, Op: "Min", Type: "Float64"}
	four := model.Config{Threads: 4, Batch: 7, Verify: true, Records: 3}
	for seed := uint64(1); seed <= 12; seed++ {
		out = append(out, small(t, SourceSelf, seed, ModeDifferential, four, mis))
	}
	out = append(out,
		small(t, SourceSelf, 13, ModeRerun, four, Signature{Kind: KindRerun, What: compare.DiffRows}),
		small(t, SourceSelf, 14, ModeRun, four, Signature{Kind: KindInternal, Class: "internal error: N"}),
		small(t, SourceSelf, 15, ModeRun, four, Signature{Kind: KindAlloc}),
		small(t, SourceSelf, 16, ModeRun, four, Signature{Kind: KindLeak}),
		small(t, SourceSelf, 17, ModeSchema, model.Reference(), Signature{Kind: KindSchema}),
		small(t, SourceDuck, 18, ModeDuck, four, Signature{Kind: KindMismatch, What: compare.DiffRows}),
	)
	// A query with a noisy column, run on four threads.
	for seed := uint64(100); ; seed++ {
		c := small(t, SourceSelf, seed, ModeDifferential, four, Signature{Kind: KindMismatch, What: compare.DiffRows})
		tables, err := c.TableMap()
		if err != nil {
			t.Fatal(err)
		}
		info, err := ast.Schema(c.Query.Root, tables)
		if err != nil {
			t.Fatal(err)
		}
		noisy := false
		for _, col := range info.Cols {
			noisy = noisy || col.Noisy
		}
		if noisy && !c.Query.Count {
			out = append(out, c)
			break
		}
	}
	return out
}

// TestEmittedTestCompiles writes the emitted tests into a temporary package of
// their own, package ursus_test over this checkout's ursus, and runs go vet on it,
// which compiles them: an emitted test that does not compile never reaches CI by
// accident. A file broken on purpose must fail the same vet.
func TestEmittedTestCompiles(t *testing.T) {
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(goBin); err != nil {
		if goBin, err = exec.LookPath("go"); err != nil {
			t.Skip("no go command to vet with")
		}
	}
	cases := emitted(t)
	dir := tempModule(t)
	modes := map[string]bool{}
	var first string
	for _, c := range cases {
		src, err := EmitGo(c, "row 0 differs\n  reference: a=1\n  variant:   a=2")
		if err != nil {
			t.Fatalf("%s: %v", c.Name(), err)
		}
		if !strings.Contains(src, "package ursus_test") || !strings.Contains(src, "func "+TestName(c)+"(t *testing.T)") {
			t.Fatalf("%s: not a test of package ursus_test:\n%s", c.Name(), src)
		}
		if err := os.WriteFile(filepath.Join(dir, c.Name()+"_test.go"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		modes[c.Mode] = true
		if first == "" {
			first = src
		}
	}
	for _, m := range []string{ModeDifferential, ModeRerun, ModeRun, ModeSchema, ModeDuck} {
		if !modes[m] {
			t.Fatalf("no emitted test of mode %s", m)
		}
	}
	if out, err := vet(goBin, dir); err != nil {
		t.Fatalf("go vet of the emitted tests: %v\n%s", err, out)
	}

	// The tooth: the vet type-checks what it is given, so a file that parses but
	// does not compile fails it.
	broken := tempModule(t)
	src := strings.Replace(first, "ursus.WithMemoryLimit(0)", `ursus.WithMemoryLimit("0")`, 1)
	if src == first {
		t.Fatal("the tooth changed nothing")
	}
	if err := os.WriteFile(filepath.Join(broken, "broken_test.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := vet(goBin, broken); err == nil || !strings.Contains(out, "WithMemoryLimit") {
		t.Fatalf("go vet passed a file that does not compile: %v\n%s", err, out)
	}
}

// tempModule is a directory holding a module that requires this checkout's ursus,
// through a replace, and arrow-go at the qcheck module's version, with the qcheck
// module's go.sum: go vet then needs no network.
func tempModule(t *testing.T) string {
	t.Helper()
	qcheck, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	goVersion, arrowVersion := "", ""
	f, err := os.Open(filepath.Join(qcheck, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		switch {
		case len(fields) == 2 && fields[0] == "go":
			goVersion = fields[1]
		case len(fields) >= 2 && fields[0] == "github.com/apache/arrow-go/v18":
			arrowVersion = fields[1]
		}
	}
	if goVersion == "" || arrowVersion == "" {
		t.Fatalf("qcheck's go.mod names no go version or arrow-go: %q %q", goVersion, arrowVersion)
	}
	dir := t.TempDir()
	mod := "module qcheckemitted\n\ngo " + goVersion + "\n\nrequire (\n\tgithub.com/advenn/ursus v0.0.0\n" +
		"\tgithub.com/apache/arrow-go/v18 " + arrowVersion + "\n)\n\nreplace github.com/advenn/ursus => " +
		filepath.Dir(qcheck) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(mod), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, err := os.ReadFile(filepath.Join(qcheck, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.sum"), sum, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func vet(goBin, dir string) (string, error) {
	cmd := exec.Command(goBin, "vet", "./...")
	cmd.Dir = dir
	env := []string{"GOFLAGS=-mod=mod", "GOPROXY=off", "GOSUMDB=off", "GOWORK=off", "GOTOOLCHAIN=local"}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GOFLAGS=") {
			env = append(env, kv)
		}
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}
