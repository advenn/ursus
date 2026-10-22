package ursus_test

import (
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/advenn/ursus"
)

// TestRowsOut is V2 of v0.6.7-scope.md (step 318): Polars' rows_by_key(named=True),
// to_dicts and iter_rows(named=True), the rows as Go values.
func TestRowsOut(t *testing.T) {
	ctx := t.Context()
	type sale struct {
		Region string  `ursus:"region"`
		Amount float64 `ursus:"amount"`
	}
	lf := ursus.Frame(strs("region", "n", "s", "n", nil), f64s("amount", 1.0, 2.0, 3.0, 4.0), i64s("id", 10, 11, 12, 13))
	df, err := lf.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("RowsByKey", func(t *testing.T) {
		got, err := df.RowsByKey[string, sale]("region")
		if err != nil {
			t.Fatal(err)
		}
		want := map[string][]sale{"n": {{"n", 1}, {"n", 3}}, "s": {{"s", 2}}, "": {{"", 4}}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
		// An Int64 key read as an int.
		byID, err := df.RowsByKey[int, sale]("id")
		if err != nil {
			t.Fatal(err)
		}
		if keys := slices.Sorted(maps.Keys(byID)); !slices.Equal(keys, []int{10, 11, 12, 13}) {
			t.Errorf("the keys are %v", keys)
		}
		if _, err := df.RowsByKey[string, sale]("nope"); err == nil || !strings.Contains(err.Error(), `"nope"`) {
			t.Errorf("an unknown key: %v", err)
		}
		if _, err := df.RowsByKey[time.Time, sale]("amount"); err == nil {
			t.Error("a float key read as a time was taken")
		}
		// A null key is the zero key, whatever its slot holds: 7 here.
		held, err := ursus.Frame(ursus.ValuesNullable("k", []int64{5, 7}, []bool{true, false}), f64s("amount", 1.0, 2.0)).
			Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		type amount struct {
			Amount float64 `ursus:"amount"`
		}
		byK, err := held.RowsByKey[int64, amount]("k")
		if err != nil {
			t.Fatal(err)
		}
		if len(byK[0]) != 1 || len(byK[7]) != 0 {
			t.Errorf("the null key's rows are under %v", byK)
		}
	})

	t.Run("ToMaps", func(t *testing.T) {
		got, err := df.ToMaps()
		if err != nil {
			t.Fatal(err)
		}
		want := []map[string]any{
			{"region": "n", "amount": 1.0, "id": int64(10)},
			{"region": "s", "amount": 2.0, "id": int64(11)},
			{"region": "n", "amount": 3.0, "id": int64(12)},
			{"region": nil, "amount": 4.0, "id": int64(13)},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
		// Nested and temporal values.
		nested, err := ursus.Frame(i64s("x", 1, 2), strs("s", "a", "b")).
			Select(ursus.ConcatList(ursus.Col("x"), ursus.Col("x").Mul(10)).Alias("l"),
				ursus.Struct(ursus.Col("x"), ursus.Col("s")).Alias("st"),
				ursus.Lit(at(2024, 1, 2, 3)).Alias("t"),
				ursus.Lit(90*time.Second).Alias("d"),
				ursus.Lit(90*time.Second).Cast(ursus.Duration(ursus.Micro)).Alias("us")).Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		m, err := nested.ToMaps()
		if err != nil {
			t.Fatal(err)
		}
		row := m[1]
		if !reflect.DeepEqual(row["l"], []any{int64(2), int64(20)}) ||
			!reflect.DeepEqual(row["st"], map[string]any{"x": int64(2), "s": "b"}) ||
			!row["t"].(time.Time).Equal(at(2024, 1, 2, 3)) || row["d"] != 90*time.Second || row["us"] != 90*time.Second {
			t.Errorf("got %v", row)
		}
	})

	t.Run("IterRows", func(t *testing.T) {
		var got []sale
		for r, err := range lf.IterRows[sale](ctx, ursus.WithBatchSize(1)) {
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, r)
		}
		want := []sale{{"n", 1}, {"s", 2}, {"n", 3}, {"", 4}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
		// A break stops the query; an error ends the walk.
		n := 0
		for range lf.IterRows[sale](ctx, ursus.WithBatchSize(1)) {
			if n++; n == 2 {
				break
			}
		}
		var errs []error
		for _, err := range lf.Select(ursus.Col("nope")).IterRows[sale](ctx) {
			errs = append(errs, err)
		}
		if len(errs) != 1 || errs[0] == nil {
			t.Errorf("an error: %v", errs)
		}
	})
}
