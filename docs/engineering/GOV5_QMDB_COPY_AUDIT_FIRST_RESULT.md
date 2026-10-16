# First copied-database audit: 2026-10-16

The `b0745937` audit failed at its final PlainState cross-check. It did not
modify or repair the retained runtime database. No valid chain TPS or recovery
nonce vector was produced.

The artifact directory is
`/data/blockchain/gov5-work/validation/20261016-gov5-qmdb-audit-b0745937`.
Its `copy-audit-0137.log`, `copy-manifest.json`, `copy-postcheck.json`, and
`status.json` retain the failed attempt. The immutable source binary SHA256 is
`8d244aac8ecc4571c6f8039e3f45ddede9a7ab83bd956399659973fe653374d1`.

The claimed copy/audit turn ran at approximately 17:34:29–17:34:38 UTC. Both
copies used reflinks into separate inodes. The original file and the unopened
backup had SHA256
`203f76c0cf66626792e884fab38cdd44f960d6f77127daa06a0aeced2a8cdead`
before and after the audit. The original was read through a file descriptor;
neither the original nor the backup was opened through MDBX. Only the working
copy was opened read-only. The process exited 1, stopped its own work, and
released its claims. The shared rotation request had no observed acknowledgment;
the turn was obtained through the normal quiet/claim protocol.

The audit passed the frozen-leaf, live-entry, bitmap, full-index, forest-root,
and applied-header checks with the in-memory genesis-leaf overlay. Its rebuilt
index had 10,415 live keys and 16,957 append slots. It then reported
`committed live keys absent from plain state`. This is not enough to conclude
that funded account data is missing, or that the proposed leaf restoration is
safe to apply.

The existing QMDB reader documents retained empty accounts that EIP-161 removes
from PlainState (`modules/state/commitment/qmdb_state_reader.go`), and
`docs/QS_BLOCK_TIME_BUDGET.md` section 6g records the empty system address as one
such difference. A follow-up diagnostic now reports the unmatched count, value
lengths, single-zero-byte count, and at most eight sorted key/slot/value samples,
including an exact system-account-key flag. It still rejects every unmatched
key; recognizing an address does not itself authorize accepting or repairing it.
The next audit must use the retained working copy and preserve this failure.
