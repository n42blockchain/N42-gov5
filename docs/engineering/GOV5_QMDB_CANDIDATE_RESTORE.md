# Restore only a new QMDB candidate copy

The promoted v2 audit proved the retained s192r1 copied state consistent with
exactly two missing sealed genesis leaf blobs and two explicitly bound empty
beneficiaries. Its report SHA256 is
`492e5064bfafd738bdd9025f7589423231cb888b2086090afaf98e3f688c0e8d`.
See `GOV5_QMDB_COPY_AUDIT_FIRST_RESULT.md` for the actual roots, blob digests and
scope. No restoration has yet been run against that retained data.

The existing `gov5-qmdb-audit --copy-db ...` entry remains read-only. The
separate `restore-candidate-copy` subcommand writes only a new, owned, private
`/data/blockchain/gov5-work/validation/<artifact>/restore-candidate/chaindata`
copy whose `mdbx.dat` is regular, mode 0600, and not hardlinked. It rejects the
runtime, the audit copy, aliases, shared inodes, existing attempt outputs and
wrong physical preimage hashes. It has no original-database write override.

The explicit inputs are:

| Flag | Binding |
| --- | --- |
| `--candidate-db` | New dedicated candidate directory, independently cloned from the audited bytes |
| `--original-db` | Stopped isolated performance `runtime/<run>/e/chaindata`; read-only file hashing and existing node LOCK only |
| `--audit-db` | Preserved mode-0400 `audit-copy/chaindata`; hashing only |
| `--backup-data` | That audit artifact's mode-0400 `untouched-backup/mdbx.dat`; hashing only, never MDBX-opened |
| `--proof` | Promoted private `audit-result.json` |
| `--proof-sha256` | Exact report digest above, supplied explicitly by the pinned controller |
| `--genesis` | Original runtime genesis bytes, matching the report's exact file digest and root/hash |
| `--output` | New candidate artifact's `restore-observation.json`; never overwritten |

The command holds the original node's LOCK to establish it is stopped, and
rehashes original, backup, audit and candidate files before work. All four
inodes must differ and all bytes must match the promoted snapshot. Neither the
original, backup nor preserved audit file is opened through MDBX. A fresh
scratch genesis must reproduce the exact original genesis root and pass its
own strict forest/PlainState cross-check.

Before opening a writable environment, it opens the candidate read-only,
repeats the complete promoted snapshot audit, and records every sorted logical
bucket, row count, sequence and length-delimited SHA256. Only LeavesTable keys
0 and 1 are excluded from the otherwise complete fingerprints. The sequence
table itself is also included, so an absent sequence and an explicitly stored
zero are distinguishable. Unreadable or malformed metadata fails closed.

An exclusive `restore-intent.json` containing file identities, proof digest,
preimage table fingerprints and absent target blob hashes is fsynced, including
its parent directory, before writable MDBX open. One transaction repeats the
snapshot and fingerprint checks, requires both target rows absent, and inserts
exactly those two independently reconstructed genesis blobs. No seeding, root
flush, canonicalization, meta/bitmap/entry/PlainState mutation or sequence
increment runs on the candidate. Whole-snapshot and all-other-row checks must
pass before commit; a failed second write rolls back the first.

After commit every transaction and the writable environment close. The file
becomes 0400 and is synced. A new read-only environment must load the exact
blobs, reproduce every original anchor/root/population/empty-beneficiary
exception with **zero overlay candidates**, and prove exactly two target rows
were added while all other logical rows and sequences remained equal. Its
physical pre/post file hashes are recorded; MDBX commit can change metadata and
free pages, so physical byte offsets are not claimed to be the only changes.

Finally the original, backup and audit file identities and SHA256 values must
still equal their pre-work observations. The command writes a source postcheck
even when the candidate action fails. Only after those checks does it emit a
successful observation. The hardware controller must promote a final result
only after clean process exit and normal claim-context exit. Any failure retains
the candidate, intent and logs, and stops; it never automatically retries,
rolls back a committed candidate, discards evidence, or writes the original.

Validation so far: all 15 command-package tests passed with
`GOMAXPROCS=2 GOFLAGS=-p=1 go test -p 1 ./cmd/gov5-qmdb-audit -count=1`, plus
`go vet -p 1 ./cmd/gov5-qmdb-audit`. Real MDBX tests use 6000 genesis accounts,
reclaimed old genesis entries, a retained empty beneficiary, a single two-row
transaction and a physical close/reopen. They also cover injected second-write
failure/rollback, other-table and sequence changes, existing target rows,
changed proof/blob metadata, path aliases and hardlinks. Claimed race/build and
retained-data candidate restoration remain pending. A later original-database
application would be a separate reviewed operation; this slice does not do it.
