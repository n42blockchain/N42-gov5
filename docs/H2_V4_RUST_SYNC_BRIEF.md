# H2-v4 sync brief for n42-26 (Rust)

Status: the chain-bound H2-v4 consensus profile is now merged into gov5
`main` (merge commit `c387d6c6`). This note is the hand-off: what landed,
what is guaranteed, and what n42-26 needs to do to complete cross-client
interop. Full wire semantics live in `docs/H2_CROSS_CLIENT_VECTORS.md`;
this brief is the action list.

## What is on gov5 main now

- **Chain-bound v4 signing domains** — every preimage starts with
  `N42H2V4`, a phase byte, chain id, genesis hash, and view; block-bearing
  phases bind the block hash, Proposal/Commit bind the validator-change
  hash. Pinned in `testdata/h2_v4_domains_v1.json`.
- **Versioned interop envelope** — v4 magic, full chain identity,
  validator-change hash, exact payload length, pinned Snappy framing.
  Pinned in `testdata/h2_v4_envelope_v1.json`.
- **Verifiable finality proofs** — `VerifyH2V4Decide(envelope, validatorSet)`
  verifies a committed Decide without running a consensus engine; intended
  for observers and cross-client sync. Vectors in
  `testdata/h2_v4_finality_v1.json`.
- **Gossip topic** — a committed Decide is republished as a v4 envelope on
  `/n42/h2/4/ssz_snappy` when the profile is active.
- **Portable replay snapshots** — `cmd/n42-qmdb-export` produces a
  cross-client bootstrap snapshot (see `docs/QMDB_CROSS_CLIENT_BOOTSTRAP.md`).
- **Opt-in only** — `hotstuff.interopV4: true` in genesis chain config, on
  every validator, for NEW static-validator chains. With the flag absent,
  the deployed legacy wire bytes are preserved exactly; the 7-node
  performance network is unaffected.

## Fixture integrity — verify before anything else

The fixtures are byte-exact contracts. SHA-256 of the files on gov5 `main`:

```
0c5877432b8d7adb3fc60c5226564ad1d0e099b6c73f39b823703926e82d2aee  cross_client_h2_v1.json
f3f20d4641455eaf7ea6c96641fc4674134080aefcb300c219ab34a53d4d9510  h2_v4_domains_v1.json
09a98f549fcfa1b4185b78b975fa680608c73e169758cb0c052c72efbff4ff83  h2_v4_envelope_v1.json
feacd6d0d2dc3babcbe3440384021ee9291b68103baaf7d47cd0ff1c6b703488  h2_v4_finality_v1.json
```

`cross_client_h2_v1.json` matches the SHA n42-26 already pins — the legacy
wire contract has NOT drifted across gov5's recent throughput work (the
consensus message codec is untouched; changes were pipeline/ordering only).

**Line-ending warning, learned the hard way:** a Windows checkout with
`core.autocrlf=true` silently rewrote these JSON files to CRLF and made the
pinned-fixture tests report phantom "drift". gov5 now pins
`testdata/*.json -text` in `.gitattributes`. If n42-26 vendors or mirrors
these files, compare SHA-256 of raw bytes, never text-mode content, and pin
the same attribute if the repo may be checked out on Windows.

## One semantic note from the merge

gov5 main resolves the validator set for QC/TC verification **per
certificate view** (epoch-aware, `resolveQCValidatorSet`), composed with the
v4 domain selection. For the first v4 profile this is invisible to n42-26:
the profile mandates a zero validator-change hash and rejects
reconfiguration, so the set is static. It matters only for the future
protocol revision that introduces dynamic validator changes — at that point
both clients must resolve historical sets identically, and both must derive
the same `changes_hash`.

## Action items for n42-26

1. **Verify fixture SHAs** (table above) against your vendored copies.
2. **Implement / re-verify the v4 domains** against
   `h2_v4_domains_v1.json` — all eight message phases.
3. **Implement the v4 envelope codec** against `h2_v4_envelope_v1.json`,
   including the five mandatory rejection paths: identity mismatch, length
   mismatch, trailing bytes, decompression expansion, non-canonical inner
   message.
4. **Consume finality proofs**: subscribe `/n42/h2/4/ssz_snappy`, verify
   with your equivalent of `VerifyH2V4Decide`, using
   `h2_v4_finality_v1.json` as the acceptance vector set.
5. **Joint testnet**: bring up a fresh static-validator gov5 chain with
   `hotstuff.interopV4: true` and attach an n42-26 observer; success = the
   observer follows finality from envelopes alone. Optionally bootstrap the
   observer from a `n42-qmdb-export` snapshot.
6. **Do not** configure epoch validator changes on any v4 chain until the
   dynamic-changes revision is specified on both sides.

## Joint-testnet status (2026-08-07, live observations)

A 4-validator gov5 v4 testnet is already up on this host (not started by
this note's author — coordinate before touching it):

- chainId **941**, `interopV4: true`, genesis at `C:/n42/v4-interop-testnet/genesis.json`
- binary `n42-v5.7.947` (gov5 main incl. the v4 merge), `--chain private`
- ports: p2p TCP 32100-32103 / UDP 33100-33103, HTTP 20112-20115
- the production 7-node performance network stays on 32000-32006 / 20012-20018
  and was rolled to the same 947 binary; v4 is dormant there (no identity configured)

Observed blocker at the time of writing: head stuck at 0 with partial peer
counts — the validators are not fully meshed. Root cause visible in the
process command lines: the nodes were launched from Git Bash, and MSYS path
conversion rewrote the `--p2p.peer` multiaddrs from `/ip4/127.0.0.1/...` to
`C:/Program Files/Git/ip4/127.0.0.1/...`, which cannot parse. Fixes, any one
of which works:

- `export MSYS_NO_PATHCONV=1` (or `MSYS2_ARG_CONV_EXCL="*"`) before launching;
- pass multiaddrs as `//ip4/...` (double slash defeats the rewrite);
- launch from PowerShell, which performs no path conversion.

A registered chainspec `h2_interop_test` (chainId **96**, 4 validators,
`interopV4: true`, reconfiguration pushed out of reach) now exists in gov5
for a reproducible interop chain that needs no ad-hoc genesis file:
`--chain h2_interop_test`. Its genesis-hash constant is backfilled on first
boot; the ad-hoc 941 network and this preset can coexist.

## Contact points in the gov5 tree

| Item | Location |
|---|---|
| Domains, envelope, finality verify | `internal/consensus/hotstuff/interop_v4*.go` |
| Pinned vectors + generator tests | `internal/consensus/hotstuff/cross_client_wire_test.go`, `interop_v4_test.go` |
| Wire semantics doc | `docs/H2_CROSS_CLIENT_VECTORS.md` |
| Snapshot bootstrap | `cmd/n42-qmdb-export`, `docs/QMDB_CROSS_CLIENT_BOOTSTRAP.md` |
| Topic + scoring | `internal/p2p/topics.go` (`H2V4Topic`), `gossip_scoring_params.go` |
