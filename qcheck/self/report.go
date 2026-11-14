package self

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/advenn/ursus/qcheck/ast"
)

// The anti-vacuity thresholds: an operation seen MinSeen times over input that has
// rows, which answered rows in fewer than MinRowsShare of them, fails as vacuous;
// so does one refused every time of at least MinSeen.
const (
	MinSeen      = 20
	MinRowsShare = 0.05
)

// MinCanaries is how many canaries must have run, none differing, before the run
// fails the float-threads rule as stale.
const MinCanaries = 10

// OpStats counts one frame operation.
type OpStats struct {
	Seen      int // nodes of it
	Refused   int // nodes whose Count was refused
	FedRows   int // nodes whose input had rows
	AnsRows   int // of those, nodes that answered rows
	Queries   int // queries holding it
	QueryRows int // of those, queries whose answer had rows
}

// Report is a run's outcome.
type Report struct {
	Cases       int
	Runs        int // queries run, every configuration and Count of a node
	Comparisons int // variants compared with the reference
	// The float-threads rule: comparisons where a noisy column met a different
	// thread count and the reference had rows, those where it fired, and the cells.
	Eligible, Fired, FiredCells int
	// The canaries run (self.canary), and those whose bits differed within the
	// bound: the rule's reason to exist.
	Canaries, CanariesFired int
	Refused                 int // queries the reference refused
	Ops                     map[string]*OpStats
	Refusals                map[string]int // each refusal's first line, counted
	Findings                []Finding
	Elapsed                 time.Duration
}

func newReport() *Report {
	return &Report{Ops: map[string]*OpStats{}, Refusals: map[string]int{}}
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

func (r *Report) refusal(err error) { r.Refusals[firstLine(err)]++ }

// countQuery counts the reference's answer against every operation of q.
func (r *Report) countQuery(q *ast.Query, a *Answer) {
	if a.Err != nil {
		r.Refused++
		r.refusal(a.Err)
	}
	rows := a.Err == nil && (a.Rows > 0 || a.Count > 0)
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
		if a.Err != nil {
			s.Refused++
		} else {
			s.FedRows++
			if a.Count > 0 {
				s.AnsRows++
			}
		}
	}
}

func (r *Report) countOp(op string, fed, ok, rows bool) {
	s := r.op(op)
	s.Seen++
	switch {
	case !ok:
		s.Refused++
	case fed:
		s.FedRows++
		if rows {
			s.AnsRows++
		}
	}
}

// finish adds the run-wide findings: vacuous operations and a stale rule.
func (r *Report) finish() {
	for _, name := range r.opNames() {
		s := r.Ops[name]
		switch {
		case s.Seen >= MinSeen && s.Refused == s.Seen:
			r.add(Finding{Kind: KindVacuous, Detail: fmt.Sprintf("%s was refused all %d times", name, s.Seen)})
		case s.FedRows >= MinSeen && float64(s.AnsRows) < MinRowsShare*float64(s.FedRows):
			r.add(Finding{Kind: KindVacuous, Detail: fmt.Sprintf("%s answered rows %d times of %d over input with rows",
				name, s.AnsRows, s.FedRows)})
		}
	}
	if r.Canaries >= MinCanaries && r.CanariesFired == 0 {
		r.add(Finding{Kind: KindStale, Detail: fmt.Sprintf("the float-threads rule's %d canaries all agreed "+
			"with one thread to the bit: a float Sum on eight threads no longer differs, so the allowance "+
			"is stale", r.Canaries)})
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
	fmt.Fprintf(&b, "qcheck self: %d cases, %d runs, %d comparisons in %v; %d finding(s)\n",
		r.Cases, r.Runs, r.Comparisons, r.Elapsed.Round(time.Millisecond), len(r.Findings))
	fmt.Fprintf(&b, "float-threads rule: fired in %d of %d comparisons it could (%d cells); "+
		"%d of %d canaries differed\n", r.Fired, r.Eligible, r.FiredCells, r.CanariesFired, r.Canaries)
	fmt.Fprintf(&b, "reference refused %d of %d queries\n", r.Refused, r.Cases)
	fmt.Fprintf(&b, "%-12s %6s %8s %10s %10s %8s %10s\n", "op", "nodes", "refused", "fed rows", "ans rows",
		"queries", "with rows")
	for _, name := range r.opNames() {
		s := r.Ops[name]
		fmt.Fprintf(&b, "%-12s %6d %8d %10d %10d %8d %10d\n", name, s.Seen, s.Refused, s.FedRows, s.AnsRows,
			s.Queries, s.QueryRows)
	}
	if len(r.Refusals) > 0 {
		type kv struct {
			msg string
			n   int
		}
		var all []kv
		for m, n := range r.Refusals {
			all = append(all, kv{m, n})
		}
		slices.SortFunc(all, func(a, b kv) int { return b.n - a.n })
		b.WriteString("refusals:\n")
		for i, e := range all {
			if i == 10 {
				fmt.Fprintf(&b, "  … %d more kinds\n", len(all)-10)
				break
			}
			fmt.Fprintf(&b, "  %5d  %s\n", e.n, e.msg)
		}
	}
	return b.String()
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
