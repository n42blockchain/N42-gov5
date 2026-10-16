# Proved stopped-original two-leaf restoration

The candidate-only restoration passed on 2026-10-16 at 19:42 UTC. Its
`restore-result.json` SHA256 is
`52c637a0442bdfd59e55cd63f0dee91b28fe8f2c1875924f7c0ead7682d86c82`,
binding the earlier read-only v2 audit SHA256
`492e5064bfafd738bdd9025f7589423231cb888b2086090afaf98e3f688c0e8d`.
All 80 logical tables and their sequences were compared; only absent leaf
keys 0 and 1 were inserted. Physical reopen reproduced the unchanged root and
all original anchors with no missing-leaf overlay. See
`GOV5_QMDB_CANDIDATE_RESTORE.md` for that completed operation.

`restore-original-two-leaves` is a separate explicit command. The default
read-only audit and candidate-copy command retain their existing path and
permission contracts. There is no force, rename, replacement, runtime start,
transaction dispatch or automatic retry option. This command repairs only the
stopped original file whose complete identity appears in the promoted
candidate proof, under the isolated performance runtime root.

The original-specific inputs are `--original-db`, `--backup-data`, `--audit-db`,
`--candidate-db`, `--proof`, `--proof-sha256`, `--candidate-proof`,
`--candidate-proof-sha256`, `--genesis`, and `--output`. The output must be a new
private validation artifact's `original-observation.json`; existing intent,
postcheck, observation or promoted-result files prevent reentry. Both reports
are bounded private regular files, decoded strictly and checked against their
explicit SHA256 values. The candidate report must prove its original sources
unchanged, exactly the two logical additions, and a successful physical reopen
whose anchors and forest match the v2 audit.

The original directory must exactly equal the one in that proof. Its entire
preimage observation must match, including device, inode, size, mode 0600,
modification/change times and SHA256. For the retained s192r1 incident these are
device/inode `66305/3088445986` and physical SHA256
`203f76c0cf66626792e884fab38cdd44f960d6f77127daa06a0aeced2a8cdead`.
The existing original `e/LOCK` is held exclusively from before original MDBX
open until the final observation is written. The hardware controller must
separately retain the normal BOX claims for the entire bounded operation.

The untouched backup, prior audit copy and successful restored candidate are
hashed against their exact proved observations. They must remain mode 0400,
owned, unaliased, not hardlinked, and on inodes distinct from each other and the
original. None is MDBX-opened. Original genesis bytes regenerate an independent
scratch genesis; both recreated sealed leaf blobs must match the promoted
lengths and hashes.

A read-only original baseline must reproduce the complete original v2 audit
and exactly the candidate proof's full preimage table fingerprints. The file
identity/SHA is checked again. An exclusive `original-intent.json` records both
report digests, the original preimage, immutable-copy identities, absent target
hashes and every table fingerprint; file and directory are synced before any
writable environment is opened.

One Accede RW transaction calls the shared transaction-only primitive. That
primitive has no file paths or database-open authority. It repeats the complete
audit and all table checks, requires each target absent, inserts exactly two
leaf rows, and verifies the restored root and every other logical key/value and
sequence before commit. Existing leaf rows are never overwritten. No seeding,
root flush, canonicalization, account mutation or sequence increment runs.

All writable transactions and the environment close before file sync and a
fresh read-only open. The original remains mode 0600 on the same inode. The
new environment must read both exact leaf blobs, audit with zero overlay,
preserve every anchor/root/population/beneficiary exception, and reproduce the
successful candidate's complete postimage logical fingerprints. Protected
copies must remain byte-for-byte and identity-for-identity unchanged afterward.
Only then can the command write a passed observation. The controller promotes
`original-result.json` only after the child and claim context both exit cleanly.

Any failure retains the original, copies, intent and logs and stops. A commit
error or failure after commit can leave the write outcome uncertain; the tool
does not automatically undo committed rows, replace the original, retry, or
discard evidence. The next action would be a separately bounded read-only
inspection. Ordinary rollback of an uncommitted transaction remains in place.

New real-MDBX tests exercise the completed temporary candidate followed by a
temporary original restoration, same inode/mode preservation, unchanged
protected copies, proof/inode/ctime/SHA rejection before open, and a real commit
followed by injected physical-reopen failure retaining both rows and the intent.
The existing two-write rollback, non-target row/sequence, malformed proof and
path-alias regressions cover the shared primitive. Actual original-site
restoration has not yet run.

After a successful original restoration, a separately gated Miner=false,
no-dispatch same-root recovery must reconcile canonical evidence and read all
6000 sender nonces from one consistent canonical tuple. Old unknown ACKs are
not replay authorization. This repair command does not perform that recovery,
change a manifest/nonce/genesis, or establish a new TPS result.
