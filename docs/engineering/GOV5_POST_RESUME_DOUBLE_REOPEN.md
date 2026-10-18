# Post-resume audit without transaction dispatch

The first retained `s192r1` four-frame attempt received all four final ACKs:
583 canonical transactions skipped, 1417 newly admitted, 2000 online signature
checks and no retries or unknown ACKs. Its subsequent production-phase exact
canonical sampling timed out. That attempt remains failed; no physical-reopen
or TPS acceptance is inferred from its ACKs or execution logs.

`scripts/gov5-perf/post_resume_audit.py` provides a separate audit-only path.
It never initializes a database, arms a resume plan, submits frames, or enables
a producer. Both phases use the already accepted node, execution-service and
replay binaries. Each phase starts the strict owner and seven validator
configuration copies whose only change is `node.miner=false`, then runs
`recover --require-canonical-roots` over the full public sender vector.

The first phase creates new post-dispatch evidence. It requires a fresh epoch,
canonical height above the historical baseline, exactly 211917 executed
transactions, and the historical vector plus the acknowledged suffixes. Only
three sender nonces increase; each sender balance decreases by `21001` times
its new transaction count. All other sender fields remain identical. Header
and loaded QMDB roots must be equal, nonzero and bound to the account snapshot.
This first audit is never described as the missing production stop snapshot.

After cleanly stopping the owner and seven validators, the second phase
physically reopens the same data with a different empty epoch. It must produce
the same canonical hash, height, root and complete account vector as the first
phase. Both phases require all nine sockets removed and clean process groups;
claim-tail or claim-cleanup failure prevents final promotion.

The separately pinned outer controller retains the ordinary runtime 90 GiB
memory gate and 64 GiB disk reserve. Within its delegated exclusive claim it
hashes the original database and three protected copies before and after the
audit. The protected copies must exactly match the earlier q2 postproof. The
original must retain its path, device, inode and mode; its new pre-audit bytes
and SHA are recorded as facts rather than assumed to equal the pre-dispatch
database. Source metadata, manifests, genesis, reports and binary pins remain
fixed. Neither this runner nor its wrapper opens a protected copy as a database.

Any actual failure preserves the attempt and data. It does not authorize a
transaction retry, rollback, nonce rounding, or reuse of the old resume plan.
This audit measures no TPS. Python mock tests cover lifecycle, exact account
deltas, root failure, second-vector drift and prevention of promotion on
cleanup or claim-tail failure; the retained-chain run requires separate actual
evidence.
