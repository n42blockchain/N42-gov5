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
path-alias regressions cover the shared primitive.

The stopped-original operation passed on 2026-10-16 at 21:54 UTC, from clean
source `97d591ae1e95f97d4f03ad511f21e356e7aeb806`. Its promoted result is
`/data/blockchain/gov5-work/validation/20261016-gov5-qmdb-original-97d591ae/original-result.json`,
SHA256 `89796a91799813ddaf246802f8d8808720afae06cbec2865b4aaa58fd1bdcb7f`.
The audit binary SHA256 is
`db71c2fdac7004924c3708c87c97d20f7eadffaad05aab17fe78f03cebdd9cc0`.
The claimed turn ran the 19-test targeted race suite (test execution 3.317s),
the Make audit-tool build, and the original restoration; all three returned 0.
The worker and all three process groups exited, and the owned BOX claims were
released before promotion.

Independent result verification compared all 80 logical table records against
the successful candidate proof. Only `qmdbTwigLeaves` target rows changed from
zero to two. Its other seven rows, every other table's rows and all sequences
remained identical. The original retained device/inode `66305/3088445986`,
2,147,483,648 bytes and mode 0600. Its new physical file SHA256 is
`13ee1715089c496e943fc80debf1b7993c67da4ce00faaa6206e194d1310affc`;
MDBX commit metadata can change the physical hash beyond the two logical rows.
All three protected copies retained their full original observations and hashes.

Fresh physical read-only reopen required no missing-leaf overlay. Canonical and
applied both remained height 254, hash
`0x62bbcf53ba1c0e26f93dc2a1e2c3a0bbe8ca44abdcc342899fb893069ae3258b`,
root `0x7e70c926394f2565ab7a8de990fbaf5ecc95959527f4c4b50730a6cea00c612d`.
The forest retained 10,415 live keys, next slot 16,957 and exactly the same two
chain-bound empty-beneficiary representation exceptions. No node runtime,
transaction dispatch, nonce replay or TPS measurement ran in this turn.

After a successful original restoration, a separately gated Miner=false,
no-dispatch same-root recovery must reconcile canonical evidence and read all
6000 sender nonces from one consistent canonical tuple. Old unknown ACKs are
not replay authorization. This repair command does not perform that recovery,
change a manifest/nonce/genesis, or establish a new TPS result.
