# Offline source-bound resume plan

Status: targeted normal/race tests, package vet and the full Make Go-package
build passed on 2026-10-17. No target corpus scan or transaction dispatch has run
for this change. The next gate is the separately controlled real source scan.

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
