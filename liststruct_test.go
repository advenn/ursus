package ursus_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/uerr"
	"github.com/advenn/ursus/ursustest"
)

// TestAListsToStructAnswersAsPolars: list.to_struct with its fields named (step
// 224), against Polars 1.44.1: field i is element i, null where the list is shorter,
// the elements past the last field dropped, a null element a null field. A null list
// is a null struct, where Polars gives a struct of nulls. Read in batches of one too,
// whose lists are slices of their children.
func TestAListsToStructAnswersAsPolars(t *testing.T) {
	n := (*int64)(nil)
	a := ints("a", [][]*int64{{iv(1), iv(2), iv(3)}, {iv(4)}, {}, nil, {n, iv(5)}})
	want, err := frameOf(t, data.NewStruct("a", []*data.Column{
		data.NewFixed("x", dtype.Int64, []int64{1, 4, 0, 0, 0}, validOf([]bool{true, true, false, false, false})),
		data.NewFixed("y", dtype.Int64, []int64{2, 0, 0, 0, 5}, validOf([]bool{true, false, false, false, true})),
	}, validOf([]bool{true, true, true, false, true}))).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	q := frameOf(t, a).Select(ursus.Col("a").List().ToStruct("x", "y"))
	for _, size := range []int{1, 5} {
		got, err := q.Collect(t.Context(), ursus.WithBatchSize(size))
		if err != nil {
			t.Fatal(err)
		}
		ursustest.AssertFrameEqual(t, got, want)
	}

	for name, e := range map[string]ursus.Expr{
		"no names":       ursus.Col("a").List().ToStruct(),
		"one name twice": ursus.Col("a").List().ToStruct("x", "x"),
	} {
		_, err := frameOf(t, a).Select(e).Collect(t.Context())
		if !errors.Is(err, uerr.ErrSchema) || !strings.Contains(err.Error(), "to_struct") {
			t.Errorf("%s should be refused, naming to_struct: %v", name, err)
		}
	}
}

// TestAStructsRenameFieldsAnswersAsPolars: struct.rename_fields (step 224), against
// Polars 1.44.1: each field under its new name, a null struct null, a null field
// null. Too few names or too many, which Polars warns 2.0 will refuse, and one name
// twice, are refused.
func TestAStructsRenameFieldsAnswersAsPolars(t *testing.T) {
	s := func(a, b string) *data.Column {
		return data.NewStruct("s", []*data.Column{
			data.NewFixed(a, dtype.Int64, []int64{1, 0, 0}, validOf([]bool{true, false, false})),
			data.NewString(b, []string{"x", "", `q"z`}, validOf([]bool{true, false, true})),
		}, validOf([]bool{true, false, true}))
	}
	got, err := frameOf(t, s("a", "b")).Select(ursus.Col("s").Struct().RenameFields("p", "q")).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want, err := frameOf(t, s("p", "q")).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, got, want)
	if f, err := got.Select(t.Context(), ursus.Col("s").Struct().Field("q")); err != nil || f.Height() != 3 {
		t.Errorf("the renamed field reads back: %v", err)
	}

	for name, e := range map[string]ursus.Expr{
		"too few":        ursus.Col("s").Struct().RenameFields("p"),
		"too many":       ursus.Col("s").Struct().RenameFields("p", "q", "r"),
		"one name twice": ursus.Col("s").Struct().RenameFields("p", "p"),
	} {
		_, err := frameOf(t, s("a", "b")).Select(e).Collect(t.Context())
		if !errors.Is(err, uerr.ErrSchema) || !strings.Contains(err.Error(), "rename_fields") {
			t.Errorf("%s should be refused, naming rename_fields: %v", name, err)
		}
	}
}
