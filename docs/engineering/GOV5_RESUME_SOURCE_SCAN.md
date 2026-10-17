# Original-source mapping for a bounded resume plan

The private `cmd/gov5-replay` scanner authenticates original corpus contents and
recovers public logical-sender mappings. It does not arm an owner, open a retained
chain database, consume an old ACK, or dispatch transactions. The existing
genesis calculation uses a temporary MDBX; this is not a zero-database operation.

Every complete shard must match the finalized manifest SHA256 before mapping
signature recovery starts. The same open file descriptors remain pinned while
the first nonce-round frames establish the complete logical-index-to-address
bijection. Each first transaction uses direct EIP155 recovery and must match the
original protected legacy transfer recipe. Addresses are joined to the complete
address-sorted historical account vector by address, never by vector position.
The recovered set must equal both that vector and the exact genesis allocation.

After choosing the minimum logical index in each nonce bucket 0, 41, 42 and 500,
the scanner rereads the four original `[0,500)` frames from those descriptors.
It checks all 2,000 original signatures, sender/nonce sequences and transfer
fields, including skipped prefixes and the all-skipped frame. No frame is cropped,
resigned or renumbered. Logical byte offsets do not depend on buffered-reader
readahead. Before and after observations compare descriptor and pathname device,
inode, size, ownership, mode, link count, mtime and ctime. Any error returns no
partial mapping and closes the descriptors.

For the real 6,000-sender shape, these are 6,000 mapping signature checks plus
2,000 selected-frame signature checks; four nonce-zero transactions occur in both
stages. Full shard hashing is not a claim to have reverified all 192M signatures.
The ordinary result is not a transferable verification capability. A future owner
must independently repeat source checks and verify its fresh actual canonical
account snapshot before enabling any resume session.

## Actual validation, 2026-10-17 UTC

The fixture has four genuinely signing senders, two shards, 4,000 original signed
transactions and deliberately different logical/address order. Its account vector
is synthetic; it is not evidence of real canonical prefix execution. Positive and
negative cases cover complete-source hashing before recovery, selected late/skip
signature and nonce errors after recomputed hashes, wrong chain/frame identity,
duplicate recovered senders, and source alias/inode/ctime changes.

- `go test -p 1 ./cmd/gov5-replay -run '^TestResumeSource' -count=1 -timeout=90s`
  passed, package 0.823s, in `validation/20261017-gov5-resume-source-v2`.
- `go test -race -p 1 ./cmd/gov5-replay -run '^TestResumeSource' -count=1 -timeout=120s`
  passed, package 2.731s, in `validation/20261017-gov5-resume-source-v3`.
- `go vet -p 1 ./cmd/gov5-replay` passed without output in v3.

All ran with GOMAXPROCS=2, GOFLAGS=-p=1, nice19 and idle IO under separate BOX
turns. The v3 acceptance file explicitly links the v2 normal test: SHA256
`6cb0a15bedc09f1228c914c2a455a8eb645aec497b36f7e1d8d253127978854c`.
All recorded processes/groups exited and all five claim paths were absent at
acceptance. Full Make build was not rerun for this private scanner slice.

Two earlier attempts remain preserved. V1 exposed a fixture-only genesis JSON
`extraData` base64/hex mismatch; its exact failed source snapshot and logs remain
under the v1 artifact. V2 normal tests passed, but its race was terminated by the
controller when another Go workload appeared, and vet did not run. That interrupted
race is not counted as a pass. V3 changed no Go bytes and ran only the remaining
race and vet. Temporary test directories followed normal Go cleanup.

No real 192M corpus scan, retained-database access, owner session, wire protocol,
transaction dispatch or new TPS result occurred in this validation. A plan-only
CLI and a separately claimed full-source scan remain subsequent work.
