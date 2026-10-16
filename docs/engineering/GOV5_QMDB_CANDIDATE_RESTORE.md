# Restore only a new QMDB candidate copy

The promoted v2 audit proved the retained s192r1 copied state consistent with
exactly two missing sealed genesis leaf blobs and two explicitly bound empty
beneficiaries. Its report SHA256 is
`492e5064bfafd738bdd9025f7589423231cb888b2086090afaf98e3f688c0e8d`.
See `GOV5_QMDB_COPY_AUDIT_FIRST_RESULT.md` for the actual roots, blob digests and
scope. A new candidate copy was restored and physically reopened successfully
on 2026-10-16 at 19:42 UTC; the original database remains unchanged.

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
changed proof/blob metadata, path aliases and hardlinks.

The retained-data candidate run used clean source
`522cdd2031c0461ff01edb2ae364e00294db7668` and binary SHA256
`3b5f3876215358e154affc9005ed6e13cfe04f8ea0e375404e5d9e9ea4119f6b`.
Three quiet samples (load 4.27, 2.58, 1.56; available memory 99.77, 100.00,
100.16 GiB) and the standard claim checks preceded the bounded turn. The
targeted race suite passed (package time 1.940 s), the Make audit-tool build
passed, and the actual clone/restore/reopen command exited 0. The controller
promoted the result only after normal claim exit and released its owned claims.
No acknowledgment appeared in the shared handoff request; obtaining this turn
does not establish an agreed fair queue.

The complete artifact is
`/data/blockchain/gov5-work/validation/20261016-gov5-qmdb-candidate-522cdd20`.
Its promoted `restore-result.json` has SHA256
`52c637a0442bdfd59e55cd63f0dee91b28fe8f2c1875924f7c0ead7682d86c82`.
The directory also retains the controller, runner hashes, stage logs,
`candidate-clone.json`, fsynced intent, source postcheck and unpromoted
observation. The candidate is retained at
`restore-candidate/chaindata/mdbx.dat`, mode 0400, device/inode
`66305/5465480066`.

| Actual check | Result |
| --- | --- |
| Candidate physical preimage | `203f76c0cf66626792e884fab38cdd44f960d6f77127daa06a0aeced2a8cdead` |
| Candidate physical postimage | `59ce4a664241731dfa1a4ba0290820cc3eb9e855d073a4a8cb1278553e2b8d4f` |
| Logical tables compared | All 80, including row fingerprints and sequences |
| Only logical changes | `qmdbTwigLeaves` keys 0 and 1 added; seven other leaf rows unchanged |
| Physical read-only reopen | Passed with no overlay or missing genesis leaf candidates |
| Canonical and applied anchor | Height 254, hash `0x62bbcf53ba1c0e26f93dc2a1e2c3a0bbe8ca44abdcc342899fb893069ae3258b` |
| Reopened root | `0x7e70c926394f2565ab7a8de990fbaf5ecc95959527f4c4b50730a6cea00c612d` |
| Forest population | 10,415 live keys, next slot 16,957, genesis slots 6,000 |
| Representation exceptions | Exactly the same two previously proved empty beneficiaries at slots 6,016 and 15,347 |

The original runtime, untouched backup and preserved audit copy were rehashed
before and after this actual run. Every identity, size, modification/change
time, mode and SHA256 matched. All three remained at the physical preimage
digest above, with distinct inodes `3088445986`, `9999553335`, and `10139408920`.
Neither the original nor backup was MDBX-opened; the preserved audit copy was
only hashed during this turn. Only the new candidate entered a writable MDBX
environment. The two blob digests remain those in the promoted v2 proof.

This proves the candidate's two-row restoration and reload consistency. It is
not an original-database repair, seven-process recovery, complete sender nonce
audit, transaction replay authorization, or a new TPS result. A later original
application requires a separate reviewed operation; no such operation ran here.
