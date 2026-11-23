package sqlgen

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/advenn/ursus/qcheck/ast"
	"github.com/advenn/ursus/qcheck/model"
)

// The wide types' and operations' translation (step 539): the unsigned integers,
// Float32, Decimal and the temporal types, and the string and temporal namespaces'
// functions. A Datetime, a Duration and a Time are their ticks in SQL, a BIGINT, as
// a Date is its days (T-DATE-DAYS), so each is computed from the ticks as ursus
// computes it. The X- rules name what is not translated, and why: a query holding
// one is excluded, and the self check alone runs it.

// The step 539 rule IDs.
const (
	TUintWrap      = "T-UINT-WRAP"
	TF32           = "T-F32"
	TDec           = "T-DEC"
	TDecFloat      = "T-DEC-FLOAT"
	TDecText       = "T-DEC-TEXT"
	TDecSum        = "T-DEC-SUM"
	TTicks         = "T-TICKS"
	TRetime        = "T-RETIME"
	TInstantArith  = "T-INSTANT-ARITH"
	TCivil         = "T-CIVIL"
	TGrid          = "T-GRID"
	TOffset        = "T-OFFSET"
	TDateOf        = "T-DATE-OF"
	TZoneTicks     = "T-ZONE-TICKS"
	TTotal         = "T-TOTAL"
	TInstantText   = "T-INSTANT-TEXT"
	TStrMatch      = "T-STR-MATCH"
	TStrReplace    = "T-STR-REPLACE"
	TStrLen        = "T-STR-LEN"
	TStrSlice      = "T-STR-SLICE"
	TStrStrip      = "T-STR-STRIP"
	TStrSplit      = "T-STR-SPLIT"
	TParseUint     = "T-PARSE-UINT"
	XCaseMap       = "X-CASE-MAP"
	XZoneWall      = "X-ZONE-WALL"
	XTemporalText  = "X-TEMPORAL-TEXT"
	XParseTemporal = "X-PARSE-TEMPORAL"
	XParseDec      = "X-PARSE-DEC"
	XF32Text       = "X-F32-TEXT"
	XF32Agg        = "X-F32-AGG"
	XF32FloorMod   = "X-F32-FLOORMOD"
	XF64F32        = "X-F64-F32"
	XFloatDec      = "X-FLOAT-DEC"
	XDecDiv        = "X-DEC-DIV"
	XDecMean       = "X-DEC-MEAN"
	XDurMean       = "X-DUR-MEAN"
)

var wideRules = []Rule{
	{TUintWrap, "expr.go arithmetic and dtype/promote.go: an unsigned integer's +, - and * wrap modulo its width",
		"computed in HUGEINT, or UHUGEINT for a product of two UBIGINTs, and reduced by u_uw8 to u_uw64; a signed and an unsigned operand meet at dtype.Promote's signed type, wrapped by u_w8 to u_w64"},
	{TF32, "dtype/promote.go: two Float32 operands, or Float32 beside an integer of 16 bits or fewer, meet at Float32, each operation rounded once to it",
		"+, -, * and / in FLOAT, which DuckDB computes in single precision; a comparison, a sort key, Min, Max and NUnique of the exact DOUBLE a FLOAT widens to"},
	{TDec, "dtype/promote.go DecimalMeet and expr/resolve.go resolveDecimal: a Decimal meets an integer or a Decimal at the Decimal holding both, its +, - and * exact; a cast keeps every digit or is null when lossy",
		"DECIMAL arithmetic over operands CAST to their DECIMAL, the answer CAST to the AST's; a lossy narrowing a CASE on the digits and the range"},
	{TDecFloat, "expr/resolve.go resolveDecimal: a Decimal beside a float converts to its nearest Float64",
		"u_d2f: the Decimal's exact text read by DuckDB's correctly rounded parser"},
	{TDecText, "dtype.FormatDecimal: a Decimal's text holds every digit of its scale, \"1.50\", \"-0.05\"",
		"CAST AS VARCHAR"},
	{TDecSum, "agg.go: a Decimal's Sum is the exact Decimal(38) of its scale, null for a group of no value",
		"sum, CAST to DECIMAL(38, s)"},
	{TTicks, "dtype.Datetime and data/timeday.go: a Datetime, a Duration and a Time are int64 ticks of their unit",
		"a BIGINT of ticks in SQL, as duck.Load registers each: DuckDB's TIMESTAMP reads int64's ends as infinities and its TIMESTAMPTZ is microseconds"},
	{TRetime, "dtype.Promote and kernel/dtfn.go: a finer unit multiplies the ticks, a lossy cast null past int64; a coarser floors an instant's and truncates a Duration's toward zero",
		"tick arithmetic: a multiplication in HUGEINT, u_idiv for an instant, // for a Duration"},
	{TInstantArith, "expr/resolve.go resolveTemporalArithmetic: an instant less one of its kind is a Duration at the finer unit, a Date's in seconds; an instant plus a Duration retimed to its unit keeps its type, a Date's a Datetime(s); a Duration times an integer is a Duration",
		"tick arithmetic, each operand retimed as T-RETIME says"},
	{TCivil, "kernel/dtfn.go component: a naive Datetime's, a Date's and a Time's year, month, day, ISO weekday, ordinal day, hour, minute, second and the second's milli-, micro- and nanoseconds, of the proleptic Gregorian calendar",
		"Hinnant's civil_from_days over the ticks' floored days (u_civil), the time of day from their floored remainder"},
	{TGrid, "expr_dt.go Truncate and Round: a time.Duration floors the instant, zone or not, to a multiple of the period; Round to the nearer, a half up",
		"u_idiv of the ticks by the period's, times the period, one more where twice the remainder reaches it"},
	{TOffset, "expr_dt.go OffsetBy: nanoseconds move elapsed time; days and months move a naive wall clock, a month's day past its end clamped",
		"ticks added; months by u_addmonths over u_civil, the day clamped by u_mdays"},
	{TDateOf, "expr_dt.go Date and Time, and Cast: a naive instant's date and time of day are its ticks floored by a day and their remainder; a cast reads the UTC wall clock, a zoned instant's too",
		"u_idiv and u_imod of the ticks by a day's"},
	{TZoneTicks, "expr_dt.go ConvertTimeZone, and Cast: another zone keeps the instant's ticks",
		"the ticks as they are"},
	{TTotal, "expr_dt.go TotalSeconds: the Total family truncates the ticks toward zero", "// by the unit's ticks"},
	{TInstantText, "dtype.FormatTemporal: a naive Datetime's text is 2006-01-02T15:04:05 and its unit's digits of a second, a Time's 15:04:05 and its",
		"u_tstext and u_todtext: u_dtext of the days, the time of day printf'd"},
	{TStrMatch, "expr_str.go Contains, StartsWith and EndsWith: a literal pattern, or RE2's, which DuckDB's regexp is",
		"contains, regexp_matches, starts_with and suffix"},
	{TStrReplace, "expr_str.go Replace and ReplaceAll: the first match or every one, a literal pattern taken as it is; qcheck's values hold neither $ nor \\",
		"regexp_replace of the pattern, quoted as RE2 reads it when literal, with 'g' for every match; replace for every literal match"},
	{TStrLen, "expr_str.go LenBytes and LenChars: bytes and runes, a Uint32", "strlen and length, CAST to UINTEGER"},
	{TStrSlice, "expr_str.go Slice: runes from the offset, a negative one from the end, a start before the first cutting the length (step 291)",
		"u_sslice: substring of the clipped window"},
	{TStrStrip, "expr_str.go StripPrefix and StripSuffix: a prefix or a suffix removed where there is one",
		"u_sprefix and u_ssuffix"},
	{TStrSplit, "expr_str.go Split: the parts of strings.Split, one for empty text; List().Len() their count", "len(string_split()), CAST to UINTEGER"},
	{TParseUint, "kernel/castparse.go: text to an unsigned integer is [+-]?[0-9]+ in range, -0 zero",
		"regexp_full_match, TRY_CAST to HUGEINT, u_irange of the target's range"},
	{XCaseMap, "expr_str.go ToUpper and ToLower: Unicode's full case mapping, ß to SS and İ to i̇ (step 539's probe)",
		"not translated: DuckDB documents no such mapping; the self check runs them"},
	{XZoneWall, "expr_dt.go and feature-gaps §9: a zoned Datetime's components, Date, Time and calendar OffsetBy read its zone's wall clock from the system's zone database",
		"not translated: DuckDB's zones are ICU's, its TIMESTAMPTZ microseconds"},
	{XTemporalText, "dtype.FormatTemporal: a zoned Datetime's text has its offset, a Duration's is the frame's display",
		"not translated"},
	{XParseTemporal, "feature-gaps §9 (step 356): text to a Datetime or a Time reads each value by its own pattern",
		"not translated"},
	{XParseDec, "feature-gaps §9: text to a Decimal rounds a tie half away from zero, by ursus's grammar", "not translated"},
	{XF32Text, "kernel/caststring.go FloatText: a Float32's text is its own shortest digits, and text is parsed at 32 bits",
		"not translated"},
	{XF32Agg, "agg.go: a Float32 Sum or Mean accumulates as ursus's kernel does", "not translated"},
	{XF32FloorMod, "expr.go FloorDiv and Mod of Float32: as ursus's kernel rounds them", "not translated"},
	{XF64F32, "kernel/cast.go: Float64 to Float32 rounds, null only past the range's rounding boundary", "not translated"},
	{XFloatDec, "kernel/cast.go: a float to a Decimal is its shortest digits, exact at the scale or null (step 539's probe)",
		"not translated"},
	{XDecDiv, "expr/resolve.go resolveDecimal: a Decimal's quotient is the Float64 nearest the exact one", "not translated: DuckDB divides in DOUBLE"},
	{XDecMean, "agg.go: a Decimal's Mean is the Float64 nearest the exact one", "not translated"},
	{XDurMean, "agg.go: a Duration's Mean", "not translated"},
}

var wideMacros = []string{
	// u_uw8 to u_uw64 reduce v modulo 2^bits (T-UINT-WRAP).
	`CREATE OR REPLACE MACRO u_uwrap(u_v, u_m) AS ((CAST(u_v AS HUGEINT) % u_m) + u_m) % u_m`,
	`CREATE OR REPLACE MACRO u_uw8(u_v) AS CAST(u_uwrap(u_v, CAST('256' AS HUGEINT)) AS UTINYINT)`,
	`CREATE OR REPLACE MACRO u_uw16(u_v) AS CAST(u_uwrap(u_v, CAST('65536' AS HUGEINT)) AS USMALLINT)`,
	`CREATE OR REPLACE MACRO u_uw32(u_v) AS CAST(u_uwrap(u_v, CAST('4294967296' AS HUGEINT)) AS UINTEGER)`,
	`CREATE OR REPLACE MACRO u_uw64(u_v) AS CAST(u_uwrap(u_v, CAST('18446744073709551616' AS HUGEINT)) AS UBIGINT)`,
	`CREATE OR REPLACE MACRO u_uw64u(u_v) AS CAST(CAST(u_v AS UHUGEINT) % CAST('18446744073709551616' AS UHUGEINT) AS UBIGINT)`,

	// u_d2f is a Decimal's nearest Float64 (T-DEC-FLOAT).
	`CREATE OR REPLACE MACRO u_d2f(u_x) AS CAST(CAST(u_x AS VARCHAR) AS DOUBLE)`,

	// u_f2h is a float as a HUGEINT where it is an integer in [lo, hi), else null.
	`CREATE OR REPLACE MACRO u_f2h(u_x, u_lo, u_hi) AS CASE WHEN u_x >= u_lo AND u_x < u_hi AND u_x = trunc(u_x) THEN CAST(u_x AS HUGEINT) END`,

	// u_brange is a HUGEINT as a BIGINT where it fits, else null (T-RETIME).
	`CREATE OR REPLACE MACRO u_brange(u_h) AS CAST(u_irange(CAST(u_h AS HUGEINT), CAST('-9223372036854775808' AS HUGEINT), CAST('9223372036854775807' AS HUGEINT)) AS BIGINT)`,

	// u_civil is Hinnant's civil_from_days, a struct of the year, month and day of
	// the days since 1970-01-01 (T-CIVIL).
	`CREATE OR REPLACE MACRO u_cv4(u_y, u_doy, u_mp) AS {'y': u_y + (CASE WHEN u_mp >= 10 THEN 1 ELSE 0 END),
		'm': u_mp + (CASE WHEN u_mp < 10 THEN 3 ELSE -9 END), 'd': u_doy - (153 * u_mp + 2) // 5 + 1}`,
	`CREATE OR REPLACE MACRO u_cv3(u_yoe, u_doe, u_era) AS list_transform(
		[{'doy': u_doe - (365 * u_yoe + u_yoe // 4 - u_yoe // 100), 'y': u_yoe + u_era * 400}],
		lambda u_r: u_cv4(u_r.y, u_r.doy, (5 * u_r.doy + 2) // 153))[1]`,
	`CREATE OR REPLACE MACRO u_cv2(u_doe, u_era) AS list_transform(
		[{'yoe': (u_doe - u_doe // 1460 + u_doe // 36524 - u_doe // 146096) // 365, 'doe': u_doe, 'era': u_era}],
		lambda u_r: u_cv3(u_r.yoe, u_r.doe, u_r.era))[1]`,
	`CREATE OR REPLACE MACRO u_cv1(u_z) AS list_transform(
		[{'doe': u_z - u_fdiv(u_z, 146097) * 146097, 'era': u_fdiv(u_z, 146097)}],
		lambda u_r: u_cv2(u_r.doe, u_r.era))[1]`,
	`CREATE OR REPLACE MACRO u_civil(u_d0) AS CASE WHEN u_d0 IS NULL THEN NULL
		ELSE list_transform([CAST(u_d0 AS BIGINT) + 719468], lambda u_z: u_cv1(u_z))[1] END`,
	// u_ordinal is the day of the year, 1 to 366.
	`CREATE OR REPLACE MACRO u_ordinal(u_d) AS CAST(u_d AS BIGINT) - u_civ(struct_extract(u_civil(u_d), 'y') - 1, 1, 1) + 1`,
	// u_addmonths is the days of the date n months on, its day clamped to the month's
	// last (T-OFFSET).
	`CREATE OR REPLACE MACRO u_addm2(u_t, u_d) AS list_transform(
		[{'y': u_fdiv(u_t, 12), 'm': u_t - u_fdiv(u_t, 12) * 12 + 1}],
		lambda u_q: u_civ(u_q.y - (CASE WHEN u_q.m <= 2 THEN 1 ELSE 0 END), u_q.m, least(u_d, u_mdays(u_q.y, u_q.m))))[1]`,
	`CREATE OR REPLACE MACRO u_addmonths(u_d0, u_n) AS list_transform([u_civil(u_d0)],
		lambda u_c: u_addm2(u_c.y * 12 + (u_c.m - 1) + u_n, u_c.d))[1]`,

	// u_todtext is a time of day of per ticks a second as 15:04:05 and digits of
	// its fraction; u_tstext a naive Datetime's text (T-INSTANT-TEXT). per is
	// multiplied as a BIGINT: DuckDB types the literal 1000000000 an INTEGER, and
	// 3600 times it overflowed INT32 (#96's qcheck-duckdb, seeds 20261257 and
	// 20261475).
	`CREATE OR REPLACE MACRO u_todtext(u_t, u_per, u_digits) AS
		printf('%02d:%02d:%02d', u_t // (3600 * CAST(u_per AS BIGINT)), (u_t // (60 * CAST(u_per AS BIGINT))) % 60,
			(u_t // u_per) % 60)
		|| (CASE WHEN u_digits > 0 THEN '.' || lpad(CAST(u_t % u_per AS VARCHAR), u_digits, '0') ELSE '' END)`,
	`CREATE OR REPLACE MACRO u_tstext(u_x, u_per, u_digits) AS CASE WHEN u_x IS NULL THEN NULL ELSE
		u_dtext(u_idiv(u_x, 86400 * CAST(u_per AS BIGINT))) || 'T' ||
		u_todtext(u_imod(u_x, 86400 * CAST(u_per AS BIGINT)), u_per, u_digits) END`,

	// u_sslice is Slice's window (T-STR-SLICE): from the offset, a negative one from
	// the end, a start before the first cutting the length.
	`CREATE OR REPLACE MACRO u_sslice(u_s0, u_o, u_l) AS list_transform([{'n': length(u_s0), 's': u_s0}], lambda u_r: CASE
		WHEN u_r.s IS NULL THEN NULL
		WHEN u_o >= 0 THEN substring(u_r.s, u_o + 1, u_l)
		WHEN u_r.n + u_o >= 0 THEN substring(u_r.s, u_r.n + u_o + 1, u_l)
		ELSE substring(u_r.s, 1, greatest(u_l + (u_r.n + u_o), 0)) END)[1]`,
	// u_sprefix and u_ssuffix are StripPrefix and StripSuffix (T-STR-STRIP).
	`CREATE OR REPLACE MACRO u_sprefix(u_s, u_p) AS CASE WHEN starts_with(u_s, u_p) THEN substring(u_s, length(u_p) + 1) ELSE u_s END`,
	`CREATE OR REPLACE MACRO u_ssuffix(u_s, u_p) AS CASE WHEN suffix(u_s, u_p) THEN substring(u_s, 1, length(u_s) - length(u_p)) ELSE u_s END`,
}

func init() {
	Rules = append(Rules, wideRules...)
	Macros = append(Macros, wideMacros...)
}

// --- types and literals -----------------------------------------------------------

func wideType(t model.Type) (string, bool) {
	switch {
	case t.IsDatetime(), t.IsDuration(), t.IsTime():
		return "BIGINT", true
	case t.IsDecimal():
		return fmt.Sprintf("DECIMAL(%d, %d)", t.Prec(), t.Scale()), true
	}
	switch t {
	case model.Uint8:
		return "UTINYINT", true
	case model.Uint16:
		return "USMALLINT", true
	case model.Uint32:
		return "UINTEGER", true
	case model.Float32:
		return "FLOAT", true
	}
	return "", false
}

func wideLit(l *ast.Lit) (string, bool) {
	t := l.T
	switch {
	case t.IsUnsigned():
		return "CAST('" + strconv.FormatUint(l.V.(uint64), 10) + "' AS " + Type(t) + ")", true
	case t == model.Float32:
		return Float32(l.V.(float32)), true
	case t.IsDatetime(), t.IsDuration(), t.IsTime():
		return "CAST('" + strconv.FormatInt(l.V.(int64), 10) + "' AS BIGINT)", true
	case t.IsDecimal():
		return "CAST('" + ast.DecimalText(l.V.(string), t.Scale()) + "' AS " + Type(t) + ")", true
	}
	return "", false
}

// Float32 is f as a FLOAT literal of its bits: its shortest text at 32 bits.
func Float32(f float32) string {
	g := float64(f)
	switch {
	case math.IsNaN(g):
		return "CAST('nan' AS FLOAT)"
	case math.IsInf(g, 1):
		return "CAST('inf' AS FLOAT)"
	case math.IsInf(g, -1):
		return "CAST('-inf' AS FLOAT)"
	}
	return "CAST('" + strconv.FormatFloat(g, 'g', -1, 32) + "' AS FLOAT)"
}

// hugeRange is t's least and greatest values as HUGEINT literals.
func hugeRange(t model.Type) string {
	var lo, hi string
	switch {
	case t.IsUnsigned():
		lo, hi = "0", strconv.FormatUint(math.MaxUint64>>(64-t.Bits()), 10)
	default:
		lo, hi = strconv.FormatInt(int64(-1)<<(t.Bits()-1), 10), strconv.FormatInt(int64(1)<<(t.Bits()-1)-1, 10)
	}
	return "CAST('" + lo + "' AS HUGEINT), CAST('" + hi + "' AS HUGEINT)"
}

// pow10 is 10^n as a HUGEINT literal.
func pow10(n int) string { return "CAST('1" + strings.Repeat("0", n) + "' AS HUGEINT)" }

// --- coercion ---------------------------------------------------------------------

// coerce is s, of type from, as type to where to holds every value from does, as a
// comparison, a branch, a fill or an operand meets them; ok false where it is no
// such meeting.
func (b *builder) coerce(s string, from, to model.Type) (string, bool) {
	switch {
	case from == to:
		return s, true
	case from.IsInt() && (to.IsInt() || to.IsFloat()), from == model.Float32 && to == model.Float64:
		b.fire(TPromote)
		return "CAST(" + s + " AS " + Type(to) + ")", true
	case from == model.Float64 && (to.IsInt() || to == model.Float32):
		// A weak fill's literal, which the column's type holds (fill.go).
		b.fire(TFillWeak)
		return "CAST(" + s + " AS " + Type(to) + ")", true
	case (from.IsInt() || from.IsDecimal()) && to.IsDecimal():
		b.fire(TDec)
		return "CAST(" + s + " AS " + Type(to) + ")", true
	case from.IsDecimal() && to == model.Float64:
		b.fire(TDecFloat)
		return "u_d2f(" + s + ")", true
	case from.IsTemporal() && from.Kind() == to.Kind() && from != model.Date && to.Unit() >= from.Unit():
		b.fire(TRetime)
		return finer(s, from.Unit(), to.Unit()), true
	}
	return "", false
}

// finer is ticks of unit from as ticks of the finer unit to, in BIGINT.
func finer(s string, from, to model.Unit) string {
	if from == to {
		return s
	}
	return fmt.Sprintf("CAST(CAST(%s AS HUGEINT) * %d AS BIGINT)", s, to.PerSecond()/from.PerSecond())
}

// coarserInstant floors an instant's ticks of unit from to the coarser unit to.
func coarserInstant(s string, from, to model.Unit) string {
	if from == to {
		return s
	}
	return fmt.Sprintf("CAST(u_idiv(%s, %d) AS BIGINT)", s, from.PerSecond()/to.PerSecond())
}

// coarserDuration truncates a Duration's ticks of unit from toward zero to the
// coarser unit to.
func coarserDuration(s string, from, to model.Unit) string {
	if from == to {
		return s
	}
	return fmt.Sprintf("(CAST(%s AS BIGINT) // %d)", s, from.PerSecond()/to.PerSecond())
}

// --- arithmetic ---------------------------------------------------------------------

// wideBin is a binary node the step 539 rules translate, or false for one the core
// translation takes.
func (b *builder) wideBin(e *ast.Bin) (string, bool) {
	lt, rt := e.L.Type(), e.R.Type()
	if e.Op.IsCompare() || e.Op == ast.And || e.Op == ast.Or {
		return "", false
	}
	if lt.IsTemporal() || rt.IsTemporal() {
		return b.temporalBin(e), true
	}
	switch {
	case e.T.IsDecimal():
		b.fire(TDec)
		op := map[ast.BinOp]string{ast.Add: " + ", ast.Sub: " - ", ast.Mul: " * "}[e.Op]
		return "CAST((" + b.asDecimal(e.L) + op + b.asDecimal(e.R) + ") AS " + Type(e.T) + ")", true
	case e.Op == ast.Div && (lt.IsDecimal() || rt.IsDecimal()):
		b.refuse(XDecDiv, "a Decimal's quotient")
	case e.T == model.Float32:
		switch e.Op {
		case ast.FloorDiv, ast.Mod:
			b.refuse(XF32FloorMod, "%s of Float32", e.Op)
		case ast.Div:
			b.fire(TDivFloat)
		}
		b.fire(TF32)
		op := map[ast.BinOp]string{ast.Add: " + ", ast.Sub: " - ", ast.Mul: " * ", ast.Div: " / "}[e.Op]
		return "(" + b.cast(e.L, e.T) + op + b.cast(e.R, e.T) + ")", true
	case e.T.IsUnsigned():
		switch e.Op {
		case ast.Add, ast.Mul:
			b.fire(TUintWrap)
			op := map[ast.BinOp]string{ast.Add: " + ", ast.Mul: " * "}[e.Op]
			if e.T == model.Uint64 {
				return "u_uw64u(CAST(" + b.expr(e.L) + " AS UHUGEINT)" + op + "CAST(" + b.expr(e.R) + " AS UHUGEINT))", true
			}
			return fmt.Sprintf("u_uw%d(CAST(%s AS HUGEINT)%sCAST(%s AS HUGEINT))", e.T.Bits(), b.expr(e.L), op, b.expr(e.R)), true
		case ast.Sub:
			b.fire(TUintWrap)
			return fmt.Sprintf("u_uw%d(CAST(%s AS HUGEINT) - CAST(%s AS HUGEINT))", e.T.Bits(), b.expr(e.L), b.expr(e.R)), true
		case ast.FloorDiv:
			b.fire(TFloor)
			return "CAST(u_idiv(" + b.expr(e.L) + ", " + b.expr(e.R) + ") AS " + Type(e.T) + ")", true
		}
	}
	return "", false
}

// asDecimal is a Decimal arithmetic's operand: a Decimal as it is, an integer as
// the Decimal of its digits (expr/resolve.go resolveDecimal).
func (b *builder) asDecimal(e ast.Expr) string {
	t := e.Type()
	if t.IsDecimal() {
		return b.expr(e)
	}
	p, _, _ := model.DecimalDigits(t)
	return "CAST(" + b.expr(e) + " AS DECIMAL(" + strconv.Itoa(p) + ", 0))"
}

// temporalBin is tick arithmetic (T-INSTANT-ARITH).
func (b *builder) temporalBin(e *ast.Bin) string {
	b.fire(TInstantArith)
	lt, rt := e.L.Type(), e.R.Type()
	l, r := b.expr(e.L), b.expr(e.R)
	switch {
	case e.Op == ast.Sub && lt.Kind() == rt.Kind() && !lt.IsDuration():
		if lt == model.Date {
			return "((CAST(" + l + " AS BIGINT) - CAST(" + r + " AS BIGINT)) * 86400)"
		}
		u := e.T.Unit()
		return "(" + finer(l, lt.Unit(), u) + " - " + finer(r, rt.Unit(), u) + ")"
	case lt.IsDuration() && rt.IsDuration():
		u := e.T.Unit()
		op := map[ast.BinOp]string{ast.Add: " + ", ast.Sub: " - "}[e.Op]
		return "(" + finer(l, lt.Unit(), u) + op + finer(r, rt.Unit(), u) + ")"
	case e.Op == ast.Mul:
		return "(CAST(" + l + " AS BIGINT) * CAST(" + r + " AS BIGINT))"
	}
	// An instant and a Duration, in either order for +.
	inst, it, dur, dt := l, lt, r, rt
	if lt.IsDuration() {
		inst, it, dur, dt = r, rt, l, lt
	}
	u := e.T.Unit()
	if it == model.Date {
		inst = "(CAST(" + inst + " AS BIGINT) * 86400)"
	}
	if dt.Unit() > u {
		dur = coarserDuration(dur, dt.Unit(), u)
	} else {
		dur = finer(dur, dt.Unit(), u)
	}
	op := map[ast.BinOp]string{ast.Add: " + ", ast.Sub: " - "}[e.Op]
	return "(" + inst + op + dur + ")"
}

// --- casts --------------------------------------------------------------------------

// wideConvert is a cast the step 539 rules translate, or false for one the core
// translation takes.
func (b *builder) wideConvert(e *ast.Cast, x string) (string, bool) {
	from, to := e.X.Type(), e.To
	switch {
	case to == model.String:
		switch {
		case from.IsUnsigned():
			b.fire(TText)
			return "CAST(" + x + " AS VARCHAR)", true
		case from == model.Float32:
			b.refuse(XF32Text, "Float32 to text")
		case from.IsDecimal():
			b.fire(TDecText)
			return "CAST(" + x + " AS VARCHAR)", true
		case from.IsDatetime() && from.Zone() == "":
			b.fire(TInstantText)
			return fmt.Sprintf("u_tstext(%s, %d, %d)", x, from.Unit().PerSecond(), 3*int(from.Unit())), true
		case from.IsTime():
			b.fire(TInstantText)
			return fmt.Sprintf("u_todtext(%s, %d, %d)", x, from.Unit().PerSecond(), 3*int(from.Unit())), true
		case from.IsDatetime(), from.IsDuration():
			b.refuse(XTemporalText, "%s to text", from)
		}
	case from == model.String:
		switch {
		case to.IsUnsigned():
			b.fire(TParseUint)
			return "(CASE WHEN regexp_full_match(" + x + ", '[+-]?[0-9]+') THEN CAST(u_irange(TRY_CAST(" + x +
				" AS HUGEINT), " + hugeRange(to) + ") AS " + Type(to) + ") END)", true
		case to == model.Float32:
			b.refuse(XF32Text, "text to Float32")
		case to.IsDecimal():
			b.refuse(XParseDec, "text to %s", to)
		case to.IsDatetime(), to.IsTime():
			b.refuse(XParseTemporal, "text to %s", to)
		}
	case to == model.Float32 && from == model.Float64:
		b.refuse(XF64F32, "Float64 to Float32")
	case from.IsFloat() && to.IsDecimal():
		b.refuse(XFloatDec, "%s to %s", from, to)
	case from.IsFloat() && to.IsInt():
		b.fire(TCastExact)
		lo, hi := floatRange(to)
		if to.IsUnsigned() {
			lo, hi = "CAST('0' AS DOUBLE)", fmt.Sprintf("CAST('%s' AS DOUBLE)", strconv.FormatFloat(math.Ldexp(1, to.Bits()), 'f', -1, 64))
		}
		return "CAST(u_f2h(CAST(" + x + " AS DOUBLE), " + lo + ", " + hi + ") AS " + Type(to) + ")", true
	case from.IsInt() && to.IsInt():
		if StrictInt(from, to) {
			b.fire(TPromote)
			return "CAST(" + x + " AS " + Type(to) + ")", true
		}
		b.fire(TCastRange)
		return "CAST(u_irange(CAST(" + x + " AS HUGEINT), " + hugeRange(to) + ") AS " + Type(to) + ")", true
	case (from.IsInt() || from == model.Bool) && to.IsFloat(), from == model.Float32 && to == model.Float64:
		b.fire(TPromote)
		if from == model.Bool {
			x = "CAST(" + x + " AS INTEGER)"
		}
		return "CAST(" + x + " AS " + Type(to) + ")", true
	case from == model.Bool && to.IsUnsigned():
		b.fire(TPromote)
		return "CAST(CAST(" + x + " AS INTEGER) AS " + Type(to) + ")", true
	case from.IsDecimal() && to == model.Float64:
		b.fire(TDecFloat)
		return "u_d2f(" + x + ")", true
	case from.IsInt() && to.IsDecimal():
		b.fire(TDec)
		if !e.Lossy {
			return "CAST(" + x + " AS " + Type(to) + ")", true
		}
		return "(CASE WHEN abs(CAST(" + x + " AS HUGEINT)) < " + pow10(to.Prec()-to.Scale()) + " THEN CAST(" + x +
			" AS " + Type(to) + ") END)", true
	case from.IsDecimal() && to.IsInt():
		b.fire(TDec)
		return "(CASE WHEN " + x + " = trunc(" + x + ") THEN CAST(u_irange(CAST(" + x + " AS HUGEINT), " + hugeRange(to) +
			") AS " + Type(to) + ") END)", true
	case from.IsDecimal() && to.IsDecimal():
		b.fire(TDec)
		if !e.Lossy {
			return "CAST(" + x + " AS " + Type(to) + ")", true
		}
		return fmt.Sprintf("(CASE WHEN round(%s, %d) = %s AND CAST(trunc(abs(%s)) AS HUGEINT) < %s THEN CAST(%s AS %s) END)",
			x, to.Scale(), x, x, pow10(to.Prec()-to.Scale()), x, Type(to)), true
	case (from.IsDatetime() || from.IsDuration() || from.IsTime()) && to == model.Int64,
		from == model.Int64 && (to.IsDatetime() || to.IsDuration()):
		b.fire(TTicks)
		return "CAST(" + x + " AS BIGINT)", true
	case from == model.Int64 && to.IsTime():
		b.fire(TTicks)
		return fmt.Sprintf("(CASE WHEN %s >= 0 AND %s < %d THEN CAST(%s AS BIGINT) END)", x, x, to.Unit().PerDay(), x), true
	case from.IsDatetime() && to.IsDatetime():
		b.fire(TRetime)
		if from.Zone() != to.Zone() {
			b.fire(TZoneTicks)
		}
		return retime(x, from.Unit(), to.Unit(), true), true
	case from.IsDuration() && to.IsDuration():
		b.fire(TRetime)
		return retime(x, from.Unit(), to.Unit(), false), true
	case from.IsTime() && to.IsTime():
		b.fire(TRetime)
		return retime(x, from.Unit(), to.Unit(), true), true
	case from.IsDatetime() && to.IsTime():
		b.fire(TDateOf)
		return retime(fmt.Sprintf("CAST(u_imod(%s, %d) AS BIGINT)", x, from.Unit().PerDay()), from.Unit(), to.Unit(), true), true
	case from.IsDatetime() && to == model.Date:
		b.fire(TDateOf)
		return fmt.Sprintf("CAST(u_irange(u_idiv(%s, %d), CAST('%d' AS HUGEINT), CAST('%d' AS HUGEINT)) AS INTEGER)", x,
			from.Unit().PerDay(), math.MinInt32, math.MaxInt32), true
	case from == model.Date && to.IsDatetime():
		b.fire(TDateOf)
		return fmt.Sprintf("u_brange(CAST(%s AS HUGEINT) * %d)", x, to.Unit().PerDay()), true
	}
	return "", false
}

// StrictInt is whether an integer cast from to to keeps every value.
func StrictInt(from, to model.Type) bool {
	switch {
	case from.IsSignedInt() == to.IsSignedInt():
		return to.Bits() >= from.Bits()
	case from.IsUnsigned():
		return to.Bits() > from.Bits()
	}
	return false
}

// retime is ticks of from as ticks of to: finer multiplied, null past int64; coarser
// floored, an instant's, or truncated toward zero, a Duration's (T-RETIME).
func retime(x string, from, to model.Unit, instant bool) string {
	switch {
	case to > from:
		return fmt.Sprintf("u_brange(CAST(%s AS HUGEINT) * %d)", x, to.PerSecond()/from.PerSecond())
	case instant:
		return coarserInstant(x, from, to)
	}
	return coarserDuration(x, from, to)
}

// --- aggregates ---------------------------------------------------------------------

// wideAgg is an aggregate the step 539 rules translate, or false for one the core
// translation takes.
func (b *builder) wideAgg(a *ast.Agg, x string) (string, bool) {
	xt := a.X.Type()
	switch {
	case xt == model.Float32:
		switch a.Op {
		case ast.Sum, ast.Mean:
			b.refuse(XF32Agg, "%s of Float32", a.Op)
		case ast.Min, ast.Max:
			b.fire(TExtremum)
			b.fire(TF32)
			fn := map[ast.AggOp]string{ast.Min: "u_fmin", ast.Max: "u_fmax"}[a.Op]
			return "CAST(" + fn + "(" + orderedList("CAST("+x+" AS DOUBLE)") + ") AS FLOAT)", true
		case ast.NUnique:
			b.fire(TCount)
			b.fire(TNUnique)
			b.fire(TKeyZero)
			return "CAST(count(DISTINCT u_fkey(CAST(" + x + " AS DOUBLE))) AS UBIGINT)", true
		}
	case xt.IsDecimal():
		switch a.Op {
		case ast.Sum:
			b.fire(TDecSum)
			return "CAST(sum(" + x + ") AS " + Type(a.T) + ")", true
		case ast.Mean:
			b.refuse(XDecMean, "Mean of %s", xt)
		}
	case xt.IsDuration():
		switch a.Op {
		case ast.Sum:
			b.fire(TSum)
			b.fire(TTicks)
			return "CAST(sum(CAST(" + x + " AS HUGEINT)) AS BIGINT)", true
		case ast.Mean:
			b.refuse(XDurMean, "Mean of %s", xt)
		}
	case xt.IsUnsigned():
		switch a.Op {
		case ast.Sum:
			b.fire(TSum)
			return "sum(CAST(" + x + " AS HUGEINT))", true
		case ast.Mean:
			b.fire(TMeanInt)
			return "u_imean(sum(CAST(" + x + " AS HUGEINT)), count(" + x + "))", true
		}
	}
	return "", false
}

// --- functions ----------------------------------------------------------------------

// fn is a namespace's function.
func (b *builder) fn(e *ast.Fn) string {
	x := b.expr(e.X)
	xt := e.X.Type()
	switch e.Op {
	case ast.StrContains:
		b.fire(TStrMatch)
		if e.B {
			return "contains(" + x + ", " + String(e.S[0]) + ")"
		}
		return "regexp_matches(" + x + ", " + String(e.S[0]) + ")"
	case ast.StrStartsWith:
		b.fire(TStrMatch)
		return "starts_with(" + x + ", " + String(e.S[0]) + ")"
	case ast.StrEndsWith:
		b.fire(TStrMatch)
		return "suffix(" + x + ", " + String(e.S[0]) + ")"
	case ast.StrReplace, ast.StrReplaceAll:
		b.fire(TStrReplace)
		if strings.ContainsAny(e.S[1], `\$`) {
			panic("sqlgen: a replacement holding $ or \\: " + e.S[1])
		}
		if e.Op == ast.StrReplaceAll && e.B {
			return "replace(" + x + ", " + String(e.S[0]) + ", " + String(e.S[1]) + ")"
		}
		pat := e.S[0]
		if e.B {
			pat = regexp.QuoteMeta(pat)
		}
		if e.Op == ast.StrReplaceAll {
			return "regexp_replace(" + x + ", " + String(pat) + ", " + String(e.S[1]) + ", 'g')"
		}
		return "regexp_replace(" + x + ", " + String(pat) + ", " + String(e.S[1]) + ")"
	case ast.StrLenBytes:
		b.fire(TStrLen)
		return "CAST(strlen(" + x + ") AS UINTEGER)"
	case ast.StrLenChars:
		b.fire(TStrLen)
		return "CAST(length(" + x + ") AS UINTEGER)"
	case ast.StrSlice:
		b.fire(TStrSlice)
		return fmt.Sprintf("u_sslice(%s, %d, %d)", x, e.I[0], e.I[1])
	case ast.StrToUpper, ast.StrToLower:
		b.refuse(XCaseMap, "%s", e.Op)
	case ast.StrStripPrefix:
		b.fire(TStrStrip)
		return "u_sprefix(" + x + ", " + String(e.S[0]) + ")"
	case ast.StrStripSuffix:
		b.fire(TStrStrip)
		return "u_ssuffix(" + x + ", " + String(e.S[0]) + ")"
	case ast.StrSplitLen:
		b.fire(TStrSplit)
		return "CAST(len(string_split(" + x + ", " + String(e.S[0]) + ")) AS UINTEGER)"
	case ast.StrToInteger:
		b.fire(TParseInt)
		return "(CASE WHEN regexp_full_match(" + x + ", '[+-]?[0-9]+') THEN TRY_CAST(" + x + " AS BIGINT) END)"
	case ast.DtConvertTimeZone:
		b.fire(TZoneTicks)
		return x
	case ast.DtTruncate, ast.DtRound:
		b.fire(TGrid)
		p := e.I[0] / tick(xt)
		out := "BIGINT"
		if xt == model.Date {
			out = "INTEGER"
		}
		if e.Op == ast.DtTruncate {
			return fmt.Sprintf("CAST(u_idiv(%s, %d) * %d AS %s)", x, p, p, out)
		}
		return fmt.Sprintf("CAST((u_idiv(%s, %d) + (CASE WHEN 2 * u_imod(%s, %d) >= %d THEN 1 ELSE 0 END)) * %d AS %s)",
			x, p, x, p, p, p, out)
	case ast.DtOffsetBy:
		return b.offset(e, x, xt)
	}
	if e.Op.IsTotal() {
		b.fire(TTotal)
		return fmt.Sprintf("(CAST(%s AS BIGINT) // %d)", x, e.Op.TotalUnitNanos()/xt.Unit().Nanos())
	}
	if xt.IsDatetime() && xt.Zone() != "" {
		b.refuse(XZoneWall, "%s of %s", e.Op, xt)
	}
	switch e.Op {
	case ast.DtDate:
		b.fire(TDateOf)
		return fmt.Sprintf("CAST(u_idiv(%s, %d) AS INTEGER)", x, xt.Unit().PerDay())
	case ast.DtTime:
		b.fire(TDateOf)
		return fmt.Sprintf("CAST(u_imod(%s, %d) AS BIGINT)", x, xt.Unit().PerDay())
	}
	if e.Op.IsPart() {
		b.fire(TCivil)
		return b.part(e.Op, x, xt)
	}
	panic(fmt.Sprintf("sqlgen: unknown function %s", e.Op))
}

// tick is an instant's tick in nanoseconds, a Date's a day.
func tick(t model.Type) int64 {
	if t == model.Date {
		return 86_400e9
	}
	return t.Unit().Nanos()
}

// part is a component of a naive Datetime, a Date or a Time (T-CIVIL).
func (b *builder) part(op ast.FnOp, x string, xt model.Type) string {
	var days, tod string
	var per int64
	switch {
	case xt == model.Date:
		days = x
	case xt.IsTime():
		tod, per = "CAST("+x+" AS BIGINT)", xt.Unit().PerSecond()
	default:
		per = xt.Unit().PerSecond()
		days = fmt.Sprintf("u_idiv(%s, %d)", x, xt.Unit().PerDay())
		tod = fmt.Sprintf("CAST(u_imod(%s, %d) AS BIGINT)", x, xt.Unit().PerDay())
	}
	frac := func(nanosPer int64) string {
		return fmt.Sprintf("CAST(((%s %% %d) * %d) // %d AS INTEGER)", tod, per, 1_000_000_000/per, nanosPer)
	}
	switch op {
	case ast.DtYear:
		return "TRY_CAST(struct_extract(u_civil(" + days + "), 'y') AS INTEGER)"
	case ast.DtMonth:
		return "CAST(struct_extract(u_civil(" + days + "), 'm') AS INTEGER)"
	case ast.DtDay:
		return "CAST(struct_extract(u_civil(" + days + "), 'd') AS INTEGER)"
	case ast.DtWeekday:
		return "CAST(u_imod(CAST(" + days + " AS HUGEINT) + 3, 7) + 1 AS INTEGER)"
	case ast.DtOrdinalDay:
		return "CAST(u_ordinal(" + days + ") AS INTEGER)"
	case ast.DtHour:
		return fmt.Sprintf("CAST(%s // %d AS INTEGER)", tod, 3600*per)
	case ast.DtMinute:
		return fmt.Sprintf("CAST((%s // %d) %% 60 AS INTEGER)", tod, 60*per)
	case ast.DtSecond:
		return fmt.Sprintf("CAST((%s // %d) %% 60 AS INTEGER)", tod, per)
	case ast.DtMillisecond:
		return frac(1_000_000)
	case ast.DtMicrosecond:
		return frac(1_000)
	case ast.DtNanosecond:
		return frac(1)
	}
	panic(fmt.Sprintf("sqlgen: unknown component %s", op))
}

// offset is OffsetBy (T-OFFSET): nanoseconds as ticks, whatever the zone; days and
// months on a naive wall clock or a Date.
func (b *builder) offset(e *ast.Fn, x string, xt model.Type) string {
	iv, err := ast.ParseInterval(e.S[0])
	if err != nil {
		panic("sqlgen: " + err.Error())
	}
	if (iv.Months != 0 || iv.Days != 0) && xt.IsDatetime() && xt.Zone() != "" {
		b.refuse(XZoneWall, "OffsetBy %s of %s", e.S[0], xt)
	}
	b.fire(TOffset)
	if xt == model.Date {
		days := iv.Days + iv.Nanos/86_400e9
		if iv.Months != 0 {
			return fmt.Sprintf("CAST(u_addmonths(%s, %d) + %d AS INTEGER)", x, iv.Months, days)
		}
		return fmt.Sprintf("CAST(CAST(%s AS BIGINT) + %d AS INTEGER)", x, days)
	}
	perDay := xt.Unit().PerDay()
	ticks := iv.Nanos/xt.Unit().Nanos() + iv.Days*perDay
	if iv.Months != 0 {
		return fmt.Sprintf("CAST(CAST(u_addmonths(u_idiv(%s, %d), %d) AS HUGEINT) * %d + u_imod(%s, %d) + %d AS BIGINT)",
			x, perDay, iv.Months, perDay, x, perDay, ticks)
	}
	return fmt.Sprintf("(CAST(%s AS BIGINT) + %d)", x, ticks)
}
