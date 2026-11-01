package ursus_test

// Step 421: a cancelled query stops calling rolling_map's Go function, where the
// function ran under context.Background() and was called for every window.

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/advenn/ursus"
)

// TestACancelledRollingMapStops: over 200,000 rows, a rolling_map whose tenth call
// cancels its query is called a few times more, at most a thousand, and the query
// gives context.Canceled, where the function was called for all 200,000 windows.
func TestACancelledRollingMapStops(t *testing.T) {
	const rows = 200_000
	vals := make([]float64, rows)
	for i := range vals {
		vals[i] = float64(i)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var calls atomic.Int64
	fn := func(w []float64, ok []bool) (float64, error) {
		if calls.Add(1) == 10 {
			cancel()
		}
		return w[len(w)-1], nil
	}
	_, err := ursus.Frame(ursus.Values("v", vals)).
		Select(ursus.Col("v").RollingMap[float64, float64]("last", ursus.Float64, 3, fn)).
		Collect(ctx, ursus.WithThreads(1))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want context.Canceled", err)
	}
	if n := calls.Load(); n > 1000 {
		t.Errorf("the function was called %d times after the query was cancelled at 10", n)
	}
}
