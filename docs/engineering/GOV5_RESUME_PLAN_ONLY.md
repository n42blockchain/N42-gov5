# Offline source-bound resume plan

Status: targeted normal/race tests, package vet and the full Make Go-package
build passed on 2026-10-17. The separately controlled real source scan also
passed. Owner activation, resume transport and transaction dispatch remain
unimplemented; this is neither a fresh chain-state audit nor a TPS result.

`gov5-replay resume-plan` is an explicit offline command. It requires the exact
SHA256 pins of the finalized source manifest, retained runtime metadata, promoted
historical recovery report, complete account-vector report, and original restore
report. Its output must be a fresh `plan-output` directory inside an independent
validation artifact. Existing output is never replaced.

The command checks the historical reports' identity, canonical anchors, empty
owner session, seven nonproducing validators and account-vector digest. Report
pins identify previously reviewed bytes; this command does not reperform all of
the earlier database repair or process-lifecycle proofs. The complete address
vector is joined to the original source's logical indices by direct EIP155 sender
recovery after every source shard has passed its complete SHA256 check. Selected
frames are reread from the same descriptors and all 500 signatures and original
transfer fields are checked, including each canonical prefix and the all-skipped
frame. Temporary MDBX use inside the existing genesis calculation remains; no
retained execution or validator database is opened.

The artifact dependency order is fixed:

1. `source-proof.json` contains source identity, complete shard hashes, all public
   logical-mapping witnesses and mapping digest. Its typed JSON bytes end with a
   newline and contain no session, timestamp, self hash or PlanID.
2. The SHA256 of those actual bytes enters the four-frame plan. `plan.json`
   contains that plan and its deterministic PlanID.
3. `result-candidate.json` binds both actual artifact hashes, input pins and the source-file
   metadata observations. It explicitly reports no owner session, no fresh
   canonical database recheck and zero dispatched transactions. Scan or
   publication errors preserve the output directory and a failure record. The
   output directory's parent and final directory entries are synchronized.

A candidate is not a promoted result. A controller must require child exit code
zero, exact artifact hashes, no failure record, and successful completion of the
hardware claim's final ownership checks before publishing independent success
evidence. A complete candidate can remain after a late write, readback or fsync
failure. Its existence alone is insufficient. The CLI never creates `result.json`.

The v1 plan selects the minimum logical sender index in each canonical-nonce
bucket 0, 41, 42 and 500. Each selected original frame covers `[0,500)`. It records
583 canonical-prefix transactions and 1,417 prospective new transactions; it
does not change, crop, resign or transmit those original frames.

Neither `SourceProofSHA256` nor `PlanID` authorizes an owner or proves that a
different process verified the source. Future owner startup must independently
rehash the complete source, recover its public mapping, validate the selected
frames, reconstruct the same semantic proof, and compare it with the plan. It
must also freshly verify the actual canonical/applied anchor and complete account
vector before arming a new empty epoch. This command implements no arming or
resume wire protocol.

Validation evidence is retained at
`/data/blockchain/gov5-work/validation/20261017-gov5-resume-plan-cli-v2-source80gib/acceptance.json`
(SHA256 `2678df392285747399e32a9546e3de246e42c8053f3ce2ef7933a657458e4a3d`).
The actual commands were `go test -p 1 ./cmd/gov5-replay -run '^TestResumePlan'
-count=1 -timeout=90s`, its `-race` variant with `-timeout=120s`,
`go vet -p 1 ./cmd/gov5-replay`, and `make -o version-build build`.
All returned zero; the normal/race package times were 1.057s/3.316s. The Make
version-writing prerequisite was skipped to preserve the pinned source, while
the full `go build ./...` target ran. These are synthetic report/source fixtures,
not a current retained-chain recovery or TPS result.

The original 90GiB waiting controller was stopped with no stages executed and
its complete history preserved. The independently reviewed controller-local
policy `gov5-plan-cli-source-validation-box80gib-v1` used raw MemAvailable >=80GiB,
GOMAXPROCS=2, Go parallelism 1, nice19/idle IO and one 600s execution budget.
Ten Python boundary fixtures passed. The three quiet samples, mirrored BOX
claims, settle, foreign workload checks, full process-group cleanup and final
ownership checks were retained. The 80GiB floor also applied during execution.
Repository/fleet/source-scan defaults were not changed. All recorded processes
and groups exited and all five owned claim paths were absent at acceptance.

The real source run used clean source
`6215f96b8b861e958cdd8746a5ffb1157066621f` and dedicated replay binary SHA256
`e54f46efa5d957fc829879c88dec0e091ba6f56ce81677a23865b2d9f8f8ea30`.
Build and scan returned zero in 2.643s and 24.569s. All 64 finalized shards
(20,379,691,444 bytes) passed full SHA256 checks before public mapping recovery;
6,000 mapping signatures and 2,000 selected-frame signatures were checked.
These counts describe the two verification stages, not 8,000 unique signatures
or a new full-signature audit of all 192 million source transactions.

The actual selected logical indices were 0, 64, 69 and 3803. Their fixed
canonical skips are 500, 42, 41 and 0; prospective new counts are 0, 458, 459
and 500. The plan keeps all four original 500-transaction frames intact.
The mapping digest is
`70744b4336bbba0a7fc72b589ec081904fe3470221a71e941354f47b7e046284`;
PlanID is `79afc62975fbf5c0df56b295a1f477bd3834cc523ca93c12bf82e0eaadcbbe7e`.

Independent Sol acceptance is
`/data/blockchain/gov5-work/validation/20261017-gov5-resume-plan-real-source-6215f96b/acceptance.json`
(SHA256 `beda62f000b0a3d66e0edfc7f97c90ef729613642052728bf1fee42336ccbc5f`).
It independently re-encodes both binary IDs with Python, checks the artifact
hash chain and historical account vector, unchanged source metadata, actual
stage logs, empty recorded process groups and released claims. The promoted
result SHA256 is `08685cb953cff983b5a56dc6d2dc07e0e53877f8bdc0ab0fb54cb50745d10b78`.
The run used a separate controller-local source-scan policy with the same raw
80GiB floor, quiet/claim/cleanup checks, 64GiB disk low-water mark and one 600s
budget for build plus scan. No corpus copy, retained database, socket, owner
session or dispatch was involved; the existing genesis calculation used its
small temporary MDBX. The 90GiB real-chain launch floor remains unchanged.
