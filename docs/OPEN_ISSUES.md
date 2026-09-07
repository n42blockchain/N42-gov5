# Open issues

Known defects and deferred work that no round or commit has resolved yet.
Each entry names the evidence, the blast radius, and what "done" is. Newest
first; move an entry to the commit that closes it, do not delete it here.

## Falcon-512 signature verification is forgeable (reported 2026-09-06, scheduled later)

`crypto/falcon/` is a simplified Falcon: `computeNTRUComplement`
(`crypto/falcon/internal.go`) is marked "a simplified version for
demonstration", and `falcon.Verify` (`crypto/falcon/falcon.go`) accepts
signatures the reference verifier would reject. A signature that passes
`Verify` can be produced without the private key.

Blast radius: the 0x14 precompile (`internal/vm/pq_contracts.go`,
`falconVerify`, gas 3500) and any account or transaction type whose
`SigAlgo` selects Falcon. The precompile is not in the standard fork maps
and activates only through `ChainConfig.PQPrecompilesTime`, so no live chain
is exposed today; `docs/DEVLOG.md` still lists Falcon-512 as production ready
and must not be believed until this is closed.

Done means: a verifier that matches the NIST Falcon-512 reference on the KAT
vectors (accepts every valid vector, rejects every mutated one), or the
precompile refuses Falcon until then. Scheduled after the qs throughput
work; do not enable `PQPrecompilesTime` on any chain before it lands.

## QMDB live-tree reload is O(history) (2026-09-06)

`ReloadForBuild` replays the entry log, so a node's start time grows with
the chain (rounds 32-35: 60-90 s at 13.8M blocks). Done means a checkpoint
the reload can start from.

## Intermittent startup stall after big-block legs (2026-09-06, rounds 32/34; 35g/35h)

A node logs "qmdb index loaded" and then nothing for 10+ minutes; the leg
loses that node past the 600 s readiness deadline and the next start of
the same store is fine. Rounds 35g (node3) and 35h (node2) added the
shape: the "index loaded" line reports a SHORT index (2,230,875 and
6,187,593 of 8,500,566 keys, puts == liveKeys), which the load prints
from its deferred summary on error as well as on success -- so the twig
scan most likely returned an error part way, `NewNode` returned it, and
the process exited (the pprof dump at +150 s is 0 bytes because the
server never came up). The stalled start's run.log was overwritten by the
next leg's start before it was read. debb97ef names the twig in the error
and logs it; the 35i runner copies run.log/run.err at +150 s and reports
whether the process is alive. Done means: the error named and fixed, or
the load made retry-safe.

## node3 one-time in-memory divergence (2026-09-06, round 32 first start)

Recorded in `docs/QS_BLOCK_TIME_BUDGET.md` (round 32). A restart healed it;
the cause is not known.

## Plain `Account` table frozen at 13,750,514 on the qs fleet (2026-09-06, round 26)

`N42_STATE_WRITE_QMDB_ONLY=1` stopped plain Account writes; QMDBMeta records
`accountFrozenAt`. Every later start of those datadirs needs
`N42_STATE_READ_QMDB=1`, or the miner, txpool and RPC read stale accounts.
No repair tool rebuilds the table from the tree yet.
