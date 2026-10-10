package ursus_test

// Step 171: Struct().WithFields sets fields of a struct: replacing those an
// expression names, in place, and adding the rest after; keeping the struct's own
// validity.

import (
	"strings"
	"testing"

	"github.com/advenn/ursus"
)

func TestWithFields(t *testing.T) {
	c := ursus.Col
	frame := ursus.Frame(
		ursus.ValuesNullable("age", []int64{30, 40, 0}, []bool{true, true, false}),
		ursus.Values("name", []string{"ann", "bo", "cy"}),
		ursus.Values("city", []string{"x", "y", "z"}),
	)
	// p is a struct of age and name; the third row's age is null. A null struct is
	// the kernel test's: a conditional cannot make one.
	people := frame.Select(ursus.Struct(c("age"), c("name")).Alias("p"), c("city"))
	p := c("p").Struct()
	out, err := people.Select(
		p.WithFields(p.Field("age").Add(int64(1)).Alias("age"), c("city"), ursus.Lit("fixed").Alias("tag")).Alias("p"),
	).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// age replaced in place, name kept, city and tag added after.
	sch := out.Schema().String()
	for _, want := range []string{"age", "name", "city", "tag"} {
		if !strings.Contains(sch, want) {
			t.Fatalf("schema %s has no field %s", sch, want)
		}
	}
	if i, j := strings.Index(sch, "age"), strings.Index(sch, "name"); i > j {
		t.Fatalf("age moved after name: %s", sch)
	}
	fields, err := out.Lazy().Select(
		c("p").Struct().Field("age").Alias("age"), c("p").Struct().Field("name").Alias("name"),
		c("p").Struct().Field("city").Alias("city"), c("p").Struct().Field("tag").Alias("tag"),
	).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"31|ann|x|fixed|",
		"41|bo|y|fixed|",
		"∅|cy|z|fixed|", // a null age, plus one, is null
	}
	if got := sortedRenderedRows(t, fields); strings.Join(got, "\n") != strings.Join(sortedStrings(want), "\n") {
		t.Fatalf("rows:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(sortedStrings(want), "\n"))
	}
}

func TestWithFieldsRefuses(t *testing.T) {
	c := ursus.Col
	frame := ursus.Frame(ursus.Values("a", []int64{1}), ursus.Values("b", []int64{2}))
	for name, e := range map[string]ursus.Expr{
		"not a struct": c("a").Struct().WithFields(c("b")),
		"one name twice": ursus.Struct(c("a")).Alias("s").Struct().WithFields(
			c("b").Alias("x"), c("a").Alias("x")),
	} {
		if _, err := frame.Select(e).Collect(t.Context()); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// sortedStrings is ss sorted, a copy.
func sortedStrings(ss []string) []string {
	out := append([]string(nil), ss...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// TestWithFieldsNamesAFieldAsPlanned: a Field read is named after its struct in the
// plan, p, though its kernel may name the column after the field. WithFields sets the
// field the plan names, a new p, and leaves name as it was.
func TestWithFieldsNamesAFieldAsPlanned(t *testing.T) {
	c := ursus.Col
	frame := ursus.Frame(ursus.Values("age", []int64{30, 40}), ursus.Values("name", []string{"ann", "bo"}))
	p := c("p").Struct()
	out, err := frame.Select(ursus.Struct(c("age"), c("name")).Alias("p")).
		Select(p.WithFields(p.Field("name")).Alias("q")).
		Select(c("q").Struct().Field("p").Alias("copy"), c("q").Struct().Field("name").Alias("name")).
		Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := sortedRenderedRows(t, out); strings.Join(got, ",") != "ann|ann|,bo|bo|" {
		t.Fatalf("rows %v", got)
	}
}
