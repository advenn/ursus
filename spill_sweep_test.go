package ursus_test

// Every spilling operator answers as it does in memory, generated.
//
// A payload column of every element type step 81's sweep uses, plus a List and a
// Struct, rides through each operator that spills: a sort, an ordered group_by, a
// join, a unique, and windows over one and two partitionings. Each runs under a
// limit far below what it holds, must reach disk, and must give the answer it gives
// with no limit — order included, except for the join, whose order under a limit is
// documented as unspecified.

import (
	"slices"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/ursustest"
)

// knownSpillMismatches counts, per shape, the payload types that answer wrongly.
var knownSpillMismatches = map[string]int{}

func TestSpillingAnswersAsInMemory(t *testing.T) {
	c := ursus.Col
	const n, keys = 1200, 600
	k := make([]int64, n)
	seq := make([]int64, n)
	valid := make([]bool, n)
	for i := range n {
		k[i], seq[i], valid[i] = int64((i*7919)%keys), int64(i), i%5 != 0
	}
	list, st := nestedColumns(n)
	type payload struct {
		name string
		col  *ursus.Column
	}
	var payloads []payload
	for _, dt := range nestedElementTypes {
		payloads = append(payloads, payload{dt.String(), spillPayload(t, dt, valid)})
	}
	payloads = append(payloads, payload{"List(String)", list.Rename("v")}, payload{"Struct", st.Rename("v")})

	ordered := ursus.WindowSpec{PartitionBy: []ursus.Expr{c("k")}, OrderBy: []ursus.SortKey{ursus.Desc(c("seq"))}}
	right := ursus.Frame(ursus.Values("k", spillInts(0, keys)), ursus.Values("r", spillInts(keys, 2*keys)))
	shapes := []spillShape{
		{"sort", func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.Sort(ursus.Desc(c("k")), ursus.Asc(c("seq")))
		}, nil},
		{"group_by MaintainOrder", func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.GroupBy(c("k")).MaintainOrder().Agg(c("v").First(), c("seq").Sum())
		}, nil},
		{"join", func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.Join(right, ursus.JoinOn(c("k")))
		}, []ursustest.Option{ursustest.IgnoreRowOrder()}},
		{"unique", func(lf *ursus.LazyFrame) *ursus.LazyFrame { return lf.Unique("k") }, nil},
		{"over two partitionings", func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.WithColumns(c("seq").Sum().Over(c("k")).Alias("t"),
				c("seq").Rank(ursus.RankOrdinal, false).Over(c("k").Mod(50)).Alias("r"))
		}, nil},
		{"over ordered, descending", func(lf *ursus.LazyFrame) *ursus.LazyFrame {
			return lf.WithColumns(c("seq").CumSum(false).OverWith(ordered).Alias("cs"),
				c("v").Shift(1).OverWith(ordered).Alias("vs"))
		}, nil},
	}

	var routes int
	wrong := map[string]int{}
	for _, sh := range shapes {
		for _, p := range payloads {
			routes++
			lf := sh.build(ursus.Frame(ursus.Values("k", k), ursus.Values("seq", seq), p.col))
			why := spillRouteWrong(t, lf, sh.opts)
			if why == "" {
				continue
			}
			wrong[sh.name]++
			if _, known := knownSpillMismatches[sh.name]; !known {
				t.Errorf("%s, %s payload: %s", sh.name, p.name, why)
			}
		}
	}
	for _, sh := range shapes {
		got, want := wrong[sh.name], knownSpillMismatches[sh.name]
		switch {
		case want > 0 && got == 0:
			t.Errorf("%s answers as in memory for every payload now; delete it from knownSpillMismatches", sh.name)
		case want > 0 && got != want:
			t.Errorf("%s: %d payloads answer wrongly, the ratchet says %d", sh.name, got, want)
		}
	}
	for name := range knownSpillMismatches {
		if !slices.ContainsFunc(shapes, func(s spillShape) bool { return s.name == name }) {
			t.Errorf("knownSpillMismatches names %q, which is not a shape", name)
		}
	}
	if routes != len(shapes)*(len(nestedElementTypes)+2) {
		t.Fatalf("%d routes ran — the sweep has gone vacuous", routes)
	}
}

// spillPayload is a column v of type dt holding 1..97 over and over, null where
// valid is false, cast from Int64 as typedColumn's are.
func spillPayload(t *testing.T, dt dtype.DataType, valid []bool) *ursus.Column {
	t.Helper()
	vals := make([]int64, len(valid))
	for i := range vals {
		vals[i] = int64(i%97 + 1)
	}
	e := ursus.Col("v")
	if dt.ID() == dtype.TypeBinary {
		e = e.Cast(ursus.String)
	}
	df, err := ursus.Frame(ursus.ValuesNullable("v", vals, valid)).Select(e.Cast(dt)).Collect(t.Context())
	if err != nil {
		t.Fatalf("building %s: %v", dt, err)
	}
	col, _ := df.Batch().ByName("v")
	return col
}

type spillShape struct {
	name  string
	build func(*ursus.LazyFrame) *ursus.LazyFrame
	opts  []ursustest.Option
}

// spillRouteWrong runs lf under a spilling limit and without one, and says how the
// two differ, or that the first never reached disk.
func spillRouteWrong(t *testing.T, lf *ursus.LazyFrame, opts []ursustest.Option) string {
	t.Helper()
	want, err := lf.Collect(t.Context(), ursus.WithBatchSize(64))
	if err != nil {
		t.Fatalf("in memory: %v", err)
	}
	got, stats, err := spillCollect(t, lf, 8<<10, t.TempDir())
	if err != nil {
		return err.Error()
	}
	if d := framesDiffer(t, got, want, opts...); d != "" {
		return d
	}
	if stats.Spills == 0 {
		return "answered without spilling"
	}
	return ""
}
