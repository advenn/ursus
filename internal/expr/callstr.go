package expr

import (
	"regexp"
	"strconv"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

func strCallOut(fn CallFn) dtype.DataType {
	switch fn {
	case FnStrContains, FnStrStartsWith, FnStrEndsWith, FnStrContainsAny:
		return dtype.Bool
	case FnStrFind, FnStrCountMatches, FnStrLenBytes, FnStrLenChars:
		return dtype.Uint32
	case FnStrFindMany:
		return dtype.List(dtype.Uint32)
	case FnStrDecode:
		return dtype.Binary
	case FnStrSplit, FnStrSplitInclusive, FnStrExtractAll, FnStrExtractMany:
		// The first calls in the namespace whose output is a NESTED type, and the
		// only place that fact is written down. The kernel never names List(String)
		// — data.NewList derives the element type from the child column it is
		// handed, deliberately, so that the declared type cannot disagree with the
		// data. This is the other half of that contract, and the two agreeing is
		// what ResolveCall means by being the single authority.
		//
		// Constant for every receiver, because the family has already normalised
		// Enum and Binary receivers down to String by the time this is reached.
		return dtype.List(dtype.String)
	default:
		return dtype.String
	}
}

// strStructOut types the string functions that answer a Struct (step 270), whose
// fields their arguments name: extract_groups a String field a capture group, named
// as GroupNames names it; split_exact field_0 to field_n; splitn field_0 to
// field_{n-1}, as Polars names them.
func strStructOut(c *Call) (dtype.DataType, error) {
	var names []string
	switch c.Fn {
	case FnStrExtractGroups:
		pat, ok := callLitString(c, 1)
		if !ok {
			return dtype.Null, uerr.Internalf("expr: %s without its pattern", c.Fn)
		}
		re, err := regexp.Compile(pat)
		if err != nil {
			return dtype.Null, uerr.Wrap(err, uerr.KindValue, "str", "invalid regular expression %q", pat)
		}
		if names = GroupNames(re); len(names) == 0 {
			return dtype.Null, uerr.New(uerr.KindValue, "str.extract_groups",
				"extract_groups's pattern %q has no capture group", pat).
				Hint("a group is (...), and named (?<name>...)")
		}
	default:
		l, ok := c.Args[len(c.Args)-1].(*Lit)
		if c.Fn == FnStrSplitExact && len(c.Args) == 4 {
			l, ok = c.Args[2].(*Lit)
		}
		var n int64
		if ok {
			n, ok = l.Value.(int64)
		}
		if !ok {
			return dtype.Null, uerr.Internalf("expr: %s without its count", c.Fn)
		}
		if c.Fn == FnStrSplitExact {
			n++
		}
		if n < 1 {
			return dtype.Null, uerr.New(uerr.KindValue, "str.splitn", "splitn takes at least one part, got %d", n).
				Hint("Split makes a List of every part")
		}
		for j := range n {
			names = append(names, "field_"+strconv.FormatInt(j, 10))
		}
	}
	fields := make([]dtype.Field, len(names))
	for i, nm := range names {
		fields[i] = dtype.Field{Name: nm, Type: dtype.String, Nullable: true}
	}
	return dtype.Struct(fields...), nil
}

// jsonDecodeOut is json_decode's type, the type of its literal argument: Booleans,
// numbers, Strings, and Lists and Structs of them (step 271).
func jsonDecodeOut(c *Call) (dtype.DataType, error) {
	l, ok := c.Args[len(c.Args)-1].(*Lit)
	if !ok || len(c.Args) != 2 {
		return dtype.Null, uerr.Internalf("expr: %s without its type", c.Fn)
	}
	var check func(t dtype.DataType) error
	check = func(t dtype.DataType) error {
		switch {
		case t.ID() == dtype.TypeList:
			return check(t.Inner())
		case t.ID() == dtype.TypeStruct:
			for _, f := range t.Fields() {
				if err := check(f.Type); err != nil {
					return err
				}
			}
			return nil
		case t.ID() == dtype.TypeBool, t.ID() == dtype.TypeString, t.IsInteger(), t.IsFloat(), t.IsNull():
			return nil
		}
		return uerr.New(uerr.KindType, "str.json_decode",
			"json_decode decodes to Booleans, numbers, Strings, Lists and Structs, not %s", t).
			Hint("decode to a String and convert it after")
	}
	if err := check(l.DT); err != nil {
		return dtype.Null, err
	}
	return nullableThrough(l.DT), nil
}

// nullableThrough is t with every Struct field nullable, at any depth: a JSON object
// may leave out any field.
func nullableThrough(t dtype.DataType) dtype.DataType {
	switch t.ID() {
	case dtype.TypeList:
		return dtype.List(nullableThrough(t.Inner()))
	case dtype.TypeStruct:
		fields := t.Fields()
		out := make([]dtype.Field, len(fields))
		for i, f := range fields {
			out[i] = dtype.Field{Name: f.Name, Type: nullableThrough(f.Type), Nullable: true}
		}
		return dtype.Struct(out...)
	}
	return t
}

// GroupNames are a pattern's capture groups' field names, as Polars names them:
// each group's name, or its number where it has none.
func GroupNames(re *regexp.Regexp) []string {
	names := re.SubexpNames()[1:]
	out := make([]string, len(names))
	for i, nm := range names {
		if nm == "" {
			nm = strconv.Itoa(i + 1)
		}
		out[i] = nm
	}
	return out
}
