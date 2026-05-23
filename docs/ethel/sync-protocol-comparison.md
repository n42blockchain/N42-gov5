# N42 Sync Protocol Comparison: eth/68 + 12-sec Live Loop

**Date:** 2026-05-22
**Status:** Gap analysis + recommended path
**References:** `../reth`, `../erigon` siblings of N42-gov5

This doc answers two questions:

1. Does N42 speak the eth/68 devp2p subprotocol that reth/erigon/geth
   use to sync block headers, bodies, receipts, and pooled-tx
   announcements?

2. Does N42 have the runtime to (a) catch up from a snapshot to chain
   tip, then (b) ingest new blocks every ~12 s like a normal post-Merge
   EL node?

The answers shape what the snapshot-distribution pipeline (this
session's Phases E/F/H + sim) needs to integrate with — versus what
it can stand alone.

---

## TL;DR

| Capability | N42 today | Reth | Erigon | Gap |
|:--|:-:|:-:|:-:|:--|
| Snapshot bootstrap | ✓ (Phases E/F/H + libp2p `snapsync/`) | ✓ static-file + era | ✓ stage_snapshots | — |
| Live block ingest via **Engine API** | ✓ (`internal/ethel/engineapi`) | ✓ | ✓ | none |
| Live block ingest via **devp2p** (no CL) | ✗ stub only | ✓ | ✓ | **eth/68 not consumed** |
| eth/68 NewPooledTransactionHashes68 | ✗ ignored (`eth_handler.go:146`) | ✓ | ✓ | **decoder + mempool wiring** |
| eth/68 GetBlockHeaders / Bodies responses | partial responder | ✓ | ✓ | responses produced but no peer-data **consumer** |
| Staged sync pipeline | ✓ (`internal/sync/staged/`) | ✓ | ✓ | none |
| Witness-based stateless verify | ✓ partial | ✗ | ✓ | — |
| Mainline ETH peer compat | ✗ | ✓ | ✓ | requires Option A below |

---

## Part 1 — What N42 has today

### libp2p (primary) — already production-grade

```
internal/p2p/                   libp2p host, discovery, gossip topics
internal/sync/
  staged/                       staged sync orchestrator (per-stage MDBX progress)
  snapsync/                     consensus-driven pivot + full state download
  initialsync/                  round-robin peer fetcher + FSM, catch-up to tip
  subscriber_blocks.go          GossipSub blocks → chain.InsertChain()
  rpc_*.go                      libp2p RPC: status, ping, goodbye, blocks, blobs, snap
```

This stack is **complete and battle-tested**: it handles snap sync,
initial backfill, gossip-driven live blocks, and HotStuff consensus
notifications. It's the path n42's own chain uses.

### devp2p (eth/69 advertised, eth/68 ignored) — stub

```
internal/devp2p/
  server.go                     devp2p listener, advertises ETH69
  eth_handler.go                Status, GetBlockHeaders, GetBlockBodies, GetReceipts handlers
                                  — case 8 (NewPooledTransactionHashesMsg) IGNORED
  protocol.go                   eth/69 wire types
  downloader.go                 RequestHeaders/RequestBodies — NOT consumed by sync
```

Today's devp2p server is a **read-only stub**:
- Status handshake works (forkID, genesis, latestBlock)
- It can answer GetBlockHeaders / GetBlockBodies (but BlockBodies often
  returns nil because the local store lookup isn't wired up)
- It **discards** NewPooledTransactionHashes (no mempool tie-in)
- It **never consumes** peer-provided headers/bodies — the downloader
  fetches but the result isn't piped into any sync stage

### Engine API (consumer of CL payloads) — already complete

```
internal/ethel/engineapi/service.go   JWT-authed HTTP server (8551 by default)
internal/api/engine_api_v1.go         engine_newPayloadV1/V2/V3, engine_forkchoiceUpdated
internal/ethel/witness.go             ProcessBlock validates + executes payload
internal/api/engine_state_adapter.go  Commits to MDBX
```

This is the **shipping path** for n42 as a post-Merge EL node: a CL
(Caplin embedded in `cmd/eth-el` or external) calls `engine_newPayload`
every slot (~12 s on PoS), N42 validates + executes + commits.

---

## Part 2 — What eth/68 actually requires

eth/68 = devp2p subprotocol version 68, message codes 0x00..0x10:

| Code | Message | eth/68 specifics |
|:-:|:--|:--|
| 0x00 | Status | post-Merge omits totalDifficulty (eth/69 cleaned this up further) |
| 0x01 | NewBlockHashes | block-hash announcements |
| 0x02 | Transactions | full tx bodies for mempool propagation |
| 0x03 / 0x04 | GetBlockHeaders / BlockHeaders | request-response with requestID |
| 0x05 / 0x06 | GetBlockBodies / BlockBodies | request-response by hash list |
| 0x07 | NewBlock | full block push (deprecated post-Merge) |
| **0x08** | **NewPooledTransactionHashes68** | **eth/68 extension: hashes + types + sizes** |
| 0x09 / 0x0a | GetPooledTransactions / PooledTransactions | tx body prefetch |
| 0x0f / 0x10 | GetReceipts / Receipts | block receipt sync |

Reth's encoder:
`crates/net/eth-wire-types/src/broadcast.rs:415-517`
— `NewPooledTransactionHashes68 { types: Vec<u8>, sizes: Vec<usize>, hashes: Vec<B256> }`,
all same length, RLP-encoded with `types` as RLP byte-string (not list).

Erigon's wire:
`p2p/protocols/eth/protocol.go:54-76` — ProtocolLengths{ETH68: 17}.
Routed via `sentryproto.MessageId_NEW_POOLED_TRANSACTION_HASHES_68`.

---

## Part 3 — The 12-second live sync loop

### N42's current loop (with CL)

```
CL (Caplin or external)             N42 EL
─────────────────────                ─────
                          engine_newPayloadV3
                         ─────────────────────►   ProcessBlock (validate + execute)
                                                  EngineStateAdapter.commit() to MDBX
                                                  emit NewChainEvent
                          engine_forkchoiceUpdated
                         ─────────────────────►   set canonical head
                                                  prune any forks not under new head
```

Slot rate (~12 s) is dictated by the CL. The EL is reactive.
Latency budget per slot:

| Phase | typical |
|:--|--:|
| receive + decode payload | < 10 ms |
| EVM execution (1000 tx) | 1–3 s |
| state commit (PlainState writes + trie root) | 1–2 s |
| ForkchoiceUpdated + finalisation | < 100 ms |
| **Total** | **3–5 s** (well inside 12-s budget) |

✓ This works today via `internal/ethel/engineapi`.

### N42 without a CL (standalone EL)

There is currently **no production path**:
- Devp2p stub doesn't consume NewBlock / NewBlockHashes
- libp2p alone won't talk to Ethereum mainline peers (different
  transport, different topics)

So today, N42 is a **CL-dependent post-Merge EL**, exactly like
geth/reth/erigon are when running post-Merge.

---

## Part 4 — The snapshot-distribution pipeline (this session)

Phases E + F + H built **out-of-band** sync: clients download the
state + freezer files from a publisher mirror over file:// or
http(s)://. After a successful bootstrap or delta-apply, the client
has the full archive at some height H and can:

1. **Serve RPC** for blocks ≤ H (the snapshot tier handles current
   state; the history index + freezer handle historical queries)
2. **Continue syncing live** via either the Engine API (if a CL is
   attached) or libp2p (within the N42 chain)

The 12-min sim demonstrates the continuous catch-up case: publisher
extends the archive every 30 s, clients delta-apply within
milliseconds, and the post-sim Verify confirms byte-exact
convergence.

**This pipeline IS NOT a substitute for eth/68 with mainline
ethereum peers.** It's a complementary distribution model:

| Sync model | Used for | Source |
|:--|:--|:--|
| eth/68 devp2p | live blocks from arbitrary peers | reth/erigon/geth/n42 peers |
| Engine API | live blocks from your own CL | Caplin / Lighthouse / etc. |
| libp2p (N42-native) | live blocks within N42 chain | other N42 nodes |
| **Snapshot distribution (this session)** | **fast initial bootstrap + delta-apply** | **publisher mirrors** |

The snapshot pipeline replaces the "download every block from
genesis" workflow, NOT the live-tip ingest.

---

## Part 5 — Decision matrix

Two reasonable end-states:

### Option A — Wire eth/68 (full devp2p compat)

Make N42 a first-class Ethereum EL peer.

**Work:**
1. Implement NewPooledTransactionHashes68 decoder + plumb into txpool
   (`internal/devp2p/eth_handler.go:146`, ~150 LOC)
2. Connect devp2p `downloader.go` to `internal/sync/initialsync/`
   fetcher pipeline (~300 LOC refactor)
3. Implement BlockBodies / Receipts response producers (currently
   return nil) — ~200 LOC
4. Test against reth+erigon+geth testnet pods

**Benefit:** N42 can sync from any Ethereum mainnet/testnet peer
without snapshot dependency. Useful for mainline interop, bridge
relayers, light verifier nodes.

**Cost:** 1–2 weeks engineering + ongoing protocol-version
maintenance (eth/69 was 2024, eth/70 / eth/71 are landing).

### Option B — CL + snapshot only (current state, just documented)

Treat N42 as a snapshot-distribution post-Merge EL that requires a
CL for live blocks. Devp2p stays a stub for the few protocols where
n42 needs to look like an Ethereum peer (DiscoveryV5 announcements,
etc.) but never participates in block sync.

**Work:**
- Document explicitly that N42's catch-up path is:
  1. snapshot fetch (Phase F)
  2. delta-apply until current (Phase H)
  3. Engine API for live blocks
- Add a `cmd/eth-el --no-devp2p-sync` flag for clarity
- Optionally: hide the devp2p stub behind a feature flag so
  it doesn't advertise on the network

**Benefit:** Simpler architecture, fewer surfaces to maintain.
Matches the "modular CL+EL" model that mature post-Merge nodes use
anyway.

**Cost:** Low — mostly documentation + a CLI flag.

---

## Part 6 — Recommendation

**Default: Option B (snapshot + CL).**

Justification:
- The snapshot-distribution pipeline already provides faster initial
  sync than eth/68 download for any practical scale (a 849 GB
  archive takes 1–2 h to fetch, eth/68 would take days)
- The Engine API + CL path is the mainline path for ALL post-Merge
  EL nodes — N42 already has it
- The 12-min sim demonstrates the snapshot pipeline holds tip
  convergence with 36 KB/12-min steady-state delta cost (negligible)
- Adding eth/68 doubles the wire-protocol surface to maintain for
  modest marginal benefit (mainline EL interop)

**Re-evaluate Option A if:**
- Someone wants a mainline-ETH-interop bridge / relayer running on
  N42 code
- A specific application needs eth/68 mempool integration (e.g., a
  MEV searcher that wants tx announcements before they're mined)

---

## Companion docs

- `docs/ethel/n42-eth-client-distribution.md` — Phases E/F/H spec
- `docs/ethel/n42-eth-distribution-test-plan.md` — the sim + IT-1..IT-7
- `docs/ethel/devlog-eth-el-node.md` — `cmd/eth-el` architecture
- `internal/devp2p/eth_handler.go` — eth/69 stub (today)
- `internal/sync/initialsync/service.go` — catch-up FSM
- `internal/ethel/engineapi/service.go` — Engine API consumer
