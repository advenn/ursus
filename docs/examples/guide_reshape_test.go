package examples_test

import (
	"context"
	"fmt"
	"log"

	"github.com/advenn/ursus"
)

// --- the user guide: reshaping -------------------------------------------------------

func Example_reshapePivot() {
	ctx := context.Background()
	df, err := sales().Collect(ctx)
	if err != nil {
		log.Fatal(err)
	}
	wide, err := df.Pivot(ctx, ursus.PivotOptions{
		On:          []string{"product"},
		Index:       []string{"region"},
		Values:      []string{"qty"},
		Agg:         ursus.Expr.Sum,
		SortColumns: true,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(wide)
	// Output:
	// shape: (3, 4)
	// ┌─────────┬────────┬────────┬────────┐
	// │ region  │ apple  │ fig    │ pear   │
	// │ String  │ Int128 │ Int128 │ Int128 │
	// ├─────────┼────────┼────────┼────────┤
	// │ "north" │ 10     │ 3      │ 7      │
	// │ "south" │ 4      │ 9      │ 6      │
	// │ "east"  │ 12     │ null   │ 2      │
	// └─────────┴────────┴────────┴────────┘
}

func Example_reshapePivotLazy() {
	products := ursus.Values("product", []string{"apple", "pear", "plum"})
	wide := sales().Pivot(ursus.PivotOptions{
		On:        []string{"product"},
		OnColumns: []*ursus.Column{products},
		Index:     []string{"region"},
		Values:    []string{"qty"},
		Agg:       ursus.Expr.Sum,
	})
	show(wide)
	// Output:
	// shape: (3, 4)
	// ┌─────────┬────────┬────────┬────────┐
	// │ region  │ apple  │ pear   │ plum   │
	// │ String  │ Int128 │ Int128 │ Int128 │
	// ├─────────┼────────┼────────┼────────┤
	// │ "north" │ 10     │ 7      │ null   │
	// │ "south" │ 4      │ 6      │ null   │
	// │ "east"  │ 12     │ 2      │ null   │
	// └─────────┴────────┴────────┴────────┘
}

func Example_reshapePivotSeveral() {
	ctx := context.Background()
	df, err := ursus.Frame(
		ursus.Values("shop", []string{"a", "a", "b", "b"}),
		ursus.Values("quarter", []string{"q1", "q2", "q1", "q1"}),
		ursus.Values("channel", []string{"web", "web", "store", "web"}),
		ursus.Values("sales", []int64{10, 12, 7, 3}),
	).Collect(ctx)
	if err != nil {
		log.Fatal(err)
	}
	wide, err := df.Pivot(ctx, ursus.PivotOptions{
		On:     []string{"quarter", "channel"},
		Index:  []string{"shop"},
		Values: []string{"sales"},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(wide)
	// Output:
	// shape: (2, 4)
	// ┌────────┬──────────────┬──────────────┬────────────────┐
	// │ shop   │ {"q1","web"} │ {"q2","web"} │ {"q1","store"} │
	// │ String │ Int64        │ Int64        │ Int64          │
	// ├────────┼──────────────┼──────────────┼────────────────┤
	// │ "a"    │ 10           │ 12           │ null           │
	// │ "b"    │ 3            │ null         │ 7              │
	// └────────┴──────────────┴──────────────┴────────────────┘
}

func Example_reshapeUnpivot() {
	quarters := ursus.Frame(
		ursus.Values("region", []string{"north", "south"}),
		ursus.Values("q1", []int64{120, 80}),
		ursus.Values("q2", []int64{135, 95}),
	)
	show(quarters.Unpivot(ursus.UnpivotOptions{
		On:           []string{"q1", "q2"},
		Index:        []string{"region"},
		VariableName: "quarter",
		ValueName:    "sales",
	}))
	// Output:
	// shape: (4, 3)
	// ┌─────────┬─────────┬───────┐
	// │ region  │ quarter │ sales │
	// │ String  │ String  │ Int64 │
	// ├─────────┼─────────┼───────┤
	// │ "north" │ "q1"    │ 120   │
	// │ "north" │ "q2"    │ 135   │
	// │ "south" │ "q1"    │ 80    │
	// │ "south" │ "q2"    │ 95    │
	// └─────────┴─────────┴───────┘
}

func Example_reshapeExplode() {
	posts := ursus.Frame(
		ursus.Values("post", []int64{1, 2, 3}),
		ursus.Values("tags", []string{"go,data", "go", ""}),
	).WithColumns(ursus.Col("tags").Str().Split(","))
	show(posts)
	show(posts.Explode("tags"))
	// Output:
	// shape: (3, 2)
	// ┌───────┬────────────────┐
	// │ post  │ tags           │
	// │ Int64 │ List(String)   │
	// ├───────┼────────────────┤
	// │ 1     │ ["go", "data"] │
	// │ 2     │ ["go"]         │
	// │ 3     │ [""]           │
	// └───────┴────────────────┘
	//
	// shape: (4, 2)
	// ┌───────┬────────┐
	// │ post  │ tags   │
	// │ Int64 │ String │
	// ├───────┼────────┤
	// │ 1     │ "go"   │
	// │ 1     │ "data" │
	// │ 2     │ "go"   │
	// │ 3     │ ""     │
	// └───────┴────────┘
}

func Example_reshapeUnnest() {
	points := ursus.Frame(
		ursus.Values("id", []int64{1, 2}),
		ursus.Values("x", []float64{0.5, 2}),
		ursus.Values("y", []float64{1, -1}),
	).Select(
		ursus.Col("id"),
		ursus.Struct(ursus.Col("x"), ursus.Col("y")).Alias("point"),
	)
	show(points)
	show(points.Unnest("point"))
	// Output:
	// shape: (2, 2)
	// ┌───────┬────────────────────────────────┐
	// │ id    │ point                          │
	// │ Int64 │ Struct(x: Float64, y: Float64) │
	// ├───────┼────────────────────────────────┤
	// │ 1     │ {x: 0.5, y: 1}                 │
	// │ 2     │ {x: 2, y: -1}                  │
	// └───────┴────────────────────────────────┘
	//
	// shape: (2, 3)
	// ┌───────┬─────────┬─────────┐
	// │ id    │ x       │ y       │
	// │ Int64 │ Float64 │ Float64 │
	// ├───────┼─────────┼─────────┤
	// │ 1     │ 0.5     │ 1       │
	// │ 2     │ 2       │ -1      │
	// └───────┴─────────┴─────────┘
}

func Example_reshapeConcat() {
	january := ursus.Frame(
		ursus.Values("day", []int64{30, 31}),
		ursus.Values("qty", []int64{4, 6}),
	)
	february := ursus.Frame(
		ursus.Values("day", []int64{1, 2}),
		ursus.Values("qty", []int64{3, 8}),
	)
	show(ursus.Concat([]*ursus.LazyFrame{january, february}))
	// Output:
	// shape: (4, 2)
	// ┌───────┬───────┐
	// │ day   │ qty   │
	// │ Int64 │ Int64 │
	// ├───────┼───────┤
	// │ 30    │ 4     │
	// │ 31    │ 6     │
	// │ 1     │ 3     │
	// │ 2     │ 8     │
	// └───────┴───────┘
}

func Example_reshapeConcatDiagonal() {
	old := ursus.Frame(
		ursus.Values("id", []int64{1, 2}),
		ursus.Values("name", []string{"Ada", "Ben"}),
	)
	recent := ursus.Frame(
		ursus.Values("id", []int64{3}),
		ursus.Values("email", []string{"cai@example.com"}),
	)
	show(ursus.Concat([]*ursus.LazyFrame{old, recent},
		ursus.WithConcatMode(ursus.ConcatDiagonal)))
	// Output:
	// shape: (3, 3)
	// ┌───────┬────────┬───────────────────┐
	// │ id    │ name   │ email             │
	// │ Int64 │ String │ String            │
	// ├───────┼────────┼───────────────────┤
	// │ 1     │ "Ada"  │ null              │
	// │ 2     │ "Ben"  │ null              │
	// │ 3     │ null   │ "cai@example.com" │
	// └───────┴────────┴───────────────────┘
}

func Example_reshapeHStack() {
	names := ursus.Frame(ursus.Values("name", []string{"Ada", "Ben"}))
	scores := ursus.Frame(ursus.Values("score", []int64{90, 75}))
	show(names.HStack(scores))
	// Output:
	// shape: (2, 2)
	// ┌────────┬───────┐
	// │ name   │ score │
	// │ String │ Int64 │
	// ├────────┼───────┤
	// │ "Ada"  │ 90    │
	// │ "Ben"  │ 75    │
	// └────────┴───────┘
}

func Example_reshapeTranspose() {
	ctx := context.Background()
	df, err := ursus.Frame(
		ursus.Values("metric", []string{"orders", "revenue"}),
		ursus.Values("north", []float64{3, 39.75}),
		ursus.Values("south", []float64{3, 46.5}),
	).Collect(ctx)
	if err != nil {
		log.Fatal(err)
	}
	t, err := df.Drop(ctx, "metric")
	if err != nil {
		log.Fatal(err)
	}
	t, err = t.Transpose(ctx, ursus.TransposeOptions{
		IncludeHeader: true,
		HeaderName:    "region",
		ColumnNames:   []string{"orders", "revenue"},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(t)
	// Output:
	// shape: (2, 3)
	// ┌─────────┬─────────┬─────────┐
	// │ region  │ orders  │ revenue │
	// │ String  │ Float64 │ Float64 │
	// ├─────────┼─────────┼─────────┤
	// │ "north" │ 3       │ 39.75   │
	// │ "south" │ 3       │ 46.5    │
	// └─────────┴─────────┴─────────┘
}
