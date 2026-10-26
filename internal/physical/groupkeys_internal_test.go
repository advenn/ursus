package physical

import (
	"testing"

	"github.com/advenn/ursus/dtype"
)

// TestAnIntegerGroupKeyIsNotEncoded: one integer field, or one stored as an integer,
// is keyed in an IntKeyTable (step 160); one String or Binary field by its own bytes
// (step 205); several integer fields that fit two words with their nulls, or three,
// packed into them (steps 381 and 384); anything else is encoded.
func TestAnIntegerGroupKeyIsNotEncoded(t *testing.T) {
	form := func(g groupKeys) string {
		switch {
		case g.ints != nil:
			return "ints"
		case g.pairs != nil && g.pairs.Words() == 2:
			return "two words"
		case g.pairs != nil:
			return "three words"
		case g.raw:
			return "raw"
		case g.bytes != nil:
			return "encoded"
		}
		return "none"
	}
	i64 := func(name string) dtype.Field { return dtype.Of(name, dtype.Int64) }
	for _, c := range []struct {
		fields []dtype.Field
		want   string
	}{
		{[]dtype.Field{i64("k")}, "ints"},
		{[]dtype.Field{dtype.Of("k", dtype.Uint8)}, "ints"},
		{[]dtype.Field{dtype.Of("k", dtype.Date)}, "ints"},
		{[]dtype.Field{dtype.Of("k", dtype.Datetime(dtype.Micro, "UTC"))}, "ints"},
		{[]dtype.Field{dtype.Of("k", dtype.String)}, "raw"},
		{[]dtype.Field{dtype.Of("k", dtype.Binary)}, "raw"},
		{[]dtype.Field{dtype.Of("k", dtype.Float64)}, "encoded"},
		{[]dtype.Field{dtype.Of("k", dtype.Int128)}, "encoded"},
		{[]dtype.Field{dtype.Of("a", dtype.Int32), dtype.Of("b", dtype.Int32)}, "two words"},
		{[]dtype.Field{dtype.Of("a", dtype.Date), dtype.Of("b", dtype.Uint16)}, "two words"},
		{[]dtype.Field{i64("a"), i64("b")}, "three words"},
		{[]dtype.Field{i64("a"), dtype.Of("b", dtype.Date), dtype.Of("c", dtype.Int32)}, "three words"},
		{[]dtype.Field{i64("a"), i64("b"), i64("c")}, "encoded"},
		{[]dtype.Field{i64("a"), dtype.Of("b", dtype.Float64)}, "encoded"},
		{[]dtype.Field{dtype.Of("a", dtype.String), dtype.Of("b", dtype.String)}, "encoded"},
		{nil, "encoded"},
	} {
		sch, err := dtype.NewSchema(c.fields...)
		if err != nil {
			t.Fatal(err)
		}
		g := newGroupKeys(sch)
		if got := form(g); got != c.want {
			t.Errorf("%v: keyed %s, want %s", c.fields, got, c.want)
		}
		if e := g.empty(); form(e) != c.want || e.Len() != 0 {
			t.Errorf("%v: empty() changed the form or kept keys", c.fields)
		}
	}
}
