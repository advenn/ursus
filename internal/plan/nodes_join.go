package plan

import (
	"strconv"
	"strings"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/expr"
)

// JoinKind is the shape of a join.
type JoinKind uint8

const (
	// JoinInner is the zero value and the universal default: only matching pairs.
	JoinInner JoinKind = iota
	JoinLeft           // every left row; right columns null where unmatched
	JoinRight          // every right row; the mirror
	JoinFull           // every row from both sides
	JoinSemi           // left rows with at least one match — a filter
	JoinAnti           // left rows with no match — the inverse of semi
	JoinCross          // cartesian product, no keys
)

// String is load-bearing: it appears in Label, and golden plan files depend on it.
func (k JoinKind) String() string {
	switch k {
	case JoinLeft:
		return "LEFT"
	case JoinRight:
		return "RIGHT"
	case JoinFull:
		return "FULL"
	case JoinSemi:
		return "SEMI"
	case JoinAnti:
		return "ANTI"
	case JoinCross:
		return "CROSS"
	case JoinInner:
		return "INNER"
	default:
		// NOT "INNER". An out-of-range kind used to render as INNER, so a forgotten
		// entry would produce a plausible label and every golden would still pass —
		// the hazard the ConcatMode.String() comment above documents for eight other
		// enums, with this one left as its unfixed instance. AggOp.String() takes the
		// same care and says why: two ops rendering identically collide in the three
		// maps that dedup on String().
		return "JoinKind(" + strconv.Itoa(int(k)) + ")"
	}
}

// Keyed reports whether the kind matches on keys. Only Cross does not.
func (k JoinKind) Keyed() bool { return k != JoinCross }

// filtersLeft reports whether the kind emits left columns and nothing else.
func (k JoinKind) filtersLeft() bool { return k == JoinSemi || k == JoinAnti }

// leftCanBeNull reports whether a left column can be null-extended by this kind.
func (k JoinKind) leftCanBeNull() bool { return k == JoinRight || k == JoinFull }

// rightCanBeNull reports whether a right column can be null-extended.
func (k JoinKind) rightCanBeNull() bool { return k == JoinLeft || k == JoinFull }

// CoalesceMode is tri-state, and that is not over-engineering.
//
// The user-facing option has three states — unset, on, off — and unset is not
// equal to either: it means "merge for Inner/Left/Right/Semi/Anti, keep both for
// Full". A bool field would make a hand-built node's zero value mean
// Coalesce(false), which is not what the public API produces for the same input.
// CoalesceAuto as the zero value makes the node and the API agree by construction,
// which is the same reason Pushdown's zero value is Unsupported.
type CoalesceMode uint8

const (
	CoalesceAuto CoalesceMode = iota
	CoalesceOn
	CoalesceOff
)

// JoinValidation asserts a cardinality, erroring when the data violates it.
//
// The first term names the LEFT frame, matching pandas and Polars: ManyToOne on
// orders.Join(customers) asserts that the customers are unique.
//
// ursus-api.md calls accidental fan-out "the single most common analytics bug",
// which is the whole reason this exists.
type JoinValidation uint8

const (
	ValidateNone       JoinValidation = iota
	ValidateOneToOne                  // keys unique on BOTH sides
	ValidateOneToMany                 // keys unique on the LEFT
	ValidateManyToOne                 // keys unique on the RIGHT
	ValidateManyToMany                // no constraint
)

func (v JoinValidation) String() string {
	switch v {
	case ValidateOneToOne:
		return "1:1"
	case ValidateOneToMany:
		return "1:m"
	case ValidateManyToOne:
		return "m:1"
	case ValidateManyToMany:
		return "m:m"
	default:
		return "none"
	}
}

// RequiresLeftUnique and RequiresRightUnique are what the operator checks.
func (v JoinValidation) RequiresLeftUnique() bool {
	return v == ValidateOneToOne || v == ValidateOneToMany
}

func (v JoinValidation) RequiresRightUnique() bool {
	return v == ValidateOneToOne || v == ValidateManyToOne
}

// DefaultJoinSuffix is appended to a right-side column whose name collides.
const DefaultJoinSuffix = "_right"

// Join combines two inputs.
//
// # The first two-child node
//
// Every other plan node has exactly one child. The generic traversals — Walk,
// TransformUp, Explain, and both pushdown rules' default branches — were already
// written N-ary and need nothing. What does need care is anything that reasons
// about a SIDE, because left keys name left columns and right keys name right
// columns; see Expressions and rule_projection.
//
// # Self-joins
//
// lf.Join(lf, ...) is legal. The plan is then a shared subtree rather than a DAG,
// so Walk and TransformUp visit it twice, Resolve resolves it twice (idempotent —
// resolveScan short-circuits on Full != nil), Explain prints it twice, and the
// physical planner opens the source twice. All acceptable, none surprising once
// stated.
// Under a memory limit the build side is partitioned to disk on the join key and
// the buckets are replayed one at a time, so an equi-join over more data than
// memory works. The ORDER of the output then depends on the limit — see
// WithMemoryLimit — and there is deliberately no flag to opt out, because restoring
// probe order would mean holding an output that can be larger than both inputs.
type Join struct {
	Left, Right Node

	// LeftOn[i] is matched against RightOn[i]. Equal lengths, enforced by
	// resolveJoin. Empty for JoinCross and non-empty for every other kind.
	LeftOn, RightOn []expr.Node

	Kind JoinKind

	// Suffix is appended to a colliding right column name. "" means
	// DefaultJoinSuffix, so the zero value is the ordinary behaviour.
	Suffix string

	Coalesce   CoalesceMode
	NullsEqual bool // zero value false: a null key matches nothing, as in SQL
	Validate   JoinValidation

	// Residual is evaluated per candidate PAIR, before the match verdict, and is
	// SEMI/ANTI ONLY.
	//
	// # Why it cannot be a Filter above the join
	//
	// A semi join emits a left row once if ANY right row matches. Filtering its
	// output filters left rows that already survived, which answers a different
	// question: `EXISTS(r : k(r) == k(l) AND p(l, r))` is not
	// `EXISTS(r : k(r) == k(l)) AND p(l, ...)` — the second has no r to name.
	//
	// Inner needs none of this. There a pair IS the output row, so the residual is
	// exactly a Filter above the join, which is what JoinWhere and
	// collapse_cross_join do and why they needed no operator change.
	//
	// Names resolve against PairLayout, not Layout: Semi and Anti output the left
	// schema alone, so the predicate's right-hand columns exist in no schema this
	// node publishes.
	Residual []expr.Node
}

// HasResidual reports whether the match verdict depends on more than the keys.
//
// The physical layer branches on this in several places, and spelling it as a
// method rather than a len() keeps "what makes a join residual" in one place.
func (j *Join) HasResidual() bool { return len(j.Residual) > 0 }

func (j *Join) planNode()        {}
func (j *Join) Children() []Node { return []Node{j.Left, j.Right} }

func (j *Join) WithChildren(kids []Node) Node {
	if len(kids) != 2 {
		panic("plan: Join takes exactly two children")
	}
	c := *j
	c.Left, c.Right = kids[0], kids[1]
	return &c
}

func (j *Join) suffix() string {
	if j.Suffix == "" {
		return DefaultJoinSuffix
	}
	return j.Suffix
}

// coalescesKey reports whether a key pair is merged into one output column.
//
// # Why Auto requires the names to match
//
// Polars coalesces under its default regardless of the key names, so
// LeftOn("cust_id"), RightOn("id") silently DELETES the right frame's id column.
// ursus does not.
//
// Coalescing exists to solve the duplicate-name problem. When the keys are named
// differently each key already appears exactly once, so there is nothing for a
// default to fix — and dropping a column the user named explicitly breaks the
// standard "left join, then test the right key for null to find the non-matches"
// idiom. Coalesce(true) still merges them for anyone who wants it.
func (j *Join) coalescesKey(leftName, rightName string) bool {
	switch j.Coalesce {
	case CoalesceOn:
		return true
	case CoalesceOff:
		return false
	default:
		// Full keeps both keys because a full join can have a null key on either
		// side, so there is no single side to take the merged value from without
		// a coalesce expression.
		return j.Kind != JoinFull && j.Kind != JoinCross && leftName == rightName
	}
}

// coalescesAny reports the effective coalesce state for rendering. A pure function
// of Kind and Coalesce, so Label can call it without touching a schema.
func (j *Join) coalescesAny() bool {
	switch j.Coalesce {
	case CoalesceOn:
		return true
	case CoalesceOff:
		return false
	default:
		return j.Kind != JoinFull && j.Kind != JoinCross
	}
}

func (j *Join) Schema() (*dtype.Schema, error) {
	l, err := j.Layout()
	if err != nil {
		return nil, err
	}
	return l.Schema, nil
}

// Label renders the join without touching a schema.
//
// It must be total and deterministic: predicatePushdown.Apply detects whether it
// changed anything by comparing Explain output before and after, so a Label that
// could fail would make the optimizer fail.
//
// The effective coalesce state is ALWAYS rendered, even at its default, because
// coalescing changes the output schema — and step 2 had to fix Aggregate.Label
// for printing "2 aggregates", which made two structurally different plans
// indistinguishable in a golden file. Everything else renders only when it
// differs from the default, so goldens stay terse.
func (j *Join) Label() string {
	var b strings.Builder
	b.WriteString("JOIN ")
	b.WriteString(j.Kind.String())

	if j.Kind.Keyed() && len(j.LeftOn) > 0 {
		b.WriteString(" [")
		b.WriteString(expr.StringAll(j.LeftOn))
		b.WriteString("] = [")
		b.WriteString(expr.StringAll(j.RightOn))
		b.WriteString("]")
		// Semi and Anti emit no right column, so there is nothing a coalesce
		// setting could change and rendering one would be noise.
		if !j.Kind.filtersLeft() {
			if j.coalescesAny() {
				b.WriteString(" coalesce")
			} else {
				b.WriteString(" no coalesce")
			}
		}
	}
	// The residual is rendered because Explain is the only place a user can see
	// which conjuncts became hash keys and which stayed a per-pair test — the
	// difference between an O(|L|+|R|) query and an O(|L|x|R|) one.
	if j.HasResidual() {
		b.WriteString(" residual [")
		b.WriteString(expr.StringAll(j.Residual))
		b.WriteString("]")
	}
	if j.NullsEqual {
		b.WriteString(" nulls_equal")
	}
	if j.Validate != ValidateNone {
		b.WriteString(" validate ")
		b.WriteString(j.Validate.String())
	}
	if j.Suffix != "" && j.Suffix != DefaultJoinSuffix {
		b.WriteString(" suffix " + strconv.Quote(j.Suffix))
	}
	return b.String()
}

// childLabels marks which child is which. Without it Join(A, B) and Join(B, A)
// render identically, which for Left, Right, Semi and Anti are different queries —
// so a golden file could not see a rule that swapped the sides, and
// predicatePushdown would report "changed = false" for a rewrite that did.
func (j *Join) childLabels() []string { return []string{"left", "right"} }
