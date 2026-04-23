# Invariants: ballot tally

## Contract

The ballot tally module maintains a running count of ballots added.

## Invariants

**I1:** Adding a ballot cannot decrease the tally. The tally is monotonically non-decreasing.
