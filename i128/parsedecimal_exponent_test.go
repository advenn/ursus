package i128_test

import (
	"errors"
	"strconv"
	"testing"

	"github.com/advenn/ursus/i128"
)

// TestParseDecimalExponentDoesNotWrap: the exponent was added to the scale in an int,
// so "1e9223372036854775807" at scale 2 wrapped to a shift far below zero, rounded to
// nothing and read as 0, with no error; DuckDB 1.5.5 and Polars 1.44.1 refuse it. An
// exponent past ±2^40 is out of range either way, as both refuse the mirror
// "5.0e-9223372036854775808"; a tiny value inside it still rounds to 0, as both
// round 1e-50, and a zero to 0.
func TestParseDecimalExponentDoesNotWrap(t *testing.T) {
	for _, c := range []struct {
		text        string
		prec, scale int
		want        int64
		outOfRange  bool
	}{
		{"1e9223372036854775807", 38, 2, 0, true},
		{"-1e9223372036854775807", 38, 2, 0, true},
		{"1e9223372036854775806", 38, 0, 0, true},
		{"9.99e9223372036854775807", 10, 2, 0, true},
		{"5.0e-9223372036854775808", 38, 0, 0, true},
		{"1e1099511627777", 38, 2, 0, true},
		{"1e-1099511627777", 38, 2, 0, true},
		{"1e40", 38, 2, 0, true},
		{"1e-50", 38, 2, 0, false},
		{"5e-3", 38, 2, 1, false},
		{"0e9223372036854775807", 38, 2, 0, false},
		{"1.5e2", 10, 2, 15000, false},
	} {
		v, err := i128.ParseDecimal(c.text, c.prec, c.scale)
		if c.outOfRange {
			if !errors.Is(err, strconv.ErrRange) {
				t.Errorf("%s at (%d,%d): got %v, %v; want ErrRange", c.text, c.prec, c.scale, v, err)
			}
			continue
		}
		if err != nil || v != i128.FromInt64(c.want) {
			t.Errorf("%s at (%d,%d): got %v, %v; want %d", c.text, c.prec, c.scale, v, err, c.want)
		}
	}
}
