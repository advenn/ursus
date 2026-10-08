# Step 135 — as built

**The memory test's spilling join is bounded on an exact reading, not a sampled
one.** Step 134's gate failed once on it: 128 MB against a bound of 96, in a run where
it usually reads about 20. No engine code changed, and no benchmark was run.

## 1. What was flaky, and why

`TestQueriesHoldWhatTheBudgetCounts`' third case joins two million rows under a 16 MB
budget, so the join spills, and bounded the **sampled** live heap at five times the
budget and 16 MB more.

**A sample counts whatever a collection marks live, including what is allocated while
it marks.** Here, a filter on eight workers feeds the join, copying its rows. Alone
the reading wandered from 19.9 to 35.5 MB.

In a gate, every package's tests compete for the CPU. The collector falls behind the
filter and the reading grows with it: once, 128 MB. Step 129 recorded the same noise
in a `Collect`, 145 MB in one run and 111 in the next.

## 2. What changed

**The case reads the live heap exactly** where the other cases do: at each column a
concatenation copies, after a full collection, through `kernel.ConcatColumnDone`.

In a spilled join, that is each bucket's sub-join concatenating its build side, its
largest moment in the replay. It read **14.3 MB in every run:**

- ten alone, five in the scalar build and five with SIMD;
- six while the root, kernel, physical and plan tests ran beside it, as in a gate.

**Two bounds:**

- **Exact:** at most the budget and 8 MB more, 24 MB. A sub-join holds its bucket
  within the budget and one concatenated column more.
- **Sampled:** only what the noise cannot reach. Spilled, it must hold less than the
  same join counted when it held everything, read from the first case: about 179 MB.

## 3. What it catches

The case is for one kind of fault: a spilling join that does not bound its memory.

| fault | sampled, step 134's bound of 96 MB | exact, this bound of 24 MB |
| --- | --- | --- |
| build rows kept after being written to their spill file | **bites,** 105.7 MB | **bites,** 105.7 MB |
| every bucket's sub-join kept through the replay | **bites,** 103.0 MB | **bites,** 103.0 MB |

Both were caught before, by 7 to 10%, with a healthy run sometimes reaching 128 MB.
Now a fault reads four times the bound, and a healthy run 0.6 of it, every time.

**Each fault was modelled with a goroutine that blocks forever, then reads the value.**
A goroutine that only took it as an unused parameter kept nothing alive: Go's
liveness analysis treats a parameter that is never read as dead. The first version of
both teeth did exactly that and was silent, the trap step 129 recorded.

**Silent, and why that is right:**

- **The resident table kept through the replay:** with it kept, the replay still
  read under the 24 MB bound, against 14.3 without it, so it held under 10 MB. At a
  16 MB budget nearly the whole build side goes to disk; a larger budget would leave
  more resident, and this case does not test one.
- **A freeze releasing its account after concatenating** does hold the build side
  twice: the unspilled join's freeze read 260.7 MB, against 180.7 without it. But
  the account then counts the parts and the table together, so the peak it reports
  rose to 276.5 MB with the heap. Held twice and counted is what these tests allow:
  they are for memory the budget does not see.
- **A freeze concatenating with `Concat`:** each bucket's build side is a few
  megabytes, and twice that stays inside the bound. The first case, the unspilled
  join, catches it, as it did.

**Gate:** test-all 115 ok, race 23 ok, levels, vet ×3 and the bench engine tests
clean. No benchmark, by request.
