# Step 271 — as built

**S of `v0.6.6-scope.md`, second half: encodings, integers in a base, options, and
JSON.** These are Polars' `encode` and `decode`, `to_integer(base, dtype, strict)`,
`replace`'s `n`, `concat_str(ignore_nulls=True)`, `json_path_match` and
`json_decode(dtype)`. This finishes S. On the way, a list's eval over an aggregate in
a `Select`, `Col("a").Implode().List().Eval(...)`, was found failing as an internal
error, and is fixed.

## 1. What changed

**Encodings** (`internal/kernel/strcodec.go`):

- **`Encode(Hex | Base64)`:** each value's bytes, lowercase hex or padded base64;
- **`Decode(enc, strict)`:** to Binary. Hex reads either case. Base64 must be padded,
  as Polars has it: `aGk` does not decode. Text that does not decode is null, or with
  strict refused naming its row.

**`ToIntegerWith(base, dt, strict)`:**

- **The base** is a constant or an expression, read a row at a time. It is a
  horizontal call, `FnStrParseInt` or `FnStrParseUint`, over the string, the base and
  strict.
- **Reading:** a sign, then digits of the base, by `strconv.ParseInt` or `ParseUint`.
  No space, `0x` or underscore reads, as in Polars: `" 7"`, `"0x10"` and `"1_000"` are
  null.
- **The type:** an unsigned type parses as UInt64 and a signed one as Int64, then is
  cast to `dt`. A value outside `dt` is null without strict and refused with it.
- **Strict** refuses with up to three values that did not read.
- The existing `ToInteger()`, a lossy cast to Int64, stays.

**`ReplaceN(pattern, value, literal, n)`:**

- **A literal** replaces the first `n` matches, or all where `n` is negative, by
  `strings.Replace`.
- **A regex** replaces the first `n` matches through `FindAllStringSubmatchIndex`,
  each expanded as `Replace` expands `$1`. Polars refuses a regex with `n > 1`;
  ursus takes it. `n == 0` replaces nothing.
- The count is a fourth argument, before the literal flag, so `Replace` and
  `ReplaceAll` are unchanged.

**`ConcatStrIgnoreNulls(sep, exprs...)`:**

- **A horizontal call** with the separator as its last operand, so the first names
  the column, as `ConcatStr`'s first does.
- **The answer:** each row joins its values that are not null. A row of nulls alone
  is `""`, not null, as Polars makes it.
- **The typing** refuses a call without a String separator last. The evaluator
  contract's matrix drives it with one operand, which reached the kernel before this
  check and panicked there.

**JSON** (`internal/kernel/strjson.go`, `internal/expr/jsonpath.go`):

- **A small ordered parser:** `jsonValue` is a JSON value with its object's members
  in order and each number's literal kept. It is read token by token with
  `encoding/json`'s decoder, and trailing text is not JSON.
- **Repeated keys:** Polars' two functions use two parsers, and they differ.
  `json_path_match` keeps the last value, in the first key's place; `json_decode`
  keeps the first. Both are matched.
- **The path** is parsed when the plan is typed, so a path ursus cannot follow is
  refused before a row is read. The subset is `$`, then `.key`, `['key']`, `[n]` (from
  the end where negative) and `..key`. `..key` takes the key at this level, or else
  its first place below, depth first in order, as Polars picks it.
- **`json_path_match`'s answer** is text:
  - a string, unquoted;
  - any other value, compact JSON, escaping only a quote, a backslash and control
    characters (`\u0001`, lowercase);
  - JSON null, no match and text that is not JSON: null.
- **`json_path_match`'s numbers** are reprinted, as Polars reprints them:
  - an integer that fits Int64 or UInt64 as written;
  - any other number, `-0` among them, as a float. It is laid out as Rust's ryu lays
    it out, but with an exponent's `+`: `1000.0`, `0.00001`, `1e+16`, `1.5e-7`,
    `-0.0`. `shortFloat` does it from Go's shortest digits.
- **`json_decode(dtype)`** reads to the type given; ursus does not infer one:
  - Booleans, integers, floats, Strings, Lists and Structs of them;
  - every Struct field is made nullable in the output type, as a JSON object may leave
    any out;
  - a missing field is null, and an extra one is skipped.
- **`json_decode`'s conversions**, each read off Polars' answers:
  - a Boolean as a number is 1 or 0;
  - a float as an integer is truncated;
  - a number past its type's range is null, but a Float32 is infinite;
  - a number or a Boolean as a String is its text. An integer stays as written, but
    `-0` is `0`, and a float is written without an exponent;
  - a scalar as a List is a List of itself, so `[1, [2]]` as `List(List(Int64))` is
    `[[1], [2]]`.
- **Refused, naming the shape and the type:**
  - an object as a List;
  - anything but an object as a Struct;
  - a string as a number;
  - a number as a Boolean;
  - an array or object as a String;
  - text that is not JSON, naming its row.

**A list's eval over an aggregate:**

- **The failure:** `Select(Col("a").Implode().List().Eval(...))` failed with
  `extractAggs: unhandled node *expr.ListMap`.
- **The fix:** the aggregation's rewrite now descends a `ListMap`'s list and outer
  values, which makes each aggregate below a temporary, and rebuilds it. The body is
  not a child, since an aggregate there is a list's own.
- **A fold over aggregates** needed nothing: `Expand` writes a fold out into its
  arithmetic before this rewrite sees it.

**CallFns:**

- `FnStrEncode`, `FnStrDecode`, `FnStrJsonPathMatch` and `FnStrJsonDecode` join the
  800 block;
- `FnStrParseInt`, `FnStrParseUint` and `FnConcatStrIgnoreNulls` join the horizontal
  block;
- the names test expects 171.

**The contract:** its matrix drives `encode`, `decode`, `json_path_match`,
`to_integer`, `concat_str` with `ignore_nulls`, and `replace` with `n`. `json_decode`
is driven without its type, since every receiver there is text that is not JSON.

**Public:**

- `Str().Encode`, `Decode` and the `Encoding` constants `Hex` and `Base64`;
- `ToIntegerWith`, `ReplaceN`, `JsonPathMatch` and `JsonDecode`;
- `ConcatStrIgnoreNulls`.

## 2. Tests and teeth

Every answer is Polars 1.44.1's, except a regex replaced more than once.

- **`TestEncodings`:** encode both ways; decode of either case, of foreign digits,
  odd lengths and unpadded base64; the Binary type; a strict refusal naming its row;
  an unknown encoding.
- **`TestToIntegerWith`:**
  - base 16;
  - a base a row;
  - Int8 and UInt8 past their range;
  - a sign but not an underscore;
  - the type asked for;
  - strict refusals naming the values and the type;
  - a Float64 target refused.
- **`TestReplaceNAndConcatSkipping`:** `n` of 2, 0 and −1; a regex with `$1`;
  `concat_str` past nulls, a row of nulls `""`, and a literal operand.
- **`TestJsonPathMatch`:**
  - eleven paths over Polars' document;
  - twelve numbers reprinted;
  - a repeated key, matched and printed;
  - escapes;
  - the deep scan's order;
  - a path not at `$` refused.
- **`TestJsonDecode`:**
  - a Struct with missing, extra and null rows;
  - a List;
  - the conversions to Int64, Int8, UInt8, UInt64, Float32 and String;
  - scalars as Lists;
  - a repeated key;
  - six refusals.
- **`TestListEvalAndFoldOverAggregates`:** `[2, 4, 6]` and 36.

| tooth | result |
| --- | --- |
| hex is base64 | **bites** |
| decode is never strict | **bites** |
| to_integer is never strict | **bites** |
| a strict dtype casts lossily | **bites** |
| replace's n is ignored | **bites** |
| a regex's n replaces all | **bites** |
| a null is joined | **bites** |
| the separator is first | **bites** |
| numbers as written | **bites** |
| no plus in an exponent | **bites** |
| 1e16 in full | **bites** |
| a repeated key's first value | **bites** |
| decode keeps the last value | **bites** |
| a deep key below first | **bites** |
| [-1] is one short | **bites** |
| control characters raw | **bites** |
| trailing text is JSON | **bites** |
| a scalar is no List | **bites** |
| a Boolean is no integer | **bites** |
| -0.5 is no UInt64 | **bites** |
| -0 stays -0 as text | **bites** |
| text numbers in exponent form | **bites** |
| decoded fields keep their nullability | **bites** |
| a list's eval over an aggregate | **bites** |
| concat_str's separator unchecked (the contract) | **bites** |

A tooth that dropped the fold arm from the aggregation's rewrite was silent. That is
how it was found that no fold reaches the rewrite, and the arm was removed.

**Not matched:** Polars' JSON parser rounds some 17-digit numbers wrongly.
`12345678901234567.0` prints as `1.2345678901234566e+16`, where the nearest double
is …568. ursus prints the correctly rounded …568.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean; the 22 PDS-H answers match DuckDB.
