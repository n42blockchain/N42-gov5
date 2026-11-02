# Sealed import references (unverified draft)

This worktree is based on `61bb466a`. This draft has only received static review,
gofmt and whitespace checks; it has not been compiled or tested. It is not part of
the frozen `fb1671a4` first-fleet repair source or its running passive watcher.
The same-root eight-transaction baseline and recovery checks take priority.

The default-false process option `--performance.import-ref` selects reference
imports only for explicitly configured performance metadata validators. It does
not change genesis, transaction rules, ordinary node defaults or owner execution.
Selecting the flag without the required performance role/configuration must fail
at the existing metadata constructor before opening a database.

`POST /v1/import-ref` carries the complete execution identity and sealed block
hash. The owner only looks up an existing full-body import job under its registry
mutex, releases that mutex, and waits for the job or request cancellation. It
never creates a placeholder, acquires the execution lock, consults unsigned Build
cache entries, reads chain state or re-executes a block. Existing bounded job
retention remains unchanged. A missing or evicted job returns `need_body`; the
replica then makes one ordinary full-body Import call. Transport, framing,
identity and commitment errors do not trigger fallback.

Before either transport path, the replica still decodes the full received body,
checks its transaction root, publicly verifies the sealed header and stores the
complete block locally. Both responses pass the same identity, sealed hash,
parent, height and execution-result checks. Only durable verified results become
local execution evidence. The existing five-of-seven commit certificate and
canonical bridge remain necessary; a reference is not an availability proof,
independent execution proof or finality certificate.

Unexecuted test drafts cover real BlockChain execution with seven Unix clients,
zero work registration on a miss, joining while the execution lock is held,
waiter cancellation, strict bounded framing, invalid response commitments,
single need_body fallback, unchanged default full-body transport, transport
failure without fallback, complete local availability and four/five-vote gates.
They need restricted targeted Go tests and race checks after the first-fleet
watcher's hardware lease, before any feature commit or runtime use.

This change only avoids repeated owner-side full-body HTTP uploads and decoding
on a reference hit. P2P still sends full bodies, each validator still decodes and
stores them, and the producer still uses the original full-body sealed Import.
It does not provide frame transport, remove the shared execution lock, demonstrate
a fleet run or establish any TPS result.
