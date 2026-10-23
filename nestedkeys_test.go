package ursus_test

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/ursustest"
)

type listKeyRow struct {
	L *[]*int64
	V int64
}

// TestNestedKeys is step 344: a List, an Array or a Struct is a key, of a group-by,
// a unique, a join and a window, element by element and field by field, as Polars
// keys them. Every answer is Polars 1.44.1's but where noted.
func TestNestedKeys(t *testing.T) {
	ctx := t.Context()
	c := ursus.Col
	i := func(v int64) *int64 { return &v }
	list := func(vs ...*int64) *[]*int64 {
		if vs == nil {
			vs = []*int64{} // an empty list: a nil slice is FromRows' null
		}
		return &vs
	}
	lf := ursus.FromRows([]listKeyRow{
		{list(i(1), i(2)), 1}, {list(i(1), i(2)), 2}, {list(), 3}, {nil, 4},
		{list(i(1), nil), 5}, {list(i(1), nil), 6}, {list(i(2), i(1)), 7}, {list(nil, i(1)), 8},
	}).Select(c("L").Alias("l"), c("V").Alias("v"))
	sums := func(q *ursus.LazyFrame) string {
		t.Helper()
		df, err := q.Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := df.ToMaps()
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, len(rows))
		for k, r := range rows {
			out[k] = fmt.Sprint(r["l"], ":", r["v"])
		}
		return strings.Join(out, " ")
	}
	// [] is not null, and a null element is a value, as Polars groups them.
	if got := sums(lf.GroupBy(c("l")).MaintainOrder().Agg(c("v").Sum())); got != "[1 2]:3 []:3 <nil>:4 [1 <nil>]:11 [2 1]:7 [<nil> 1]:8" {
		t.Errorf("group_by a List: %s", got)
	}
	if got := cells(t, lf.UniqueKeep(ursus.KeepFirst, "l").Sort(ursus.Asc(c("v"))), "v"); got != "1 3 4 5 7 8 Int64" {
		t.Errorf("unique by a List: %s", got)
	}
	if got := cells(t, lf.Select(c("v").Sum().Over(c("l"))), "v"); got != "3 3 3 4 11 11 7 8 Int128" {
		t.Errorf("over a List: %s", got)
	}
	right := ursus.FromRows([]listKeyRow{{list(i(1), i(2)), 10}, {list(i(2), i(1)), 20}, {nil, 30}}).
		Select(c("L").Alias("l"), c("V").Alias("w"))
	if got := cells(t, lf.Join(right, ursus.JoinOn(c("l"))).Sort(ursus.Asc(c("v"))), "w"); got != "10 10 20 Int64" {
		t.Errorf("join on a List, a null key matching none: %s", got)
	}
	// Polars' n_unique of the List column is 6, the null among them; ursus's passes
	// it over, as its n_unique of any type does (feature-gaps §9).
	if got := cells(t, lf.Select(c("l").NUnique()), "l"); got != "5 Uint64" {
		t.Errorf("n_unique of a List: %s", got)
	}

	// NaN is NaN and -0.0 is 0.0 within a list too; each group's key is its first.
	floats := ursus.Frame(f64s("x", math.NaN(), math.NaN(), 0.0, math.Copysign(0, -1)), i64s("v", 1, 2, 3, 4)).
		Select(ursus.ConcatList(c("x")).Alias("l"), c("v"))
	if got := sums(floats.GroupBy(c("l")).MaintainOrder().Agg(c("v").Sum())); got != "[NaN]:3 [0]:7" {
		t.Errorf("group_by a List of floats: %s", got)
	}

	// A Struct, field by field, a null struct apart from one of null fields.
	structs := ursus.Frame(i64s("a", 1, 1, nil, nil), strs("b", "x", "x", "x", nil), i64s("v", 1, 2, 3, 4)).
		Select(ursus.When(c("v").Eq(int64(4))).Then(ursus.Null(ursus.StructOf(ursus.Field{Name: "a", Type: ursus.Int64, Nullable: true},
			ursus.Field{Name: "b", Type: ursus.String, Nullable: true}))).
			Otherwise(ursus.Struct(c("a"), c("b"))).Alias("l"), c("v"))
	if got := sums(structs.GroupBy(c("l")).MaintainOrder().Agg(c("v").Sum())); got != "map[a:1 b:x]:3 map[a:<nil> b:x]:3 <nil>:4" {
		t.Errorf("group_by a Struct: %s", got)
	}
	if got := cells(t, structs.Join(structs.Select(c("l"), c("v").Alias("w")), ursus.JoinOn(c("l"))).
		Sort(ursus.Asc(c("v")), ursus.Asc(c("w"))), "w"); got != "1 2 1 2 3 Int64" {
		t.Errorf("join on a Struct: %s", got)
	}

	// An Array.
	arrays := ursus.Frame(i64s("x", 1, 1, 3), i64s("y", 2, 2, 4), i64s("v", 1, 2, 3)).
		Select(ursus.ConcatList(c("x"), c("y")).List().ToArray(2).Alias("l"), c("v"))
	if got := sums(arrays.GroupBy(c("l")).MaintainOrder().Agg(c("v").Sum())); got != "[1 2]:3 [3 4]:3" {
		t.Errorf("group_by an Array: %s", got)
	}

	// Past a memory limit a group-by of List keys partitions them to disk.
	const n = 6000
	ks := make([]int64, n)
	for r := range ks {
		ks[r] = int64(r % 1500)
	}
	many := ursus.Frame(ursus.Values("k", ks)).Select(ursus.ConcatList(c("k"), c("k").Mul(int64(3))).Alias("l"), c("k").Alias("v"))
	q := many.GroupBy(c("l")).Agg(c("v").Sum(), ursus.Len().Alias("n")).Select(c("l").List().First().Alias("k"), c("v"), c("n"))
	got, stats, err := spillCollect(t, q, 32<<10, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Spills == 0 {
		t.Error("a group-by of List keys under 32 KiB did not spill")
	}
	ursustest.AssertFrameEqual(t, got, collect(t, q), ursustest.IgnoreRowOrder())

	// A List of Structs is a key too; a sort of a List is still refused, having no
	// order.
	if _, err := lf.Sort(ursus.Asc(c("l"))).Collect(ctx); err == nil || !strings.Contains(err.Error(), "sort") {
		t.Errorf("a List sorted: %v", err)
	}
}
