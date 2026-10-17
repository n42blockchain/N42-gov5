# Resume ingress implementation boundary

The source verifier is shared by the offline plan command and the future owner
startup path. `sharedexec.ScanResumeSources` receives actual manifest bytes,
checks all complete shard hashes, recovers the public logical sender mapping,
and verifies every transaction in the four selected original frames. A private
lease keeps the same file descriptors available for an owner startup's eventual
fresh-state and metadata checks. The public wrapper always closes them and
returns only audit data. No returned report is an activation capability.

`EncodeResumeSourceProof` constructs the typed document and canonical
newline-terminated JSON bytes together. The CLI uses this one implementation.
A genuine signed-source fixture compares it byte-for-byte with the previous
CLI's independent literal document layout, including nested witness fields.
The recipient and private genesis profile checks also share their original
implementations; ordinary strict streaming and default execution behavior are
unchanged.

This extraction enables no owner session, wire protocol, runtime flag or
transaction dispatch. Owner resume startup still needs independent local source
verification, a fresh complete canonical/applied account vector and root check,
and a mutually exclusive startup gate. The sealed resume protocol, atomic
prefix admission, actual four-frame execution and physical restart acceptance
remain subsequent gates. The real source plan described in
[GOV5_RESUME_PLAN_ONLY.md](GOV5_RESUME_PLAN_ONLY.md) is historical input, never
permission to replay old nonces.

Targeted normal/race tests of `TestResume(Source|Plan)` in `cmd/gov5-replay` and
`internal/sharedexec`, both package vets, and `make -o version-build build`
returned zero on 2026-10-17. The Make target ran the complete `go build ./...`
with only its version-writing prerequisite skipped to preserve pinned source.
Independent acceptance is retained at
`/data/blockchain/gov5-work/validation/20261017-gov5-resume-shared-source-v1/acceptance.json`
(SHA256 `ab70cbecaccdea662f21aca8a48346a608f034591f661b625f6db898cfe4277a`);
status SHA256 is
`b13200cebf5851c4377705be64b6f1eed23188a6330abd43337a87b348700a36`.
It binds the exact nine changed source files, four stage logs, controller-local
raw80GiB source-only policy, exited processes/groups and released claims. These
small fixtures use a temporary genesis MDBX; they do not open the retained
chain, scan the real corpus again, or measure TPS.
