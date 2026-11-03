# Step 438 — as built

**`TestInternedTypesAreCollected`'s heap bound leaves room for the table's own
map.** A flake found by `release-0.7`'s CI at #29's merge commit, `d84a103`, on
2026-11-02, before v0.7.2 was tagged.

## 1. What was wrong

Step 427's test drops ten rounds of 40,000 types and bounds two things:
- the intern table's entries, which must return to the two held;
- the heap's growth after the first round, which must stay under 2 MB.

On CI's simd=256 leg the heap grew 2,102,728 bytes. Every other run, local and on CI,
had stayed under.

The growth is the table's map. Go never shrinks a map. A later round's entries can
outnumber the first round's while they wait for their cleanups, which run when they
run, and the buckets that took them stay allocated. The entry count was back to the
two held. Nothing leaked.

## 2. The change

The heap's bound is 16 MB.
- Held for good, the types grew it 118.8 MB.
- With no cleanup, only their keys held, 50.8 MB, run again here.

The entry count stays exact, and it is the test's sharper check.

## 3. Teeth

| mutation | |
| --- | --- |
| no cleanup | **bites** on both checks: 383,021 entries, and 50.8 MB against 16 |

Five runs pass in a row locally.

## 4. The tag

v0.7.2's tag moves from #30's merge commit to this pull request's, so the commit
tagged is one whose CI on `release-0.7` passed.
