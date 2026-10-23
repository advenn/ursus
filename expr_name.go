package ursus

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/advenn/ursus/internal/expr"
	"github.com/advenn/ursus/internal/uerr"
)

// The name namespace (step 323): Polars' Expr.name.

// NameExpr is an expression's naming functions, as Polars' name namespace has them:
// Col("a").Name().Suffix("_x"). Each names every column a multi-column expression
// makes, as Alias, one name, cannot.
type NameExpr struct{ e Expr }

// Name is the expression's naming functions.
func (e Expr) Name() NameExpr { return NameExpr{e} }

// Keep names the output after the first column the expression reads, through any
// alias or rename, as Polars' name.keep: Col("a").Mul(2).Alias("x").Name().Keep() is
// a. An expression that reads no column is refused.
func (n NameExpr) Keep() Expr {
	return wrap(&expr.Rename{Child: n.e.node(), Keep: true, Label: "name.keep()"})
}

// Map derives the output name from the input name, as MapName does.
func (n NameExpr) Map(fn func(string) string) Expr { return n.e.MapName(fn) }

// Prefix prepends to the output name, and Suffix appends to it, as Expr's do.
func (n NameExpr) Prefix(p string) Expr { return n.e.Prefix(p) }

func (n NameExpr) Suffix(s string) Expr { return n.e.Suffix(s) }

// ToLower and ToUpper change the output name's case, every script's, as Polars'
// name.to_lowercase and to_uppercase.
func (n NameExpr) ToLower() Expr {
	return wrap(&expr.Rename{Child: n.e.node(), Fn: strings.ToLower, Label: "name.to_lowercase()"})
}

func (n NameExpr) ToUpper() Expr {
	return wrap(&expr.Rename{Child: n.e.node(), Fn: strings.ToUpper, Label: "name.to_uppercase()"})
}

// Replace replaces every match of pattern in the output name with value, as Polars'
// name.replace: a regular expression whose groups value names as $1 or ${name}, or
// with literal the text itself. A pattern that does not compile is refused when the
// query is planned.
func (n NameExpr) Replace(pattern, value string, literal bool) Expr {
	label := "name.replace(" + quote(pattern) + ", " + quote(value) + ", literal=" +
		strconv.FormatBool(literal) + ")"
	if literal {
		return wrap(&expr.Rename{Child: n.e.node(), Label: label,
			Fn: func(s string) string { return strings.ReplaceAll(s, pattern, value) }})
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return wrap(&expr.Err{E: uerr.New(uerr.KindValue, "name.replace",
			"the pattern %q does not compile: %v", pattern, err)})
	}
	return wrap(&expr.Rename{Child: n.e.node(), Label: label,
		Fn: func(s string) string { return re.ReplaceAllString(s, value) }})
}

// PrefixFields prepends to the name of each field of a struct, and SuffixFields
// appends to it, as Polars' name.prefix_fields and suffix_fields; MapFields derives
// each from the old. The struct's own fields only, not a nested struct's, and the
// column keeps its name. The expression must be a Struct, and two fields of one new
// name are refused.
func (n NameExpr) PrefixFields(p string) Expr {
	return wrap(&expr.Rename{Child: n.e.node(), Label: "name.prefix_fields(" + quote(p) + ")",
		Fields: func(s string) string { return p + s }})
}

func (n NameExpr) SuffixFields(s string) Expr {
	return wrap(&expr.Rename{Child: n.e.node(), Label: "name.suffix_fields(" + quote(s) + ")",
		Fields: func(x string) string { return x + s }})
}

// MapFields is fn of each field's name. fn is called while the query is planned
// and as it runs; a panic in it is the caller's error, of kind ErrValue.
func (n NameExpr) MapFields(fn func(string) string) Expr {
	return wrap(expr.NewRename(expr.Rename{Child: n.e.node(), Label: "name.map_fields(...)",
		Fields: guardName("name.map_fields", "MapFields", fn)}))
}
