package ursus_test

// A Struct column read from Parquet.
//
// The risk is one row: a struct that is PRESENT and whose fields are all null. It
// holds exactly what a null struct holds — nothing — and differs only in the
// struct's own validity bit, which is carried by the definition levels and cannot be
// recovered once they are consumed. Every fixture here contains that row.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/schema"

	"github.com/advenn/ursus"
	"github.com/advenn/ursus/internal/uerr"
)

// person is one row of the struct column: an age, a city, and whether the STRUCT
// itself is present. A field is absent when its pointer is nil.
type person struct {
	age  *int64
	city *string
	ok   bool
}

func age(v int64) *int64    { return &v }
func city(s string) *string { return &s }

// writeStructFile builds `person: Struct(age: Int64, city: String)` followed by
// `id: int64`, with the definition levels written by hand.
//
// The struct comes FIRST deliberately. Every column readable before this step
// consumed exactly one Parquet leaf, so a schema field index and a file leaf index
// happened to agree; a two-field struct ends that, and `id` is at field 1 but leaf 2.
// A reader that conflates them reads the wrong column under the right name.
//
//	def 2  the field is present
//	def 1  the field is null but the STRUCT is present
//	def 0  the struct is null
func writeStructFile(t *testing.T, rows []person, ids []int64) string {
	t.Helper()
	opt, req := parquet.Repetitions.Optional, parquet.Repetitions.Required

	ageNode, err := schema.NewPrimitiveNodeLogical("age", opt,
		schema.NewIntLogicalType(64, true), parquet.Types.Int64, -1, -1)
	if err != nil {
		t.Fatal(err)
	}
	cityNode, err := schema.NewPrimitiveNodeLogical("city", opt,
		schema.StringLogicalType{}, parquet.Types.ByteArray, -1, -1)
	if err != nil {
		t.Fatal(err)
	}
	personNode, err := schema.NewGroupNode("person", opt,
		schema.FieldList{ageNode, cityNode}, -1)
	if err != nil {
		t.Fatal(err)
	}
	idNode, err := schema.NewPrimitiveNodeLogical("id", req,
		schema.NewIntLogicalType(64, true), parquet.Types.Int64, -1, -1)
	if err != nil {
		t.Fatal(err)
	}
	root, err := schema.NewGroupNode("schema", req,
		schema.FieldList{personNode, idNode}, -1)
	if err != nil {
		t.Fatal(err)
	}

	var ages []int64
	var cities []parquet.ByteArray
	var ageDefs, cityDefs []int16
	for _, r := range rows {
		switch {
		case !r.ok:
			ageDefs, cityDefs = append(ageDefs, 0), append(cityDefs, 0)
		default:
			if r.age != nil {
				ages = append(ages, *r.age)
				ageDefs = append(ageDefs, 2)
			} else {
				ageDefs = append(ageDefs, 1)
			}
			if r.city != nil {
				cities = append(cities, parquet.ByteArray(*r.city))
				cityDefs = append(cityDefs, 2)
			} else {
				cityDefs = append(cityDefs, 1)
			}
		}
	}

	path := filepath.Join(t.TempDir(), "structs.parquet")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := file.NewParquetWriter(f, root)
	rg := w.AppendRowGroup()

	cw, err := rg.NextColumn()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cw.(*file.Int64ColumnChunkWriter).WriteBatch(ages, ageDefs, nil); err != nil {
		t.Fatal(err)
	}
	if err := cw.Close(); err != nil {
		t.Fatal(err)
	}

	cw, err = rg.NextColumn()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cw.(*file.ByteArrayColumnChunkWriter).WriteBatch(cities, cityDefs, nil); err != nil {
		t.Fatal(err)
	}
	if err := cw.Close(); err != nil {
		t.Fatal(err)
	}

	cw, err = rg.NextColumn()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cw.(*file.Int64ColumnChunkWriter).WriteBatch(ids, nil, nil); err != nil {
		t.Fatal(err)
	}
	// w.Close() closes the file too.
	for _, c := range []interface{ Close() error }{cw, rg, w} {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// structFixture:
//
//	row 0  {age: 30,   city: "NY"}    both present
//	row 1  {age: null, city: "SF"}    one field null, struct present
//	row 2  {age: null, city: null}    struct PRESENT, every field null
//	row 3  null                       the struct itself is absent
func structFixture(t *testing.T) string {
	t.Helper()
	return writeStructFile(t, []person{
		{age: age(30), city: city("NY"), ok: true},
		{city: city("SF"), ok: true},
		{ok: true},
		{},
	}, []int64{1, 2, 3, 4})
}

// TestStructReadsThroughAQuery is the whole point: scan, collect, and see it.
func TestStructReadsThroughAQuery(t *testing.T) {
	df, err := ursus.ScanParquet(structFixture(t)).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatalf("a file with a readable struct column must collect: %v", err)
	}
	if df.Height() != 4 || df.Width() != 2 {
		t.Fatalf("got %dx%d, want 4x2\n%s", df.Height(), df.Width(), df)
	}
	if got := df.Schema().Field(0).Type.String(); got != "Struct(age: Int64, city: String)" {
		t.Errorf("person is %s\n%s", got, df)
	}
	// The column after the struct is the one a leaf/field index conflation gets
	// wrong: id is schema field 1 but file leaf 2.
	for i, want := range []int64{1, 2, 3, 4} {
		v, ok, err := df.At[int64](i, "id")
		if err != nil {
			t.Fatal(err)
		}
		if !ok || v != want {
			t.Errorf("id row %d = (%d, valid %v), want %d\n%s", i, v, ok, want, df)
		}
	}
	t.Logf("\n%s", df)
}

// TestStructSemantics is the table from the package doc, every cell.
//
// Rows 2 and 3 are the pair that matters: they hold identical field values and
// differ only in the struct's validity. Field access answers null for both — there
// is no age either way — so the distinction has to be checked on the struct itself.
func TestStructSemantics(t *testing.T) {
	path := structFixture(t)

	for _, size := range []int{1, 2, 3, 8192} {
		df, err := ursus.ScanParquet(path).Select(
			ursus.Col("person"),
			ursus.Col("person").Struct().Field("age").Alias("age"),
			ursus.Col("person").Struct().Field("city").Alias("city"),
			ursus.Col("person").IsNull().Alias("absent"),
		).Collect(t.Context(), ursus.WithBatchSize(size), ursus.WithVerify())
		if err != nil {
			t.Fatalf("batch size %d: %v", size, err)
		}

		// The struct's own validity. Row 2 is PRESENT with every field null; row 3
		// is absent. Deriving this from the fields would make them the same row.
		col, ok := df.Batch().ByName("person")
		if !ok {
			t.Fatalf("no person column\n%s", df)
		}
		for i, want := range []bool{true, true, true, false} {
			if got := col.IsValid(i); got != want {
				t.Errorf("batch %d row %d: person valid = %v, want %v — a null struct "+
					"and a struct of nulls are NOT the same\n%s", size, i, got, want, df)
			}
		}

		// IsNull is how the distinction is reachable from an expression: row 2 holds
		// nothing and is NOT null, row 3 is.
		for i, want := range []bool{false, false, false, true} {
			v, ok, err := df.At[bool](i, "absent")
			if err != nil {
				t.Fatal(err)
			}
			if !ok || v != want {
				t.Errorf("batch %d row %d: person.IsNull() = (%v, valid %v), want %v\n%s",
					size, i, v, ok, want, df)
			}
		}

		for i, w := range []struct {
			v  int64
			ok bool
		}{{30, true}, {0, false}, {0, false}, {0, false}} {
			v, ok, err := df.At[int64](i, "age")
			if err != nil {
				t.Fatal(err)
			}
			if ok != w.ok || (ok && v != w.v) {
				t.Errorf("batch %d row %d: age = (%d, valid %v), want (%d, valid %v)\n%s",
					size, i, v, ok, w.v, w.ok, df)
			}
		}
		for i, w := range []struct {
			v  string
			ok bool
		}{{"NY", true}, {"SF", true}, {"", false}, {"", false}} {
			v, ok, err := df.At[string](i, "city")
			if err != nil {
				t.Fatal(err)
			}
			if ok != w.ok || (ok && v != w.v) {
				t.Errorf("batch %d row %d: city = (%q, valid %v), want (%q, valid %v)\n%s",
					size, i, v, ok, w.v, w.ok, df)
			}
		}
	}
}

// TestStructRenders: the two rows that differ only in validity must LOOK different,
// which is the cheapest place the distinction can be seen breaking.
func TestStructRenders(t *testing.T) {
	df, err := ursus.ScanParquet(structFixture(t)).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	out := df.String()
	for _, want := range []string{
		`{age: 30, city: "NY"}`,
		`{age: null, city: null}`, // present, holding nulls
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the frame should render %s:\n%s", want, out)
		}
	}
	// And the null struct is not rendered as a struct at all.
	if strings.Count(out, "{") != 3 {
		t.Errorf("expected 3 rendered structs and one bare null:\n%s", out)
	}
}

// TestStructSurvivesTakeAndFilter: a filter gathers, and every field has to be
// gathered by the SAME selection or the fields end up describing different rows.
func TestStructSurvivesTakeAndFilter(t *testing.T) {
	path := writeStructFile(t, []person{
		{age: age(10), city: city("a"), ok: true},
		{age: age(20), city: city("b"), ok: true},
		{age: age(30), city: city("c"), ok: true},
		{ok: true}, // present, all null
		{},         // absent
	}, []int64{1, 2, 3, 4, 5})

	for _, size := range []int{1, 2, 8192} {
		df, err := ursus.ScanParquet(path).
			Filter(ursus.Col("id").Gt(int64(1))).
			Collect(t.Context(), ursus.WithBatchSize(size), ursus.WithVerify())
		if err != nil {
			t.Fatalf("batch size %d: %v", size, err)
		}
		if df.Height() != 4 {
			t.Fatalf("batch %d: %d rows, want 4\n%s", size, df.Height(), df)
		}
		col, _ := df.Batch().ByName("person")
		for i, want := range []bool{true, true, true, false} {
			if got := col.IsValid(i); got != want {
				t.Errorf("batch %d row %d: person valid = %v, want %v\n%s",
					size, i, got, want, df)
			}
		}

		// BOTH fields, read out of the gathered struct. Checking only one would pass
		// with the other field gathered by a different selection, which is the way a
		// struct goes wrong: the row is the right length and the fields describe
		// different rows.
		got, err := ursus.ScanParquet(path).
			Filter(ursus.Col("id").Gt(int64(1))).
			Select(
				ursus.Col("person").Struct().Field("age").Alias("age"),
				ursus.Col("person").Struct().Field("city").Alias("city"),
			).
			Collect(t.Context(), ursus.WithBatchSize(size), ursus.WithVerify())
		if err != nil {
			t.Fatal(err)
		}
		for i, w := range []struct {
			age  int64
			city string
			ok   bool
		}{{20, "b", true}, {30, "c", true}, {0, "", false}, {0, "", false}} {
			a, aok, err := got.At[int64](i, "age")
			if err != nil {
				t.Fatal(err)
			}
			if aok != w.ok || (aok && a != w.age) {
				t.Errorf("batch %d row %d: age = (%d, valid %v), want (%d, valid %v)\n%s",
					size, i, a, aok, w.age, w.ok, got)
			}
			c, cok, err := got.At[string](i, "city")
			if err != nil {
				t.Fatal(err)
			}
			if cok != w.ok || (cok && c != w.city) {
				t.Errorf("batch %d row %d: city = (%q, valid %v), want (%q, valid %v)\n%s",
					size, i, c, cok, w.city, w.ok, got)
			}
		}
	}
}

// TestStructFieldIsAnOrdinaryColumn: once a field is out, everything applies to it.
func TestStructFieldIsAnOrdinaryColumn(t *testing.T) {
	path := writeStructFile(t, []person{
		{age: age(30), city: city("NY"), ok: true},
		{age: age(40), city: city("NY"), ok: true},
		{age: age(50), city: city("SF"), ok: true},
	}, []int64{1, 2, 3})

	df, err := ursus.ScanParquet(path).
		GroupBy(ursus.Col("person").Struct().Field("city").Alias("city")).
		Agg(ursus.Col("person").Struct().Field("age").Alias("age").Sum().Alias("total")).
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
	}{{"NY", 70}, {"SF", 50}} {
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

// TestStructProjectionSkipsTheStruct is step 28's guarantee, still holding: a column
// this query does not want is never opened, so its leaves are never decompressed.
func TestStructProjectionSkipsTheStruct(t *testing.T) {
	df, err := ursus.ScanParquet(structFixture(t)).
		Select(ursus.Col("id")).Collect(t.Context(), ursus.WithVerify())
	if err != nil {
		t.Fatal(err)
	}
	if df.Width() != 1 || df.Height() != 4 {
		t.Fatalf("got %dx%d, want 4x1\n%s", df.Height(), df.Width(), df)
	}
}

// TestStructRefusals: what is still not readable, and what is not askable.
func TestStructRefusals(t *testing.T) {
	path := structFixture(t)

	// A field the struct does not have — refused while PLANNING, with the names.
	_, err := ursus.ScanParquet(path).
		Select(ursus.Col("person").Struct().Field("nope")).Collect(t.Context())
	if err == nil {
		t.Fatal("an unknown field must be refused")
	}
	if !errors.Is(err, uerr.ErrSchema) {
		t.Errorf("kind should be Schema: %v", err)
	}
	if !strings.Contains(err.Error(), "age") || !strings.Contains(err.Error(), "city") {
		t.Errorf("the message should list the fields that do exist: %v", err)
	}

	// `.struct` on something that is not a struct.
	_, err = ursus.ScanParquet(path).
		Select(ursus.Col("id").Struct().Field("age")).Collect(t.Context())
	if err == nil {
		t.Fatal("`.struct` on a non-Struct column must be refused")
	}
	if !errors.Is(err, uerr.ErrType) {
		t.Errorf("kind should be Type: %v", err)
	}
}
