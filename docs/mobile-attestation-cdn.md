# Mobile Attestation — Distribution Deployment

Phase 5 of `docs/mobile-attestation-design.md`. Two transports, one
data plane. Both serve the identical, content-addressed StreamPacket;
the phone verifies against the committed header regardless of how the
bytes arrived (design §8.3), so distribution is untrusted plumbing.

## Why a distribution layer at all

Direct serving from an IDC origin is egress-bound at ~10⁷ registered
devices per node (`docs/mobile-attestation-design.md` §10). The 10⁸+
target needs the packet bytes to fan out through infrastructure that
does not cost the origin one upload per phone. Two forms, deployable
independently or together.

## Form A — CDN in front of the packet endpoint (ship first)

Every IDC node already exposes:

```
GET /mobileverify/packet/{blockHash}
```

Packets are immutable and content-addressed by block hash — the handler
sets `Cache-Control: public, max-age=31536000, immutable`. That is
everything a CDN needs.

Recipe:

1. Enable the HTTP surface on each IDC node:
   ```yaml
   mobile_verify:
     enabled: true
     http_addr: "0.0.0.0:8555"
     packet_window: 256
   ```
2. Put a CDN (CloudFront / Cloudflare / Fastly / any origin-pull CDN)
   in front of the fleet with the origin group = the IDC nodes'
   `:8555`. Cache key = full path (the block hash is the cache key).
3. Cache policy:
   - `/mobileverify/packet/*` — **cache** (respect origin headers; they
     say immutable).
   - `/mobileverify/register`, `/receipt`, `/cert/*`, `/magnet/*`,
     `/health` — **bypass** (dynamic; never cache).
4. Point the mobile client's `baseURL` at the CDN hostname. Registration
   and receipt POSTs pass through to a healthy origin; packet GETs are
   almost always edge hits.

Effect: the origin serves each block's packet to the CDN a handful of
times (once per edge PoP on cache miss), not once per phone. Origin
egress becomes independent of the device count; the binding cost moves
to receipt intake (signature-verify, shardable) and never to packet
egress.

Origins stay interchangeable: any IDC node holds the same rolling
window (packets arrive by gossip), so the CDN origin group needs no
affinity — a cache miss can pull from any node that has the block.

## Form B — BitTorrent swarm (target form)

Enable seeding on the IDC nodes (reuses the existing torrent client, the
same one eth-el cold-segment distribution uses):

```yaml
mobile_verify:
  enabled: true
  torrent_enabled: true
torrent_dist:
  enabled: true
  listen_addr: "0.0.0.0:42069"
  enable_dht: true
  piece_size: 262144
```

Each cached packet (produced locally or received by gossip) is seeded
best-effort; a phone resolves the swarm URI with:

```
GET /mobileverify/magnet/{blockHash}   ->   {"magnet": "magnet:?xt=urn:btih:..."}
```

then joins the swarm out of band and pulls the packet from whichever
IDC/edge peers are closest. The origin's upload cost is amortised across
the swarm; peer count scales the available bandwidth up, not down.

The `MobileVerifyClient.FetchMagnet` SDK call resolves the URI; the
phone's own BitTorrent stack (or a future embedded light client — the
App-side decision noted in the design roadmap) does the transfer. The
decoded packet is verified identically to the HTTP path.

## Choosing / combining

- **CDN only**: simplest, no app-side P2P stack, gets to ~10⁸ if the CDN
  bill is acceptable. The recommended first deployment.
- **Swarm only**: no CDN bill, but needs a BitTorrent stack in the app
  and healthy peer density.
- **Both**: `FetchMagnet` first, fall back to the CDN `packet` endpoint
  when the swarm is cold (early blocks, sparse peers) — belt and
  suspenders, and the natural end state.

## What does NOT go through distribution

Registration, receipt submission, and certificate/status queries are
dynamic per-request and small; they hit an IDC node directly (or through
the CDN as pass-through). Only the packet — the large, identical,
cacheable object — uses the distribution layer.
