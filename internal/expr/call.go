package expr

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/advenn/ursus/dtype"
	"github.com/advenn/ursus/internal/uerr"
)

// CallFn names a namespaced function — the `.str` and `.dt` families.
//
// # Why these are not UnaryOps
//
// `Contains(pattern, literal)` and `Truncate(every)` carry ARGUMENTS, and
// Unary{Op, Child} has nowhere to put them. Three shapes were possible: one op per
// (function, argument) pair, which does not terminate; arguments smuggled into the
// op enum, which is the same thing wearing a hat; or one node that holds a
// function and its operands.
//
// This is the third. It is also the shape every remaining namespace will need —
// `.list`, `.struct`, `.arr` — so paying for it once here is cheaper than three
// more bespoke node types later.
//
// # Appending only
//
// Like UnaryOp, the classifiers below are range comparisons over declaration
// order. Insert a constant mid-enum and a neighbouring function silently changes
// family. Append, and add a name.
type CallFn uint16

const (
	// --- string ---
	FnStrContains CallFn = iota
	FnStrStartsWith
	FnStrEndsWith
	FnStrFind
	FnStrCountMatches
	FnStrExtract
	FnStrReplace
	FnStrReplaceAll
	FnStrToLower
	FnStrToUpper
	FnStrLenBytes
	FnStrLenChars
	FnStrSlice
	FnStrStripChars
	FnStrStripPrefix
	FnStrStripSuffix
	FnStrReverse
	FnStrPadStart
	FnStrPadEnd
	FnStrZFill
	FnStrStripCharsStart
	FnStrStripCharsEnd
	FnStrEscapeRegex
	FnStrSplit
	FnStrSplitN
	FnStrExtractAll
	// The three parse a string with a strftime-style format (step 113): args are
	// the format, strict, and for a Datetime the unit and zone, for a Time the
	// unit. One function per target, because a type is not a literal.
	FnStrToDateFmt
	FnStrToDatetimeFmt
	FnStrToTimeFmt
	fnStrEnd

	// --- temporal ---
	FnDtYear CallFn = iota + 100
	FnDtMonth
	FnDtDay
	FnDtHour
	FnDtMinute
	FnDtSecond
	FnDtMillisecond
	FnDtMicrosecond
	FnDtNanosecond
	FnDtWeekday
	FnDtOrdinalDay
	FnDtQuarter
	FnDtWeek
	FnDtEpoch
	FnDtTruncate
	FnDtTotalDays
	FnDtTotalHours
	FnDtTotalMinutes
	FnDtTotalSeconds
	FnDtStrftime // formats with a strftime-style format; the one arg is the format
	// The calendar (step 145). offset_by's args are an interval's months, days and
	// nanos; round's are truncate's; convert_time_zone's one arg is the zone.
	FnDtOffsetBy
	FnDtRound
	FnDtMonthStart
	FnDtMonthEnd
	FnDtIsLeapYear
	FnDtConvertTimeZone
	// Step 174: replace_time_zone's args are the zone, "" for none, then the
	// ambiguous and non_existent policies, in Polars' words (ZonePolicies).
	FnDtReplaceTimeZone
	FnDtDaysInMonth // the month's length, read in the column's zone (step 230)
	// The local date and time of day of an instant, Polars' dt.date and dt.time
	// (step 249).
	FnDtDate
	FnDtTime
	fnDtEnd

	// --- type-agnostic ---
	//
	// A third family, and the first that is not keyed to a receiver type. The two
	// above dispatch on what the receiver IS — a String, an instant — but IsIn
	// applies to anything hashable, so it needs its own classifier rather than
	// widening either of theirs.
	FnIsIn CallFn = iota + 200

	// A value map (step 226). Args[0] is the receiver, Args[1] the count k of old
	// values as an Int64 literal, then the k old values, the k new ones, and for
	// FnReplaceOr the default. ReplaceParts reads them.
	FnReplace       // a value not among old keeps itself
	FnReplaceStrict // and here is refused
	FnReplaceOr     // and here, a null too, is the default
	fnGenEnd

	// --- maths ---
	//
	// A fourth family, for the maths operations that carry a PARAMETER. The
	// parameterless ones are UnaryOps; Round needs a decimal count, and a Call is
	// where a parameter belongs.
	//
	// Not a field on expr.Unary, which was the obvious alternative: three places
	// rebuild a Unary by hand — physical/agg.go's extractAggs and both predicate
	// pushdown walkers — as `&Unary{Op: t.Op, Child: ...}`, so a new field would be
	// silently DROPPED the moment a predicate moved, and Round(2) would quietly
	// become Round(0). That is step 7's Agg.Params bug, three sites over. Call.String()
	// renders its arguments structurally, so the same class is impossible here.
	FnMathRound CallFn = iota + 300
	// FnMathAsFloat takes a number, or a Duration's ticks, to a float without
	// changing its value: an integer or a Decimal to the nearest Float64, a float as
	// itself. It is how PctChange subtracts in float, and is not public.
	FnMathAsFloat
	// FnMathDiffWiden takes an unsigned integer to the signed type that holds the
	// difference of any two of its values, and leaves every other type as it is. It
	// is how Diff subtracts without wrapping, and is not public.
	FnMathDiffWiden
	// The bit counts of an integer (step 146), Polars' bitwise_count_ones and its
	// five siblings, each a Uint32.
	FnMathCountOnes
	FnMathCountZeros
	FnMathLeadingOnes
	FnMathLeadingZeros
	FnMathTrailingOnes
	FnMathTrailingZeros
	fnMathEnd

	// --- list ---
	//
	// A fifth family, keyed to a List receiver. These all reduce a list to a
	// SCALAR: nothing here builds a list, which is the seam the step was cut on —
	// sort, unique, reverse, slice and the set operations construct a new List
	// column and are a different problem.
	FnListLen CallFn = iota + 400
	FnListGet
	FnListContains
	FnListMin
	FnListMax
	FnListSum
	FnListMean
	FnListJoin // the elements of a List of String, joined; the one arg is the separator

	// list -> LIST. These reshape the list and hand back a list, which is the
	// distinction that split the namespace across two steps: everything above
	// answers a question ABOUT a list, everything here returns one.
	FnListReverse
	FnListHead
	FnListTail
	FnListSlice
	FnListSort
	FnListUnique
	FnListDropNulls

	// list -> a SCALAR again, each a column aggregate over a list's elements (step
	// 222), after the reshaping half because that is where the family ends.
	FnListNUnique
	FnListArgMin
	FnListArgMax

	// list -> STRUCT (step 224): element i as field i, its names the arguments.
	FnListToStruct
	fnListEnd
)

// The `.struct` family.
const (
	FnStructField        CallFn = iota + 500
	FnStructRenameFields        // every field renamed, in order (step 224)
	FnStructJSONEncode          // each struct as a JSON object (step 231)
	fnStructEnd
)

// The horizontal family (step 146): every argument is an OPERAND, a column read
// row by row, where each family above takes a receiver and literal parameters.
//
// concat_str joins its operands' text, and a separator is an operand like any
// other, a literal between each two; struct builds a Struct whose fields are its
// operands, named as they are. Neither has a parameter, so Call's rule — Args[1:]
// literal — does not apply to them, and they are typed from every operand's type
// (ResolveHorizontal) rather than from a receiver's.
//
// cut (step 147) puts its first operand in a bin: the operands after it are the
// breaks, numbers, then none or one label more than breaks, strings. Its breaks are
// operands because QCut's are quantiles of the column, known only when it runs.
// cut_left_closed is the same with each bin closed on the left.
const (
	FnConcatStr CallFn = iota + 600
	FnStructOf
	FnCut
	FnCutLeftClosed
	FnStructWithFields

	// The list set operations and gather (step 223): their second List is an
	// operand, a column, not a parameter, so they take this family's shape. The
	// answer is named after the first.
	FnListSetUnion
	FnListSetIntersection
	FnListSetDifference
	FnListSetSymmetricDifference
	FnListGather       // an index out of range is refused
	FnListGatherOrNull // and here gives null

	// The horizontal reductions (step 225): one value a row from every operand,
	// max, min, sum and mean each the column aggregate over the row's values, and
	// any and all Kleene's.
	FnMaxHorizontal
	FnMinHorizontal
	FnSumHorizontal
	FnMeanHorizontal
	FnAnyHorizontal
	FnAllHorizontal

	// A Date's or Datetime's day with a Time of day (step 230): its second operand
	// is a column, so it takes this family's shape.
	FnDtCombine

	// A list sorted by another of its length, element for element (step 253),
	// ascending or descending, nulls first: its second list is an operand.
	FnListSortBy
	FnListSortByDesc

	// Membership and indexing whose argument is a column (step 254): is_in against
	// each row's own list, and list.get and list.contains with a per-row index and
	// value. The constant forms are their families' own; these take this one's
	// shape, every operand a column.
	FnIsInExpr
	FnListGetExpr
	FnListContainsExpr

	// String functions whose pattern is a column, and replace's whose value is (step
	// 255). Each takes the constant form's arguments after the receiver, any of the
	// pattern or value a column; the flags stay literals.
	FnStrContainsExpr   // the receiver, the pattern, literal
	FnStrStartsWithExpr // the receiver, the prefix
	FnStrEndsWithExpr
	FnStrStripPrefixExpr
	FnStrStripSuffixExpr
	FnStrSplitExpr
	FnStrReplaceExpr // the receiver, the pattern (a literal), the value, literal
	FnStrReplaceAllExpr

	// dt.offset_by with an offset a row, a duration string as Every reads it (step
	// 256).
	FnDtOffsetByExpr

	// Generators, a list a row (step 263): int_ranges(start, end, step),
	// linear_spaces(start, end, n, closed) with closed a literal Closed, and
	// value.repeat_by(n). The scalar forms are these over one row, exploded.
	FnIntRanges
	FnLinearSpaces
	FnRepeatBy

	// Temporal ranges, a list a row (step 264): start, end, the interval's months,
	// days and nanoseconds, and closed, the last four literals.
	FnDateRanges
	FnDatetimeRanges
	FnTimeRanges

	// Values from their components (step 265): date(y, m, d), datetime(y, m, d, h,
	// mi, s, us), time(h, mi, s, us); a duration's and an epoch count's ticks, Int64,
	// which the root package casts; and business_day_count(start, end, mask,
	// holidays), the mask an Int64 literal and the holidays a List(Int64) literal of
	// days.
	FnMakeDate
	FnMakeDatetime
	FnMakeTime
	FnDurationTicks
	FnEpochTicks
	FnBusinessDayCount

	// concat_list(exprs...): each row's operands in one list, a List operand's
	// elements and another's value (step 266).
	FnConcatList

	// list.shift(n) and list.count_matches(v), n and v a value a row (step 269).
	FnListShift
	FnListCountMatches

	// to_integer in a base a row, signed or unsigned, strict a literal; and
	// concat_str past nulls, the separator last (step 271).
	FnStrParseInt
	FnStrParseUint
	FnConcatStrIgnoreNulls

	// Step 272: is_business_day(value, mask, holidays), the calendar as
	// business_day_count's; add_business_days(value, n, mask, holidays, roll); and
	// replace(value, year, month, day, hour, minute, second, microsecond, ambiguous,
	// non_existent), a component a null literal to keep the value's own.
	FnDtIsBusinessDay
	FnDtAddBusinessDays
	FnDtReplace

	// Step 274's binary functions of a row's operands: contains, starts_with and
	// ends_with(value, pattern); get(value, index, null_on_oob); and slice(value, a,
	// b, mode), mode "slice" with a the offset and b the length, a null for the rest,
	// or "head" or "tail" with a their n and b a null.
	FnBinContains
	FnBinStartsWith
	FnBinEndsWith
	FnBinGet
	FnBinSlice

	// order_cmp(a, b): -1, 0 or 1 as a sorts before, with or after b in the total
	// order Sort uses, NaN above every number and -0 with 0 (step 279), which
	// search_sorted, index_of and the peaks are written in.
	FnOrderCmp
	fnHorizontalEnd
)

// The list family, continued (step 253): the aggregates a list did not have, which a
// reshape's aggregate lowers to, and the reshapes of E3's second half. std and var
// take ddof, quantile q and an interpolation; gather_every n and an offset.
//
// A block of its own: the first families share one const block, whose iota runs on,
// so the list family's first block already ends at the struct family's base, and
// six more there collided with FnStructField. IsList covers both blocks.
const (
	FnListMedian CallFn = iota + 700
	FnListStd
	FnListVar
	FnListQuantile
	FnListDropNans
	FnListGatherEvery
	// A List of Lists flattened a level (step 259), which Explode in an aggregation
	// lowers to: an empty or null inner list one null, as explode makes it a row.
	FnListExplode
	// Each list's indices (step 267): in its elements' sorted order, descending and
	// nulls last two literal Booleans; of its true elements; of each value's first.
	FnListArgSort
	FnListArgTrue
	FnListArgUnique
	// list.any and list.all, ignore_nulls a literal Boolean; list.item, allow_empty
	// one; list.sample, n (negative for a fraction), the fraction, with_replacement,
	// shuffle and the seed (step 269).
	FnListAny
	FnListAll
	FnListItem
	FnListSample
	// list.mode, maintain_order a literal Boolean (step 277), which Mode in an
	// aggregation lowers to.
	FnListMode
	// list.unique_counts; list.value_counts, sort, normalize, the count's name and the
	// value's (step 280).
	FnListUniqueCounts
	FnListValueCounts
	fnListMoreEnd
)

// The string family, continued (step 270): many literal patterns at once
// (contains_any, replace_many, extract_many, find_many), a List literal of them
// first; extract_groups and split_exact, a Struct; split keeping its separator;
// to_titlecase; normalize, the form's name.
//
// A block of its own, as the list family's at 700 is: nine more in the first block
// pushed the list family's end onto the struct family's base, 500, and the build
// caught the duplicate keys. IsString covers both blocks.
const (
	FnStrContainsAny CallFn = iota + 800
	FnStrReplaceMany
	FnStrExtractMany
	FnStrFindMany
	FnStrExtractGroups
	FnStrSplitExact
	FnStrSplitInclusive
	FnStrToTitlecase
	FnStrNormalize
	// Step 271: encode and decode, hex or base64; json_path_match, the path;
	// json_decode, a null literal of the type it decodes to.
	FnStrEncode
	FnStrDecode
	FnStrJsonPathMatch
	FnStrJsonDecode
	fnStrMoreEnd
)

// The temporal family, continued (step 272): iso_year, century and millennium; a
// zone's base and daylight-saving offsets; with_time_unit, the TimeUnit's code; the
// totals in the smaller units, and fractional in any, the unit's nanoseconds; second
// with its fraction; and epoch in a unit, its nanoseconds, a day's being days.
//
// A block of its own, as the string family's at 800 is. IsTemporal covers both.
const (
	FnDtIsoYear CallFn = iota + 900
	FnDtCentury
	FnDtMillennium
	FnDtBaseUtcOffset
	FnDtDstOffset
	FnDtWithTimeUnit
	FnDtTotalMilliseconds
	FnDtTotalMicroseconds
	FnDtTotalNanoseconds
	FnDtTotalFractional
	FnDtSecondFractional
	FnDtEpochIn
	fnDtMoreEnd
)

// The categorical family (step 273): get_categories, which Expand writes out as its
// Enum's categories once the receiver's type is known, so no kernel sees it; and
// physical, an Enum's indices at Polars' width. The text functions of .cat are the
// string family's, which read an Enum as its text.
//
// A block of its own, as the others after 600 are. IsGeneral covers it.
const (
	FnCatGetCategories CallFn = iota + 1000
	FnCatPhysical
	fnCatEnd
)

// The binary family (step 274): encode and decode, the encoding's name and decode's
// strict; size, the unit's bytes, 1 for bytes; and reinterpret, a null literal of
// the type and the endianness. Its functions of a row's operands are horizontal.
// The general family, continued (step 279): hash, the seed; and order_sign, a
// value's sign in the total order, beside its type's zero. IsGeneral covers it.
const (
	FnHash CallFn = iota + 1200
	FnOrderSign
	fnGenMoreEnd
)

const (
	FnBinEncode CallFn = iota + 1100
	FnBinDecode
	FnBinSize
	FnBinReinterpret
	fnBinEnd
)

// IsString, IsTemporal and IsGeneral classify a function by family.
//
// These are range comparisons over declaration order, which is why the enum is
// append-only within each block and the blocks are spaced 100 apart.
func (f CallFn) IsString() bool {
	return f < fnStrEnd || f >= FnStrContainsAny && f < fnStrMoreEnd
}
func (f CallFn) IsTemporal() bool {
	return f >= FnDtYear && f < fnDtEnd || f >= FnDtIsoYear && f < fnDtMoreEnd
}
func (f CallFn) IsGeneral() bool {
	return f >= FnIsIn && f < fnGenEnd || f >= FnCatGetCategories && f < fnCatEnd ||
		f >= FnHash && f < fnGenMoreEnd
}
func (f CallFn) IsMath() bool { return f >= FnMathRound && f < fnMathEnd }
func (f CallFn) IsList() bool {
	return f >= FnListLen && f < fnListEnd || f >= FnListMedian && f < fnListMoreEnd
}
func (f CallFn) IsStruct() bool { return f >= FnStructField && f < fnStructEnd }
func (f CallFn) IsBinary() bool { return f >= FnBinEncode && f < fnBinEnd }
func (f CallFn) IsHorizontal() bool {
	return f >= FnConcatStr && f < fnHorizontalEnd
}

var callNames = map[CallFn]string{
	FnStrContains: "str.contains", FnStrStartsWith: "str.starts_with",
	FnStrEndsWith: "str.ends_with", FnStrFind: "str.find",
	FnStrCountMatches: "str.count_matches", FnStrExtract: "str.extract",
	FnStrReplace: "str.replace", FnStrReplaceAll: "str.replace_all",
	FnStrToLower: "str.to_lower", FnStrToUpper: "str.to_upper",
	FnStrLenBytes: "str.len_bytes", FnStrLenChars: "str.len_chars",
	FnStrSlice: "str.slice", FnStrStripChars: "str.strip_chars",
	FnStrStripPrefix: "str.strip_prefix", FnStrStripSuffix: "str.strip_suffix",
	FnStrReverse: "str.reverse", FnStrPadStart: "str.pad_start",
	FnStrPadEnd: "str.pad_end", FnStrZFill: "str.zfill",
	FnStrStripCharsStart: "str.strip_chars_start",
	FnStrStripCharsEnd:   "str.strip_chars_end",
	FnStrEscapeRegex:     "str.escape_regex", FnStrSplit: "str.split",
	FnStrSplitN: "str.splitn", FnStrExtractAll: "str.extract_all",
	FnStrContainsAny: "str.contains_any", FnStrReplaceMany: "str.replace_many",
	FnStrExtractMany: "str.extract_many", FnStrFindMany: "str.find_many",
	FnStrExtractGroups: "str.extract_groups", FnStrSplitExact: "str.split_exact",
	FnStrSplitInclusive: "str.split_inclusive", FnStrToTitlecase: "str.to_titlecase",
	FnStrNormalize: "str.normalize", FnStrEncode: "str.encode", FnStrDecode: "str.decode",
	FnStrJsonPathMatch: "str.json_path_match", FnStrJsonDecode: "str.json_decode",
	FnStrParseInt: "str.to_integer", FnStrParseUint: "str.to_uinteger",
	FnConcatStrIgnoreNulls: "concat_str_ignore_nulls",
	FnStrToDateFmt:         "str.to_date", FnStrToDatetimeFmt: "str.to_datetime",
	FnStrToTimeFmt: "str.to_time",

	FnDtYear: "dt.year", FnDtMonth: "dt.month", FnDtDay: "dt.day",
	FnDtHour: "dt.hour", FnDtMinute: "dt.minute", FnDtSecond: "dt.second",
	FnDtMillisecond: "dt.millisecond", FnDtMicrosecond: "dt.microsecond",
	FnDtNanosecond: "dt.nanosecond", FnDtWeekday: "dt.weekday",
	FnDtOrdinalDay: "dt.ordinal_day", FnDtQuarter: "dt.quarter",
	FnDtWeek: "dt.week", FnDtEpoch: "dt.epoch", FnDtTruncate: "dt.truncate",
	FnDtTotalDays: "dt.total_days", FnDtTotalHours: "dt.total_hours",
	FnDtTotalMinutes: "dt.total_minutes", FnDtTotalSeconds: "dt.total_seconds",
	FnDtStrftime: "dt.strftime", FnDtOffsetBy: "dt.offset_by", FnDtRound: "dt.round",
	FnDtMonthStart: "dt.month_start", FnDtMonthEnd: "dt.month_end",
	FnDtIsLeapYear: "dt.is_leap_year", FnDtConvertTimeZone: "dt.convert_time_zone",
	FnDtReplaceTimeZone: "dt.replace_time_zone", FnDtDaysInMonth: "dt.days_in_month",
	FnDtDate: "dt.date", FnDtTime: "dt.time",
	FnDtIsoYear: "dt.iso_year", FnDtCentury: "dt.century", FnDtMillennium: "dt.millennium",
	FnDtBaseUtcOffset: "dt.base_utc_offset", FnDtDstOffset: "dt.dst_offset",
	FnDtWithTimeUnit: "dt.with_time_unit", FnDtTotalMilliseconds: "dt.total_milliseconds",
	FnDtTotalMicroseconds: "dt.total_microseconds", FnDtTotalNanoseconds: "dt.total_nanoseconds",
	FnDtTotalFractional: "dt.total_fractional", FnDtSecondFractional: "dt.second_fractional",
	FnDtEpochIn:       "dt.epoch_in",
	FnDtIsBusinessDay: "dt.is_business_day", FnDtAddBusinessDays: "dt.add_business_days",
	FnDtReplace:        "dt.replace",
	FnCatGetCategories: "cat.get_categories", FnCatPhysical: "cat.physical",
	FnBinContains: "bin.contains", FnBinStartsWith: "bin.starts_with", FnBinEndsWith: "bin.ends_with",
	FnBinGet: "bin.get", FnBinSlice: "bin.slice", FnBinEncode: "bin.encode", FnBinDecode: "bin.decode",
	FnBinSize: "bin.size", FnBinReinterpret: "bin.reinterpret",
	FnOrderCmp: "order_cmp", FnHash: "hash", FnOrderSign: "order_sign",

	FnIsIn: "is_in",

	FnMathRound: "round", FnMathAsFloat: "as_float", FnMathDiffWiden: "diff_widen",
	FnMathCountOnes: "bitwise_count_ones", FnMathCountZeros: "bitwise_count_zeros",
	FnMathLeadingOnes: "bitwise_leading_ones", FnMathLeadingZeros: "bitwise_leading_zeros",
	FnMathTrailingOnes: "bitwise_trailing_ones", FnMathTrailingZeros: "bitwise_trailing_zeros",
	FnConcatStr: "concat_str", FnStructOf: "struct", FnCut: "cut",
	FnCutLeftClosed: "cut_left_closed", FnStructWithFields: "struct.with_fields",

	FnListLen: "list.len", FnListGet: "list.get",
	FnListContains: "list.contains", FnListMin: "list.min",
	FnListMax: "list.max", FnListSum: "list.sum", FnListMean: "list.mean",
	FnListReverse: "list.reverse", FnListHead: "list.head",
	FnListTail: "list.tail", FnListSlice: "list.slice",
	FnListSort: "list.sort", FnListUnique: "list.unique",
	FnListDropNulls: "list.drop_nulls", FnListJoin: "list.join",
	FnListNUnique: "list.n_unique", FnListArgMin: "list.arg_min",
	FnListArgMax:   "list.arg_max",
	FnListSetUnion: "list.set_union", FnListSetIntersection: "list.set_intersection",
	FnListSetDifference:          "list.set_difference",
	FnListSetSymmetricDifference: "list.set_symmetric_difference",
	FnListGather:                 "list.gather", FnListGatherOrNull: "list.gather_or_null",
	FnMaxHorizontal: "max_horizontal", FnMinHorizontal: "min_horizontal",
	FnSumHorizontal: "sum_horizontal", FnMeanHorizontal: "mean_horizontal",
	FnAnyHorizontal: "any_horizontal", FnAllHorizontal: "all_horizontal",
	FnDtCombine: "dt.combine",

	FnStructField: "struct.field", FnStructRenameFields: "struct.rename_fields",
	FnStructJSONEncode: "struct.json_encode",
	FnReplace:          "replace", FnReplaceStrict: "replace_strict", FnReplaceOr: "replace_or",
	FnListToStruct: "list.to_struct",
	FnListMedian:   "list.median", FnListStd: "list.std", FnListVar: "list.var",
	FnListQuantile: "list.quantile", FnListDropNans: "list.drop_nans",
	FnListGatherEvery: "list.gather_every", FnListExplode: "list.explode",
	FnListArgSort: "list.arg_sort", FnListArgTrue: "list.arg_true", FnListArgUnique: "list.arg_unique",
	FnListSortBy: "list.sort_by", FnListSortByDesc: "list.sort_by_desc",
	FnIsInExpr: "is_in_expr", FnListGetExpr: "list.get_expr",
	FnListContainsExpr: "list.contains_expr",
	FnStrContainsExpr:  "str.contains_expr", FnStrStartsWithExpr: "str.starts_with_expr",
	FnStrEndsWithExpr: "str.ends_with_expr", FnStrStripPrefixExpr: "str.strip_prefix_expr",
	FnStrStripSuffixExpr: "str.strip_suffix_expr", FnStrSplitExpr: "str.split_expr",
	FnStrReplaceExpr: "str.replace_expr", FnStrReplaceAllExpr: "str.replace_all_expr",
	FnDtOffsetByExpr: "dt.offset_by_expr",
	FnIntRanges:      "int_ranges", FnLinearSpaces: "linear_spaces", FnRepeatBy: "repeat_by",
	FnDateRanges: "date_ranges", FnDatetimeRanges: "datetime_ranges", FnTimeRanges: "time_ranges",
	FnMakeDate: "date", FnMakeDatetime: "datetime", FnMakeTime: "time",
	FnDurationTicks: "duration_ticks", FnEpochTicks: "epoch_ticks",
	FnBusinessDayCount: "business_day_count", FnConcatList: "concat_list",
	FnListShift: "list.shift", FnListCountMatches: "list.count_matches",
	FnListAny: "list.any", FnListAll: "list.all", FnListItem: "list.item", FnListSample: "list.sample",
	FnListMode: "list.mode", FnListUniqueCounts: "list.unique_counts", FnListValueCounts: "list.value_counts",
}

func (f CallFn) String() string {
	if s, ok := callNames[f]; ok {
		return s
	}
	return "call(" + strconv.Itoa(int(f)) + ")"
}

// Call applies a namespaced function.
//
// Args[0] is the RECEIVER — the column the method was called on. Args[1:] are the
// function's parameters and must resolve to literals; a column-valued pattern is
// representable in this IR and simply not implemented yet, which is the right way
// round for a constraint that may lift later.
//
// Args are ordinary children, so RootNames, expansion and projection pushdown all
// see through a Call with no special case. That is the property the design docs
// predicted for window nodes and it holds here for the same reason.
type Call struct {
	Fn   CallFn
	Args []Node
}

func (c *Call) node()            {}
func (c *Call) Children() []Node { return c.Args }

func (c *Call) String() string {
	parts := make([]string, len(c.Args))
	for i, a := range c.Args {
		parts[i] = a.String()
	}
	if len(parts) == 0 || c.Fn.IsHorizontal() {
		return c.Fn.String() + "(" + strings.Join(parts, ", ") + ")"
	}
	return parts[0] + "." + c.Fn.String() + "(" + strings.Join(parts[1:], ", ") + ")"
}

func (c *Call) Field(in *dtype.Schema) (dtype.Field, error) {
	if len(c.Args) == 0 {
		return dtype.Field{}, uerr.Internalf("expr: %s has no receiver", c.Fn)
	}
	if c.Fn.IsHorizontal() {
		fields := make([]dtype.Field, len(c.Args))
		for i, a := range c.Args {
			f, err := a.Field(in)
			if err != nil {
				return dtype.Field{}, err
			}
			fields[i] = f
		}
		out, err := ResolveHorizontal(c.Fn, fields)
		if err != nil {
			return dtype.Field{}, err
		}
		return dtype.Field{Name: fields[0].Name, Type: out, Nullable: true}, nil
	}
	recv, err := c.Args[0].Field(in)
	if err != nil {
		return dtype.Field{}, err
	}
	out, err := ResolveCall(c, recv.Type)
	if err != nil {
		return dtype.Field{}, err
	}
	// The name follows the receiver — leftmost-column-wins, same as everywhere
	// else — so `Col("s").Str().ToLower()` is still called "s" unless aliased. But a
	// struct's field is named after the field, as Polars names it (step 275).
	return dtype.Field{Name: CallOutputName(c, recv.Name), Type: out, Nullable: true}, nil
}

// CallOutputName is a call's name for a receiver named recv: the receiver's, but a
// struct's field's, which is the field's own.
func CallOutputName(c *Call, recv string) string {
	if c.Fn == FnStructField {
		if name, ok := callLitString(c, 1); ok {
			return name
		}
	}
	return recv
}

// ResolveCall gives the output type of a call applied to a receiver of type in.
//
// It is the single authority, consulted by Field for the plan's schema and by the
// evaluator for the kernel's output type. Two copies would drift, which is the
// Binding lesson.
//
// It takes the whole Call rather than just the function because `.struct.field` is
// the first call whose output type depends on an ARGUMENT — `field("age")` is Int64
// and `field("city")` is String, from the same receiver.
//
// It used to add "Every other family answers from the receiver's type alone", and
// that was false. `.dt.truncate` is the second, and the divergence lived exactly in
// the sentence: the kernel routes on the interval — `truncate(Time, 1 month)` has no
// answer while `truncate(Time, 1 hour)` does — so a resolver that saw only the
// receiver promised both. The argument is a CONSTANT of the expression, not data, so
// there was never a reason the planner could not see it.
func ResolveCall(c *Call, in dtype.DataType) (dtype.DataType, error) {
	fn := c.Fn
	switch {
	case fn.IsString():
		// HasStringStorage, not IsString. IsString is TRUE FOR ENUM, whose values are
		// logically strings but are stored as a Uint32 index — so a column that
		// passed this gate reached kernel.StrCall, which calls Strings() and gets a
		// zero accessor, and PANICKED on the first row. dtype.IsString's own doc warns
		// about exactly this misuse and names HasStringStorage as the storage
		// question's predicate. Latent today only because Enum columns cannot yet be
		// built from the public API.
		//
		// An Enum is read as its text, which the string kernels decode it to first
		// (step 273).
		//
		// A Binary is refused, as Polars refuses it (step 274): its bytes are
		// Bin()'s, and a text function such as slice would split them as runes.
		if in.ID() != dtype.TypeString && in.ID() != dtype.TypeNull && in.ID() != dtype.TypeEnum {
			e := uerr.New(uerr.KindType, "str", "%s requires a String operand, got %s", fn, in)
			if in.ID() == dtype.TypeBinary {
				return dtype.Null, e.Hint("a Binary column's functions are Bin()'s, e.g. .Bin().Slice(0, 4)")
			}
			return dtype.Null, e.Hint("cast the column first, e.g. .Cast(ursus.String)")
		}
		if fn.isStrptime() {
			return strptimeOut(c)
		}
		switch fn {
		case FnStrExtractGroups, FnStrSplitExact, FnStrSplitN:
			return strStructOut(c)
		case FnStrJsonDecode:
			return jsonDecodeOut(c)
		case FnStrJsonPathMatch:
			path, ok := callLitString(c, 1)
			if !ok {
				return dtype.Null, uerr.Internalf("expr: %s without its path", fn)
			}
			if _, err := ParseJSONPath(path); err != nil {
				return dtype.Null, err
			}
		}
		return strCallOut(fn), nil

	case fn.IsTemporal():
		return dtCallOut(c, in)

	case fn.IsGeneral():
		return genCallOut(c, in)

	case fn.IsMath():
		return mathCallOut(fn, in)

	case fn.IsList():
		return listCallOut(c, in)

	case fn.IsStruct():
		return structCallOut(c, in)

	case fn.IsBinary():
		return binCallOut(c, in)

	case fn.IsHorizontal():
		// Typed from every operand, which a receiver's type is not: Field and the
		// evaluator call ResolveHorizontal.
		return dtype.Null, uerr.Internalf("expr: %s is typed by ResolveHorizontal", fn)

	default:
		return dtype.Null, uerr.Internalf("expr: unknown call %d", fn)
	}
}

// CutBreaks is how many of a cut's operands after the first are breaks: those before
// the first label, which is a String.
func CutBreaks(ops []dtype.Field) int {
	k := 0
	for _, f := range ops[1:] {
		if f.Type.ID() == dtype.TypeString {
			break
		}
		k++
	}
	return k
}

// ResolveHorizontal gives the output type of a horizontal call over operands of
// these fields. Like ResolveCall, it is the one authority, for the plan's schema and
// the kernel's output alike.
func ResolveHorizontal(fn CallFn, ops []dtype.Field) (dtype.DataType, error) {
	if len(ops) == 0 {
		return dtype.Null, uerr.Internalf("expr: %s has no operands", fn)
	}
	switch fn {
	case FnConcatStr:
		// Any operand that formats as text is formatted, as Polars' concat_str
		// formats it: the kernel casts it to String, the cast every type here has.
		for i, f := range ops {
			if !dtype.CanCast(f.Type, dtype.String) {
				return dtype.Null, uerr.New(uerr.KindType, "concat_str",
					"concat_str cannot format operand %d (%s), a %s, as text", i+1, f.Name, f.Type).
					Hint("take a field or an element of it first, which can be formatted")
			}
		}
		return dtype.String, nil

	case FnStructOf:
		// A field is named after its operand, so two operands of one name would be
		// two fields Field("name") could not tell apart.
		fields := make([]dtype.Field, len(ops))
		seen := make(map[string]bool, len(ops))
		for i, f := range ops {
			if seen[f.Name] {
				return dtype.Null, uerr.New(uerr.KindSchema, "struct",
					"two fields of the struct are named %q", f.Name).
					Hint("alias one of them, e.g. .Alias(\"%s_2\")", f.Name)
			}
			seen[f.Name] = true
			fields[i] = dtype.Field{Name: f.Name, Type: f.Type, Nullable: true}
		}
		return dtype.Struct(fields...), nil

	case FnStructWithFields:
		return withFieldsOut(ops)

	case FnCut, FnCutLeftClosed:
		if !ops[0].Type.IsNumeric() {
			return dtype.Null, uerr.New(uerr.KindType, "cut",
				"%s requires a numeric operand, got %s", fn, ops[0].Type).
				Hint("cast it first, e.g. .Cast(ursus.Float64)")
		}
		k := CutBreaks(ops)
		for _, f := range ops[1 : 1+k] {
			if !f.Type.IsNumeric() && !f.Type.IsNull() {
				return dtype.Null, uerr.Internalf("expr: %s break of type %s", fn, f.Type)
			}
		}
		if labels := len(ops) - 1 - k; labels != 0 && labels != k+1 {
			return dtype.Null, uerr.Internalf("expr: %s with %d breaks and %d labels", fn, k, labels)
		}
		return dtype.String, nil

	case FnListSetUnion, FnListSetIntersection, FnListSetDifference,
		FnListSetSymmetricDifference:
		return listSetOut(fn, ops)

	case FnMaxHorizontal, FnMinHorizontal, FnSumHorizontal, FnMeanHorizontal:
		in, err := HorizontalInput(fn, ops)
		if err != nil {
			return dtype.Null, err
		}
		b, err := ResolveAggBinding(HorizontalAgg(fn), in)
		if err != nil {
			return dtype.Null, err
		}
		return b.Out, nil

	case FnAnyHorizontal, FnAllHorizontal:
		for i, f := range ops {
			if !f.Type.IsBool() && !f.Type.IsNull() {
				return dtype.Null, uerr.New(uerr.KindType, fn.String(),
					"%s requires Boolean operands, and operand %d (%s) is a %s", fn, i+1, f.Name, f.Type).
					Hint("compare it first, e.g. Col(%q).Gt(0)", f.Name)
			}
		}
		return dtype.Bool, nil

	case FnDtCombine:
		if len(ops) != 2 {
			return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
		}
		if t := ops[1].Type; t.ID() != dtype.TypeTime && !t.IsNull() {
			return dtype.Null, uerr.New(uerr.KindType, "dt",
				"%s takes a Time of day, got %s (%s)", fn, t, ops[1].Name).
				Hint("cast it first, e.g. .Cast(ursus.Time(ursus.Nano))")
		}
		// The receiver's own unit, a Date's microseconds, and its zone: Polars
		// answers microseconds unless asked, and a Cast gives another unit.
		switch in := ops[0].Type; in.ID() {
		case dtype.TypeDate:
			return dtype.Datetime(dtype.Micro, ""), nil
		case dtype.TypeDatetime:
			return in, nil
		default:
			return dtype.Null, uerr.New(uerr.KindType, "dt",
				"%s requires a Date or Datetime operand, got %s", fn, in).
				Hint("a Time has no day to combine with")
		}

	case FnListSortBy, FnListSortByDesc:
		if len(ops) != 2 {
			return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
		}
		if ops[0].Type.ID() != dtype.TypeList || ops[1].Type.ID() != dtype.TypeList {
			return dtype.Null, uerr.New(uerr.KindType, "list",
				"%s requires two Lists, got %s and %s", fn, ops[0].Type, ops[1].Type).
				Hint("each row's keys are a list of the row's length")
		}
		if !ops[1].Type.Inner().IsOrdered() {
			return dtype.Null, uerr.New(uerr.KindType, "list",
				"%s sorts by %s, which has no order", fn, ops[1].Type.Inner())
		}
		return ops[0].Type, nil

	case FnStrContainsExpr, FnStrStartsWithExpr, FnStrEndsWithExpr, FnStrStripPrefixExpr,
		FnStrStripSuffixExpr, FnStrSplitExpr, FnStrReplaceExpr, FnStrReplaceAllExpr:
		return strExprOut(fn, ops)

	case FnIntRanges:
		if len(ops) != 3 {
			return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
		}
		for _, f := range ops {
			if !f.Type.IsInteger() && !f.Type.IsNull() {
				return dtype.Null, uerr.New(uerr.KindType, "int_range",
					"int_range takes integers, and %s is a %s", f.Name, f.Type).
					Hint("cast it first, e.g. .Cast(ursus.Int64)")
			}
		}
		return dtype.List(dtype.Int64), nil

	case FnLinearSpaces:
		if len(ops) != 4 {
			return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
		}
		if n := ops[2].Type; !n.IsInteger() && !n.IsNull() {
			return dtype.Null, uerr.New(uerr.KindType, "linear_space",
				"linear_space takes its number of samples as an integer, got %s", n)
		}
		elem, err := linearElem(ops[0].Type, ops[1].Type)
		if err != nil {
			return dtype.Null, err
		}
		return dtype.List(elem), nil

	case FnMakeDate, FnMakeDatetime, FnMakeTime:
		if want := map[CallFn]int{FnMakeDate: 3, FnMakeDatetime: 7, FnMakeTime: 4}[fn]; len(ops) != want {
			return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
		}
		for _, f := range ops {
			if !f.Type.IsInteger() && !f.Type.IsNull() {
				return dtype.Null, uerr.New(uerr.KindType, fn.String(),
					"%s takes integer components, and %s is a %s", fn, f.Name, f.Type).
					Hint("cast it first, e.g. .Cast(ursus.Int64)")
			}
		}
		switch fn {
		case FnMakeDate:
			return dtype.Date, nil
		case FnMakeDatetime:
			return dtype.Datetime(dtype.Micro, ""), nil
		}
		return dtype.Time(dtype.Nano), nil

	case FnDurationTicks, FnEpochTicks:
		if want := map[CallFn]int{FnDurationTicks: 9, FnEpochTicks: 2}[fn]; len(ops) != want {
			return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
		}
		for _, f := range ops {
			if !f.Type.IsInteger() && !f.Type.IsFloat() && !f.Type.IsNull() {
				op := map[CallFn]string{FnDurationTicks: "duration", FnEpochTicks: "from_epoch"}[fn]
				return dtype.Null, uerr.New(uerr.KindType, op,
					"%s takes numbers, and %s is a %s", op, f.Name, f.Type)
			}
		}
		return dtype.Int64, nil

	case FnStrParseInt, FnStrParseUint:
		if len(ops) != 3 {
			return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
		}
		if s := ops[0].Type; s.ID() != dtype.TypeString && !s.IsNull() && s.ID() != dtype.TypeEnum {
			return dtype.Null, uerr.New(uerr.KindType, "str", "%s requires a String operand, got %s", fn, s)
		}
		if b := ops[1].Type; !b.IsInteger() && !b.IsNull() {
			return dtype.Null, uerr.New(uerr.KindType, "str.to_integer", "to_integer takes an integer base, got %s", b)
		}
		if ops[2].Type != dtype.Bool {
			return dtype.Null, uerr.Internalf("expr: %s with strict a %s", fn, ops[2].Type)
		}
		if fn == FnStrParseUint {
			return dtype.Uint64, nil
		}
		return dtype.Int64, nil

	case FnConcatStrIgnoreNulls:
		if len(ops) < 2 || !ops[len(ops)-1].Type.HasStringStorage() {
			return dtype.Null, uerr.Internalf("expr: %s without its separator last", fn)
		}
		for i, f := range ops[:len(ops)-1] {
			if !dtype.CanCast(f.Type, dtype.String) {
				return dtype.Null, uerr.New(uerr.KindType, "concat_str",
					"concat_str cannot format operand %d (%s), a %s, as text", i+1, f.Name, f.Type)
			}
		}
		return dtype.String, nil

	case FnListShift, FnListCountMatches:
		if len(ops) != 2 {
			return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
		}
		l, v := ops[0].Type, ops[1].Type
		if l.ID() != dtype.TypeList {
			return dtype.Null, uerr.New(uerr.KindType, "list", "%s requires a List operand, got %s", fn, l)
		}
		if fn == FnListShift {
			if !v.IsInteger() && !v.IsNull() {
				return dtype.Null, uerr.New(uerr.KindType, "list.shift", "list.shift takes an integer count, got %s", v)
			}
			return l, nil
		}
		if !v.IsNull() {
			if _, err := ResolveBinary(OpEq, l.Inner(), v); err != nil {
				return dtype.Null, uerr.New(uerr.KindType, "list.count_matches",
					"list.count_matches cannot compare a List of %s with a %s value", l.Inner(), v)
			}
		}
		return dtype.Uint32, nil

	case FnConcatList:
		elems := make([]dtype.Field, len(ops))
		for i, f := range ops {
			elems[i] = f
			if f.Type.ID() == dtype.TypeList {
				elems[i].Type = f.Type.Inner()
			}
		}
		elem, err := HorizontalInput(fn, elems)
		if err != nil {
			return dtype.Null, err
		}
		return dtype.List(elem), nil

	case FnDtIsBusinessDay, FnDtAddBusinessDays:
		want := map[CallFn]int{FnDtIsBusinessDay: 3, FnDtAddBusinessDays: 5}[fn]
		if len(ops) != want {
			return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
		}
		v := ops[0].Type
		if v.ID() != dtype.TypeDate && v.ID() != dtype.TypeDatetime && !v.IsNull() {
			return dtype.Null, uerr.New(uerr.KindType, "dt",
				"%s requires a Date or Datetime operand, got %s", fn, v)
		}
		if fn == FnDtIsBusinessDay {
			return dtype.Bool, nil
		}
		if n := ops[1].Type; !n.IsInteger() && !n.IsNull() {
			return dtype.Null, uerr.New(uerr.KindType, "dt",
				"add_business_days takes an integer count, got %s", n)
		}
		if v.IsNull() {
			return dtype.Date, nil
		}
		return v, nil

	case FnBinContains, FnBinStartsWith, FnBinEndsWith, FnBinGet, FnBinSlice:
		return binHorizontalOut(fn, ops)

	case FnOrderCmp:
		if len(ops) != 2 {
			return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
		}
		if _, err := OrderCmpType(ops[0].Type, ops[1].Type); err != nil {
			return dtype.Null, err
		}
		return dtype.Int8, nil

	case FnDtReplace:
		if len(ops) != 10 {
			return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
		}
		v := ops[0].Type
		if v.ID() != dtype.TypeDate && v.ID() != dtype.TypeDatetime {
			return dtype.Null, uerr.New(uerr.KindType, "dt",
				"%s requires a Date or Datetime operand, got %s", fn, v).
				Hint("a Time is replaced by building it again with MakeTime")
		}
		for i, f := range ops[1:8] {
			if !f.Type.IsInteger() && !f.Type.IsNull() {
				return dtype.Null, uerr.New(uerr.KindType, "dt",
					"replace's %s is an integer, got %s", DateTimeParts[i], f.Type)
			}
		}
		return v, nil

	case FnBusinessDayCount:
		if len(ops) != 4 {
			return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
		}
		for _, f := range ops[:2] {
			if f.Type.ID() != dtype.TypeDate && !f.Type.IsNull() {
				return dtype.Null, uerr.New(uerr.KindType, "business_day_count",
					"business_day_count counts between Dates, and %s is a %s", f.Name, f.Type).
					Hint("take the date first, e.g. .Dt().Date()")
			}
		}
		return dtype.Int32, nil

	case FnDateRanges, FnDatetimeRanges, FnTimeRanges:
		if len(ops) != 6 {
			return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
		}
		elem, err := rangeElem(fn, ops[0].Type, ops[1].Type)
		if err != nil {
			return dtype.Null, err
		}
		return dtype.List(elem), nil

	case FnRepeatBy:
		if len(ops) != 2 {
			return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
		}
		if n := ops[1].Type; !n.IsInteger() && !n.IsNull() {
			return dtype.Null, uerr.New(uerr.KindType, "repeat_by",
				"repeat_by takes its count as an integer, got %s", n)
		}
		return dtype.List(ops[0].Type), nil

	case FnDtOffsetByExpr:
		if len(ops) != 2 {
			return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
		}
		in := ops[0].Type
		if in.ID() != dtype.TypeDate && in.ID() != dtype.TypeDatetime {
			e := uerr.New(uerr.KindType, "dt", "%s requires a Date or Datetime operand, got %s", fn, in)
			if in.ID() == dtype.TypeDuration || in.ID() == dtype.TypeTime {
				e.Hint("add a Duration with Add instead")
			}
			return dtype.Null, e
		}
		if by := ops[1].Type; !by.HasStringStorage() && !by.IsNull() {
			return dtype.Null, uerr.New(uerr.KindType, "dt",
				"%s takes its offsets as Strings such as \"1mo2d\", got %s", fn, by).
				Hint("a Duration column is added with Add")
		}
		return in, nil

	case FnIsInExpr:
		if len(ops) != 2 {
			return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
		}
		x, l := ops[0].Type, ops[1].Type
		if l.ID() != dtype.TypeList {
			e := uerr.New(uerr.KindType, "is_in",
				"is_in takes a List of each row's values, and %s is a %s", ops[1].Name, l)
			if l == x {
				// Polars 1.44 deprecates this spelling as ambiguous, and asks for implode.
				e.Hint("for membership among the whole column's values, Implode it: IsIn(Col(%q).Implode())",
					ops[1].Name)
			}
			return dtype.Null, e
		}
		if x.IsNull() {
			return dtype.Bool, nil
		}
		if !x.IsHashable() {
			return dtype.Null, uerr.New(uerr.KindType, "is_in", "is_in is not defined for %s", x).
				Hint("the value must be hashable: numeric, temporal, string or boolean")
		}
		if _, err := ResolveBinary(OpEq, x, l.Inner()); err != nil {
			return dtype.Null, uerr.New(uerr.KindType, "is_in",
				"is_in cannot compare %s with a List of %s", x, l.Inner()).
				Hint("it compares as == does, and == has no common type for these")
		}
		return dtype.Bool, nil

	case FnListContainsExpr:
		if len(ops) != 2 {
			return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
		}
		l, v := ops[0].Type, ops[1].Type
		if l.ID() != dtype.TypeList {
			return dtype.Null, uerr.New(uerr.KindType, "list",
				"%s requires a List operand, got %s", fn, l).
				Hint("only a List column has elements to look among")
		}
		if !v.IsNull() {
			if _, err := ResolveBinary(OpEq, l.Inner(), v); err != nil {
				return dtype.Null, uerr.New(uerr.KindType, "list.contains",
					"list.contains cannot compare a List of %s with a %s value", l.Inner(), v).
					Hint("it compares as == does, and == has no common type for these")
			}
		}
		return dtype.Bool, nil

	case FnListGetExpr:
		if len(ops) != 2 {
			return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
		}
		if ops[0].Type.ID() != dtype.TypeList {
			return dtype.Null, uerr.New(uerr.KindType, "list",
				"%s requires a List operand, got %s", fn, ops[0].Type).
				Hint("only a List column has elements to get")
		}
		if i := ops[1].Type; !i.IsInteger() && !i.IsNull() {
			return dtype.Null, uerr.New(uerr.KindType, "list.get",
				"list.get takes an integer index, got %s", i).
				Hint("cast it first, e.g. .Cast(ursus.Int64)")
		}
		return ops[0].Type.Inner(), nil

	case FnListGather, FnListGatherOrNull:
		if len(ops) != 2 {
			return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
		}
		if ops[0].Type.ID() != dtype.TypeList {
			return dtype.Null, uerr.New(uerr.KindType, "list",
				"%s requires a List operand, got %s", fn, ops[0].Type).
				Hint("only a List column has elements to gather")
		}
		if ops[1].Type.ID() != dtype.TypeList || !ops[1].Type.Inner().IsInteger() {
			return dtype.Null, uerr.New(uerr.KindType, "list",
				"%s takes its indices as a List of integers, got %s", fn, ops[1].Type).
				Hint("each row's indices are a list; Implode a column of them, or cast")
		}
		return ops[0].Type, nil
	}
	return dtype.Null, uerr.Internalf("expr: unknown horizontal call %d", fn)
}

// rangeElem is a temporal range's element type from ends of types s and e (step
// 264), as Polars types it: a Date for date_range, from Dates or Datetimes; for
// datetime_range the Datetime of the ends, a Date's taken as Datetime(us); a Time
// for time_range.
func rangeElem(fn CallFn, s, e dtype.DataType) (dtype.DataType, error) {
	switch {
	case s.IsNull():
		s = e
	case e.IsNull():
		e = s
	}
	date := func(t dtype.DataType) bool { return t.ID() == dtype.TypeDate }
	stamp := func(t dtype.DataType) bool { return t.ID() == dtype.TypeDatetime }
	switch fn {
	case FnDateRanges:
		if (date(s) || stamp(s)) && (date(e) || stamp(e)) {
			return dtype.Date, nil
		}
	case FnDatetimeRanges:
		switch {
		case date(s) && date(e):
			return dtype.Datetime(dtype.Micro, ""), nil
		case stamp(s) && (s == e || date(e)):
			return s, nil
		case date(s) && stamp(e):
			return e, nil
		}
	case FnTimeRanges:
		if s.ID() == dtype.TypeTime && s == e {
			return s, nil
		}
	}
	return dtype.Null, uerr.New(uerr.KindType, Generators[fn],
		"%s takes two ends of its type, got %s and %s", Generators[fn], s, e).
		Hint("cast one end to the other's type")
}

// linearElem is the element type of linear_space from ends of types s and e, as
// Polars types it: Float32 for two Float32 ends, Float64 for other numbers, and a
// temporal type for two ends of it, a Date's spaced as Datetime(us).
func linearElem(s, e dtype.DataType) (dtype.DataType, error) {
	switch {
	case s.IsNull() && e.IsNull():
		return dtype.Float64, nil
	case s.IsNull():
		s = e
	case e.IsNull():
		e = s
	}
	switch {
	case s.IsNumeric() && e.IsNumeric():
		if s == dtype.Float32 && e == dtype.Float32 {
			return dtype.Float32, nil
		}
		return dtype.Float64, nil
	case s.IsTemporal() && s == e:
		if s.ID() == dtype.TypeDate {
			return dtype.Datetime(dtype.Micro, ""), nil
		}
		return s, nil
	}
	return dtype.Null, uerr.New(uerr.KindType, "linear_space",
		"linear_space spaces two numbers or two values of one temporal type, got %s and %s", s, e).
		Hint("cast one end to the other's type")
}

// strConstant is each per-row string function's constant form, which types it.
var strConstant = map[CallFn]CallFn{
	FnStrContainsExpr: FnStrContains, FnStrStartsWithExpr: FnStrStartsWith,
	FnStrEndsWithExpr: FnStrEndsWith, FnStrStripPrefixExpr: FnStrStripPrefix,
	FnStrStripSuffixExpr: FnStrStripSuffix, FnStrSplitExpr: FnStrSplit,
	FnStrReplaceExpr: FnStrReplace, FnStrReplaceAllExpr: FnStrReplaceAll,
}

// strExprOut types a string function of a per-row pattern or value (step 255): the
// receiver, pattern and value Strings, the flags Bools, and the answer the constant
// form's.
func strExprOut(fn CallFn, ops []dtype.Field) (dtype.DataType, error) {
	flag := -1 // the literal flag's operand
	want := 2
	switch fn {
	case FnStrContainsExpr:
		flag, want = 2, 3
	case FnStrReplaceExpr, FnStrReplaceAllExpr:
		flag, want = 3, 4
	}
	if len(ops) != want {
		return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
	}
	for i, f := range ops {
		if i == flag {
			if f.Type.ID() != dtype.TypeBool {
				return dtype.Null, uerr.Internalf("expr: %s's flag is a %s", fn, f.Type)
			}
			continue
		}
		if f.Type.ID() != dtype.TypeString && !f.Type.IsNull() && f.Type.ID() != dtype.TypeEnum {
			what := "pattern"
			switch {
			case i == 0:
				what = "operand"
			case want == 4 && i == 2:
				what = "value"
			}
			return dtype.Null, uerr.New(uerr.KindType, "str",
				"%s requires a String %s, got %s", fn, what, f.Type).
				Hint("cast it first, e.g. .Cast(ursus.String)")
		}
	}
	return strCallOut(strConstant[fn]), nil
}

// HorizontalAgg is the aggregate a horizontal reduction runs over each row's values.
func HorizontalAgg(fn CallFn) AggOp {
	switch fn {
	case FnMinHorizontal:
		return AggMin
	case FnSumHorizontal:
		return AggSum
	case FnMeanHorizontal:
		return AggMean
	}
	return AggMax
}

// HorizontalInput is the type a horizontal reduction's operands meet at, as a
// conditional's branches meet: Promote, so an Int32 and a Float64 are a Float64.
// The aggregate's binding then types the answer, as for a column.
func HorizontalInput(fn CallFn, ops []dtype.Field) (dtype.DataType, error) {
	in := dtype.Null
	for _, f := range ops {
		t, ok := dtype.Promote(in, f.Type)
		if !ok {
			return dtype.Null, uerr.New(uerr.KindType, fn.String(),
				"%s: its operands have no common type: %s and %s (%s)", fn, in, f.Type, f.Name).
				Hint("cast the operands so they agree")
		}
		in = t
	}
	return in, nil
}

// listSetOut types a list set operation: a List of the type both lists' elements
// meet at, compared by grouping equality.
//
// PromoteExact, as a join's keys and a Concat meet: an Int64 against a Float64 would
// be compared after rounding, so 2⁵³+1 would equal 2⁵³. Polars meets them at Float64;
// this refuses. Elements are compared as group_by compares keys, so they must be
// hashable.
func listSetOut(fn CallFn, ops []dtype.Field) (dtype.DataType, error) {
	if len(ops) != 2 {
		return dtype.Null, uerr.Internalf("expr: %s with %d operands", fn, len(ops))
	}
	a, b := ops[0].Type, ops[1].Type
	if a.ID() != dtype.TypeList || b.ID() != dtype.TypeList {
		return dtype.Null, uerr.New(uerr.KindType, "list",
			"%s requires two Lists, got %s and %s", fn, a, b).
			Hint("only List columns have elements to compare")
	}
	elem, ok := dtype.PromoteExact(a.Inner(), b.Inner())
	if !ok {
		return dtype.Null, uerr.New(uerr.KindType, "list",
			"%s: the two lists' elements have no common type: %s and %s", fn, a.Inner(), b.Inner()).
			Hint("cast one of them, e.g. .Cast(ursus.List(%s))", a.Inner())
	}
	if !elem.IsHashable() && !elem.IsNull() {
		return dtype.Null, uerr.New(uerr.KindType, "list",
			"%s compares elements, and %s elements cannot be compared", fn, elem).
			Hint("the elements must be numeric, temporal, string or boolean")
	}
	return dtype.List(elem), nil
}

// genCallOut types the family that does not dispatch on the receiver's type.
func genCallOut(c *Call, in dtype.DataType) (dtype.DataType, error) {
	switch fn := c.Fn; fn {
	case FnIsIn:
		// Membership is decided by the same encoding group_by and distinct use, so
		// the receiver must be a type that encoding accepts.
		if !in.IsHashable() && !in.IsNull() {
			return dtype.Null, uerr.New(uerr.KindType, "is_in",
				"is_in is not defined for %s", in).
				Hint("the value must be hashable: numeric, temporal, string or boolean")
		}
		if !in.IsNull() {
			if _, err := MembershipType(c, in); err != nil {
				return dtype.Null, err
			}
		}
		return dtype.Bool, nil

	case FnReplace, FnReplaceStrict, FnReplaceOr:
		return replaceOut(c, in)

	case FnCatGetCategories:
		return dtype.Null, uerr.Internalf("expr: %s is written out when its expression is expanded", fn)

	case FnHash:
		if !in.IsHashable() && !in.IsNull() {
			return dtype.Null, uerr.New(uerr.KindType, "hash", "hash reads values a group-by can key on, not %s", in)
		}
		return dtype.Uint64, nil

	case FnOrderSign:
		// Numbers, instants and Booleans have a zero; text has none, and Polars'
		// peaks refuse it.
		if !in.IsOrdered() || in.HasStringStorage() || in.ID() == dtype.TypeEnum {
			return dtype.Null, uerr.New(uerr.KindType, "order_sign", "a %s has no zero to be measured from", in)
		}
		return dtype.Int8, nil

	case FnCatPhysical:
		if in.ID() != dtype.TypeEnum {
			return dtype.Null, uerr.New(uerr.KindType, "cat",
				"%s requires an Enum operand, got %s", fn, in)
		}
		return EnumPhysical(in), nil

	default:
		return dtype.Null, uerr.Internalf("expr: unknown general call %d", fn)
	}
}

// ReplaceParts reads a replace call's literals: its old values, its new ones, and
// its default, nil but for FnReplaceOr.
func ReplaceParts(c *Call) (old, new []*Lit, def *Lit, err error) {
	lits := make([]*Lit, 0, len(c.Args)-1)
	for _, a := range c.Args[1:] {
		l, ok := a.(*Lit)
		if !ok {
			return nil, nil, nil, uerr.Internalf("expr: %s argument %s is not a literal", c.Fn, a)
		}
		lits = append(lits, l)
	}
	if len(lits) == 0 {
		return nil, nil, nil, uerr.Internalf("expr: %s has no count", c.Fn)
	}
	k, ok := lits[0].Value.(int64)
	want := 1 + 2*int(k)
	if c.Fn == FnReplaceOr {
		want++
	}
	if !ok || k < 0 || len(lits) != want {
		return nil, nil, nil, uerr.Internalf("expr: %s with %d literals", c.Fn, len(lits))
	}
	old, new = lits[1:1+k], lits[1+k:1+2*k]
	if c.Fn == FnReplaceOr {
		def = lits[1+2*k]
	}
	return old, new, def, nil
}

// replaceOut types a value map.
//
// The old values meet the receiver as is_in's do (membershipOf), so `x.Replace(old,
// new)` replaces exactly the rows `x.IsIn(old...)` holds. Replace answers as the
// conditional `When(x.IsIn(old_j)).Then(new_j)…Otherwise(x)` would: each new value
// meets the receiver as a branch meets the other, a weak literal taking the
// column's type when it fits, so 20 into an Int32 is an Int32 and 2.5 into an Int64
// is a Float64, where Polars truncates it to 2. ReplaceStrict and ReplaceOr answer
// the type their new values and default meet at, as Polars' replace_strict does.
func replaceOut(c *Call, in dtype.DataType) (dtype.DataType, error) {
	old, new, def, err := ReplaceParts(c)
	if err != nil {
		return dtype.Null, err
	}
	if !in.IsHashable() && !in.IsNull() {
		return dtype.Null, uerr.New(uerr.KindType, c.Fn.String(),
			"%s is not defined for %s", c.Fn, in).
			Hint("the value must be hashable: numeric, temporal, string or boolean")
	}
	if !in.IsNull() {
		if _, err := membershipOf(c.Fn, in, old); err != nil {
			return dtype.Null, err
		}
	}
	if c.Fn == FnReplace {
		out := in
		for _, l := range new {
			if out, err = ResolveCond(l, c.Args[0], l.DT, out); err != nil {
				return dtype.Null, uerr.New(uerr.KindType, c.Fn.String(),
					"%s cannot put a %s value in a %s column", c.Fn, l.DT, in).
					Hint("cast the column, or use ReplaceStrict, whose answer is the new values' type")
			}
		}
		return out, nil
	}
	vals := new
	if def != nil {
		vals = append(slices.Clone(new), def)
	}
	if len(vals) == 0 {
		return dtype.Null, uerr.New(uerr.KindSchema, c.Fn.String(),
			"%s has no values to answer with", c.Fn).
			Hint("give it at least one old value and its new one")
	}
	out := vals[0].DT
	for _, l := range vals[1:] {
		t, ok := dtype.Promote(out, l.DT)
		if !ok {
			return dtype.Null, uerr.New(uerr.KindType, c.Fn.String(),
				"%s: its new values have no common type: %s and %s", c.Fn, out, l.DT).
				Hint("give new values of one type")
		}
		out = t
	}
	return out, nil
}

// MembershipType is the type is_in and list.contains compare at: the type Eq would
// compare the receiver — for list.contains, its element — and each value at. So
// `x.IsIn(v)` refuses exactly the pairs `x.Eq(v)` refuses, and answers the same
// rows wherever both are defined.
//
// The values used to be cast to the receiver's type, which answered a different
// question. A value the column cannot hold — 5000 against an Int8, −1 against a
// Uint64 — was an error where Eq says false; a string, a bool or an instant was
// parsed or converted, so an Int64 matched "1", a Bool matched 1 and a Date matched
// 13:00 on that day, all pairs Eq refuses; and a value was rounded to the column's
// precision, so a Datetime(s) matched 00:00:01.5 and a Float32 matched 0.1.
//
// Equality is still grouping equality, so NaN matches NaN — the one place the two
// differ, and on purpose.
func MembershipType(c *Call, against dtype.DataType) (dtype.DataType, error) {
	if c.Fn == FnReplace || c.Fn == FnReplaceStrict || c.Fn == FnReplaceOr {
		old, _, _, err := ReplaceParts(c)
		if err != nil {
			return dtype.Null, err
		}
		return membershipOf(c.Fn, against, old)
	}
	lits := make([]*Lit, 0, len(c.Args)-1)
	for _, a := range c.Args[1:] {
		l, ok := a.(*Lit)
		if !ok {
			return dtype.Null, uerr.Internalf("expr: %s argument %s is not a literal", c.Fn, a)
		}
		lits = append(lits, l)
	}
	return membershipOf(c.Fn, against, lits)
}

// membershipOf is MembershipType over the values lits.
func membershipOf(fn CallFn, against dtype.DataType, lits []*Lit) (dtype.DataType, error) {
	common := against
	for _, l := range lits {
		b, err := ResolveBinary(OpEq, common, l.DT)
		if err != nil {
			return dtype.Null, uerr.New(uerr.KindType, fn.String(),
				"%s cannot compare %s with a %s value", fn, against, l.DT).
				Hint("it compares as == does, and == has no common type for these").
				Hint("cast one side so the two meet, e.g. .Cast(ursus.%s)", l.DT)
		}
		common = b.CastL
	}
	return common, nil
}

// mathCallOut types the parameterised maths family.
//
// Round PRESERVES the operand's type, like Floor and Ceil and for the same reason:
// rounding an integer to any number of decimals is the identity, and widening it
// to a float would change a schema in exchange for nothing.
func mathCallOut(fn CallFn, in dtype.DataType) (dtype.DataType, error) {
	switch fn {
	case FnMathRound:
		// Null is refused, as it is for floor() and ceil() — round is their
		// Call-surface twin and shared their old mistake of promising `in` for a
		// Null operand, which is a type no rounding kernel can dispatch on. The
		// matrix cannot see this one: it has no Call arm, so it was found by
		// following floor and ceil rather than by measurement.
		if !in.IsNumeric() {
			return dtype.Null, uerr.New(uerr.KindType, "round",
				"round() requires a numeric operand, got %s", in).
				Hint("cast it first, e.g. .Cast(ursus.Float64)")
		}
		if in.ID() == dtype.TypeDecimal {
			return dtype.Null, decimalRemedy(uerr.New(uerr.KindType, "round",
				"round() is not defined for %s", in).
				Hint("a decimal is stored as an unscaled integer, so rounding it would " +
					"scale the wrong number"))
		}
		return in, nil
	case FnMathAsFloat:
		// Only PctChange builds this, so its refusal speaks for PctChange: the
		// change between two instants is a Duration, which has no ratio to an
		// instant.
		switch {
		case in.IsFloat():
			return in, nil
		case in.IsNumeric() || in.ID() == dtype.TypeDuration:
			return dtype.Float64, nil
		}
		return dtype.Null, uerr.New(uerr.KindType, "pct_change",
			"pct_change is not defined for %s", in).
			Hint("it is defined for numbers and Durations; for an instant, take Diff, a Duration")
	case FnMathCountOnes, FnMathCountZeros, FnMathLeadingOnes, FnMathLeadingZeros,
		FnMathTrailingOnes, FnMathTrailingZeros:
		// Counted in the type's own width, so the leading zeros of an Int8 1 are 7.
		// Int128 has no kernel here, and a Bool is not a run of bits.
		if !in.IsInteger() || in.ID() == dtype.TypeInt128 {
			return dtype.Null, uerr.New(uerr.KindType, fn.String(),
				"%s requires an integer operand of 8 to 64 bits, got %s", fn, in)
		}
		return dtype.Uint32, nil
	case FnMathDiffWiden:
		// One width up, which holds every difference exactly: a UInt8 difference
		// is within ±255. UInt64's is within ±(2^64−1), which only Int128 holds;
		// Polars gives Int64 there, and nulls where it overflows.
		switch in.ID() {
		case dtype.TypeUint8:
			return dtype.Int16, nil
		case dtype.TypeUint16:
			return dtype.Int32, nil
		case dtype.TypeUint32:
			return dtype.Int64, nil
		case dtype.TypeUint64:
			return dtype.Int128, nil
		}
		return in, nil
	default:
		return dtype.Null, uerr.Internalf("expr: unknown maths call %d", fn)
	}
}

func strCallOut(fn CallFn) dtype.DataType {
	switch fn {
	case FnStrContains, FnStrStartsWith, FnStrEndsWith, FnStrContainsAny:
		return dtype.Bool
	case FnStrFind, FnStrCountMatches, FnStrLenBytes, FnStrLenChars:
		return dtype.Uint32
	case FnStrFindMany:
		return dtype.List(dtype.Uint32)
	case FnStrDecode:
		return dtype.Binary
	case FnStrSplit, FnStrSplitInclusive, FnStrExtractAll, FnStrExtractMany:
		// The first calls in the namespace whose output is a NESTED type, and the
		// only place that fact is written down. The kernel never names List(String)
		// — data.NewList derives the element type from the child column it is
		// handed, deliberately, so that the declared type cannot disagree with the
		// data. This is the other half of that contract, and the two agreeing is
		// what ResolveCall means by being the single authority.
		//
		// Constant for every receiver, because the family has already normalised
		// Enum and Binary receivers down to String by the time this is reached.
		return dtype.List(dtype.String)
	default:
		return dtype.String
	}
}

// strStructOut types the string functions that answer a Struct (step 270), whose
// fields their arguments name: extract_groups a String field a capture group, named
// as GroupNames names it; split_exact field_0 to field_n; splitn field_0 to
// field_{n-1}, as Polars names them.
func strStructOut(c *Call) (dtype.DataType, error) {
	var names []string
	switch c.Fn {
	case FnStrExtractGroups:
		pat, ok := callLitString(c, 1)
		if !ok {
			return dtype.Null, uerr.Internalf("expr: %s without its pattern", c.Fn)
		}
		re, err := regexp.Compile(pat)
		if err != nil {
			return dtype.Null, uerr.Wrap(err, uerr.KindValue, "str", "invalid regular expression %q", pat)
		}
		if names = GroupNames(re); len(names) == 0 {
			return dtype.Null, uerr.New(uerr.KindValue, "str.extract_groups",
				"extract_groups's pattern %q has no capture group", pat).
				Hint("a group is (...), and named (?<name>...)")
		}
	default:
		l, ok := c.Args[len(c.Args)-1].(*Lit)
		if c.Fn == FnStrSplitExact && len(c.Args) == 4 {
			l, ok = c.Args[2].(*Lit)
		}
		var n int64
		if ok {
			n, ok = l.Value.(int64)
		}
		if !ok {
			return dtype.Null, uerr.Internalf("expr: %s without its count", c.Fn)
		}
		if c.Fn == FnStrSplitExact {
			n++
		}
		if n < 1 {
			return dtype.Null, uerr.New(uerr.KindValue, "str.splitn", "splitn takes at least one part, got %d", n).
				Hint("Split makes a List of every part")
		}
		for j := range n {
			names = append(names, "field_"+strconv.FormatInt(j, 10))
		}
	}
	fields := make([]dtype.Field, len(names))
	for i, nm := range names {
		fields[i] = dtype.Field{Name: nm, Type: dtype.String, Nullable: true}
	}
	return dtype.Struct(fields...), nil
}

// jsonDecodeOut is json_decode's type, the type of its literal argument: Booleans,
// numbers, Strings, and Lists and Structs of them (step 271).
func jsonDecodeOut(c *Call) (dtype.DataType, error) {
	l, ok := c.Args[len(c.Args)-1].(*Lit)
	if !ok || len(c.Args) != 2 {
		return dtype.Null, uerr.Internalf("expr: %s without its type", c.Fn)
	}
	var check func(t dtype.DataType) error
	check = func(t dtype.DataType) error {
		switch {
		case t.ID() == dtype.TypeList:
			return check(t.Inner())
		case t.ID() == dtype.TypeStruct:
			for _, f := range t.Fields() {
				if err := check(f.Type); err != nil {
					return err
				}
			}
			return nil
		case t.ID() == dtype.TypeBool, t.ID() == dtype.TypeString, t.IsInteger(), t.IsFloat(), t.IsNull():
			return nil
		}
		return uerr.New(uerr.KindType, "str.json_decode",
			"json_decode decodes to Booleans, numbers, Strings, Lists and Structs, not %s", t).
			Hint("decode to a String and convert it after")
	}
	if err := check(l.DT); err != nil {
		return dtype.Null, err
	}
	return nullableThrough(l.DT), nil
}

// nullableThrough is t with every Struct field nullable, at any depth: a JSON object
// may leave out any field.
func nullableThrough(t dtype.DataType) dtype.DataType {
	switch t.ID() {
	case dtype.TypeList:
		return dtype.List(nullableThrough(t.Inner()))
	case dtype.TypeStruct:
		fields := t.Fields()
		out := make([]dtype.Field, len(fields))
		for i, f := range fields {
			out[i] = dtype.Field{Name: f.Name, Type: nullableThrough(f.Type), Nullable: true}
		}
		return dtype.Struct(out...)
	}
	return t
}

// GroupNames are a pattern's capture groups' field names, as Polars names them:
// each group's name, or its number where it has none.
func GroupNames(re *regexp.Regexp) []string {
	names := re.SubexpNames()[1:]
	out := make([]string, len(names))
	for i, nm := range names {
		if nm == "" {
			nm = strconv.Itoa(i + 1)
		}
		out[i] = nm
	}
	return out
}

// OrderCmpType is the type order_cmp compares a and b in: their common one, which
// must have an order.
func OrderCmpType(a, b dtype.DataType) (dtype.DataType, error) {
	common, ok := dtype.Promote(a, b)
	if !ok || !common.IsOrdered() && !common.IsNull() {
		return dtype.Null, uerr.New(uerr.KindType, "", "%s and %s have no order to compare them in", a, b)
	}
	return common, nil
}

// DateTimeParts are dt.replace's components, in their operands' order.
var DateTimeParts = []string{"year", "month", "day", "hour", "minute", "second", "microsecond"}

// TotalUnits are the units a fractional total counts in, as their nanoseconds: days
// to nanoseconds. EpochUnits are epoch's: nanoseconds to seconds, and days.
var (
	TotalUnits = []int64{86_400e9, 3_600e9, 60e9, 1e9, 1e6, 1e3, 1}
	EpochUnits = []int64{1, 1e3, 1e6, 1e9, EpochDay}
)

// EpochDay is a day's nanoseconds, epoch's unit for days.
const EpochDay = 86_400e9

// dtCallOut takes the Call rather than the CallFn because truncate's answer depends
// on its INTERVAL and not only on its receiver. See ResolveCall's doc.
func dtCallOut(c *Call, in dtype.DataType) (dtype.DataType, error) {
	fn := c.Fn
	isDur := in.ID() == dtype.TypeDuration
	isInstant := in.ID() == dtype.TypeDate || in.ID() == dtype.TypeDatetime ||
		in.ID() == dtype.TypeTime

	switch fn {
	case FnDtIsoYear, FnDtCentury, FnDtMillennium:
		if in.ID() != dtype.TypeDate && in.ID() != dtype.TypeDatetime {
			return dtype.Null, uerr.New(uerr.KindType, "dt",
				"%s requires a Date or Datetime operand, got %s", fn, in)
		}
		return dtype.Int32, nil

	case FnDtBaseUtcOffset, FnDtDstOffset:
		if in.ID() != dtype.TypeDatetime || in.TimeZone() == "" {
			return dtype.Null, uerr.New(uerr.KindType, "dt",
				"%s requires a Datetime with a time zone, got %s", fn, in).
				Hint("give it one with .Dt().ReplaceTimeZone(tz)")
		}
		// Milliseconds whatever the value's unit, as Polars answers.
		return dtype.Duration(dtype.Milli), nil

	case FnDtWithTimeUnit:
		args, err := CallArgs(c)
		if err != nil {
			return dtype.Null, err
		}
		unit := dtype.TimeUnit(callLitInt(args, 0))
		if len(args) != 1 || unit > dtype.Nano {
			return dtype.Null, uerr.Internalf("expr: %s without its unit", fn)
		}
		switch in.ID() {
		case dtype.TypeDatetime:
			return dtype.Datetime(unit, in.TimeZone()), nil
		case dtype.TypeDuration:
			return dtype.Duration(unit), nil
		}
		return dtype.Null, uerr.New(uerr.KindType, "dt",
			"%s requires a Datetime or Duration operand, got %s", fn, in)

	case FnDtTotalMilliseconds, FnDtTotalMicroseconds, FnDtTotalNanoseconds, FnDtTotalFractional:
		if !isDur {
			return dtype.Null, uerr.New(uerr.KindType, "dt",
				"%s requires a Duration operand, got %s", fn, in).
				Hint("subtract two instants to get a Duration")
		}
		if fn != FnDtTotalFractional {
			return dtype.Int64, nil
		}
		args, err := CallArgs(c)
		if err != nil {
			return dtype.Null, err
		}
		if per := callLitInt(args, 0); len(args) != 1 || !slices.Contains(TotalUnits, per) {
			return dtype.Null, uerr.Internalf("expr: %s in %d nanoseconds", fn, per)
		}
		return dtype.Float64, nil

	case FnDtSecondFractional:
		if in.ID() != dtype.TypeDatetime && in.ID() != dtype.TypeTime {
			return dtype.Null, uerr.New(uerr.KindType, "dt",
				"%s requires a Datetime or Time operand, got %s", fn, in)
		}
		return dtype.Float64, nil

	case FnDtEpochIn:
		if in.ID() != dtype.TypeDate && in.ID() != dtype.TypeDatetime {
			return dtype.Null, uerr.New(uerr.KindType, "dt",
				"%s requires a Date or Datetime operand, got %s", fn, in)
		}
		args, err := CallArgs(c)
		if err != nil {
			return dtype.Null, err
		}
		per := callLitInt(args, 0)
		if len(args) != 1 || !slices.Contains(EpochUnits, per) {
			return dtype.Null, uerr.Internalf("expr: %s in %d nanoseconds", fn, per)
		}
		if per == EpochDay {
			return dtype.Int32, nil
		}
		return dtype.Int64, nil

	case FnDtTotalDays, FnDtTotalHours, FnDtTotalMinutes, FnDtTotalSeconds:
		if !isDur {
			return dtype.Null, uerr.New(uerr.KindType, "dt",
				"%s requires a Duration operand, got %s", fn, in).
				Hint("subtract two instants to get a Duration")
		}
		return dtype.Int64, nil

	case FnDtTruncate, FnDtRound:
		if !isInstant {
			return dtype.Null, uerr.New(uerr.KindType, "dt",
				"%s requires a Date, Time or Datetime operand, got %s", fn, in)
		}
		return truncateOut(c, in)

	case FnDtOffsetBy, FnDtMonthStart, FnDtMonthEnd, FnDtIsLeapYear, FnDtDaysInMonth:
		// A Time has no date to move or to read a year from.
		if in.ID() != dtype.TypeDate && in.ID() != dtype.TypeDatetime {
			e := uerr.New(uerr.KindType, "dt", "%s requires a Date or Datetime operand, got %s", fn, in)
			if fn == FnDtOffsetBy && (isDur || in.ID() == dtype.TypeTime) {
				e.Hint("add a Duration with Add instead")
			}
			return dtype.Null, e
		}
		switch fn {
		case FnDtIsLeapYear:
			return dtype.Bool, nil
		case FnDtDaysInMonth:
			// Int8, as Polars': a month has 28 to 31 days.
			return dtype.Int8, nil
		case FnDtOffsetBy:
			args, err := CallArgs(c)
			if err != nil {
				return dtype.Null, err
			}
			iv := dtype.IntervalOf(
				int32(callLitInt(args, 0)), int32(callLitInt(args, 1)), callLitInt(args, 2))
			if err := OffsetRefusal(iv, in); err != nil {
				return dtype.Null, err
			}
		}
		return in, nil

	case FnDtConvertTimeZone:
		return convertZoneOut(c, in)

	case FnDtReplaceTimeZone:
		return replaceZoneOut(c, in)

	case FnDtStrftime:
		if !isInstant {
			return dtype.Null, uerr.New(uerr.KindType, "dt",
				"%s requires a Date, Time or Datetime operand, got %s", fn, in)
		}
		return strftimeOut(c, in)

	case FnDtEpoch:
		if !isInstant {
			return dtype.Null, uerr.New(uerr.KindType, "dt",
				"%s requires a Date, Time or Datetime operand, got %s", fn, in)
		}
		return dtype.Int64, nil

	case FnDtDate:
		if in.ID() != dtype.TypeDate && in.ID() != dtype.TypeDatetime {
			return dtype.Null, uerr.New(uerr.KindType, "dt",
				"%s requires a Date or Datetime operand, got %s", fn, in)
		}
		return dtype.Date, nil

	case FnDtTime:
		switch in.ID() {
		case dtype.TypeTime:
			return in, nil
		case dtype.TypeDatetime:
			// The Datetime's own unit: a time of day in it is exact, and Polars'
			// nanoseconds would overflow nothing but gain nothing either.
			return dtype.Time(in.TimeUnit()), nil
		}
		e := uerr.New(uerr.KindType, "dt", "%s requires a Time or Datetime operand, got %s", fn, in)
		if in.ID() == dtype.TypeDate {
			e.Hint("a Date has no time of day")
		}
		return dtype.Null, e

	default:
		if !isInstant {
			e := uerr.New(uerr.KindType, "dt",
				"%s requires a Date, Time or Datetime operand, got %s", fn, in)
			if isDur {
				e.Hint("a Duration is a length, not a point in time; it has no " +
					"calendar components — use TotalDays, TotalHours and friends")
			} else {
				e.Hint("cast the column first, e.g. .Cast(ursus.Datetime(ursus.Micro, \"UTC\"))")
			}
			return dtype.Null, e
		}
		// Components are small non-negative numbers except the year, which can be
		// negative. Int32 throughout keeps one kernel shape.
		return dtype.Int32, nil
	}
}

// truncateOut types dt.truncate and dt.round, whose refusals depend on their
// argument rather than only on their receiver.
//
// # It asks the kernel's rule, it does not paraphrase it
//
// The interval is rebuilt with dtype.IntervalOf exactly as truncateTemporal does, and
// the refusal is GridRefusal, which the kernel asks too. A plan-time check that
// drifted from the kernel would be worse than no check at all, which is the Binding
// lesson one file over. Until step 145 the two were copies, held together by
// TestTruncateRefusalsAgree in internal/kernel, which still runs.
//
// A missing argument reads as zero, which the first refusal then catches — the same
// answer the kernel's argInt gives, and a truncate node with no interval is not a
// thing any builder produces.
func truncateOut(c *Call, in dtype.DataType) (dtype.DataType, error) {
	args, err := CallArgs(c)
	if err != nil {
		return dtype.Null, err
	}
	iv := dtype.IntervalOf(
		int32(callLitInt(args, 0)), int32(callLitInt(args, 1)), callLitInt(args, 2))

	if err := GridRefusal(c.Fn, iv, in); err != nil {
		return dtype.Null, err
	}
	return in, nil
}

// GridRefusal is dt.truncate's and dt.round's refusal of their interval, for a
// receiver of type in. truncateOut asks it at plan time and the kernel at run time,
// which kernel.DtCall's callers reach without resolving, so the two give one answer
// (TestTruncateRefusalsAgree).
func GridRefusal(fn CallFn, iv dtype.Interval, in dtype.DataType) error {
	verb, method := "truncate", "Truncate"
	if fn == FnDtRound {
		verb, method = "round", "Round"
	}
	// Reachable from the public API, which is why it is checked at plan time.
	// dtype.FromDuration does no validation, so `Truncate(time.Duration(0))` and
	// `Truncate(-time.Hour)` carry no error out of DtExpr.Truncate's iv.Err() check —
	// they planned, rendered in Explain, and failed at Collect.
	//
	// Err() is checked first, because an interval that failed to BUILD renders as
	// <invalid> and the message below would then say nothing useful. Ordering is the
	// whole of it: after, this would be unreachable, since a refused interval is
	// zero-valued and IsZero fires first.
	if err := iv.Err(); err != nil {
		return err
	}
	if iv.IsZero() || iv.Negative() {
		return uerr.New(uerr.KindValue, "dt",
			"%s needs a positive interval, got %s", verb, iv).
			Hint(`pass a time.Duration or an interval, e.g. .Dt().%s(time.Hour) `+
				`or .Dt().%s(ursus.Every("1mo"))`, method, method)
	}
	if in.ID() != dtype.TypeTime {
		return nil
	}
	// A Time rounded up from 23:59 to the hour is 24:00, which a Time cannot hold, and
	// wrapping it to 00:00 would put it before the value it rounds.
	if fn == FnDtRound {
		return uerr.New(uerr.KindType, "dt", "round is not defined for %s", in).
			Hint("a Time can round up to 24:00, which it cannot hold").
			Hint("use Truncate, or cast to Datetime first")
	}
	// A Time is a wall clock with no date. A sub-day interval on the same column is
	// fine, which is exactly why the receiver's type alone could never decide it.
	if iv.IsCalendar() {
		return uerr.New(uerr.KindType, "dt",
			"truncate by %s is not defined for %s", iv, in).
			Hint("a Time has no date, so it cannot be floored to a day or a month").
			Hint("use a sub-day interval, or cast to Datetime first")
	}
	return nil
}

// OffsetRefusal is dt.offset_by's refusal of its interval, for a Date or Datetime of
// type in, asked at plan time and by the kernel as GridRefusal is.
//
// The answer has the receiver's type, so an offset it cannot hold is refused rather
// than rounded: an hour is no number of days, and a nanosecond no number of
// microseconds.
func OffsetRefusal(iv dtype.Interval, in dtype.DataType) error {
	if err := iv.Err(); err != nil {
		return err
	}
	npt, ok := in.NanosPerTick()
	if !ok {
		return uerr.Internalf("dt: offset_by on %s", in)
	}
	if iv.Nanos()%npt == 0 {
		return nil
	}
	e := uerr.New(uerr.KindType, "dt", "offset_by %s is finer than %s holds", iv, in)
	if in.ID() == dtype.TypeDate {
		return e.Hint("a Date moves by whole days; cast it to a Datetime first")
	}
	return e.Hint("cast it to a finer unit first, e.g. .Cast(ursus.Datetime(ursus.Nano, zone))")
}

// convertZoneOut types dt.convert_time_zone: the same instants, labelled with another
// zone, so their wall clocks are read there. A naive Datetime is a wall clock in no
// zone and names no instant, so it is refused, as Polars refuses it.
func convertZoneOut(c *Call, in dtype.DataType) (dtype.DataType, error) {
	if in.ID() != dtype.TypeDatetime {
		return dtype.Null, uerr.New(uerr.KindType, "dt",
			"%s requires a Datetime operand, got %s", c.Fn, in)
	}
	if in.TimeZone() == "" {
		return dtype.Null, uerr.New(uerr.KindType, "dt",
			"%s requires a Datetime with a time zone, got %s", c.Fn, in).
			Hint("a naive Datetime names no instant to convert; if its wall clocks " +
				"are UTC, cast it first: .Cast(ursus.Datetime(unit, \"UTC\"))")
	}
	tz, ok := callLitString(c, 1)
	if !ok || tz == "" {
		return dtype.Null, uerr.New(uerr.KindValue, "dt",
			"%s needs a time zone, such as \"Europe/London\"", c.Fn)
	}
	if tz != "UTC" {
		if _, err := time.LoadLocation(tz); err != nil {
			return dtype.Null, uerr.New(uerr.KindValue, "dt",
				"the time zone %q is not known: %v", tz, err)
		}
	}
	return dtype.Datetime(in.TimeUnit(), tz), nil
}

// The policies replace_time_zone takes for a wall clock its new zone reads twice,
// in a fall-back fold, or never, in a spring-forward gap: Polars' own words, which
// the plan carries as its third and fourth args.
const (
	ZoneRaise    = "raise"    // refuse the query, naming the wall clock; the default
	ZoneEarliest = "earliest" // a fold's first reading, at the offset before it
	ZoneLatest   = "latest"   // a fold's second reading
	ZoneNull     = "null"     // a null
)

// Rolls are add_business_days' answers to a value not on a business day, in Polars'
// words (step 272).
const (
	RollRaise    = "raise"
	RollForward  = "forward"
	RollBackward = "backward"
)

// replaceZoneOut types replace_time_zone: a Datetime, naive or zoned, keeps its unit
// and takes the zone its first arg names, "" making it naive. The zone and both
// policies are checked here, so a query that names an unknown one is refused while
// it is planned.
func replaceZoneOut(c *Call, in dtype.DataType) (dtype.DataType, error) {
	if in.ID() != dtype.TypeDatetime {
		return dtype.Null, uerr.New(uerr.KindType, "dt",
			"%s requires a Datetime operand, got %s", c.Fn, in)
	}
	tz, ok := callLitString(c, 1)
	if !ok {
		return dtype.Null, uerr.New(uerr.KindValue, "dt",
			"%s needs a time zone, such as \"Europe/London\", or \"\" for none", c.Fn)
	}
	if tz != "" && tz != "UTC" {
		if _, err := time.LoadLocation(tz); err != nil {
			return dtype.Null, uerr.New(uerr.KindValue, "dt",
				"the time zone %q is not known: %v", tz, err)
		}
	}
	for i, allowed := range [][]string{
		{ZoneRaise, ZoneEarliest, ZoneLatest, ZoneNull}, // ambiguous
		{ZoneRaise, ZoneNull},                           // non_existent
	} {
		p, ok := callLitString(c, 2+i)
		if !ok || !slices.Contains(allowed, p) {
			return dtype.Null, uerr.New(uerr.KindValue, "dt",
				"%s's %s policy is %q; it is one of %q", c.Fn,
				[]string{"ambiguous", "non_existent"}[i], p, allowed)
		}
	}
	return dtype.Datetime(in.TimeUnit(), tz), nil
}

func callLitInt(args []any, i int) int64 {
	if i >= len(args) {
		return 0
	}
	v, _ := args[i].(int64)
	return v
}

// CallArgs extracts the literal arguments of a Call, requiring each to be a
// literal rather than a computed expression.
//
// Column-valued arguments are expressible in the IR and rejected here, so the
// error names the limitation rather than the evaluator failing on a shape it did
// not expect.
func CallArgs(c *Call) ([]any, error) {
	out := make([]any, 0, len(c.Args)-1)
	for _, a := range c.Args[1:] {
		l, ok := a.(*Lit)
		if !ok {
			return nil, uerr.New(uerr.KindUnsupported, "call",
				"%s requires constant arguments, got %s", c.Fn, a.String()).
				Hint("a column-valued argument is not implemented yet")
		}
		out = append(out, l.Value)
	}
	return out, nil
}

// structCallOut gives the output type of a `.struct` call.
//
// `field(name)` answers with the FIELD's type, which is why ResolveCall takes the
// whole Call: the answer is in the argument, not the receiver. Resolving it here
// rather than at execution time is what lets an unknown field name be refused while
// the query is still being planned, with the names that do exist listed.
func structCallOut(c *Call, in dtype.DataType) (dtype.DataType, error) {
	if in.ID() != dtype.TypeStruct {
		return dtype.Null, uerr.New(uerr.KindType, "struct",
			"%s requires a Struct operand, got %s", c.Fn, in).
			Hint("only a Struct column has fields")
	}
	switch c.Fn {
	case FnStructField:
		name, ok := callLitString(c, 1)
		if !ok {
			return dtype.Null, uerr.Internalf("expr: struct.field has no name argument")
		}
		for _, f := range in.Fields() {
			if f.Name == name {
				return f.Type, nil
			}
		}
		names := make([]string, 0, len(in.Fields()))
		for _, f := range in.Fields() {
			names = append(names, f.Name)
		}
		return dtype.Null, uerr.New(uerr.KindSchema, "struct",
			"no field %q in %s", name, in).
			Hint("the fields are: %s", strings.Join(names, ", "))

	case FnStructRenameFields:
		names, err := callFieldNames(c, "struct")
		if err != nil {
			return dtype.Null, err
		}
		old := in.Fields()
		if len(names) != len(old) {
			// Polars renames as many as it is given and drops the rest, and warns that
			// 2.0 will refuse it; this refuses it now.
			return dtype.Null, uerr.New(uerr.KindSchema, "struct",
				"rename_fields was given %d names for %d fields", len(names), len(old)).
				Hint("name every field, in order; select a field with Field to keep fewer")
		}
		fields := make([]dtype.Field, len(old))
		for i, f := range old {
			fields[i] = dtype.Field{Name: names[i], Type: f.Type, Nullable: true}
		}
		return dtype.Struct(fields...), nil

	case FnStructJSONEncode:
		if bad, ok := jsonUnencodable(in); ok {
			return dtype.Null, uerr.New(uerr.KindType, "struct",
				"%s cannot write a %s, which JSON has no value for", c.Fn, bad).
				Hint("cast it to String first, or drop the field")
		}
		return dtype.String, nil
	}
	return dtype.Null, uerr.Internalf("expr: unknown struct call %d", c.Fn)
}

// jsonUnencodable finds a type within t that json_encode cannot write: a Binary,
// whose bytes JSON has no value for. Polars panics on one.
func jsonUnencodable(t dtype.DataType) (dtype.DataType, bool) {
	switch t.ID() {
	case dtype.TypeBinary:
		return t, true
	case dtype.TypeList:
		return jsonUnencodable(t.Inner())
	case dtype.TypeStruct:
		for _, f := range t.Fields() {
			if bad, ok := jsonUnencodable(f.Type); ok {
				return bad, true
			}
		}
	}
	return dtype.Null, false
}

// callFieldNames reads the constant strings after a call's receiver: the names a
// struct's fields are given. Two of one name are refused, as a struct cannot hold
// them.
func callFieldNames(c *Call, op string) ([]string, error) {
	names := make([]string, 0, len(c.Args)-1)
	seen := make(map[string]bool, len(c.Args)-1)
	for i := 1; i < len(c.Args); i++ {
		name, ok := callLitString(c, i)
		if !ok {
			return nil, uerr.Internalf("expr: %s argument %d is not a name", c.Fn, i)
		}
		if seen[name] {
			return nil, uerr.New(uerr.KindSchema, op,
				"%s names two fields %q", c.Fn, name).
				Hint("give each field a name of its own")
		}
		seen[name] = true
		names = append(names, name)
	}
	return names, nil
}

// callLitString reads a constant string argument.
func callLitString(c *Call, i int) (string, bool) {
	if i < 0 || i >= len(c.Args) {
		return "", false
	}
	l, ok := c.Args[i].(*Lit)
	if !ok {
		return "", false
	}
	s, ok := l.Value.(string)
	return s, ok
}

// listCallOut gives the output type of a `.list` call.
//
// The namespace has two halves and the output type is what tells them apart. One
// reduces a list to a SCALAR, giving a fixed type or the ELEMENT type; the other
// reshapes it and gives back the RECEIVER'S type, unchanged. A caller can read
// which half a function is in from this switch alone, which is the point of
// keeping the rule in one place.
func listCallOut(c *Call, in dtype.DataType) (dtype.DataType, error) {
	fn := c.Fn
	if in.ID() != dtype.TypeList {
		return dtype.Null, uerr.New(uerr.KindType, "list",
			"%s requires a List operand, got %s", fn, in).
			Hint("only a List column has elements to reduce")
	}
	elem := in.Inner()

	switch fn {
	case FnListLen:
		// Uint32, matching str.len_bytes and str.len_chars. A length is never
		// negative and never needs 64 bits.
		return dtype.Uint32, nil

	case FnListContains:
		if _, err := MembershipType(c, elem); err != nil {
			return dtype.Null, err
		}
		return dtype.Bool, nil

	case FnListJoin:
		if elem.ID() != dtype.TypeString && elem.ID() != dtype.TypeNull {
			return dtype.Null, uerr.New(uerr.KindType, "list",
				"%s requires a List of String, got %s", fn, in).
				Hint("cast the elements first, e.g. .Cast(ursus.List(ursus.String))")
		}
		if _, ok := callLitString(c, 1); !ok {
			return dtype.Null, uerr.Internalf("expr: %s without a separator", fn)
		}
		return dtype.String, nil

	case FnListGet, FnListMin, FnListMax:
		// The element type unchanged: picking one element out, or the smallest or
		// largest of them, cannot change what type it is.
		return elem, nil

	case FnListMean:
		// Delegated for the reason FnListSum gives below, and it was not: this arm
		// answered Float64 for every element type without reading elem at all.
		// listReduce has always derived the real type from ResolveAggBinding, so the
		// two disagreed wherever the aggregate's own rule is not Float64 — a
		// Duration mean is a Duration since step 62, and a Float32 mean has been a
		// Float32 for as long as the function has existed. Both are the dangerous
		// direction: Eval SUCCEEDS and hands back a column of the wrong type.
		//
		// Int64 is why it survived. ResolveAggBinding(AggMean, Int64) is Float64,
		// so every integer element agreed, and an integer element is what every
		// fixture had.
		b, err := ResolveAggBinding(AggMean, elem)
		if err != nil {
			return dtype.Null, err
		}
		return b.Out, nil

	case FnListAny, FnListAll:
		if elem.ID() != dtype.TypeBool && !elem.IsNull() {
			return dtype.Null, uerr.New(uerr.KindType, "list", "%s takes a List of Booleans, got %s", fn, in)
		}
		return dtype.Bool, nil

	case FnListItem:
		// allow_empty, which the kernel reads and cannot run without.
		if args, err := CallArgs(c); err != nil || len(args) != 1 {
			return dtype.Null, uerr.Internalf("expr: %s without allow_empty", fn)
		} else if _, ok := args[0].(bool); !ok {
			return dtype.Null, uerr.Internalf("expr: %s with allow_empty %v", fn, args[0])
		}
		return elem, nil

	case FnListSample:
		return in, nil

	case FnListMode, FnListUniqueCounts, FnListValueCounts:
		// A Struct or a List cannot be a key here, so its mode is refused, where
		// Polars counts them.
		if !elem.IsHashable() && !elem.IsNull() {
			return dtype.Null, uerr.New(uerr.KindType, "mode", "%s counts values, and %s cannot be a key", fn, elem)
		}
		switch fn {
		case FnListUniqueCounts:
			return dtype.List(dtype.Uint32), nil
		case FnListValueCounts:
			args, err := CallArgs(c)
			if err != nil || len(args) != 4 {
				return dtype.Null, uerr.Internalf("expr: %s without its arguments", fn)
			}
			label, _ := args[2].(string)
			value, _ := args[3].(string)
			normalize, _ := args[1].(bool)
			return dtype.List(ValueCountsType(dtype.Field{Name: value, Type: elem}, label, normalize)), nil
		}
		return in, nil

	case FnListArgSort, FnListArgTrue, FnListArgUnique:
		switch {
		case fn == FnListArgSort && !elem.IsOrdered() && !elem.IsNull():
			return dtype.Null, uerr.New(uerr.KindType, "arg_sort", "arg_sort orders values, and %s has no order", elem)
		case fn == FnListArgTrue && elem.ID() != dtype.TypeBool && !elem.IsNull():
			return dtype.Null, uerr.New(uerr.KindType, "arg_true", "arg_true takes Booleans, got %s", elem)
		case fn == FnListArgUnique && !elem.IsHashable() && !elem.IsNull():
			return dtype.Null, uerr.New(uerr.KindType, "arg_unique", "arg_unique compares values, and %s cannot be", elem)
		}
		return dtype.List(dtype.Uint32), nil

	case FnListExplode:
		if elem.ID() != dtype.TypeList {
			return dtype.Null, uerr.New(uerr.KindType, "explode",
				"explode requires a List column, got %s", elem).
				Hint("a column that is not a List has nothing to explode")
		}
		return elem, nil

	case FnListDropNans:
		if !elem.IsFloat() {
			return dtype.Null, uerr.New(uerr.KindType, "list",
				"%s requires a List of floats, got %s", fn, in).
				Hint("only a float is ever NaN")
		}
		return in, nil

	case FnListMedian, FnListStd, FnListVar, FnListQuantile:
		// The aggregate's rule, as for Sum and Mean.
		op := map[CallFn]AggOp{FnListMedian: AggMedian, FnListStd: AggStd, FnListVar: AggVar,
			FnListQuantile: AggQuantile}[fn]
		b, err := ResolveAggBinding(op, elem)
		if err != nil {
			return dtype.Null, err
		}
		return b.Out, nil

	case FnListReverse, FnListHead, FnListTail, FnListSlice,
		FnListSort, FnListUnique, FnListDropNulls, FnListGatherEvery:
		// The receiver's type, unchanged. Reshaping a list cannot change what it is
		// a list OF — and returning `in` rather than rebuilding List(elem) means a
		// future parameterised list type carries through without this needing to
		// know about it.
		return in, nil

	case FnListNUnique, FnListArgMin, FnListArgMax:
		// The aggregate's rule, as for Sum below: a count is a Uint64, a position
		// too, and arg_min refuses an element type with no ordering, naming itself.
		op := AggNUnique
		switch fn {
		case FnListArgMin:
			op = AggArgMin
		case FnListArgMax:
			op = AggArgMax
		}
		b, err := ResolveAggBinding(op, elem)
		if err != nil {
			return dtype.Null, err
		}
		return b.Out, nil

	case FnListToStruct:
		names, err := callFieldNames(c, "list")
		if err != nil {
			return dtype.Null, err
		}
		if len(names) == 0 {
			return dtype.Null, uerr.New(uerr.KindSchema, "list",
				"to_struct needs at least one field name").
				Hint("name the fields the first elements become, e.g. ToStruct(\"x\", \"y\")")
		}
		fields := make([]dtype.Field, len(names))
		for i, name := range names {
			fields[i] = dtype.Field{Name: name, Type: elem, Nullable: true}
		}
		return dtype.Struct(fields...), nil

	case FnListSum:
		// Delegated to the AGGREGATE's rule rather than restated, so a per-row sum
		// and a column-wide sum agree about their type. That rule widens an integer
		// to Int128, which step 2 chose deliberately: a per-row sum that overflowed
		// where the column-wide one does not would be the worse surprise.
		b, err := ResolveAggBinding(AggSum, elem)
		if err != nil {
			return dtype.Null, err
		}
		return b.Out, nil

	default:
		return dtype.Null, uerr.Internalf("expr: unknown list call %d", fn)
	}
}

// withFieldsOut types struct.with_fields (step 171): the first operand's fields, each
// replaced in place by an operand of its name, then the operands of new names, in
// order. Two operands of one name would set one field twice, so they are refused.
func withFieldsOut(ops []dtype.Field) (dtype.DataType, error) {
	in := ops[0].Type
	if in.ID() != dtype.TypeStruct {
		return dtype.Null, uerr.New(uerr.KindType, "struct.with_fields",
			"with_fields requires a Struct, got %s", in).
			Hint("only a Struct column has fields")
	}
	set := make(map[string]dtype.DataType, len(ops)-1)
	var added []dtype.Field
	for _, f := range ops[1:] {
		if _, dup := set[f.Name]; dup {
			return dtype.Null, uerr.New(uerr.KindSchema, "struct.with_fields",
				"two new fields are named %q", f.Name).
				Hint("alias one of them, e.g. .Alias(\"%s_2\")", f.Name)
		}
		set[f.Name] = f.Type
	}
	fields := make([]dtype.Field, 0, len(in.Fields())+len(ops)-1)
	for _, f := range in.Fields() {
		if t, ok := set[f.Name]; ok {
			f.Type, f.Nullable = t, true
			delete(set, f.Name)
		}
		fields = append(fields, f)
	}
	for _, f := range ops[1:] {
		if _, ok := set[f.Name]; ok {
			added = append(added, dtype.Field{Name: f.Name, Type: f.Type, Nullable: true})
		}
	}
	return dtype.Struct(append(fields, added...)...), nil
}
