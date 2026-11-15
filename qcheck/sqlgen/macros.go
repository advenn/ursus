package sqlgen

// Macros are the DuckDB macros the translations call, each a CREATE OR REPLACE
// MACRO statement: a connection runs them once, in order, before any query. A
// macro's parameters are substituted where it reads them, so one that reads an
// argument many times binds it once through list_transform, to keep a query's
// expression linear in its AST. Parameter and lambda names start u_, which no
// generated column does.
//
// Integer arithmetic is in HUGEINT, which holds any product of two BIGINTs, and
// wrapped to the width (T-INT-WRAP). Integer // and % are computed so that they
// floor whether DuckDB's own // and % truncate or floor (T-FLOOR): the remainder
// ((a % b) + b) % b takes the divisor's sign either way, and a minus it is a
// multiple of b, so the quotient is exact either way.
var Macros = []string{
	// u_wrap is v reduced to the signed integers of m = 2^bits values, h = m / 2.
	`CREATE OR REPLACE MACRO u_wrap(u_v, u_m, u_h) AS ((CAST(u_v AS HUGEINT) + u_h) % u_m + u_m) % u_m - u_h`,
	`CREATE OR REPLACE MACRO u_w8(u_v) AS CAST(u_wrap(u_v, CAST('256' AS HUGEINT), CAST('128' AS HUGEINT)) AS TINYINT)`,
	`CREATE OR REPLACE MACRO u_w16(u_v) AS CAST(u_wrap(u_v, CAST('65536' AS HUGEINT), CAST('32768' AS HUGEINT)) AS SMALLINT)`,
	`CREATE OR REPLACE MACRO u_w32(u_v) AS CAST(u_wrap(u_v, CAST('4294967296' AS HUGEINT), CAST('2147483648' AS HUGEINT)) AS INTEGER)`,
	`CREATE OR REPLACE MACRO u_w64(u_v) AS CAST(u_wrap(u_v, CAST('18446744073709551616' AS HUGEINT), CAST('9223372036854775808' AS HUGEINT)) AS BIGINT)`,

	// u_imod and u_idiv floor, a zero divisor a null (T-FLOOR).
	`CREATE OR REPLACE MACRO u_imod(u_a, u_b) AS ((CAST(u_a AS HUGEINT) % NULLIF(CAST(u_b AS HUGEINT), 0)) + CAST(u_b AS HUGEINT)) % NULLIF(CAST(u_b AS HUGEINT), 0)`,
	`CREATE OR REPLACE MACRO u_idiv(u_a, u_b) AS (CAST(u_a AS HUGEINT) - u_imod(u_a, u_b)) // NULLIF(CAST(u_b AS HUGEINT), 0)`,
	// u_fdiv floors a by a positive b, for the calendar's arithmetic.
	`CREATE OR REPLACE MACRO u_fdiv(u_a, u_b) AS (u_a - ((u_a % u_b) + u_b) % u_b) // u_b`,

	// u_fmod is ursus's float remainder (T-FLOAT-MOD): fmod, then the divisor added
	// where the remainder's sign differs from it, a zero remainder signed as it.
	`CREATE OR REPLACE MACRO u_fmod(u_a, u_b) AS CASE
		WHEN fmod(u_a, u_b) = 0 THEN (CASE WHEN u_b < 0 THEN CAST('-0.0' AS DOUBLE) ELSE CAST('0.0' AS DOUBLE) END)
		WHEN (fmod(u_a, u_b) < 0) <> (u_b < 0) THEN fmod(u_a, u_b) + u_b
		ELSE fmod(u_a, u_b) END`,

	// u_fkey is a float as a key of the total order (T-KEY-ZERO): -0 as +0, and
	// every NaN as one NaN, whatever its payload. Both zeros are named, so it holds
	// whether DuckDB's = tells them apart or not.
	`CREATE OR REPLACE MACRO u_fkey(u_x) AS CASE WHEN isnan(u_x) THEN CAST('nan' AS DOUBLE)
		WHEN u_x = CAST('0.0' AS DOUBLE) OR u_x = CAST('-0.0' AS DOUBLE) THEN CAST('0.0' AS DOUBLE) ELSE u_x END`,

	// The IEEE comparisons (T-IEEE-CMP): a null either side null, NaN either side
	// false, true for <>; otherwise the keys compare, where DuckDB's order is IEEE's.
	`CREATE OR REPLACE MACRO u_feq(u_a, u_b) AS CASE WHEN u_a IS NULL OR u_b IS NULL THEN NULL WHEN isnan(u_a) OR isnan(u_b) THEN false ELSE u_fkey(u_a) = u_fkey(u_b) END`,
	`CREATE OR REPLACE MACRO u_fne(u_a, u_b) AS CASE WHEN u_a IS NULL OR u_b IS NULL THEN NULL WHEN isnan(u_a) OR isnan(u_b) THEN true ELSE u_fkey(u_a) <> u_fkey(u_b) END`,
	`CREATE OR REPLACE MACRO u_flt(u_a, u_b) AS CASE WHEN u_a IS NULL OR u_b IS NULL THEN NULL WHEN isnan(u_a) OR isnan(u_b) THEN false ELSE u_fkey(u_a) < u_fkey(u_b) END`,
	`CREATE OR REPLACE MACRO u_fle(u_a, u_b) AS CASE WHEN u_a IS NULL OR u_b IS NULL THEN NULL WHEN isnan(u_a) OR isnan(u_b) THEN false ELSE u_fkey(u_a) <= u_fkey(u_b) END`,
	`CREATE OR REPLACE MACRO u_fgt(u_a, u_b) AS CASE WHEN u_a IS NULL OR u_b IS NULL THEN NULL WHEN isnan(u_a) OR isnan(u_b) THEN false ELSE u_fkey(u_a) > u_fkey(u_b) END`,
	`CREATE OR REPLACE MACRO u_fge(u_a, u_b) AS CASE WHEN u_a IS NULL OR u_b IS NULL THEN NULL WHEN isnan(u_a) OR isnan(u_b) THEN false ELSE u_fkey(u_a) >= u_fkey(u_b) END`,
	// u_fless is the total order's <, NaN above everything, -0 equal to +0
	// (T-EXTREMUM).
	`CREATE OR REPLACE MACRO u_fless(u_a, u_b) AS (NOT isnan(u_a)) AND (isnan(u_b) OR u_fkey(u_a) < u_fkey(u_b))`,

	// The float aggregates over a list in the rows' order (T-FLOAT-AGG, T-EXTREMUM):
	// a sum from +0, as ursus's accumulator starts; a group of no value null.
	`CREATE OR REPLACE MACRO u_fsum(u_l) AS CASE WHEN len(u_l) = 0 THEN NULL
		ELSE list_reduce(u_l, lambda u_a, u_b: u_a + u_b, CAST('0.0' AS DOUBLE)) END`,
	`CREATE OR REPLACE MACRO u_fmean(u_l) AS CASE WHEN len(u_l) = 0 THEN NULL
		ELSE list_reduce(u_l, lambda u_a, u_b: u_a + u_b, CAST('0.0' AS DOUBLE)) / CAST(len(u_l) AS DOUBLE) END`,
	`CREATE OR REPLACE MACRO u_fmin(u_l) AS CASE WHEN len(u_l) = 0 THEN NULL
		ELSE list_reduce(u_l, lambda u_a, u_b: CASE WHEN u_fless(u_b, u_a) THEN u_b ELSE u_a END) END`,
	`CREATE OR REPLACE MACRO u_fmax(u_l) AS CASE WHEN len(u_l) = 0 THEN NULL
		ELSE list_reduce(u_l, lambda u_a, u_b: CASE WHEN u_fless(u_a, u_b) THEN u_b ELSE u_a END) END`,

	// u_f2i is a float as a BIGINT where it is an integer in [lo, hi), else null
	// (T-CAST-EXACT). NaN fails x < hi, DuckDB's NaN being above everything.
	`CREATE OR REPLACE MACRO u_f2i(u_x, u_lo, u_hi) AS CASE WHEN u_x >= u_lo AND u_x < u_hi AND u_x = trunc(u_x) THEN CAST(u_x AS BIGINT) END`,

	// u_pbool is strconv.ParseBool (T-PARSE-BOOL).
	`CREATE OR REPLACE MACRO u_pbool(u_s) AS CASE WHEN u_s IN ('1', 't', 'T', 'TRUE', 'true', 'True') THEN true
		WHEN u_s IN ('0', 'f', 'F', 'FALSE', 'false', 'False') THEN false END`,

	// u_pfloat is dtype.ParseFloat (T-PARSE-FLOAT): inf, infinity and nan in any
	// ASCII case after an optional sign; else the decimal grammar, whose overflow,
	// an infinity, is null.
	`CREATE OR REPLACE MACRO u_pfloat(u_s0) AS list_transform([u_s0], lambda u_s: CASE
		WHEN regexp_full_match(u_s, '(?i)\+?(inf|infinity)') THEN CAST('inf' AS DOUBLE)
		WHEN regexp_full_match(u_s, '(?i)-(inf|infinity)') THEN CAST('-inf' AS DOUBLE)
		WHEN regexp_full_match(u_s, '(?i)[+-]?nan') THEN CAST('nan' AS DOUBLE)
		WHEN regexp_full_match(u_s, '[+-]?([0-9]+\.?[0-9]*|\.[0-9]+)([eE][+-]?[0-9]+)?')
			THEN (CASE WHEN isinf(TRY_CAST(u_s AS DOUBLE)) THEN NULL ELSE TRY_CAST(u_s AS DOUBLE) END)
		END)[1]`,

	// u_pdate is time.Parse("2006-01-02") (T-PARSE-DATE): four, two and two ASCII
	// digits, a month of 1 to 12, a day the month has; its days since 1970-01-01 by
	// Hinnant's days_from_civil.
	`CREATE OR REPLACE MACRO u_mdays(u_y, u_m) AS CASE
		WHEN u_m = 2 THEN (CASE WHEN (u_y % 4 = 0 AND u_y % 100 <> 0) OR u_y % 400 = 0 THEN 29 ELSE 28 END)
		WHEN u_m IN (4, 6, 9, 11) THEN 30 ELSE 31 END`,
	`CREATE OR REPLACE MACRO u_civ(u_y2, u_m, u_d) AS
		u_fdiv(u_y2, 400) * 146097
		+ (u_y2 - u_fdiv(u_y2, 400) * 400) * 365
		+ (u_y2 - u_fdiv(u_y2, 400) * 400) // 4
		- (u_y2 - u_fdiv(u_y2, 400) * 400) // 100
		+ (153 * (u_m + (CASE WHEN u_m > 2 THEN -3 ELSE 9 END)) + 2) // 5 + u_d - 1 - 719468`,
	`CREATE OR REPLACE MACRO u_ymd(u_y, u_m, u_d) AS CASE
		WHEN u_m >= 1 AND u_m <= 12 AND u_d >= 1 AND u_d <= u_mdays(u_y, u_m)
		THEN CAST(u_civ(u_y - (CASE WHEN u_m <= 2 THEN 1 ELSE 0 END), u_m, u_d) AS INTEGER) END`,
	`CREATE OR REPLACE MACRO u_pdate(u_s0) AS list_transform([u_s0], lambda u_s: CASE
		WHEN regexp_full_match(u_s, '[0-9]{4}-[0-9]{2}-[0-9]{2}')
		THEN u_ymd(CAST(substr(u_s, 1, 4) AS BIGINT), CAST(substr(u_s, 6, 2) AS BIGINT), CAST(substr(u_s, 9, 2) AS BIGINT))
		END)[1]`,

	// u_dtext is dtype.FormatTemporal of a Date (T-DATE-TEXT): Hinnant's
	// civil_from_days, then Go's 2006-01-02, whose year has four digits at least and
	// a '-' before a negative one.
	`CREATE OR REPLACE MACRO u_dfmt(u_y, u_m, u_d) AS
		(CASE WHEN u_y < 0 THEN '-' || printf('%04d', -u_y) ELSE printf('%04d', u_y) END)
		|| '-' || printf('%02d', u_m) || '-' || printf('%02d', u_d)`,
	`CREATE OR REPLACE MACRO u_dt4(u_y, u_doy, u_mp) AS u_dfmt(
		u_y + (CASE WHEN u_mp >= 10 THEN 1 ELSE 0 END),
		u_mp + (CASE WHEN u_mp < 10 THEN 3 ELSE -9 END),
		u_doy - (153 * u_mp + 2) // 5 + 1)`,
	`CREATE OR REPLACE MACRO u_dt3(u_yoe, u_doe, u_era) AS list_transform(
		[{'doy': u_doe - (365 * u_yoe + u_yoe // 4 - u_yoe // 100), 'y': u_yoe + u_era * 400}],
		lambda u_r: u_dt4(u_r.y, u_r.doy, (5 * u_r.doy + 2) // 153))[1]`,
	`CREATE OR REPLACE MACRO u_dt2(u_doe, u_era) AS list_transform(
		[{'yoe': (u_doe - u_doe // 1460 + u_doe // 36524 - u_doe // 146096) // 365, 'doe': u_doe, 'era': u_era}],
		lambda u_r: u_dt3(u_r.yoe, u_r.doe, u_r.era))[1]`,
	`CREATE OR REPLACE MACRO u_dt1(u_z) AS list_transform(
		[{'doe': u_z - u_fdiv(u_z, 146097) * 146097, 'era': u_fdiv(u_z, 146097)}],
		lambda u_r: u_dt2(u_r.doe, u_r.era))[1]`,
	`CREATE OR REPLACE MACRO u_dtext(u_d0) AS CASE WHEN u_d0 IS NULL THEN NULL
		ELSE list_transform([CAST(u_d0 AS BIGINT) + 719468], lambda u_z: u_dt1(u_z))[1] END`,

	// u_ftext is kernel.FloatText (T-FLOAT-TEXT). DuckDB writes a double's shortest
	// digits; they are taken apart, into the digits without zeros at either end and
	// the exponent of the first, and written as FloatText writes them: plain from
	// 1e-5 to below 1e16, a whole number with ".0", else d.ddde±x with no padding.
	`CREATE OR REPLACE MACRO u_fplain(u_d, u_x) AS CASE
		WHEN u_x >= 0 THEN (CASE WHEN length(u_d) > u_x + 1
			THEN substr(u_d, 1, CAST(u_x + 1 AS BIGINT)) || '.' || substr(u_d, CAST(u_x + 2 AS BIGINT))
			ELSE rpad(u_d, CAST(u_x + 1 AS INTEGER), '0') || '.0' END)
		ELSE '0.' || repeat('0', CAST(-u_x - 1 AS BIGINT)) || u_d END`,
	`CREATE OR REPLACE MACRO u_fsci(u_d, u_x) AS substr(u_d, 1, 1)
		|| (CASE WHEN length(u_d) > 1 THEN '.' || substr(u_d, 2) ELSE '' END)
		|| 'e' || (CASE WHEN u_x < 0 THEN '-' ELSE '+' END) || CAST(abs(u_x) AS VARCHAR)`,
	`CREATE OR REPLACE MACRO u_fparts(u_all, u_point, u_plain) AS list_transform(
		[{'d': rtrim(ltrim(u_all, '0'), '0'), 'x': u_point - (length(u_all) - length(ltrim(u_all, '0'))) - 1, 'p': u_plain}],
		lambda u_q: CASE WHEN u_q.p THEN u_fplain(u_q.d, u_q.x) ELSE u_fsci(u_q.d, u_q.x) END)[1]`,
	`CREATE OR REPLACE MACRO u_fdigits(u_s0, u_plain0) AS list_transform([{'s': u_s0, 'p': u_plain0}],
		lambda u_r: u_fparts(
			replace(regexp_extract(u_r.s, '^([0-9.]+)', 1), '.', ''),
			length(split_part(regexp_extract(u_r.s, '^([0-9.]+)', 1), '.', 1))
				+ (CASE WHEN contains(u_r.s, 'e') THEN CAST(regexp_extract(u_r.s, 'e([-+]?[0-9]+)$', 1) AS INTEGER) ELSE 0 END),
			u_r.p))[1]`,
	`CREATE OR REPLACE MACRO u_ftext(u_x) AS CASE
		WHEN u_x IS NULL THEN NULL
		WHEN isnan(u_x) THEN 'NaN'
		WHEN u_x = CAST('inf' AS DOUBLE) THEN 'inf'
		WHEN u_x = CAST('-inf' AS DOUBLE) THEN '-inf'
		WHEN u_x = CAST('0.0' AS DOUBLE) OR u_x = CAST('-0.0' AS DOUBLE) THEN (CASE WHEN signbit(u_x) THEN '-0.0' ELSE '0.0' END)
		ELSE (CASE WHEN u_x < 0 THEN '-' ELSE '' END) || u_fdigits(CAST(abs(u_x) AS VARCHAR),
			abs(u_x) >= CAST('1e-5' AS DOUBLE) AND abs(u_x) < CAST('1e16' AS DOUBLE)) END`,
}
