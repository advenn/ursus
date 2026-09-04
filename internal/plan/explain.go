package plan

import (
	"strconv"
	"strings"
)

// ExplainOptions controls Explain's output.
type ExplainOptions struct {
	// Schema annotates each node with its output schema. Verbose, but it is the
	// fastest way to see where a type went wrong.
	Schema bool
	// Projection shows the column pruning a scan ended up with, and how many of
	// the source's columns that is. This is the visible evidence that pushdown
	// happened, so it is on by default.
	Projection bool
}

// DefaultExplainOptions shows projections but not schemas.
func DefaultExplainOptions() ExplainOptions {
	return ExplainOptions{Projection: true}
}

// Explain renders a plan as an indented tree.
//
// The output is deliberately stable and diff-friendly: it is snapshot-tested in
// golden files, which is how optimizer regressions get caught. A query that
// returns the right answer by reading forty columns instead of two is still a
// regression, and the only thing that notices is a plan diff.
func Explain(n Node, opts ExplainOptions) (string, error) {
	var b strings.Builder
	if err := explainNode(&b, n, 0, opts); err != nil {
		return "", err
	}
	return b.String(), nil
}

func explainNode(b *strings.Builder, n Node, depth int, opts ExplainOptions) error {
	indent := strings.Repeat("  ", depth)

	b.WriteString(indent)
	b.WriteString(n.Label())
	b.WriteString("\n")

	detail := indent + "  "

	if s, ok := n.(*Scan); ok && opts.Projection && s.Full != nil {
		if s.Projection == nil {
			b.WriteString(detail + "projection: * (" +
				strconv.Itoa(s.Full.Len()) + " cols)\n")
		} else {
			b.WriteString(detail + "projection: [" +
				strings.Join(s.Projection, ", ") + "] (" +
				strconv.Itoa(len(s.Projection)) + "/" +
				strconv.Itoa(s.Full.Len()) + " cols)\n")
		}
		if len(s.Predicate) > 0 {
			parts := make([]string, len(s.Predicate))
			for i, p := range s.Predicate {
				parts[i] = p.String()
			}
			b.WriteString(detail + "predicate: [" + strings.Join(parts, " AND ") + "]\n")

			// A pushed predicate makes the scan read columns it does not emit, so
			// the projection line above is no longer the whole story. Show the read
			// set whenever it differs, or a plan rendered as "projection: [a]" while
			// decoding two columns misleads exactly the person debugging it.
			// Projection nil means the scan already reads everything, so there is
			// nothing extra to report.
			if reads := s.ReadSet(); s.Projection != nil && len(reads) != len(s.Projection) {
				b.WriteString(detail + "reads: [" + strings.Join(reads, ", ") + "] (" +
					strconv.Itoa(len(reads)) + "/" + strconv.Itoa(s.Full.Len()) + " cols)\n")
			}
		}
		if s.MaxRows > 0 {
			b.WriteString(detail + "max rows: " + strconv.Itoa(s.MaxRows) + "\n")
		}
	}

	if opts.Schema {
		s, err := n.Schema()
		if err != nil {
			return err
		}
		b.WriteString(detail + "schema: " + s.String() + "\n")
	}

	// A node whose children are not interchangeable labels them.
	//
	// Without this, Join(A, B) and Join(B, A) render identically — and for Left,
	// Right, Semi and Anti those are different queries. Two things would break: a
	// golden file could not see a rule that swapped the sides, and
	// predicatePushdown, which detects change by string-comparing Explain output,
	// would report "changed = false" for a rewrite that did.
	//
	// An optional interface rather than a Node method, so the seven existing
	// single-child nodes implement nothing and their rendering is byte-identical.
	kids := n.Children()
	if l, ok := n.(childLabeler); ok {
		labels := l.childLabels()
		for i, c := range kids {
			if i < len(labels) {
				b.WriteString(detail + labels[i] + ":\n")
			}
			if err := explainNode(b, c, depth+2, opts); err != nil {
				return err
			}
		}
		return nil
	}

	for _, c := range kids {
		if err := explainNode(b, c, depth+1, opts); err != nil {
			return err
		}
	}
	return nil
}

// childLabeler is implemented by nodes whose children are not interchangeable.
type childLabeler interface{ childLabels() []string }
