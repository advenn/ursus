package parquet

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"maps"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

// sizesByArrow is arraysOf's answer as arrow-go's IPC reader gives it, as arraysOf
// read the key before step 510: the oracle of the walk that replaced it, on schemas
// arrow-go writes, which its reader can be trusted with.
func sizesByArrow(t *testing.T, raw []byte) map[string]int {
	t.Helper()
	r, err := ipc.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("arrow-go reads back none of its own schema: %v", err)
	}
	defer r.Release()
	out := map[string]int{}
	for _, f := range r.Schema().Fields() {
		switch t := f.Type.(type) {
		case *arrow.FixedSizeListType:
			out[f.Name] = int(t.Len())
		case *arrow.DictionaryType:
			switch t.ValueType.ID() {
			case arrow.STRING, arrow.LARGE_STRING, arrow.STRING_VIEW:
				out[f.Name] = categorical
			}
		}
	}
	return out
}

// arrowSchemaBytes is s as Arrow's writers keep it in a footer, before its base64.
func arrowSchemaBytes(t *testing.T, s *arrow.Schema) []byte {
	t.Helper()
	var b bytes.Buffer
	w := ipc.NewWriter(&b, ipc.WithSchema(s), ipc.WithAllocator(memory.NewGoAllocator()))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// arrowSchemas are schemas of every type arraysOf tells apart and of the kinds it
// passes over, nested and flat, with and without metadata, built from a seed so that
// field order, names and sizes vary.
func arrowSchemas() []*arrow.Schema {
	md := arrow.NewMetadata([]string{"k", "pandas"}, []string{"v", "{}"})
	types := []arrow.DataType{
		arrow.PrimitiveTypes.Int64, arrow.BinaryTypes.String, arrow.BinaryTypes.LargeString,
		arrow.BinaryTypes.StringView, arrow.BinaryTypes.Binary, arrow.FixedWidthTypes.Boolean,
		&arrow.DictionaryType{IndexType: arrow.PrimitiveTypes.Int8, ValueType: arrow.BinaryTypes.String},
		&arrow.DictionaryType{IndexType: arrow.PrimitiveTypes.Uint32, ValueType: arrow.BinaryTypes.LargeString},
		&arrow.DictionaryType{IndexType: arrow.PrimitiveTypes.Int16, ValueType: arrow.PrimitiveTypes.Int64},
		arrow.FixedSizeListOf(1, arrow.PrimitiveTypes.Int64),
		arrow.FixedSizeListOf(3, arrow.BinaryTypes.String),
		arrow.FixedSizeListOf(100_000, arrow.PrimitiveTypes.Float32),
		arrow.FixedSizeListOf(2, arrow.FixedSizeListOf(2, arrow.PrimitiveTypes.Int8)),
		arrow.ListOf(arrow.FixedSizeListOf(4, arrow.PrimitiveTypes.Int32)),
		arrow.StructOf(arrow.Field{Name: "a", Type: arrow.PrimitiveTypes.Int64},
			arrow.Field{Name: "b", Type: arrow.FixedSizeListOf(5, arrow.BinaryTypes.String)}),
		arrow.MapOf(arrow.BinaryTypes.String, arrow.PrimitiveTypes.Int64),
		&arrow.TimestampType{Unit: arrow.Microsecond, TimeZone: "Europe/Dublin"},
	}
	var out []*arrow.Schema
	out = append(out, arrow.NewSchema(nil, nil))
	for seed := range 3 * len(types) {
		var fields []arrow.Field
		for i := range 1 + seed%5 {
			typ := types[(seed*7+i*3)%len(types)]
			f := arrow.Field{Name: fmt.Sprintf("c%d_%s", i, []string{"x", "é", "名", ""}[(seed+i)%4]),
				Type: typ, Nullable: (seed+i)%2 == 0}
			if (seed+i)%3 == 0 {
				f.Metadata = md
			}
			fields = append(fields, f)
		}
		var smd *arrow.Metadata
		if seed%2 == 1 {
			smd = &md
		}
		out = append(out, arrow.NewSchema(fields, smd))
	}
	return out
}

// TestTheFootersArrowSchemaIsReadAsArrowGoReadsIt: the walk of the footer's Arrow
// schema (step 510) gives arraysOf what arrow-go's IPC reader gave it, on every
// schema arrowSchemas builds, and refuses each of them cut short.
//
// Each schema is read as written, with the continuation marker, and without it, as
// writers before Arrow 0.15 framed a message.
func TestTheFootersArrowSchemaIsReadAsArrowGoReadsIt(t *testing.T) {
	arrays, cats := 0, 0
	for i, s := range arrowSchemas() {
		raw := arrowSchemaBytes(t, s)
		want := sizesByArrow(t, raw)
		for _, framed := range [][]byte{raw, raw[4:]} {
			fields, ok := schemaFields(framed)
			if !ok {
				t.Fatalf("schema %d, %s: not read", i, s)
			}
			got := map[string]int{}
			for _, f := range fields {
				if f.size != 0 {
					got[f.name] = f.size
				}
			}
			if !maps.Equal(got, want) {
				t.Errorf("schema %d, %s:\n walked %v\n arrow-go %v", i, s, got, want)
			}
		}
		for _, size := range want {
			if size == categorical {
				cats++
			} else {
				arrays++
			}
		}
		fields, _ := schemaFields(raw)
		if len(fields) != len(s.Fields()) {
			t.Errorf("schema %d: %d fields walked of %d", i, len(fields), len(s.Fields()))
		}
		n := int(binary.LittleEndian.Uint32(raw[4:])) + 8
		for cut := range n {
			if _, ok := schemaFields(raw[:cut]); ok && cut < n {
				// A message cut short of its length is refused by its length; the
				// walk must never read past what it was given.
				t.Fatalf("schema %d cut to %d of %d bytes read", i, cut, n)
			}
		}
	}
	if arrays == 0 || cats == 0 {
		t.Fatalf("the schemas hold %d Arrays and %d Categoricals: the walk was compared on neither", arrays, cats)
	}
}

// TestAFieldClaimingBillionsOfChildrenAllocatesNoneOfThem: an Arrow schema in a
// footer whose field claims 3,221,225,472 children, as the Parquet target's fuzzer
// forged one (step 510). arrow-go's IPC reader made a slice of them, 283 GB, and the
// process died of "out of memory", which no recover catches. The walk reads no field's
// children, so it answers what it answers for the honest schema, and allocates about
// nothing.
func TestAFieldClaimingBillionsOfChildrenAllocatesNoneOfThem(t *testing.T) {
	s := arrow.NewSchema([]arrow.Field{
		{Name: "a", Type: arrow.FixedSizeListOf(3, arrow.PrimitiveTypes.Int64)},
		{Name: "c", Type: &arrow.DictionaryType{IndexType: arrow.PrimitiveTypes.Int32, ValueType: arrow.BinaryTypes.String}},
	}, nil)
	raw := arrowSchemaBytes(t, s)
	honest, ok := schemaFields(raw)
	if !ok {
		t.Fatal("the honest schema is not read")
	}

	forged := bytes.Clone(raw)
	b := flatbuf(forged[8:])
	msg, _ := b.deref(0)
	schema, _ := b.tableField(msg, 2)
	vec, _ := b.refField(schema, 1)
	field, _ := b.deref(vec + 4)
	children, ok := b.refField(field, 5)
	if !ok || children == 0 {
		t.Fatal("the FixedSizeList field holds no children to claim more of")
	}
	binary.LittleEndian.PutUint32(b[children:], 3_221_225_472)

	before := allocatedBytes()
	got, ok := schemaFields(forged)
	if grew := allocatedBytes() - before; grew > 1<<20 {
		t.Errorf("walking the forged schema allocated %d bytes", grew)
	}
	if !ok || fmt.Sprint(got) != fmt.Sprint(honest) {
		t.Errorf("the forged schema: %v, %v; the honest one %v", got, ok, honest)
	}
}
