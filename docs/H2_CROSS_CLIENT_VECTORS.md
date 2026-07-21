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

