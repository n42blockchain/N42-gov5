# Isolated Go signed-corpus transport

Status: implementation under development; no 6M/s qualification or chain TPS
result has been obtained from this code. The runtime transaction admission,
consensus, execution and signature policies remain unchanged.

The four codec tests passed on 2026-10-15 with `GOMAXPROCS=2 go test -p 1
./internal/sharedexec -run '^TestCorpus' -count=1` (package 0.014s). The BOX
claim was released after that test turn. CLI/socket tests and the full-size
qualification remain pending; this codec result is not a throughput result.

## Corpus identity and integrity

The v1 descriptor binds full chain identity (chain ID 4205001, namespace and
genesis hash), exact genesis-file SHA256, signed Ethereum RLP/EIP-155/secp256k1
algorithm identifiers, key/recipient derivation identifiers, seed commitment,
sender/nonce ranges, recipient count, shard count and maximum frame count.
`CorpusID` is SHA256 of a deterministic, domain-separated descriptor encoding.
It intentionally does not contain the final shard hashes: those files embed
CorpusID themselves, so including their hashes would introduce a cycle.

The finalized manifest separately records every shard's SHA256, byte count,
frame count and transaction count. A connection pins SHA256 of the **complete
manifest bytes**, not just CorpusID. Each receiver process also chooses a new
session epoch. Reconnection and unknown-ACK recovery are unsupported; an unknown
outcome stops this session. No transaction is patched, re-signed or replayed in
a loop during measurement.

Each shard owns sender indices `shard + k * shard_count`. It visits these senders
in order for each nonce chunk. A frame contains consecutive signed nonces from
one sender; the last chunk can be shorter. Thus uneven sender/shard division and
nonce tails still cover every sender/nonce pair exactly once. Offline preparation
checks all actual persisted genesis accounts, signs and independently recovers
every encoded transaction before publishing a finalized manifest.

## Wire format

All integer fields are little endian. A GCF1 frame has an 84-byte header:

| Offset | Field |
| --- | --- |
| 0 | 4-byte `GCF1` magic |
| 4 | 32-byte CorpusID |
| 36 | uint16 shard |
| 38 | uint64 sequence |
| 46 | uint16 transaction count |
| 48 | uint32 payload byte count |
| 52 | 32-byte SHA256 of payload |

Payload is exactly `count` entries of uint32 byte length plus complete original
signed transaction bytes. Count is at most 500 and each transaction is at most
2048 bytes. Receivers bound length/count before allocation; trailing entries,
truncation, digest mismatches and unknown identities fail.

A self-consistent frame does **not** prove sender, nonce, signature or membership
in the finalized corpus. Per-frame ACKs are provisional transport consumption.
Final shard ACKs require EOF and exact manifest SHA256/bytes/frames/transactions.
Qualification requires all final shard integrity checks. These checks bind the
stream to the previously generated and offline-verified corpus; they do not
perform online cryptographic transaction verification.

## Resource and measurement boundary

The initial prepare command creates a fresh private corpus under the dedicated
supply root: 6000 senders × 32000 nonces = 192M unique transactions, 64 shards,
and a 22,857-recipient hot set. Generated seeds and seven new validator keys stay
in that private runtime directory and must never be committed. Failed partial
preparation directories are retained. An existing directory is never reset.

Replay uses 64 real Unix socket connections with up to 32 frames each. A global
credit allocator limits unacknowledged frames to both 2M transactions and 512MiB
encoded bytes. Each connection drains ACKs when global credits bind before its
32-frame window fills. Receiver-owned payload buffers have independent global
credits; the sink consumes and discards bodies, retaining only bounded sequence
and digest evidence. Replay uses an 8MiB file buffer per shard. Credit budgets
are not a claim about actual process RSS, kernel socket buffers or page cache;
those must be reported from an actual qualification run.

The default offer is 6.5M/s, with 4 seconds warmup and a measured 20-second
receiver window. Passing requires receiver-confirmed provisional unique delta
divided by actual monotonic elapsed time >=6M/s, followed by successful final
integrity checks, and no corpus exhaustion in the measured window. Offline
signing/full verification cost is separate. Output explicitly reports
`crypto_verified=0`, `on_chain_committed=0` and scope
`raw-signed-wire-transport-only`. This is not chain admission throughput, EVM
execution throughput or committed TPS, and does not qualify a 90-second window.

Preparation, builds and qualification require separate BOX-protocol hardware
claim windows. Existing independent/native/ETH runtime paths are unchanged;
Ed25519, gateway trust, frame-body consensus and deferred execution are not
enabled by this tool. Wiring the single executor's bounded queue and
BuildFromQueue is a later independent change after raw transport qualification.
