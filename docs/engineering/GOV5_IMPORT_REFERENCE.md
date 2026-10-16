# Sealed import reference RPC

`POST /v1/import-ref` is an explicit private execution-owner API carrying the
full performance chain identity and sealed block hash. It joins only an
existing full-body Import job. A miss returns `need_body`; it never creates a
placeholder, acquires the execution lock, consults unsigned Build or prepared
state, or reads chain state/receipts. Existing bounded job retention applies.
Cancellation of a reference does not remove or cancel its execution owner.

The client checks identity/hash, mutually exclusive miss/result framing and the
same parent, height and execution commitments as ordinary full-body Import.
Requests are limited to 4 KiB, responses to 16 KiB, with strict JSON framing.
Transient results are not retained. An evicted completed job can explicitly
fall back to full-body Import, whose existing durable DB check avoids repeated
execution. Errors and malformed results do not authorize fallback.

Six reference tests pass across targeted runs (0.049s and 0.061s). They use
actual temporary BlockChain execution and real Unix clients: seven references
join one execution, including while `execute` is locked; missing/build-only
hashes return without taking that lock or registering work; cancellation keeps
the owner; wrong identities, commitments and malformed framing fail; transient
and evicted jobs miss, and durable full-body fallback does not re-execute.
Five existing full-body/build/5-of-7 certificate regressions pass (0.079s).
Targeted sharedexec vet passed. Race and process-level comparisons remain gated
on hardware availability.

This slice adds only the owner/client RPC. Replicas and nodes continue their
existing full-body path; there is no CLI activation in this commit. It does not
reduce P2P/local body storage, change signature or execution policy, remove the
shared execution lock, prove independent execution correctness, or establish a
new fleet/TPS result. The frozen b02d84a2 functional source, df691d4f strict
observation source and main tree's offline Ed work are unchanged.
