// Package uerr is ursus's error type.
//
// Error message quality is a feature, not a courtesy. A dataframe library is used
// interactively; the difference between
//
//	unknown column
//
// and
//
//	ursus: select: unknown column "reveune"
//	  did you mean: "revenue"?
//	  available: id, ts, revenue, region, units
//
// is the difference between a library people enjoy and one they tolerate. Every
// error that names a column MUST list the available columns and offer near
// matches; the helpers here make that the path of least resistance.
package uerr

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Kind classifies an error so callers can branch without string matching.
type Kind uint8

const (
	// KindInternal is a bug in ursus. Users should never legitimately see one.
	KindInternal Kind = iota
	// KindSchema is an unknown, duplicate, or ambiguous column.
	KindSchema
	// KindType is a dtype mismatch: an operation not defined for the given types.
	KindType
	// KindValue is a bad literal, an out-of-range cast, an unparseable pattern.
	KindValue
	// KindUnsupported is a well-formed request ursus cannot yet serve.
	KindUnsupported
	// KindIO is a failure reading or writing a data source.
	KindIO
	// KindResource is a limit ursus refused to exceed rather than a request it
	// could not understand: the query is well-formed and would have worked with
	// more memory or more spill space.
	//
	// It is its own kind because it is the one class of failure where the right
	// response is to change a knob and retry, and a caller should be able to
	// detect that without matching on a message.
	KindResource
)

func (k Kind) String() string {
	switch k {
	case KindSchema:
		return "schema"
	case KindType:
		return "type"
	case KindValue:
		return "value"
	case KindUnsupported:
		return "unsupported"
	case KindIO:
		return "io"
	case KindResource:
		return "resource"
	default:
		return "internal"
	}
}

// Error is ursus's error type.
//
// Op is the user-facing operation ("select", "filter", "cast"), not the Go
// function name. Node is set by the plan layer as the error propagates outward,
// so a user learns *where* in their query the failure was — something a bare
// error string cannot express.
type Error struct {
	Kind  Kind
	Op    string   // "select", "filter", "with_columns"
	Msg   string   // the one-line statement of what is wrong
	Node  string   // plan node attribution, e.g. "Project#3"; set by WithNode
	Hints []string // one per line, indented under Msg
	Err   error    // wrapped cause, if any
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("ursus: ")
	if e.Op != "" {
		b.WriteString(e.Op)
		b.WriteString(": ")
	}
	b.WriteString(e.Msg)
	for _, h := range e.Hints {
		b.WriteString("\n  ")
		b.WriteString(h)
	}
	if e.Node != "" {
		b.WriteString("\n  at ")
		b.WriteString(e.Node)
	}
	if e.Err != nil {
		b.WriteString("\n  caused by: ")
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

func (e *Error) Unwrap() error { return e.Err }

// Is supports errors.Is(err, uerr.Kind*) style matching via a sentinel per kind.
func (e *Error) Is(target error) bool {
	var k *kindSentinel
	if errors.As(target, &k) {
		return k.kind == e.Kind
	}
	return false
}

type kindSentinel struct{ kind Kind }

func (k *kindSentinel) Error() string { return k.kind.String() + " error" }

// Sentinels for errors.Is. Use as: errors.Is(err, uerr.ErrSchema).
var (
	ErrInternal    error = &kindSentinel{KindInternal}
	ErrSchema      error = &kindSentinel{KindSchema}
	ErrType        error = &kindSentinel{KindType}
	ErrValue       error = &kindSentinel{KindValue}
	ErrUnsupported error = &kindSentinel{KindUnsupported}
	ErrIO          error = &kindSentinel{KindIO}
	ErrResource    error = &kindSentinel{KindResource}
)

// New builds an Error.
func New(kind Kind, op, format string, args ...any) *Error {
	return &Error{Kind: kind, Op: op, Msg: fmt.Sprintf(format, args...)}
}

// Wrap attaches a cause.
func Wrap(err error, kind Kind, op, format string, args ...any) *Error {
	return &Error{Kind: kind, Op: op, Msg: fmt.Sprintf(format, args...), Err: err}
}

// Internalf reports a bug in ursus.
func Internalf(format string, args ...any) *Error {
	return &Error{Kind: KindInternal, Msg: fmt.Sprintf(format, args...),
		Hints: []string{"this is a bug in ursus; please report it"}}
}

// Hint appends a hint line. Returns e for chaining.
func (e *Error) Hint(format string, args ...any) *Error {
	e.Hints = append(e.Hints, fmt.Sprintf(format, args...))
	return e
}

// WithOp sets Op if it is not already set. The innermost operation wins, so an
// error raised inside expression typing keeps "cast" rather than being relabelled
// "select" by an outer frame.
func (e *Error) WithOp(op string) *Error {
	if e.Op == "" {
		e.Op = op
	}
	return e
}

// WithNode sets plan-node attribution if not already set, same innermost-wins rule.
func (e *Error) WithNode(node string) *Error {
	if e.Node == "" {
		e.Node = node
	}
	return e
}

// Annotate applies WithOp/WithNode to err if it is an *Error, and otherwise
// leaves it alone. Plan nodes call this as errors propagate outward.
func Annotate(err error, op, node string) error {
	var e *Error
	if errors.As(err, &e) {
		e.WithOp(op).WithNode(node)
	}
	return err
}

// UnknownColumn is the single most common error in a dataframe library, so it has
// a dedicated constructor that always produces the full diagnostic.
func UnknownColumn(op, name string, available []string) *Error {
	e := New(KindSchema, op, "unknown column %q", name)
	if m := NearMatches(name, available, 3); len(m) > 0 {
		e.Hint("did you mean: %s?", quoteJoin(m))
	}
	if len(available) == 0 {
		e.Hint("the frame has no columns")
	} else {
		e.Hint("available: %s", strings.Join(available, ", "))
	}
	return e
}

// DuplicateColumn reports a name collision, naming both positions.
func DuplicateColumn(op, name string, firstAt, secondAt int) *Error {
	return New(KindSchema, op, "duplicate column %q", name).
		Hint("appears at positions %d and %d; column names must be unique", firstAt, secondAt).
		Hint("use .Alias(...) to rename one of them")
}

func quoteJoin(xs []string) string {
	q := make([]string, len(xs))
	for i, x := range xs {
		q[i] = strconv.Quote(x)
	}
	return strings.Join(q, ", ")
}
