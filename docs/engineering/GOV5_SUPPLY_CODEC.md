# Isolated Go signed-corpus transport

Status: targeted tests, race, the clean Make build and preparation of 192M signed
transactions have passed. No 6M/s qualification or chain TPS result has been
obtained. Runtime transaction admission, consensus, execution and signature
policies remain unchanged.

The four codec tests passed on 2026-10-15 with `GOMAXPROCS=2 go test -p 1
./internal/sharedexec -run '^TestCorpus' -count=1` (package 0.014s). The BOX
claim was released after that test turn. All nine CLI tests also passed with
`GOMAXPROCS=2 go test -p 1 ./cmd/gov5-replay -run '^TestSupply' -count=1
-timeout=90s` (package 0.055s), followed by targeted `go vet`. These small tests
used low scheduling priority and two Go workers; no fleet or corpus throughput
test ran concurrently. Tests include actual Unix socket exchange, bounded global
credits, malformed/unknown ACKs, manifest/epoch binding, duplicate/conflicting
frames, final stream integrity, and 15 genuinely signed transactions prepared
and reloaded from a fresh temporary genesis/shards. Unit-test times are not
throughput measurements.

A subsequent regression expanded the CLI suite to ten tests (0.057s; targeted
vet passed). Receiver completion time also participates in the exhaustion gate,
so a sampler pause after reading a partial count cannot hide corpus exhaustion
before its eventual end timestamp. Window timestamps conservatively surround
counter reads, and cancellation preserves the original error before closing
the client's socket.

## First full-size preparation evidence

On 2026-10-15 the ten CLI tests passed under `GOMAXPROCS=2 go test -race -p 1
./cmd/gov5-replay -run '^TestSupply' -count=1 -timeout=90s` (package 1.659s;
command 26.014s), followed by `GOMAXPROCS=2 make gov5-replay` (2.001s). The
build claim was released before acquiring a separate preparation claim.

The clean source commit was `b0d9bdd5ff17109f450beb13d4dea418e8b40f9d`.
The resulting binary SHA256 is
`3bc57ac0f29ba110ff57001fd01bb13d6ff8bf3af990e95d0dadabb7c3ade597`.
With `GOMAXPROCS=36`, preparation signed and directly recovered all 192,000,000
transactions in 294.123 seconds. The 64 finalized shards contain 384,000 frames,
19,579,435,444 original signed-transaction bytes and 20,379,691,444 total encoded
file bytes. Preparation's sampled process RSS maximum was 1,222,916 KiB; this is
not an aggregate memory limit or a claim of 36 exclusive physical cores.

The immutable manifest SHA256 is
`5fc8e1f94ba95bf3d14c93a10997db70c35305ff8757711dd5d59640da6a5d93`.
Its chain ID is 4205001 and genesis hash is
`0xb58a081ecf8b349f17fdd75bae27f23e34987fc2c5c89b51ef7ad8701525eeef`.
Evidence is retained under
`/data/blockchain/gov5-work/validation/20261015-gov5-supply-b0d9bdd5/` and
`/data/blockchain/gov5-performance-5m/supply/s192a/prepare-cost.json`.
The preparation claim was released; qualification must acquire a new BOX turn.
Offline verification does not count as online verification or canonical execution:
both reported counters remain zero. No receiver transport rate has been measured
by this preparation result.

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
