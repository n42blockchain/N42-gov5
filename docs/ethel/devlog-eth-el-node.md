# eth-el node — development log

**Scope**: design and implement a self-contained Ethereum execution-layer
node for N42, including an optional embedded Caplin consensus layer
and a unified multi-transport file-fetching layer shared by the
bootstrap and catch-up services.

**Status**: shipped. All binaries build on default and `-tags n42el`.
All new unit, integration, and race tests pass. The native `cmd/n42`
path is untouched (zero `internal/cl` dependencies).

---

## Binaries after this change

| Binary | Purpose | `internal/cl` deps |
|---|---|---|
| `cmd/n42` | N42 native chain (HotStuff / apoa / apos) | **0** |
| `cmd/ethexec` | Stage-validation test program — re-execute ETH mainnet from Geth ancient data, produce leaves + witness | **0** |
| `cmd/eth-el` | **NEW** — Ethereum execution-layer node | 0 default; 25 with `-tags n42el` |

The first scope mistake of this project was that Caplin had been wired
into `cmd/ethexec`. `cmd/ethexec` is a developer tool, not a
deployable node; the Caplin wiring moved to `cmd/eth-el` in one
commit and `cmd/ethexec/main.go` is back to its pre-Caplin shape.

## Node architecture

`internal/ethel.Node` is a pure orchestrator:

1. `openStorage` — chaindata MDBX + output + input freezer
2. Factory-registered services, run in registration order:
   - `bootstrap` — one-shot: leaves manifest → fetch → `RebuildState`
   - `catchup` — one-shot: segments manifest → fetch → `ethel.CatchUp` (executor replay)
   - `engineAPI` — HTTP + JWT + `EngineStateAdapter`
   - `caplin` (optional, `n42el` tag) — embedded CL sidecar
3. `live` — sentinel, blocks on ctx.Done
4. Reverse-order `Stop` on SIGINT / second SIGINT forces exit

The factory pattern (`ServiceFactory`) exists because `internal/api`
already imports `internal/ethel` for `ProcessBlock` / `Reorg`, so a
service that itself imports `internal/api` (bootstrap, engineAPI)
cannot live inside `internal/ethel` without creating a cycle.
Factories run inside `Node.Start` **after** `openStorage` so they see
valid `Node.DB()` / `Node.OutFreezer()` handles.

## Fetcher layer

`internal/ethel/fetch/` is the unified download abstraction. Core
types:

```go
type Asset struct {
    Name, SizeBytes, SHA256
    Sources []Source
}
type Source struct {
    Kind     SourceKind // https, http, bt, bt-v2, webrtc
    URI      string
    Priority int
}
type Fetcher interface {
    Kinds() []SourceKind
    Fetch(ctx, asset, dstDir, progress) error
}
```

`MultiSourceFetcher` composes per-transport implementations and
dispatches each Asset to the highest-priority Source whose transport
is registered. Failure on one Source falls through to the next;
`context.Canceled` aborts immediately.

### Implementations

| Implementation | File | Notes |
|---|---|---|
| `HTTPFetcher` | `http_fetcher.go` | HTTPS only by default; AllowPlaintext opt-in; Range resume via `.part` file; streaming SHA256; atomic rename on commit; 3-attempt retry budget per Source |
| `TorrentFetcher` | `torrent_fetcher.go` | Wraps `internal/distributed/storage/torrent.Client`; BT v1 and **BT v2** (BEP 52) magnets; WebSeed (BEP 19) free from anacrolix; single-file torrents only |
| `WebRTCFetcher` | `webrtc_fetcher.go` | Variant B: direct pion DataChannel, not WebTorrent. HTTPS signaling endpoint; non-trickle ICE; default STUN via `stun.l.google.com`; streaming SHA256; atomic commit |

Manifest format is JSON over HTTPS (or file://), defined in
`manifest.go`. Two envelopes:

- `ManifestKindLeaves` — consumed by `bootstrap`
- `ManifestKindSegments` — consumed by `catchup`

The same `Asset` shape carries every transport's coordinates in a
`sources[]` array per asset. A manifest publisher advertises HTTPS
mirrors, BT magnets, and WebRTC signaling URLs side by side; runtime
picks whichever transport the local node has enabled and the swarm
can serve.

## Engine API wiring (CL seam)

The CL seam is intentionally minimal:

`internal/api/EngineStateAdapter` already existed in N42 with
`ExecutePayload` and `ForkchoiceUpdated` bodies that bypass
`common.IBlockChain` entirely and read/write chaindata directly.
`internal/ethel/engineapi.Service` wraps it:

1. `api.NewAPI(nil, db, engine, nil, nil, chainCfg)` — nil BlockChain,
   nil txpool, nil accounts — safe because `EngineAPIV1` only derefs
   them through nil-safe helpers once `stateAdapter` is set.
2. `api.NewEngineStateAdapter(db, freezer, chainCfg, engine)` is
   attached via `EngineAPIV1.SetStateAdapter`.
3. `jsonrpc.Server.RegisterName("engine", ...)` × 3 namespaces.
4. `http.ServeMux` with a `jwtHandler` wrapper at root; `ReadTimeout`
   30s, `WriteTimeout` 30s, `IdleTimeout` 120s.

### Fixes to `internal/api` required for the nil-bc path

| Symbol | Fix |
|---|---|
| `EngineAPIV1.currentHead()` | Added `stateAdapter.CurrentHead()` fallback |
| `EngineAPIV1.currentHeadHash()` | Added `stateAdapter.CurrentHeadHash()` fallback |
| `EngineAPIV1.parentHeader()` | Added `stateAdapter.HeaderByHash()` fallback |
| `EngineStateAdapter` | New methods `CurrentHead()`, `CurrentHeadHash()`, `HeaderByHash()` reading chaindata via `rawdb.ReadHeadBlockHash` + `ReadHeaderNumber` + `ReadBlockByHash` / `ReadHeaderByHash` |

Without these fallbacks, `forkchoiceUpdatedV4` would have always
returned `SYNCING` in cmd/eth-el because the nil-bc check at the top
of `currentHead()` short-circuits. Now `SYNCING` is only returned when
chaindata genuinely has no head — which is the spec-correct behavior.

## Caplin integration (optional, n42el tag)

`internal/cl/` holds the N42 fork of Erigon's Caplin. The subtree is
gated behind the `n42el` build tag so the native `cmd/n42` binary
links none of it.

`internal/cl/eladapter/` is the seam: it implements the 14-method
`execution_client.ExecutionEngine` interface that Caplin drives
internally. A compile-time assertion
(`var _ execution_client.ExecutionEngine = (*Adapter)(nil)`) breaks
the n42el build immediately if Caplin upstream adds a new method, so
the seam cannot silently go partial.

Caplin in the eth-el binary runs as a sidecar: `cmd/eth-el` owns the
`*cl.Service` via `beacon_wire.go` (gated) and `beacon_wire_stub.go`
(default). The Caplin Backend is fed the eth-el chaindata handle
through a small `chaindbProvider` interface so tests can inject an
in-memory MDBX.

Three Backend methods are wired to real chaindata
(`CurrentHeadNumber`, `HasBlock`, `IsCanonicalHash`, `Ready`). The
rest of the ExecutionEngine methods are stubs that return
`eladapter.ErrNotImplemented` — the stub list is documented in
`internal/cl/eladapter/PHASE6_NOTES.md` along with the three
architectural paths forward (spawn minimal Node / add executor
payload-execute mode / run Caplin out-of-process).

## Transport upgrades delivered in this commit

### BT v2 (was evaluation, became real)

`BT_V2_NOTES.md` pre-flight check revealed `lib/downloader` + `lib/chain/snapcfg`
had **zero callers outside themselves**. Execution:

1. `rm -rf lib/downloader lib/chain/snapcfg`
2. Removed the `replace github.com/anacrolix/torrent => github.com/erigontech/torrent v1.54.2-alpha-8`
   from `go.mod`
3. `go mod tidy` → upstream `anacrolix/torrent v1.61.0` + automatic
   pion upgrade
4. One line of API drift fixed in `internal/distributed/storage/torrent/client.go`
   (`t.Complete.On()` → `t.Complete().On()` — v1.61 changed the field
   to a method)
5. Added `SourceBTV2` to `TorrentFetcher.Kinds()`
6. New test `TestTorrentFetcher_AcceptsBTV2` pins the dispatch guard

### WebRTC DataChannel fetcher

Chose variant B (direct DataChannel) over variant A (WebTorrent
bridge). Variant A is 2–3 weeks of protocol work; variant B solved
the "CDN blocked, BT blocked, WebRTC punches through" use case in
300 lines.

**End-to-end test** uses pion on both sides: the test stands up a
`webrtcSender` helper that receives an SDP offer over HTTPS, replies
with an answer, and streams the payload through the DataChannel. The
real pion stack is exercised, not a mock. Transfers a 4 KiB payload
through a real PeerConnection, verifies SHA256 on receive, confirms
the `.part` file is renamed atomically.

## Tests delivered

| Package | Tests | Notes |
|---|---|---|
| `internal/ethel/fetch` | **46** | 8 asset/multi + 7 http + 6 torrent (inc BT v2) + 4 webrtc + 21 manifest |
| `internal/ethel/bootstrap` | 7 | disabled / populated / local / no-fetcher / manifest happy path / wrong kind / fetch-failure-aborts |
| `internal/ethel/catchup` | 6 | no-manifest executor / no-fetcher / happy path / wrong kind / fetch-fail aborts / executor error wrapped |
| `internal/ethel/engineapi` | 2 | unauth 403 / authed 200 |
| `cmd/eth-el` | 8 | backend 1 + flags/lifecycle 2 + CL integration 5 |
| `internal/api` | (existing, still passing) | `EngineStateAdapter` path |

All tested with `-race`.

CL integration tests (all 5 pass against a real eth-el Node):

- `TestCL_ExchangeCapabilities` — JSON-RPC capability handshake
- `TestCL_ForkchoiceUpdatedV4EmptyState` — SYNCING on empty chain (spec-correct)
- `TestCL_NewPayloadV4MissingFields` — INVALID on malformed payload
- `TestCL_RejectsUnauthenticated` — 403 without JWT
- `TestCL_StaleJWTRejected` — 403 with iat -10min (§3.1 drift window)

## Known gaps (documented, not bugs)

1. **`newPayloadV4` happy path** — no integration test drives a real
   mainnet block through `EngineStateAdapter.ExecutePayload` yet.
   `internal/api` has existing block fixtures that should be reused
   for this; it is the highest-value follow-up for confidence that
   real CL traffic produces VALID responses.

2. **`internal/ethel` package's pre-existing tests** — several
   `TestDecodeGeth*` tests require real Geth ancient data on disk
   (path `e:\geth\...`) and fail in any environment without it.
   These failures existed on `origin/main` before this work; they
   are environmental, not regressions.

3. **Manifest publishing tooling** — `cmd/ethexec` writes leaves +
   witness but does not produce a JSON manifest for distribution. A
   small publish script is needed as part of the operator toolkit.

4. **Real Caplin stage loop** — the eladapter's
   `NewPayload`/`ForkChoiceUpdate`/`InsertBlock(s)` still return
   `ErrNotImplemented`. Running eth-el with `--caplin.enabled` today
   brings Caplin up as a sidecar but Caplin cannot push blocks into
   the EL yet. See `internal/cl/eladapter/PHASE6_NOTES.md` for the
   three paths forward.

5. **BT v2 real-world test** — the upgrade is in but no test
   actually downloads through a v2 swarm. Needs published v2-only
   magnets in a test manifest to prove.

6. **WebRTC production signaling** — the default STUN server is
   Google public (`stun.l.google.com`). Production deployments should
   configure `WebRTCFetcherOptions.ICEServers` with operator-hosted
   TURN for reliable NAT traversal.

## Code footprint

New packages:

```
internal/ethel/fetch/        ~1600 lines incl. tests
internal/ethel/bootstrap/     ~400 lines incl. tests
internal/ethel/catchup/       ~400 lines incl. tests
internal/ethel/engineapi/     ~400 lines incl. tests
internal/cl/                 ~30k lines (Caplin fork, n42el gated)
cmd/eth-el/                   ~900 lines incl. tests
```

Deleted:

```
lib/downloader/              ~3500 lines (erigon snapshot downloader, unused)
lib/chain/snapcfg/            ~400 lines (only used by lib/downloader)
```

Net: ~30k lines of gated Caplin + ~4k lines of production eth-el
infrastructure; ~4k lines of dead erigon tree removed.

## How to run eth-el

```bash
# Default build, no embedded CL (use external Lighthouse/Prysm)
go build ./cmd/eth-el
./eth-el --datadir /var/lib/eth-el \
         --network mainnet \
         --bootstrap.enabled --bootstrap.manifest https://cdn.example/mainnet-leaves.json \
         --catchup.manifest   https://cdn.example/mainnet-segments.json \
         --engine.enabled --engine.jwt /etc/eth-el/jwtsecret \
         --torrent.enabled --torrent.dht --torrent.pex \
         --webrtc.enabled

# n42el build — embedded Caplin sidecar
go build -tags n42el ./cmd/eth-el
./eth-el --datadir /var/lib/eth-el \
         --caplin.enabled --caplin.network mainnet \
         --caplin.checkpoint.url https://checkpoint-sync.mainnet.example \
         # ... plus all flags from above
```

Two SIGINTs for force-exit; the first walks `node.Stop()` in reverse
service order.
