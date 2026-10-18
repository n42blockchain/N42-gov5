# Post-finite double physical-reopen audit

This explicit Python runner audits the retained root after a finite preload
fully acknowledged and drained, but its stopped-production stable snapshot
failed. It does not retry the admission session or create a TPS result.

Inputs pin the finite candidate, all 64 ACK journals, the independent failure
acceptance, successful drain and frozen producer phase evidence. These remain
finite evidence; they are not converted to a failed twenty-second window.
The first fresh nonproducing snapshot must have exactly the canonical count
and each sender nonce/balance increment implied by the known receipts. Both
header and loaded QMDB roots must match. Its head cannot regress below the
last drain observation. A second complete close/reopen must return the same
head, roots, all accounts and vector digest in a different empty ingress epoch.

Only ordinary strict owner startup, seven Miner=false configuration copies and
the existing root-bearing recover command run. Initialization, source scanning,
plan arming, production, transaction dispatch, marker repair and replay are absent.
A startup that cannot reconcile actual applied and canonical state fails closed.

The outer wrapper must hold one original BOX claim across full original and
protected-copy pre/post observations. The original's current SHA is a newly
observed value after the failed run, not the previous successful run's SHA.
Its physical identity is retained, and the three protected copies stay exact.
Only after both audits, all 18 process exits, socket and claim release checks
may an independent outer result be promoted. Any actual failure preserves
all evidence and does not trigger automatic retry or rollback.

The runner's pure Python mocks cover exact sender deltas, input projection,
two real process lifecycles as mocked processes, cleanup failures and claim tail.
They do not establish an actual retained-database recovery result.
