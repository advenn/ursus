package expr

import (
	"strconv"
	"sync/atomic"

	"github.com/advenn/ursus/dtype"
)

// UDFImpl is a user-supplied kernel, erased.
//
// # Why an opaque interface rather than a func field
//
// The obvious field is `func(*data.Column) (*data.Column, error)`, and it does not
// compile. internal/gen/levels puts internal/data and internal/expr at the SAME
// level (20), and the rule is strictly-lower, so this package cannot import
// internal/data. Nor can that be fixed by renumbering: internal/plan imports this
// package, and TestPlanPackageNeedsNoArrow asserts that plan has zero transitive
// dependency on data, bitmap, arrowx or anything named arrow.
//
// So the erasure goes one step further than it first appears. A generic method on
// the public Expr captures its type parameters into a closure — which is required
// anyway, because Go 1.27 permits generic methods but forbids one from implementing
// an interface method, and Node is an interface. That closure is then held here
// behind a marker interface and implemented in a package above internal/data.
// internal/kernel (30) declares the concrete type; internal/physical (50) asserts
// it back out.
//
// # It is NOT sealed, and cannot be
//
// Node is sealed with an unexported node() because every Node implementation lives
// in this package. That technique is unavailable here for the very reason this
// interface exists: an unexported method can only be implemented from inside the
// package that declares it, and the implementation MUST live outside — above
// internal/data, which this package may not import. So the marker is exported.
//
// The consequence is that a type outside internal/kernel could satisfy this and
// reach expr.UDF.Impl. physical.evalUDF asserts for the one concrete type it knows
// and reports anything else as an internal error rather than ignoring it, which is
// the containment the seal would otherwise have provided.
type UDFImpl interface{ UDFKernel() }

// UDF applies a user-supplied Go function to one column.
//
// It is the escape hatch, and it is OPAQUE: nothing in the planner can inspect it,
// fold it, prove it pure, or predict what it returns. Everything the engine knows
// about it, the user declared — which is why the evaluator CHECKS those
// declarations rather than trusting them. See physical.evalUDF.
//
// # Name is required, and that is a correctness argument rather than a style one
//
// Node.String is load-bearing: "it appears in Explain output, in golden test files,
// and in error messages". Worse, FOUR maps deduplicate expressions on their
// rendering, over three code sites, because extractAggs is instantiated twice —
// plan.resolveWindow's temporaries, physical.extractAggs' byKey for a hash
// aggregate AND for a temporal group, and physical's partition sharing — and
// names_test.go states the consequence:
// "two expressions that RENDER THE SAME become one computation, and both names get
// the first one's answer".
//
// A Go closure has no printable identity. Without a name, two DIFFERENT user
// functions over the same column render alike, collapse into one computation, and
// both results come from whichever was seen first. That is the shape of two bugs
// this project has already shipped and fixed: an unparameterised Quantile that
// collapsed p50 and p99, and weak-versus-strong literals.
//
// A monotonic counter would deduplicate correctly too, and would make every golden
// plan file depend on allocation order. So: a name, supplied by the caller.
//
// That objection is about a counter that RENDERS. The id field below is a monotonic
// counter which never renders — String is unchanged and the golden plans with it —
// and it is what lets plan.CheckUDFNames refuse a collision instead of merging one.
// The name still does the deduplicating; the id only tells the planner when two
// names that look alike are not.
type UDF struct {
	Child Node

	// Out is the output type as DECLARED by the caller. It is not derived from
	// anything and cannot be — the function is opaque. The evaluator refuses a
	// column whose type disagrees with it.
	Out dtype.DataType

	// Name distinguishes this UDF from every other one in the query. Required.
	Name string

	// Kind is "map_elements" or "map_batches", for rendering and errors only.
	Kind string

	Impl UDFImpl

	// id is what tells two UDFs apart when their renderings cannot. Unexported, so
	// NewUDF is the only way to get one; a node built by a bare composite literal
	// carries 0, and CheckUDFNames reports that as an ursus bug rather than
	// treating two unidentified UDFs as the same one.
	//
	// It is NOT derived from Impl, and the obvious derivation does not work.
	// reflect.ValueOf(impl).Pointer() returns the same CODE pointer for every
	// MapElements udf in the program — they all close over the one func literal in
	// udf.go — so a check built on it matches always and detects nothing. Four
	// design documents in context_files record that remedy; it is a trap, and step
	// 65's as-built says so. Nor can Impl itself be compared: == on two UDFImpl
	// values compiles, because the field is an interface, and PANICS at runtime
	// with "comparing uncomparable type kernel.ColumnUDF".
	//
	// It must survive `c := *t`, which is how Rebuild here and extractAggs in
	// internal/physical copy this node — node-POINTER identity would be useless,
	// because those make a fresh pointer for the same udf on every rewrite. A plain
	// field is carried across for free, which is the whole reason it is one.
	//
	// It must also be SHARED by the copies multi-column expansion makes:
	// Col("a","b").MapElements("neg", ...) becomes two nodes with one name and one
	// closure, which is legal and must stay legal. Rebuild's struct copy gives them
	// one id, so the check never sees them as a collision.
	id uint64
}

// udfSeq mints UDF identities. Monotonic because it never has to be stable across
// runs — only distinct within one.
var udfSeq atomic.Uint64

// NewUDF builds a UDF node with a fresh identity, and is the only way to mint one.
//
// The fields stay exported because plan and physical read them and copy the struct
// wholesale. The id does not, so it cannot be forged onto a different Impl.
func NewUDF(child Node, out dtype.DataType, name, kind string, impl UDFImpl) *UDF {
	// Add returns the NEW value, so the first id is 1 and 0 is reliably "never
	// minted by this constructor".
	return &UDF{Child: child, Out: out, Name: name, Kind: kind, Impl: impl,
		id: udfSeq.Add(1)}
}

// ID is the node's identity: distinct for every UDF the constructor ever built, and
// EQUAL for every copy of one. Zero means the node did not come from NewUDF.
func (u *UDF) ID() uint64 { return u.id }

func (u *UDF) node()            {}
func (u *UDF) Children() []Node { return []Node{u.Child} }

// String renders the name, because the closure cannot be rendered and the name is
// the only thing that makes two UDFs distinguishable. Quoted so that a name
// containing punctuation cannot forge a different expression's rendering.
func (u *UDF) String() string {
	return u.Child.String() + "." + u.Kind + "(" + strconv.Quote(u.Name) + " -> " +
		u.Out.String() + ")"
}

// Field takes the child's NAME and the caller's declared type.
//
// Naming follows the receiver, like Call.Field: leftmost-column-wins means
// Col("c").MapElements(...) is named "c", and an Alias above overrides it.
//
// Resolving the child is not a formality — it is what makes a UDF over an unknown
// column a schema error at plan time rather than at the first batch.
func (u *UDF) Field(in *dtype.Schema) (dtype.Field, error) {
	if _, err := u.Child.Field(in); err != nil {
		return dtype.Field{}, err
	}
	return dtype.Field{
		Name: OutputName(u),
		Type: u.Out,
		// ALWAYS nullable, and there is deliberately no way to say otherwise.
		//
		// Every other node derives nullability from something the engine can
		// see. A UDF cannot: only the user knows whether fn returns nulls, and
		// step 49 established that a false non-null declaration is no longer
		// cosmetic — plan.sameValue gates a fold on it and the Parquet struct
		// reader skips validity tracking on it.
		//
		// So the one declaration the engine cannot check is the one it does not
		// accept. A caller who genuinely knows better says so with a strict
		// Cast, which IS checked.
		Nullable: true,
	}, nil
}
