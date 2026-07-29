# HotStuff-2 Spec ↔ Implementation Map

This document maps the HotStuff-2 paper's safety/liveness rules onto the
N42 implementation in `internal/consensus/hotstuff/`. It exists so that any
reviewer can verify, rule-by-rule, that the code enforces what the paper
requires, and to make the gap between paper and implementation explicit
where one exists.

**Reference paper:** Dahlia Malkhi and Kartik Nayak, *"HotStuff-2: Optimal
Two-Phase Responsive BFT"*, IACR Cryptology ePrint Archive 2023/397, 2023.
<https://eprint.iacr.org/2023/397>

The N42 implementation is **not** a literal port. It uses the paper's
2-round optimistic flow but with engineering choices (BLS aggregation,
batch verification, adaptive pacemaker, GossipSub + Rotor relay,
piggybacked PrepareQC, double-vote tracker) that the paper does not
prescribe. Those choices are called out in §3 *Differences from the paper*.

## Phase model

The paper presents a 2-round optimistic protocol:

```
Round 1 (Prepare):  Leader proposes block → validators vote → PrepareQC
Round 2 (Commit):   Leader broadcasts PrepareQC → validators commit-vote → CommitQC → block decided
```

The implementation tracks per-view phases as `internal/consensus/hotstuff/types.go:45-56`:

| Constant                       | Meaning                                                   |
|--------------------------------|-----------------------------------------------------------|
| `PhaseWaitingForProposal`      | Validator initial state, awaiting leader proposal        |
| `PhaseVoting`                  | Leader has proposed; collecting Round 1 votes            |
| `PhasePreCommit`               | PrepareQC formed; collecting Round 2 commit votes         |
| `PhaseCommitted`               | CommitQC formed; block committed (terminal for the view) |
| `PhaseTimedOut`                | View timed out; collecting timeout messages               |

`RoundState` (`round_state.go:15-26`) holds the three pieces of safety
state required by the paper: `currentView`, `lockedQC` (the safety lock),
`lastCommittedQC`. It also holds two additional fields enforcing the
implementation's stronger no-equivocation discipline: `votedInView` and
`commitVotedInView` (one Round 1 + one Round 2 vote per view; see SR9).

---

## §1 Safety Rules

### SR1 — Voting Rule

> A validator votes for a proposal only if `proposal.justify_qc.view ≥ locked_qc.view`.

| Field | Value |
|-------|-------|
| **Implementation** | `RoundState.IsSafeToVote` — `internal/consensus/hotstuff/round_state.go:151-153` |
| **Call site** | `processProposal` — `proposal.go:102-107` (rejects with `SafetyViolationError`) |
| **Existing test** | `TestRejectSafetyViolation` — `hotstuff_test.go` (referenced in chaos_test.go) |
| **New test** | `TestSafetyRuleRejectStaleQC` |

```go
// round_state.go:149-153
func (rs *RoundState) IsSafeToVote(justifyQC *QuorumCertificate) bool {
    return justifyQC.View >= rs.lockedQC.View
}
```

`processProposal` calls `IsSafeToVote` *after* it has already verified the
proposer's BLS signature on the proposal and the aggregate BLS signature
on the embedded `justify_qc`. This ordering matters: a malicious proposer
cannot bypass SR1 by sending a forged `justify_qc`, because the BLS check
runs first (`proposal.go:88-99`).

### SR2 — Lock Rule

> Whenever a validator sees a PrepareQC, it updates `locked_qc` (monotonically).

| Field | Value |
|-------|-------|
| **Implementation** | `RoundState.UpdateLockedQC` — `round_state.go:142-147` |
| **Call sites** | `processProposal` (proposal's justify_qc) — `proposal.go:121`; `processProposal` (piggybacked PrepareQC) — `proposal.go:124-130`; `tryFormPrepareQC` (leader self-update on quorum) — `voting.go:148`; `processPrepareQC` — `proposal.go:178`; `processNewView` (TC's high_qc) — `timeout.go:262`; `tryFormTCAndAdvance` — `timeout.go:354`; `processDecide` — `timeout.go:320` |
| **New test** | `TestLockMonotonic` |

```go
// round_state.go:142-147
func (rs *RoundState) UpdateLockedQC(qc *QuorumCertificate) {
    if qc.View > rs.lockedQC.View {
        rs.lockedQC = qc.Clone()
    }
}
```

The `qc.View > rs.lockedQC.View` guard enforces monotonicity: an attacker
cannot push the lock backwards by replaying an older PrepareQC.

### SR3 — Two-Phase Commit

> A block is committed when, in the same view, both PrepareQC and CommitQC have formed. No third phase.

| Field | Value |
|-------|-------|
| **Round 1 → PrepareQC** | `tryFormPrepareQC` — `voting.go:122-171` |
| **Round 2 → CommitQC**  | `tryFormCommitQC` — `voting.go:267-331` |
| **Phase transition** | `EnterPreCommit` (`round_state.go:81-83`) at PrepareQC formation; `Commit` (`round_state.go:85-95`) at CommitQC formation |
| **Existing test** | Happy-path multi-node consensus in `chaos_test.go` |

The paper's "no third phase" property is enforced structurally: there is
no `PreCommitQC` or third-round message type in `types.go`. After
`tryFormCommitQC` succeeds, the leader broadcasts `Decide` (`voting.go:299-312`)
and advances to `view+1`. Replicas receiving `Decide` jump straight to
`PhaseCommitted` via `processDecide` (`timeout.go:273-339`).

### SR4 — View Change

> The new leader uses the highest `high_qc` from the TC as its `justify_qc`.

| Field | Value |
|-------|-------|
| **TimeoutMessage carries `HighQC`** | `timeout.go:25-30` (initial), `timeout.go:77-82` (re-broadcast) |
| **TC builder picks highest** | `BuildTC` — `quorum.go:240-282` (the `if highestQC == nil || entry.highQC.View > highestQC.View` selection at lines 250-253) |
| **New leader installs as locked_qc** | `tryFormTCAndAdvance` — `timeout.go:354` (`UpdateLockedQC(&tc.HighQC)`) |
| **NewView carries TC** | `NewViewMsg{TimeoutCert}` constructed at `timeout.go:359-364` |
| **Replicas update locked_qc on NewView** | `processNewView` — `timeout.go:262` |
| **New test** | `TestViewChangeUsesHighQC` |

The flow (`timeout.go:341-381 tryFormTCAndAdvance`):

1. `BuildTC` walks all collected timeout messages, picks the highest valid
   `high_qc`, and verifies it (`quorum.go:267-274`).
2. `e.roundState.UpdateLockedQC(&tc.HighQC)` installs that QC as the new
   leader's lock.
3. The new leader signs a `NewViewSigningMessage` and broadcasts
   `NewViewMsg{View: nextView, TimeoutCert: *tc, ...}`.
4. Replicas in `processNewView` re-verify TC + embedded `high_qc` and
   call `UpdateLockedQC(&nv.TimeoutCert.HighQC)` themselves.

When that new leader subsequently calls `onBlockReady`, the proposal's
`justify_qc` is built from `e.roundState.LockedQC().Clone()`
(`proposal.go:25`), which has just been updated to the highest `high_qc`.
SR4 holds.

### SR5 — Quorum

> `n = 3f + 1`, `quorum = 2f + 1`.

| Field | Value |
|-------|-------|
| **Implementation** | `ValidatorSet.QuorumSize` — `validator.go:50-53` |
| **Storage of f** | `ValidatorSet.faultTolerance` (`uint32`) — `validator.go:24-27`; set by `NewValidatorSet` `validator.go:29-38` |
| **Quorum check sites** | `processVote` — `voting.go:91`; `tryFormPrepareQC` — `voting.go:134`; `processCommitVote` — `voting.go:236`; `tryFormCommitQC` — `voting.go:278`; `processTimeout` — `timeout.go:127, 147`; `processDecide` — `timeout.go:281-288`; `verifyAggregateSignature` — `quorum.go:354` |
| **Existing tests** | `chaos_test.go` runs n=4 (f=1) and n=7 (f=2) |
| **New test** | `TestQuorumBoundary` |

```go
// validator.go:50-53
func (vs *ValidatorSet) QuorumSize() int {
    return int(2*uint64(vs.faultTolerance) + 1)
}
```

**Note:** The paper writes the formula as `quorum = 2f + 1` over `n = 3f + 1`.
N42 stores `f` directly (set by the caller of `NewValidatorSet`) rather
than deriving it from `n`. This is correct for any `n ≥ 3f + 1`, including
oversized sets where `n > 3f + 1` (e.g. `n = 10, f = 3 → quorum = 7`),
which the paper allows. The caller (epoch reconfiguration) is responsible
for picking `f = ⌊(n − 1) / 3⌋`.

### SR6 — Justify QC

> Any QC must contain ≥ 2f+1 valid signatures, and each signed message must match `(view, block_hash)`.

| Field | Value |
|-------|-------|
| **Aggregate-signature verification** | `verifyAggregateSignature` — `quorum.go:352-` |
| **PrepareQC domain** | `VerifyQC` — `quorum.go:286-289` (uses `SigningMessage(view, blockHash)`) |
| **CommitQC domain** | `VerifyCommitQC` — `quorum.go:292-295` (uses `CommitSigningMessage(view, blockHash)`) |
| **Cross-domain verifier** | `VerifyQCAnyDomain` — `quorum.go:297-306` (used for QCs whose origin domain is unknown, e.g. embedded in timeout messages) |
| **Per-vote BLS check** | `voting.go:336-380 batchVerifyVotes` (with individual fallback on batch failure) |
| **Call sites** | Proposal validation `proposal.go:94-99`; PrepareQC handler `proposal.go:168`; Decide handler `timeout.go:299`; TimeoutMessage embedded QC `timeout.go:120-124`; NewView TC's high_qc `timeout.go:258-260`; piggybacked PrepareQC `proposal.go:124-130` |
| **New test** | `TestForgedQCRejected` |

The signing messages bind the signature to `(view, block_hash)`:

```go
// quorum.go:286-295
func VerifyQC(qc *QuorumCertificate, vs *ValidatorSet) error {
    msg := SigningMessage(qc.View, qc.BlockHash)
    return verifyAggregateSignature(qc, vs, msg, "QC")
}
func VerifyCommitQC(qc *QuorumCertificate, vs *ValidatorSet) error {
    msg := CommitSigningMessage(qc.View, qc.BlockHash)
    return verifyAggregateSignature(qc, vs, msg, "CommitQC")
}
```

`verifyAggregateSignature` checks the bitmap length matches the validator
set, counts signers, requires `signers ≥ quorumSize`, deserializes the
aggregate signature, and verifies it against the aggregate public key of
the named signers.

### SR7 — Single Broadcast (no extra Key phase)

> The leader broadcasts the proposal once, with `justify_qc` included. There is no separate "Key" phase as in HotStuff v1.

| Field | Value |
|-------|-------|
| **Proposal carries justify_qc** | `Proposal{... JustifyQC: justifyQC, ...}` — `proposal.go:31-39` |
| **Single broadcast** | `onBlockReady` emits exactly one `OutputBroadcast` with `MsgProposal` — `proposal.go:55-63` |
| **No "Key" message type** | `types.go` defines `MsgProposal`, `MsgVote`, `MsgPrepareQC`, `MsgCommitVote`, `MsgDecide`, `MsgTimeout`, `MsgNewView`. There is no `MsgKey` or analogous third-phase message |
| **Spec note** | No new test required; structural property of the message types |

The implementation also piggybacks the *previous* view's PrepareQC on
the new proposal (`proposal.go:28-29, 37`) — a chained-mode optimization
that does not affect SR7 (the single broadcast still carries everything).

### SR8 — Equivocation Detection

> A validator that signs two different blocks in the same view is faulty.

| Field | Value |
|-------|-------|
| **Per-view per-validator tracker (Round 1)** | `equivocationTracker map[ValidatorIndex]types.Hash` — populated in `processVote` `voting.go:46-61` |
| **Per-view per-validator tracker (Round 2)** | `commitEquivocationTracker` — populated in `processCommitVote` `voting.go:191-207` |
| **Detection action** | Log a warning + emit `OutputEquivocationDetected` with both hashes (`voting.go:48-56`, `voting.go:194-202`); return without counting the conflicting vote |
| **Slashing hook** | `internal/consensus/hotstuff/slashing.go` consumes `OutputEquivocationDetected` events |
| **New test** | `TestEquivocationDetected` |

The trackers are reset per view as part of `advanceToView`. SR8 is
enforced both for prepare votes (Round 1) and for commit votes (Round 2),
matching the paper's notion that equivocation in *either* phase makes a
validator faulty.

### SR9 — Single Vote per (view, round)

> Each validator casts at most one vote per (view, round).

| Field | Value |
|-------|-------|
| **Round 1 tracking** | `RoundState.HasVotedInView` / `RecordVote` — `round_state.go:107-116` |
| **Round 1 enforcement** | `processProposal` — `proposal.go:148-157` (suppresses duplicate, records before send) |
| **Round 2 tracking** | `RoundState.HasCommitVotedInView` / `RecordCommitVote` — `round_state.go:118-126` |
| **Round 2 enforcement** | `processPrepareQC` — `proposal.go:171-205` (suppresses duplicate, records before send) |
| **Collector-level dedup** | `VoteCollector.AddVote` — `quorum.go:36-43` (`DuplicateVoteError`) |
| **Equivocation tracker** | Same as SR8 (`equivocationTracker`/`commitEquivocationTracker`) — same-view conflicting votes are dropped |
| **New test** | `TestDoubleVoteRejected` |

`processProposal` records the vote *before* emitting it (`proposal.go:154`)
so that an emit failure cannot leave the validator in a "voted-but-not-recorded"
state and re-attempt. The same pattern is used in `processPrepareQC` for
commit votes (`proposal.go:195`).

---

## §2 Liveness

The paper relies on the *responsiveness* property: a correct leader can
drive the protocol forward at network speed, and view-change is triggered
when a faulty leader stalls. The implementation realizes this with an
adaptive pacemaker.

| Concern | Implementation |
|---------|----------------|
| **Leader rotation** | `LeaderForView` round-robin: `leader = view mod n` — `validator.go:130-135` |
| **Pacemaker timeout** | `pacemaker.go` (exponential backoff in `consecutiveTimeouts`); `RoundState.Timeout` — `round_state.go:128-135` |
| **Backoff reset on commit** | `RoundState.Commit` — `round_state.go:94` (`consecutiveTimeouts = 0`) |
| **Future-view timeout fast-forward** | `handleFutureViewTimeout` — `timeout.go:154-221` (advances to a future view if a peer reports timeout there) |
| **NewView aggregation** | `tryFormTCAndAdvance` — `timeout.go:341-381` |

A liveness corner case: if a validator already in `PhaseTimedOut` re-fires
its timeout, `onTimeout` (`timeout.go:18-49`) re-broadcasts the timeout
message and re-checks the collector for a fresh quorum. This handles late
arrivals after a pacemaker tick.

---

## §3 Differences from the paper

The implementation makes engineering choices the paper does not specify.
None of them weaken safety; some change the *liveness* trade-offs.

1. **BLS aggregate signatures** instead of (paper's) generic threshold
   signatures. `voting.go` uses `crypto/bls/blst` for both single and
   aggregate verification. Aggregate verification key is built per-QC
   from the `Signers` bitmap (`quorum.go:verifyAggregateSignature`).

2. **Batch BLS verification** with `batchVerifyThreshold = 4`
   (`voting.go:17`). Below 4 buffered votes, individual verification is
   used because the batch overhead outweighs the win on small sets. On
   batch-verify failure the implementation falls back to individual
   verification to isolate the bad signature
   (`voting.go:336-380 batchVerifyVotes`).

3. **Optimistic vote** at `proposal.go:155-157`: the validator votes
   immediately after structural validation, *before* it has executed the
   block. Block execution is dispatched as `OutputExecuteBlock` and
   verified out-of-band via `onBlockImported`'s Baby Raptr DA check
   (`proposal.go:233-259`). The paper does not mandate this; if execution
   later fails, the validator's vote was for a block that won't be applied
   locally, which is benign as long as 2f+1 honest validators reach the
   same conclusion.

4. **Piggybacked PrepareQC** in chained mode (`proposal.go:28-29, 124-130`).
   A leader may attach the previous view's PrepareQC alongside its own
   `justify_qc`, allowing replicas to advance their lock by an extra view
   in a single message. The piggybacked QC is verified independently and,
   on failure, simply ignored (`proposal.go:127-129`) — it is an
   optimization, not a safety dependency.

5. **Dual signing-message domains** (`SigningMessage` vs
   `CommitSigningMessage`) for Round 1 vs Round 2. This is a domain-
   separation hardening: even if an adversary tricked a validator into
   signing the wrong round's message, the signature would not validate as
   the other round's QC. `VerifyQCAnyDomain` (`quorum.go:299-306`) tries
   both domains for QCs whose origin is ambiguous (timeout-embedded
   high_qc, NewView-embedded high_qc).

6. **No explicit "Key" message type**: the paper sometimes presents
   HotStuff variants with a third phase. HotStuff-2 has only two; the
   N42 message vocabulary in `types.go` reflects that — there is no
   message type beyond Proposal / Vote / PrepareQC / CommitVote / Decide /
   Timeout / NewView.

7. **Tail-fork detection** (`proposal.go:109-119`): a warning when a new
   leader proposes with a `justify_qc` that skips a view where we have a
   newer QC. This is monitoring, not enforcement; the paper does not
   require it. It does not reject the proposal — that would weaken
   liveness — but it logs for operator visibility.

8. **Adaptive pacemaker**: exponential backoff via `consecutiveTimeouts`,
   reset on every successful commit (`round_state.go:85-95`). The paper
   only requires *eventual* synchrony; the adaptive scheme tightens the
   timeout bound on healthy networks.

9. **Single binary, multiple network paths**: GossipSub + Rotor relay
   (`rotor.go`) for proposal dissemination. The paper assumes an
   abstract broadcast primitive; the implementation provides a
   single-hop relay overlay (`rotor.go:113-176 SelectRelays`) deterministic
   from the view number, with GossipSub fallback. This is a network-layer
   choice, orthogonal to the consensus rules above.

---

## §4 Crash recovery

`RoundStateFromSnapshot` restores `currentView`, `lockedQC`,
`lastCommittedQC`, and `consecutiveTimeouts`. The v2 persistence record also
stores both rounds' `(view, block_hash)` vote commitments. Each vote is
journalled before release to the network, and recovery reinstates those
commitments before processing messages. `mergeMonotonic` rejects conflicting
non-zero hashes at the same view and independently preserves the highest
locked/committed QCs, so delayed snapshots cannot reopen an SR9 vote or lock
regression window.

---

## §5 Open items

1. **Formal Byzantine-test coverage.** Existing `chaos_test.go` covers
   happy-path multi-node consensus. `byzantine_test.go` extends it with
   per-SR scenario tests: `TestByzantine_LockMonotonic`,
   `TestByzantine_DoubleVoteSuppressed`,
   `TestByzantine_DoubleCommitVoteSuppressed`,
   `TestByzantine_CommitEquivocationDetected`,
   `TestByzantine_ForgedQCInsufficientSigners`,
   `TestByzantine_ForgedQCWrongDomain`, `TestByzantine_QuorumBoundary`,
   `TestByzantine_ViewChangeUsesHighestHighQC`,
   `TestByzantine_NetworkPartition`. SR1 + SR8 Round 1 are covered by
   the pre-existing `TestRejectSafetyViolation` and
   `TestEquivocationDetection`.

2. **External audit** of the BLS aggregation path (`crypto/bls/blst`) and
   the domain-separation tags. These are correctness-critical and not
   covered by paper-level proofs.
