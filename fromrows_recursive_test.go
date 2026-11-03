package ursus_test

// Step 413: FromRows of a recursive struct type is refused, naming the field, where
// its walk of the type followed it forever and overflowed the goroutine's stack, a
// fatal error no recover catches.

import (
	"errors"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

type recNode struct {
	V    int64
	Next *recNode
}

type recTree struct {
	V    int64
	Kids []recTree
}

type recA struct {
	V int64
	B *recB
}

type recB struct {
	As []recA
}

type recRing struct {
	V int64
	R *[2]recRing
}

// recSkipped is recursive only through a field FromRows leaves out.
type recSkipped struct {
	V    int64
	Next *recSkipped `ursus:"-"`
	next *recSkipped
}

// TestFromRowsRefusesARecursiveType: a linked list, a tree, two types that hold each
// other and a pointer to an array of the type itself each overflowed a 64 MB stack,
// however few rows, a nil pointer's zero value walked again; each is now an ErrType
// naming the field that refers back. MapRows builds its answers so, and refuses them
// too. A type recursive only through fields left out reads.
func TestFromRowsRefusesARecursiveType(t *testing.T) {
	defer debug.SetMaxStack(debug.SetMaxStack(64 << 20))
	cases := []struct {
		name  string
		lf    *ursus.LazyFrame
		field string
	}{
		{"a linked list", ursus.FromRows([]recNode{{1, nil}}), "recNode.Next"},
		{"a tree", ursus.FromRows([]recTree{{1, nil}}), "recTree.Kids"},
		{"two types holding each other", ursus.FromRows([]recA{{1, nil}}), "recB.As"},
		{"a pointer to an array of itself", ursus.FromRows([]recRing{{1, nil}}), "recRing.R"},
		{"no rows", ursus.FromRows([]recNode{}), "recNode.Next"},
	}
	for _, c := range cases {
		_, err := c.lf.Collect(t.Context())
		if err == nil || !errors.Is(err, ursus.ErrType) || !strings.Contains(err.Error(), c.field) ||
			!strings.Contains(err.Error(), "recursive") {
			t.Errorf("%s: got %v, want an ErrType naming %s as recursive", c.name, err, c.field)
		}
	}

	df, err := ursus.Frame(ursus.Values("V", []int64{1, 2})).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, err = df.MapRows(t.Context(), func(r struct{ V int64 }) (recNode, error) { return recNode{V: r.V}, nil })
	if err == nil || !errors.Is(err, ursus.ErrType) || !strings.Contains(err.Error(), "recursive") {
		t.Errorf("MapRows to a linked list: got %v, want an ErrType", err)
	}
	_, err = df.MapRows(t.Context(), func(r struct{ V int64 }) (*recNode, error) { return &recNode{V: r.V}, nil })
	if err == nil || !errors.Is(err, ursus.ErrType) || !strings.Contains(err.Error(), "recNode.Next") {
		t.Errorf("MapRows to a pointer to a linked list: got %v, want an ErrType naming recNode.Next", err)
	}

	got, err := ursus.FromRows([]recSkipped{{V: 7}}).Collect(t.Context())
	if err != nil || got.Width() != 1 || got.Height() != 1 {
		t.Errorf("a type recursive only through left-out fields: %v", err)
	}
}
