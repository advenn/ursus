package ursustest

import (
	"context"
	"flag"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/i128"
	"github.com/advenn/ursus/internal/data"
)

// update rewrites golden files instead of comparing against them.
// Registered here rather than in each test package so `-update` means the same
// thing everywhere.
var update = flag.Bool("update", false, "rewrite ursustest golden files")

// contextFor returns the test's context, falling back to Background for a
// testing.TB implementation that does not provide one (a benchmark, or a fake).
func contextFor(t testing.TB) context.Context {
	type hasContext interface{ Context() context.Context }
	if c, ok := t.(hasContext); ok {
		return c.Context()
	}
	return context.Background()
}

// renderValue formats one non-null value for comparison and for failure output.
//
// Floats use 'g' with full precision so that two values differing in the last
// bit render differently. Rounding here would make AssertFrameEqual quietly
// tolerant of exactly the errors it exists to catch.
//
// # A type it cannot render is an error, not a placeholder
//
// It used to end in `return "<unrenderable " + type + ">"`, and List and Struct
// reached it. The placeholder is the same string for every row, so two nested
// columns holding different values rendered identically and AssertFrameEqual
// passed on them — at every call site with a nested column, and silently. A
// renderer that cannot tell values apart has to say so.
func renderValue(c *data.Column, row int) (string, error) {
	switch c.DType().ID() {
	case dtype.TypeList:
		return renderList(c, row)
	case dtype.TypeStruct:
		return renderStruct(c, row)
	}
	if s, err := data.TypedColumn[string](c); err == nil {
		v, _ := s.Get(row)
		return strconv.Quote(v), nil
	}
	if s, err := data.TypedColumn[bool](c); err == nil {
		v, _ := s.Get(row)
		return strconv.FormatBool(v), nil
	}
	if s, err := data.TypedColumn[float64](c); err == nil {
		v, _ := s.Get(row)
		return strconv.FormatFloat(v, 'g', -1, 64), nil
	}
	if s, err := data.TypedColumn[float32](c); err == nil {
		v, _ := s.Get(row)
		return strconv.FormatFloat(float64(v), 'g', -1, 32), nil
	}
	// Temporal types are physically Int32/Int64, so without this they fall through
	// to the integer branch and a Datetime prints as its raw tick count —
	// 2024-01-01 as 1704067200000000000. Decimal has the same shape of guard
	// immediately below, for the same reason.
	if dt := c.DType(); dt.IsTemporal() {
		if ticks, ok := temporalTicks(c, row); ok {
			return dtype.FormatTemporal(dt, ticks), nil
		}
	}
	if s, err := data.TypedColumn[i128.Int128](c); err == nil {
		v, _ := s.Get(row)
		// Same reason as renderCell: a Decimal is physically Int128, and printing
		// the unscaled units would make a failure message disagree with the frame
		// the user is looking at.
		if dt := c.DType(); dt.ID() == dtype.TypeDecimal {
			return dtype.FormatDecimal(v.String(), dt.Scale()), nil
		}
		return v.String(), nil
	}
	if s, err := data.TypedColumn[int64](c); err == nil {
		v, _ := s.Get(row)
		return strconv.FormatInt(v, 10), nil
	}
	if s, err := data.TypedColumn[int32](c); err == nil {
		v, _ := s.Get(row)
		return strconv.FormatInt(int64(v), 10), nil
	}
	if s, err := data.TypedColumn[int16](c); err == nil {
		v, _ := s.Get(row)
		return strconv.FormatInt(int64(v), 10), nil
	}
	if s, err := data.TypedColumn[int8](c); err == nil {
		v, _ := s.Get(row)
		return strconv.FormatInt(int64(v), 10), nil
	}
	if s, err := data.TypedColumn[uint64](c); err == nil {
		v, _ := s.Get(row)
		return strconv.FormatUint(v, 10), nil
	}
	if s, err := data.TypedColumn[uint32](c); err == nil {
		v, _ := s.Get(row)
		return strconv.FormatUint(uint64(v), 10), nil
	}
	if s, err := data.TypedColumn[uint16](c); err == nil {
		v, _ := s.Get(row)
		return strconv.FormatUint(uint64(v), 10), nil
	}
	if s, err := data.TypedColumn[uint8](c); err == nil {
		v, _ := s.Get(row)
		return strconv.FormatUint(uint64(v), 10), nil
	}
	return "", fmt.Errorf("ursustest: cannot render a %s value in column %q",
		c.DType(), c.Name())
}

// renderList reads the row's range through Get, whose offsets are ABSOLUTE indices
// into the child. A sliced List keeps its parent's whole child, so rendering the
// child from zero would print elements that belong to other rows — exactly the
// defect step 59 fixed in two kernels, and one a renderer must not share.
func renderList(c *data.Column, row int) (string, error) {
	lists := c.Lists()
	lo, hi, _ := lists.Get(row)
	child := lists.Child()
	var sb strings.Builder
	sb.WriteByte('[')
	for e := lo; e < hi; e++ {
		if e > lo {
			sb.WriteString(", ")
		}
		s, err := cell(child, int(e))
		if err != nil {
			return "", err
		}
		sb.WriteString(s)
	}
	sb.WriteByte(']')
	return sb.String(), nil
}

// renderStruct names each field, so two structs whose values match in a different
// field order cannot render alike.
func renderStruct(c *data.Column, row int) (string, error) {
	var sb strings.Builder
	sb.WriteByte('{')
	for i, f := range c.Fields() {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(strconv.Quote(f.Name()))
		sb.WriteString(": ")
		s, err := cell(f, row)
		if err != nil {
			return "", err
		}
		sb.WriteString(s)
	}
	sb.WriteByte('}')
	return sb.String(), nil
}

// temporalTicks reads one row of a temporal column as an int64 tick count,
// whatever its storage width. Date is int32 days; the rest are int64.
func temporalTicks(c *data.Column, row int) (int64, bool) {
	if s, err := data.TypedColumn[int32](c); err == nil {
		v, ok := s.Get(row)
		return int64(v), ok
	}
	if s, err := data.TypedColumn[int64](c); err == nil {
		v, ok := s.Get(row)
		return v, ok
	}
	return 0, false
}
