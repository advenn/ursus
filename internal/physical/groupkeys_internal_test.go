package physical

import (
	"testing"

	"github.com/advenn/ursus/dtype"
)

// TestAnIntegerGroupKeyIsNotEncoded: one integer field, or one stored as an integer,
// is keyed in an IntKeyTable (step 160); anything else is encoded.
func TestAnIntegerGroupKeyIsNotEncoded(t *testing.T) {
	for _, c := range []struct {
		fields []dtype.Field
		ints   bool
	}{
		{[]dtype.Field{dtype.Of("k", dtype.Int64)}, true},
		{[]dtype.Field{dtype.Of("k", dtype.Uint8)}, true},
		{[]dtype.Field{dtype.Of("k", dtype.Date)}, true},
		{[]dtype.Field{dtype.Of("k", dtype.Datetime(dtype.Micro, "UTC"))}, true},
		{[]dtype.Field{dtype.Of("k", dtype.String)}, false},
		{[]dtype.Field{dtype.Of("k", dtype.Float64)}, false},
		{[]dtype.Field{dtype.Of("k", dtype.Int128)}, false},
		{[]dtype.Field{dtype.Of("a", dtype.Int64), dtype.Of("b", dtype.Int64)}, false},
		{nil, false},
	} {
		sch, err := dtype.NewSchema(c.fields...)
		if err != nil {
			t.Fatal(err)
		}
		g := newGroupKeys(sch)
		if (g.ints != nil) != c.ints || (g.bytes != nil) == c.ints {
			t.Errorf("%v: integer table %v, encoded table %v; want integers %v",
				c.fields, g.ints != nil, g.bytes != nil, c.ints)
		}
		if e := g.empty(); (e.ints != nil) != c.ints || e.Len() != 0 {
			t.Errorf("%v: empty() changed the form or kept keys", c.fields)
		}
	}
}
