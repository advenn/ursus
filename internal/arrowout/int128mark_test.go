package arrowout_test

// Int128 is marked on its Arrow field, at every depth.
//
// Arrow has no 128-bit integer, so an Int128 exports as Decimal128(38, 0) — which is
// also exactly what a Decimal(38, 0) exports as. The two are the same Arrow type, and
// only arrowx.FieldTypeKey on the FIELD tells an import which one it is reading. A
// field exists at three places here — a schema's, a list's element, a struct's child
// — and each is built by a different arm, so each is checked separately: a mark
// dropped on one arm would turn every Int128 at that depth into a Decimal on the way
// back in, silently.

import (
	"bytes"
	"maps"
	"slices"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/arrowout"
	"github.com/advenn/ursus/internal/arrowx"
)

// markSchema holds an Int128 at every depth a field can sit, next to a Decimal(38, 0)
// at the same depth, which must stay unmarked.
func markSchema(t *testing.T) *dtype.Schema {
	t.Helper()
	dec := dtype.Decimal(38, 0)
	s, err := dtype.NewSchema(
		dtype.Of("top", dtype.Int128),
		dtype.Of("top_dec", dec),
		dtype.Of("list", dtype.List(dtype.Int128)),
		dtype.Of("list_dec", dtype.List(dec)),
		dtype.Of("st", dtype.Struct(
			dtype.Field{Name: "i", Type: dtype.Int128, Nullable: true},
			dtype.Field{Name: "d", Type: dec, Nullable: true},
		)),
		dtype.Of("deep", dtype.List(dtype.Struct(
			dtype.Field{Name: "xs", Type: dtype.List(dtype.Int128), Nullable: true},
		))),
	)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// wantMarks is every path that must carry the mark, and no other.
var wantMarks = []string{"top", "list.item", "st.i", "deep.item.xs.item"}

// marks walks an Arrow field tree and returns the path of every marked field, and
// the value each mark holds.
func marks(s *arrow.Schema) map[string]string {
	out := map[string]string{}
	var walk func(prefix string, f arrow.Field)
	walk = func(prefix string, f arrow.Field) {
		path := prefix + f.Name
		if v, ok := f.Metadata.GetValue(arrowx.FieldTypeKey); ok {
			out[path] = v
		}
		switch at := f.Type.(type) {
		case *arrow.ListType:
			walk(path+".", at.ElemField())
		case *arrow.StructType:
			for _, c := range at.Fields() {
				walk(path+".", c)
			}
		}
	}
	for _, f := range s.Fields() {
		walk("", f)
	}
	return out
}

func assertMarks(t *testing.T, where string, s *arrow.Schema) {
	t.Helper()
	got := marks(s)
	if paths := slices.Sorted(maps.Keys(got)); !slices.Equal(paths, slices.Sorted(slices.Values(wantMarks))) {
		t.Errorf("%s: marked fields are %v, want %v", where, paths, wantMarks)
	}
	for path, v := range got {
		if v != arrowx.FieldTypeInt128 {
			t.Errorf("%s: %s is marked %q, want %q", where, path, v, arrowx.FieldTypeInt128)
		}
	}
}

func TestInt128IsMarkedAtEveryDepth(t *testing.T) {
	s, err := arrowout.Schema(markSchema(t))
	if err != nil {
		t.Fatal(err)
	}
	assertMarks(t, "exported", s)
}

// TestInt128MarkSurvivesIPC: a mark that exists in memory and not on the wire is no
// mark at all, since IPC is how the data actually leaves the process.
func TestInt128MarkSurvivesIPC(t *testing.T) {
	s, err := arrowout.Schema(markSchema(t))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	w := ipc.NewWriter(&buf, ipc.WithSchema(s), ipc.WithAllocator(memory.NewGoAllocator()))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := ipc.NewReader(&buf, ipc.WithAllocator(memory.NewGoAllocator()))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Release()
	assertMarks(t, "after an IPC round trip", r.Schema())
}
