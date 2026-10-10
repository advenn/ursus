# Step 169 — as built

**The other of step 160's found items: a `LIKE` with two wildcards ran as a
backtracking regexp.**

## 1. Evidence first

PDS-H q13 filters orders by `o_comment NOT LIKE '%special%requests%'`. The port writes
it as `Str().Contains("special.*requests", false)`, a regexp, since the wildcards
between the words are not a substring test. In step 160's q13 profile, Go's
`regexp.(*Regexp).backtrack` was 0.74 s of 4.01 s, about 18% of the query, on that one
pattern.

## 2. What changed

**A pattern of literals joined by `.*` is matched by substring search**
(`kernel/strfn.go`):

- `literalChain` reads such a pattern's literals. It reads no other pattern: none with
  another metacharacter, a flag, an anchor, a lazy `.*?`, a single `.`, or a newline in
  a literal.
- `containsChain` finds each literal after the end of the one before, by
  `strings.Index`. The leftmost occurrence of each leaves the most of the value for the
  rest, so it finds a match wherever there is one.
- **The regexp still decides a value that holds a newline,** since `.` matches anything
  but a newline.

The chain is read from the compiled regexp's pattern, once per batch, so
`CompilePattern` and `StrCall` keep their signatures.

## 3. Measured

Step 168's runner against this one, alternated, three rounds of five iterations at
SF=1. CPU time per iteration is from rusage, warm-up included:

| query | CPU per iteration | median wall |
| --- | --- | --- |
| q13 | 1,272 → 1,081 ms (−15%) | 425 → 358 ms (−16%) |

## 4. Tests

- **`TestALiteralChainIsOnlyLiteralsAndDotStars`:** sixteen patterns, which read as a
  chain and which do not, and the literals of those that do.
- **`TestContainsAChainAnswersAsTheRegexp`:** eight chain patterns over 4,000 random
  values built from pieces that include `"\n"`, `"é"` and the invalid byte `"\xff"`.
  `Contains` answers as the regexp's `MatchString` on every one.

## 5. Teeth

| tooth | result |
| --- | --- |
| a literal found overlapping the one before | **bites:** `TestContainsAChainAnswersAsTheRegexp` |
| a value with a newline matched by search | **bites:** the same |
| a metacharacter read as a literal | **bites:** `TestALiteralChainIsOnlyLiteralsAndDotStars`, `TestStrRegexAndLiteralDiffer` |
| a newline in the pattern read as a literal | **bites:** `TestALiteralChainIsOnlyLiteralsAndDotStars` |

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
