package ursus_test

// An Enum, built and used, against answers worked out by hand: v0.3-scope.md §2.3.
//
// An Enum is a string with a fixed, ordered vocabulary. Its values are stored as
// indices into its categories, so its order is the categories' declared order, as in
// Polars; it meets a String at the String for equality and at itself for order. Every
// answer here is read off the column's indices and the type's own category list, so
// no case depends on the engine's casts to check the engine's casts.

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
)

// knownEnumDefects names each case that answers wrongly today, with what it answers.
var knownEnumDefects = map[string]string{
	"the frame renders its categories":        "rendering panicked: runtime error: index out of range [0] with length ",
	"sort is category order":                  "recovered a panic",
	"group by an Enum":                        "recovered a panic",
	"unique":                                  "recovered a panic",
	"a join of an Enum to the same Enum":      "recovered a panic",
	"concat":                                  "collect: recovered a panic",
	"shift":                                   "recovered a panic",
	"cum_max is category order":               "recovered a panic",
	"String -> Enum":                          "cast: cannot cast String to Enum(lo, mid, hi)",
	"String -> Enum refuses an unknown value": "a type error, want value error: cast: cannot cast String to Enum(lo, m",
	"CastLossy String -> Enum nulls an unknown value": "cast: cannot cast String to Enum(lo, mid, hi)",
	"Enum -> String":                                          "cast: cannot cast Enum(lo, mid, hi) to String",
	"Enum -> another Enum":                                    "cast: cannot cast Enum(lo, mid, hi) to Enum(hi, lo, mid)",
	"Enum(1, 2) -> Int64 reads the text":                      "recovered a panic",
	"Int64 -> Enum is refused":                                "the refusal does not say \"String\": cast: cannot cast Int64 to Enum(lo,",
	"== a String column, by value":                            "select: operator == has no common type for Enum(lo, mid, hi) and Strin",
	"Concat with a String is a String":                        "concat: cannot stack column \"e\": no common type for Enum(lo, mid, hi) ",
	"a join to a String key matches by value":                 "join: cannot join key col(\"e\") to col(\"e\"): no common type for Enum(lo",
	"CSV writes an Enum's text":                               "wrote \"\", sink_csv: cannot write column \"e\" of type Enum(lo, mid, hi) ",
	"CSV reads an Enum schema":                                "scan: opening csv source",
	"an unknown value in an Enum CSV column is a value error": "a unsupported error, want value error: scan: opening csv source",
	"== \"mid\"":                                              "select: operator == has no common type for Enum(lo, mid, hi) and Strin",
	"== \"zzz\" is false":                                     "select: operator == has no common type for Enum(lo, mid, hi) and Strin",
	"< \"mid\" is category order":                             "select: operator < has no common type for Enum(lo, mid, hi) and String",
	"< \"zzz\" is refused":                                    "a type error, want value error: select: operator < has no common type ",
	"IsIn(\"lo\", \"zzz\")":                                   "is_in: is_in cannot compare Enum(lo, mid, hi) with a String value",
	"FillNullWith(\"lo\") stays the Enum":                     "when: the then and otherwise branches have no common type: Enum(lo, mi",
	"FillNullWith(\"zzz\") is a String":                       "when: the then and otherwise branches have no common type: Enum(lo, mi",
}

// null is the text enumCol and enumText use for a null row.
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

// enumText reads an Enum column back as its category texts.
func enumText(df *ursus.DataFrame, name string) ([]string, error) {
	c, ok := df.Batch().ByName(name)
	if !ok {
		return nil, fmt.Errorf("no column %q", name)
	}
	if c.DType().ID() != dtype.TypeEnum {
		return nil, fmt.Errorf("column %q is %s, want an Enum", name, c.DType())
	}
	idx, err := data.Values[uint32](c)
	if err != nil {
		return nil, err
	}
	cats := c.DType().Categories()
	out := make([]string, c.Len())
	for i := range out {
		switch {
		case !c.Validity().Get(i):
			out[i] = null
		case int(idx[i]) < len(cats):
			out[i] = cats[idx[i]]
		default:
			out[i] = fmt.Sprintf("<index %d>", idx[i])
		}
	}
	return out, nil
}

// stringsOf reads a String column, null as null.
func stringsOf(df *ursus.DataFrame, name string) ([]string, error) {
	col, err := df.Column[string](name)
	if err != nil {
		return nil, err
	}
	out := make([]string, col.Len())
	for i := range out {
		v, ok := col.Get(i)
		if !ok {
			v = null
		}
		out[i] = v
	}
	return out, nil
}

func TestEnumByHand(t *testing.T) {
	c := ursus.Col
	et := ursus.Enum("lo", "mid", "hi")
	ef := func() *ursus.LazyFrame {
		return ursus.Frame(enumCol(t, "e", et, "hi", "lo", "mid", null), ursus.Values("n", []int64{1, 2, 3, 4}))
	}
	collect := func(t *testing.T, lf *ursus.LazyFrame) (df *ursus.DataFrame, err error) {
		defer func() {
			if p := recover(); p != nil {
				err = fmt.Errorf("panicked: %v", p)
			}
		}()
		return lf.Collect(t.Context())
	}
	// wantEnum checks column name is the Enum et holding want.
	wantEnum := func(lf *ursus.LazyFrame, name string, want ...string) func(*testing.T) string {
		return func(t *testing.T) string {
			df, err := collect(t, lf)
			if err != nil {
				return err.Error()
			}
			got, err := enumText(df, name)
			if err != nil {
				return err.Error()
			}
			if !slices.Equal(got, want) {
				return fmt.Sprintf("%s = %v, want %v", name, got, want)
			}
			return ""
		}
	}
	wantStrings := func(lf *ursus.LazyFrame, name string, want ...string) func(*testing.T) string {
		return func(t *testing.T) string {
			df, err := collect(t, lf)
			if err != nil {
				return err.Error()
			}
			got, err := stringsOf(df, name)
			if err != nil {
				return err.Error()
			}
			if !slices.Equal(got, want) {
				return fmt.Sprintf("%s = %v, want %v", name, got, want)
			}
			return ""
		}
	}
	// wantBools reads a Bool column, null as -1.
	wantBools := func(lf *ursus.LazyFrame, name string, want ...int) func(*testing.T) string {
		return func(t *testing.T) string {
			df, err := collect(t, lf)
			if err != nil {
				return err.Error()
			}
			col, err := df.Column[bool](name)
			if err != nil {
				return err.Error()
			}
			got := make([]int, col.Len())
			for i := range got {
				v, ok := col.Get(i)
				switch {
				case !ok:
					got[i] = -1
				case v:
					got[i] = 1
				}
			}
			if !slices.Equal(got, want) {
				return fmt.Sprintf("%s = %v, want %v (1 true, 0 false, -1 null)", name, got, want)
			}
			return ""
		}
	}
	wantInts := func(lf *ursus.LazyFrame, name string, want ...int64) func(*testing.T) string {
		return func(t *testing.T) string {
			df, err := collect(t, lf)
			if err != nil {
				return err.Error()
			}
			col, err := df.Column[int64](name)
			if err != nil {
				return err.Error()
			}
			got := make([]int64, col.Len())
			for i := range got {
				v, ok := col.Get(i)
				if !ok {
					v = math.MinInt64
				}
				got[i] = v
			}
			if !slices.Equal(got, want) {
				return fmt.Sprintf("%s = %v, want %v (MinInt64 is null)", name, got, want)
			}
			return ""
		}
	}
	refused := func(lf *ursus.LazyFrame, kind error, words ...string) func(*testing.T) string {
		return func(t *testing.T) string {
			df, err := collect(t, lf)
			return refusal(df, err, kind, words...)
		}
	}
	read := func(text string, opts ...ursus.CSVOption) *ursus.LazyFrame {
		return ursus.ScanCSVReader([]byte(text), "x.csv", opts...)
	}
	notNull := c("e").IsNotNull()

	cases := []ioCase{
		// --- the column is usable at all ---
		{"the frame renders its categories", func(t *testing.T) (wrong string) {
			df, err := collect(t, ef())
			if err != nil {
				return err.Error()
			}
			defer func() {
				if p := recover(); p != nil {
					wrong = fmt.Sprintf("rendering panicked: %v", p)
				}
			}()
			if s := df.String(); !strings.Contains(s, "hi") || !strings.Contains(s, "mid") {
				return "rendered without its categories: " + strings.Join(strings.Fields(s), " ")
			}
			return ""
		}},
		{"sort is category order", wantEnum(ef().Filter(notNull).Sort(ursus.Asc(c("e"))), "e", "lo", "mid", "hi")},
		{"min and max are category order", func(t *testing.T) string {
			lf := ef().GroupBy().Agg(c("e").Min().Alias("mn"), c("e").Max().Alias("mx"))
			if w := wantEnum(lf, "mn", "lo")(t); w != "" {
				return w
			}
			return wantEnum(lf, "mx", "hi")(t)
		}},
		{"group by an Enum", wantEnum(ef().Filter(notNull).GroupBy(c("e")).Agg(c("n").Sum()).Sort(ursus.Asc(c("e"))),
			"e", "lo", "mid", "hi")},
		{"unique", func(t *testing.T) string {
			df, err := collect(t, ursus.Frame(enumCol(t, "e", et, "lo", "lo", "hi")).Unique())
			if err != nil {
				return err.Error()
			}
			if df.Height() != 2 {
				return fmt.Sprintf("%d rows, want 2", df.Height())
			}
			return ""
		}},
		{"a join of an Enum to the same Enum", func(t *testing.T) string {
			df, err := collect(t, ef().Join(ef().Select(c("e"), c("n").Alias("m")), ursus.JoinOn(c("e"))))
			if err != nil {
				return err.Error()
			}
			if df.Height() != 3 {
				return fmt.Sprintf("%d rows, want 3: a null key matches nothing", df.Height())
			}
			return ""
		}},
		{"concat", wantEnum(ursus.Concat([]*ursus.LazyFrame{ef(), ef()}), "e",
			"hi", "lo", "mid", null, "hi", "lo", "mid", null)},
		{"shift", wantEnum(ef().Select(c("e").Shift(1)), "e", null, "hi", "lo", "mid")},
		{"cum_max is category order", wantEnum(ef().Select(c("e").CumMax(false)), "e", "hi", "hi", "hi", null)},

		// --- casts ---
		{"String -> Enum", wantEnum(ursus.Frame(ursus.Values("s", []string{"lo", "hi"})).Select(c("s").Cast(et)),
			"s", "lo", "hi")},
		{"String -> Enum refuses an unknown value", refused(
			ursus.Frame(ursus.Values("s", []string{"lo", "zzz"})).Select(c("s").Cast(et)), ursus.ErrValue, "zzz")},
		{"CastLossy String -> Enum nulls an unknown value", wantEnum(
			ursus.Frame(ursus.Values("s", []string{"lo", "zzz"})).Select(c("s").CastLossy(et)), "s", "lo", null)},
		{"Enum -> String", wantStrings(ef().Select(c("e").Cast(ursus.String)), "e", "hi", "lo", "mid", null)},
		{"Enum -> another Enum", func(t *testing.T) string {
			other := ursus.Enum("hi", "lo", "mid")
			lf := ef().Select(c("e").Cast(other))
			df, err := collect(t, lf)
			if err != nil {
				return err.Error()
			}
			if got := df.Schema().Field(0).Type; got != other {
				return fmt.Sprintf("type %s, want %s", got, other)
			}
			return wantEnum(lf, "e", "hi", "lo", "mid", null)(t)
		}},
		{"Enum(1, 2) -> Int64 reads the text", wantInts(
			ursus.Frame(enumCol(t, "e", ursus.Enum("1", "2"), "2", "1")).Select(c("e").Cast(ursus.Int64)), "e", 2, 1)},
		{"Int64 -> Enum is refused", refused(
			ursus.Frame(ursus.Values("x", []int64{0})).Select(c("x").Cast(et)), ursus.ErrType, "String")},

		// --- comparisons ---
		{`== "mid"`, wantBools(ef().Select(c("e").Eq("mid")), "e", 0, 0, 1, -1)},
		{`== "zzz" is false`, wantBools(ef().Select(c("e").Eq("zzz")), "e", 0, 0, 0, -1)},
		{`< "mid" is category order`, wantBools(ef().Select(c("e").Lt("mid")), "e", 0, 1, 0, -1)},
		{`< "zzz" is refused`, refused(ef().Select(c("e").Lt("zzz")), ursus.ErrValue, "zzz")},
		{"== a String column, by value", wantBools(
			ef().WithColumns(ursus.Lit("").Alias("s")).WithColumns(
				ursus.When(c("n").Eq(1)).Then(ursus.Lit("hi")).When(c("n").Eq(2)).Then(ursus.Lit("x")).
					When(c("n").Eq(3)).Then(ursus.Lit("mid")).Otherwise(ursus.Lit("lo")).Alias("s")).
				Select(c("e").Eq(c("s"))), "e", 1, 0, 1, -1)},
		{`IsIn("lo", "zzz")`, wantBools(ef().Select(c("e").IsIn("lo", "zzz")), "e", 0, 1, 0, -1)},

		// --- meeting other types ---
		{`FillNullWith("lo") stays the Enum`, wantEnum(ef().Select(c("e").FillNullWith("lo")), "e", "hi", "lo", "mid", "lo")},
		{`FillNullWith("zzz") is a String`, wantStrings(ef().Select(c("e").FillNullWith("zzz")), "e", "hi", "lo", "mid", "zzz")},
		{"Concat with a String is a String", wantStrings(
			ursus.Concat([]*ursus.LazyFrame{ef().Select(c("e")), ursus.Frame(ursus.Values("e", []string{"x"}))}),
			"e", "hi", "lo", "mid", null, "x")},
		{"a join to a String key matches by value", func(t *testing.T) string {
			df, err := collect(t, ef().Join(ursus.Frame(ursus.Values("e", []string{"lo", "x"}),
				ursus.Values("r", []int64{1, 2})), ursus.JoinOn(c("e"))))
			if err != nil {
				return err.Error()
			}
			if df.Height() != 1 {
				return fmt.Sprintf("%d rows, want 1", df.Height())
			}
			return ""
		}},

		// --- refusals that stay ---
		{".str is refused, with the cast hint", refused(ef().Select(c("e").Str().LenChars()), ursus.ErrType,
			"Cast(ursus.String)")},

		// --- CSV ---
		{"CSV writes an Enum's text", func(t *testing.T) string {
			text, err := csvText(t, ef())
			if want := "e,n\nhi,1\nlo,2\nmid,3\n,4\n"; err != nil || text != want {
				return fmt.Sprintf("wrote %q, %v; want %q", text, err, want)
			}
			return ""
		}},
		{"CSV reads an Enum schema", wantEnum(read("e\nhi\nlo\n\n",
			ursus.WithSchema(dtype.MustSchema(dtype.Of("e", et)))), "e", "hi", "lo", null)},
		{"an unknown value in an Enum CSV column is a value error", refused(read("e\nzzz\n",
			ursus.WithSchema(dtype.MustSchema(dtype.Of("e", et)))), ursus.ErrValue, "zzz")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wrong := tc.run(t)
			why, known := knownEnumDefects[tc.name]
			switch {
			case known && wrong == "":
				t.Errorf("answers correctly now; delete it from knownEnumDefects (%s)", why)
			case known:
				t.Logf("known defect (%s): %s", why, wrong)
			case wrong != "":
				t.Error(wrong)
			}
		})
	}
	for name := range knownEnumDefects {
		if !slices.ContainsFunc(cases, func(c ioCase) bool { return c.name == name }) {
			t.Errorf("knownEnumDefects names %q, which is not a case", name)
		}
	}
}
