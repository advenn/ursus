package parquet

// A Parquet file with one nested column used to be unopenable in its entirety.
//
// fileSchema refused the whole file if any column was unsupported, and the reason
// was sound — dropping a column silently would hand back a frame missing data the
// user asked for. But nested columns are ordinary in real Parquet (anything
// JSON-derived, any event stream with a tags array), so the practical effect was
// that such a file had no readable columns at all and no workaround.
//
// These pin the narrowed contract: the column is VISIBLE in the schema with the
// type it actually has, reading it is refused with the message it always
// produced, and reading around it works.

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/schema"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/data"
	"github.com/advenn/ursus/internal/source"
	"github.com/advenn/ursus/internal/uerr"
)

// writeNested builds the fixture with the LOW-LEVEL writer and hand-written
// definition and repetition levels.
//
// pqarrow would be four lines instead of forty, and it was the first attempt. It
// drags grpc, protobuf, x/net and x/text into the ROOT module's dependency graph
// — for a test fixture, in a library that otherwise depends on arrow-go and
// nothing else. Not worth it.
//
// Writing the levels by hand also documents the encoding a future step has to
// DECODE, which is the hard part of reading nested Parquet:
//
//	tags: optional group (LIST) { repeated group list { optional int64 element } }
//	  max def 3, max rep 1
//	  [10,11] -> (def 3, rep 0), (def 3, rep 1)
//	  []      -> (def 2, rep 0)      <- present but empty: def stops one short
//	  [12]    -> (def 3, rep 0)
func writeNested(t *testing.T) string {
	t.Helper()

	req, opt := parquet.Repetitions.Required, parquet.Repetitions.Optional
	i64 := func(name string, rep parquet.Repetition) schema.Node {
		n, err := schema.NewPrimitiveNodeLogical(name, rep,
			schema.NewIntLogicalType(64, true), parquet.Types.Int64, -1, -1)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	str := func(name string, rep parquet.Repetition) schema.Node {
		n, err := schema.NewPrimitiveNodeLogical(name, rep,
			schema.StringLogicalType{}, parquet.Types.ByteArray, -1, -1)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	tags, err := schema.ListOf(i64("element", opt), opt, -1)
	if err != nil {
		t.Fatal(err)
	}
	tags, err = schema.NewGroupNodeLogical("tags", opt,
		schema.FieldList{tags.Field(0)}, schema.NewListLogicalType(), -1)
	if err != nil {
		t.Fatal(err)
	}
	user, err := schema.NewGroupNode("user", opt, schema.FieldList{
		i64("age", req), str("city", opt),
	}, -1)
	if err != nil {
		t.Fatal(err)
	}
	// meta is a struct whose field is itself a group, which is what keeps this file
	// containing something unreadable now that `user` can be read. Step 29 removed
	// `tags` from that role and step 34 removed `user`; the guarantee being defended
	// — a column that cannot be read is refused rather than silently dropped — has
	// outlived both.
	inner, err := schema.NewGroupNode("inner", opt, schema.FieldList{i64("x", opt)}, -1)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := schema.NewGroupNode("meta", opt, schema.FieldList{inner}, -1)
	if err != nil {
		t.Fatal(err)
	}
	root, err := schema.NewGroupNode("schema", req, schema.FieldList{
		i64("id", req), str("name", opt), tags, user, meta,
	}, -1)
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "nested.parquet")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := file.NewParquetWriter(f, root)
	rg := w.AppendRowGroup()

	write := func(fn func(w file.ColumnChunkWriter)) {
		cw, err := rg.NextColumn()
		if err != nil {
			t.Fatal(err)
		}
		fn(cw)
		if err := cw.Close(); err != nil {
			t.Fatal(err)
		}
	}

	write(func(cw file.ColumnChunkWriter) { // id
		_, err := cw.(*file.Int64ColumnChunkWriter).WriteBatch(
			[]int64{1, 2, 3}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
	})
	write(func(cw file.ColumnChunkWriter) { // name
		_, err := cw.(*file.ByteArrayColumnChunkWriter).WriteBatch(
			[]parquet.ByteArray{[]byte("a"), []byte("b"), []byte("c")},
			[]int16{1, 1, 1}, nil)
		if err != nil {
			t.Fatal(err)
		}
	})
	write(func(cw file.ColumnChunkWriter) { // tags.list.element
		_, err := cw.(*file.Int64ColumnChunkWriter).WriteBatch(
			[]int64{10, 11, 12},
			[]int16{3, 3, 2, 3}, // the empty list is def 2
			[]int16{0, 1, 0, 0},
		)
		if err != nil {
			t.Fatal(err)
		}
	})
	write(func(cw file.ColumnChunkWriter) { // user.age
		_, err := cw.(*file.Int64ColumnChunkWriter).WriteBatch(
			[]int64{20, 21, 22}, []int16{1, 1, 1}, nil)
		if err != nil {
			t.Fatal(err)
		}
	})
	write(func(cw file.ColumnChunkWriter) { // user.city
		_, err := cw.(*file.ByteArrayColumnChunkWriter).WriteBatch(
			[]parquet.ByteArray{[]byte("lon"), []byte("nyc"), []byte("tok")},
			[]int16{2, 2, 2}, nil)
		if err != nil {
			t.Fatal(err)
		}
	})
	write(func(cw file.ColumnChunkWriter) { // meta.inner.x, def 3 = all present
		_, err := cw.(*file.Int64ColumnChunkWriter).WriteBatch(
			[]int64{30, 31, 32}, []int16{3, 3, 3}, nil)
		if err != nil {
			t.Fatal(err)
		}
	})

	if err := rg.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func openSource(t *testing.T, path string) *Source {
	t.Helper()
	return New([]Opener{func() (parquet.ReaderAtSeeker, io.Closer, error) {
		f, err := os.Open(path)
		return f, f, err
	}}, path, Options{})
}

// TestNestedFileOpensAndNamesItsColumns is the headline: the file is readable at
// all, and the nested columns say what they are instead of being hidden.
func TestNestedFileOpensAndNamesItsColumns(t *testing.T) {
	s := openSource(t, writeNested(t))

	sch, err := s.Schema(t.Context())
	if err != nil {
		t.Fatalf("a file with nested columns must still open: %v", err)
	}

	// One field per TOP-LEVEL field, not per leaf. `user` alone contributes two
	// leaves, so a schema built by iterating leaves would have five fields here
	// and would name them "age" and "city".
	if sch.Len() != 5 {
		t.Fatalf("got %d fields, want 5 (one per top-level field)\n%s", sch.Len(), sch)
	}

	want := map[string]dtype.DataType{
		"id":   dtype.Int64,
		"name": dtype.String,
		"tags": dtype.List(dtype.Int64),
		"user": dtype.Struct(
			dtype.Field{Name: "age", Type: dtype.Int64},
			dtype.Field{Name: "city", Type: dtype.String, Nullable: true},
		),
	}
	for name, wantType := range want {
		i := sch.IndexOf(name)
		if i < 0 {
			t.Errorf("column %q is missing from the schema", name)
			continue
		}
		if got := sch.Field(i).Type; got != wantType {
			t.Errorf("column %q typed as %s, want %s", name, got, wantType)
		}
	}
}

// TestNestedFileReadsItsFlatColumns is the point of the whole step.
func TestNestedFileReadsItsFlatColumns(t *testing.T) {
	s := openSource(t, writeNested(t))

	proj, err := dtype.NewSchema(
		dtype.Field{Name: "id", Type: dtype.Int64},
		dtype.Field{Name: "name", Type: dtype.String, Nullable: true},
	)
	if err != nil {
		t.Fatal(err)
	}

	bs, err := s.Open(t.Context(), source.ScanSpec{Projection: proj.Names()})
	if err != nil {
		t.Fatalf("reading the flat columns of a nested file must work: %v", err)
	}
	defer bs.Close()

	batch, err := bs.Next(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if batch.Rows() != 3 {
		t.Fatalf("got %d rows, want 3", batch.Rows())
	}
	// The values matter: `user` sits between `name` and the end of the file, so a
	// field-index-as-leaf-index slip reads the wrong chunk rather than failing.
	ids, err := data.Values[int64](batch.Columns()[0])
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []int64{1, 2, 3} {
		if ids[i] != want {
			t.Errorf("id[%d] = %d, want %d — a wrong leaf index reads another column",
				i, ids[i], want)
		}
	}
}

// TestNestedColumnIsRefusedNotHidden: asking for one that still cannot be read
// fails, and fails the way it always did. This is what keeps "nothing is silently
// missing" true.
//
// The list has been shrinking as the arc progressed: `tags` left it in step 29 when
// a List of fixed-width elements became readable, and `user` left it in step 34 when
// a Struct of flat fields did. `meta` is a struct of a struct — the reader's leaf
// readers have no level machine for that — and it is what keeps this test honest.
func TestNestedColumnIsRefusedNotHidden(t *testing.T) {
	for _, col := range []string{"meta"} {
		t.Run(col, func(t *testing.T) {
			s := openSource(t, writeNested(t))
			sch, err := s.Schema(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			proj, err := sch.Select([]string{"id", col})
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.Open(t.Context(), source.ScanSpec{Projection: proj.Names()})
			if err == nil {
				t.Fatalf("selecting %q must be refused, not silently read", col)
			}
			if !errors.Is(err, uerr.ErrUnsupported) {
				t.Errorf("kind should be Unsupported: %v", err)
			}
			if !strings.Contains(err.Error(), "not supported") {
				t.Errorf("the message should be the one it always produced: %v", err)
			}
		})
	}
}

// TestSelectAllOfNestedFileStillRefuses is the case the original design existed
// to prevent. A nil projection means every column, so it must fail — otherwise a
// user would get a frame quietly missing two of its four columns.
func TestSelectAllOfNestedFileStillRefuses(t *testing.T) {
	s := openSource(t, writeNested(t))
	if _, err := s.Open(t.Context(), source.ScanSpec{}); err == nil {
		t.Fatal("select-all over a file with nested columns must be refused")
	}
}
