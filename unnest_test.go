package ursus_test

// Unnest replaces a struct column with its fields.
//
// Two things are easy to get wrong and neither errors: putting the fields in the
// wrong PLACE (appending them instead of splicing them in), and letting a field of
// an ABSENT struct carry a value. The fixture has a column after the struct for the
// first, and the second is checked in internal/physical where an inconsistent
// column can be built by hand.

import (
	"errors"
	"strings"
	"testing"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/uerr"
)

// TestUnnestReplacesInPlace is the headline, and the column ORDER is half of it.
func TestUnnestReplacesInPlace(t *testing.T) {
	path := structFixture(t) // person: Struct(age, city), then id

	for _, size := range []int{1, 2, 3, 8192} {
		df, err := ursus.ScanParquet(path).Unnest("person").
			Collect(t.Context(), ursus.WithBatchSize(size), ursus.WithVerify())
		if err != nil {
			t.Fatalf("batch size %d: %v", size, err)
		}

		// Positions, not just names: appending the fields instead of splicing them
		// would give {id, age, city} and every by-name check would still pass.
		if got := df.Columns(); strings.Join(got, ",") != "age,city,id" {
			t.Fatalf("batch %d: columns are %v, want [age city id] — the fields "+
				"replace the struct where it stood\n%s", size, got, df)
		}
		if df.Height() != 4 {
			t.Fatalf("batch %d: %d rows, want 4 — unnest does not change the height\n%s",
				size, df.Height(), df)
		}

		// row 0 {30,"NY"} | 1 {null,"SF"} | 2 {null,null} | 3 null
		for i, w := range []struct {
			age  int64
			ok   bool
			city string
			cok  bool
		}{
			{30, true, "NY", true},
			{0, false, "SF", true},
			{0, false, "", false}, // present struct, both fields null
			{0, false, "", false}, // absent struct — indistinguishable now
		} {
			v, ok, err := df.At[int64](i, "age")
			if err != nil {
				t.Fatal(err)
			}
			if ok != w.ok || (ok && v != w.age) {
				t.Errorf("batch %d row %d: age = (%d, valid %v), want (%d, valid %v)\n%s",
					size, i, v, ok, w.age, w.ok, df)
			}
			c, cok, err := df.At[string](i, "city")
			if err != nil {
				t.Fatal(err)
			}
			if cok != w.cok || (cok && c != w.city) {
				t.Errorf("batch %d row %d: city = (%q, valid %v), want (%q, valid %v)\n%s",
					size, i, c, cok, w.city, w.cok, df)
			}
		}
		// id must survive untouched, which is the other half of "in place".
		for i, want := range []int64{1, 2, 3, 4} {
			v, ok, err := df.At[int64](i, "id")
			if err != nil {
				t.Fatal(err)
			}
			if !ok || v != want {
				t.Errorf("batch %d row %d: id = (%d, valid %v), want %d\n%s",
					size, i, v, ok, want, df)
			}
		}
	}
}

// TestUnnestErasesTheNullDistinction states the consequence out loud: rows 2 and 3
// were distinguishable before and are not after. Explode does the same to a list.
func TestUnnestErasesTheNullDistinction(t *testing.T) {
	path := structFixture(t)

	// Before: IsNull separates them.
	before, err := ursus.ScanParquet(path).
		Select(ursus.Col("person").IsNull().Alias("absent")).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	r2, _, _ := before.At[bool](2, "absent")
	r3, _, _ := before.At[bool](3, "absent")
	if r2 || !r3 {
		t.Fatalf("before unnest, row 2 should be present and row 3 absent\n%s", before)
	}

	// After: both rows are all-null and nothing tells them apart.
	after, err := ursus.ScanParquet(path).Unnest("person").
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []int{2, 3} {
		if _, ok, _ := after.At[int64](row, "age"); ok {
			t.Errorf("row %d: age should be null\n%s", row, after)
		}
		if _, ok, _ := after.At[string](row, "city"); ok {
			t.Errorf("row %d: city should be null\n%s", row, after)
		}
	}
}

// TestUnnestFieldsAreOrdinaryColumns is the point of the operation: no namespace,
// no `.Struct().Field(...)`, just columns.
func TestUnnestFieldsAreOrdinaryColumns(t *testing.T) {
	path := writeStructFile(t, []person{
		{age: age(30), city: city("NY"), ok: true},
		{age: age(40), city: city("NY"), ok: true},
		{age: age(50), city: city("SF"), ok: true},
		{ok: true},
	}, []int64{1, 2, 3, 4})

	df, err := ursus.ScanParquet(path).
		Unnest("person").
		Filter(ursus.Col("age").Gt(int64(30))).
		GroupBy(ursus.Col("city")).
		Agg(ursus.Col("age").Sum().Alias("total")).
		Sort(ursus.Asc(ursus.Col("city"))).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if df.Height() != 2 {
		t.Fatalf("got %d groups, want 2\n%s", df.Height(), df)
	}
	for i, w := range []struct {
		city  string
		total int64
	}{{"NY", 40}, {"SF", 50}} {
		c, _, err := df.At[string](i, "city")
		if err != nil {
			t.Fatal(err)
		}
		v, _, err := df.At[ursus.Int128Value](i, "total")
		if err != nil {
			t.Fatal(err)
		}
		got, fits := v.Int64()
		if c != w.city || !fits || got != w.total {
			t.Errorf("row %d = (%q, %s), want (%q, %d)\n%s", i, c, v, w.city, w.total, df)
		}
	}
}

// TestUnnestUnderAProjection: the optimizer's projection rule does not recognise
// Unnest, and its conservative default has to be conservative in the right
// direction — pruning a column the fields come from would be silent.
func TestUnnestUnderAProjection(t *testing.T) {
	df, err := ursus.ScanParquet(structFixture(t)).
		Unnest("person").
		Select(ursus.Col("city"), ursus.Col("id")).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if got := df.Columns(); strings.Join(got, ",") != "city,id" {
		t.Fatalf("columns are %v, want [city id]\n%s", got, df)
	}
	if v, ok, _ := df.At[string](0, "city"); !ok || v != "NY" {
		t.Errorf("city row 0 = (%q, %v), want \"NY\"\n%s", v, ok, df)
	}

	// And a predicate above an Unnest must not be pushed into the scan, where the
	// column it names does not exist yet.
	filtered, err := ursus.ScanParquet(structFixture(t)).
		Unnest("person").
		Filter(ursus.Col("city").Eq("NY")).
		Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if filtered.Height() != 1 {
		t.Fatalf("got %d rows, want 1\n%s", filtered.Height(), filtered)
	}
}

// TestUnnestRefusals: everything it declines to guess at.
func TestUnnestRefusals(t *testing.T) {
	path := structFixture(t)

	for _, c := range []struct {
		what string
		lf   *ursus.LazyFrame
		kind error
		want string
	}{
		{"no columns", ursus.ScanParquet(path).Unnest(), uerr.ErrValue, "at least one"},
		{"unknown column", ursus.ScanParquet(path).Unnest("nope"), uerr.ErrSchema, "unknown column"},
		{"not a struct", ursus.ScanParquet(path).Unnest("id"), uerr.ErrType, "not a Struct"},
		{"named twice", ursus.ScanParquet(path).Unnest("person", "person"), uerr.ErrValue, "twice"},
	} {
		_, err := c.lf.Collect(t.Context())
		if err == nil {
			t.Errorf("%s must be refused", c.what)
			continue
		}
		if !errors.Is(err, c.kind) {
			t.Errorf("%s: wrong kind: %v", c.what, err)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: message should mention %q: %v", c.what, c.want, err)
		}
	}

	// A field colliding with a column already in the frame. The message must name
	// BOTH sides — dtype.NewSchema would also catch this, but it would tell the
	// caller to alias a column that does not exist yet.
	_, err := ursus.ScanParquet(path).
		WithColumns(ursus.Col("id").Alias("age")).
		Unnest("person").
		Collect(t.Context())
	if err == nil {
		t.Fatal("a field colliding with an existing column must be refused")
	}
	if !errors.Is(err, uerr.ErrSchema) {
		t.Errorf("kind should be Schema: %v", err)
	}
	for _, want := range []string{`"age"`, `"person"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message should name %s: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "positions") {
		t.Errorf("this is unnest's refusal, not NewSchema's generic one: %v", err)
	}

	// Unnest is not an expression, and says which rule it broke.
	_, err = ursus.ScanParquet(path).Select(ursus.Col("person").Unnest()).
		Collect(t.Context())
	if err == nil {
		t.Fatal("`Col(...).Unnest()` must be refused")
	}
	if !strings.Contains(err.Error(), "one column per struct field") {
		t.Errorf("the message should say WHY — width, not height: %v", err)
	}
}
