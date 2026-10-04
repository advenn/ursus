package ursus_test

// At what type is a join key compared? audit.md §4 and §11 item 6.
//
// Each side's key resolves against its own schema, and the two are compared in one
// promoted type. These cases are every way that answer was wrong, each answered by
// hand under the rule chosen for this step: a key meets the other side's at a type
// that holds BOTH exactly, it is cast there strictly, and a pair with no such type
// is refused at plan time with a cast the caller can write.

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/bitmap"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/ursustest"
)

// knownJoinDefects names each case that answers wrongly today, with what it answers.
var knownJoinDefects = map[string]string{
	"J3 spilled right join, key (1, null)":                  "k1 is null: coalesced from the left's padding",
	"J5 1:m with unmatched duplicate left keys":             "passes: an unmatched key is never checked",
	"J5 1:m with duplicate null left keys under NullsEqual": "passes",
	"J6 AsOfBy null by-keys":                                "the null by-key matched the right null",
	"O6 filter on a coalesced key of two widths":            "0 rows: k*k computed at Int32 in the pushed filter",
}

type joinCase struct {
	name   string
	got    *ursus.LazyFrame
	want   *ursus.LazyFrame // nil for a refusal
	opts   []ursus.CollectOption
	spills *ursus.MemoryStats // when set, the case must have spilled
	kind   error              // the refusal's sentinel
	refuse []string           // what a refusal must say
	notSay string
}

// ticks builds a temporal column from its tick counts; valid nil means all valid.
func ticks(name string, dt dtype.DataType, v []int64, valid []bool) *ursus.Column {
	if valid == nil {
		return data.NewFixed(name, dt, v, bitmap.AllSet(len(v)))
	}
	b := bitmap.NewBuilder(len(v))
	for _, ok := range valid {
		b.Append(ok)
	}
	return data.NewFixed(name, dt, v, b.Finish())
}

func TestJoinKeysByHand(t *testing.T) {
	c := ursus.Col
	ms, ns := dtype.Datetime(dtype.Milli, ""), dtype.Datetime(dtype.Nano, "")
	const y3000 = 32503680000000 // 3000-01-01 in milliseconds: past int64 nanoseconds
	const p53 = 1 << 53
	on := ursus.JoinOn(c("k"))
	left := ursus.JoinHow(ursus.JoinLeft)

	// A right side of many batches, large enough that a tiny memory limit spills its
	// build, with one row whose second key is null: (1, null), marked by rv = -1.
	var parts []*ursus.LazyFrame
	for b := range int64(16) {
		k := make([]int64, 256)
		for i := range k {
			k[i] = b*256 + int64(i)
		}
		parts = append(parts, ursus.Frame(ursus.Values("k1", k), ursus.Values("k2", k), ursus.Values("rv", k)))
	}
	parts = append(parts, ursus.Frame(ursus.Values("k1", []int64{1}),
		ursus.ValuesNullable("k2", []int64{0}, []bool{false}), ursus.Values("rv", []int64{-1})))
	spillRight := ursus.Concat(parts)
	spillLeft := ursus.Frame(ursus.Values("k1", []int64{5}), ursus.Values("k2", []int64{5}), ursus.Values("lv", []int64{50}))
	var stats ursus.MemoryStats
	spill := []ursus.CollectOption{ursus.WithBatchSize(256), ursus.WithMemoryLimit(8 << 10),
		ursus.WithSpillDir(t.TempDir()), ursus.WithMemoryStats(&stats)}

	cases := []joinCase{
		// --- J1: as-of by-keys of different widths ---
		{name: "J1 AsOfBy Int32 against Int64",
			got: ursus.Frame(ursus.Values("k", []int64{10, 20}), ursus.Values("g", []int32{1, 2}),
				ursus.Values("lv", []int64{0, 1})).
				JoinAsOf(ursus.Frame(ursus.Values("k", []int64{5, 15}), ursus.Values("g", []int64{1, 2}),
					ursus.Values("rv", []int64{100, 200})), ursus.AsOfOn(c("k")), ursus.AsOfBy(c("g"))).
				Select(c("lv"), c("rv")),
			want: ursus.Frame(ursus.Values("lv", []int64{0, 1}), ursus.Values("rv", []int64{100, 200}))},

		// --- J2: a temporal key the finer unit cannot hold ---
		{name: "J2 Datetime(ms) year 3000 against Datetime(ns), left join",
			got: ursus.Frame(ticks("k", ms, []int64{y3000, 1000}, nil), ursus.Values("lv", []int64{0, 1})).
				Join(ursus.Frame(ticks("k", ns, []int64{1_000_000_000}, nil), ursus.Values("rv", []int64{9})), on, left),
			kind: ursus.ErrValue, refuse: []string{"k", "Datetime(ns)"}},
		{name: "J2 the same, nullable key",
			got: ursus.Frame(ticks("k", ms, []int64{y3000, 1000, 0}, []bool{true, true, false}),
				ursus.Values("lv", []int64{0, 1, 2})).
				Join(ursus.Frame(ticks("k", ns, []int64{1_000_000_000}, nil), ursus.Values("rv", []int64{9})), on, left),
			kind: ursus.ErrValue, refuse: []string{"k", "Datetime(ns)"}},
		{name: "J2 the same, under NullsEqual against a null key",
			got: ursus.Frame(ticks("k", ms, []int64{y3000}, nil), ursus.Values("lv", []int64{0})).
				Join(ursus.Frame(ticks("k", ns, []int64{0}, []bool{false}), ursus.Values("rv", []int64{9})),
					on, ursus.JoinNullsEqual(true)),
			kind: ursus.ErrValue, refuse: []string{"k", "Datetime(ns)"}},

		// --- J3: a spilled right join keeps the right key it coalesces ---
		{name: "J3 spilled right join, key (1, null)",
			got: spillLeft.Join(spillRight, ursus.JoinOn(c("k1"), c("k2")), ursus.JoinHow(ursus.JoinRight)).
				// Reading both sides keeps the filter above the join: pushed into the
				// right side, it would shrink the build to one row, which never spills.
				Filter(c("rv").Eq(-1).Or(c("lv").Eq(-12345))).Select(c("k1"), c("k2"), c("rv")),
			want: ursus.Frame(ursus.Values("k1", []int64{1}),
				ursus.ValuesNullable("k2", []int64{0}, []bool{false}), ursus.Values("rv", []int64{-1})),
			opts: spill, spills: &stats},
		{name: "control: the same right join, in memory",
			got: spillLeft.Join(spillRight, ursus.JoinOn(c("k1"), c("k2")), ursus.JoinHow(ursus.JoinRight)).
				Filter(c("rv").Eq(-1).Or(c("lv").Eq(-12345))).Select(c("k1"), c("k2"), c("rv")),
			want: ursus.Frame(ursus.Values("k1", []int64{1}),
				ursus.ValuesNullable("k2", []int64{0}, []bool{false}), ursus.Values("rv", []int64{-1}))},

		// --- J5: Validate checks every left key ---
		{name: "J5 1:m with unmatched duplicate left keys",
			got: ursus.Frame(ursus.Values("k", []int64{1, 1, 2})).
				Join(ursus.Frame(ursus.Values("k", []int64{2})), on, ursus.JoinValidate(ursus.ValidateOneToMany)),
			kind: ursus.ErrValue, refuse: []string{"requires unique keys on the left side"}},
		{name: "J5 1:m with duplicate null left keys under NullsEqual",
			got: ursus.Frame(ursus.ValuesNullable("k", []int64{0, 0, 2}, []bool{false, false, true})).
				Join(ursus.Frame(ursus.Values("k", []int64{2})), on,
					ursus.JoinNullsEqual(true), ursus.JoinValidate(ursus.ValidateOneToMany)),
			kind: ursus.ErrValue, refuse: []string{"requires unique keys on the left side"}},
		{name: "J5 control: duplicate null left keys, nulls not equal",
			got: ursus.Frame(ursus.ValuesNullable("k", []int64{0, 0, 2}, []bool{false, false, true})).
				Join(ursus.Frame(ursus.Values("k", []int64{2})), on, ursus.JoinValidate(ursus.ValidateOneToMany)),
			want: ursus.Frame(ursus.Values("k", []int64{2}))},

		// --- J6: a null as-of by-key matches nothing ---
		{name: "J6 AsOfBy null by-keys",
			got: ursus.Frame(ursus.Values("k", []int64{10}), ursus.ValuesNullable("g", []int64{0}, []bool{false})).
				JoinAsOf(ursus.Frame(ursus.Values("k", []int64{5}), ursus.ValuesNullable("g", []int64{0}, []bool{false}),
					ursus.Values("rv", []int64{100})), ursus.AsOfOn(c("k")), ursus.AsOfBy(c("g"))).
				Select(c("k"), c("rv")),
			want: ursus.Frame(ursus.Values("k", []int64{10}), ursus.ValuesNullable("rv", []int64{0}, []bool{false}))},

		// --- J8: no type holds an Int64 and a Float64 exactly ---
		{name: "J8 Int64 against Float64",
			got: ursus.Frame(ursus.Values("k", []int64{p53 + 1})).
				Join(ursus.Frame(ursus.Values("k", []float64{p53})), on),
			kind: ursus.ErrType, refuse: []string{"Int64", "Float64", "exactly"}},
		{name: "J8 Concat of Int64 and Float64",
			got: ursus.Concat([]*ursus.LazyFrame{ursus.Frame(ursus.Values("x", []int64{p53 + 1})),
				ursus.Frame(ursus.Values("x", []float64{1.5}))}),
			kind: ursus.ErrType, refuse: []string{"Int64", "Float64", "exactly"}},
		{name: "J8 Unpivot of Int64 and Float64",
			got: ursus.Frame(ursus.Values("id", []int64{1}), ursus.Values("a", []int64{p53 + 1}),
				ursus.Values("b", []float64{1.5})).Unpivot(ursus.UnpivotOptions{On: []string{"a", "b"}}),
			kind: ursus.ErrType, refuse: []string{"Int64", "Float64", "exactly"}},
		{name: "control: Int32 against Float32 is exact",
			got: ursus.Frame(ursus.Values("k", []int32{16777217, 3})).
				Join(ursus.Frame(ursus.Values("k", []float32{16777216, 3})), on).Select(c("k").Cast(ursus.Int64)),
			want: ursus.Frame(ursus.Values("k", []int64{3}))},
		{name: "control: Int64 against Uint64 meets at Int128",
			got: ursus.Frame(ursus.Values("k", []int64{-1, 1<<62 + 1})).
				Join(ursus.Frame(ursus.Values("k", []uint64{math.MaxUint64, 1<<62 + 1})), on).Select(c("k").Cast(ursus.Int64)),
			want: ursus.Frame(ursus.Values("k", []int64{1<<62 + 1}))},
		{name: "control: Datetime(ms) against Datetime(ns), in range",
			got: ursus.Frame(ticks("k", ms, []int64{1000, 2000}, nil), ursus.Values("lv", []int64{0, 1})).
				Join(ursus.Frame(ticks("k", ns, []int64{2_000_000_000}, nil), ursus.Values("rv", []int64{9})), on).
				Select(c("lv"), c("rv")),
			want: ursus.Frame(ursus.Values("lv", []int64{1}), ursus.Values("rv", []int64{9}))},

		// --- O6: a filter on the coalesced key runs at the key's type ---
		// The output k is Int64, where 50000*50000 = 2.5e9 > 2e9. Pushed into the
		// Int32 side unconverted, the product wraps negative and the row is lost.
		{name: "O6 filter on a coalesced key of two widths",
			got: ursus.Frame(ursus.Values("k", []int32{50000}), ursus.Values("lv", []int64{1})).
				Join(ursus.Frame(ursus.Values("k", []int64{50000}), ursus.Values("rv", []int64{2})), on).
				Filter(c("k").Mul(c("k")).Gt(int64(2_000_000_000))).Select(c("lv"), c("rv")),
			want: ursus.Frame(ursus.Values("lv", []int64{1}), ursus.Values("rv", []int64{2}))},

		// --- J10: the hint names a cast that fixes the mismatch ---
		{name: "J10 UTC against naive Datetime",
			got: ursus.Frame(ticks("k", dtype.Datetime(dtype.Nano, "UTC"), []int64{1}, nil)).
				Join(ursus.Frame(ticks("k", ns, []int64{1}, nil)), on),
			kind: ursus.ErrType, refuse: []string{"Datetime(ns, UTC)"}, notSay: "Cast(ursus.Int64)"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wrong := joinWrong(t, tc)
			why, known := knownJoinDefects[tc.name]
			switch {
			case known && wrong == "":
				t.Errorf("answers correctly now; delete it from knownJoinDefects (%s)", why)
			case known:
				t.Logf("known defect (%s): %s", why, wrong)
			case wrong != "":
				t.Error(wrong)
			}
		})
	}
	for name := range knownJoinDefects {
		if !slices.ContainsFunc(cases, func(c joinCase) bool { return c.name == name }) {
			t.Errorf("knownJoinDefects names %q, which is not a case", name)
		}
	}
}

func joinWrong(t *testing.T, tc joinCase) string {
	df, err := tc.got.Collect(t.Context(), tc.opts...)
	if tc.want == nil {
		switch {
		case err == nil:
			return fmt.Sprintf("answered %s; want a refusal", strings.Join(strings.Fields(df.String()), " "))
		case errors.Is(err, ursus.ErrInternal):
			return "ErrInternal: " + err.Error()
		case !errors.Is(err, tc.kind):
			return fmt.Sprintf("a %s error, want %v: %v", kindOf(err), tc.kind, err)
		}
		for _, w := range tc.refuse {
			if !strings.Contains(err.Error(), w) {
				return fmt.Sprintf("the refusal does not say %q: %v", w, err)
			}
		}
		if tc.notSay != "" && strings.Contains(err.Error(), tc.notSay) {
			return fmt.Sprintf("the refusal says %q: %v", tc.notSay, err)
		}
		return ""
	}
	if err != nil {
		if errors.Is(err, ursus.ErrInternal) {
			return "ErrInternal: " + err.Error()
		}
		return err.Error()
	}
	if tc.spills != nil && tc.spills.Spills == 0 {
		t.Fatal("the fixture did not spill, so it tests nothing about a spilled join")
	}
	want, err := tc.want.Collect(t.Context())
	if err != nil {
		t.Fatalf("the hand answer: %v", err)
	}
	return framesDiffer(t, df, want, ursustest.IgnoreRowOrder())
}
