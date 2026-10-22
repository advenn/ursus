---
description: >-
  Joins in ursus, the Go dataframe library: inner, left, right, full, semi, anti and
  cross joins, keys of different names, suffixes, validation, null keys, as-of joins,
  joins on a condition, and filtering by another frame.
---

# Joins

`Join(other, options...)` matches this frame's rows with `other`'s by equal keys. The
examples join five orders with four customers: customer 2, Ben, has no order, and
order 13's customer, 5, is not a customer.

--8<-- "func.customers.md"

--8<-- "func.orders.md"

and `show` collects a query and prints it:

--8<-- "func.show.md"

## The kinds

`JoinOn` names the keys, and `JoinHow` the kind; inner is the default.

=== "Inner"

    Pairs of rows whose keys match. Order 13 and Ben are left out.

    --8<-- "Example_joinInner.md"

=== "Left"

    Every row of the left frame, with nulls where the right has no match.

    --8<-- "Example_joinLeft.md"

=== "Right"

    Every row of the right frame, with nulls where the left has no match.

    --8<-- "Example_joinRight.md"

=== "Full"

    Every row of both, each key column kept, since either side's can be null.

    --8<-- "Example_joinFull.md"

=== "Semi"

    The left rows that have a match, once each, and only the left's columns: the
    customers who ordered.

    --8<-- "Example_joinSemi.md"

=== "Anti"

    The left rows that have no match: the customers who never ordered.

    --8<-- "Example_joinAnti.md"

=== "Cross"

    Every pair of rows, with no keys.

    --8<-- "Example_joinCross.md"

**Row order.** An inner join's order is not promised, as in Polars: ursus hashes the
smaller input and the rows follow the other. `JoinMaintainOrder` keeps an order, as
Polars' `maintain_order` does, for every kind: `JoinOrderLeft` the left frame's, each
row's matches in the right frame's order, as the inner example does, and
`JoinOrderRight` the right frame's. *Changed in 0.6.7:* it took a Boolean, and kept
an inner join's left order alone. Left, semi, anti and full joins keep the left
frame's order without it while they fit in memory; `JoinMaintainOrder` keeps it when
they spill too.

!!! note "A right join's order and columns are not Polars'"

    A right join here keeps the left frame's row order, with the right rows that
    matched nothing after them, and keeps the key where the left frame has it. Polars
    gives a right join's rows in the right frame's order, and its key among the right
    frame's columns. `JoinMaintainOrder(ursus.JoinOrderRight)` gives Polars' rows;
    `Select` the columns in the order you want them.

## Keys

### Merged keys

A key named the same on both sides appears once, for every kind but full, which keeps
both, `customer_id` and `customer_id_right`. `JoinCoalesce(true)` merges them, the
left's where a row has a left side and the right's where it does not:

--8<-- "Example_joinCoalesce.md"

### Keys of different names

`JoinLeftOn` and `JoinRightOn` name the keys on each side, paired by position. Both
key columns are kept, `customer_id` and `id`: merging them would delete a column you
named. Polars drops the right one; `JoinCoalesce(true)` does here too.

--8<-- "Example_joinKeysByName.md"

Several keys match on all of them: `JoinOn(Col("a"), Col("b"))`.

### Columns of the same name

A right column whose name the left frame already has takes a suffix, `_right` unless
`JoinSuffix` says otherwise:

--8<-- "Example_joinSuffix.md"

### Null keys

A null key matches nothing, as in SQL, unless `JoinNullsEqual(true)` makes nulls
equal:

--8<-- "Example_joinNulls.md"

## Validation

A join whose keys repeat where you expected them unique multiplies rows silently.
`JoinValidate` checks the keys' cardinality and fails the query when the data breaks
it:

--8<-- "Example_joinValidate.md"

The checks are `ValidateOneToOne`, `ValidateOneToMany`, `ValidateManyToOne` and
`ValidateManyToMany`, the first side named being the left: `ValidateManyToOne` on
`orders.Join(customers)` asks for each customer once.

## As-of joins

`JoinAsOf` matches each left row with the nearest right row before it, rather than an
equal one: each trade with the quote in force when it happened. `AsOfBy` matches
within equal keys first, and `AsOfTolerance` limits how far back a match may be:

--8<-- "Example_joinAsOf.md"

The 09:00:09 ACME trade's last quote is five seconds old, past the three-second
tolerance, so its bid is null.

- It is a left join: every left row comes back, its right columns null when nothing
  is near enough.
- `AsOfStrategyOpt(AsOfForward)` takes the nearest row after, and `AsOfNearest` the
  nearest either way.
- Both frames must be sorted by the as-of key, and ursus checks that they are rather
  than trusting it: an unsorted side fails with an error.

## Joins on a condition

`JoinWhere(other, predicates...)` pairs the rows that satisfy every predicate, such as
an interval: each order with the promotion running on its date. An equality among the
predicates becomes a hash join's key; without one, every pair is tested.

--8<-- "Example_joinWhere.md"

## Filtering by another frame

`WhereExists(other, predicates...)` keeps this frame's rows that have at least one row
in `other` satisfying every predicate, once each, with only this frame's columns:
SQL's `EXISTS`. `WhereNotExists` keeps those that have none. The predicates name
`other`'s columns as a join would, with the suffix where a name collides:

--8<-- "Example_whereExists.md"

A semi or anti join asks the same question when the condition is equal keys alone.

## Under a memory limit

Equi-joins spill both sides to disk under a memory budget, and give the answer they
give in memory; under a limit a join's row order is promised only with
`JoinMaintainOrder`. A backward as-of join
streams both sides. See [Memory and spilling](../concepts/memory.md).
