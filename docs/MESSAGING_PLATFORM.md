# Messaging Platform

Moved out of CLAUDE.md on 2026-10-08. Six-layer decentralized messaging stack under internal/distributed/messaging/.

## Tests

```bash
# Messaging subsystem tests (92 tests, all race-safe)
go test ./internal/distributed/messaging/...            # All messaging packages
go test -race ./internal/distributed/messaging/...      # With race detector
go test ./internal/distributed/messaging/ -run "TestRelay|TestProtocol"  # P2P relay
go test ./internal/distributed/messaging/crypto/ -v     # E2E encryption
go test ./internal/distributed/messaging/rln/ -v        # RLN anti-spam
go test ./internal/distributed/messaging/store/ -v      # Persistent storage
go test ./internal/distributed/messaging/group/ -v      # MLS group encryption
go test ./internal/distributed/messaging/stream/ -v     # SSE streaming
go test ./internal/distributed/messaging/identity/ -v   # DID identity
```

### Messaging Platform

`internal/distributed/messaging/` provides a full decentralized communications stack, built in 6 layers:

**Layer 1 — P2P Message Relay** (`protocol.go`, `relay.go`, `peer_handler.go`):
- `Envelope` wire format: version + sender (compressed secp256k1 pubkey) + topic + payload + timestamp + nonce + signature.
- `Relay` bridges the local `Service` ↔ libp2p GossipSub. Messages are sharded across 8 topics (`/n42/msg/shard/0..7`) for load balancing (configurable via `MessageShards`).
- Ring-buffer dedup cache (default 65536 entries) prevents message re-processing.
- `PeerHandler` implements the `/n42/msg/store_query/1.0.0` stream protocol for peer-to-peer historical message queries.
- P2P topics registered in `internal/p2p/topics.go`; scoring params in `gossip_scoring_params.go` (TopicWeight 0.1, non-critical); subscription filter in `pubsub_filter.go` allows `/n42/msg/` prefix.

**Layer 2 — E2E Encryption** (`crypto/`):
- `MessagingKeyPair`: X25519 keys. `DeriveFromWallet()` deterministically derives from secp256k1 wallet key via HKDF-SHA256.
- `SealEnvelope`/`OpenEnvelope`: ephemeral X25519 ECDH → HKDF → XChaCha20-Poly1305 (24-byte nonce, immune to nonce reuse).
- `Session`: bilateral encrypted channel with chain key ratcheting. Each message derives a unique key from `chainKey + counter`, then ratchets the chain key forward. Provides forward secrecy. Export/import for session persistence.
- Crypto stack: pure Go via `golang.org/x/crypto` (curve25519, chacha20poly1305, hkdf). No CGO dependency.

**Layer 3 — RLN Anti-Spam** (`rln/`):
- `MembershipTree`: Poseidon Merkle tree (depth 20 by default, ~1M members). Precomputed empty hashes for O(1) lookup.
- `GenerateProof`: for each message, produces a Shamir share `y = secret + slope * x mod p` (BN254 scalar field, `slope = Poseidon(secret, epoch)`, `x = Poseidon(epoch, messageHash)`). Same identity sending 2 messages in same epoch → 2 shares on one line → Shamir recovery of identity secret → slash.
- `NullifierRegistry`: tracks seen nullifiers per epoch. Detects duplicate nullifiers as spam.
- `GossipSubValidator`: returns Accept/Reject/Ignore. Rejects future epochs, ignores stale epochs, rejects invalid proofs, rejects spam (with secret recovery).

**Layer 4 — Persistent Storage** (`store/`):
- `PersistentStore`: CAS-backed message persistence via `CASBackend` interface. In-memory index sorted by topic + timestamp.
- `QueryEngine`: structured queries with filters (topic, time range, sender) and cursor-based pagination. Max 1000 results per query.
- `SyncProtocol`: advertise local availability ranges, compute missing ranges vs peer, export entries for sync.

**Layer 5 — MLS Group Encryption** (`group/`):
- `GroupSession`: manages group state (epoch, ratchet tree, members). Encrypt/Decrypt uses XChaCha20-Poly1305 with AAD = groupID + epoch.
- `RatchetTree`: binary tree with O(log n) `UpdatePath` for forward secrecy after member changes. `SecretTree` derives per-sender secrets.
- `KeyPackage`: MLS cipher suite `MLS_128_HPKEX25519_CHACHA20POLY1305_SHA256_Ed25519` (0x0003), 30-day lifetime.
- `Welcome` + `Commit` protocol for add/remove/update operations.

**Layer 6 — Stream API & DID Identity** (`stream/`, `identity/`):
- `StreamServer`: SSE (Server-Sent Events) server on configurable port (default 8554). Endpoints: `/ws/messages?topic=X` (subscribe), `/health`. Clients receive JSON notifications. Pre-populated topic map before client registration eliminates race conditions.
- `DIDDocument`: W3C DID v1.1 with `did:n42:<address>` method. `CreateDID()` from wallet key, `VerifyDIDSignature()` against document verification methods.
- `DIDResolver`: LRU-cached resolver (default 1024 entries, 1h TTL). Register/Resolve/Update/Deactivate lifecycle.

**Configuration** (`conf/messaging_config.go` — `MessagingCfg`):

| Field | Default | Description |
|-------|---------|-------------|
| `Enabled` | `false` | Master switch for messaging service |
| `P2PRelayEnabled` | `false` | Enable GossipSub relay bridge |
| `MessageShards` | `8` | Number of GossipSub shard topics |
| `MaxEnvelopeSize` | `262144` | Max envelope size in bytes (256 KiB) |
| `DeduplicateCacheSize` | `65536` | Dedup ring buffer size |
| `MaxMessageSize` | `65536` | Max payload size (64 KiB) |
| `StoreCapacity` | `10000` | In-memory LRU store capacity |
| `StoreTTLSec` | `3600` | In-memory message TTL (1h) |
| `StoreQueryEnabled` | `false` | Enable peer store query protocol |
| `EncryptionEnabled` | `false` | Enable E2E encryption |
| `KeyRotationIntervalSec` | `86400` | Key rotation interval (24h) |
| `RLNEnabled` | `false` | Enable RLN anti-spam |
| `RLNRateLimit` | `10` | Local rate limit (msgs/min) |
| `RLNEpochSec` | `10` | RLN epoch duration |
| `RLNMessageLimit` | `1` | Messages per epoch per identity |
| `RLNMerkleDepth` | `20` | Membership tree depth (~1M) |
| `PersistenceEnabled` | `false` | Enable CAS persistence |
| `PersistenceMaxAgeSec` | `86400` | Persistence max age (24h) |
| `PersistenceMaxSizeMB` | `256` | Persistence max size |
| `GroupsEnabled` | `false` | Enable MLS group sessions |
| `MaxGroupSize` | `256` | Max members per group |
| `MaxGroupsPerNode` | `100` | Max groups per node |
| `StreamServerEnabled` | `false` | Enable SSE streaming server |
| `StreamServerPort` | `8554` | SSE server port |
| `DIDEnabled` | `false` | Enable DID identity |

**Node integration** (`internal/node/node.go`):
- `messagingService` field (line 162), created at line 1008-1012 when `MessagingCfg.Enabled` is true.
- Relay is injected via `Service.SetRelay()` after P2P service is available.
- Stopped in the "Distributed services" shutdown phase (line 1337-1338).

**P2P integration** (`internal/p2p/`):
- `topics.go`: `GossipMessagePrefix = "message/shard/"`, `GossipMessageFormat = "/n42/msg/shard/%d"`, `StoreQueryProtocol`.
- `gossip_scoring_params.go`: `messagingTopicParams()` — lightweight scoring (TopicWeight 0.1), injected via `topicScoreParams()` switch.
- `pubsub_filter.go`: `CanSubscribe()` allows topics with `/n42/msg/` prefix.
- `P2PPublisher` interface in `relay.go` matches `p2p.Service` methods (`PublishToTopic`, `SubscribeToTopic`).

