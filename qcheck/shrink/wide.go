package shrink

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"math/bits"
	"slices"
	"strconv"
	"strings"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/decimal128"

	"github.com/advenn/ursus/qcheck/ast"
	"github.com/advenn/ursus/qcheck/model"
)

// The wide types in a case (step 539). A Value holds each as one of its fields: an
// unsigned integer's bits in I; a Datetime's, a Duration's and a Time's ticks in I;
// a Float32 in F, exactly; a Decimal's unscaled value as decimal text in S. In a
// case's file an unsigned integer and the ticks are a number, a Float32 its
// shortest text ("NaN", and a NaN of another payload "NaN:" and its 32 bits), a
// Decimal its decimal text at its scale, "-0.05".

// TableType is whether a table's column may be of t: a base type, or a wide one.
func TableType(t model.Type) bool {
	switch {
	case slices.Contains(model.Base, t), t == model.Int8, t == model.Int16, t.IsUnsigned(), t == model.Float32:
		return true
	case t.IsDatetime(), t.IsDuration(), t.IsTime():
		return true
	case t.IsDecimal():
		return t.Prec() >= 1 && t.Prec() <= model.MaxDecimalPrecision && t.Scale() <= t.Prec()
	}
	return false
}

var canonicalNaN32 = math.Float32bits(float32(math.NaN()))

// float32Text is f as text that parseFloat32Text reads to its bits.
func float32Text(f float32) string {
	if math.IsNaN(float64(f)) {
		if b := math.Float32bits(f); b != canonicalNaN32 {
			return fmt.Sprintf("NaN:%08x", b)
		}
		return "NaN"
	}
	return strconv.FormatFloat(float64(f), 'g', -1, 32)
}

func parseFloat32Text(s string) (float32, error) {
	if s == "NaN" {
		return math.Float32frombits(canonicalNaN32), nil
	}
	if h, ok := strings.CutPrefix(s, "NaN:"); ok {
		b, err := strconv.ParseUint(h, 16, 32)
		return math.Float32frombits(uint32(b)), err
	}
	f, err := strconv.ParseFloat(s, 32)
	return float32(f), err
}

// unscaled is a Decimal's decimal text of scale s as its unscaled value's text.
func unscaled(text string, s int) (string, error) {
	neg := strings.HasPrefix(text, "-")
	digits := strings.TrimPrefix(text, "-")
	whole, frac, _ := strings.Cut(digits, ".")
	if len(frac) != s || whole == "" {
		return "", fmt.Errorf("a Decimal of scale %d written %q", s, text)
	}
	u, ok := new(big.Int).SetString(whole+frac, 10)
	if !ok {
		return "", fmt.Errorf("a Decimal written %q", text)
	}
	if neg {
		u.Neg(u)
	}
	return u.String(), nil
}

// encodeWide is a wide value's JSON, or false for a type it is not.
func encodeWide(t model.Type, v Value) (json.RawMessage, bool, error) {
	switch {
	case t.IsUnsigned():
		return json.RawMessage(strconv.FormatUint(uint64(v.I), 10)), true, nil
	case t.IsDatetime(), t.IsDuration(), t.IsTime():
		return json.RawMessage(strconv.FormatInt(v.I, 10)), true, nil
	case t == model.Float32:
		b, err := json.Marshal(float32Text(float32(v.F)))
		return b, true, err
	case t.IsDecimal():
		b, err := json.Marshal(ast.DecimalText(decimalOf(v), t.Scale()))
		return b, true, err
	}
	return nil, false, nil
}

// decodeWide reads a wide value's JSON, or false for a type it is not.
func decodeWide(t model.Type, raw json.RawMessage) (Value, bool, error) {
	s := strings.TrimSpace(string(raw))
	switch {
	case t.IsUnsigned():
		u, err := strconv.ParseUint(s, 10, t.Bits())
		return Value{I: int64(u)}, true, err
	case t.IsDatetime(), t.IsDuration(), t.IsTime():
		i, err := strconv.ParseInt(s, 10, 64)
		return Value{I: i}, true, err
	case t == model.Float32:
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return Value{}, true, err
		}
		f, err := parseFloat32Text(text)
		return Value{F: float64(f)}, true, err
	case t.IsDecimal():
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return Value{}, true, err
		}
		u, err := unscaled(text, t.Scale())
		return decValue(u), true, err
	}
	return Value{}, false, nil
}

// decimalOf is a Decimal value's unscaled text, 0 for the zero Value.
func decimalOf(v Value) string {
	if v.S == "" {
		return "0"
	}
	return v.S
}

// appendValue appends v, of type t, to fb.
func appendValue(fb array.Builder, t model.Type, v Value) error {
	if v.Null {
		fb.AppendNull()
		return nil
	}
	switch fb := fb.(type) {
	case *array.Int8Builder:
		fb.Append(int8(v.I))
	case *array.Int16Builder:
		fb.Append(int16(v.I))
	case *array.Int32Builder:
		fb.Append(int32(v.I))
	case *array.Int64Builder:
		fb.Append(v.I)
	case *array.Uint8Builder:
		fb.Append(uint8(v.I))
	case *array.Uint16Builder:
		fb.Append(uint16(v.I))
	case *array.Uint32Builder:
		fb.Append(uint32(v.I))
	case *array.Uint64Builder:
		fb.Append(uint64(v.I))
	case *array.Float32Builder:
		fb.Append(float32(v.F))
	case *array.Float64Builder:
		fb.Append(v.F)
	case *array.StringBuilder:
		fb.Append(v.S)
	case *array.BooleanBuilder:
		fb.Append(v.B)
	case *array.Date32Builder:
		fb.Append(arrow.Date32(v.I))
	case *array.TimestampBuilder:
		fb.Append(arrow.Timestamp(v.I))
	case *array.DurationBuilder:
		fb.Append(arrow.Duration(v.I))
	case *array.Time32Builder:
		fb.Append(arrow.Time32(v.I))
	case *array.Time64Builder:
		fb.Append(arrow.Time64(v.I))
	case *array.Decimal128Builder:
		u, ok := new(big.Int).SetString(decimalOf(v), 10)
		if !ok {
			return fmt.Errorf("a Decimal %q", v.S)
		}
		fb.Append(decimal128.FromBigInt(u))
	default:
		return fmt.Errorf("no column of %s", t)
	}
	return nil
}

// valueAt is the value at i of col, a column of type t.
func valueAt(col arrow.Array, i int) (Value, error) {
	if col.IsNull(i) {
		return Value{Null: true}, nil
	}
	switch a := col.(type) {
	case *array.Int8:
		return Value{I: int64(a.Value(i))}, nil
	case *array.Int16:
		return Value{I: int64(a.Value(i))}, nil
	case *array.Int32:
		return Value{I: int64(a.Value(i))}, nil
	case *array.Int64:
		return Value{I: a.Value(i)}, nil
	case *array.Uint8:
		return Value{I: int64(a.Value(i))}, nil
	case *array.Uint16:
		return Value{I: int64(a.Value(i))}, nil
	case *array.Uint32:
		return Value{I: int64(a.Value(i))}, nil
	case *array.Uint64:
		return Value{I: int64(a.Value(i))}, nil
	case *array.Float32:
		return Value{F: float64(a.Value(i))}, nil
	case *array.Float64:
		return Value{F: a.Value(i)}, nil
	case *array.String:
		return Value{S: strings.Clone(a.Value(i))}, nil
	case *array.Boolean:
		return Value{B: a.Value(i)}, nil
	case *array.Date32:
		return Value{I: int64(a.Value(i))}, nil
	case *array.Timestamp:
		return Value{I: int64(a.Value(i))}, nil
	case *array.Duration:
		return Value{I: int64(a.Value(i))}, nil
	case *array.Time32:
		return Value{I: int64(a.Value(i))}, nil
	case *array.Time64:
		return Value{I: int64(a.Value(i))}, nil
	case *array.Decimal128:
		return decValue(a.Value(i).BigInt().String()), nil
	}
	return Value{}, fmt.Errorf("a column of Arrow %s", col.DataType())
}

// decValue is a Decimal's Value of unscaled text u: none for zero, as the zero
// Value is, so a zero is one value however it was read.
func decValue(u string) Value {
	if u == "0" || u == "-0" {
		return Value{}
	}
	return Value{S: u}
}

// simplerWide are values of a wide type t simpler than v: toward 0.
func simplerWide(t model.Type, v Value) []Value {
	switch {
	case t.IsUnsigned():
		u := uint64(v.I)
		out := []Value{{}}
		if u > 1 {
			out = append(out, Value{I: int64(u / 2)}, Value{I: 1})
		}
		return out
	case t.IsDatetime(), t.IsDuration(), t.IsTime():
		out := []Value{{}}
		if v.I > 1 || v.I < -1 {
			out = append(out, Value{I: v.I / 2})
		}
		if v.I < 0 && v.I != math.MinInt64 && !t.IsTime() {
			out = append(out, Value{I: -v.I})
		}
		return out
	case t == model.Float32:
		out := []Value{{}, {F: 1}}
		f := v.F
		if !math.IsNaN(f) && !math.IsInf(f, 0) {
			if tr := math.Trunc(f); tr != f {
				out = append(out, Value{F: tr})
			}
			if f < -1 {
				out = append(out, Value{F: -1})
			}
			if f < 0 {
				out = append(out, Value{F: -f})
			}
		}
		return out
	case t.IsDecimal():
		u, ok := new(big.Int).SetString(decimalOf(v), 10)
		if !ok || u.Sign() == 0 {
			return nil
		}
		out := []Value{{}}
		if u.CmpAbs(big.NewInt(1)) > 0 {
			out = append(out, decValue(new(big.Int).Quo(u, big.NewInt(2)).String()), Value{S: "1"})
		}
		if u.Sign() < 0 {
			out = append(out, Value{S: new(big.Int).Neg(u).String()})
		}
		return out
	}
	return nil
}

// wideCost is a wide value's complexity, as valueCost's.
func wideCost(t model.Type, v Value) int {
	switch {
	case t.IsUnsigned():
		return 2 * bits.Len64(uint64(v.I))
	case t.IsDatetime(), t.IsDuration(), t.IsTime():
		return intCost(v.I)
	case t == model.Float32:
		return valueCost(model.Float64, v)
	case t.IsDecimal():
		s := decimalOf(v)
		n := 2 * len(strings.TrimPrefix(s, "-"))
		if s == "0" {
			return 0
		}
		if strings.HasPrefix(s, "-") {
			n++
		}
		return n
	}
	return 0
}

// decimalSrc is a Decimal value as Go source of its decimal128.Num.
func decimalSrc(v Value) string {
	u, _ := new(big.Int).SetString(decimalOf(v), 10)
	n := decimal128.FromBigInt(u)
	return fmt.Sprintf("decimal128.New(%d, %d)", n.HighBits(), n.LowBits())
}
