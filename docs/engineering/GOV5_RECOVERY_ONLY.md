# Performance validator recovery-only barrier

This change is pending source validation. It adds a terminal, process-local mode
for an explicit owner-queue performance validator. Ordinary production defaults
are unchanged. `node.performance_recovery_only: true` requires `miner: true` and
`performance_owner_queue: true`; startup latches the mode before Service or
producer timers start. This permits recovery of an already executed, sealed
child without first creating another empty block.

`POST /v1/recovery-only` on the existing private validator Unix API requires the
full chain identity and the pinned owner queue epoch and manifest. A first valid
request latches the mode, then one shared barrier waits for the current Produce
operation. Cancellation does not unlatch the mode or manufacture an ACK.
Duplicates wait for the same barrier. Status separately reports recovery-only,
drain acknowledgement and cumulative fresh-build admissions; producer-running
continues to describe the live timer rather than pretending it was stopped.

The producer still reads and verifies its own candidate WAL, or retrieves and
verifies the owner's AppliedChild, then runs the original sealed-header,
availability, durable execution and proposal gates. Only an absent candidate
before a fresh Build or BuildFromQueue is refused. HotStuff service, networking,
pacemaker, votes, public 5/7 CommitQC and certificate replay remain active.
The mode cannot create a CommitQC when public consensus cannot form one. A
locked tip without a recoverable child or CommitQC remains blocked.

A drain ACK is a fresh-production barrier, not a finality proof. An earlier owner
Import may outlive its canceled HTTP requester. Runtime acceptance must first
stop sending, drain the exact known admissions, latch all seven validators, and
boundedly obtain seven matching canonical tuples and an owner canonical=applied
root-bound full account vector. Fresh-build counters must remain flat. After
stopping validators, recapture the owner vector/root/tuple and require exact
equality, then stop the owner and independently perform nonproducing physical
reopen validation. A late import, changing root or missing ACK fails this proof;
there is no marker edit, rollback, fabricated QC, automatic transaction replay
or fallback to a new candidate.

The prior finite comparison reached metadata count 1104509, then failed its
stable snapshot. Its subsequent ordinary Miner=false recovery also timed out;
that failed report has no complete account vector. These failures remain
failures. This source change alone does not prove recovery or throughput.
