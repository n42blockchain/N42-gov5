# Test coverage campaign

Started 2026-10-02 (America/New_York). Branch `test/coverage-70`; per-agent branches `test/coverage-70-g<N>` merge into it.

## Baseline (2026-10-02 00:37 EDT)

`go test -short -cover -covermode=atomic ./...` with tags `nosqlite,noboltdb`, Go 1.26:

| metric | value |
|---|---|
| packages | 608 (579 instrumented) |
| statements | 211,489 |
| covered | 78,696 |
| **coverage** | **37.2%** |
| packages without any test file | 366 |

Target 70% = 148,042 covered statements, i.e. +69,346. Ranking by uncovered statements is in
`scratch/cov/rank.txt` (not committed); top of the list: internal/ethel 5,912, internal/api 3,886,
internal 3,043, internal/datc 2,924, internal/vm 2,595, cmd/n42 2,583, lib/state 2,544, internal/sync 2,266,
modules/state 2,195, generated gRPC stubs lib/gointerfaces/* 5,631 (0-1%), proto/*_pb 2,326.

Denominator notes: generated code (`lib/gointerfaces/*`, `proto/*_pb`, `*_gen.go`, `*.pb.go`) and vendored
post-quantum crypto internals (`crypto/dilithium/*/internal`, `crypto/kyber/*`, `crypto/csidh`) are ~14k statements
that no hand-written test should chase; `cmd/*` mains are ~7k. Both views (with and without them) are reported at
each checkpoint.

## Rules

- One commit per test file or small package; package tests pass and `go vet` is clean before each commit.
- English commit messages, no "claude" anywhere, no trailers.
- Test-only changes; untestable functions are listed, not patched.
- Tests run with `nice -n 19 -p 1 -short`, one package at a time: the box also runs the qs benchmark fleet.

## Wave 1 (dispatched 2026-10-02 01:05 EDT)

| group | packages |
|---|---|
| g1 | internal/api, modules/rpc/jsonrpc, internal/mcp |
| g2 | internal/vm, lib/trie, lib/rlp2, accounts/abi |
| g3 | modules/state, lib/state, modules/rawdb |
| g4 | internal/consensus/hotstuff, internal/txspool, lib/txpool, internal/cscompact |

## Wave 1 results

| group | package | before | after |
|---|---|---|---|
| g4 | internal/cscompact | 19.0 | 35.8 |
| g4 | internal/txspool | 44.3 | 57.7 |
| g4 | lib/txpool | 44.6 | 48.3 |
| g4 | internal/consensus/hotstuff | 62.0 | 64.3 |
| g2 | lib/rlp2 | 16.5 | 74.1 |
| g2 | accounts/abi | 30.8 | 59.6 |
| g2 | lib/trie | 46.7 | 56.8 |
| g2 | internal/vm | 50.0 | 50.5 |

Lesson: pure-helper tests exhaust quickly; the remaining mass (vm opcodes/precompiles, trie hashing, pool main loops,
hotstuff service) needs tests built on the packages' existing harnesses. Wave 2 assigns one or two packages per agent
with that instruction.

## Found while testing (not fixed; test-only campaign)

- `lib/rlp2/encodel.go` `EncodeString`: a 56-byte string takes the short-string branch (`> 56` instead of `>= 56`),
  emitting a non-canonical `0xB8` header; `String()` then rejects it.
- `lib/rlp2/encoder.go` `writeList`: long-list header (payload > 55 bytes) writes `0x00` as the length byte
  (`f8 00` for a 60-byte item instead of `f8 3f`); corrupt RLP. Reproduce:
  `NewEncoder(nil).List(func(i *Encoder) *Encoder { return i.Str(bytes.Repeat([]byte{1}, 60)) })`.
- `lib/rlp2/commitment.go` `EncodeByteArrayAsRlp`: for a single byte >= 0x80, `generateRlpPrefixLen(1)` returns 0 but
  one prefix byte is written, so the returned length undercounts by 1.
- `internal/cscompact/history_analysis.go` `ParseErigonBitmapValue`: a malformed 16-byte buffer makes
  `roaring64.Bitmap.UnmarshalBinary` panic (`makeslice: len out of range`) instead of falling through to the
  size-estimate fallback.

| g1 | internal/mcp | 6.5 | 75.7 |
| g1 | modules/rpc/jsonrpc | 5.8 | 51.8 |
| g1 | internal/api | 43.6 | 43.8 |
| g3 | modules/rawdb | 39.7 | 63.5 |
| g3 | modules/state | 55.3 | 57.2 |
| g3 | lib/state | 49.2 | 49.2 (aggregator needs a wired multi-domain setup) |

- `modules/rpc/jsonrpc/util.go` `UnmarshalText(h types.Hash, ...)` takes the hash BY VALUE, so
  `BlockNumberOrHash.UnmarshalJSON` returns nil error and a zero hash for every `blockHash` argument: any RPC
  call that selects a block by hash through this type silently resolves to the zero hash. Likely a real bug;
  the test documents the current behaviour (TestBlockNumberOrHashUnmarshalJSON).
