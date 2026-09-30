package ursus_test

// Two literals that render alike are not the same literal.
//
// Lit.String renders a strong literal's value and not its type, so Lit(int8(100))
// and Lit(int64(100)) both render "lit(100)", as do Lit(1) and Lit(1.0). Four map
// instances deduplicate computations by rendering: the window temporaries, the
// aggregate specs of a group-by and of a temporal group, and the window
// partitionings. Two computations that differ only in a literal's type become one,
// and the second gets the first one's answer — or the first one's type under the
// second one's schema, which is ErrInternal.
//
// Each case below is answered by hand. A case whose two expressions were merged
// cannot answer correctly, whichever of the two it keeps.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/advenn/ursus"
)

// knownLiteralDefects names each case that answers wrongly today. Emptied by the
// commit that fixes them; a listed case that answers correctly fails as stale, and
// an unlisted one that answers wrongly fails.
var knownLiteralDefects = map[string]string{
	"aggregate int8 and int64":  "O3: b gets a's sum, -111",
	"aggregate 1 and 1.0":       "O3: ErrInternal, b is Int64 under a Float64 schema",
	"aggregate float32 and 0.1": "O3: ErrInternal, b is Float32 under a Float64 schema",
	"temporal group":            "O3: b gets a's sum, -128",
	"window temporary":          "O3: b is a's Int8 max, and CollectSchema says Int8",
	"window partition":          "O3: b is partitioned by a's Int8 keys",
	"control: the same literal": "",
	"control: different values": "",
	"control: one expr, reused": "",
}

func TestLiteralsOfDifferentTypesByHand(t *testing.T) {
	c := ursus.Col
	i128 := func(name string) ursus.Expr { return c(name).Cast(ursus.Int128) }
	nums := ursus.Frame(ursus.Values("k", []int64{1, 1}), ursus.Values("i8", []int8{100, 101}),
		ursus.Values("x", []int64{1, 2}), ursus.Values("f", []float32{1, 2}))
	win := ursus.Frame(ursus.Values("g", []string{"a", "a", "b"}), ursus.Values("i8", []int8{100, 101, 1}))
	part := ursus.Frame(ursus.Values("k", []int8{64, -64, 1}), ursus.Values("v", []int64{1, 10, 100}))
	e := c("i8").Add(int8(100)).Sum()

	cases := []struct {
		name      string
		got, want *ursus.LazyFrame
	}{
		// i8 + int8(100) wraps in Int8: 100+100 = -56 and 101+100 = -55, summing to
		// -111. i8 + int64(100) widens first: 200 + 201 = 401.
		{"aggregate int8 and int64",
			nums.GroupBy(c("k")).Agg(c("i8").Add(int8(100)).Sum().Alias("a"),
				c("i8").Add(int64(100)).Sum().Alias("b")),
			ursus.Frame(ursus.Values("k", []int64{1}), ursus.Values("a", []int64{-111}),
				ursus.Values("b", []int64{401})).
				Select(c("k"), i128("a"), i128("b"))},
		// x*1 stays Int64, x*1.0 is Float64; both render "lit(1)".
		{"aggregate 1 and 1.0",
			nums.GroupBy(c("k")).Agg(c("x").Mul(1).Max().Alias("a"), c("x").Mul(1.0).Max().Alias("b")),
			ursus.Frame(ursus.Values("k", []int64{1}), ursus.Values("a", []int64{2}),
				ursus.Values("b", []float64{2}))},
		// float32(0.1) and 0.1 both render "0.1". f + float32(0.1) stays Float32;
		// f + 0.1 is Float64, and float64(float32(2)) + 0.1 is 2.1 exactly as
		// Float64 computes it.
		{"aggregate float32 and 0.1",
			nums.GroupBy(c("k")).Agg(c("f").Add(float32(0.1)).Max().Alias("a"),
				c("f").Add(0.1).Max().Alias("b")),
			ursus.Frame(ursus.Values("k", []int64{1}), ursus.Values("a", []float32{2 + float32(0.1)}),
				ursus.Values("b", []float64{2 + 0.1}))},
		// hourly numbers its rows, so the windows hold v = 0 and v = 1. In Int8,
		// 1 + 127 wraps to -128; in Int64 it is 128.
		{"temporal group",
			hourly(t, "2024-01-01T00:10:00", "2024-01-01T01:05:00").
				GroupByDynamic(c("ts"), ursus.DynamicOptions{Every: ursus.Every("1h")}).
				Agg(c("v").Cast(ursus.Int8).Add(int8(127)).Sum().Alias("a"),
					c("v").Cast(ursus.Int8).Add(int64(127)).Sum().Alias("b")).
				Select(c("a"), c("b")),
			ursus.Frame(ursus.Values("a", []int64{127, -128}), ursus.Values("b", []int64{127, 128})).
				Select(i128("a"), i128("b"))},
		// Group "a" holds 100 and 101. In Int8 they become -56 and -55, max -55;
		// in Int64, 200 and 201, max 201. Group "b" holds 1: 101 either way.
		{"window temporary",
			win.Select(c("i8").Add(int8(100)).Max().Over(c("g")).Alias("a"),
				c("i8").Add(int64(100)).Max().Over(c("g")).Alias("b")),
			ursus.Frame(ursus.Values("a", []int8{-55, -55, 101}), ursus.Values("b", []int64{201, 201, 101}))},
		// k*int8(2) wraps 64 and -64 both to -128, so a's partitions are {0,1} and
		// {2}. k*int64(2) is 128, -128 and 2, three partitions. The two windows
		// differ (sum, max), so only the partition map can merge them.
		{"window partition",
			part.Select(c("v").Sum().Over(c("k").Mul(int8(2))).Alias("a"),
				c("v").Max().Over(c("k").Mul(int64(2))).Alias("b")),
			ursus.Frame(ursus.Values("a", []int64{11, 11, 100}), ursus.Values("b", []int64{1, 10, 100})).
				Select(i128("a"), c("b"))},

		// The controls: what must still merge, or must never have.
		{"control: the same literal",
			nums.GroupBy(c("k")).Agg(c("i8").Add(int8(100)).Sum().Alias("a"),
				c("i8").Add(int8(100)).Sum().Alias("b")),
			ursus.Frame(ursus.Values("k", []int64{1}), ursus.Values("a", []int64{-111}),
				ursus.Values("b", []int64{-111})).
				Select(c("k"), i128("a"), i128("b"))},
		{"control: different values",
			nums.GroupBy(c("k")).Agg(c("x").Mul(2).Max().Alias("a"), c("x").Mul(3).Max().Alias("b")),
			ursus.Frame(ursus.Values("k", []int64{1}), ursus.Values("a", []int64{4}),
				ursus.Values("b", []int64{6}))},
		{"control: one expr, reused",
			nums.GroupBy(c("k")).Agg(e.Alias("a"), e.Alias("b")),
			ursus.Frame(ursus.Values("k", []int64{1}), ursus.Values("a", []int64{-111}),
				ursus.Values("b", []int64{-111})).
				Select(c("k"), i128("a"), i128("b"))},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want, err := tc.want.Collect(t.Context())
			if err != nil {
				t.Fatalf("the hand answer: %v", err)
			}
			wrong := literalWrong(t, tc.got, want)
			why, known := knownLiteralDefects[tc.name]
			switch {
			case known && why != "" && wrong == "":
				t.Errorf("answers correctly now; delete it from knownLiteralDefects (%s)", why)
			case known && why != "":
				t.Logf("known defect (%s): %s", why, wrong)
			case wrong != "":
				t.Error(wrong)
			}
		})
	}
	for name := range knownLiteralDefects {
		if !slices.ContainsFunc(cases, func(c struct {
			name      string
			got, want *ursus.LazyFrame
		}) bool {
			return c.name == name
		}) {
			t.Errorf("knownLiteralDefects names %q, which is not a case", name)
		}
	}
}

// literalWrong reports how lf's result and its planned schema differ from want, or
// "" when they agree.
func literalWrong(t *testing.T, lf *ursus.LazyFrame, want *ursus.DataFrame) string {
	var wrong []string
	df, err := lf.Collect(t.Context())
	switch {
	case errors.Is(err, ursus.ErrInternal):
		wrong = append(wrong, "ErrInternal: "+err.Error())
	case err != nil:
		wrong = append(wrong, err.Error())
	default:
		if d := framesDiffer(t, df, want); d != "" {
			wrong = append(wrong, d)
		}
	}
	s, err := lf.CollectSchema(context.Background())
	if err != nil {
		wrong = append(wrong, "CollectSchema: "+err.Error())
	} else {
		for _, wf := range want.Schema().FieldSlice() {
			if gf, ok := s.ByName(wf.Name); !ok || gf.Type != wf.Type {
				wrong = append(wrong, fmt.Sprintf("CollectSchema: %s is %s, want %s", wf.Name, gf.Type, wf.Type))
			}
		}
	}
	if len(wrong) == 0 {
		return ""
	}
	return fmt.Sprint(wrong)
}

// TestLiteralRenderingsCollide is the premise the identity key exists for: each
// pair renders identically and is a different literal. If a pair stopped
// colliding, rendering would have become an identity for it, and this test says
// so rather than letting the sweep in the next commit test nothing.
func TestLiteralRenderingsCollide(t *testing.T) {
	for _, p := range []struct {
		name string
		a, b ursus.Expr
	}{
		{"int8 and int64", ursus.Lit(int8(100)), ursus.Lit(int64(100))},
		{"uint8 and int64", ursus.Lit(uint8(1)), ursus.Lit(int64(1))},
		{"int and float", ursus.Lit(1), ursus.Lit(1.0)},
		{"float32 and float64", ursus.Lit(float32(0.1)), ursus.Lit(0.1)},
		{"NaN widths", ursus.Lit(float32(math.NaN())), ursus.Lit(math.NaN())},
		{"Inf widths", ursus.Lit(float32(math.Inf(1))), ursus.Lit(math.Inf(1))},
		{"a Duration and int64", ursus.Lit(time.Duration(5)), ursus.Lit(int64(5))},
	} {
		if a, b := p.a.String(), p.b.String(); a != b {
			t.Errorf("%s: %s and %s no longer render alike", p.name, a, b)
		}
	}
}
