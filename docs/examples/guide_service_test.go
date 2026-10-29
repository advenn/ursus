package examples_test

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/advenn/ursus"
)

// --- the user guide: ursus in a service ------------------------------------------------

func Example_serviceHandler() {
	// Built once, as the service starts, and shared by every request: a LazyFrame is
	// never changed, so any number of goroutines may run it at once.
	totals := sales().
		GroupBy(ursus.Col("region"), ursus.Col("product")).
		Agg(ursus.Col("qty").Sum().Alias("qty")).
		Sort(ursus.Asc(ursus.Col("region")), ursus.Asc(ursus.Col("product")))

	handler := func(w http.ResponseWriter, r *http.Request) {
		q := totals.Filter(ursus.Col("region").Eq(r.URL.Query().Get("region")))
		w.Header().Set("Content-Type", "text/csv")
		// The request's context: a client that goes away stops the query.
		if err := q.WriteCSV(r.Context(), w); err != nil {
			log.Print(err)
		}
	}

	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest("GET", "/totals?region=north", nil))
	fmt.Println(rec.Code, rec.Header().Get("Content-Type"))
	fmt.Print(rec.Body.String())
	// Output:
	// 200 text/csv
	// region,product,qty
	// north,apple,10
	// north,fig,3
	// north,pear,7
}

func Example_serviceBatches() {
	ctx := context.Background()
	q := sales().Select(ursus.Col("order_id"), ursus.Col("qty"))
	// A batch at a time, never the whole answer at once: each could be encoded and
	// flushed to the client as it comes.
	for batch, err := range q.CollectBatches(ctx, ursus.WithBatchSize(3)) {
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(batch.Height(), "rows")
	}
	// Output:
	// 3 rows
	// 3 rows
	// 2 rows
}

// statusOf is the HTTP status a query's error answers with.
func statusOf(err error) int {
	if errors.Is(err, context.DeadlineExceeded) {
		return http.StatusGatewayTimeout
	}
	var e *ursus.Error
	if !errors.As(err, &e) {
		return http.StatusInternalServerError
	}
	switch e.Kind {
	case ursus.KindSchema, ursus.KindType, ursus.KindValue:
		// The request named a column, or a value, wrongly.
		return http.StatusBadRequest
	case ursus.KindResource:
		// Over the memory budget: try again later.
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}

func Example_serviceBadColumn() {
	// A column a request names, misspelt.
	_, err := sales().Select(ursus.Col("qtty")).Collect(context.Background())
	var e *ursus.Error
	if errors.As(err, &e) {
		fmt.Println(statusOf(err), e.Kind, e.Op)
		fmt.Println(e.Msg)
		fmt.Println(strings.Join(e.Hints, "\n"))
	}
	// Output:
	// 400 schema select
	// unknown column "qtty"
	// did you mean: "qty"?
	// available: order_id, date, region, product, qty, price
}

func Example_serviceDeadline() {
	// A request whose time is up before its query finishes.
	ctx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	_, err := sales().Collect(ctx)
	fmt.Println(errors.Is(err, context.DeadlineExceeded), statusOf(err))
	// Output:
	// true 504
}

type routeKey struct{}

func Example_serviceEngine() {
	// One engine for the requests: its budget, its threads, and a hook that hears of
	// every query as it ends, for the service's metrics.
	requests := ursus.NewEngine(
		ursus.WithEngineMemoryLimit(1<<30),
		ursus.WithEngineThreads(4),
		ursus.WithEngineQueryHook(func(ctx context.Context, s ursus.QueryStats) {
			fmt.Printf("%v %s: %d rows, %d spills, error %v\n",
				ctx.Value(routeKey{}), s.Op, s.Rows, s.Spills, s.Err)
		}))

	ctx := context.WithValue(context.Background(), routeKey{}, "/top")
	top := sales().Sort(ursus.Desc(ursus.Col("qty"))).Head(3)
	if _, err := top.Collect(ctx, ursus.WithEngine(requests)); err != nil {
		log.Fatal(err)
	}
	s := requests.Stats()
	fmt.Printf("%d running, %d held, %d threads\n", s.Running, s.Held, s.Threads)
	// Output:
	// /top collect: 3 rows, 0 spills, error <nil>
	// 0 running, 0 held, 4 threads
}

// data is what the service carries in its binary.
//
//go:embed testdata/sales.csv
var data embed.FS

func Example_serviceEmbed() {
	show(ursus.ScanCSVFS(data, "testdata/sales.csv").
		GroupBy(ursus.Col("product")).
		Agg(ursus.Col("qty").Sum()).
		Sort(ursus.Asc(ursus.Col("product"))))
	// Output:
	// shape: (3, 2)
	// ┌─────────┬────────┐
	// │ product │ qty    │
	// │ String  │ Int128 │
	// ├─────────┼────────┤
	// │ "apple" │ 26     │
	// │ "fig"   │ 12     │
	// │ "pear"  │ 15     │
	// └─────────┴────────┘
}
