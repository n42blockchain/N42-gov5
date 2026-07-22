# H2 cross-client vectors

gov5 is the producer of
`internal/consensus/hotstuff/testdata/cross_client_h2_v1.json`. The fixture
pins the current legacy HotStuff-2 envelope for Proposal, Vote, CommitVote,
PrepareQC, Timeout, NewView, and Decide, including nested QC/TC values,
bitmaps, signing preimages, and a raw-Snappy gossip frame.

Run:

```sh
go test ./internal/consensus/hotstuff
```

The matching n42-26 fixture must have SHA-256
`0c5877432b8d7adb3fc60c5226564ad1d0e099b6c73f39b823703926e82d2aee`.

This is a legacy compatibility fixture, not permission to mix validators.
gov5's Round-2 signature is `commit || view_le || block_hash`; current Rust
also binds validator changes. A versioned common domain is required before a
QC from one implementation can be accepted by the other.

`testdata/h2_v4_domains_v1.json` defines the replacement domain. Every
preimage starts with `N42H2V4`, a phase byte, chain id, genesis hash, and view;
block-bearing phases also bind the block hash, and Proposal/Commit bind the
validator-change hash. This prevents timeout/new-view signatures from being
replayed between chains that reuse validator keys.

`testdata/h2_v4_envelope_v1.json` wraps a canonical H2 payload with the v4
magic, full chain identity, validator-change hash, and an exact payload length.
It also pins gov5's Snappy output. Decoders reject identity mismatch, length
mismatch, trailing bytes, decompression expansion, and non-canonical inner
messages.

## Opt-in production profile

For a new static-validator chain, set `hotstuff.interopV4` to `true` in the
genesis chain configuration on every validator. This switches the production
Proposal, Vote, Commit, Timeout, NewView, QC, TC, and Decide signing and
verification paths to the chain-bound v4 domains. A committed Decide is also
published as a v4 envelope on `/n42/h2/4/ssz_snappy` for n42-26 observers.

When enabled, the service publishes and subscribes to the v4 topic while
retaining the legacy topic during migration. Incoming v4 frames must have the
configured chain identity, canonical encoding, and a zero validator-change
hash before the normal H2 state machine performs leader, signature, QC/TC and
view checks. This is the ingress used by an n42-26 validator.

The option is deliberately off by default. Existing chains that do not opt in
retain their legacy wire bytes and databases without migration. The first
production profile uses a zero validator-change hash and rejects validator
reconfiguration through the admin API; do not configure epoch validator
changes on an H2-v4 chain. Dynamic validator changes require a later protocol
revision in which both clients derive the same `changes_hash`.
