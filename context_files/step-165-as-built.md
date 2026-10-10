# Step 165 — as built

**Item 5 of `v0.6-scope.md`: q19's implied predicates.**

## 1. Evidence first

**q19's optimized plan** on step 164's build:

- lineitem is joined to part;
- the filter's two lineitem conditions go below the join;
- the third conjunct, an Or of three buckets, stays above it. Each bucket is an And
  of part conditions (brand, containers, size) and one lineitem condition
  (quantity), so the Or names both sides.

So the join built the table from every one of part's 200,000 rows, and gathered
`p_brand` and `p_container` for about 430,000 joined rows, to keep a few hundred.
Pushdown moves single-side conjuncts only (`plan/rule_predicate.go`,
`pushThroughJoin`).

## 2. What changed

**A conjunct that stays above a join now implies a predicate on each side**
(`impliedFor`):

- when every arm of its Or has conjuncts over that side alone, the side gets the Or
  of each arm's such conjuncts, Anded;
- that goes below the join, and the original conjunct stays above it;
- one arm is the same reasoning with nothing to Or: an And whose conjuncts name both
  sides sends its one-side conjuncts down too.

**Why it is implied.** A row passes the filter only where the predicate is true. Then
some arm is true, so every conjunct of that arm is true, so the derived Or is true.
A side row the derived predicate is not true for therefore joins to no row the filter
keeps, and filtering it out first changes nothing. This holds under Kleene logic as
under two-valued. Leaving conjuncts out of an arm only weakens the implication.

**What it leaves alone:**

- a conjunct that may fail, judged as rewritten for the side, in its own types,
  since a merged key may be cast: below the join it would run on rows the join drops
  (`fallible`);
- an arm with nothing left, which means nothing is implied;
- a side the join null-extends (`joinPushLegal`), as for any pushed conjunct.

**q19's plan after:**

- lineitem gains the Or of its three quantity ranges, which also reaches the Parquet
  scan's predicate;
- part gains the Or of the three buckets' brand, containers and size.

## 3. Measured

Step 164's runner against this one, alternated, three rounds of five iterations at
SF=1. CPU time per iteration is from rusage, warm-up included:

| query | CPU per iteration | median wall |
| --- | --- | --- |
| q19 | 1,620 → 1,426 ms (−12%) | 415 → 409 ms |
| q12 | 939 → 894 ms | 312 → 297 ms |
| q7 | 2,516 → 2,600 ms | 516 → 619 ms |

q12's and q7's plans do not change. q7's port is already a union of its two
directions, with no Or. Their moves are the session's spread: q7's runs reached from
2,335 to 2,636 ms on both runners.

**Why q19's wall barely moved.** A profile after shows the join's probe at 0.7% of
the query: the join is gone as a cost. The Parquet reader is 61%, and of it the
decoding of two string columns, `l_shipinstruct` and `l_shipmode`, to filter them.
The reader is Tier 2's dictionary decode, and Tier 3's predicates evaluated inside the
reader.

## 4. Tests

- **`TestImpliedPredicatesKeepTheAnswer`:** six predicates, each against join pushdown
  turned off. Inner, left, right and full joins, three seeds each, nullable keys and
  values:
  - q19's shape, an Or of Ands over both sides;
  - an arm with nothing on the left;
  - one And over both sides;
  - an Or that nulls decide, through `IsNull`;
  - one over the merged key;
  - a fallible arm: a cast that fails on the left rows that never join. Inner and right
    joins only, since a left or full join keeps those rows above the join, where the
    cast fails with or without pushdown.
- **`TestAnOrImpliesWhatEachSideMustHold`:** where the conditions land, counted in the
  plan:
  - an inner join: above the join and once more on each side;
  - a left join: nothing into its null-extended right side;
  - an arm with nothing on the left: nothing into the left;
  - a fallible arm: the cast stays above.

## 5. Teeth

| tooth | result |
| --- | --- |
| a fallible conjunct implied | **bites:** both new tests, `TestACalendarRefusalIsNotPushedPastAJoin` |
| an arm with nothing on the side skipped | **bites:** both new tests |
| implied into a side the join null-extends | **bites:** `TestAnOrImpliesWhatEachSideMustHold`, `TestJoinPredicatePushdownSoundness` and more |
| the implied predicate in place of the original | **bites:** both new tests |
| an And's conjuncts read as arms | **bites:** `TestAnOrImpliesWhatEachSideMustHold` |

**Two read silent at first, and were the teeth's fault:**

- The fallible check ran twice, before the rewrite and after, and the tooth removed
  only the first. The code now keeps only the second, the rewritten form's, which is
  the stricter. The tooth removing it bites.
- The null-extension tooth opened only the left side's check, which a left join
  allows anyway. Opening both, it bites.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. **PDS-H at SF=0.1:** all 22 answers match DuckDB's.
