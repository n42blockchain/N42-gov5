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

## Follow-up diagnostic and bounded beneficiary policy

The `fd06a6f8` tool ran against the same retained working copy on 2026-10-16,
finishing at approximately 17:44 UTC. Its targeted race test and Make binary
build succeeded; the audit still exited 1. The working-copy SHA remained the
same. Evidence is under
`/data/blockchain/gov5-work/validation/20261016-gov5-qmdb-diagnostic-fd06a6f8`.

The two unmatched entries were both exactly `00`:

| Append slot | Account key hash | Exact public-address match |
| --- | --- | --- |
| 6016 | `015e9b776707cfa01f2c14c53d2763a203f2309e7b40df49d82be9fc9e34dfaf` | Genesis validator 1, `0xeA2657E22d210e97Ea3c58965DEF60bD44028569` |
| 15347 | `cd8394f3b1eaaf70952da6b527ae166fa7f3fe00ab5b73d86fd3673d948e32b5` | Genesis validator 2, `0xD58df8E9625F8a13e698830A3E41a5aEDA145aa9` |

Neither key was the system address. Mapping only public genesis addresses,
recipient derivations and system-address candidates identified these two
preimages without opening any database. `public-address-map.json` records the
mapping and script digest in the diagnostic artifact directory.

The v2 audit adds an explicit exception report for retained empty beneficiaries.
This follows the existing QMDB reader's EIP-161 interpretation; it does not
change stored leaves, state roots, balances or production behavior. It requires:

- EIP-161 active from genesis, and at most 4096 headers to exact genesis.
- Every ancestral header bound to its requested parent hash and height.
- The candidate address to be an ancestral nonzero-gas block's coinbase and a
  member of the exact genesis validator set.
- A committed account key with that exact address preimage, value exactly `00`,
  and no corresponding PlainState account row, including no empty-valued row.

The report retains the original root and live count, and lists each classified
address, key, append slot and value. Unknown zero-valued keys, storage-shaped
zero values, nonempty missing accounts, all PlainState value mismatches and
structural errors still fail. The generic audit path remains strict. This is a
state/forest consistency audit under an explicit existing empty-account policy;
it is not independent execution correctness, consensus-finality verification,
or permission to discard any old unknown ACK outcome.

The new real-MDBX tests preserve a retained empty account in QMDB while omitting
its PlainState row, verify the unchanged root/live count and explicit report,
and reject unknown/preimage-mismatched/noncanonical values, an existing plain
row, malformed storage, wrong genesis, missing ancestry and non-validator
beneficiaries. Physical-site validation of this revised policy is still pending;
no repair has been applied.
