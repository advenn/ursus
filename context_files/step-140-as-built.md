# Step 140 — as built

**The scope of 0.5, written as [`v0.5-scope.md`](./v0.5-scope.md).** No Go changed.

## 1. How it was made

**The maintainer chose the focus,** after v0.4.0 was tagged:

- speed;
- the features users reach for first;
- object stores still out.

**Three surveys,** each read-only:

1. **The open items:** what steps 117 to 139, `audit.md` and the changelog's
   limitations leave open.
2. **The evidence behind the widest gaps:** step 127's report against the records'
   stated causes, and whether each cause was profiled or read from the code.
3. **A sizing against the code:** each candidate, with where it would live, what it
   reuses, its size, its risk and its dependencies.

**Three of the sizing's claims were checked in the code before use:**

- `plan.Resolve` is a `TransformUp`, so a shared subtree is planned twice;
- `parallelsink.go` declines joins, and says why;
- the CSV reader converts by column, after a serial scan.

## 2. What it found that shapes the order

**None of the widest gaps was profiled after steps 123 to 126,** so 0.5 starts with a
profile:

- q15 and q2: causes read from the code;
- q17: no recorded cause;
- `gb1` and `gb4` over CSV: inferred from timings.

**The CSV reader's serial scan** is a candidate the 0.4 scope did not have: two
columns read convert two ways at most. It is item 5, if the profile confirms it.

## 3. The correction

**The changelog's v0.4.0 "Memory" limitation ended at step 131's figures.** Steps 133
to 138 had moved them:

- `gb10` over Parquet peaks at 2.72 GB and allocates 16.0 GB;
- over CSV, 2.75 GB and 14.9 GB.

It is corrected on master, and says it was corrected after the tag.
