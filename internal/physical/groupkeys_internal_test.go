package physical

import (
	"testing"

	"github.com/advenn/ursus/dtype"
)

// TestAnIntegerGroupKeyIsNotEncoded: one integer field, or one stored as an integer,
// is keyed in an IntKeyTable (step 160); one String or Binary field by its own bytes
// (step 205); anything else is encoded.
func TestAnIntegerGroupKeyIsNotEncoded(t *testing.T) {
	for _, c := range []struct {
		fields    []dtype.Field
		ints, raw bool
	}{
		{[]dtype.Field{dtype.Of("k", dtype.Int64)}, true, false},
		{[]dtype.Field{dtype.Of("k", dtype.Uint8)}, true, false},
		{[]dtype.Field{dtype.Of("k", dtype.Date)}, true, false},
		{[]dtype.Field{dtype.Of("k", dtype.Datetime(dtype.Micro, "UTC"))}, true, false},
		{[]dtype.Field{dtype.Of("k", dtype.String)}, false, true},
		{[]dtype.Field{dtype.Of("k", dtype.Binary)}, false, true},
		{[]dtype.Field{dtype.Of("k", dtype.Float64)}, false, false},
		{[]dtype.Field{dtype.Of("k", dtype.Int128)}, false, false},
		{[]dtype.Field{dtype.Of("a", dtype.Int64), dtype.Of("b", dtype.Int64)}, false, false},
		{[]dtype.Field{dtype.Of("a", dtype.String), dtype.Of("b", dtype.String)}, false, false},
		{nil, false, false},
	} {
		sch, err := dtype.NewSchema(c.fields...)
		if err != nil {
			t.Fatal(err)
		}
		g := newGroupKeys(sch)
		if (g.ints != nil) != c.ints || (g.bytes != nil) == c.ints || g.raw != c.raw {
			t.Errorf("%v: integer table %v, byte table %v, raw %v; want integers %v, raw %v",
				c.fields, g.ints != nil, g.bytes != nil, g.raw, c.ints, c.raw)
		}
		if e := g.empty(); (e.ints != nil) != c.ints || e.raw != c.raw || e.Len() != 0 {
			t.Errorf("%v: empty() changed the form or kept keys", c.fields)
		}
	}
}
