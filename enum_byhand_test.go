package ursus_test

// An Enum, built and used: v0.3-scope.md §2.3.
//
// An Enum is a string with a fixed, ordered vocabulary. Its values are stored as
// indices into its categories, so its order is the categories' declared order, as in
// Polars; it meets a String at the String for equality and at itself for order. The
// fixture is built from the column's indices and the type's own category list, so no
// case depends on the engine's casts to check the engine's casts. Every answer with a
// value is Polars 2.1.0's, recorded in the enum_byhand group of testdata/oracle and
// spelled in oracle_enum_byhand_test.go since step 499, which took this file's hand
// values. What stays asserts the rendering, the counts, the types and the refusals,
// and holds what Polars 2 refuses and ursus answers to a differential: cum_max, a
// cast of an Enum of digits to Int64, .str and the CSV reader and writer, each
// against the same through a String.

import (
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/ursustest"
)

// null is the text enumCol takes for a null row.
const null = "∅"

// enumCol is a column of Enum type et holding vals, null where a value is null.
func enumCol(t *testing.T, name string, et dtype.DataType, vals ...string) *ursus.Column {
	t.Helper()
	idx := make([]uint32, len(vals))
	valid := bitmap.NewBuilder(len(vals))
	for i, v := range vals {
		if v == null {
			valid.Append(false)
			continue
		}
		j := slices.Index(et.Categories(), v)
		if j < 0 {
			t.Fatalf("%q is not a category of %s", v, et)
		}
		idx[i] = uint32(j)
		valid.Append(true)
	}
	return data.NewFixed(name, et, idx, valid.Finish())
}

func TestEnumByHand(t *testing.T) {
	c := ursus.Col
	et := ursus.Enum("lo", "mid", "hi")
	ef := func() *ursus.LazyFrame {
		return ursus.Frame(enumCol(t, "e", et, "hi", "lo", "mid", null), ursus.Values("n", []int64{1, 2, 3, 4}))
	}
	collect := func(t *testing.T, lf *ursus.LazyFrame) *ursus.DataFrame {
		t.Helper()
		df, err := lf.Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return df
	}
	// same holds two queries to the same frame, a differential.
	same := func(a, b *ursus.LazyFrame) func(*testing.T) {
		return func(t *testing.T) { ursustest.AssertFrameEqual(t, collect(t, a), collect(t, b)) }
	}
	rows := func(lf *ursus.LazyFrame, n int, why string) func(*testing.T) {
		return func(t *testing.T) {
			if df := collect(t, lf); df.Height() != n {
				t.Errorf("%d rows, want %d: %s", df.Height(), n, why)
			}
		}
	}
	refused := func(lf *ursus.LazyFrame, kind error, words ...string) func(*testing.T) {
		return func(t *testing.T) {
			df, err := lf.Collect(t.Context())
			if wrong := refusal(df, err, kind, words...); wrong != "" {
				t.Error(wrong)
			}
		}
	}
	read := func(text string, opts ...ursus.CSVOption) *ursus.LazyFrame {
		return ursus.ScanCSVReader([]byte(text), "x.csv", opts...)
	}
	asString := c("e").Cast(ursus.String)

	for name, run := range map[string]func(*testing.T){
		// --- the column is usable at all ---
		"the frame renders its categories": func(t *testing.T) {
			df := collect(t, ef())
			// The cells, below the header rule: the header's type line already names
			// every category, so a check of the whole text passes on cells that
			// print indices — which the first version of this case did.
			_, body, _ := strings.Cut(df.String(), "├")
			for _, want := range []string{"hi", "lo", "mid"} {
				if !strings.Contains(body, want) {
					t.Fatalf("rendered cells without their categories: %s", strings.Join(strings.Fields(body), " "))
				}
			}
			if v, ok, err := df.At[string](0, "e"); err != nil || !ok || v != "hi" {
				t.Errorf("At[string](0) = %q, %v, %v; want \"hi\"", v, ok, err)
			}
		},
		"unique": rows(ursus.Frame(enumCol(t, "e", et, "lo", "lo", "hi")).Unique(), 2, "two distinct values"),
		"a join of an Enum to the same Enum": rows(ef().Join(ef().Select(c("e"), c("n").Alias("m")), ursus.JoinOn(c("e"))), 3,
			"a null key matches nothing"),
		// cum_max of an Enum, which Polars 2.1 refuses, is the cum_max of its indices.
		"cum_max is category order": same(ef().Select(c("e").CumMax(false).Cat().Physical().Alias("p")),
			ef().Select(c("e").Cat().Physical().CumMax(false).Alias("p"))),

		// --- casts ---
		"String -> Enum refuses an unknown value": refused(
			ursus.Frame(ursus.Values("s", []string{"lo", "zzz"})).Select(c("s").Cast(et)), ursus.ErrValue, "zzz"),
		// Polars 2.1 refuses a cast of an Enum to a number; here it reads the text.
		"Enum(1, 2) -> Int64 reads the text": same(
			ursus.Frame(enumCol(t, "e", ursus.Enum("1", "2"), "2", "1")).Select(c("e").Cast(ursus.Int64)),
			ursus.Frame(enumCol(t, "e", ursus.Enum("1", "2"), "2", "1")).Select(asString.Cast(ursus.Int64))),
		"Int64 -> Enum is refused": refused(ursus.Frame(ursus.Values("x", []int64{0})).Select(c("x").Cast(et)), ursus.ErrType, "String"),

		// --- comparisons ---
		`< "zzz" is refused`: refused(ef().Select(c("e").Lt("zzz")), ursus.ErrValue, "zzz"),
		"two different Enums have no common order": refused(
			ef().WithColumns(c("e").Cast(ursus.Enum("hi", "lo", "mid")).Alias("f")).Select(c("e").Lt(c("f"))), ursus.ErrType, "order"),

		// --- meeting other types ---
		`FillNullWith("zzz") is a String`: func(t *testing.T) {
			// Polars 2.1 keeps the null and the Enum; the oracle's enum_byhand/fill_null_zzz
			// is left recorded, a finding for the maintainer (step 499).
			df := collect(t, ef().Select(c("e").FillNullWith("zzz")))
			if got := df.Schema().Field(0).Type; got != ursus.String {
				t.Errorf("type %s, want String", got)
			}
		},
		"a join to a String key matches by value": rows(ef().Join(ursus.Frame(ursus.Values("e", []string{"lo", "x"}),
			ursus.Values("r", []int64{1, 2})), ursus.JoinOn(c("e"))), 1, "lo alone"),

		// --- .str reads an Enum's text (step 273), where it was refused and Polars refuses ---
		".str reads the text": same(ef().Select(c("e").Str().LenChars()), ef().Select(asString.Str().LenChars())),

		// --- CSV ---
		"CSV writes an Enum's text": func(t *testing.T) {
			got, err := csvText(t, ef())
			want, err2 := csvText(t, ef().Select(asString, c("n")))
			if err != nil || err2 != nil || got != want {
				t.Errorf("wrote %q, %v; the String column writes %q, %v", got, err, want, err2)
			}
		},
		"CSV reads an Enum schema": same(read("e\nhi\nlo\n\n", ursus.WithSchema(dtype.MustSchema(dtype.Of("e", et)))),
			read("e\nhi\nlo\n\n").Select(c("e").Cast(et))),
		"an unknown value in an Enum CSV column is a value error": refused(read("e\nzzz\n",
			ursus.WithSchema(dtype.MustSchema(dtype.Of("e", et)))), ursus.ErrValue, "zzz"),
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if p := recover(); p != nil {
					t.Errorf("panicked: %v", p)
				}
			}()
			run(t)
		})
	}
}
