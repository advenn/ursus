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

// list.to_struct with its fields named and struct.rename_fields (step 224): every
// answer is Polars 2.1.0's, recorded in the liststruct group of testdata/oracle and
// spelled in oracle_liststruct_test.go since step 499, which took this file's hand
// frames. What stays reads the lists in batches of one, a differential, and asserts
// the refusals.

// TestAListsToStructReadsSlicedLists: to_struct read in batches of one, whose lists
// are slices of their children, answers as in one batch; no names, and one name
// twice, are refused naming to_struct.
func TestAListsToStructReadsSlicedLists(t *testing.T) {
	n := (*int64)(nil)
	a := ints("a", [][]*int64{{iv(1), iv(2), iv(3)}, {iv(4)}, {}, nil, {n, iv(5)}})
	q := frameOf(t, a).Select(ursus.Col("a").List().ToStruct("x", "y"))
	whole, err := q.Collect(t.Context(), ursus.WithBatchSize(5))
	if err != nil {
		t.Fatal(err)
	}
	got, err := q.Collect(t.Context(), ursus.WithBatchSize(1))
	if err != nil {
		t.Fatal(err)
	}
	ursustest.AssertFrameEqual(t, got, whole)

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

// TestAStructsRenameFieldsRefuses: struct.rename_fields (step 224) with too few
// names or too many, which Polars warns 2.0 will refuse, and one name twice, are
// refused naming rename_fields; the renamed field reads back under its new name.
func TestAStructsRenameFieldsRefuses(t *testing.T) {
	s := data.NewStruct("s", []*data.Column{
		data.NewFixed("a", dtype.Int64, []int64{1, 0, 0}, validOf([]bool{true, false, false})),
		data.NewString("b", []string{"x", "", `q"z`}, validOf([]bool{true, false, true})),
	}, validOf([]bool{true, false, true}))
	got, err := frameOf(t, s).Select(ursus.Col("s").Struct().RenameFields("p", "q")).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if f, err := got.Select(t.Context(), ursus.Col("s").Struct().Field("q")); err != nil || f.Height() != 3 {
		t.Errorf("the renamed field reads back: %v", err)
	}

	for name, e := range map[string]ursus.Expr{
		"too few":        ursus.Col("s").Struct().RenameFields("p"),
		"too many":       ursus.Col("s").Struct().RenameFields("p", "q", "r"),
		"one name twice": ursus.Col("s").Struct().RenameFields("p", "p"),
	} {
		_, err := frameOf(t, s).Select(e).Collect(t.Context())
		if !errors.Is(err, uerr.ErrSchema) || !strings.Contains(err.Error(), "rename_fields") {
			t.Errorf("%s should be refused, naming rename_fields: %v", name, err)
		}
	}
}
