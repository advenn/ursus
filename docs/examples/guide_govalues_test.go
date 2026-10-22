package examples_test

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/advenn/ursus"
)

// --- the user guide: Go values in and out ---------------------------------------------

func Example_goIn() {
	type order struct {
		ID     int64
		Placed time.Time
		Total  float64
		Note   *string // nil: no note
	}
	note := "gift wrap"
	in := []order{
		{1, time.Date(2026, 1, 3, 9, 0, 0, 0, time.UTC), 15, nil},
		{2, time.Date(2026, 1, 4, 14, 30, 0, 0, time.UTC), 27, &note},
	}

	ids, totals := make([]int64, len(in)), make([]float64, len(in))
	placed := make([]time.Time, len(in))
	notes, hasNote := make([]string, len(in)), make([]bool, len(in))
	for i, o := range in {
		ids[i], placed[i], totals[i] = o.ID, o.Placed, o.Total
		if o.Note != nil {
			notes[i], hasNote[i] = *o.Note, true
		}
	}
	show(ursus.Frame(
		ursus.Values("id", ids),
		ursus.Values("placed", placed),
		ursus.Values("total", totals),
		ursus.ValuesNullable("note", notes, hasNote),
	))
	// Output:
	// shape: (2, 4)
	// ┌───────┬────────────────────────────────┬─────────┬─────────────┐
	// │ id    │ placed                         │ total   │ note        │
	// │ Int64 │ Datetime(ns, UTC)              │ Float64 │ String      │
	// ├───────┼────────────────────────────────┼─────────┼─────────────┤
	// │ 1     │ 2026-01-03T09:00:00.000000000Z │ 15      │ null        │
	// │ 2     │ 2026-01-04T14:30:00.000000000Z │ 27      │ "gift wrap" │
	// └───────┴────────────────────────────────┴─────────┴─────────────┘
}

func Example_goFromRows() {
	type order struct {
		ID     int64     `ursus:"id"`
		Placed time.Time `ursus:"placed"`
		Total  float64   `ursus:"total"`
		Note   *string   `ursus:"note"` // nil: no note
	}
	note := "gift wrap"
	show(ursus.FromRows([]order{
		{1, time.Date(2026, 1, 3, 9, 0, 0, 0, time.UTC), 15, nil},
		{2, time.Date(2026, 1, 4, 14, 30, 0, 0, time.UTC), 27, &note},
	}))
	// Output:
	// shape: (2, 4)
	// ┌───────┬────────────────────────────────┬─────────┬─────────────┐
	// │ id    │ placed                         │ total   │ note        │
	// │ Int64 │ Datetime(ns, UTC)              │ Float64 │ String      │
	// ├───────┼────────────────────────────────┼─────────┼─────────────┤
	// │ 1     │ 2026-01-03T09:00:00.000000000Z │ 15      │ null        │
	// │ 2     │ 2026-01-04T14:30:00.000000000Z │ 27      │ "gift wrap" │
	// └───────┴────────────────────────────────┴─────────┴─────────────┘
}

func Example_goColumn() {
	df, err := sales().Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	qty, err := df.Column[int64]("qty")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(qty.Name(), qty.Len(), qty.NullCount())
	fmt.Println(qty.Values())

	total := qty.Fold(int64(0), func(acc, v int64) int64 { return acc + v })
	fmt.Println("total:", total)

	kinds := qty.Map[string](func(v int64) string {
		if v >= 8 {
			return "bulk"
		}
		return "single"
	})
	fmt.Println(kinds)
	// Output:
	// qty 8 0
	// [10 4 7 2 9 12 3 6]
	// total: 53
	// [bulk single single single bulk bulk single single]
}

func Example_goNulls() {
	df, err := ursus.Frame(
		ursus.ValuesNullable("score", []int64{90, 0, 75}, []bool{true, false, true}),
	).Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	score, err := df.Column[int64]("score")
	if err != nil {
		log.Fatal(err)
	}
	for v, present := range score.All() {
		fmt.Println(v, present)
	}
	v, ok := score.Get(1)
	fmt.Println("row 1:", v, ok)
	// Output:
	// 90 true
	// 0 false
	// 75 true
	// row 1: 0 false
}

func Example_goRows() {
	type sale struct {
		ID      int64     `ursus:"order_id"`
		Day     time.Time `ursus:"date"`
		Product string
		Qty     int64
		Revenue float64 `ursus:"-"` // filled by the program, not read
	}
	df, err := sales().
		Filter(ursus.Col("region").Eq("east")).
		Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	rows, err := df.Rows[sale]()
	if err != nil {
		log.Fatal(err)
	}
	for _, r := range rows {
		fmt.Println(r.ID, r.Day.Format("Jan 2"), r.Product, r.Qty)
	}
	// Output:
	// 4 Jan 5 pear 2
	// 6 Jan 6 apple 12
}

func Example_goRowsNulls() {
	type reading struct {
		Hour int64
		Temp *float64 // nil where the reading is null
	}
	df, err := readings().Head(3).Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	rows, err := df.Rows[reading]()
	if err != nil {
		log.Fatal(err)
	}
	for _, r := range rows {
		if r.Temp == nil {
			fmt.Println(r.Hour, "no reading")
			continue
		}
		fmt.Println(r.Hour, *r.Temp)
	}
	// Output:
	// 0 10
	// 1 no reading
	// 2 no reading
}

func Example_goBatches() {
	type sale struct {
		ID  int64 `ursus:"order_id"`
		Qty int64
	}
	ctx := context.Background()
	total := int64(0)
	for batch, err := range sales().CollectBatches(ctx, ursus.WithBatchSize(3)) {
		if err != nil {
			log.Fatal(err)
		}
		rows, err := batch.Rows[sale]()
		if err != nil {
			log.Fatal(err)
		}
		for _, r := range rows {
			total += r.Qty
		}
		fmt.Println("batch of", len(rows), "- running total", total)
	}
	// Output:
	// batch of 3 - running total 21
	// batch of 3 - running total 44
	// batch of 2 - running total 53
}
