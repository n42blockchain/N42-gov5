# WebRTC fetcher — evaluation & design

Reference for Step 9. Like `BT_V2_NOTES.md`, this file documents what a
WebRTC transport would look like, what it takes to implement, and why
we are reserving the `SourceWebRTC` constant without shipping a real
implementation yet.

## What "WebRTC fetcher" could mean

The name is ambiguous. There are two distinct projects people reach
for when they say "WebRTC download":

### A. WebTorrent (protocol bridge on top of BitTorrent)

WebTorrent is a WebRTC DataChannel transport for the BitTorrent wire
protocol, designed so browsers can join the same swarm as native BT
clients. Adding it to eth-el would mean:

- Embedding a WebTorrent tracker client (unique URL scheme:
  `wss://tracker.example/announce`)
- Opening a PeerConnection + DataChannel per peer discovered through
  the tracker
- Running the BitTorrent wire protocol (handshake, bitfield,
  request/piece/have) over the DataChannel framing

anacrolix/torrent **does not** speak WebTorrent. The Go ecosystem
does not currently have a production-grade WebTorrent client. The
closest is `pion/webrtc/v4` plus a hand-rolled bridge, which would be
a substantial project (estimated 2000+ lines, weeks of work).

### B. Direct WebRTC data-channel fetch

A much simpler shape: eth-el POSTs an SDP offer to a signaling URL
(`https://signal.example/asset/<name>`), receives an SDP answer,
opens a DataChannel, and reads the raw file bytes until EOF. The
sender is a dedicated origin service — this is WebRTC used as a
firewall-friendlier alternative to HTTPS, not as a peer mesh.

Benefits over plain HTTPS:

- Punches through symmetric NAT where HTTPS cannot (rare for servers,
  common for peers)
- Interoperates with browser-based seeders that cannot open raw TCP
- Offers end-to-end DTLS without requiring a public CA on the origin

Drawbacks:

- Needs a signaling server operated by us (or chosen partners). CDN
  providers do not expose WebRTC endpoints.
- Adds `pion/webrtc/v4` as a load-bearing dependency (already present
  as *indirect* via libp2p — see `grep pion go.mod`)
- Single-peer by construction: the fetcher has zero fallback within
  a single Asset source

## Recommendation

**Ship nothing right now. Reserve `SourceWebRTC` for a future
WebTorrent bridge, because that is the variant users will actually
want** — it opens the N42 swarm to browser clients, which is the
strategic reason to care about WebRTC at all.

Variant B (direct DataChannel) is easier to build but solves a
problem we do not have: plain HTTPS already covers every CDN
scenario, and punching through NAT for a batch download is a
marginal improvement over what the BT fetcher does via uTP.

## What we ARE going to ship

`SourceWebRTC` is a reserved `SourceKind` constant in
`internal/ethel/fetch/asset.go`. It is:

- Accepted by `Asset.Validate()` (no-op; validation only checks
  string format for now)
- Visible in JSON manifests as `"kind": "webrtc"`
- **NOT** dispatched by any registered Fetcher in `cmd/eth-el`

If a manifest publishes a `webrtc` source today, the
`MultiSourceFetcher` will log "no fetcher for source kind, skipping"
and fall through to the next Source. This is the correct behavior:
unknown transports degrade gracefully to the transports the runtime
does speak.

## When we DO ship a WebRTCFetcher

Implementation plan:

1. **Decide: WebTorrent or direct DataChannel?** (see above)
2. **Write a `signaling.Client` abstraction** so tests can inject a
   fake signaling endpoint without spinning up a real browser
3. **Implement `fetch.WebRTCFetcher`**:
   - `Kinds() = []SourceKind{SourceWebRTC}`
   - `Fetch` opens `pion.PeerConnection`, exchanges SDP via the
     signaling client, reads the DataChannel into the same
     `.part → rename` commit path HTTPFetcher and TorrentFetcher
     already use
4. **Add `fetch.NewWebRTCFetcher(...)` to `cmd/eth-el/main.go`** under
   a `--webrtc.enabled` flag, same pattern as `--torrent.enabled`
5. **Unit tests**: fake signaling, fake DataChannel sender, cover the
   happy path + the "peer disconnected mid-stream" error case

Realistic effort for variant B: **1–2 days**. For variant A
(WebTorrent): **2–3 weeks**, depending on how polished the bridge
needs to be.

## Pre-flight check (for future work)

```bash
# Confirm pion is still available when the implementation happens:
go list -m github.com/pion/webrtc/v4

# Check whether libp2p still pulls it in indirectly — if not, it
# needs a direct require line and a `go get`:
grep "pion/webrtc" go.mod
```

Current state (2026-04-10): `github.com/pion/webrtc/v4 v4.2.9` is
present as `// indirect` via libp2p.
