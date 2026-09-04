package ursustest

import (
	"context"
	"flag"
	"strconv"
	"testing"

	"ursus/dtype"
	"ursus/i128"
	"ursus/internal/data"
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
func renderValue(c *data.Column, row int) string {
	if s, err := data.TypedColumn[string](c); err == nil {
		v, _ := s.Get(row)
		return strconv.Quote(v)
	}
	if s, err := data.TypedColumn[bool](c); err == nil {
		v, _ := s.Get(row)
		return strconv.FormatBool(v)
	}
	if s, err := data.TypedColumn[float64](c); err == nil {
		v, _ := s.Get(row)
		return strconv.FormatFloat(v, 'g', -1, 64)
	}
	if s, err := data.TypedColumn[float32](c); err == nil {
		v, _ := s.Get(row)
		return strconv.FormatFloat(float64(v), 'g', -1, 32)
	}
	// Temporal types are physically Int32/Int64, so without this they fall through
	// to the integer branch and a Datetime prints as its raw tick count —
	// 2024-01-01 as 1704067200000000000. Decimal has the same shape of guard
	// immediately below, for the same reason.
	if dt := c.DType(); dt.IsTemporal() {
		if ticks, ok := temporalTicks(c, row); ok {
			return dtype.FormatTemporal(dt, ticks)
		}
	}
	if s, err := data.TypedColumn[i128.Int128](c); err == nil {
		v, _ := s.Get(row)
		// Same reason as renderCell: a Decimal is physically Int128, and printing
		// the unscaled units would make a failure message disagree with the frame
		// the user is looking at.
		if dt := c.DType(); dt.ID() == dtype.TypeDecimal {
			return dtype.FormatDecimal(v.String(), dt.Scale())
		}
		return v.String()
	}
	if s, err := data.TypedColumn[int64](c); err == nil {
		v, _ := s.Get(row)
		return strconv.FormatInt(v, 10)
	}
	if s, err := data.TypedColumn[int32](c); err == nil {
		v, _ := s.Get(row)
		return strconv.FormatInt(int64(v), 10)
	}
	if s, err := data.TypedColumn[int16](c); err == nil {
		v, _ := s.Get(row)
		return strconv.FormatInt(int64(v), 10)
	}
	if s, err := data.TypedColumn[int8](c); err == nil {
		v, _ := s.Get(row)
		return strconv.FormatInt(int64(v), 10)
	}
	if s, err := data.TypedColumn[uint64](c); err == nil {
		v, _ := s.Get(row)
		return strconv.FormatUint(v, 10)
	}
	if s, err := data.TypedColumn[uint32](c); err == nil {
		v, _ := s.Get(row)
		return strconv.FormatUint(uint64(v), 10)
	}
	if s, err := data.TypedColumn[uint16](c); err == nil {
		v, _ := s.Get(row)
		return strconv.FormatUint(uint64(v), 10)
	}
	if s, err := data.TypedColumn[uint8](c); err == nil {
		v, _ := s.Get(row)
		return strconv.FormatUint(uint64(v), 10)
	}
	return "<unrenderable " + c.DType().String() + ">"
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
