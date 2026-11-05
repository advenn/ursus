package expr

import (
	"strconv"
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
	FnStructSelectFields        // the named fields, in that order, the struct's validity kept (step 299)
	FnStructDropFields          // the fields not named, Polars' struct.drop (step 323)
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

	// list.append(a, b, mode): each row's two lists one after the other, their
	// elements met as Append's mode says (step 282), which Append in an aggregation
	// lowers to.
	FnListAppend

	// hash_rows(operands..., seed): each row's hash over its operands, the row's
	// group-by key hashed as hash hashes one value's, the seed an Int64 literal last
	// (step 302).
	FnHashRows

	// arr.shift(n) and arr.dot(other), n and other a value a row (step 342): an
	// Array's list.shift, the Array kept, and the sum of two Arrays' products.
	FnArrShift
	FnArrDot
	// concat_arr(exprs...): each row's operands in one Array, an Array operand's
	// elements and another's value, of one element type (step 343).
	FnConcatArr
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
	FnListRle // list.rle, each list's runs, a Struct each (step 281)
	// list.hist, the bin count or -1, include_category, include_breakpoint and the
	// edges (step 286), which Hist in an aggregation lowers to.
	FnListHist
	FnListToArray // list.to_array, the width: an Array of it (step 341)
	FnListReshape // list.reshape, the shape: each list's values in Arrays of it, as a list (step 343)
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
	FnDtCastTimeUnit // a Datetime's or a Duration's values in another unit, Polars' cast_time_unit (step 325)
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
	FnToPhysical  // the values as stored, Polars' to_physical (step 324)
	FnReinterpret // an integer's bits at the other signedness; Args[1] signed
	fnGenMoreEnd
)

// The maths family, continued (step 283): round_sig_figs, the digits; truncate, the
// decimals, round's kernel toward zero. round's mode is its own second argument.
// IsMath covers it.
const (
	FnMathRoundSigFigs CallFn = iota + 1300
	FnMathTruncate
	fnMathMoreEnd
)

const (
	FnBinEncode CallFn = iota + 1100
	FnBinDecode
	FnBinSize
	FnBinReinterpret
	fnBinEnd
)

// The array family (step 341): to_list, an Array's rows as a List's, which holds them
// as an Array does. A block of its own at 1400. Step 342's reverse and sort, a List's
// kept an Array, sort with list.sort's arguments; and to_struct, the fields' names,
// field_0 on unless given.
const (
	FnArrToList CallFn = iota + 1400
	FnArrReverse
	FnArrSort
	FnArrToStruct
	fnArrEnd
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
func (f CallFn) IsMath() bool {
	return f >= FnMathRound && f < fnMathEnd || f >= FnMathRoundSigFigs && f < fnMathMoreEnd
}
func (f CallFn) IsList() bool {
	return f >= FnListLen && f < fnListEnd || f >= FnListMedian && f < fnListMoreEnd
}
func (f CallFn) IsStruct() bool { return f >= FnStructField && f < fnStructEnd }
func (f CallFn) IsBinary() bool { return f >= FnBinEncode && f < fnBinEnd }
func (f CallFn) IsArr() bool    { return f >= FnArrToList && f < fnArrEnd }
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
	FnDtWithTimeUnit: "dt.with_time_unit", FnDtCastTimeUnit: "dt.cast_time_unit", FnDtTotalMilliseconds: "dt.total_milliseconds",
	FnDtTotalMicroseconds: "dt.total_microseconds", FnDtTotalNanoseconds: "dt.total_nanoseconds",
	FnDtTotalFractional: "dt.total_fractional", FnDtSecondFractional: "dt.second_fractional",
	FnDtEpochIn:       "dt.epoch_in",
	FnDtIsBusinessDay: "dt.is_business_day", FnDtAddBusinessDays: "dt.add_business_days",
	FnDtReplace:        "dt.replace",
	FnCatGetCategories: "cat.get_categories", FnCatPhysical: "cat.physical",
	FnBinContains: "bin.contains", FnBinStartsWith: "bin.starts_with", FnBinEndsWith: "bin.ends_with",
	FnBinGet: "bin.get", FnBinSlice: "bin.slice", FnBinEncode: "bin.encode", FnBinDecode: "bin.decode",
	FnBinSize: "bin.size", FnBinReinterpret: "bin.reinterpret",
	FnOrderCmp: "order_cmp", FnHash: "hash", FnOrderSign: "order_sign", FnToPhysical: "to_physical", FnReinterpret: "reinterpret", FnHashRows: "hash_rows",
	FnListAppend: "list.append",

	FnIsIn: "is_in",

	FnMathRound: "round", FnMathRoundSigFigs: "round_sig_figs", FnMathTruncate: "truncate",
	FnMathAsFloat: "as_float", FnMathDiffWiden: "diff_widen",
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
	FnStructJSONEncode: "struct.json_encode", FnStructSelectFields: "struct.select_fields",
	FnStructDropFields: "struct.drop",
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
	FnListHist: "list.hist", FnListToArray: "list.to_array", FnListReshape: "list.reshape",
	FnConcatArr: "concat_arr",
	FnArrToList: "arr.to_list", FnArrReverse: "arr.reverse", FnArrSort: "arr.sort",
	FnArrToStruct: "arr.to_struct", FnArrShift: "arr.shift", FnArrDot: "arr.dot",
	FnListRle: "list.rle",
}

func (f CallFn) String() string {
	if s, ok := callNames[f]; ok {
		return s
	}
	return "call(" + strconv.Itoa(int(f)) + ")"
}
