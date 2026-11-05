package expr

import (
	"strings"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// CallFn, its constants, classifiers and names are callfn.go.

// Call applies a namespaced function.
//
// Args[0] is the RECEIVER — the column the method was called on. Args[1:] are the
// function's parameters and must resolve to literals; a column-valued pattern is
// representable in this IR and simply not implemented yet, which is the right way
// round for a constraint that may lift later.
//
// Args are ordinary children, so RootNames, expansion and projection pushdown all
// see through a Call with no special case. That is the property the design docs
// predicted for window nodes and it holds here for the same reason.
type Call struct {
	Fn   CallFn
	Args []Node
}

func (c *Call) node()            {}
func (c *Call) Children() []Node { return c.Args }

func (c *Call) String() string {
	parts := make([]string, len(c.Args))
	for i, a := range c.Args {
		parts[i] = a.String()
	}
	if len(parts) == 0 || c.Fn.IsHorizontal() {
		return c.Fn.String() + "(" + strings.Join(parts, ", ") + ")"
	}
	return parts[0] + "." + c.Fn.String() + "(" + strings.Join(parts[1:], ", ") + ")"
}

func (c *Call) Field(in *dtype.Schema) (dtype.Field, error) {
	if len(c.Args) == 0 {
		return dtype.Field{}, uerr.Internalf("expr: %s has no receiver", c.Fn)
	}
	if c.Fn.IsHorizontal() {
		fields := make([]dtype.Field, len(c.Args))
		for i, a := range c.Args {
			f, err := a.Field(in)
			if err != nil {
				return dtype.Field{}, err
			}
			fields[i] = f
		}
		if c.Fn == FnListAppend {
			// Typed by its mode, a literal the lowering wrote, which ResolveHorizontal's
			// types alone do not carry.
			out, err := listAppendOut(c, fields)
			if err != nil {
				return dtype.Field{}, err
			}
			return dtype.Field{Name: fields[0].Name, Type: out, Nullable: true}, nil
		}
		out, err := ResolveHorizontal(c.Fn, fields)
		if err != nil {
			return dtype.Field{}, err
		}
		return dtype.Field{Name: fields[0].Name, Type: out, Nullable: true}, nil
	}
	recv, err := c.Args[0].Field(in)
	if err != nil {
		return dtype.Field{}, err
	}
	out, err := ResolveCall(c, recv.Type)
	if err != nil {
		return dtype.Field{}, err
	}
	// The name follows the receiver — leftmost-column-wins, same as everywhere
	// else — so `Col("s").Str().ToLower()` is still called "s" unless aliased. But a
	// struct's field is named after the field, as Polars names it (step 275).
	return dtype.Field{Name: CallOutputName(c, recv.Name), Type: out, Nullable: true}, nil
}

// CallOutputName is a call's name for a receiver named recv: the receiver's, but a
// struct's field's, which is the field's own.
func CallOutputName(c *Call, recv string) string {
	if c.Fn == FnStructField {
		if name, ok := callLitString(c, 1); ok {
			return name
		}
	}
	return recv
}

// ResolveCall gives the output type of a call applied to a receiver of type in.
//
// It is the single authority, consulted by Field for the plan's schema and by the
// evaluator for the kernel's output type. Two copies would drift, which is the
// Binding lesson.
//
// It takes the whole Call rather than just the function because `.struct.field` is
// the first call whose output type depends on an ARGUMENT — `field("age")` is Int64
// and `field("city")` is String, from the same receiver.
//
// It used to add "Every other family answers from the receiver's type alone", and
// that was false. `.dt.truncate` is the second, and the divergence lived exactly in
// the sentence: the kernel routes on the interval — `truncate(Time, 1 month)` has no
// answer while `truncate(Time, 1 hour)` does — so a resolver that saw only the
// receiver promised both. The argument is a CONSTANT of the expression, not data, so
// there was never a reason the planner could not see it.
func ResolveCall(c *Call, in dtype.DataType) (dtype.DataType, error) {
	fn := c.Fn
	switch {
	case fn.IsString():
		// HasStringStorage, not IsString. IsString is TRUE FOR ENUM, whose values are
		// logically strings but are stored as a Uint32 index — so a column that
		// passed this gate reached kernel.StrCall, which calls Strings() and gets a
		// zero accessor, and PANICKED on the first row. dtype.IsString's own doc warns
		// about exactly this misuse and names HasStringStorage as the storage
		// question's predicate. Latent today only because Enum columns cannot yet be
		// built from the public API.
		//
		// An Enum is read as its text, which the string kernels decode it to first
		// (step 273), and a Categorical too, which they relabel a String (step 351),
		// where Polars refuses both.
		//
		// A Binary is refused, as Polars refuses it (step 274): its bytes are
		// Bin()'s, and a text function such as slice would split them as runes.
		if in.ID() != dtype.TypeString && in.ID() != dtype.TypeNull && in.ID() != dtype.TypeEnum && in.ID() != dtype.TypeCategorical {
			e := uerr.New(uerr.KindType, "str", "%s requires a String operand, got %s", fn, in)
			if in.ID() == dtype.TypeBinary {
				return dtype.Null, e.Hint("a Binary column's functions are Bin()'s, e.g. .Bin().Slice(0, 4)")
			}
			return dtype.Null, e.Hint("cast the column first, e.g. .Cast(ursus.String)")
		}
		if fn.isStrptime() {
			return strptimeOut(c)
		}
		switch fn {
		case FnStrExtractGroups, FnStrSplitExact, FnStrSplitN:
			return strStructOut(c)
		case FnStrJsonDecode:
			return jsonDecodeOut(c)
		case FnStrJsonPathMatch:
			path, ok := callLitString(c, 1)
			if !ok {
				return dtype.Null, uerr.Internalf("expr: %s without its path", fn)
			}
			if _, err := ParseJSONPath(path); err != nil {
				return dtype.Null, err
			}
		}
		return strCallOut(fn), nil

	case fn.IsTemporal():
		return dtCallOut(c, in)

	case fn.IsGeneral():
		return genCallOut(c, in)

	case fn.IsMath():
		return mathCallOut(fn, in, c)

	case fn.IsList():
		return listCallOut(c, in)

	case fn.IsStruct():
		return structCallOut(c, in)

	case fn.IsBinary():
		return binCallOut(c, in)

	case fn.IsArr():
		return arrCallOut(c, in)

	case fn.IsHorizontal():
		// Typed from every operand, which a receiver's type is not: Field and the
		// evaluator call ResolveHorizontal.
		return dtype.Null, uerr.Internalf("expr: %s is typed by ResolveHorizontal", fn)

	default:
		return dtype.Null, uerr.Internalf("expr: unknown call %d", fn)
	}
}

// The families' output types are callhorizontal.go (ResolveHorizontal and its
// helpers), callgen.go (the general and maths families), callstr.go (string),
// calldt.go (temporal) and callnested.go (list, struct and arr).

func callLitInt(args []any, i int) int64 {
	if i >= len(args) {
		return 0
	}
	v, _ := args[i].(int64)
	return v
}

// CallArgs extracts the literal arguments of a Call, requiring each to be a
// literal rather than a computed expression.
//
// Column-valued arguments are expressible in the IR and rejected here, so the
// error names the limitation rather than the evaluator failing on a shape it did
// not expect.
func CallArgs(c *Call) ([]any, error) {
	out := make([]any, 0, len(c.Args)-1)
	for _, a := range c.Args[1:] {
		l, ok := a.(*Lit)
		if !ok {
			return nil, uerr.New(uerr.KindUnsupported, "call",
				"%s requires constant arguments, got %s", c.Fn, a.String()).
				Hint("a column-valued argument is not implemented yet")
		}
		out = append(out, l.Value)
	}
	return out, nil
}

// callFieldNames reads the constant strings after a call's receiver: the names a
// struct's fields are given. Two of one name are refused, as a struct cannot hold
// them.
func callFieldNames(c *Call, op string) ([]string, error) {
	names := make([]string, 0, len(c.Args)-1)
	seen := make(map[string]bool, len(c.Args)-1)
	for i := 1; i < len(c.Args); i++ {
		name, ok := callLitString(c, i)
		if !ok {
			return nil, uerr.Internalf("expr: %s argument %d is not a name", c.Fn, i)
		}
		if seen[name] {
			return nil, uerr.New(uerr.KindSchema, op,
				"%s names two fields %q", c.Fn, name).
				Hint("give each field a name of its own")
		}
		seen[name] = true
		names = append(names, name)
	}
	return names, nil
}

// callLitString reads a constant string argument.
func callLitString(c *Call, i int) (string, bool) {
	if i < 0 || i >= len(c.Args) {
		return "", false
	}
	l, ok := c.Args[i].(*Lit)
	if !ok {
		return "", false
	}
	s, ok := l.Value.(string)
	return s, ok
}
