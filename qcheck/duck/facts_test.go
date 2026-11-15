package duck

import (
	"context"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"math/rand/v2"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/kernel"
	"github.com/advenn/ursus/qcheck/gen"
	"github.com/advenn/ursus/qcheck/model"
	"github.com/advenn/ursus/qcheck/sqlgen"
)

// check holds got to want, value by value.
func check(t *testing.T, what string, got, want []any) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: %d values, the oracle's %d: %v, %v", what, len(got), len(want), got, want)
		return
	}
	for i := range got {
		if !same(got[i], want[i]) {
			t.Errorf("%s, value %d: DuckDB %s, the oracle %s", what, i, show(got[i]), show(want[i]))
		}
	}
}

// values is a VALUES list of one column of SQL literals.
func values(lits []string) string {
	rows := make([]string, len(lits))
	for i, l := range lits {
		rows[i] = "(" + strconv.Itoa(i) + ", " + l + ")"
	}
	return "(VALUES " + strings.Join(rows, ", ") + ") AS v(i, x)"
}

func floatLits(fs []float64) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = sqlgen.Float(f)
	}
	return out
}

func strLits(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = sqlgen.String(s)
	}
	return out
}

// mapped is f of each x, in VALUES order.
func mapped(t *testing.T, d *DB, lits []string, f string) []any {
	t.Helper()
	return column(t, d, "SELECT "+f+" FROM "+values(lits)+" ORDER BY i")
}

// The floats the facts read: every extreme a table draws, and drawn ones.
func someFloats(n int) []float64 {
	fs := []float64{math.NaN(), 0, math.Copysign(0, -1), math.Inf(1), math.Inf(-1), math.MaxFloat64,
		-math.MaxFloat64, math.SmallestNonzeroFloat64, 2.2250738585072014e-308, 1 << 53, 1<<53 + 2,
		-(1 << 63), 1 << 63, 0.1, 0.3, 1e-5, 9.99999e-6, 1e-4, 1e15, 1e16, 9999999999999998, 123.0, 123.25,
		-1.5e-7, 1e21, 5e-324, 1.7976931348623157e308, 0.30000000000000004, 1e308, 2.5, -2.5}
	r := rand.New(rand.NewPCG(513, 1))
	for range n {
		switch r.IntN(3) {
		case 0:
			fs = append(fs, r.NormFloat64()*math.Pow(10, float64(r.IntN(40)-20)))
		case 1:
			if f := math.Float64frombits(r.Uint64()); !math.IsNaN(f) {
				fs = append(fs, f)
			}
		default:
			fs = append(fs, float64(r.IntN(2001)-1000)/4)
		}
	}
	return fs
}

// orderKey is ursus's total order of floats (kernel.OrderKeyF64): NaN above all,
// -0 equal to +0.
func orderKey(f float64) uint64 { return kernel.OrderKeyF64(f) }

// TestDuckDBSemantics holds the DuckDB facts the translations lean on, each value
// checked against a Go oracle the test computes: Go's own arithmetic, strconv,
// math/big, regexp, or ursus's own functions where the fact is a translation of
// one. A fact that fails corrects the rule that leans on it.
func TestDuckDBSemantics(t *testing.T) {
	d := open(t)
	facts := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"ORDER BY puts nulls first or last as each key says (T-NULLS)", func(t *testing.T) {
			vals := []any{int64(3), nil, int64(1), nil, int64(2)}
			lits := []string{"3", "NULL", "1", "NULL", "2"}
			for _, desc := range []bool{false, true} {
				for _, last := range []bool{false, true} {
					q := "SELECT x FROM " + values(lits) + " ORDER BY x"
					q += map[bool]string{false: " ASC", true: " DESC"}[desc]
					q += map[bool]string{false: " NULLS FIRST", true: " NULLS LAST"}[last]
					var nonNull, want []any
					for _, v := range vals {
						if v != nil {
							nonNull = append(nonNull, v)
						}
					}
					slices.SortFunc(nonNull, func(a, b any) int {
						c := int(a.(int64) - b.(int64))
						if desc {
							return -c
						}
						return c
					})
					nulls := []any{nil, nil}
					if last {
						want = append(nonNull, nulls...)
					} else {
						want = append(nulls, nonNull...)
					}
					check(t, q, column(t, d, q), want)
				}
			}
		}},
		{"ORDER BY u_fkey(x) is the total order, NaN above +Inf, -0 tying +0 (T-KEY-ZERO, T-SORT-STABLE)", func(t *testing.T) {
			fs := []float64{math.NaN(), math.Inf(1), 1, math.Copysign(0, -1), 0, math.Inf(-1), -1, 0,
				math.Copysign(0, -1), math.NaN(), 2.5}
			got := column(t, d, "SELECT i FROM "+values(floatLits(fs))+" ORDER BY u_fkey(x), i")
			idx := make([]int, len(fs))
			for i := range idx {
				idx[i] = i
			}
			slices.SortStableFunc(idx, func(a, b int) int {
				ka, kb := orderKey(fs[a]), orderKey(fs[b])
				switch {
				case ka < kb:
					return -1
				case ka > kb:
					return 1
				}
				return 0
			})
			want := make([]any, len(idx))
			for i, j := range idx {
				want[i] = int64(j)
			}
			check(t, "ORDER BY u_fkey(x), i", got, want)
		}},
		{"DuckDB's = and < take NaN as one value above +Inf, unlike IEEE (T-IEEE-CMP's reason)", func(t *testing.T) {
			nan, inf := sqlgen.Float(math.NaN()), sqlgen.Float(math.Inf(1))
			got := one(t, d, "SELECT "+nan+" = "+nan+", "+nan+" > "+inf+", "+nan+" < "+sqlgen.Float(1))
			n := math.NaN()
			check(t, "NaN = NaN, NaN > +Inf, NaN < 1", got, []any{orderKey(n) == orderKey(n),
				orderKey(n) > orderKey(math.Inf(1)), orderKey(n) < orderKey(1)})
		}},
		{"-0 equals +0 and is not below it, as IEEE", func(t *testing.T) {
			nz, z := sqlgen.Float(math.Copysign(0, -1)), sqlgen.Float(0)
			got := one(t, d, "SELECT "+nz+" = "+z+", "+nz+" < "+z)
			negZero := math.Copysign(0, -1)
			check(t, "-0 = +0, -0 < +0", got, []any{negZero == 0, negZero < 0})
		}},
		{"GROUP BY and count(DISTINCT) make one group of every NaN and one of ±0 (T-KEY-ZERO, T-NUNIQUE)", func(t *testing.T) {
			lits := []string{sqlgen.Float(math.NaN()), "CAST('0.0' AS DOUBLE) / CAST('0.0' AS DOUBLE)",
				sqlgen.Float(math.Copysign(0, -1)), sqlgen.Float(0), sqlgen.Float(1), sqlgen.Float(1)}
			keys := map[uint64]bool{}
			for _, f := range []float64{math.NaN(), math.Float64frombits(0xfff8000000000000), math.Copysign(0, -1), 0, 1, 1} {
				keys[orderKey(f)] = true
			}
			v := values(lits)
			got := one(t, d, "SELECT (SELECT count(*) FROM (SELECT x FROM "+v+" GROUP BY x)), "+
				"(SELECT count(*) FROM (SELECT u_fkey(x) FROM "+v+" GROUP BY 1)), "+
				"(SELECT count(DISTINCT x) FROM "+v+"), (SELECT count(DISTINCT u_fkey(x)) FROM "+v+")")
			n := int64(len(keys))
			check(t, "groups of x, of u_fkey(x), distinct x, distinct u_fkey(x)", got, []any{n, n, n, n})
		}},
		{"a join on doubles matches NaN with NaN and -0 with +0, raw and through u_fkey (T-KEY-ZERO)", func(t *testing.T) {
			l := []float64{math.NaN(), 0, math.Copysign(0, -1), 1, 2}
			r := []float64{math.NaN(), math.NaN(), 0, math.Copysign(0, -1), 1, 3}
			pairs := 0
			for _, a := range l {
				for _, b := range r {
					if orderKey(a) == orderKey(b) {
						pairs++
					}
				}
			}
			lv := strings.Replace(values(floatLits(l)), "AS v(i, x)", "AS l(i, x)", 1)
			rv := strings.Replace(values(floatLits(r)), "AS v(i, x)", "AS r(i, x)", 1)
			got := one(t, d, "SELECT (SELECT count(*) FROM "+lv+" JOIN "+rv+" ON l.x = r.x), "+
				"(SELECT count(*) FROM "+lv+" JOIN "+rv+" ON u_fkey(l.x) = u_fkey(r.x))")
			check(t, "pairs matched raw, through u_fkey", got, []any{int64(pairs), int64(pairs)})
		}},
		{"integer // truncates toward zero and % takes the dividend's sign, in BIGINT and HUGEINT", func(t *testing.T) {
			pairs := [][2]int64{{7, 2}, {-7, 2}, {7, -2}, {-7, -2}, {6, 3}, {-6, 3}, {1, 7}, {-1, 7},
				{math.MaxInt64, 3}, {math.MinInt64 + 1, 7}}
			for _, p := range pairs {
				a, b := strconv.FormatInt(p[0], 10), strconv.FormatInt(p[1], 10)
				got := one(t, d, fmt.Sprintf("SELECT CAST(%s AS BIGINT) // CAST(%s AS BIGINT), CAST(%s AS BIGINT) %% CAST(%s AS BIGINT), "+
					"CAST(CAST(%s AS HUGEINT) // CAST(%s AS HUGEINT) AS BIGINT), CAST(CAST(%s AS HUGEINT) %% CAST(%s AS HUGEINT) AS BIGINT)",
					a, b, a, b, a, b, a, b))
				check(t, fmt.Sprintf("%d // %d and %%", p[0], p[1]), got, []any{p[0] / p[1], p[0] % p[1], p[0] / p[1], p[0] % p[1]})
			}
		}},
		{"u_idiv and u_imod floor, a zero divisor null, and u_w64 wraps MinInt64 // -1 (T-FLOOR, T-INT-WRAP)", func(t *testing.T) {
			pairs := [][2]int64{{7, 2}, {-7, 2}, {7, -2}, {-7, -2}, {5, 0}, {0, 5}, {math.MinInt64, -1},
				{math.MinInt64, 1}, {math.MaxInt64, -1}, {math.MinInt64, 7}, {-1, math.MinInt64}, {3, math.MaxInt64}}
			for _, p := range pairs {
				a, b := p[0], p[1]
				var q, m any
				if b != 0 {
					qq, mm := a/b, a%b // Go wraps MinInt64 / -1 to MinInt64
					if mm != 0 && (mm < 0) != (b < 0) {
						qq, mm = qq-1, mm+b
					}
					q, m = qq, mm
				}
				got := one(t, d, fmt.Sprintf("SELECT u_w64(u_idiv(CAST('%d' AS BIGINT), CAST('%d' AS BIGINT))), "+
					"CAST(u_imod(CAST('%d' AS BIGINT), CAST('%d' AS BIGINT)) AS BIGINT)", a, b, a, b))
				check(t, fmt.Sprintf("%d floor-div and floor-mod %d", a, b), got, []any{q, m})
			}
		}},
		{"BIGINT arithmetic past int64 raises; HUGEINT holds a product of two BIGINTs (T-INT-WRAP's reason)", func(t *testing.T) {
			sum := new(big.Int).Add(big.NewInt(math.MaxInt64), big.NewInt(1))
			_, _, err := d.Query(context.Background(), "SELECT CAST('9223372036854775807' AS BIGINT) + CAST('1' AS BIGINT)")
			if sum.IsInt64() == (err != nil) {
				t.Errorf("MaxInt64 + 1, whose exact sum %s fits int64: %v; DuckDB's error: %v", sum, sum.IsInt64(), err)
			}
			prod := new(big.Int).Mul(big.NewInt(math.MinInt64), big.NewInt(math.MinInt64))
			got := one(t, d, "SELECT CAST(CAST('-9223372036854775808' AS HUGEINT) * CAST('-9223372036854775808' AS HUGEINT) AS VARCHAR)")
			check(t, "MinInt64 * MinInt64 in HUGEINT", got, []any{prod.String()})
		}},
		{"u_w8, u_w16, u_w32 and u_w64 wrap as Go's integers do (T-INT-WRAP)", func(t *testing.T) {
			for _, s := range []string{"0", "127", "128", "-129", "300", "32768", "-32769", "2147483648",
				"-2147483649", "9223372036854775808", "-9223372036854775809", "18446744073709551621", "-18446744073709551619"} {
				v, _ := new(big.Int).SetString(s, 10)
				var want []any
				for _, bits := range []uint{8, 16, 32, 64} {
					m := new(big.Int).Lsh(big.NewInt(1), bits)
					w := new(big.Int).Mod(v, m) // in [0, 2^bits)
					if w.Cmp(new(big.Int).Rsh(m, 1)) >= 0 {
						w.Sub(w, m)
					}
					want = append(want, w.Int64())
				}
				got := one(t, d, fmt.Sprintf("SELECT u_w8(CAST('%[1]s' AS HUGEINT)), u_w16(CAST('%[1]s' AS HUGEINT)), "+
					"u_w32(CAST('%[1]s' AS HUGEINT)), u_w64(CAST('%[1]s' AS HUGEINT))", s))
				check(t, "wraps of "+s, got, want)
			}
		}},
		{"a double divided by zero is ±Inf or NaN under ieee_floating_point_ops (T-DIV-FLOAT)", func(t *testing.T) {
			z, nz := sqlgen.Float(0), sqlgen.Float(math.Copysign(0, -1))
			got := one(t, d, "SELECT "+sqlgen.Float(1)+" / "+z+", "+sqlgen.Float(-1)+" / "+z+", "+z+" / "+z+", "+
				sqlgen.Float(1)+" / "+nz)
			zero, negZero := 0.0, math.Copysign(0, -1)
			check(t, "1/0, -1/0, 0/0, 1/-0", got, []any{1 / zero, -1 / zero, zero / zero, 1 / negZero})
		}},
		{"% on DOUBLE is C's fmod, of a zero divisor NaN (T-FLOAT-MOD)", func(t *testing.T) {
			pairs := [][2]float64{{-7.5, 2}, {7.5, -2}, {1, 0}, {math.Copysign(0, -1), 1}, {5, math.Inf(1)},
				{math.Inf(1), 2}, {-4, 2}, {1e300, 3}, {0.1, 3}}
			for _, p := range pairs {
				got := one(t, d, "SELECT "+sqlgen.Float(p[0])+" % "+sqlgen.Float(p[1]))
				check(t, fmt.Sprintf("%v %% %v", p[0], p[1]), got, []any{math.Mod(p[0], p[1])})
			}
		}},
		{"fmod() is x - y·floor(x/y), not C's fmod, so u_fmod uses % (T-FLOAT-MOD)", func(t *testing.T) {
			pairs := [][2]float64{{-7.5, 2}, {7.5, -2}, {math.Copysign(0, -1), 1}, {5, math.Inf(1)}, {-4, 2}, {1e300, 3}}
			for _, p := range pairs {
				x, y := p[0], p[1]
				got := one(t, d, "SELECT fmod("+sqlgen.Float(x)+", "+sqlgen.Float(y)+")")
				// The conversion keeps Go from fusing the product and the difference.
				check(t, fmt.Sprintf("fmod(%v, %v)", x, y), got, []any{x - float64(y*math.Floor(x/y))})
			}
		}},
		{"u_fmod is ursus's float remainder (T-FLOAT-MOD)", func(t *testing.T) {
			fs := []float64{-7.5, 7.5, 4, -4, 2, -2, 0, math.Copysign(0, -1), math.Inf(1), math.Inf(-1), math.NaN(), 1e300, 3}
			for _, a := range fs {
				for _, b := range fs {
					r := math.Mod(a, b) // modFloatScalar's rule, computed here
					switch {
					case r == 0:
						r = math.Copysign(0, b)
					case (r < 0) != (b < 0):
						r += b
					}
					got := one(t, d, "SELECT u_fmod("+sqlgen.Float(a)+", "+sqlgen.Float(b)+")")
					check(t, fmt.Sprintf("u_fmod(%v, %v)", a, b), got, []any{r})
				}
			}
		}},
		{"floor keeps -0, negation makes -0 of +0, abs makes +0 of -0 (T-FLOOR, T-INT-WRAP)", func(t *testing.T) {
			nz, z := sqlgen.Float(math.Copysign(0, -1)), sqlgen.Float(0)
			got := one(t, d, "SELECT floor("+nz+"), -("+z+"), abs("+nz+"), floor("+sqlgen.Float(-0.5)+"), -("+nz+")")
			zero, negZero := 0.0, math.Copysign(0, -1)
			check(t, "floor(-0), -(+0), abs(-0), floor(-0.5), -(-0)", got,
				[]any{math.Floor(negZero), -zero, math.Abs(negZero), math.Floor(-0.5), -negZero})
		}},
		{"signbit is the sign bit", func(t *testing.T) {
			fs := []float64{math.Copysign(0, -1), 0, -1, 1, math.Inf(-1)}
			got := mapped(t, d, floatLits(fs), "signbit(x)")
			want := make([]any, len(fs))
			for i, f := range fs {
				want[i] = math.Signbit(f)
			}
			check(t, "signbit", got, want)
		}},
		{"CAST(BIGINT AS DOUBLE) rounds to the nearest, ties to even (T-PROMOTE)", func(t *testing.T) {
			is := []int64{1<<53 + 1, 1<<53 + 3, math.MaxInt64, math.MinInt64 + 1, -(1<<53 + 1), 123}
			for _, i := range is {
				got := one(t, d, fmt.Sprintf("SELECT CAST(CAST('%d' AS BIGINT) AS DOUBLE)", i))
				check(t, fmt.Sprint(i), got, []any{float64(i)})
			}
		}},
		{"CAST of a fraction to BIGINT does not truncate, as ursus refuses it (T-CAST-EXACT's reason)", func(t *testing.T) {
			for _, f := range []float64{1.5, -1.5, 0.7, -0.7} {
				got := one(t, d, "SELECT CAST("+sqlgen.Float(f)+" AS BIGINT)")
				if same(got[0], int64(math.Trunc(f))) {
					t.Errorf("CAST(%v AS BIGINT) is %v, truncation's", f, got[0])
				}
			}
		}},
		{"round() rounds half away from zero", func(t *testing.T) {
			fs := []float64{2.5, -2.5, 0.5, 1.5, -0.5}
			got := mapped(t, d, floatLits(fs), "round(x)")
			want := make([]any, len(fs))
			for i, f := range fs {
				want[i] = math.Round(f)
			}
			check(t, "round", got, want)
		}},
		{"TRY_CAST to a narrower integer is null out of range (T-CAST-RANGE)", func(t *testing.T) {
			is := []int64{127, 128, -128, -129, 0, 70000, -2147483649}
			for _, i := range is {
				got := one(t, d, fmt.Sprintf("SELECT TRY_CAST(CAST('%[1]d' AS BIGINT) AS TINYINT), TRY_CAST(CAST('%[1]d' AS BIGINT) AS SMALLINT), "+
					"TRY_CAST(CAST('%[1]d' AS BIGINT) AS INTEGER)", i))
				var want []any
				for _, bits := range []int{8, 16, 32} {
					if i >= -(1<<(bits-1)) && i < 1<<(bits-1) {
						want = append(want, i)
					} else {
						want = append(want, nil)
					}
				}
				check(t, fmt.Sprint(i), got, want)
			}
		}},
		{"TRY_CAST of signed decimal digits to BIGINT is strconv.ParseInt's (T-PARSE-INT)", func(t *testing.T) {
			ss := []string{"+5", "-0", "007", "9223372036854775807", "9223372036854775808", "-9223372036854775808",
				"-9223372036854775809", "123456789012345678901234567890", "0"}
			got := mapped(t, d, strLits(ss), "TRY_CAST(x AS BIGINT)")
			want := make([]any, len(ss))
			for i, s := range ss {
				if v, err := strconv.ParseInt(s, 10, 64); err == nil {
					want[i] = v
				}
			}
			check(t, "TRY_CAST AS BIGINT", got, want)
		}},
		{"u_pfloat is dtype.ParseFloat (T-PARSE-FLOAT)", func(t *testing.T) {
			ss := []string{"1e400", "-1e400", "1e-400", "5.", ".5", "+.5e3", "-0", "inf", "-Infinity", "+NaN",
				"nan", "NAN", "-nan", "+inf", "iNfInItY", "infinity ", " 1.5", "1_0", "0x1p3", "1e", "e5", "+-1",
				"--1", "1.5e+307", "4.9e-324", "2.4703282292062328e-324", "123456789012345678901234567890",
				"0.30000000000000004", "1e+16", "", ".", "+", "1.5E-3", "١", "İnf", "1e-5", "9007199254740993"}
			for _, f := range someFloats(200) {
				ss = append(ss, strconv.FormatFloat(f, 'g', -1, 64), strconv.FormatFloat(f, 'e', 16, 64))
			}
			got := mapped(t, d, strLits(ss), "u_pfloat(x)")
			want := make([]any, len(ss))
			for i, s := range ss {
				if f, err := dtype.ParseFloat(s, 64); err == nil {
					want[i] = f
				}
			}
			check(t, "u_pfloat", got, want)
		}},
		{"CAST(DOUBLE AS VARCHAR) writes as many digits as strconv's shortest, which read back to the bits", func(t *testing.T) {
			var fs []float64
			for _, f := range someFloats(300) {
				if !math.IsNaN(f) && !math.IsInf(f, 0) && f != 0 {
					fs = append(fs, math.Abs(f))
				}
			}
			got := mapped(t, d, floatLits(fs), "CAST(x AS VARCHAR)")
			digits := regexp.MustCompile(`^([0-9.]+)`)
			for i, f := range fs {
				s, _ := got[i].(string)
				back, err := strconv.ParseFloat(s, 64)
				if err != nil || back != f {
					t.Errorf("%v written %q, read back %v (%v)", f, s, back, err)
					continue
				}
				m := strings.Trim(strings.ReplaceAll(digits.FindString(s), ".", ""), "0")
				want := strings.Trim(strings.ReplaceAll(strings.Split(strconv.FormatFloat(f, 'e', -1, 64), "e")[0], ".", ""), "0")
				if m != want {
					t.Errorf("%v written %q: digits %s, strconv's shortest %s", f, s, m, want)
				}
			}
		}},
		{"u_ftext is kernel.FloatText (T-FLOAT-TEXT)", func(t *testing.T) {
			fs := someFloats(400)
			got := mapped(t, d, floatLits(fs), "u_ftext(x)")
			want := make([]any, len(fs))
			for i, f := range fs {
				want[i] = kernel.FloatText(f, 64)
			}
			check(t, "u_ftext", got, want)
		}},
		{"u_dtext is dtype.FormatTemporal of a Date (T-DATE-TEXT)", func(t *testing.T) {
			days := []int64{math.MinInt32, math.MinInt32 + 1, -719528, -719529, -719162, -719163, -1, 0, 1,
				2932896, 2932897, math.MaxInt32, 11016, -11017, 18262, 60, -146097, 146097}
			r := rand.New(rand.NewPCG(513, 2))
			for range 300 {
				days = append(days, int64(int32(r.Uint32())), int64(r.IntN(2_000_001))-1_000_000)
			}
			lits := make([]string, len(days))
			want := make([]any, len(days))
			for i, v := range days {
				lits[i] = fmt.Sprintf("CAST('%d' AS INTEGER)", v)
				want[i] = dtype.FormatTemporal(dtype.Date, v)
			}
			check(t, "u_dtext", mapped(t, d, lits, "u_dtext(x)"), want)
		}},
		{"u_pdate is dtype.ParseTemporal of a Date (T-PARSE-DATE)", func(t *testing.T) {
			ss := []string{"2024-02-29", "2023-02-29", "1900-02-29", "2000-02-29", "0000-01-01", "0000-02-29",
				"9999-12-31", "2024-1-01", "2024-01-1", "+202-01-01", "-001-01-01", " 2024-01-01", "2024-01-01 ",
				"2024/01/01", "20240101", "2024-00-10", "2024-13-01", "2024-04-31", "2024-04-30", "2024-01-00",
				"10000-01-01", "2024-01-01T00:00:00", "", "abcd-ef-gh", "１２３４-01-01", "1970-01-01", "1969-12-31"}
			r := rand.New(rand.NewPCG(513, 3))
			for range 200 {
				ss = append(ss, dtype.FormatTemporal(dtype.Date, int64(r.IntN(2932897+719162))-719162))
			}
			want := make([]any, len(ss))
			for i, s := range ss {
				if v, ok := dtype.ParseTemporal(dtype.Date, s); ok {
					want[i] = v
				}
			}
			check(t, "u_pdate", mapped(t, d, strLits(ss), "CAST(u_pdate(x) AS BIGINT)"), want)
		}},
		{"u_pbool is strconv.ParseBool (T-PARSE-BOOL)", func(t *testing.T) {
			ss := []string{"1", "t", "T", "TRUE", "true", "True", "0", "f", "F", "FALSE", "false", "False",
				"tRUE", "yes", "no", "", " true", "2", "on"}
			want := make([]any, len(ss))
			for i, s := range ss {
				if b, err := strconv.ParseBool(s); err == nil {
					want[i] = b
				}
			}
			check(t, "u_pbool", mapped(t, d, strLits(ss), "u_pbool(x)"), want)
		}},
		{"u_feq, u_fne and the rest are IEEE, a null either side null (T-IEEE-CMP)", func(t *testing.T) {
			fs := []float64{math.NaN(), math.Inf(-1), -1, math.Copysign(0, -1), 0, 1, math.Inf(1)}
			ops := []struct {
				fn  string
				go_ func(a, b float64) bool
			}{{"u_feq", func(a, b float64) bool { return a == b }}, {"u_fne", func(a, b float64) bool { return a != b }},
				{"u_flt", func(a, b float64) bool { return a < b }}, {"u_fle", func(a, b float64) bool { return a <= b }},
				{"u_fgt", func(a, b float64) bool { return a > b }}, {"u_fge", func(a, b float64) bool { return a >= b }}}
			for _, op := range ops {
				var sel []string
				var want []any
				for _, a := range fs {
					for _, b := range fs {
						sel = append(sel, op.fn+"("+sqlgen.Float(a)+", "+sqlgen.Float(b)+")")
						want = append(want, op.go_(a, b))
					}
				}
				sel = append(sel, op.fn+"(CAST(NULL AS DOUBLE), "+sqlgen.Float(math.NaN())+")")
				want = append(want, nil)
				check(t, op.fn, one(t, d, "SELECT "+strings.Join(sel, ", ")), want)
			}
		}},
		{"u_fless is ursus's total order's < (T-EXTREMUM)", func(t *testing.T) {
			fs := []float64{math.NaN(), math.Inf(-1), -1, math.Copysign(0, -1), 0, 1, math.Inf(1)}
			var sel []string
			var want []any
			for _, a := range fs {
				for _, b := range fs {
					sel = append(sel, "u_fless("+sqlgen.Float(a)+", "+sqlgen.Float(b)+")")
					want = append(want, orderKey(a) < orderKey(b))
				}
			}
			check(t, "u_fless", one(t, d, "SELECT "+strings.Join(sel, ", ")), want)
		}},
		{"u_f2i keeps a float that is an integer in range, else null (T-CAST-EXACT)", func(t *testing.T) {
			fs := []float64{0, math.Copysign(0, -1), 1.5, -1.5, 127, 128, -128, -129, math.NaN(), math.Inf(1),
				math.Inf(-1), -(1 << 63), 1 << 63, 9.223372036854774e18, 1e300}
			lits := floatLits(fs)
			for _, bits := range []int{8, 64} {
				lo, hi := -math.Ldexp(1, bits-1), math.Ldexp(1, bits-1)
				want := make([]any, len(fs))
				for i, f := range fs {
					if f >= lo && f < hi && f == math.Trunc(f) {
						want[i] = int64(f)
					}
				}
				got := mapped(t, d, lits, fmt.Sprintf("u_f2i(x, %s, %s)", sqlgen.Float(lo), sqlgen.Float(hi)))
				check(t, fmt.Sprintf("u_f2i at %d bits", bits), got, want)
			}
		}},
		{"u_imean is the double nearest Σx/n, as ursus's integer Mean (T-MEAN-INT)", func(t *testing.T) {
			type sn struct {
				s *big.Int
				n int64
			}
			cases := []sn{{big.NewInt(0), 3}, {big.NewInt(-7), 2}, {big.NewInt(7), 2}, {big.NewInt(1), 3}, {big.NewInt(-1), 3},
				{new(big.Int).Add(new(big.Int).Lsh(big.NewInt(1), 54), big.NewInt(1)), 2}, // a midpoint, ties to even
				{new(big.Int).Add(new(big.Int).Lsh(big.NewInt(1), 54), big.NewInt(3)), 2},
				{new(big.Int).Mul(big.NewInt(math.MaxInt64), big.NewInt(2)), 2}, {new(big.Int).Mul(big.NewInt(math.MinInt64), big.NewInt(3)), 3},
				{new(big.Int).Mul(big.NewInt(math.MaxInt64), big.NewInt(8193)), 8193}}
			r := rand.New(rand.NewPCG(513, 4))
			for range 300 {
				n := int64(1 + r.IntN(16384))
				v := new(big.Int).Mul(big.NewInt(int64(r.Uint64())), big.NewInt(n))
				v.Add(v, big.NewInt(int64(r.IntN(int(n)))-n/2))
				if r.IntN(3) == 0 {
					v = big.NewInt(int64(r.IntN(2_000_001)) - 1_000_000)
				}
				cases = append(cases, sn{v, n})
			}
			var sel []string
			var want []any
			for _, c := range cases {
				sel = append(sel, fmt.Sprintf("u_imean(CAST('%s' AS HUGEINT), CAST('%d' AS BIGINT))", c.s, c.n))
				f, _ := new(big.Rat).SetFrac(c.s, big.NewInt(c.n)).Float64()
				want = append(want, f)
			}
			for i := 0; i < len(sel); i += 50 {
				j := min(i+50, len(sel))
				check(t, fmt.Sprintf("u_imean, cases %d to %d", i, j), one(t, d, "SELECT "+strings.Join(sel[i:j], ", ")), want[i:j])
			}
		}},
		{"DuckDB drops TRY_CAST's null where the cast is compared with a constant, so T-CAST-RANGE uses u_irange", func(t *testing.T) {
			xs := []int64{-351887289613166473, 23229, 1105936727514926853, 5}
			ss := []string{"300", "5", "x", "-129"}
			tab := table("tc", col{"x", model.Int64, []any{xs[0], xs[1], xs[2], xs[3]}}, col{"s", model.String, []any{ss[0], ss[1], ss[2], ss[3]}})
			rec := WithRID(tab.Rec)
			defer rec.Release()
			if err := d.Register("u_tc", rec); err != nil {
				t.Fatal(err)
			}
			var cast, dropped, ranged, text []any
			for i, x := range xs {
				in := x >= math.MinInt32 && x <= math.MaxInt32
				dropped = append(dropped, x > 18988) // the uncast comparison: DuckDB's answer
				if in {
					cast, ranged = append(cast, x), append(ranged, x > 18988)
				} else {
					cast, ranged = append(cast, nil), append(ranged, nil)
				}
				if v, err := strconv.ParseInt(ss[i], 10, 8); err == nil {
					text = append(text, v > 5)
				} else {
					text = append(text, nil)
				}
			}
			q := func(e string) []any { return column(t, d, "SELECT "+e+` FROM "u_tc" ORDER BY "__rid"`) }
			check(t, "TRY_CAST(x AS INTEGER)", q(`TRY_CAST("x" AS INTEGER)`), cast)
			check(t, "TRY_CAST(x AS INTEGER) > 18988, the null dropped", q(`TRY_CAST("x" AS INTEGER) > CAST('18988' AS INTEGER)`), dropped)
			check(t, "u_irange(x) > 18988", q(`CAST(u_irange("x", CAST('-2147483648' AS BIGINT), CAST('2147483647' AS BIGINT)) AS INTEGER) > CAST('18988' AS INTEGER)`), ranged)
			check(t, "TRY_CAST(s AS TINYINT) > 5", q(`TRY_CAST("s" AS TINYINT) > CAST('5' AS TINYINT)`), text)
		}},
		{"sum of BIGINT is exact past int64, exported as Decimal128(38, 0); count as Int64 (T-SUM, T-COUNT)", func(t *testing.T) {
			rs, schema := rows(t, d, "SELECT sum(x), count(*) FROM (VALUES (CAST('9223372036854775807' AS BIGINT)), "+
				"(CAST('9223372036854775807' AS BIGINT))) AS v(x)")
			want := new(big.Int).Mul(big.NewInt(math.MaxInt64), big.NewInt(2))
			check(t, "sum, count", rs[0], []any{want, int64(2)})
			if dt, ok := schema.Field(0).Type.(*arrow.Decimal128Type); !ok || dt.Scale != 0 {
				t.Errorf("sum is exported as %s, not a Decimal128 of scale 0", schema.Field(0).Type)
			}
			if schema.Field(1).Type.ID() != arrow.INT64 {
				t.Errorf("count is exported as %s, not an Int64", schema.Field(1).Type)
			}
		}},
		{"VARCHAR orders, compares, mins and maxes bytewise (T-EXTREMUM)", func(t *testing.T) {
			ss := []string{"b", "a", "", "a\x00b", "a\x01", "é", "z", "日本語", "🐻", "A", "a", "ab", "\U0010FFFF"}
			got := column(t, d, "SELECT x FROM "+values(strLits(ss))+" ORDER BY x, i")
			sorted := slices.Clone(ss)
			slices.Sort(sorted)
			want := make([]any, len(sorted))
			for i, s := range sorted {
				want[i] = s
			}
			check(t, "ORDER BY", got, want)
			check(t, "min, max", one(t, d, "SELECT min(x), max(x) FROM "+values(strLits(ss))),
				[]any{slices.Min(ss), slices.Max(ss)})
		}},
		{"min and max of BOOLEAN are false and true, nulls skipped (T-EXTREMUM)", func(t *testing.T) {
			got := one(t, d, "SELECT min(x), max(x) FROM (VALUES (true), (NULL), (false)) AS v(x)")
			check(t, "min, max", got, []any{false, true})
		}},
		{"printf('%04d') pads to four digits and does not cut (T-DATE-TEXT)", func(t *testing.T) {
			is := []int64{0, 7, 123, 2024, 12345, 5881580}
			lits := make([]string, len(is))
			want := make([]any, len(is))
			for i, v := range is {
				lits[i] = fmt.Sprintf("CAST('%d' AS BIGINT)", v)
				want[i] = fmt.Sprintf("%04d", v)
			}
			check(t, "printf", mapped(t, d, lits, "printf('%04d', x)"), want)
		}},
		{"lpad cuts a string longer than its width", func(t *testing.T) {
			ss := []string{"12345", "7", "1234"}
			want := make([]any, len(ss))
			for i, s := range ss {
				if len(s) > 4 {
					want[i] = s[:4]
				} else {
					want[i] = strings.Repeat("0", 4-len(s)) + s
				}
			}
			check(t, "lpad", mapped(t, d, strLits(ss), "lpad(x, 4, '0')"), want)
		}},
		{"regexp_full_match matches the whole text, by RE2 (T-PARSE-INT, T-PARSE-FLOAT, T-PARSE-DATE)", func(t *testing.T) {
			pats := []string{`[+-]?[0-9]+`, `[0-9]{4}-[0-9]{2}-[0-9]{2}`, `(?i)[+-]?nan`, `[+-]?([0-9]+\.?[0-9]*|\.[0-9]+)([eE][+-]?[0-9]+)?`}
			ss := []string{"12", "a12", "12a", "+12", "2024-01-01", "2024-01-01x", "NaN", "-nAn", "1.5e3", ".5", "5.", "", "1\n2", "١٢"}
			for _, p := range pats {
				re := regexp.MustCompile(`^(?:` + p + `)$`)
				want := make([]any, len(ss))
				for i, s := range ss {
					want[i] = re.MatchString(s)
				}
				check(t, p, mapped(t, d, strLits(ss), "regexp_full_match(x, "+sqlgen.String(p)+")"), want)
			}
		}},
		{"list_reduce folds in the list's order from its initial value (T-FLOAT-AGG)", func(t *testing.T) {
			fs := []float64{1e16, 1, -1e16, 1, 0.1, 0.2}
			want := 0.0
			for _, f := range fs {
				want += f
			}
			got := one(t, d, "SELECT list_reduce(["+strings.Join(floatLits(fs), ", ")+"], lambda u_a, u_b: u_a + u_b, "+sqlgen.Float(0)+")")
			check(t, "list_reduce", got, []any{want})
		}},
		{"list(x ORDER BY o) FILTER (WHERE x IS NOT NULL) keeps o's order and drops nulls (T-FLOAT-AGG)", func(t *testing.T) {
			got := one(t, d, "SELECT array_to_string(list(x ORDER BY o) FILTER (WHERE x IS NOT NULL), ',') FROM "+
				"(VALUES (3, 'c'), (1, 'a'), (2, NULL), (0, 'z'), (4, 'b')) AS v(o, x)")
			check(t, "list", got, []any{strings.Join([]string{"z", "a", "c", "b"}, ",")})
		}},
		{"decode(from_hex()) holds any text, a NUL too (T-LIT-TYPE)", func(t *testing.T) {
			ss := []string{"a\x00b", "日本語", "\U0010FFFF", "🏳️‍🌈"}
			var sel []string
			var want []any
			for _, s := range ss {
				sel = append(sel, "decode(from_hex('"+hex.EncodeToString([]byte(s))+"'))", "octet_length(encode(decode(from_hex('"+hex.EncodeToString([]byte(s))+"'))))")
				want = append(want, s, int64(len(s)))
			}
			check(t, "decode", one(t, d, "SELECT "+strings.Join(sel, ", ")), want)
		}},
		{"AND, OR and NOT are Kleene (T-KLEENE)", func(t *testing.T) {
			vals := []any{true, false, nil}
			lit := map[any]string{true: "true", false: "false", nil: "CAST(NULL AS BOOLEAN)"}
			and := func(a, b any) any {
				if a == false || b == false {
					return false
				}
				if a == nil || b == nil {
					return nil
				}
				return true
			}
			or := func(a, b any) any {
				if a == true || b == true {
					return true
				}
				if a == nil || b == nil {
					return nil
				}
				return false
			}
			for _, a := range vals {
				for _, b := range vals {
					var not any
					if a != nil {
						not = !a.(bool)
					}
					got := one(t, d, "SELECT "+lit[a]+" AND "+lit[b]+", "+lit[a]+" OR "+lit[b]+", NOT "+lit[a])
					check(t, fmt.Sprintf("%v, %v", a, b), got, []any{and(a, b), or(a, b), not})
				}
			}
		}},
		{"a CASE whose condition is null takes the ELSE, as ursus's When (T-WHEN-NULL)", func(t *testing.T) {
			when := func(cond any, then, els int64) int64 { // cond.go: a null condition is false
				if cond == true {
					return then
				}
				return els
			}
			got := one(t, d, "SELECT CASE WHEN CAST(NULL AS BOOLEAN) THEN 1 ELSE 2 END, CASE WHEN true THEN 1 ELSE 2 END")
			check(t, "CASE", got, []any{when(nil, 1, 2), when(true, 1, 2)})
		}},
		{"a registered table of no rows reads back as no rows", func(t *testing.T) {
			tab := gen.Table(1, "empty", 0)
			rec := WithRID(tab.Rec)
			defer rec.Release()
			if err := d.Register("u_empty", rec); err != nil {
				t.Fatal(err)
			}
			check(t, "count", one(t, d, `SELECT count(*) FROM "u_empty"`), []any{rec.NumRows()})
		}},
		{"a DATE is ±infinity at int32's greatest and least-but-one days, so a Date is an INTEGER (T-DATE-DAYS's reason)", func(t *testing.T) {
			days := []int64{math.MaxInt32, -math.MaxInt32, 0, 2932896, -719162}
			tab := table("dates", col{"d", model.Date, []any{days[0], days[1], days[2], days[3], days[4]}})
			if err := d.Register("u_dates", tab.Rec); err != nil {
				t.Fatal(err)
			}
			want := make([]any, len(days))
			for i, v := range days {
				want[i] = v == math.MaxInt32 || v == -math.MaxInt32
			}
			check(t, "isinf(d)", column(t, d, `SELECT isinf("d") FROM "u_dates"`), want)
		}},
	}
	for _, f := range facts {
		t.Run(f.name, f.run)
	}
	if len(facts) < 30 {
		t.Errorf("%d facts", len(facts))
	}
}
