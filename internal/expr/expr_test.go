package expr_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// schema is the fixture every test resolves against. Note there is no data
// anywhere in this file: the whole point of Field is that type resolution never
// touches a byte.
var schema = dtype.MustSchema(
	dtype.NotNull("id", dtype.Int64),
	dtype.Of("qty", dtype.Int32),
	dtype.Of("price", dtype.Float64),
	dtype.Of("weight", dtype.Float32),
	dtype.Of("name", dtype.String),
	dtype.Of("flag", dtype.Bool),
	dtype.Of("ts", dtype.Datetime(dtype.Micro, "UTC")),
	dtype.Of("dur", dtype.Duration(dtype.Nano)),
	dtype.Of("big", dtype.Uint64),
)

func col(n string) expr.Node                { return &expr.Col{Name: n} }
func lit(v any, d dtype.DataType) expr.Node { return &expr.Lit{Value: v, DT: d} }
func bin(op expr.BinaryOp, l, r expr.Node) expr.Node {
	return &expr.Binary{Op: op, L: l, R: r}
}

func TestFieldResolution(t *testing.T) {
	cases := []struct {
		name     string
		e        expr.Node
		wantName string
		wantType dtype.DataType
		wantNull bool
	}{
		{"column", col("price"), "price", dtype.Float64, true},
		{"non-nullable column", col("id"), "id", dtype.Int64, false},
		{"literal", lit(int64(5), dtype.Int64), "literal", dtype.Int64, false},
		{"null literal is nullable", lit(nil, dtype.Int64), "literal", dtype.Int64, true},

		// Promotion: the wider type wins, and nullability is the union.
		{"int + int", bin(expr.OpAdd, col("id"), col("qty")), "id", dtype.Int64, true},
		{"int + float", bin(expr.OpAdd, col("qty"), col("price")), "qty", dtype.Float64, true},
		{"f32 + f64", bin(expr.OpMul, col("weight"), col("price")), "weight", dtype.Float64, true},

		// Two non-nullable operands stay non-nullable.
		{"nn + nn", bin(expr.OpAdd, col("id"), lit(int64(1), dtype.Int64)), "id", dtype.Int64, false},
		{"uint64 + int64 widens to Int128",
			bin(expr.OpAdd, col("big"), col("id")), "big", dtype.Int128, true},

		// True division is always float, even for two integers.
		{"int / int", bin(expr.OpDiv, col("id"), col("qty")), "id", dtype.Float64, true},
		{"f32 / f32", bin(expr.OpDiv, col("weight"), col("weight")), "weight", dtype.Float32, true},
		{"int // int", bin(expr.OpFloorDiv, col("id"), col("qty")), "id", dtype.Int64, true},

		// Comparisons are Bool and three-valued, so they inherit nullability.
		{"comparison", bin(expr.OpGt, col("price"), lit(5.0, dtype.Float64)), "price", dtype.Bool, true},
		{"comparison of nn", bin(expr.OpGt, col("id"), lit(int64(5), dtype.Int64)), "id", dtype.Bool, false},

		// Missing-comparison is total: nulls are data, so the result is never null.
		{"eq_missing", bin(expr.OpEqMissing, col("price"), col("price")), "price", dtype.Bool, false},

		// Null predicates are never null. NaN predicates are.
		{"is_null", &expr.Unary{Op: expr.OpIsNull, Child: col("price")}, "price", dtype.Bool, false},
		{"is_nan", &expr.Unary{Op: expr.OpIsNan, Child: col("price")}, "price", dtype.Bool, true},

		// A conditional: branches promote pairwise, and the result is nullable if
		// EITHER branch is or if the CONDITION is — a null condition takes neither
		// branch and manufactures a null that appears in neither.
		{"cond promotes branches",
			&expr.Cond{Pred: bin(expr.OpGt, col("id"), lit(int64(0), dtype.Int64)),
				Then: col("id"), Else: col("price")},
			"id", dtype.Float64, true},
		{"cond of non-null branches under a non-null condition",
			&expr.Cond{Pred: bin(expr.OpGt, col("id"), lit(int64(0), dtype.Int64)),
				Then: col("id"), Else: lit(int64(0), dtype.Int64)},
			"id", dtype.Int64, false},
		{"cond is nullable when the condition is",
			&expr.Cond{Pred: bin(expr.OpGt, col("price"), lit(0.0, dtype.Float64)),
				Then: lit(int64(1), dtype.Int64), Else: lit(int64(0), dtype.Int64)},
			"literal", dtype.Int64, true},
		// Named after the THEN branch, not the condition: leftmost-column-wins would
		// pick "id", a column that does not appear in the result.
		{"cond names after then",
			&expr.Cond{Pred: bin(expr.OpGt, col("id"), lit(int64(0), dtype.Int64)),
				Then: col("name"), Else: col("name")},
			"name", dtype.String, true},

		// is_in is Bool and inherits the receiver's nullability path through Call,
		// which marks every call nullable.
		{"is_in",
			&expr.Call{Fn: expr.FnIsIn, Args: []expr.Node{col("name"),
				lit("a", dtype.String)}},
			"name", dtype.Bool, true},

		// Naming: leftmost column wins; alias overrides at the root only.
		{"leftmost name", bin(expr.OpAdd, col("price"), col("qty")), "price", dtype.Float64, true},
		{"literal-first name", bin(expr.OpAdd, lit(1.0, dtype.Float64), col("price")), "price", dtype.Float64, true},
		{"alias", &expr.Alias{Child: col("price"), Name: "p"}, "p", dtype.Float64, true},
		{"alias below op does not win",
			bin(expr.OpAdd, &expr.Alias{Child: col("price"), Name: "p"}, col("qty")),
			"price", dtype.Float64, true},

		// A non-strict cast is always nullable, even from a non-nullable column,
		// because an unrepresentable value becomes null.
		{"strict cast", &expr.Cast{Child: col("id"), To: dtype.Int32, Strict: true}, "id", dtype.Int32, false},
		{"non-strict cast", &expr.Cast{Child: col("id"), To: dtype.Int32}, "id", dtype.Int32, true},

		// Temporal algebra.
		{"datetime - datetime", bin(expr.OpSub, col("ts"), col("ts")), "ts", dtype.Duration(dtype.Micro), true},
		{"datetime + duration", bin(expr.OpAdd, col("ts"), col("dur")), "ts", dtype.Datetime(dtype.Micro, "UTC"), true},
		{"duration + duration", bin(expr.OpAdd, col("dur"), col("dur")), "dur", dtype.Duration(dtype.Nano), true},
		{"duration * int", bin(expr.OpMul, col("dur"), col("qty")), "dur", dtype.Duration(dtype.Nano), true},

		// Kleene boolean.
		{"and", bin(expr.OpAnd, col("flag"), col("flag")), "flag", dtype.Bool, true},
		{"not", &expr.Unary{Op: expr.OpNot, Child: col("flag")}, "flag", dtype.Bool, true},

		// Rename composes and is expansion-safe.
		{"rename", &expr.Rename{Child: col("price"),
			Fn: func(s string) string { return "avg_" + s }, Label: `name.prefix("avg_")`},
			"avg_price", dtype.Float64, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := expr.Resolve(c.e, schema)
			if err != nil {
				t.Fatalf("Resolve(%s): %v", c.e, err)
			}
			if got.Name != c.wantName {
				t.Errorf("name = %q, want %q", got.Name, c.wantName)
			}
			if got.Type != c.wantType {
				t.Errorf("type = %s, want %s", got.Type, c.wantType)
			}
			if got.Nullable != c.wantNull {
				t.Errorf("nullable = %v, want %v", got.Nullable, c.wantNull)
			}
		})
	}
}

func TestFieldResolutionErrors(t *testing.T) {
	cases := []struct {
		name     string
		e        expr.Node
		wantKind error
		wantSub  string
	}{
		{"unknown column", col("nope"), uerr.ErrSchema, `unknown column "nope"`},
		{"string + int", bin(expr.OpAdd, col("name"), col("id")), uerr.ErrType, "no common type"},
		// Bool has a "common type" with itself, so this is caught one step later,
		// by the numeric check — which produces the clearer message of the two.
		{"bool arithmetic", bin(expr.OpAdd, col("flag"), col("flag")), uerr.ErrType,
			"operator + is not defined for Bool"},
		{"and on ints", bin(expr.OpAnd, col("id"), col("id")), uerr.ErrType, "requires Boolean operands"},
		{"datetime + datetime", bin(expr.OpAdd, col("ts"), col("ts")), uerr.ErrType, "not defined"},
		{"is_nan on int", &expr.Unary{Op: expr.OpIsNan, Child: col("id")}, uerr.ErrType, "floating-point"},
		{"neg on unsigned", &expr.Unary{Op: expr.OpNeg, Child: col("big")}, uerr.ErrType, "unsigned"},
		{"bad cast", &expr.Cast{Child: col("name"), To: dtype.List(dtype.Int64), Strict: true},
			uerr.ErrType, "cannot cast"},
		{"non-Boolean condition",
			&expr.Cond{Pred: col("id"), Then: col("id"), Else: col("id")},
			uerr.ErrType, "must be Boolean"},
		{"conditional branches with no common type",
			&expr.Cond{Pred: col("flag"), Then: col("name"), Else: col("id")},
			uerr.ErrType, "no common type"},
		// Two temporal types do not promote, even though CanCast permits the cast
		// and kernel.Cast performs it. The hint names the way out.
		{"conditional over two temporal units",
			&expr.Cond{Pred: col("flag"), Then: col("ts"), Else: col("dur")},
			uerr.ErrType, "no common type"},
		{"is_in on an unhashable type",
			&expr.Call{Fn: expr.FnIsIn, Args: []expr.Node{
				&expr.Cast{Child: col("name"), To: dtype.List(dtype.Int64), Strict: false},
				lit("a", dtype.String)}},
			uerr.ErrType, "cannot cast"},

		{"ordering on struct",
			bin(expr.OpLt,
				&expr.Cast{Child: col("name"), To: dtype.String, Strict: true},
				lit(nil, dtype.List(dtype.Int64))),
			uerr.ErrType, "no common type"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := expr.Resolve(c.e, schema)
			if err == nil {
				t.Fatalf("Resolve(%s) succeeded, want an error", c.e)
			}
			if !errors.Is(err, c.wantKind) {
				t.Errorf("kind = %v, want %v (%v)", err, c.wantKind, err)
			}
			if !strings.Contains(err.Error(), c.wantSub) {
				t.Errorf("message %q does not contain %q", err.Error(), c.wantSub)
			}
		})
	}
}

// TestUint64PromotesToInt128 pins the behaviour change that came with 128-bit
// integer sums.
//
// Uint64 combined with a signed type used to be REJECTED, because at 64 bits the
// only options were to wrap or to lose precision above 2^53, and both are silently
// wrong. Int128 holds both ranges, so the combination is now lossless and the
// explicit-cast requirement — which existed solely to work around a missing type —
// is gone.
func TestUint64PromotesToInt128(t *testing.T) {
	f, err := expr.Resolve(bin(expr.OpAdd, col("big"), col("id")), schema)
	if err != nil {
		t.Fatalf("Uint64 + Int64 must promote losslessly, got: %v", err)
	}
	if f.Type != dtype.Int128 {
		t.Errorf("type = %s, want Int128", f.Type)
	}
}

func TestRootNamesAndDedup(t *testing.T) {
	e := bin(expr.OpAdd,
		bin(expr.OpMul, col("price"), col("qty")),
		bin(expr.OpSub, col("price"), col("id")))

	got := expr.RootNames(e)
	want := []string{"price", "qty", "id"} // first-seen order, de-duplicated
	if len(got) != len(want) {
		t.Fatalf("RootNames = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("RootNames = %v, want %v", got, want)
		}
	}
}

func TestExpansion(t *testing.T) {
	must := func(m expr.Matcher, err error) expr.Matcher {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	rx := func(p string) expr.Matcher {
		m, err := expr.NewRegexMatcher(p)
		return must(m, err)
	}
	match := func(m expr.Matcher) expr.Node { return &expr.Match{M: m} }

	cases := []struct {
		name string
		e    expr.Node
		want []string // rendered output of each expansion
	}{
		{
			"explicit names",
			&expr.Binary{Op: expr.OpMul,
				L: match(&expr.NameMatcher{Names: []string{"price", "weight"}}),
				R: lit(2.0, dtype.Float64)},
			[]string{`(col("price") * lit(2))`, `(col("weight") * lit(2))`},
		},
		{
			"by dtype",
			&expr.Unary{Op: expr.OpNeg,
				Child: match(&expr.DTypeMatcher{Types: []dtype.DataType{dtype.Float64}})},
			[]string{`col("price").neg()`},
		},
		{
			"regex",
			match(rx("^(price|weight)$")),
			[]string{`col("price")`, `col("weight")`},
		},
		{
			"all minus one",
			match(&expr.ExcludeMatcher{
				Inner: &expr.AllMatcher{},
				Names: []string{"id", "qty", "price", "weight", "name", "flag", "ts", "dur"},
			}),
			[]string{`col("big")`},
		},
		{
			// The same matcher twice expands in lockstep: each column minus its own
			// mean, not a cross product.
			"repeated matcher expands in lockstep",
			&expr.Binary{Op: expr.OpSub,
				L: match(&expr.NameMatcher{Names: []string{"price", "weight"}}),
				R: match(&expr.NameMatcher{Names: []string{"price", "weight"}})},
			[]string{`(col("price") - col("price"))`, `(col("weight") - col("weight"))`},
		},
		{
			"no matcher passes through",
			col("price"),
			[]string{`col("price")`},
		},
		{
			// Matching nothing yields nothing rather than erroring, so
			// schema-driven pipelines do not need a guard.
			"matching nothing yields nothing",
			match(&expr.DTypeMatcher{Types: []dtype.DataType{dtype.Binary}}),
			nil,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := expr.Expand(c.e, schema)
			if err != nil {
				t.Fatalf("Expand: %v", err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("expanded to %d nodes %v, want %d %v",
					len(got), render(got), len(c.want), c.want)
			}
			for i := range c.want {
				if got[i].String() != c.want[i] {
					t.Errorf("expansion %d = %s, want %s", i, got[i], c.want[i])
				}
			}
		})
	}
}

func TestExpansionErrors(t *testing.T) {
	match := func(m expr.Matcher) expr.Node { return &expr.Match{M: m} }

	t.Run("two different matchers are ambiguous", func(t *testing.T) {
		e := &expr.Binary{Op: expr.OpAdd,
			L: match(&expr.NameMatcher{Names: []string{"price", "weight"}}),
			R: match(&expr.NameMatcher{Names: []string{"qty", "id"}})}
		_, err := expr.Expand(e, schema)
		if err == nil {
			t.Fatal("expected an ambiguity error")
		}
		if !strings.Contains(err.Error(), "at most one selection") {
			t.Errorf("unhelpful message: %v", err)
		}
	})

	t.Run("alias over an expansion collides", func(t *testing.T) {
		e := &expr.Alias{
			Child: match(&expr.NameMatcher{Names: []string{"price", "weight"}}),
			Name:  "x",
		}
		_, err := expr.Expand(e, schema)
		if err == nil {
			t.Fatal("expected a name-collision error")
		}
		if !strings.Contains(err.Error(), `all be named "x"`) {
			t.Errorf("message should name the collision: %v", err)
		}
		if !strings.Contains(err.Error(), "Name().Prefix") {
			t.Errorf("message should suggest the fix: %v", err)
		}
	})

	t.Run("rename over an expansion is fine", func(t *testing.T) {
		e := &expr.Rename{
			Child: match(&expr.NameMatcher{Names: []string{"price", "weight"}}),
			Fn:    func(s string) string { return "avg_" + s },
			Label: `name.prefix("avg_")`,
		}
		got, err := expr.Expand(e, schema)
		if err != nil {
			t.Fatalf("Rename over an expansion must work: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d expansions, want 2", len(got))
		}
		for i, wantName := range []string{"avg_price", "avg_weight"} {
			f, err := expr.Resolve(got[i], schema)
			if err != nil {
				t.Fatal(err)
			}
			if f.Name != wantName {
				t.Errorf("expansion %d named %q, want %q", i, f.Name, wantName)
			}
		}
	})

	t.Run("unknown name in an explicit list", func(t *testing.T) {
		_, err := expr.Expand(match(&expr.NameMatcher{Names: []string{"price", "nope"}}), schema)
		if err == nil || !errors.Is(err, uerr.ErrSchema) {
			t.Fatalf("want a schema error, got %v", err)
		}
	})

	t.Run("unexpanded matcher reaching Resolve is an internal error", func(t *testing.T) {
		_, err := expr.Resolve(match(&expr.AllMatcher{}), schema)
		if err == nil {
			t.Fatal("Resolve must reject an unexpanded tree")
		}
		if !strings.Contains(err.Error(), "unexpanded") {
			t.Errorf("message = %v", err)
		}
	})
}

// TestDeferredError checks the expression half of the sticky-error model: a
// construction failure has nowhere to go at call time, so it rides in an *Err node
// and surfaces at resolution.
func TestDeferredError(t *testing.T) {
	_, err := expr.NewRegexMatcher("(unclosed")
	if err == nil {
		t.Fatal("expected a compile error")
	}

	e := &expr.Binary{Op: expr.OpAdd, L: col("price"), R: &expr.Err{E: err}}
	if got := expr.FirstErr(e); got == nil {
		t.Fatal("FirstErr did not find the deferred error")
	}
	if _, rerr := expr.Resolve(e, schema); rerr == nil {
		t.Fatal("Resolve must surface a deferred error")
	}
	if _, xerr := expr.Expand(e, schema); xerr == nil {
		t.Fatal("Expand must surface a deferred error")
	}
}

// TestImmutability is the property that makes `base.Filter(x)` and
// `base.Filter(y)` independent: expansion rebuilds trees rather than mutating them.
func TestImmutability(t *testing.T) {
	shared := bin(expr.OpMul,
		&expr.Match{M: &expr.NameMatcher{Names: []string{"price", "weight"}}},
		lit(2.0, dtype.Float64))
	before := shared.String()

	if _, err := expr.Expand(shared, schema); err != nil {
		t.Fatal(err)
	}
	if after := shared.String(); after != before {
		t.Errorf("Expand mutated its input:\n before %s\n after  %s", before, after)
	}
}

func render(ns []expr.Node) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = n.String()
	}
	return out
}
