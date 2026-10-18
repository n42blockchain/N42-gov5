# Certified canonical commit idempotency

The bounded-window q2 attempt remains failed: its acknowledged prefix did not
drain within the original 45-second limit. Its fixed canonical window recorded
204,844 transactions over 20.001224798 seconds, or 10,241.572807105451 TPS, as a
diagnostic observation. This is neither a passing persistence test nor a 5M
capacity result. The source did not exhaust. Owner/replay/validator GOMAXPROCS
were 36/4/2; no CPU utilization profile was collected.

The subsequent ordinary nonproducing double physical reopen passed. Both complete
6000-account vectors and loaded-QMDB/header roots agree at height 1351, hash
`0x6d964853b880caa4b8ec2eed07e491f021821c54a234127f8a088de840a909f5`,
nonce sum 2,287,353, and root
`0x761d82df395fd2c83bc62901ee7785c959c5b6c947bdca659070fbaad3b55f27`.
Of 2,204,491 newly admitted transactions, 1,182,844 became canonical and
1,021,647 did not. The original database inode and three protected copies were
preserved. These final counts come from fresh account snapshots, not pending
queue arithmetic. The failed admission session cannot be replayed.

Evidence under `/data/blockchain/gov5-work/validation/`:

- `20261018-gov5-bounded-20s-quiescent-target-v1/failure-acceptance.json`:
  `9d45d969fc26312db2cbf4a1720449006ed76a889af30dc0ab0b73d2a3d343f1`.
- `20261018-gov5-post-bounded-double-reopen-v2/audit-result.json`:
  `419c926af9132b7a58b94b026cfef8b8e98510bc9e209906ab005082d12c9cac`.
- The latter independent `acceptance.json`:
  `1de0328875aeff7fe6d9e1a0b26635af67f48d4f8c56df5c836e4fd87ff24d98`.

## Restricted duplicate path

Frozen phase records show one original and six duplicate commit calls per
height. The old duplicate path performed legacy canonical/index repair and
advanced ingress generation even when the same finalized block and clean queue
were already current. The change targets this case alone.

`VerifyCertifiedCanonicalPrefix` holds the chain lock, freshly verifies the full
public CommitQC and proposal seal, and checks stored block identity, current
canonical head, covering canonical finality, and the actual applied lineage.
It never falls back from a missing applied marker. With no asynchronous
transaction indexer, every physical TxLookup row must contain the block height;
a missing or wrong row selects the existing writer so legacy repair remains.
An in-flight lag write also selects the old path without joining it inside the
read-only helper. No certificate, receipt or sender-verification cache is added.

The owner retains its complete durable receipt/root/gas/bloom check. Only a
certified duplicate whose queue is clean, outside bootstrap, and stamped at the
helper's current canonical head can skip the repeated writer and queue gate.
Dirty, lagging or bootstrapping queues retain the existing reconciliation path.
The final canonical/finality durability read and ordinary advance ordering are
unchanged. The trusted owner-capability path is unchanged.

Controlled source validation passed in
`20261018-gov5-certified-commit-validation-v1`: targeted normal tests, race, vet,
and Make all returned zero. The original mutation-lock/default-chain regressions
and seven new certified-prefix tests passed, including physical index repair,
held-reservation publication, fresh invalid-QC rejection, and seven concurrent
certificates advancing ingress generation only once. The independent acceptance
is `757358986149d7f807811b36855a2481772d8cec8dc81628f2b9c8a680879d54`;
its terminal status is
`04122ef439edf0d8f9a031c4c328f7c75c3281ee6e25b40867944e31773b4e5d`.

A new real measurement is still required. Physical index reads and receipt checks
remain proportional to block size; no throughput improvement is claimed before
measurement. Source validation did not open the retained chain or dispatch
transactions to it.
