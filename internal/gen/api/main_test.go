package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// module writes a module example.com/lib of a public package and an internal one.
func module(t *testing.T, lib, in string) string {
	t.Helper()
	dir := t.TempDir()
	for name, src := range map[string]string{
		"go.mod":            "module example.com/lib\n\ngo 1.27\n",
		"lib.go":            "package lib\n\nimport \"example.com/lib/internal/in\"\n\n" + lib,
		"internal/in/in.go": "package in\n\n" + in,
	} {
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

const before = `
type E struct{}

type T struct{ E; A int; b int }

func (t T) V(x int) string  { return "" }
func (t *T) P(y int) error  { return nil }

type I interface{ M(n int) int }

type C interface{ ~int | ~string }

func F[X C](name X) X { return name }

func G(f func(a int) bool) {}

type S = in.S

type G2[E any] struct{}

func (g *G2[E]) Map[U any](f func(e E) U) []U { return nil }

const K = 3

var V int

var _ = in.S{}
`

const after = `
type E struct{}

type T struct{ A int; b int; Z string }

func (t *T) V(other int) string { return "" }
func (t T) P(other int) error   { return nil }

type I interface {
	M(m int) int
	N()
}

type C interface{ ~int | ~string | ~float64 }

func F[Y C](other Y) Y { return other }

func G(g func(b int) bool) {}

type S = in.S

type G2[E any] struct{}

func (g *G2[E]) Map[W any](h func(x E) W) []W { return nil }

const K = 4

var V int

var _ = in.S{}
`

// TestABreakIsWhatACallerSees: between two versions of a module, the features gone
// are its breaks, and only they: a method moved to a pointer's receiver, an
// interface's method added, a constraint's term added, a constant's value changed,
// an internal type's method its public alias had, an embedded field taken out. A parameter renamed, a field
// added, a method moved to a value's receiver and a type parameter renamed are not.
func TestABreakIsWhatACallerSees(t *testing.T) {
	old, err := features(module(t, before, "type S struct{ X int }\n\nfunc (s *S) Len() int { return 0 }\n"))
	if err != nil {
		t.Fatal(err)
	}
	cur, err := features(module(t, after, "type S struct{ X int }\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"pkg example.com/lib, method (*G2) Map[T0 any](func(P0) T0) []T0",
		"pkg example.com/lib, method (*S) Len() int",
		"pkg example.com/lib, type S struct, X int",
		"pkg example.com/lib, type G2[P0 any] struct",
		"pkg example.com/lib, func F[T0 C](T0) T0",
		"pkg example.com/lib, func G(func(int) bool)",
	} {
		if !slices.Contains(old, want) {
			t.Errorf("the API does not list %q:\n%s", want, strings.Join(old, "\n"))
		}
	}
	got := removed(old, cur)
	want := []string{
		"pkg example.com/lib, const K = 3",
		"pkg example.com/lib, method (*S) Len() int",
		"pkg example.com/lib, method (T) V(int) string",
		"pkg example.com/lib, type C interface, embedded ~int | ~string",
		"pkg example.com/lib, type I interface { M }",
		"pkg example.com/lib, type T struct, embedded E",
	}
	if !slices.Equal(got, want) {
		t.Errorf("breaks:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestABreakIsNamedInAnUnreleasedBreaksSection: a break is named by its name, its
// type's or Type.Member, in backquotes, in a Breaks section of an unreleased version
// alone: not in its Features, nor in a released version's Breaks.
func TestABreakIsNamedInAnUnreleasedBreaksSection(t *testing.T) {
	log := "# Changelog\n\n## Unreleased — toward v0.9\n\n### Breaks\n\n" +
		"- **`Engine.Stats` reports more** (1), `WithThreads(n)` too.\n\n### Features\n\n- `NewThing`.\n\n" +
		"## Unreleased — toward v0.8\n\n### Breaks\n\n- `Concat` and `ConcatMode`.\n\n" +
		"## v0.7.0\n\n### Breaks\n\n- `Released`.\n"
	named := breaksNamed(log)
	for _, c := range []struct {
		feat string
		ok   bool
	}{
		{"pkg m, method (*Engine) Stats() EngineStats", true},
		{"pkg m, func WithThreads(int) CollectOption", true},
		{"pkg m, func Concat(...*LazyFrame) *LazyFrame", true},
		{"pkg m, const ConcatStrict ConcatMode", true},
		{"pkg m, type EngineStats struct, Held int64", false},
		{"pkg m, func NewThing() int", false},
		{"pkg m, func Released()", false},
		{"pkg m, method (*Frame) Stats() int", false},
	} {
		if got := slices.ContainsFunc(names(c.feat), func(n string) bool { return named[n] }); got != c.ok {
			t.Errorf("%q named %v, want %v (names %v)", c.feat, got, c.ok, names(c.feat))
		}
	}
}

// TestTheNewestTagIsByVersion: v0.10.0 is newer than v0.9.1.
func TestTheNewestTagIsByVersion(t *testing.T) {
	dir := t.TempDir()
	for _, v := range []string{"v0.9.1", "v0.10.0", "v0.6.8"} {
		if err := os.WriteFile(filepath.Join(dir, v+".txt"), []byte("# "+v+"\npkg m, func "+strings.ReplaceAll(v, ".", "_")+"()\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tag, feats, err := newest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if tag != "v0.10.0" || len(feats) != 1 || feats[0] != "pkg m, func v0_10_0()" {
		t.Errorf("newest %s %v, want v0.10.0 and its one feature", tag, feats)
	}
}

// TestAnUnnamedBreakFails: a feature of the newest tag's that the tree does not have
// fails the check until a Breaks section of an unreleased version names it.
func TestAnUnnamedBreakFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := "pkg m, func Gone(int) int\npkg m, func Kept()\n"
	if err := os.WriteFile(filepath.Join(dir, "api", "v0.1.0.txt"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	cur := []string{"pkg m, func Gone(string) int", "pkg m, func Kept()"}
	for _, c := range []struct {
		log string
		ok  bool
	}{
		{"## Unreleased — toward v0.2\n\n### Features\n\n- `Gone` takes a string.\n", false},
		{"## Unreleased — toward v0.2\n\n### Breaks\n\n- `Gone` takes a string.\n", true},
	} {
		if err := os.WriteFile(filepath.Join(dir, "CHANGELOG.md"), []byte(c.log), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := checkBreaks(dir, cur); (err == nil) != c.ok {
			t.Errorf("the break named in %q: %v", c.log, err)
		}
	}
}
