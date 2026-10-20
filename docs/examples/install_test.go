package examples_test

import (
	"context"
	"fmt"
	"log"

	"github.com/advenn/ursus"
)

// The installation page's first program, shown whole.
func Example_install() {
	df, err := ursus.Frame(
		ursus.Values("name", []string{"ursus", "polars", "pandas"}),
		ursus.Values("language", []string{"Go", "Rust", "Python"}),
	).Filter(ursus.Col("language").Eq("Go")).Collect(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(df)
	// Output:
	// shape: (1, 2)
	// ┌─────────┬──────────┐
	// │ name    │ language │
	// │ String  │ String   │
	// ├─────────┼──────────┤
	// │ "ursus" │ "Go"     │
	// └─────────┴──────────┘
}
