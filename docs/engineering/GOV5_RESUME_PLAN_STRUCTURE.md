# Offline bounded resume-plan structure

`internal/sharedexec/resume_plan.go` defines offline values and deterministic
hashing for the proposed four-frame recovery slice. It does not open files,
read chain state, recover source signers, arm a session, expose a wire endpoint,
change default ingress, or authorize dispatch. Runtime resume remains disabled.

`ValidateResumePlanStructure` binds exact bounded manifest bytes and existing
corpus rules to the plan's full chain identity, corpus/genesis hashes, source
proof, public mapping, recovery/restore proofs, canonical hash/height/root,
complete account-vector hash and nonce sum. The address-sorted existing EOA
vector joins the logical-index sender mapping **by address**, not slice position.
Nonces and conservative remaining-transfer funding are bounded. All sender
indices and addresses must form a complete, unique mapping.

Version 1 selects exactly the minimum logical index in each canonical nonce
bucket `{0, 41, 42, 500}`. In logical-index order these must identify four
original `[0, 500)` frames. Exact prefixes produce 583 canonical-skipped and
1417 new transactions; no rounding or substitute later frame is accepted.
This structure does not mutate those original frames or their signatures.

`ResumeMappingID` encodes the ordered `(index,address)` mapping without nonces.
`ResumePlanID` first validates structure, then encodes every plan field,
including all nonces and whitelisted frame commitments, using explicit fixed
little-endian widths and raw hash/address bytes. Their SHA256 domains are
`gov5-resume-mapping-v1\x00` and `gov5-resume-plan-v1\x00`. Plan IDs exclude the
runtime epoch, timestamps, paths, receipts and the ID itself. Only the existing
canonical-vector digest contract uses JSON.

A structurally valid mapping permutation can still name the wrong original
source signer. A passing test explicitly demonstrates this limitation. Before
any owner/session use, a separate scanner must verify all original shard
hashes, retain the same opened files, directly recover public sender addresses
from the original signed frames using their actual logical FrameRange, and
match this mapping digest. Proof hash fields alone do not authenticate proof
contents. Current plan functions return no authorized/session capability.
The real target's 6000 accounts, precise proof files and checkpoint must be
pinned by the later controlled runner, not hardcoded into these fixture-usable
structure helpers.

## Actual validation

On 2026-10-17, under a separate BOX claim after releasing the startup-probe
turn, `GOMAXPROCS=2 GOFLAGS=-p=1`, nice19/idle IO:

```text
go test -p 1 ./internal/sharedexec -run '^TestResumePlan' -count=1 -timeout=45s
go test -race -p 1 ./internal/sharedexec -run '^TestResumePlan' -count=1 -timeout=60s
go vet -p 1 ./internal/sharedexec
```

All three commands exited zero. Targeted tests took 0.015s and race tests 1.058s;
vet produced no output. Cases cover logical order differing from address order,
exact partial/all-skipped prefixes, minimum-index selection, all proof/payload
ID bindings, malformed vectors/ranges/ordering, and the mapping-authentication
limitation. A fixed synthetic metadata golden was independently encoded with
Python `struct.pack`/`hashlib`, then matched by the actual Go tests:

- Plan: `d8a783c8c9fc6389a48664113fdbe0a582d712b567d154414ea15cedc3f691c7`.
- Mapping: `8a8344766de09386b274acecd040e4b23cfbce715c74a052eaadb01f5ad11db7`.

Evidence directory:
`/data/blockchain/gov5-work/validation/20261017-gov5-resume-plan-v1`.
`status.json` SHA256:
`4a2c5689edfb0ae519e0faddb57409fd86af39d4306741775cf128e601547c5a`.
`independent-acceptance.json` SHA256:
`86606c0ebc7fe85848c736b9ffb5d39ecf5d4d957a5dc2c4c684a7f6c8262afb`.

Sol independently checked fixed source/controller/runner hashes, actual logs,
three exit codes, absent owned process groups and all five claim paths after
normal claim exit at 03:25:30 UTC. No original corpus, retained database, node,
owner, listener or transaction dispatch was involved. Full Make/build and real
source/session/transport/execution/restart validation remain later stages.
