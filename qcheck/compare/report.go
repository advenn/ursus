package compare

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/advenn/ursus/qcheck/ast"
	"github.com/advenn/ursus/qcheck/sqlgen"
)

// The anti-vacuity thresholds, the self check's: an operation seen MinSeen times
// over input that has rows, which answered rows in fewer than MinRowsShare of
// them, fails as vacuous.
const (
	MinSeen      = 20
	MinRowsShare = 0.05
)

// OpStats counts one frame operation.
type OpStats struct {
	Seen      int // nodes of it in compared queries that agreed
	FedRows   int // nodes whose input had rows
	AnsRows   int // of those, nodes that answered rows
	Queries   int // compared queries holding it
	QueryRows int // of those, queries whose answer had rows
}

// Report is a run's outcome.
type Report struct {
	Cases    int
	Runs     int            // ursus runs
	Compared int            // queries both engines answered, compared
	Excluded map[string]int // queries not translated, by the rule that excludes them
	Fired    map[string]int // each translation rule's firings, over translated queries
	Allowed  map[string]int // the comparator's allowances that fired, by kind
	Ops      map[string]*OpStats
	Findings []Finding
	Elapsed  time.Duration
}

func newReport() *Report {
	return &Report{Excluded: map[string]int{}, Fired: map[string]int{}, Allowed: map[string]int{},
		Ops: map[string]*OpStats{}}
}

func (r *Report) op(name string) *OpStats {
	s := r.Ops[name]
	if s == nil {
		s = &OpStats{}
		r.Ops[name] = s
	}
	return s
}

func (r *Report) add(f Finding) { r.Findings = append(r.Findings, f) }

// countQuery counts the engine's answer against every operation of q.
func (r *Report) countQuery(q *ast.Query, a *Answer) {
	rows := a.Rows > 0 || a.Count > 0
	for _, op := range q.Ops() {
		s := r.op(op)
		s.Queries++
		if rows {
			s.QueryRows++
		}
	}
	if q.Count {
		s := r.op("Count")
		s.Seen++
		s.FedRows++
		if a.Count > 0 {
			s.AnsRows++
		}
	}
}

func (r *Report) countOp(op string, fed, rows bool) {
	s := r.op(op)
	s.Seen++
	if fed {
		s.FedRows++
		if rows {
			s.AnsRows++
		}
	}
}

// finish adds the run-wide findings: vacuous operations.
func (r *Report) finish() {
	for _, name := range r.opNames() {
		s := r.Ops[name]
		if s.FedRows >= MinSeen && float64(s.AnsRows) < MinRowsShare*float64(s.FedRows) {
			r.add(Finding{Kind: KindVacuous, Detail: fmt.Sprintf("%s answered rows %d times of %d over input with rows",
				name, s.AnsRows, s.FedRows)})
		}
	}
}

func (r *Report) opNames() []string {
	var names []string
	for n := range r.Ops {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// Failed is whether the run found anything.
func (r *Report) Failed() bool { return len(r.Findings) > 0 }

// Summary is the run's counts, without its findings.
func (r *Report) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "qcheck duck: %d cases, %d compared, %d excluded, in %v; %d finding(s)\n",
		r.Cases, r.Compared, sum(r.Excluded), r.Elapsed.Round(time.Millisecond), len(r.Findings))
	if len(r.Excluded) > 0 {
		fmt.Fprintf(&b, "excluded: %v\n", r.Excluded)
	}
	fmt.Fprintf(&b, "allowances fired: %v\n", r.Allowed)
	b.WriteString("rules fired:")
	for _, rule := range sqlgen.Rules {
		fmt.Fprintf(&b, " %s %d;", rule.ID, r.Fired[rule.ID])
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "%-12s %6s %10s %10s %8s %10s\n", "op", "nodes", "fed rows", "ans rows", "queries", "with rows")
	for _, name := range r.opNames() {
		s := r.Ops[name]
		fmt.Fprintf(&b, "%-12s %6d %10d %10d %8d %10d\n", name, s.Seen, s.FedRows, s.AnsRows, s.Queries, s.QueryRows)
	}
	return b.String()
}

func sum(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

// String is the findings and the summary.
func (r *Report) String() string {
	var b strings.Builder
	for _, f := range r.Findings {
		b.WriteString(f.String() + "\n")
	}
	b.WriteString(r.Summary())
	return b.String()
}
