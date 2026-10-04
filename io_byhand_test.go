package ursus_test

// CSV and I/O, against answers worked out by hand: audit.md §5, I8–I10, I19, I21 and
// I22, with S25 and S21's String casts, which share their parsers.
//
// Each case is a silent wrong answer or a refusal the audit measured, answered under
// the rule this step chose for it — most of them Polars' and DuckDB's answers. Where
// the two engines differ, the case says which ursus follows.

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
)

// knownIODefects names each case that answers wrongly today, with what it answers.
var knownIODefects = map[string]string{
	"I8 Amsterdam 1930 round-trips":                               "32 s off: +00:19:32 written as +00:19",
	"I8 Monrovia 1970 round-trips":                                "30 s off: -00:44:30 written as -00:44",
	"I9 inference of 1_000 and 0x1p3":                             "Float64 1000 and 8",
	"I9 a Float64 schema refuses 1_000":                           "read as 1000",
	"S25 Cast(String→Float64) refuses 1_000 and 0x1p3":            "parsed as 1000",
	"S25 CastLossy(String→Float64) of 0x1p3 is null":              "8",
	"I10 a blank line in a one-column Int64 CSV":                  "the line dropped",
	"I10 a blank line in a one-column String CSV":                 "the line dropped",
	"I10 a trailing blank line":                                   "the line dropped",
	"I19 inference of u64::MAX":                                   "lossy Float64",
	"I19 inference of 2^63 beside 1":                              "lossy Float64",
	"I19 an Int128 schema":                                        "refused: opening csv source",
	"I19 a Decimal(10, 2) schema rounds half away from zero":      "refused: opening csv source",
	"I19 a Decimal past its precision is refused":                 "refused as unsupported",
	"S21 Cast(String→Int128)":                                     "no such cast",
	"S21 Cast(String→Decimal(10, 2))":                             "no such cast",
	"I19 i128.Parse refuses an overflow":                          "wraps to MinInt128",
	"I21 a Float64 column round-trips as Float64":                 "String: NaN and +Inf have no digit",
	"I22 CSV refuses a frame with rows and no columns":            "writes a column named \"\"",
	"I22 Parquet refuses a frame with rows and no columns":        "writes (0, 0)",
	"I22 a frame with no rows and no columns writes an empty CSV": "writes a blank header line",
}

type ioCase struct {
	name string
	run  func(t *testing.T) string // what is wrong, or ""
}

// csvText writes lf as CSV.
func csvText(t *testing.T, lf *ursus.LazyFrame) (string, error) {
	t.Helper()
	var b bytes.Buffer
	err := lf.WriteCSV(t.Context(), &b)
	return b.String(), err
}

// i128Col is a column of Int128 values given as decimal text.
func i128Col(t *testing.T, dt dtype.DataType, name string, vals ...string) *ursus.Column {
	t.Helper()
	out := make([]i128.Int128, len(vals))
	for i, s := range vals {
		v, ok := i128.Parse(s)
		if !ok {
			t.Fatalf("i128.Parse(%q)", s)
		}
		out[i] = v
	}
	return data.NewFixed(name, dt, out, bitmap.AllSet(len(vals)))
}

// ioEqual collects got and want and reports how they differ.
func ioEqual(t *testing.T, got, want *ursus.LazyFrame) string {
	t.Helper()
	g, err := got.Collect(t.Context())
	if err != nil {
		return err.Error()
	}
	w, err := want.Collect(t.Context())
	if err != nil {
		t.Fatalf("the hand answer: %v", err)
	}
	return framesDiffer(t, g, w)
}

// ioRefused collects lf and reports anything but a refusal of kind naming each of words.
func ioRefused(t *testing.T, lf *ursus.LazyFrame, kind error, words ...string) string {
	t.Helper()
	df, err := lf.Collect(t.Context())
	return refusal(df, err, kind, words...)
}

func refusal(df *ursus.DataFrame, err error, kind error, words ...string) string {
	switch {
	case err == nil && df != nil:
		return fmt.Sprintf("answered %s; want a refusal", strings.Join(strings.Fields(df.String()), " "))
	case err == nil:
		return "answered; want a refusal"
	case !errors.Is(err, kind):
		return fmt.Sprintf("a %s error, want %v: %v", kindOf(err), kind, err)
	}
	for _, w := range words {
		if !strings.Contains(err.Error(), w) {
			return fmt.Sprintf("the refusal does not say %q: %v", w, err)
		}
	}
	return ""
}

func TestIOByHand(t *testing.T) {
	c := ursus.Col
	read := func(text string, opts ...ursus.CSVOption) *ursus.LazyFrame {
		return ursus.ScanCSVReader([]byte(text), "x.csv", opts...)
	}
	schema := func(name string, dt dtype.DataType) ursus.CSVOption {
		return ursus.WithSchema(dtype.MustSchema(dtype.Of(name, dt)))
	}
	ints := func(name string, v []int64, valid ...bool) *ursus.LazyFrame {
		if valid == nil {
			return ursus.Frame(ursus.Values(name, v))
		}
		return ursus.Frame(ursus.ValuesNullable(name, v, valid))
	}
	strs := func(name string, v []string, valid ...bool) *ursus.LazyFrame {
		if valid == nil {
			return ursus.Frame(ursus.Values(name, v))
		}
		return ursus.Frame(ursus.ValuesNullable(name, v, valid))
	}
	// roundTrip writes lf and reads it back with its own schema.
	roundTrip := func(t *testing.T, lf *ursus.LazyFrame) string {
		text, err := csvText(t, lf)
		if err != nil {
			return "write: " + err.Error()
		}
		sc, err := lf.CollectSchema(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return ioEqual(t, read(text, ursus.WithSchema(sc)), lf)
	}
	decimal := func(name string, scale uint8, unscaled ...string) *ursus.LazyFrame {
		return ursus.Frame(i128Col(t, dtype.Decimal(10, scale), name, unscaled...))
	}

	cases := []ioCase{
		// --- I8: an offset with seconds is written in UTC ---
		// Amsterdam's offset was +00:19:32 until 1937 and Monrovia's −00:44:30 until
		// 1972; "Z07:00" printed +00:19 and −00:44, naming instants 32 s and 30 s away.
		{"I8 Amsterdam 1930 round-trips", func(t *testing.T) string {
			return roundTrip(t, instants(t, "Europe/Amsterdam", dtype.Micro, "1929-12-31T23:40:28"))
		}},
		{"I8 Monrovia 1970 round-trips", func(t *testing.T) string {
			return roundTrip(t, instants(t, "Africa/Monrovia", dtype.Micro, "1970-01-01T00:44:30"))
		}},
		{"control: Amsterdam 2024 keeps its local text", func(t *testing.T) string {
			text, err := csvText(t, instants(t, "Europe/Amsterdam", dtype.Micro, "2024-01-01T12:00:00"))
			if err != nil || !strings.Contains(text, "2024-01-01T13:00:00.000000+01:00") {
				return fmt.Sprintf("wrote %q, %v", text, err)
			}
			return ""
		}},

		// --- I9 / S25: one float grammar, without underscores or hex ---
		{"I9 inference of 1_000 and 0x1p3", func(t *testing.T) string {
			return ioEqual(t, read("a,b\n1_000,0x1p3\n"),
				ursus.Frame(ursus.Values("a", []string{"1_000"}), ursus.Values("b", []string{"0x1p3"})))
		}},
		{"I9 a Float64 schema refuses 1_000", func(t *testing.T) string {
			return ioRefused(t, read("a\n1_000\n", schema("a", dtype.Float64)), ursus.ErrValue, "1_000")
		}},
		{"S25 Cast(String→Float64) refuses 1_000 and 0x1p3", func(t *testing.T) string {
			for _, s := range []string{"1_000", "0x1p3"} {
				if w := ioRefused(t, strs("s", []string{s}).Select(c("s").Cast(ursus.Float64)), ursus.ErrValue, s); w != "" {
					return s + ": " + w
				}
			}
			return ""
		}},
		{"S25 CastLossy(String→Float64) of 0x1p3 is null", func(t *testing.T) string {
			return ioEqual(t, strs("s", []string{"0x1p3"}).Select(c("s").CastLossy(ursus.Float64)),
				ursus.Frame(ursus.ValuesNullable("s", []float64{0}, []bool{false})))
		}},
		{"control: the float grammar's ordinary forms", func(t *testing.T) string {
			return ioEqual(t, strs("s", []string{"1e3", "+1.5", ".5", "5.", "-inf", "NaN", "-1E-2"}).Select(c("s").Cast(ursus.Float64)),
				ursus.Frame(ursus.Values("s", []float64{1000, 1.5, 0.5, 5, math.Inf(-1), math.NaN(), -0.01})))
		}},

		// --- I10: a blank line in a one-column CSV is a null ---
		{"I10 a blank line in a one-column Int64 CSV", func(t *testing.T) string {
			return ioEqual(t, read("a\n1\n\n3\n"), ints("a", []int64{1, 0, 3}, true, false, true))
		}},
		{"I10 a blank line in a one-column String CSV", func(t *testing.T) string {
			return ioEqual(t, read("a\nx\n\nz\n"), strs("a", []string{"x", "", "z"}, true, false, true))
		}},
		{"I10 a trailing blank line", func(t *testing.T) string {
			return ioEqual(t, read("a\n1\n2\n\n"), ints("a", []int64{1, 2, 0}, true, true, false))
		}},
		{"control: a blank line in a two-column CSV is skipped", func(t *testing.T) string {
			return ioEqual(t, read("a,b\n1,2\n\n3,4\n"),
				ursus.Frame(ursus.Values("a", []int64{1, 3}), ursus.Values("b", []int64{2, 4})))
		}},

		// --- I19: Int128 and Decimal, read and parsed exactly ---
		{"I19 inference of u64::MAX", func(t *testing.T) string {
			return ioEqual(t, read("a\n18446744073709551615\n"),
				ursus.Frame(i128Col(t, dtype.Int128, "a", "18446744073709551615")))
		}},
		{"I19 inference of 2^63 beside 1", func(t *testing.T) string {
			return ioEqual(t, read("a\n9223372036854775808\n1\n"),
				ursus.Frame(i128Col(t, dtype.Int128, "a", "9223372036854775808", "1")))
		}},
		{"I19 an Int128 schema", func(t *testing.T) string {
			return ioEqual(t, read("a\n170141183460469231731687303715884105727\n-5\n", schema("a", dtype.Int128)),
				ursus.Frame(i128Col(t, dtype.Int128, "a", "170141183460469231731687303715884105727", "-5")))
		}},
		{"I19 a Decimal(10, 2) schema rounds half away from zero", func(t *testing.T) string {
			return ioEqual(t, read("a\n12.34\n1.235\n-1.235\n7\n", schema("a", dtype.Decimal(10, 2))),
				decimal("a", 2, "1234", "124", "-124", "700"))
		}},
		{"I19 a Decimal past its precision is refused", func(t *testing.T) string {
			return ioRefused(t, read("a\n123456789012\n", schema("a", dtype.Decimal(10, 2))), ursus.ErrValue, "123456789012")
		}},
		{"S21 Cast(String→Int128)", func(t *testing.T) string {
			return ioEqual(t, strs("s", []string{"-170141183460469231731687303715884105728", "42"}).Select(c("s").Cast(ursus.Int128)),
				ursus.Frame(i128Col(t, dtype.Int128, "s", "-170141183460469231731687303715884105728", "42")))
		}},
		{"S21 Cast(String→Decimal(10, 2))", func(t *testing.T) string {
			return ioEqual(t, strs("s", []string{"1.235", "-0.5"}).Select(c("s").Cast(ursus.Decimal(10, 2))),
				decimal("s", 2, "124", "-50"))
		}},
		{"I19 i128.Parse refuses an overflow", func(t *testing.T) string {
			for _, s := range []string{"170141183460469231731687303715884105728", "-170141183460469231731687303715884105729",
				"999999999999999999999999999999999999999999"} {
				if v, ok := i128.Parse(s); ok {
					return fmt.Sprintf("i128.Parse(%s) = %s", s, v)
				}
			}
			return ""
		}},

		// --- I21: a float is written so it reads back as a float ---
		{"I21 a Float64 column round-trips as Float64", func(t *testing.T) string {
			f := ursus.Frame(ursus.Values("f", []float64{1, 2.5, math.NaN(), math.Inf(1), math.Inf(-1)}))
			text, err := csvText(t, f)
			if err != nil {
				return err.Error()
			}
			return ioEqual(t, read(text), f)
		}},
		{"control: a Float32 column round-trips as Float64", func(t *testing.T) string {
			text, err := csvText(t, ursus.Frame(ursus.Values("h", []float32{1.5, 2})))
			if err != nil {
				return err.Error()
			}
			return ioEqual(t, read(text), ursus.Frame(ursus.Values("h", []float64{1.5, 2})))
		}},
		{"control: a column of only NaN infers String", func(t *testing.T) string {
			return ioEqual(t, read("a\nNaN\n"), strs("a", []string{"NaN"}))
		}},

		// --- I22: a frame with rows and no columns is refused by the writers ---
		{"I22 CSV refuses a frame with rows and no columns", func(t *testing.T) string {
			_, err := csvText(t, ints("a", []int64{1, 2, 3}).Select())
			return refusal(nil, err, ursus.ErrValue, "3 rows")
		}},
		{"I22 Parquet refuses a frame with rows and no columns", func(t *testing.T) string {
			var b bytes.Buffer
			err := ints("a", []int64{1, 2, 3}).Select().WriteParquet(t.Context(), &b)
			return refusal(nil, err, ursus.ErrValue, "3 rows")
		}},
		{"I22 a frame with no rows and no columns writes an empty CSV", func(t *testing.T) string {
			text, err := csvText(t, ints("a", []int64{1}).Filter(c("a").Gt(9)).Select())
			if err != nil || text != "" {
				return fmt.Sprintf("wrote %q, %v", text, err)
			}
			return ""
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wrong := tc.run(t)
			why, known := knownIODefects[tc.name]
			switch {
			case known && wrong == "":
				t.Errorf("answers correctly now; delete it from knownIODefects (%s)", why)
			case known:
				t.Logf("known defect (%s): %s", why, wrong)
			case wrong != "":
				t.Error(wrong)
			}
		})
	}
	for name := range knownIODefects {
		if !slices.ContainsFunc(cases, func(c ioCase) bool { return c.name == name }) {
			t.Errorf("knownIODefects names %q, which is not a case", name)
		}
	}
}
