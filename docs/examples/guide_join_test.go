package examples_test

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/advenn/ursus"
)

// customers and orders are the joins chapter's frames: customer 2 has no order, and
// order 13's customer, 5, is no customer.
func customers() *ursus.LazyFrame {
	return ursus.Frame(
		ursus.Values("customer_id", []int64{1, 2, 3, 4}),
		ursus.Values("name", []string{"Ada", "Ben", "Cai", "Dee"}),
	)
}

func orders() *ursus.LazyFrame {
	return ursus.Frame(
		ursus.Values("order", []int64{10, 11, 12, 13, 14}),
		ursus.Values("customer_id", []int64{1, 3, 1, 5, 4}),
		ursus.Values("amount", []float64{25, 40, 10, 99, 15}),
	)
}

// show collects a query and prints it.
func show(lf *ursus.LazyFrame) {
	df, err := lf.Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
}

// --- the user guide: joins ---------------------------------------------------------

func Example_joinInner() {
	on := ursus.JoinOn(ursus.Col("customer_id"))
	show(orders().Join(customers(), on, ursus.JoinMaintainOrder(ursus.JoinOrderLeft)))
	// Output:
	// shape: (4, 4)
	// ┌───────┬─────────────┬─────────┬────────┐
	// │ order │ customer_id │ amount  │ name   │
	// │ Int64 │ Int64       │ Float64 │ String │
	// ├───────┼─────────────┼─────────┼────────┤
	// │ 10    │ 1           │ 25      │ "Ada"  │
	// │ 11    │ 3           │ 40      │ "Cai"  │
	// │ 12    │ 1           │ 10      │ "Ada"  │
	// │ 14    │ 4           │ 15      │ "Dee"  │
	// └───────┴─────────────┴─────────┴────────┘
}

func Example_joinLeft() {
	on := ursus.JoinOn(ursus.Col("customer_id"))
	show(orders().Join(customers(), on, ursus.JoinHow(ursus.JoinLeft)))
	// Output:
	// shape: (5, 4)
	// ┌───────┬─────────────┬─────────┬────────┐
	// │ order │ customer_id │ amount  │ name   │
	// │ Int64 │ Int64       │ Float64 │ String │
	// ├───────┼─────────────┼─────────┼────────┤
	// │ 10    │ 1           │ 25      │ "Ada"  │
	// │ 11    │ 3           │ 40      │ "Cai"  │
	// │ 12    │ 1           │ 10      │ "Ada"  │
	// │ 13    │ 5           │ 99      │ null   │
	// │ 14    │ 4           │ 15      │ "Dee"  │
	// └───────┴─────────────┴─────────┴────────┘
}

func Example_joinRight() {
	on := ursus.JoinOn(ursus.Col("customer_id"))
	show(orders().Join(customers(), on, ursus.JoinHow(ursus.JoinRight)))
	// Output:
	// shape: (5, 4)
	// ┌───────┬─────────────┬─────────┬────────┐
	// │ order │ customer_id │ amount  │ name   │
	// │ Int64 │ Int64       │ Float64 │ String │
	// ├───────┼─────────────┼─────────┼────────┤
	// │ 10    │ 1           │ 25      │ "Ada"  │
	// │ 11    │ 3           │ 40      │ "Cai"  │
	// │ 12    │ 1           │ 10      │ "Ada"  │
	// │ 14    │ 4           │ 15      │ "Dee"  │
	// │ null  │ 2           │ null    │ "Ben"  │
	// └───────┴─────────────┴─────────┴────────┘
}

func Example_joinFull() {
	on := ursus.JoinOn(ursus.Col("customer_id"))
	show(orders().Join(customers(), on, ursus.JoinHow(ursus.JoinFull)))
	// Output:
	// shape: (6, 5)
	// ┌───────┬─────────────┬─────────┬───────────────────┬────────┐
	// │ order │ customer_id │ amount  │ customer_id_right │ name   │
	// │ Int64 │ Int64       │ Float64 │ Int64             │ String │
	// ├───────┼─────────────┼─────────┼───────────────────┼────────┤
	// │ 10    │ 1           │ 25      │ 1                 │ "Ada"  │
	// │ 11    │ 3           │ 40      │ 3                 │ "Cai"  │
	// │ 12    │ 1           │ 10      │ 1                 │ "Ada"  │
	// │ 13    │ 5           │ 99      │ null              │ null   │
	// │ 14    │ 4           │ 15      │ 4                 │ "Dee"  │
	// │ null  │ null        │ null    │ 2                 │ "Ben"  │
	// └───────┴─────────────┴─────────┴───────────────────┴────────┘
}

func Example_joinSemi() {
	on := ursus.JoinOn(ursus.Col("customer_id"))
	show(customers().Join(orders(), on, ursus.JoinHow(ursus.JoinSemi)))
	// Output:
	// shape: (3, 2)
	// ┌─────────────┬────────┐
	// │ customer_id │ name   │
	// │ Int64       │ String │
	// ├─────────────┼────────┤
	// │ 1           │ "Ada"  │
	// │ 3           │ "Cai"  │
	// │ 4           │ "Dee"  │
	// └─────────────┴────────┘
}

func Example_joinAnti() {
	on := ursus.JoinOn(ursus.Col("customer_id"))
	show(customers().Join(orders(), on, ursus.JoinHow(ursus.JoinAnti)))
	// Output:
	// shape: (1, 2)
	// ┌─────────────┬────────┐
	// │ customer_id │ name   │
	// │ Int64       │ String │
	// ├─────────────┼────────┤
	// │ 2           │ "Ben"  │
	// └─────────────┴────────┘
}

func Example_joinCross() {
	sizes := ursus.Frame(ursus.Values("size", []string{"S", "M"}))
	colours := ursus.Frame(ursus.Values("colour", []string{"red", "blue", "green"}))
	show(sizes.Join(colours, ursus.JoinHow(ursus.JoinCross)))
	// Output:
	// shape: (6, 2)
	// ┌────────┬─────────┐
	// │ size   │ colour  │
	// │ String │ String  │
	// ├────────┼─────────┤
	// │ "S"    │ "red"   │
	// │ "S"    │ "blue"  │
	// │ "S"    │ "green" │
	// │ "M"    │ "red"   │
	// │ "M"    │ "blue"  │
	// │ "M"    │ "green" │
	// └────────┴─────────┘
}

func Example_joinCoalesce() {
	on := ursus.JoinOn(ursus.Col("customer_id"))
	show(orders().Join(customers(), on,
		ursus.JoinHow(ursus.JoinFull),
		ursus.JoinCoalesce(true)))
	// Output:
	// shape: (6, 4)
	// ┌───────┬─────────────┬─────────┬────────┐
	// │ order │ customer_id │ amount  │ name   │
	// │ Int64 │ Int64       │ Float64 │ String │
	// ├───────┼─────────────┼─────────┼────────┤
	// │ 10    │ 1           │ 25      │ "Ada"  │
	// │ 11    │ 3           │ 40      │ "Cai"  │
	// │ 12    │ 1           │ 10      │ "Ada"  │
	// │ 13    │ 5           │ 99      │ null   │
	// │ 14    │ 4           │ 15      │ "Dee"  │
	// │ null  │ 2           │ null    │ "Ben"  │
	// └───────┴─────────────┴─────────┴────────┘
}

func Example_joinKeysByName() {
	people := ursus.Frame(
		ursus.Values("id", []int64{1, 3}),
		ursus.Values("name", []string{"Ada", "Cai"}),
	)
	show(orders().
		Join(people,
			ursus.JoinLeftOn(ursus.Col("customer_id")),
			ursus.JoinRightOn(ursus.Col("id"))).
		Sort(ursus.Asc(ursus.Col("order"))))
	// Output:
	// shape: (3, 5)
	// ┌───────┬─────────────┬─────────┬───────┬────────┐
	// │ order │ customer_id │ amount  │ id    │ name   │
	// │ Int64 │ Int64       │ Float64 │ Int64 │ String │
	// ├───────┼─────────────┼─────────┼───────┼────────┤
	// │ 10    │ 1           │ 25      │ 1     │ "Ada"  │
	// │ 11    │ 3           │ 40      │ 3     │ "Cai"  │
	// │ 12    │ 1           │ 10      │ 1     │ "Ada"  │
	// └───────┴─────────────┴─────────┴───────┴────────┘
}

func Example_joinSuffix() {
	shipped := ursus.Frame(
		ursus.Values("order", []int64{10, 11, 12}),
		ursus.Values("amount", []float64{25, 38, 10}),
	)
	show(orders().
		Join(shipped,
			ursus.JoinOn(ursus.Col("order")),
			ursus.JoinSuffix("_shipped"),
			ursus.JoinMaintainOrder(ursus.JoinOrderLeft)).
		Select(ursus.Col("order"), ursus.Col("amount"), ursus.Col("amount_shipped")))
	// Output:
	// shape: (3, 3)
	// ┌───────┬─────────┬────────────────┐
	// │ order │ amount  │ amount_shipped │
	// │ Int64 │ Float64 │ Float64        │
	// ├───────┼─────────┼────────────────┤
	// │ 10    │ 25      │ 25             │
	// │ 11    │ 40      │ 38             │
	// │ 12    │ 10      │ 10             │
	// └───────┴─────────┴────────────────┘
}

func Example_joinValidate() {
	twice := ursus.Frame(
		ursus.Values("customer_id", []int64{1, 1}),
		ursus.Values("name", []string{"Ada", "Ada Lovelace"}),
	)
	_, err := orders().
		Join(twice,
			ursus.JoinOn(ursus.Col("customer_id")),
			ursus.JoinValidate(ursus.ValidateManyToOne)).
		Collect(context.Background())
	fmt.Println(err)
	// Output:
	// ursus: join: validate=m:1 requires unique keys on the right side, but a key is duplicated
	//   first duplicate at right row 1
	//   key: col("customer_id")
	//   each duplicate multiplies every matching row on the other side
	//   de-duplicate with .Unique(...), or pass JoinValidate(ursus.ValidateManyToMany)
}

func Example_joinNulls() {
	key := ursus.ValuesNullable("k", []int64{1, 0}, []bool{true, false}) // 1 and null
	left := ursus.Frame(key, ursus.Values("l", []string{"a", "b"}))
	right := ursus.Frame(key, ursus.Values("r", []string{"x", "y"}))
	on := ursus.JoinOn(ursus.Col("k"))
	ordered := ursus.JoinMaintainOrder(ursus.JoinOrderLeft)
	show(left.Join(right, on, ordered))
	show(left.Join(right, on, ordered, ursus.JoinNullsEqual(true)))
	// Output:
	// shape: (1, 3)
	// ┌───────┬────────┬────────┐
	// │ k     │ l      │ r      │
	// │ Int64 │ String │ String │
	// ├───────┼────────┼────────┤
	// │ 1     │ "a"    │ "x"    │
	// └───────┴────────┴────────┘
	//
	// shape: (2, 3)
	// ┌───────┬────────┬────────┐
	// │ k     │ l      │ r      │
	// │ Int64 │ String │ String │
	// ├───────┼────────┼────────┤
	// │ 1     │ "a"    │ "x"    │
	// │ null  │ "b"    │ "y"    │
	// └───────┴────────┴────────┘
}

func Example_joinAsOf() {
	at := func(s int) time.Time { return time.Date(2026, 1, 5, 9, 0, s, 0, time.UTC) }
	trades := ursus.Frame(
		ursus.Values("time", []time.Time{at(1), at(5), at(9)}),
		ursus.Values("ticker", []string{"ACME", "BOLT", "ACME"}),
		ursus.Values("qty", []int64{100, 50, 70}),
	)
	quotes := ursus.Frame(
		ursus.Values("time", []time.Time{at(0), at(2), at(4), at(6)}),
		ursus.Values("ticker", []string{"ACME", "BOLT", "ACME", "BOLT"}),
		ursus.Values("bid", []float64{10.0, 20.0, 10.1, 20.2}),
	)
	show(trades.JoinAsOf(quotes,
		ursus.AsOfOn(ursus.Col("time")),
		ursus.AsOfBy(ursus.Col("ticker")),
		ursus.AsOfTolerance(ursus.Every("3s")),
	))
	// Output:
	// shape: (3, 4)
	// ┌────────────────────────────────┬────────┬───────┬─────────┐
	// │ time                           │ ticker │ qty   │ bid     │
	// │ Datetime(ns, UTC)              │ String │ Int64 │ Float64 │
	// ├────────────────────────────────┼────────┼───────┼─────────┤
	// │ 2026-01-05T09:00:01.000000000Z │ "ACME" │ 100   │ 10      │
	// │ 2026-01-05T09:00:05.000000000Z │ "BOLT" │ 50    │ 20      │
	// │ 2026-01-05T09:00:09.000000000Z │ "ACME" │ 70    │ null    │
	// └────────────────────────────────┴────────┴───────┴─────────┘
}

func Example_joinWhere() {
	promotions := ursus.Frame(
		ursus.Values("promotion", []string{"new year", "mid week"}),
		ursus.Values("from", []string{"2026-01-01", "2026-01-06"}),
		ursus.Values("to", []string{"2026-01-04", "2026-01-07"}),
	).WithColumns(ursus.Col("from").Str().ToDate(), ursus.Col("to").Str().ToDate())
	show(sales().
		Select(ursus.Col("order_id"), ursus.Col("date")).
		JoinWhere(promotions,
			ursus.Col("date").Ge(ursus.Col("from")),
			ursus.Col("date").Le(ursus.Col("to"))).
		Select(ursus.Col("order_id"), ursus.Col("date"), ursus.Col("promotion")).
		Sort(ursus.Asc(ursus.Col("order_id"))))
	// Output:
	// shape: (6, 3)
	// ┌──────────┬────────────┬────────────┐
	// │ order_id │ date       │ promotion  │
	// │ Int64    │ Date       │ String     │
	// ├──────────┼────────────┼────────────┤
	// │ 1        │ 2026-01-03 │ "new year" │
	// │ 2        │ 2026-01-03 │ "new year" │
	// │ 3        │ 2026-01-04 │ "new year" │
	// │ 6        │ 2026-01-06 │ "mid week" │
	// │ 7        │ 2026-01-07 │ "mid week" │
	// │ 8        │ 2026-01-07 │ "mid week" │
	// └──────────┴────────────┴────────────┘
}

func Example_whereExists() {
	show(customers().WhereExists(orders(),
		ursus.Col("customer_id").Eq(ursus.Col("customer_id_right")),
		ursus.Col("amount").Gt(20.0)))
	// Output:
	// shape: (2, 2)
	// ┌─────────────┬────────┐
	// │ customer_id │ name   │
	// │ Int64       │ String │
	// ├─────────────┼────────┤
	// │ 1           │ "Ada"  │
	// │ 3           │ "Cai"  │
	// └─────────────┴────────┘
}
