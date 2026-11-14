// Package model is qcheck's vocabulary, shared by every other package of the module:
// the types a generated column or an answer takes, a generated table, and a
// configuration a query runs under.
//
// It knows nothing of ursus, so that step 513's SQL side and step 514's shrinker read
// the same tables and configurations as the self check does; build maps a Type to
// ursus's, and sqlgen will map it to DuckDB's.
package model

import (
	"fmt"
	"strings"

	"github.com/apache/arrow-go/v18/arrow"
)

// Type is a column's type: the six a generated table holds, and the ones an
// expression or an aggregate over them answers.
type Type uint8

// The types. Int8 and Int16 come from casts, Int128 from an integer Sum, Uint64
// from Count, Len and NUnique: ursus's own answer types (expr.go, agg.go).
const (
	Invalid Type = iota
	Bool
	Int8
	Int16
	Int32
	Int64
	Int128
	Uint64
	Float64
	String
	Date
)

// Base are the types of a generated table's columns, step 453's list.
var Base = []Type{Int64, Int32, Float64, String, Bool, Date}

var typeNames = [...]string{"Invalid", "Bool", "Int8", "Int16", "Int32", "Int64", "Int128",
	"Uint64", "Float64", "String", "Date"}

func (t Type) String() string {
	if int(t) < len(typeNames) {
		return typeNames[t]
	}
	return fmt.Sprintf("Type(%d)", t)
}

// IsSignedInt is an integer of 8 to 128 bits.
func (t Type) IsSignedInt() bool { return t >= Int8 && t <= Int128 }

// IsInt is a signed integer or Uint64.
func (t Type) IsInt() bool { return t.IsSignedInt() || t == Uint64 }

// IsNumeric is an integer or a float.
func (t Type) IsNumeric() bool { return t.IsInt() || t == Float64 }

// Bits is a signed integer's width.
func (t Type) Bits() int {
	switch t {
	case Int8:
		return 8
	case Int16:
		return 16
	case Int32:
		return 32
	case Int64:
		return 64
	case Int128:
		return 128
	}
	return 0
}

// Arith is a type arithmetic is generated on: the signed integers up to 64 bits and
// Float64. Int128's arithmetic is exact or refused and Uint64's Neg is refused
// (expr.go), so an aggregate's answer is compared and sorted but not computed on.
func (t Type) Arith() bool { return (t.IsSignedInt() && t != Int128) || t == Float64 }

// Promote is the type two operands meet at in arithmetic, a conditional's branches
// and a fill by a column, as ursus's dtype.Promote answers for the types qcheck
// generates: a type with itself; two signed integers at the wider; an integer and a
// float at Float64. Text, a Boolean and a Date meet only themselves (feature-gaps
// §9: a conditional's branches meet by Promote, which refuses the rest).
func Promote(a, b Type) (Type, bool) {
	switch {
	case a == b:
		return a, true
	case a.IsSignedInt() && b.IsSignedInt():
		if a.Bits() >= b.Bits() {
			return a, true
		}
		return b, true
	case a.IsSignedInt() && b == Float64, a == Float64 && b.IsSignedInt():
		return Float64, true
	}
	return Invalid, false
}

// Comparable is whether two types compare with Eq, Lt and the rest.
func Comparable(a, b Type) bool {
	if a.IsNumeric() && b.IsNumeric() {
		return true
	}
	return a == b
}

// Orderable is a type Min, Max and Sort take.
func (t Type) Orderable() bool { return t != Invalid }

// Profile is how a column's values are drawn.
type Profile uint8

const (
	// Tame values: small integers, few distinct values, floats in quarters (whose
	// sums are exact), short ASCII text, dates of two recent years.
	Tame Profile = iota
	// Wild values: integers over the whole range, floats of arbitrary bits across
	// many magnitudes, text of any ASCII, dates across millennia.
	Wild
	// Extreme values among the wild: NaN, ±0, ±Inf, each type's least and greatest,
	// subnormals, Unicode, NUL, text of thousands of bytes.
	Extreme
)

var profileNames = [...]string{"tame", "wild", "extreme"}

func (p Profile) String() string {
	if int(p) < len(profileNames) {
		return profileNames[p]
	}
	return fmt.Sprintf("Profile(%d)", p)
}

// Column describes one generated column.
type Column struct {
	Name     string
	Type     Type
	Profile  Profile
	NullRate float64 // 0, 0.1, 0.5 or 1
	// Card bounds a tame column's distinct values, so keys repeat; 0 is no bound.
	Card int
}

// Table is a generated table: its columns' description and its rows, one Arrow
// record. A seed alone rebuilds it (gen.Table).
type Table struct {
	Name string
	Seed uint64
	Rows int
	Cols []Column
	Rec  arrow.RecordBatch
}

// Col is the column named name, or nil.
func (t *Table) Col(name string) *Column {
	for i := range t.Cols {
		if t.Cols[i].Name == name {
			return &t.Cols[i]
		}
	}
	return nil
}

// String describes the table on one line.
func (t *Table) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%d rows, seed %d):", t.Name, t.Rows, t.Seed)
	for _, c := range t.Cols {
		fmt.Fprintf(&b, " %s %s/%s/null %.1f", c.Name, c.Type, c.Profile, c.NullRate)
		if c.Card > 0 {
			fmt.Fprintf(&b, "/card %d", c.Card)
		}
		b.WriteByte(';')
	}
	return strings.TrimSuffix(b.String(), ";")
}
