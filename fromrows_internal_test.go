package ursus

import (
	"reflect"
	"testing"
)

// dag32 holds no cycle but reaches each type below it by many paths: thirty-two
// levels, each of two fields holding the level below, 2^32 paths to dag0.
type dag0 struct{ V int64 }

type dag1 struct {
	A *dag0
	B []dag0
}

type dag2 struct {
	A *dag1
	B []dag1
}

type dag3 struct {
	A *dag2
	B []dag2
}

type dag4 struct {
	A *dag3
	B []dag3
}

type dag5 struct {
	A *dag4
	B []dag4
}

type dag6 struct {
	A *dag5
	B []dag5
}

type dag7 struct {
	A *dag6
	B []dag6
}

type dag8 struct {
	A *dag7
	B []dag7
}

type dag9 struct {
	A *dag8
	B []dag8
}

type dag10 struct {
	A *dag9
	B []dag9
}

type dag11 struct {
	A *dag10
	B []dag10
}

type dag12 struct {
	A *dag11
	B []dag11
}

type dag13 struct {
	A *dag12
	B []dag12
}

type dag14 struct {
	A *dag13
	B []dag13
}

type dag15 struct {
	A *dag14
	B []dag14
}

type dag16 struct {
	A *dag15
	B []dag15
}

type dag17 struct {
	A *dag16
	B []dag16
}

type dag18 struct {
	A *dag17
	B []dag17
}

type dag19 struct {
	A *dag18
	B []dag18
}

type dag20 struct {
	A *dag19
	B []dag19
}

type dag21 struct {
	A *dag20
	B []dag20
}

type dag22 struct {
	A *dag21
	B []dag21
}

type dag23 struct {
	A *dag22
	B []dag22
}

type dag24 struct {
	A *dag23
	B []dag23
}

type dag25 struct {
	A *dag24
	B []dag24
}

type dag26 struct {
	A *dag25
	B []dag25
}

type dag27 struct {
	A *dag26
	B []dag26
}

type dag28 struct {
	A *dag27
	B []dag27
}

type dag29 struct {
	A *dag28
	B []dag28
}

type dag30 struct {
	A *dag29
	B []dag29
}

type dag31 struct {
	A *dag30
	B []dag30
}

type dag32 struct {
	A *dag31
	B []dag31
}

// TestRecursiveFieldWalksEachTypeOnce: a type that holds no cycle is walked once a
// type, not once a path, so dag32's 2^32 paths take no time.
func TestRecursiveFieldWalksEachTypeOnce(t *testing.T) {
	if field := recursiveField(reflect.TypeFor[dag32]()); field != "" {
		t.Errorf("a type with no cycle: got %q", field)
	}
}
