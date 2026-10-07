# Step 118 — as built

**What the audit of 0.4's join work found, fixed.** A read-only review of steps 104,
109 and 110, made while step 117's benchmark ran, traced four defects. Each was then
reproduced by a test before its fix.

## 1. The defects

| | what happened | why |
| --- | --- | --- |
| an as-of key | A Uint64 key joined to an Int64 one, or an Int64 one to a Decimal, planned and then failed at `Collect` with *"Int128 has no order key"*, as ursus's bug. | Step 109 checked the LEFT key's type. The search runs on the type both keys meet at, which is Int128 or a Decimal. |
| nearest on a float key | A NaN in the right input won every nearest match it was a candidate for. An exact match on +Inf among duplicates went forward to the first of them, where every other key type goes backward to the last. | NaN sorts last, so it is the forward candidate of any larger key, and `x <= NaN` is false. Inf − Inf is NaN too. |
| a sorted input | `Sort(ts).Join(users)` swapped when `users` was the larger, so its output followed `users`, and `.Head(n)` returned other rows. A join under an as-of join or `MergeSorted` was swapped too, and they refused its now unsorted output: whether a query ran depended on the tables' sizes. | The rule only knew that an inner join promises no order. |
| a merged zero key | A join on a float key could return +0.0 for the left input's -0.0. | The merged key comes from the side the table is built from, and the hash equates the two zeros. |

## 2. The fixes

- **The as-of key** is checked again after `Layout`, on `KeyTypes[0]`. The refusal
  names both keys and the type they meet at, and suggests a cast; the test runs that
  cast.
- **`floatDistance`** is 0 for equal order keys and +Inf when either is NaN.
- **`build_side` walks the plan itself,** bottom-up as `TransformUp` does, telling each
  node whether its parent depends on its order:
  - as-of joins and `MergeSorted` need theirs;
  - order-keeping nodes pass the need down;
  - a join passes it to its left input.

  It leaves alone a join that owes its order, and one whose left input is a `Sort`
  reached through order-keeping nodes.
- **No join on a float key is swapped.**

**Gave up:** a query that sorts a small table and then joins it to a large one now
hashes the large one. Since this is the caller asking for order, that is the price.

## 3. Tests and teeth

`joinaudit_test.go`:

- `TestAsOfKeysAreCheckedWhereTheyMeet`;
- `TestNearestAsOfOnAFloatKey`, with NaN, duplicate infinities, and the integer path
  as the control;
- `TestBuildSideKeepsWhatTheCallerOrdered`: the unordered join still swaps; a sorted
  left, an as-of join above and `MergeSorted` above do not;
- `TestBuildSideKeepsAMergedZeroKeysSign`.

| tooth | result |
| --- | --- |
| the as-of key checked on the left type only | **bites:** both shapes |
| the float distance as before | **bites:** NaN and the infinities |
| no order need from above | **bites:** as-of and `MergeSorted` |
| a sorted left ignored | **bites** |
| float keys swap | **bites** |

**Gate:** with steps 119–122; see step 122.
