# Historical Subsystems Health

This is the operator-facing health snapshot for every "historical" subsystem
package — i.e. the AI / messaging / distributed compute / MEV / bundler /
ZK / EXEX subsystems that grew up alongside the EthEL replay and consensus
core. The point of this document is to answer "do I know which optional
subsystem is currently rotting?" without grepping the test output.

## How to reproduce this audit

```bash
# Run from repo root. Each subsystem package is tested independently
# (not via ./... because pre-existing build issues in cmd/utils may
# block umbrella runs).
go test -count=1 -timeout 60s ./internal/ai/...
go test -count=1 -timeout 60s ./internal/distributed/messaging/...
go test -count=1 -timeout 60s ./internal/distributed/compute/...
go test -count=1 -timeout 60s ./internal/distributed/coprocessor/...
go test -count=1 -timeout 60s ./internal/distributed/storage/...
go test -count=1 -timeout 60s ./internal/distributed/notify/...
go test -count=1 -timeout 60s ./internal/mev/...
go test -count=1 -timeout 60s ./internal/bundler/...
go test -count=1 -timeout 60s ./internal/mcp/...
go test -count=1 -timeout 60s ./internal/exex/...
go test -count=1 -timeout 60s ./internal/peerdas/...
go test -count=1 -timeout 60s ./internal/deferred/...
go test -count=1 -timeout 60s ./internal/zkprover/...
go test -count=1 -timeout 60s ./internal/zkverifier/...
```

The race-detector pass is significantly slower; the messaging subsystem in
particular has been validated race-safe per the CLAUDE.md test inventory
(`go test -race ./internal/distributed/messaging/...`). For the periodic
health audit, the non-race run is sufficient.

## Snapshot — 2026-04-10

**Result: 26 subsystems healthy / 0 need attention / 0 untested.**

| Package | Status | Notes |
|---------|--------|-------|
| `internal/ai/wallet` | ✅ PASS | Agent wallet, session keys, spend policies, paymaster |
| `internal/ai/coord` | ✅ PASS | Agent registry, negotiation, reputation |
| `internal/ai/governance` | ✅ PASS | Dataset registry + ethics committee voting |
| `internal/ai/training` | ✅ PASS | ZK training verification |
| `internal/ai/attestation` | ✅ PASS | Inference attestation chain |
| `internal/distributed/messaging/crypto` | ✅ PASS | X25519 / ChaCha20-Poly1305 / HKDF |
| `internal/distributed/messaging/group` | ✅ PASS | MLS-inspired group encryption |
| `internal/distributed/messaging/identity` | ✅ PASS | DID v1.1 (`did:n42:<address>`) |
| `internal/distributed/messaging/rln` | ✅ PASS | Rate-limiting nullifier (Waku RLN v2 pattern) |
| `internal/distributed/messaging/store` | ✅ PASS | Persistent CAS-backed store + sync |
| `internal/distributed/messaging/stream` | ✅ PASS | SSE streaming server |
| `internal/distributed/compute/wasm` | ✅ PASS | WASM execution engine (wazero-compatible) |
| `internal/distributed/compute/batch` | ✅ PASS | MapReduce batch compute |
| `internal/distributed/compute/inference` | ✅ PASS | AI inference + opML verification |
| `internal/distributed/coprocessor` | ✅ PASS | Tiered verification (ZK / Optimistic / TEE), 6 test files: challenge / marketplace / provider / service / slashing / verification |
| `internal/distributed/storage/ed2k` | ✅ PASS | eDonkey2000 bridge (MD4 hash + ed2k links) |
| `internal/distributed/storage/torrent` | ✅ PASS | BitTorrent bridge (CAS↔infohash) |
| `internal/distributed/notify` | ✅ PASS | Push notifications (contract events → wallet) |
| `internal/mev` | ✅ PASS | AI block optimizer + gas predictor |
| `internal/bundler` | ✅ PASS | ERC-4337 bundler + agent session validator |
| `internal/mcp` | ✅ PASS | MCP server (AI agent data queries) |
| `internal/exex` | ✅ PASS | Execution Extensions framework + AI data indexer |
| `internal/peerdas` | ✅ PASS | PeerDAS data availability sampling (EIP-7594) |
| `internal/deferred` | ✅ PASS | Deferred execution pipeline |
| `internal/zkprover` | ✅ PASS | STARK / SNARK / SP1 backends, 8 test files (incl. guest) |
| `internal/zkverifier` | ✅ PASS | ZKML proof verification |

### Highlights

1. **Coprocessor** has the heaviest test surface — 6 test files, all green.
2. **ZK stack** (zkprover + zkverifier) is fully tested across guest execution, input building, proof generation, and verification.
3. **Messaging** subsystems (6 sub-packages) all have unit tests with no external dependencies — they use in-memory mocks throughout.
4. **No external service dependencies** were detected during the audit. None of the subsystems require a running libp2p network, an SP1 prover, or any HTTP service to pass their unit tests.

### Notable absences from the catalog

These `internal/` packages are intentionally NOT in this audit because
they're core stack covered elsewhere:

- `internal/ethel/` — covered by P0/P1 freezer, executor, and journal verify work
- `internal/consensus/{apoa, apos, hotstuff}` — covered by `chaos_test.go` and `byzantine_test.go`
- `internal/bridge/` — covered by `publisher_test.go`
- `internal/node/`, `internal/api/`, `internal/sync/`, `internal/p2p/`, `internal/vm/` — core, tested via their own dedicated suites
- `internal/cscompact/`, `internal/parallel/`, `internal/replay/`, `internal/snapshot/`, `internal/txspool/`, `internal/miner/` — not in scope for the historical-subsystems audit

If you add a new "optional" subsystem package under `internal/`, add it to
the table above and to the reproduce-the-audit command list at the top.

## When to re-run

- After any major refactor that touches package boundaries
- Before each release tag
- Monthly as a baseline check

Don't trust this snapshot beyond ~30 days without re-running.

## Future work

Today's audit only exercises **unit** tests. The following are open
opportunities (not stability blockers — listed here so someone can pick
them up):

1. **Cross-package integration tests** between `messaging` and `notify`,
   between `compute/wasm` and `coprocessor`, between `bundler` and
   `ai/wallet` (session-key validation path).
2. **Real libp2p network scenarios** for `messaging/{store, stream}` —
   the current tests use in-memory mocks. A scripted 3-node libp2p
   harness would catch real-world serialization and gossip-routing bugs.
3. **SP1 prover end-to-end** for `zkprover` — currently tested with
   mock prover; a CI job that runs the real SP1 toolchain on a small
   guest binary would catch toolchain regressions.
4. **opML challenge protocol** for `distributed/compute/inference` —
   today's tests cover the cache and executor, but not the full
   challenge-response game between provers and verifiers.

## See also

- `memory/reference_subsystems_health.md` — point-in-time machine snapshot of this catalog
- `CLAUDE.md` — full subsystem inventory with file:line references
- `docs/ethel/freezer-tables.md` — EthEL output table catalog
- `docs/consensus/hotstuff2-spec.md` — HotStuff-2 SR1–SR9 mapping
- `docs/bridge/deployment.md` — bridge runtime mode matrix
