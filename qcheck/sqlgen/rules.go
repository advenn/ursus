package sqlgen

// Rule is one translation rule: what ursus answers, by which of its docs or
// feature-gaps §9's entries, and how the SQL computes that answer. Where ursus
// answers differently from DuckDB by a rule of its own, the SQL follows ursus's
// rule (step 506 §1); a difference no rule explains is a finding, never a rule.
type Rule struct {
	ID     string
	Source string // the ursus doc or feature-gaps entry the rule encodes
	What   string // what the translation does
}

// The rule IDs. The R- rules are step 512 §3's refusals: the AST avoids each, so
// SQL never sees it, and Translate refuses a tree that holds one. The T- rules are
// translations.
const (
	RCastStrict   = "R-CAST-STRICT"
	RPromote      = "R-PROMOTE"
	RI128Arith    = "R-I128-ARITH"
	RMeanBool     = "R-MEAN-BOOL"
	RJoinSuffix   = "R-JOIN-SUFFIX"
	RJoinKeys     = "R-JOIN-KEYS"
	RSelectLit    = "R-SELECT-LIT"
	TNulls        = "T-NULLS"
	TSortStable   = "T-SORT-STABLE"
	TKeyZero      = "T-KEY-ZERO"
	TIEEECmp      = "T-IEEE-CMP"
	TCmpPromote   = "T-CMP-PROMOTE"
	TIntWrap      = "T-INT-WRAP"
	TFloor        = "T-FLOOR"
	TFloatMod     = "T-FLOAT-MOD"
	TDivFloat     = "T-DIV-FLOAT"
	TLitType      = "T-LIT-TYPE"
	TPromote      = "T-PROMOTE"
	TKleene       = "T-KLEENE"
	TWhenNull     = "T-WHEN-NULL"
	TFillWeak     = "T-FILL-WEAK"
	TCastExact    = "T-CAST-EXACT"
	TCastRange    = "T-CAST-RANGE"
	TParseInt     = "T-PARSE-INT"
	TParseFloat   = "T-PARSE-FLOAT"
	TParseBool    = "T-PARSE-BOOL"
	TParseDate    = "T-PARSE-DATE"
	TDateDays     = "T-DATE-DAYS"
	TDateText     = "T-DATE-TEXT"
	TFloatText    = "T-FLOAT-TEXT"
	TText         = "T-TEXT"
	TSum          = "T-SUM"
	TFloatAgg     = "T-FLOAT-AGG"
	TExtremum     = "T-EXTREMUM"
	TMeanInt      = "T-MEAN-INT"
	TNUnique      = "T-NUNIQUE"
	TCount        = "T-COUNT"
	TGroupNull    = "T-GROUP-NULL"
	TMaintain     = "T-MAINTAIN-ORDER"
	TWithSeq      = "T-WITH-SEQ"
	TJoinNull     = "T-JOIN-NULL"
	TJoinShape    = "T-JOIN-SHAPE"
	TJoinOrder    = "T-JOIN-ORDER"
	TSlice        = "T-SLICE"
	TISINNull     = "T-ISIN-NULL"
	TOrderPromise = "T-ORDER"
)

// Rules are every rule, the refusals first.
var Rules = []Rule{
	{RCastStrict, "Cast's doc (expr.go): a strict cast fails the query on the first value it cannot represent",
		"a strict cast only where none can fail (ast.StrictCast); every other is CastLossy, so no SQL cast raises"},
	{RPromote, "feature-gaps §9: a conditional's branches meet by Promote, which refuses text, a Boolean or a Date beside a number",
		"branches, fills and arithmetic meet only where model.Promote does"},
	{RI128Arith, "expr.go: Int128 arithmetic is exact or refused; Neg of an unsigned integer is refused",
		"no arithmetic on an Int128 or a Uint64: compared, sorted and carried only"},
	{RMeanBool, "agg.go: Mean of a Boolean is refused", "Mean of integers and floats only"},
	{RJoinSuffix, "join.go: a join whose suffixed name is taken is refused",
		"no generated name ends in _right, and a join ast cannot type is not drawn"},
	{RJoinKeys, "join.go: keys that do not meet are refused", "keys are integer with integer, text with text, float with float"},
	{RSelectLit, "feature-gaps §9: a Select of literals alone is a row for each of the frame's rows",
		"SQL's SELECT of literals over a FROM is a row a row, as ursus's"},
	{TNulls, "kernel/order.go SortSpec: NullsLast places nulls at the end of the output, independent of direction",
		"every ORDER BY key says NULLS FIRST or NULLS LAST"},
	{TSortStable, "agg.go Sort's doc: the sort is stable", "every ORDER BY ends with the input's order, __o"},
	{TKeyZero, "kernel/order.go OrderKeyF64: sort, group-by and join keys take the total order, NaN one value above +Inf, -0 equal to +0",
		"a float key, in ORDER BY, a join's ON and count(DISTINCT), is u_fkey(x): -0 made +0"},
	{TIEEECmp, "kernel/order.go: ursus uses IEEE for comparison expressions, NaN equal to nothing and ordered against nothing",
		"a float comparison is u_feq and its kin: false where either side is NaN, true for Ne; DuckDB's NaN equals NaN"},
	{TCmpPromote, "expr/resolve.go resolveComparison: both sides are cast to the type Promote meets them at",
		"an integer beside a float compares as DOUBLE, an Int128 or Uint64 beside an integer as HUGEINT"},
	{TIntWrap, "expr.go arithmetic: an integer result that leaves its type wraps, two's complement; MinInt64 // -1 is MinInt64",
		"integer +, -, *, Neg and Abs are computed in HUGEINT and wrapped to the width by u_wrap; DuckDB raises on overflow"},
	{TFloor, "expr.go FloorDiv's doc: FloorDiv floors, Mod takes the divisor's sign, an integer divisor of zero is null",
		"integer // and % are u_idiv and u_imod over HUGEINT, a zero divisor NULLIF'd to a null; a float // is floor(a / b)"},
	{TFloatMod, "feature-gaps §9 and kernel/scalar.go modFloatScalar: a float remainder takes the divisor's sign, a zero one too",
		"a float % is u_fmod: fmod, then the divisor added where the signs differ, a zero remainder signed as the divisor"},
	{TDivFloat, "expr.go Div's doc and feature-gaps §9: Div always returns a float, IEEE, x/0 an infinity or NaN",
		"both sides CAST AS DOUBLE, under ieee_floating_point_ops"},
	{TLitType, "feature-gaps §9: a literal has its Go value's type",
		"every literal is CAST to its AST type; a float literal is read from its shortest text, which rounds to its bits"},
	{TPromote, "dtype.Promote (feature-gaps §9): two integers meet at the wider, an integer and a float at Float64",
		"each operand is CAST to the type its node meets at before the operator"},
	{TKleene, "expr.go: And, Or and Not are Kleene logic", "SQL's AND, OR and NOT, which are"},
	{TWhenNull, "cond.go: a null condition is false (step 311)", "SQL's CASE WHEN, which takes the ELSE for a null condition"},
	{TFillWeak, "fill.go and feature-gaps §9: a fill's literal is weak, a value that fits keeping the column's type",
		"COALESCE at the fill's AST type, the literal cast to it"},
	{TCastExact, "feature-gaps §9 and castnum.go floatToInt: a float cast to an integer is exact or null when lossy",
		"CASE x >= lo AND x < hi AND x = trunc(x): DuckDB's cast rounds"},
	{TCastRange, "castnum.go narrowI128 and Cast's doc: an integer cast lossy to a narrower one, or to a Date, is null out of range",
		"u_irange, a CASE on the target's range, an Int64 to a Date on int32's; not TRY_CAST, whose null DuckDB 1.5.5 drops in a comparison with a constant"},
	{TParseInt, "castparse.go parseSigned: text to an integer is strconv.ParseInt, base 10, at the target's width",
		"regexp_full_match [+-]?[0-9]+ before TRY_CAST: DuckDB's cast trims spaces and reads more"},
	{TParseFloat, "dtype/numtext.go ParseFloat: ursus's float grammar, inf, infinity and nan in any case, an overflow null",
		"u_pfloat: the grammar as a regexp, the words by name, an infinity from digits null"},
	{TParseBool, "castparse.go parseBools: text to a Boolean is strconv.ParseBool", "u_pbool: strconv.ParseBool's twelve words"},
	{TParseDate, "dtype/temporal.go ParseTemporal: text to a Date is time.Parse(\"2006-01-02\"), the day one that exists",
		"u_pdate: four, two and two digits, the month and day checked, the days counted by Hinnant's days_from_civil"},
	{TDateDays, "dtype.Date: a Date is int32 days, over the whole range",
		"a Date is an INTEGER of days in SQL: DuckDB's DATE reads int32's ends as ±infinity"},
	{TDateText, "dtype.FormatTemporal: a Date's text is Go's 2006-01-02 of the proleptic Gregorian day, a year of four digits at least",
		"u_dtext: Hinnant's civil_from_days, printf'd, '-' before a negative year"},
	{TFloatText, "kernel/caststring.go FloatText (step 325): the shortest digits, plain from 1e-5 to below 1e16, else 1.5e-7",
		"u_ftext: DuckDB's shortest digits taken apart and written as FloatText writes them"},
	{TText, "kernel/caststring.go formatToString: an integer as strconv's decimal, a Boolean as true or false",
		"CAST AS VARCHAR, which writes both alike"},
	{TSum, "agg.go and feature-gaps §9: an integer's or a Boolean's Sum is an exact Int128, null for a group of no value",
		"sum(CAST(x AS HUGEINT)), a Boolean through INTEGER"},
	{TFloatAgg, "kernel/aggsum.go sumAcc and aggwhole.go floatWhole: a float Sum adds the values in the rows' order from +0, Mean divides it",
		"list_reduce over list(x ORDER BY __o) from +0: DuckDB's sum may add in any order"},
	{TExtremum, "kernel/aggminmax.go: float Min and Max take the total order, NaN above all, the first of equal values kept",
		"list_reduce over list(x ORDER BY __o) keeping the first of equal values; other types min and max"},
	{TMeanInt, "kernel/aggsum.go finishDecimal: an integer Mean is the exact sum divided once, rounded to the nearest float",
		"u_imean: |Σx| // n and thirty digits of the remainder over n, read by DuckDB's exact parser; avg rounds twice"},
	{TNUnique, "feature-gaps §9 and kernel/aggcount.go: NUnique does not count null, and counts by grouping equality",
		"count(DISTINCT u_fkey(x)) for a float"},
	{TCount, "feature-gaps §9: Count, Len and NUnique are a Uint64", "CAST(count(...) AS UBIGINT)"},
	{TGroupNull, "join.go JoinNullsEqual's doc: in GroupBy null forms its own group", "SQL's GROUP BY, which groups nulls"},
	{TMaintain, "agg.go: MaintainOrder keeps the groups in the order each first appears",
		"a group's __o is min(__o), and an ordered answer is ORDER BY __o"},
	{TWithSeq, "agg.go WithColumns: later expressions read the columns earlier ones added", "one CTE per column"},
	{TJoinNull, "join.go JoinNullsEqual: by default a null key matches nothing", "SQL's = in ON and EXISTS"},
	{TJoinShape, "join.go and feature-gaps §9: the left's columns, a coalesced key at the type the keys meet at, the right's others suffixed _right; JoinLeftOn and JoinRightOn keep both keys",
		"an explicit select list"},
	{TJoinOrder, "join.go JoinOrderLeft: the left's rows in order, each row's matches in the right's; a semi or anti join keeps the left's",
		"row_number() OVER (ORDER BY l.__o, r.__o); EXISTS keeps l.__o"},
	{TSlice, "frameops.go Slice: a negative offset counts from the end, the window clipped to the frame; a negative length is to the end",
		"row_number and count(*) OVER (), the window's bounds computed"},
	{TISINNull, "not used: step 512's AST generates no IsIn", "none"},
	{TOrderPromise, "ast.Info: an answer whose order the query promises compares by position, any other as a multiset",
		"an ordered answer ends ORDER BY __o"},
}

// unused are the rules no generated query can fire.
var unused = map[string]bool{TISINNull: true}

// Unused is whether no generated query can fire rule id.
func Unused(id string) bool { return unused[id] }
