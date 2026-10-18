# Request-local certified execution reads

The q3 bounded window remains failed because its acknowledged prefix did not
drain within the original 45-second deadline. Its canonical window recorded
326,000 transactions over 20.011817662 seconds: 16,290.37429313751 TPS as a
failed-run diagnostic, not a passing persistence or 5M capacity result. Source
supply did not exhaust; no unknown ACK error was observed.

The subsequent ordinary nonproducing double physical reopen passed at height
1358, hash `0x55ed1238d2fd8b74533bb2b670c5466c8bf113e050a688c39643604dc48811b7`,
canonical nonce sum 3,428,353, root
`0xbfe0654d5921cc776daf644e03c66432a41b25969b094a43e16a35fe9cfbab16`,
and account-vector hash
`0xbb3f1d3c84a290bb71082f75ba7b392c091b02c0177a04fa2030489f470d8cd8`.
The two complete 6000-account snapshots agree with different empty epochs.
Actual newly persisted transactions were 1,141,000; 1,184,647 newly ACKed
transactions were not canonical. Neither the old admission session nor its
pending queue is an authorization to replay.

Evidence under `/data/blockchain/gov5-work/validation/`:

- `20261018-gov5-certified-commit-20s-target-v1/failure-acceptance.json`:
  `12534dcf4920a326c217e0072bfb6733f28c4efcf229b5ea38c5e404401ee261`.
- `20261018-gov5-post-bounded-double-reopen-v3/audit-result.json`:
  `28286e2110cfb99a2c628c811dae008d762d1c2f313a212e4699e2240df870fb`.
- The latter independent `acceptance.json`:
  `7be766498bd9a930c57c708627b8e737e0128088a4c042d7533e737f7cf60d5e`.

The frozen q3 records show that repeated certificates no longer change ingress
generation or force repeated ingress signature verification. They also show
repeated body/receipt decoding inside the serialized owner execution lock.
Overlapping lock-wait durations are not summed as CPU time. A separate 29.252s
read-only process observation averaged 6.938 logical CPU equivalents across the
nine runtime processes, including 2.831 for the owner. It was not aligned to the
20-second canonical window and does not establish a CPU bottleneck.

## Narrow read reuse

The new chain-owned `VerifyCertifiedCanonicalExecution` reads the actual stored
block once, freshly verifies the full public CommitQC and canonical/finality/
applied-lineage evidence, and derives durable receipts from that same block in
the same read transaction. Every physical TxLookup row is still checked.
Transaction hashes may be reused only on that request's decoded transactions.
The returned proof contains scalar observations, not block objects or reusable
authorization. The original prefix-only API retains its contract.

A clean certified duplicate can use this complete proof without performing two
additional owner-side body decodes. Any missing lookup, pending lag write,
missing covering finality, or dirty/bootstrap/lagging queue follows the original
full-body/durable/writer/reconcile path. Invalid evidence remains an error.
The final owner canonical durability read remains mandatory. Trusted commits,
ordinary advances and legacy receipt readers retain their behavior.

The new error-aware receipt reader propagates hot-store/archive/decode/metadata
errors, including errors for empty blocks. It still permits the existing valid
zero-byte encoding of an empty receipt list. No cross-request cache, signature
bypass, codec change or owner authorization change is introduced.

This slice is awaiting controlled source validation and a new real measurement.
No throughput improvement is claimed from source inspection or read counts.
