package expr

import (
	"fmt"
	"strconv"
	"strings"
)

// Identity is what two expressions must share to be one computation.
//
// # String is a rendering, not an identity
//
// String is for Explain, the golden plans and error messages, and it leaves two
// things out:
//
//   - A strong literal renders its value and not its type. Lit(int8(100)) and
//     Lit(int64(100)) both render "lit(100)", as do Lit(1) and Lit(1.0), and
//     float32(0.1) and 0.1. They compute different things: i8 + int8(100) wraps
//     in Int8, and i8 + int64(100) does not.
//   - A UDF renders its name, and two different functions can share one.
//
// Four map instances merged computations on String — the window temporaries, the
// aggregate specs of a group-by and of a temporal group, and the window
// partitionings — so the second of two literals that render alike got the first
// one's answer, or the first one's type under its own schema (audit.md, O3).
//
// Identity is String followed by what the rendering leaves out: for every literal,
// its Go type and its DataType; for every UDF, the id NewUDF minted. Every part is
// length-prefixed, so no two different sequences of parts concatenate alike — a
// DataType's own rendering can hold anything, an Enum's categories for one.
// It is never shown to anyone, which is what lets it carry an id that would make
// every golden plan depend on allocation order.
//
// # Why a rendering, and not the values compared with ==
//
// == merges −0 and +0, and x/−0.0 and x/0.0 are −Inf and +Inf. It never merges
// NaN with NaN, which compute alike. And a []byte literal in a map key panics. The
// rendering has none of those problems: fmt prints the shortest string that reads
// back as the same float, so −0 prints "-0", and every NaN prints "NaN".
//
// # Why a string, and not a rewritten tree
//
// A tree with the types written into it would need a Node type of its own, in a
// package whose type switches promise to be exhaustive over Node.
func Identity(n Node) string {
	var b strings.Builder
	part := func(s string) {
		b.WriteString(strconv.Itoa(len(s)))
		b.WriteByte(':')
		b.WriteString(s)
	}
	part(n.String())
	Walk(n, func(x Node) bool {
		switch t := x.(type) {
		case *Lit:
			part("lit")
			part(fmt.Sprintf("%T", t.Value))
			part(t.DT.String())
			part(t.String())
		case *UDF:
			part("udf")
			part(strconv.FormatUint(t.id, 10))
		}
		return true
	})
	return b.String()
}

// IdentityAll is Identity for a list of expressions, where the list is the unit —
// a window's partition keys.
func IdentityAll(ns []Node) string {
	var b strings.Builder
	b.WriteString(strconv.Itoa(len(ns)))
	for _, n := range ns {
		id := Identity(n)
		b.WriteByte(';')
		b.WriteString(strconv.Itoa(len(id)))
		b.WriteByte(':')
		b.WriteString(id)
	}
	return b.String()
}
