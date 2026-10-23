---
description: >-
  Working with text in ursus, the Go dataframe library: the Str namespace for case,
  trimming, lengths, pattern matching with regular expressions, extraction,
  replacement, splitting, padding and slicing, parsing numbers and dates, and joining.
---

# Strings

Text methods sit behind `Str()`. A method returns an expression, so a second one
needs `Str()` again: `Col("name").Str().StripChars(" ").Str().ToLower()`. The
examples read a small frame of people:

--8<-- "func.people.md"

## Cleaning

--8<-- "Example_strClean.md"

- `StripChars(chars)` trims those characters from both ends, and `StripCharsStart` and
  `StripCharsEnd` from one; `StripPrefix` and `StripSuffix` remove a whole affix;
- `ToUpper`, `ToLower` and `ToTitlecase`;
- `LenChars` counts characters and `LenBytes` bytes of UTF-8: "Zoë Smith " is ten
  characters and eleven bytes.

## Matching

`Contains(pattern, literal)` tests a regular expression, or a literal string when
`literal` is true. `StartsWith` and `EndsWith` take a literal, and `CountMatches`
counts a pattern's matches:

--8<-- "Example_strMatch.md"

Patterns are Go's regular expressions, RE2, as Polars' are Rust's `regex`: the same
syntax for nearly everything, and no backreferences or look-around in either.
`ContainsAny(patterns, ...)` and `ReplaceMany` match many literal strings at once, in
one pass.

## Extracting

`Extract(pattern, group)` takes a capture group of the first match, null where nothing
matches; `ExtractAll(pattern)` takes every match, a list a row; `ExtractGroups` every
named group, as a struct.

--8<-- "Example_strExtract.md"

## Replacing

`Replace` replaces the first match and `ReplaceAll` every one; `ReplaceN` the first n.
With a regular expression, the replacement may refer to groups: `$1`, or `${name}`.

--8<-- "Example_strReplace.md"

## Splitting

`Split(by)` gives a list a row. `SplitExact(by, n, inclusive)` gives a struct of n + 1
fields, null where there are fewer parts, which `Unnest` turns into columns:

--8<-- "Example_strSplit.md"

`SplitN(by, n)` splits at most n − 1 times, the rest in the last part.

## Padding and slicing

--8<-- "Example_strSlice.md"

`ZFill`, `PadStart` and `PadEnd` pad to a width; `Slice(offset, length)`, `Head` and
`Tail` count characters, not bytes, and `Reverse` reverses them.

## Parsing

`Strptime(type, format, strict)` parses dates and times with a strftime format;
`ToInteger` parses integers:

--8<-- "Example_strParse.md"

`ToInteger`, `ToDate` and `ToDatetime` turn what does not parse into a null. Polars'
`to_integer` and `to_date` fail the query by default instead. For that, ask for
strictness: `Strptime(..., true)`, or `ToIntegerWith(base, type, true)`.

*New in 0.6.7:* `ToTime(format, strict)` parses a time of day, by a format or, with
none, each value as it comes, and `ToDecimal(scale)` a number into a `Decimal(38,
scale)`, rounded half away from zero, as Polars' `to_time` and `to_decimal`.

`JsonDecode(type)` parses JSON into a value of a type, a struct or a list, and
`Encode` and `Decode` convert to and from hex or base64.

## Joining

`ConcatStr(sep, exprs...)` joins columns row by row, and `Format` fills a template;
`Str().Join(sep)` in an aggregation joins a group's values into one string:

--8<-- "Example_strCombine.md"

A null in `ConcatStr`'s inputs makes the row null, as in Polars; `ConcatStrIgnoreNulls`
skips nulls instead.
