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
