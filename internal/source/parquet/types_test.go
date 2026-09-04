package parquet

import (
	"errors"
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/schema"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/uerr"
)

// mustNode takes the (node, error) pair directly so it can wrap a constructor
// call inside a table literal — Go does not allow spreading a multi-value call
// alongside other arguments, so this cannot also take a *testing.T.
func mustNode(n *schema.PrimitiveNode, err error) *schema.PrimitiveNode {
	if err != nil {
		panic(err)
	}
	return n
}

// column builds a *schema.Column at the given levels, which is what toDataType
// inspects.
func column(t *testing.T, n *schema.PrimitiveNode, maxDef, maxRep int16) *schema.Column {
	t.Helper()
	return schema.NewColumn(n, maxDef, maxRep)
}

// TestTypeMapping covers the pairs where the LOGICAL annotation is the only thing
// separating two identical physical layouts. Parquet stores Int8, Int16 and Int32
// the same way; reading all three as Int32 would change the schema the plan
// resolved against.
func TestTypeMapping(t *testing.T) {
	cases := []struct {
		name string
		node *schema.PrimitiveNode
		want dtype.DataType
	}{
		{"plain int32", mustNode(schema.NewPrimitiveNode(
			"c", parquet.Repetitions.Optional, parquet.Types.Int32, -1, -1)), dtype.Int32},
		{"plain int64", mustNode(schema.NewPrimitiveNode(
			"c", parquet.Repetitions.Optional, parquet.Types.Int64, -1, -1)), dtype.Int64},
		{"plain boolean", mustNode(schema.NewPrimitiveNode(
			"c", parquet.Repetitions.Optional, parquet.Types.Boolean, -1, -1)), dtype.Bool},
		{"plain float", mustNode(schema.NewPrimitiveNode(
			"c", parquet.Repetitions.Optional, parquet.Types.Float, -1, -1)), dtype.Float32},
		{"plain double", mustNode(schema.NewPrimitiveNode(
			"c", parquet.Repetitions.Optional, parquet.Types.Double, -1, -1)), dtype.Float64},
		{"unannotated byte array is Binary", mustNode(schema.NewPrimitiveNode(
			"c", parquet.Repetitions.Optional, parquet.Types.ByteArray, -1, -1)), dtype.Binary},
		{"STRING", mustNode(schema.NewPrimitiveNodeLogical(
			"c", parquet.Repetitions.Optional, schema.StringLogicalType{},
			parquet.Types.ByteArray, -1, -1)), dtype.String},
		{"INT(8, true)", mustNode(schema.NewPrimitiveNodeLogical(
			"c", parquet.Repetitions.Optional, schema.NewIntLogicalType(8, true),
			parquet.Types.Int32, -1, -1)), dtype.Int8},
		{"INT(16, false)", mustNode(schema.NewPrimitiveNodeLogical(
			"c", parquet.Repetitions.Optional, schema.NewIntLogicalType(16, false),
			parquet.Types.Int32, -1, -1)), dtype.Uint16},
		{"INT(64, false)", mustNode(schema.NewPrimitiveNodeLogical(
			"c", parquet.Repetitions.Optional, schema.NewIntLogicalType(64, false),
			parquet.Types.Int64, -1, -1)), dtype.Uint64},
		{"DATE", mustNode(schema.NewPrimitiveNodeLogical(
			"c", parquet.Repetitions.Optional, schema.DateLogicalType{},
			parquet.Types.Int32, -1, -1)), dtype.Date},
		{"DECIMAL on int32", mustNode(schema.NewPrimitiveNodeLogical(
			"c", parquet.Repetitions.Optional, schema.NewDecimalLogicalType(9, 2),
			parquet.Types.Int32, -1, -1)), dtype.Decimal(9, 2)},
		// TIMESTAMP and TIME, added in step 14. The isAdjustedToUTC bool is the only
		// zone information Parquet carries, so it maps to "UTC" or to naive and a
		// named zone is not representable in either direction.
		{"TIMESTAMP millis UTC", mustNode(schema.NewPrimitiveNodeLogical(
			"c", parquet.Repetitions.Optional,
			schema.NewTimestampLogicalType(true, schema.TimeUnitMillis),
			parquet.Types.Int64, -1, -1)), dtype.Datetime(dtype.Milli, "UTC")},
		{"TIMESTAMP micros naive", mustNode(schema.NewPrimitiveNodeLogical(
			"c", parquet.Repetitions.Optional,
			schema.NewTimestampLogicalType(false, schema.TimeUnitMicros),
			parquet.Types.Int64, -1, -1)), dtype.Datetime(dtype.Micro, "")},
		{"TIMESTAMP nanos UTC", mustNode(schema.NewPrimitiveNodeLogical(
			"c", parquet.Repetitions.Optional,
			schema.NewTimestampLogicalType(true, schema.TimeUnitNanos),
			parquet.Types.Int64, -1, -1)), dtype.Datetime(dtype.Nano, "UTC")},
		{"TIME millis on INT32", mustNode(schema.NewPrimitiveNodeLogical(
			"c", parquet.Repetitions.Optional,
			schema.NewTimeLogicalType(false, schema.TimeUnitMillis),
			parquet.Types.Int32, -1, -1)), dtype.Time(dtype.Milli)},
		{"TIME micros on INT64", mustNode(schema.NewPrimitiveNodeLogical(
			"c", parquet.Repetitions.Optional,
			schema.NewTimeLogicalType(false, schema.TimeUnitMicros),
			parquet.Types.Int64, -1, -1)), dtype.Time(dtype.Micro)},
		{"DECIMAL on FLBA", mustNode(schema.NewPrimitiveNodeLogical(
			"c", parquet.Repetitions.Optional, schema.NewDecimalLogicalType(38, 10),
			parquet.Types.FixedLenByteArray, 16, -1)), dtype.Decimal(38, 10)},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := toDataType(column(t, c.node, 1, 0))
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("got %s, want %s", got, c.want)
			}
		})
	}
}

// TestTypeRefusals: an unsupported column must fail with a user-facing error that
// names the column, not with a panic, a wrong type, or a silently dropped column.
//
// Dropping the column would be the tempting alternative and is the worst one: the
// user gets a frame that quietly lacks the data they asked for.
func TestTypeRefusals(t *testing.T) {
	cases := []struct {
		name   string
		node   *schema.PrimitiveNode
		maxDef int16
		maxRep int16
		want   string
	}{
		{"repeated", mustNode(schema.NewPrimitiveNode(
			"c", parquet.Repetitions.Repeated, parquet.Types.Int32, -1, -1)),
			1, 1, "repeated"},
		{"nested", mustNode(schema.NewPrimitiveNode(
			"c", parquet.Repetitions.Optional, parquet.Types.Int32, -1, -1)),
			2, 0, "nested"},
		{"int96", mustNode(schema.NewPrimitiveNode(
			"c", parquet.Repetitions.Optional, parquet.Types.Int96, -1, -1)),
			1, 0, "INT96"},
		{"decimal too wide", mustNode(schema.NewPrimitiveNodeLogical(
			"c", parquet.Repetitions.Optional, schema.NewDecimalLogicalType(38, 10),
			parquet.Types.FixedLenByteArray, 32, -1)), 1, 0, "128 bits"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := toDataType(column(t, c.node, c.maxDef, c.maxRep))
			if err == nil {
				t.Fatal("expected a refusal")
			}
			if !errors.Is(err, uerr.ErrUnsupported) {
				t.Errorf("kind should be Unsupported: %v", err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error should mention %q: %v", c.want, err)
			}
			if !strings.Contains(err.Error(), `"c"`) {
				t.Errorf("error should name the column: %v", err)
			}
		})
	}
}

// TestDecimalFromBytes covers sign extension, which is the only subtle part of
// decoding a Parquet DECIMAL. A 4-byte -1 is ff ff ff ff, and zero-extending it to
// 128 bits gives 4294967295 rather than -1 — a plausible-looking number that is
// wrong by 2^32.
func TestDecimalFromBytes(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want int64
	}{
		{"zero", []byte{0}, 0},
		{"one byte positive", []byte{0x7f}, 127},
		{"one byte negative", []byte{0xff}, -1},
		{"four byte negative one", []byte{0xff, 0xff, 0xff, 0xff}, -1},
		{"four byte positive", []byte{0x00, 0x00, 0x04, 0xd2}, 1234},
		{"four byte negative", []byte{0xff, 0xff, 0xfb, 0x2e}, -1234},
		{"empty", nil, 0},
		{"sixteen bytes", []byte{
			0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
			0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := decimalFromBytes(c.in)
			if err != nil {
				t.Fatal(err)
			}
			if want := i128.FromInt64(c.want); got.Cmp(want) != 0 {
				t.Errorf("got %s, want %s", got, want)
			}
		})
	}

	if _, err := decimalFromBytes(make([]byte, 17)); err == nil {
		t.Error("a value wider than 128 bits must be refused, not truncated")
	}
}

// TestPutI128BERoundTrips: the writer's encoding and the reader's decoding must be
// inverses. They are separate functions — putI128BE and decimalFromBytes — and the
// one thing that must not happen is one of them adopting i128.AppendBigEndian's
// flipped sign bit, which is right for a sort key and wrong for a file format.
func TestPutI128BERoundTrips(t *testing.T) {
	vals := []i128.Int128{
		i128.FromInt64(0), i128.FromInt64(1), i128.FromInt64(-1),
		i128.FromInt64(1234), i128.FromInt64(-1234),
		i128.FromInt64(9223372036854775807), i128.FromInt64(-9223372036854775808),
	}
	for _, v := range vals {
		var buf [16]byte
		putI128BE(buf[:], v)
		got, err := decimalFromBytes(buf[:])
		if err != nil {
			t.Fatal(err)
		}
		if got.Cmp(v) != 0 {
			t.Errorf("round trip of %s gave %s (bytes %x)", v, got, buf)
		}
	}
}
