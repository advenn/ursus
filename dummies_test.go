package ursus_test

import (
	"errors"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/execopt"
	"github.com/advenn/ursus/internal/uerr"
	"github.com/advenn/ursus/ursustest"
)

// u8s is a Uint8 column of 0s and 1s, a dummy's.
func u8s(name string, vs ...uint8) *ursus.Column { return ursus.Values(name, vs) }

// TestToDummies is tier 2 of v0.6.7-scope.md (step 320): Polars' to_dummies, every
// answer Polars 1.44.1's.
func TestToDummies(t *testing.T) {
	ctx := t.Context()
	df, err := ursus.Frame(i64s("foo", 1, 2, 3, 1), strs("bar", "a", "b", nil, "a"),
		f64s("x", 1.5, 2.5, 3.5, 4.5)).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	foo, x := i64s("foo", 1, 2, 3, 1), f64s("x", 1.5, 2.5, 3.5, 4.5)
	for _, tc := range []struct {
		name string
		df   *ursus.DataFrame
		o    ursus.DummiesOptions
		want []*ursus.Column
	}{
		{"a column, in its place", df, ursus.DummiesOptions{Columns: []string{"bar"}},
			[]*ursus.Column{foo, u8s("bar_a", 1, 0, 0, 1), u8s("bar_b", 0, 1, 0, 0), u8s("bar_null", 0, 0, 1, 0), x}},
		{"a separator, the first dropped", df, ursus.DummiesOptions{Columns: []string{"bar"}, Separator: ":", DropFirst: true},
			[]*ursus.Column{foo, u8s("bar:b", 0, 1, 0, 0), u8s("bar:null", 0, 0, 1, 0), x}},
		{"the null dropped", df, ursus.DummiesOptions{Columns: []string{"bar"}, DropNulls: true},
			[]*ursus.Column{foo, u8s("bar_a", 1, 0, 0, 1), u8s("bar_b", 0, 1, 0, 0), x}},
		{"every column", df, ursus.DummiesOptions{},
			[]*ursus.Column{u8s("foo_1", 1, 0, 0, 1), u8s("foo_2", 0, 1, 0, 0), u8s("foo_3", 0, 0, 1, 0),
				u8s("bar_a", 1, 0, 0, 1), u8s("bar_b", 0, 1, 0, 0), u8s("bar_null", 0, 0, 1, 0),
				u8s("x_1.5", 1, 0, 0, 0), u8s("x_2.5", 0, 1, 0, 0), u8s("x_3.5", 0, 0, 1, 0), u8s("x_4.5", 0, 0, 0, 1)}},
		// Ordered by name, so 10 before 2; the first to appear dropped, not the least.
		{"ordered by name", dfOf(t, i64s("a", 10, 2, 1, 2, nil)), ursus.DummiesOptions{},
			[]*ursus.Column{u8s("a_1", 0, 0, 1, 0, 0), u8s("a_10", 1, 0, 0, 0, 0), u8s("a_2", 0, 1, 0, 1, 0), u8s("a_null", 0, 0, 0, 0, 1)}},
		{"ordered by name, the first dropped", dfOf(t, i64s("a", 10, 2, 1, 2, nil)), ursus.DummiesOptions{DropFirst: true},
			[]*ursus.Column{u8s("a_1", 0, 0, 1, 0, 0), u8s("a_2", 0, 1, 0, 1, 0), u8s("a_null", 0, 0, 0, 0, 1)}},
		// The first value that is not null is dropped, though a null comes first.
		{"a null first", dfOf(t, i64s("c", nil, 5, 3)), ursus.DummiesOptions{DropFirst: true},
			[]*ursus.Column{u8s("c_3", 0, 0, 1), u8s("c_null", 1, 0, 0)}},
		{"only nulls, none dropped first", dfOf(t, i64s("c", nil, nil)), ursus.DummiesOptions{DropFirst: true},
			[]*ursus.Column{u8s("c_null", 1, 1)}},
		{"text by its bytes", dfOf(t, strs("c", "zebra", nil, "apple", "zebra", "Banana")), ursus.DummiesOptions{DropFirst: true},
			[]*ursus.Column{u8s("c_Banana", 0, 0, 0, 0, 1), u8s("c_apple", 0, 0, 1, 0, 0), u8s("c_null", 0, 1, 0, 0, 0)}},
		// -0.0 is 0.0, named as it first appears, and NaN is one value.
		{"floats", dfOf(t, f64s("f", 1.0, math.Copysign(0, -1), 0.0, math.NaN(), 2.5)), ursus.DummiesOptions{},
			[]*ursus.Column{u8s("f_-0.0", 0, 1, 1, 0, 0), u8s("f_1.0", 1, 0, 0, 0, 0), u8s("f_2.5", 0, 0, 0, 0, 1), u8s("f_NaN", 0, 0, 0, 1, 0)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.df.ToDummies(ctx, tc.o)
			if err != nil {
				t.Fatal(err)
			}
			ursustest.AssertFrameEqual(t, got, dfOf(t, tc.want...))
		})
	}

	t.Run("a series", func(t *testing.T) {
		s, err := dfOf(t, strs("s", "b", "a", nil, "b")).Column[string]("s")
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.ToDummies(ctx, ursus.DummiesOptions{DropFirst: true})
		if err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, got, dfOf(t, u8s("s_a", 0, 1, 0, 0), u8s("s_null", 0, 0, 1, 0)))
	})

	t.Run("nothing left", func(t *testing.T) {
		got, err := dfOf(t, i64s("c", nil, nil)).ToDummies(ctx, ursus.DummiesOptions{DropNulls: true})
		if err != nil {
			t.Fatal(err)
		}
		if got.Width() != 0 || got.Height() != 0 {
			t.Errorf("no dummies left should be a frame of none, as Polars': %d × %d", got.Height(), got.Width())
		}
	})

	t.Run("past the memory limit", func(t *testing.T) {
		// A million distinct values over a million rows would be 10^12 bytes of
		// dummies: refused before any is made, not the process killed.
		const n = 1_000_000
		if lim := execopt.DefaultLimit(); lim == 0 || lim >= n*n {
			t.Skipf("no default limit below %d bytes here: %d", n*n, lim)
		}
		ids := make([]int64, n)
		for i := range ids {
			ids[i] = int64(i)
		}
		_, err := dfOf(t, ursus.Values("id", ids)).ToDummies(ctx, ursus.DummiesOptions{})
		if !errors.Is(err, uerr.ErrResource) || !strings.Contains(err.Error(), "1000000 values to encode") {
			t.Errorf("a million dummies of a million rows: %v", err)
		}
	})

	t.Run("refused", func(t *testing.T) {
		for name, tc := range map[string]struct {
			df   *ursus.DataFrame
			o    ursus.DummiesOptions
			want string
		}{
			"an unknown column":        {df, ursus.DummiesOptions{Columns: []string{"nope"}}, "nope"},
			"a name twice":             {dfOf(t, strs("c", "a"), i64s("c_a", 1)), ursus.DummiesOptions{Columns: []string{"c"}}, "c_a"},
			"the text null and a null": {dfOf(t, strs("c", "null", nil)), ursus.DummiesOptions{}, "c_null"},
			"a list": {collect(t, ursus.Frame(i64s("a", 1, 2)).Select(ursus.ConcatList(ursus.Col("a")).Alias("l"))),
				ursus.DummiesOptions{}, "List"},
		} {
			if _, err := tc.df.ToDummies(ctx, tc.o); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%s: %v, want an error naming %q", name, err, tc.want)
			}
		}
		if _, err := df.ToDummies(ctx, ursus.DummiesOptions{Columns: []string{"nope"}}); !errors.Is(err, uerr.ErrSchema) {
			t.Errorf("an unknown column should be a schema error: %v", err)
		}
	})
}

// TestDummyNames holds each type's dummy names to Polars 1.44.1's: its value as
// Polars displays it, AnyValue's Display.
func TestDummyNames(t *testing.T) {
	cast := func(c *ursus.Column, dt ursus.DataType) *ursus.LazyFrame {
		return ursus.Frame(c).Select(ursus.Col("v").Cast(dt))
	}
	durations := ursus.Values("v", []int64{0, 1, 999, 1000, 1001, 1_000_000, 1_500_000, 1_000_000_000,
		61_000_000_123, -90_000_000_000, -1, 86_400_000_000_000 + 1, 3_600_000_000_000*25 + 7_000})
	local := ursus.Values("v", []int64{1704164645000000, 1719889445001000}) // 2024-01-02 03:04:05, 2024-07-02 03:04:05.001
	zoned := func(tz string) *ursus.LazyFrame {
		return ursus.Frame(local).Select(ursus.Col("v").Cast(ursus.Datetime(ursus.Micro, "")).Dt().ReplaceTimeZone(tz))
	}
	for _, tc := range []struct {
		name string
		lf   *ursus.LazyFrame
		want []string
	}{
		{"Float64", ursus.Frame(ursus.Values("v", []float64{0.30000000000000004, 12.0000000001, 1234567.0, 999999.0,
			999999.5, -1e-7, 1e-6, 123456.789, math.Inf(1), math.Inf(-1), math.NaN(), math.Copysign(0, -1)})),
			[]string{"-0.0", "-1.0000e-7", "-inf", "0.000001", "0.3", "1.234567e6", "12.0", "123456.789", "9.99999e5", "999999.5", "NaN", "inf"}},
		{"Float64, more", ursus.Frame(ursus.Values("v", []float64{1e15, 0.0000011, 99999.99999, 1234567.5, 5e-324,
			math.MaxFloat64, 1.0 / 3, 2.0 / 3 * 1e6, 1e20, 1.5e-7, 100.0, 123456789.0, 2.5})),
			[]string{"0.0000011", "0.333333", "1.0000e15", "1.0000e20", "1.23456789e8", "1.5000e-7", "1.7977e308", "100.0", "1234567.5", "2.5", "4.9407e-324", "666666.666667", "99999.99999"}},
		{"Float32", ursus.Frame(ursus.Values("v", []float32{1e20, 1.5e-7, 0.30000001, 2.0, 1234567.0, 16777217.0, 3.4e38, 1e-45, -2.5, 0.1})),
			[]string{"-2.5", "0.1", "0.3", "1.0000e20", "1.234567e6", "1.4013e-45", "1.5000e-7", "1.6777216e7", "2.0", "3.4000e38"}},
		{"Duration(ns)", cast(durations, ursus.Duration(ursus.Nano)),
			[]string{"-1m -30s", "-1ns", "0ns", "1001ns", "1500µs", "1d 1h 7µs", "1d 1ns", "1m 1s 123ns", "1ms", "1ns", "1s", "1µs", "999ns"}},
		{"Duration(µs)", cast(durations, ursus.Duration(ursus.Micro)),
			[]string{"-1d -1h", "-1µs", "0µs", "1000d 1µs", "1001µs", "1041d 16h 7ms", "16h 56m 40s 123µs", "16m 40s", "1ms", "1s", "1s 500ms", "1µs", "999µs"}},
		{"Duration(ms)", cast(durations, ursus.Duration(ursus.Milli)),
			[]string{"-1041d -16h", "-1ms", "0ms", "1000000d 1ms", "1041666d 16h 7s", "11d 13h 46m 40s", "16m 40s", "1ms", "1s", "1s 1ms", "25m", "706d 26m 40s 123ms", "999ms"}},
		{"Date", cast(ursus.Values("v", []int32{-800000, 3000000, 0, -719162}), ursus.Date),
			[]string{"+10183-09-21", "-0221-09-04", "0001-01-01", "1970-01-01"}},
		{"Datetime(µs)", cast(ursus.Values("v", []int64{-62135596800000000 - 86400000000, 253402300800000000, 1704164645123456}), ursus.Datetime(ursus.Micro, "")),
			[]string{"+10000-01-01 00:00:00", "0000-12-31 00:00:00", "2024-01-02 03:04:05.123456"}},
		{"Datetime(ns)", cast(ursus.Values("v", []int64{1_000_000_123, 1_500_000_000, 1_000_000_000}), ursus.Datetime(ursus.Nano, "")),
			[]string{"1970-01-01 00:00:01", "1970-01-01 00:00:01.000000123", "1970-01-01 00:00:01.500"}},
		{"Datetime(ms)", cast(ursus.Values("v", []int64{1_000_123, 1_000_000}), ursus.Datetime(ursus.Milli, "")),
			[]string{"1970-01-01 00:16:40", "1970-01-01 00:16:40.123"}},
		{"Time", cast(ursus.Values("v", []int64{1, 1000, 1_000_000, 86_399_999_999_999, 11045000000000}), ursus.TimeOf(ursus.Nano)),
			[]string{"00:00:00.000000001", "00:00:00.000001", "00:00:00.001", "03:04:05", "23:59:59.999999999"}},
		{"Europe/Berlin", zoned("Europe/Berlin"), []string{"2024-01-02 03:04:05 CET", "2024-07-02 03:04:05.001 CEST"}},
		{"Asia/Kolkata", zoned("Asia/Kolkata"), []string{"2024-01-02 03:04:05 IST", "2024-07-02 03:04:05.001 IST"}},
		{"Asia/Tashkent", zoned("Asia/Tashkent"), []string{"2024-01-02 03:04:05 +05", "2024-07-02 03:04:05.001 +05"}},
		{"UTC", zoned("UTC"), []string{"2024-01-02 03:04:05 UTC", "2024-07-02 03:04:05.001 UTC"}},
		{"America/New_York", zoned("America/New_York"), []string{"2024-01-02 03:04:05 EST", "2024-07-02 03:04:05.001 EDT"}},
		{"Binary", ursus.Frame(ursus.Values("v", [][]byte{[]byte("a'b"), []byte(`q"`), []byte(`back\`), []byte("nl\n"),
			[]byte("\t\r"), []byte("ü"), {0x7f}, []byte(" ~"), {}})),
			[]string{`b""`, `b"\x09\x0d"`, `b"\x20~"`, `b"\x7f"`, `b"\xc3\xbc"`, `b"a'b"`, `b"back\"`, `b"nl\x0a"`, `b"q""`}},
		{"Decimal", cast(ursus.Values("v", []string{"1.50", "2.25", "-3.00", "-0.05", "0.00"}), ursus.Decimal(10, 2)),
			[]string{"-0.05", "-3.00", "0.00", "1.50", "2.25"}},
		{"Decimal of no scale", cast(ursus.Values("v", []string{"1", "-20"}), ursus.Decimal(10, 0)), []string{"-20", "1"}},
		{"UInt64", ursus.Frame(ursus.Values("v", []uint64{math.MaxUint64, 0})), []string{"0", "18446744073709551615"}},
		{"Int8", ursus.Frame(ursus.Values("v", []int8{-128, 5, 127})), []string{"-128", "127", "5"}},
		{"Boolean", ursus.Frame(ursus.ValuesNullable("v", []bool{true, false, false, true}, []bool{true, true, false, true})),
			[]string{"false", "null", "true"}},
		{"Enum", cast(ursus.Values("v", []string{"b", "a", "b"}), ursus.Enum("b", "a")), []string{"a", "b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := collect(t, tc.lf).ToDummies(t.Context(), ursus.DummiesOptions{})
			if err != nil {
				t.Fatal(err)
			}
			names := got.Columns()
			for i, name := range names {
				names[i] = strings.TrimPrefix(name, "v_")
			}
			if !slices.Equal(names, tc.want) {
				t.Errorf("names\n got %q\nwant %q", names, tc.want)
			}
		})
	}
}

// dfOf is a frame of these columns.
func dfOf(t *testing.T, cols ...*ursus.Column) *ursus.DataFrame {
	t.Helper()
	return collect(t, ursus.Frame(cols...))
}
