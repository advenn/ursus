package examples_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync/atomic"

	"github.com/advenn/ursus"
)

// --- the user guide: Go functions ------------------------------------------------------

func Example_udfElements() {
	slug := ursus.Col("product").MapElements("slug", ursus.String,
		func(s string) (string, error) {
			return strings.ToUpper(s[:1]) + "-" + fmt.Sprint(len(s)), nil
		})
	show(sales().Head(3).Select(ursus.Col("product"), slug.Alias("slug")))
	// Output:
	// shape: (3, 2)
	// ┌─────────┬────────┐
	// │ product │ slug   │
	// │ String  │ String │
	// ├─────────┼────────┤
	// │ "apple" │ "A-5"  │
	// │ "apple" │ "A-5"  │
	// │ "pear"  │ "P-4"  │
	// └─────────┴────────┘
}

func Example_udfTypes() {
	band := ursus.Col("qty").MapElements("band", ursus.String,
		func(q int64) (string, error) {
			switch {
			case q >= 10:
				return "large", nil
			case q >= 5:
				return "medium", nil
			}
			return "small", nil
		})
	show(sales().
		GroupBy(band.Alias("band")).
		Agg(ursus.Len().Alias("orders")).
		Sort(ursus.Asc(ursus.Col("band"))))
	// Output:
	// shape: (3, 2)
	// ┌──────────┬────────┐
	// │ band     │ orders │
	// │ String   │ Uint64 │
	// ├──────────┼────────┤
	// │ "large"  │ 2      │
	// │ "medium" │ 3      │
	// │ "small"  │ 3      │
	// └──────────┴────────┘
}

func Example_udfNulls() {
	var calls atomic.Int64
	double := ursus.Col("temp").MapElements("double", ursus.Float64,
		func(t float64) (float64, error) {
			calls.Add(1)
			return 2 * t, nil
		})
	show(readings().Select(ursus.Col("temp"), double.Alias("doubled")))
	fmt.Println("calls:", calls.Load())
	// Output:
	// shape: (6, 2)
	// ┌─────────┬─────────┐
	// │ temp    │ doubled │
	// │ Float64 │ Float64 │
	// ├─────────┼─────────┤
	// │ 10      │ 20      │
	// │ null    │ null    │
	// │ null    │ null    │
	// │ 13      │ 26      │
	// │ 12      │ 24      │
	// │ null    │ null    │
	// └─────────┴─────────┘
	//
	// calls: 3
}

func Example_udfBatches() {
	orZero := ursus.Col("temp").MapBatches("or_zero", ursus.Float64,
		func(vals []float64, valid []bool) ([]float64, []bool, error) {
			out, ok := make([]float64, len(vals)), make([]bool, len(vals))
			for i := range vals {
				if valid[i] {
					out[i] = vals[i]
				}
				ok[i] = true // a null becomes 0, a value of its own
			}
			return out, ok, nil
		})
	show(readings().Select(ursus.Col("temp"), orZero.Alias("or_zero")))
	// Output:
	// shape: (6, 2)
	// ┌─────────┬─────────┐
	// │ temp    │ or_zero │
	// │ Float64 │ Float64 │
	// ├─────────┼─────────┤
	// │ 10      │ 10      │
	// │ null    │ 0       │
	// │ null    │ 0       │
	// │ 13      │ 13      │
	// │ 12      │ 12      │
	// │ null    │ 0       │
	// └─────────┴─────────┘
}

func Example_udfError() {
	dozens := ursus.Col("qty").MapElements("dozens", ursus.Float64,
		func(q int64) (float64, error) {
			if q > 10 {
				return 0, errors.New("more than ten is a wholesale order")
			}
			return float64(q) / 12, nil
		})
	_, err := sales().Select(dozens).Collect(context.Background())
	fmt.Println(err)
	// Output:
	// ursus: map_elements: udf "dozens" failed on 12, row 5 of its batch of column "qty"
	//   caused by: more than ten is a wholesale order
}

func Example_udfExplain() {
	slug := ursus.Col("product").MapElements("slug", ursus.String,
		func(s string) (string, error) { return strings.ToUpper(s), nil })
	plan, err := sales().
		Filter(ursus.Col("qty").Gt(5)).
		Select(slug).
		Explain(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(plan)
	// Output:
	// PROJECT [col("product").map_elements("slug" -> String)]
	//   FILTER [(col("qty") > lit(5))]
	//     CSV SCAN testdata/sales.csv
	//       projection: [product, qty] (2/6 cols)
}
