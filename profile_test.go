package ursus_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

// profLine is one row of a profile.
type profLine struct {
	node                 string
	depth                int
	ran                  bool
	start, end, own      uint64
	rows, batches, bytes int64
}

func profLines(t *testing.T, prof *ursus.DataFrame) []profLine {
	t.Helper()
	maps, err := prof.ToMaps()
	if err != nil {
		t.Fatal(err)
	}
	out := make([]profLine, len(maps))
	for i, m := range maps {
		node := m["node"].(string)
		trimmed := strings.TrimLeft(node, " ")
		l := profLine{
			node: trimmed, depth: (len(node) - len(trimmed)) / 2,
			own: m["own"].(uint64), rows: m["rows"].(int64), batches: m["batches"].(int64),
			bytes: m["peak_memory"].(int64),
		}
		if m["start"] != nil {
			l.ran, l.start, l.end = true, m["start"].(uint64), m["end"].(uint64)
		}
		out[i] = l
	}
	return out
}

// explainNodes is Explain's lines of nodes, its details left out.
func explainNodes(t *testing.T, lf *ursus.LazyFrame) []string {
	t.Helper()
	s, err := lf.Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if l = strings.TrimLeft(l, " "); l != "" && l[0] >= 'A' && l[0] <= 'Z' {
			out = append(out, l)
		}
	}
	return out
}

// TestProfileMeasuresEachNode: Profile answers as Collect does, and has a row a node
// of the plan, as Explain prints it, after its optimization's; each node's rows, its
// span after the optimization and covering its inputs', and its memory; a node that
// never ran null; and, on one thread, nodes' own times that add to no more than the
// query's (step 369).
func TestProfileMeasuresEachNode(t *testing.T) {
	c := ursus.Col
	ctx := t.Context()
	const n = 20000
	g := make([]int64, n)
	for i := range g {
		g[i] = int64(i % 100)
	}
	lf := ursus.Frame(ursus.Values("g", g), ursus.Values("x", spillInts(0, n)))
	var pq bytes.Buffer
	if err := lf.WriteParquet(ctx, &pq); err != nil {
		t.Fatal(err)
	}
	type want struct {
		node    string // a node's label's beginning
		rows    int64
		ran     bool
		holding bool // its peak memory is above zero
	}
	queries := []struct {
		name string
		q    *ursus.LazyFrame
		want []want
	}{
		{"a group-by sorted",
			lf.Filter(c("x").Gt(9)).WithColumns(c("x").Mul(2).Alias("y")).
				GroupBy(c("g")).Agg(c("y").Sum()).Sort(ursus.Asc(c("g"))).Head(5),
			[]want{{"LIMIT", 5, true, false}, {"SORT", 5, true, true}, {"AGGREGATE", 100, true, true},
				{"WITH_COLUMNS", n - 10, true, false}, {"FILTER", n - 10, true, false},
				{"MEMORY SCAN", n, true, false}}},
		{"a join",
			lf.Join(lf.Select(c("g").Unique()), ursus.JoinOn(c("g"))).Select(c("x").Sum()),
			[]want{{"AGGREGATE", 1, true, true}, {"JOIN", n, true, true}}},
		{"a Parquet scan under a window",
			ursus.ScanParquetBytes(pq.Bytes(), "p.parquet").Select(c("x").Sum().Over(c("g"))),
			[]want{{"PARQUET SCAN", n, true, false}}},
		{"a union's second input never read",
			ursus.Concat([]*ursus.LazyFrame{lf, lf}).Head(5),
			[]want{{"LIMIT", 5, true, false}, {"MEMORY SCAN", 0, false, false}}},
	}
	for _, q := range queries {
		ref, err := q.q.Collect(ctx, ursus.WithBatchSize(1024))
		if err != nil {
			t.Fatal(q.name, err)
		}
		nodes := explainNodes(t, q.q)
		for _, threads := range []int{1, 4} {
			df, prof, err := q.q.Profile(ctx, ursus.WithBatchSize(1024), ursus.WithThreads(threads))
			if err != nil {
				t.Fatalf("%s on %d threads: %v", q.name, threads, err)
			}
			what := func(s string) string {
				return q.name + " on " + map[int]string{1: "1 thread", 4: "4 threads"}[threads] + ": " + s
			}
			sameFrame(t, what("the answer"), df, ref)
			lines := profLines(t, prof)
			opt := lines[0]
			if opt.node != "optimization" || !opt.ran || opt.start != 0 || opt.end == 0 || opt.own != opt.end {
				t.Errorf("%s", what("the first row is not the optimization's"))
			}
			lines = lines[1:]
			if len(lines) != len(nodes) {
				t.Fatalf("%s: %d nodes, Explain's %d", what("the profile"), len(lines), len(nodes))
			}
			var own uint64
			var parents []profLine
			for i, l := range lines {
				if l.node != nodes[i] {
					t.Errorf("%s", what("node "+l.node+", Explain's "+nodes[i]))
				}
				if l.ran && (l.start > l.end || l.start < opt.end) {
					t.Errorf("%s: [%d, %d], the plan optimized at %d", what(l.node), l.start, l.end, opt.end)
				}
				// A node's span covers its inputs'.
				parents = append(parents[:l.depth], l)
				if p := parents[max(l.depth-1, 0)]; l.ran && l.depth > 0 && (l.start < p.start || l.end > p.end) {
					t.Errorf("%s: [%d, %d] outside its parent's [%d, %d]", what(l.node), l.start, l.end, p.start, p.end)
				}
				own += l.own
			}
			for _, w := range q.want {
				found := false
				for _, l := range lines {
					if !strings.HasPrefix(l.node, w.node) || l.ran != w.ran || (l.rows != w.rows) {
						continue
					}
					if w.holding != (l.bytes > 0) {
						t.Errorf("%s: %d bytes at its peak", what(l.node), l.bytes)
					}
					if !l.ran && (l.batches != 0 || l.own != 0) {
						t.Errorf("%s: never ran, and %d batches and %d µs", what(l.node), l.batches, l.own)
					}
					found = true
				}
				if !found {
					t.Errorf("%s", what("no "+w.node+" of the rows and run wanted\n"+prof.String()))
				}
			}
			// On one thread every node's own time is a part of the root's.
			if root := lines[0]; threads == 1 && own > root.end-root.start+uint64(len(lines)) {
				t.Errorf("%s: own times add to %d µs, the query ran %d", what("the profile"), own, root.end-root.start)
			}
		}
	}
}

// TestExplainHonoursOptFlags: Explain renders the plan the flags would run, where it
// ran every rule (step 369).
func TestExplainHonoursOptFlags(t *testing.T) {
	c := ursus.Col
	lf := ursus.Frame(ursus.Values("x", spillInts(0, 10)), ursus.Values("y", spillInts(0, 10))).
		Select(c("x"))
	all, err := lf.Explain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	none, err := lf.Explain(t.Context(), ursus.ExplainOptFlags(ursus.NoOptFlags()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(all, "projection: [x] (1/2 cols)") {
		t.Errorf("every rule on, the scan reads more than x:\n%s", all)
	}
	if !strings.Contains(none, "projection: * (2 cols)") {
		t.Errorf("every rule off, the scan reads less than every column:\n%s", none)
	}
}
