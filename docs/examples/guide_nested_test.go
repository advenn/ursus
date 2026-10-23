package examples_test

import (
	"github.com/advenn/ursus"
)

// baskets is the lists chapter's frame: each order's items and their prices, a list a
// row.
func baskets() *ursus.LazyFrame {
	return ursus.Frame(
		ursus.Values("order", []int64{1, 2, 3}),
		ursus.Values("items", []string{"pear,apple,fig", "fig", "apple,pear"}),
		ursus.Values("prices", []string{"2.25,1.5,3", "3", "1.5,2.25"}),
	).WithColumns(
		ursus.Col("items").Str().Split(","),
		ursus.Col("prices").Str().Split(",").
			List().Eval(ursus.Element().Cast(ursus.Float64)),
	)
}

// --- the user guide: lists and structs ------------------------------------------------

func Example_listBuild() {
	show(baskets())
	show(sales().
		GroupBy(ursus.Col("region")).
		Agg(ursus.Col("qty").Implode().Alias("qtys")).
		Sort(ursus.Asc(ursus.Col("region"))))
	// Output:
	// shape: (3, 3)
	// ┌───────┬──────────────────────────┬────────────────┐
	// │ order │ items                    │ prices         │
	// │ Int64 │ List(String)             │ List(Float64)  │
	// ├───────┼──────────────────────────┼────────────────┤
	// │ 1     │ ["pear", "apple", "fig"] │ [2.25, 1.5, 3] │
	// │ 2     │ ["fig"]                  │ [3]            │
	// │ 3     │ ["apple", "pear"]        │ [1.5, 2.25]    │
	// └───────┴──────────────────────────┴────────────────┘
	//
	// shape: (3, 2)
	// ┌─────────┬─────────────┐
	// │ region  │ qtys        │
	// │ String  │ List(Int64) │
	// ├─────────┼─────────────┤
	// │ "east"  │ [2, 12]     │
	// │ "north" │ [10, 7, 3]  │
	// │ "south" │ [4, 9, 6]   │
	// └─────────┴─────────────┘
}

func Example_listQuestions() {
	items, prices := ursus.Col("items").List(), ursus.Col("prices").List()
	show(baskets().Select(
		ursus.Col("order"),
		items.Len().Alias("n"),
		items.Get(0).Alias("first"),
		items.Contains("fig").Alias("has_fig"),
		prices.Sum().Alias("total"),
		prices.Max().Alias("dearest"),
	))
	// Output:
	// shape: (3, 6)
	// ┌───────┬────────┬─────────┬─────────┬─────────┬─────────┐
	// │ order │ n      │ first   │ has_fig │ total   │ dearest │
	// │ Int64 │ Uint32 │ String  │ Bool    │ Float64 │ Float64 │
	// ├───────┼────────┼─────────┼─────────┼─────────┼─────────┤
	// │ 1     │ 3      │ "pear"  │ true    │ 6.75    │ 3       │
	// │ 2     │ 1      │ "fig"   │ true    │ 3       │ 3       │
	// │ 3     │ 2      │ "apple" │ false   │ 3.75    │ 2.25    │
	// └───────┴────────┴─────────┴─────────┴─────────┴─────────┘
}

func Example_listTransform() {
	items := ursus.Col("items").List()
	show(baskets().Select(
		ursus.Col("order"),
		items.Sort().Alias("sorted"),
		items.Join(" + ").Alias("joined"),
		ursus.Col("prices").List().Eval(ursus.Element().Mul(2)).Alias("doubled"),
	))
	// Output:
	// shape: (3, 4)
	// ┌───────┬──────────────────────────┬──────────────────────┬───────────────┐
	// │ order │ sorted                   │ joined               │ doubled       │
	// │ Int64 │ List(String)             │ String               │ List(Float64) │
	// ├───────┼──────────────────────────┼──────────────────────┼───────────────┤
	// │ 1     │ ["apple", "fig", "pear"] │ "pear + apple + fig" │ [4.5, 3, 6]   │
	// │ 2     │ ["fig"]                  │ "fig"                │ [6]           │
	// │ 3     │ ["apple", "pear"]        │ "apple + pear"       │ [3, 4.5]      │
	// └───────┴──────────────────────────┴──────────────────────┴───────────────┘
}

func Example_listFilterEval() {
	prices := ursus.Col("prices").List()
	show(baskets().Select(
		ursus.Col("order"),
		prices.Filter(ursus.Element().Ge(2.0)).Alias("from_2"),
		prices.Eval(ursus.Element().Rank(ursus.RankDense, true)).Alias("rank"),
	))
	// Output:
	// shape: (3, 3)
	// ┌───────┬───────────────┬──────────────┐
	// │ order │ from_2        │ rank         │
	// │ Int64 │ List(Float64) │ List(Uint32) │
	// ├───────┼───────────────┼──────────────┤
	// │ 1     │ [2.25, 3]     │ [2, 3, 1]    │
	// │ 2     │ [3]           │ [1]          │
	// │ 3     │ [2.25]        │ [2, 1]       │
	// └───────┴───────────────┴──────────────┘
}

func Example_listSets() {
	items := ursus.Col("items")
	fruit := ursus.Lit([]string{"apple", "pear"})
	show(baskets().Select(
		ursus.Col("order"),
		items.List().SetIntersection(fruit).Alias("in_both"),
		items.List().SetDifference(fruit).Alias("only_here"),
	))
	// Output:
	// shape: (3, 3)
	// ┌───────┬───────────────────┬──────────────┐
	// │ order │ in_both           │ only_here    │
	// │ Int64 │ List(String)      │ List(String) │
	// ├───────┼───────────────────┼──────────────┤
	// │ 1     │ ["pear", "apple"] │ ["fig"]      │
	// │ 2     │ []                │ ["fig"]      │
	// │ 3     │ ["apple", "pear"] │ []           │
	// └───────┴───────────────────┴──────────────┘
}

func Example_listExplode() {
	show(baskets().Explode("items", "prices"))
	// Output:
	// shape: (6, 3)
	// ┌───────┬─────────┬─────────┐
	// │ order │ items   │ prices  │
	// │ Int64 │ String  │ Float64 │
	// ├───────┼─────────┼─────────┤
	// │ 1     │ "pear"  │ 2.25    │
	// │ 1     │ "apple" │ 1.5     │
	// │ 1     │ "fig"   │ 3       │
	// │ 2     │ "fig"   │ 3       │
	// │ 3     │ "apple" │ 1.5     │
	// │ 3     │ "pear"  │ 2.25    │
	// └───────┴─────────┴─────────┘
}

// point is a row of Example_arrays: a name and a position, three coordinates.
type point struct {
	Name string
	XYZ  [3]float64
}

func Example_arrays() {
	points := ursus.FromRows([]point{
		{"a", [3]float64{1, 2, 2}},
		{"b", [3]float64{0, 3, 4}},
	})
	show(points.WithColumns(
		ursus.Col("XYZ").Arr().ToList().List().Sum().Alias("sum"),
		ursus.Col("XYZ").Arr().ToList().List().Head(2).List().ToArray(2).Alias("xy"),
	))
	// Output:
	// shape: (2, 4)
	// ┌────────┬───────────────────┬─────────┬───────────────────┐
	// │ Name   │ XYZ               │ sum     │ xy                │
	// │ String │ Array(Float64, 3) │ Float64 │ Array(Float64, 2) │
	// ├────────┼───────────────────┼─────────┼───────────────────┤
	// │ "a"    │ [1, 2, 2]         │ 5       │ [1, 2]            │
	// │ "b"    │ [0, 3, 4]         │ 7       │ [0, 3]            │
	// └────────┴───────────────────┴─────────┴───────────────────┘
}

func Example_structBuild() {
	show(sales().Head(3).Select(
		ursus.Col("order_id"),
		ursus.Struct(ursus.Col("product"), ursus.Col("qty")).Alias("line"),
	))
	// Output:
	// shape: (3, 2)
	// ┌──────────┬─────────────────────────────────────┐
	// │ order_id │ line                                │
	// │ Int64    │ Struct(product: String, qty: Int64) │
	// ├──────────┼─────────────────────────────────────┤
	// │ 1        │ {product: "apple", qty: 10}         │
	// │ 2        │ {product: "apple", qty: 4}          │
	// │ 3        │ {product: "pear", qty: 7}           │
	// └──────────┴─────────────────────────────────────┘
}

func Example_structFields() {
	line := ursus.Struct(ursus.Col("product"), ursus.Col("qty")).Alias("line")
	l := ursus.Col("line").Struct()
	lines := sales().Head(2).Select(line)
	show(lines.Select(
		l.Field("product").Alias("product"),
		l.WithFields(l.Field("qty").Mul(10).Alias("qty")).Alias("scaled"),
	))
	show(lines.Select(l.JsonEncode().Alias("json")))
	// Output:
	// shape: (2, 2)
	// ┌─────────┬─────────────────────────────────────┐
	// │ product │ scaled                              │
	// │ String  │ Struct(product: String, qty: Int64) │
	// ├─────────┼─────────────────────────────────────┤
	// │ "apple" │ {product: "apple", qty: 100}        │
	// │ "apple" │ {product: "apple", qty: 40}         │
	// └─────────┴─────────────────────────────────────┘
	//
	// shape: (2, 1)
	// ┌──────────────────────────────────────┐
	// │ json                                 │
	// │ String                               │
	// ├──────────────────────────────────────┤
	// │ "{\"product\":\"apple\",\"qty\":10}" │
	// │ "{\"product\":\"apple\",\"qty\":4}"  │
	// └──────────────────────────────────────┘
}
