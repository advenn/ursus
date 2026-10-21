package examples_test

import (
	"context"
	"fmt"
	"log"

	"github.com/advenn/ursus"
)

// --- the user guide: selecting and filtering ----------------------------------------

func Example_selectByName() {
	df, err := sales().
		Select(
			ursus.Col("order_id"),
			ursus.ColRegex("^(qty|price)$"),
			ursus.ColDType(ursus.String),
		).
		Head(3).
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (3, 5)
	// ┌──────────┬───────┬─────────┬─────────┬─────────┐
	// │ order_id │ qty   │ price   │ region  │ product │
	// │ Int64    │ Int64 │ Float64 │ String  │ String  │
	// ├──────────┼───────┼─────────┼─────────┼─────────┤
	// │ 1        │ 10    │ 1.5     │ "north" │ "apple" │
	// │ 2        │ 4     │ 1.5     │ "south" │ "apple" │
	// │ 3        │ 7     │ 2.25    │ "north" │ "pear"  │
	// └──────────┴───────┴─────────┴─────────┴─────────┘
}

func Example_selectExclude() {
	df, err := sales().
		Select(ursus.Exclude("order_id", "date")).
		Head(3).
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (3, 4)
	// ┌─────────┬─────────┬───────┬─────────┐
	// │ region  │ product │ qty   │ price   │
	// │ String  │ String  │ Int64 │ Float64 │
	// ├─────────┼─────────┼───────┼─────────┤
	// │ "north" │ "apple" │ 10    │ 1.5     │
	// │ "south" │ "apple" │ 4     │ 1.5     │
	// │ "north" │ "pear"  │ 7     │ 2.25    │
	// └─────────┴─────────┴───────┴─────────┘
}

func Example_selectDropRename() {
	df, err := sales().
		Drop("order_id", "date").
		Rename(map[string]string{"qty": "quantity", "price": "unit_price"}).
		Head(3).
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (3, 4)
	// ┌─────────┬─────────┬──────────┬────────────┐
	// │ region  │ product │ quantity │ unit_price │
	// │ String  │ String  │ Int64    │ Float64    │
	// ├─────────┼─────────┼──────────┼────────────┤
	// │ "north" │ "apple" │ 10       │ 1.5        │
	// │ "south" │ "apple" │ 4        │ 1.5        │
	// │ "north" │ "pear"  │ 7        │ 2.25       │
	// └─────────┴─────────┴──────────┴────────────┘
}

func Example_filterPredicates() {
	df, err := sales().
		Filter(
			ursus.Col("product").IsIn("apple", "fig"),
			ursus.Col("qty").IsBetween(4, 10),
		).
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (3, 6)
	// ┌──────────┬────────────┬─────────┬─────────┬───────┬─────────┐
	// │ order_id │ date       │ region  │ product │ qty   │ price   │
	// │ Int64    │ Date       │ String  │ String  │ Int64 │ Float64 │
	// ├──────────┼────────────┼─────────┼─────────┼───────┼─────────┤
	// │ 1        │ 2026-01-03 │ "north" │ "apple" │ 10    │ 1.5     │
	// │ 2        │ 2026-01-03 │ "south" │ "apple" │ 4     │ 1.5     │
	// │ 5        │ 2026-01-05 │ "south" │ "fig"   │ 9     │ 3       │
	// └──────────┴────────────┴─────────┴─────────┴───────┴─────────┘
}

func Example_filterOr() {
	df, err := sales().
		Filter(ursus.Col("region").Eq("east").Or(ursus.Col("price").Ge(3.0))).
		Select(ursus.Col("order_id"), ursus.Col("region"), ursus.Col("price")).
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (4, 3)
	// ┌──────────┬─────────┬─────────┐
	// │ order_id │ region  │ price   │
	// │ Int64    │ String  │ Float64 │
	// ├──────────┼─────────┼─────────┤
	// │ 4        │ "east"  │ 2.25    │
	// │ 5        │ "south" │ 3       │
	// │ 6        │ "east"  │ 1.5     │
	// │ 7        │ "north" │ 3       │
	// └──────────┴─────────┴─────────┘
}

func Example_filterAggregate() {
	df, err := sales().
		Filter(ursus.Col("qty").Gt(ursus.Col("qty").Mean())).
		Select(ursus.Col("order_id"), ursus.Col("qty")).
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (4, 2)
	// ┌──────────┬───────┐
	// │ order_id │ qty   │
	// │ Int64    │ Int64 │
	// ├──────────┼───────┤
	// │ 1        │ 10    │
	// │ 3        │ 7     │
	// │ 5        │ 9     │
	// │ 6        │ 12    │
	// └──────────┴───────┘
}

func Example_filterNulls() {
	ctx := context.Background()
	scores := ursus.Frame(
		ursus.Values("name", []string{"ana", "ben", "cai", "dee"}),
		ursus.ValuesNullable("score",
			[]int64{90, 40, 0, 75},
			[]bool{true, true, false, true}),
	)
	kept, err := scores.Filter(ursus.Col("score").Ge(50)).Collect(ctx)
	if err != nil {
		log.Fatal(err)
	}
	removed, err := scores.Remove(ursus.Col("score").Ge(50)).Collect(ctx)
	if err != nil {
		log.Fatal(err)
	}
	missing, err := scores.Filter(ursus.Col("score").IsNull()).Collect(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(kept)
	fmt.Println(removed)
	fmt.Println(missing)
	// Output:
	// shape: (2, 2)
	// ┌────────┬───────┐
	// │ name   │ score │
	// │ String │ Int64 │
	// ├────────┼───────┤
	// │ "ana"  │ 90    │
	// │ "dee"  │ 75    │
	// └────────┴───────┘
	//
	// shape: (1, 2)
	// ┌────────┬───────┐
	// │ name   │ score │
	// │ String │ Int64 │
	// ├────────┼───────┤
	// │ "ben"  │ 40    │
	// └────────┴───────┘
	//
	// shape: (1, 2)
	// ┌────────┬───────┐
	// │ name   │ score │
	// │ String │ Int64 │
	// ├────────┼───────┤
	// │ "cai"  │ null  │
	// └────────┴───────┘
}

func Example_sortKeys() {
	df, err := sales().
		Sort(ursus.Asc(ursus.Col("region")), ursus.Desc(ursus.Col("qty"))).
		Select(ursus.Col("region"), ursus.Col("product"), ursus.Col("qty")).
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (8, 3)
	// ┌─────────┬─────────┬───────┐
	// │ region  │ product │ qty   │
	// │ String  │ String  │ Int64 │
	// ├─────────┼─────────┼───────┤
	// │ "east"  │ "apple" │ 12    │
	// │ "east"  │ "pear"  │ 2     │
	// │ "north" │ "apple" │ 10    │
	// │ "north" │ "pear"  │ 7     │
	// │ "north" │ "fig"   │ 3     │
	// │ "south" │ "fig"   │ 9     │
	// │ "south" │ "pear"  │ 6     │
	// │ "south" │ "apple" │ 4     │
	// └─────────┴─────────┴───────┘
}

func Example_sortNulls() {
	df, err := ursus.Frame(
		ursus.ValuesNullable("x", []int64{3, 0, 1, 2}, []bool{true, false, true, true}),
	).Sort(ursus.Desc(ursus.Col("x")).NullsLast()).Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (4, 1)
	// ┌───────┐
	// │ x     │
	// │ Int64 │
	// ├───────┤
	// │ 3     │
	// │ 2     │
	// │ 1     │
	// │ null  │
	// └───────┘
}

func Example_topK() {
	df, err := sales().
		TopK(3, ursus.Asc(ursus.Col("qty"))).
		Select(ursus.Col("order_id"), ursus.Col("product"), ursus.Col("qty")).
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (3, 3)
	// ┌──────────┬─────────┬───────┐
	// │ order_id │ product │ qty   │
	// │ Int64    │ String  │ Int64 │
	// ├──────────┼─────────┼───────┤
	// │ 6        │ "apple" │ 12    │
	// │ 1        │ "apple" │ 10    │
	// │ 5        │ "fig"   │ 9     │
	// └──────────┴─────────┴───────┘
}

func Example_unique() {
	df, err := sales().
		Unique("region").
		Select(ursus.Col("region"), ursus.Col("order_id")).
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (3, 2)
	// ┌─────────┬──────────┐
	// │ region  │ order_id │
	// │ String  │ Int64    │
	// ├─────────┼──────────┤
	// │ "north" │ 1        │
	// │ "south" │ 2        │
	// │ "east"  │ 4        │
	// └─────────┴──────────┘
}

func Example_uniqueKeepLast() {
	df, err := sales().
		UniqueKeep(ursus.KeepLast, "region").
		Select(ursus.Col("region"), ursus.Col("order_id")).
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (3, 2)
	// ┌─────────┬──────────┐
	// │ region  │ order_id │
	// │ String  │ Int64    │
	// ├─────────┼──────────┤
	// │ "east"  │ 6        │
	// │ "north" │ 7        │
	// │ "south" │ 8        │
	// └─────────┴──────────┘
}

func Example_slices() {
	ctx := context.Background()
	ids := func(lf *ursus.LazyFrame) []int64 {
		df, err := lf.Collect(ctx)
		if err != nil {
			log.Fatal(err)
		}
		s, err := df.Column[int64]("order_id")
		if err != nil {
			log.Fatal(err)
		}
		return s.Values()
	}
	fmt.Println("head 3:     ", ids(sales().Head(3)))
	fmt.Println("tail 2:     ", ids(sales().Tail(2)))
	fmt.Println("slice 2, 3: ", ids(sales().Slice(2, 3)))
	fmt.Println("slice -3, 2:", ids(sales().Slice(-3, 2)))
	fmt.Println("reverse:    ", ids(sales().Reverse()))
	// Output:
	// head 3:      [1 2 3]
	// tail 2:      [7 8]
	// slice 2, 3:  [3 4 5]
	// slice -3, 2: [6 7]
	// reverse:     [8 7 6 5 4 3 2 1]
}

func Example_withRowIndex() {
	df, err := sales().
		Filter(ursus.Col("region").Eq("south")).
		WithRowIndex("n", 1).
		Select(ursus.Col("n"), ursus.Col("order_id"), ursus.Col("product")).
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (3, 3)
	// ┌────────┬──────────┬─────────┐
	// │ n      │ order_id │ product │
	// │ Uint32 │ Int64    │ String  │
	// ├────────┼──────────┼─────────┤
	// │ 1      │ 2        │ "apple" │
	// │ 2      │ 5        │ "fig"   │
	// │ 3      │ 8        │ "pear"  │
	// └────────┴──────────┴─────────┘
}

func Example_selectInsertColumn() {
	lines := sales().
		Select(ursus.Col("order_id"), ursus.Col("qty"), ursus.Col("price")).
		Head(3)
	revenue := ursus.Col("qty").Mul(ursus.Col("price")).Alias("revenue")
	show(lines.InsertColumn(1, revenue))
	show(lines.InsertColumn(-1, ursus.Values("note", []string{"gift", "", "rush"})))
	// Output:
	// shape: (3, 4)
	// ┌──────────┬─────────┬───────┬─────────┐
	// │ order_id │ revenue │ qty   │ price   │
	// │ Int64    │ Float64 │ Int64 │ Float64 │
	// ├──────────┼─────────┼───────┼─────────┤
	// │ 1        │ 15      │ 10    │ 1.5     │
	// │ 2        │ 6       │ 4     │ 1.5     │
	// │ 3        │ 15.75   │ 7     │ 2.25    │
	// └──────────┴─────────┴───────┴─────────┘
	//
	// shape: (3, 4)
	// ┌──────────┬───────┬────────┬─────────┐
	// │ order_id │ qty   │ note   │ price   │
	// │ Int64    │ Int64 │ String │ Float64 │
	// ├──────────┼───────┼────────┼─────────┤
	// │ 1        │ 10    │ "gift" │ 1.5     │
	// │ 2        │ 4     │ ""     │ 1.5     │
	// │ 3        │ 7     │ "rush" │ 2.25    │
	// └──────────┴───────┴────────┴─────────┘
}

func Example_selectHashRows() {
	visits := ursus.Frame(
		ursus.Values("user", []string{"ada", "ben", "ada"}),
		ursus.Values("page", []string{"/home", "/home", "/home"}),
	)
	show(visits.WithColumns(ursus.HashRows(0).Alias("hash")))
	// Output:
	// shape: (3, 3)
	// ┌────────┬─────────┬──────────────────────┐
	// │ user   │ page    │ hash                 │
	// │ String │ String  │ Uint64               │
	// ├────────┼─────────┼──────────────────────┤
	// │ "ada"  │ "/home" │ 15332602214641343754 │
	// │ "ben"  │ "/home" │ 17635480945181825081 │
	// │ "ada"  │ "/home" │ 15332602214641343754 │
	// └────────┴─────────┴──────────────────────┘
}
