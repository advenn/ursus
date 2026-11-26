# Step 547 — the arrow-go findings withheld from the repository

**Docs only.** The arrow-go findings go to the Apache Arrow security team privately,
in the report the maintainer sends on 2026-11-26. The maintainer asked that the
repository then not carry them ("if the details are sent on email, remove the
findings from our repo"). They chose the smaller scope: the catalog and the
summaries.

## Removed

- **Step 538's table of all 32 findings,** each with its status on v18.9.0, and its
  record's sentences on which are unfixed, F18's remaining hang among them.
- **The pointers to the private report's folder:** step 533's paragraph on adding C3
  to it, and step 538's teeth row naming its inputs.
- **The CHANGELOG's v0.8.1 line** naming what v18.9.0 leaves unfixed. It now says
  every guard against crafted input stays.
- **The index lines** stating arrow-go's unfixed status: step 538's row, and step
  533's "C3 of the arrow-go report".

Each place now points at the private report.

## Kept, and why

- **The guards themselves stay, and must:** their code, comments, tests and the fuzz
  corpora's crafted inputs. Removing them would drop the regression tests that keep
  ursus safe on crafted files.
- **The records of steps 510, 511, 517, 519–521, 531 and 533 keep their account of
  each fix,** arrow-go's part in it included. Trimming them was the larger scope, not
  chosen.
- **Git history and released tags keep every earlier text.** No history is
  rewritten. The email to security@apache.org says parts were public from
  2026-11-23.
