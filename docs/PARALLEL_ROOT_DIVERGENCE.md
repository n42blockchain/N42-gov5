# ProcessParallel vs. Process root divergence: root cause, and why it does not reach the fleet

## Summary

`TestCoreTProcessParallelMatchesSequential` (internal/coreT_parallel_processor_test.go) found that
`StateProcessor.ProcessParallel` (Block-STM) and the sequential `StateProcessor.Process` land on
different post-state roots for a block that mixes a repeated sender with a shared recipient, while
gas used and receipt counts agree and the parallel result is internally deterministic.

Root-caused below: this is **not** a Block-STM conflict-detection or fold bug in
`applyMVSToIBS`/`MVHashMap`/`MVS`. Every account's balance and nonce produced by `ProcessParallel`
is byte-identical to the sequential result (verified by dumping both post-states). The divergence
is entirely in how `IntraBlockState.IntermediateRoot()` enumerates *which* accounts to hash, and it
only manifests when the `IntraBlockState` has **no `RootComputer` wired** — which is exactly the
unit test's own fixture, and is **never** the case in production. Production
(`internal/blockchain.go:2228-2245`) always wires a `RootComputer` (QMDB for the native chain, MPT
for eth-el, JMT for legacy chains) before calling either `Process` or `ProcessParallel`, and that
code path was already written to tolerate exactly the write pattern `ProcessParallel` uses. I
verified this directly: wiring a real `commitment.MPTRootComputer` onto the same test harness (the
same wiring `blockchain.go` does for a live import) makes `Process` and `ProcessParallel` produce
byte-identical roots for every diverging shape below, including the test's own repro.

**Conclusion up front**: the divergence is real in the unit test, reproducible, and worth fixing
(it is a latent landmine — see "Proposed fix" — and it was a legitimate test-fixture gap worth
root-causing), but it is **not** consensus-breaking against the fleet as currently deployed,
because the fleet's `IntraBlockState` is never run without a `RootComputer`. The task's premise
("this would be invisible to the fleet but consensus-breaking") does not hold once the mechanism is
traced; the mechanism is "test fixture never calls `SetRootComputer`", not "Block-STM corrupts
state".

## 1. Minimal reproducer and divergence table

All cases below use the shared 4-account chain fixture (`coreTGetChainFixture`,
internal/coreT_chain_fixture_test.go), 4 funded senders `f.Senders[0..3]`, built through
`ApplyTransaction`/`StateProcessor.Process`/`StateProcessor.ProcessParallel` exactly as
`TestCoreTProcessParallelMatchesSequential` does. `ProcessParallel` only engages above 4
transactions (`internal/parallel_processor.go:197`), so every case below pads with independent
"filler" transfers (round-robin senders, each to its own fresh unfunded recipient) to clear that
threshold. All cases below were run twice: once with the **bare fixture's `ibs`** (`state.New(reader)`,
no `RootComputer` — what the existing test does) and once with a **real `commitment.MPTRootComputer`
wired** onto the same `ibs` (mirroring `internal/blockchain.go:2228-2245`'s production wiring).

| Case | Shape | Diverges (no RootComputer) | Diverges (MPT RootComputer wired) |
|---|---|---|---|
| A | same sender, 2 txs, different recipients | yes | no |
| B | different senders, 2 txs, same recipient | yes | no |
| C | sender is also a recipient (A→B, B→A) | yes | no |
| D | same sender, 2 txs, **same** recipient (minimal shape of the original repro) | yes | no |
| E | 2 senders issuing 2 txs apiece to one shared recipient | yes | no |
| F | one sender issuing 3 txs to one shared recipient | yes | no |
| G | same sender 2 txs to shared recipient + 1 filler + 2 other fillers (5 txs total, minimum to clear the >4 gate) | yes | no |
| Original fixture test | 8 independent fillers + 2 same-sender/same-recipient txs | yes | no (verified with the exact same shape) |

**Minimal reproducer**: case G is minimal in transaction count — 2 filler transfers (needed only to
clear `len(txs) <= 4` in `ProcessParallel`, internal/parallel_processor.go:197) plus **two
transactions from the same sender to a recipient that no other transaction in the block ever reads
the balance of**. That second property — a recipient credited only via plain `value` transfers, so
its balance is never read, only added to — is what matters; it is what drives the account's write
onto the `AddBalance`-deferred path in `IntraBlockState.AddBalance` (modules/state/intra_block_state.go:882-909)
described in the mechanism section, and it is **sufcient on its own without repeated transactions from the same sender**:
cases A, B, C above (no sender issuing more than one transaction to the *same* account) diverge
identically. The "repeated sender" framing in the original test and in `docs/COVERAGE_CAMPAIGN.md`
is not load-bearing; any block where `ProcessParallel` is used and at least one non-sender account
ends up credited without ever being balance-read diverges under the no-RootComputer fallback, with
or without repeated senders. (Sender accounts are *always* full writes, never deltas — see below —
so "repeated sender" was a red herring the original test happened to include.)

Exploration was done as a temporary, uncommitted test file
(`internal/zzscratch_divergence_test.go`, deleted before this commit — not part of this change) with
subtests `TestZZExploreMinimalDivergence/{A..G}` and `TestZZExploreWithMPT{,AllCases}`.

## 2. Post-state dump for a diverging case (case D)

Case D: 3 filler transfers (senders 0,1,2 to fresh addresses) + 2 transfers from sender 0 to a
shared, otherwise-untouched recipient `0x...dd`. Touched-account dump taken via `ibs.GetBalance`/
`ibs.GetNonce` immediately after each run, before computing the root:

```
SEQ addr=0x...d000 (filler recipient)      nonce=0 balance=0x3e8
SEQ addr=sender0 (0xE5fF...)               nonce=3 balance=0x56bc75e2d62fecda8
SEQ addr=0x...d001 (filler recipient)      nonce=0 balance=0x3e8
SEQ addr=sender1 (0x753C...)               nonce=2 balance=0x56bc75e2d631df638
SEQ addr=0x...D002 (filler recipient)      nonce=0 balance=0x3e8
SEQ addr=0x...dd  (shared recipient)       nonce=0 balance=0x3e8
SEQ addr=coinbase (0x1111...)              nonce=0 balance=0x40a81
SEQ addr=sender2 (0xdE72...)               nonce=6 balance=0x56bc3d0aebe44977e

PAR addr=sender2 (0xdE72...)               nonce=6 balance=0x56bc3d0aebe44977e
PAR addr=0x...d000                         nonce=0 balance=0x3e8
PAR addr=sender0 (0xE5fF...)               nonce=3 balance=0x56bc75e2d62fecda8
PAR addr=0x...d001                         nonce=0 balance=0x3e8
PAR addr=sender1 (0x753C...)               nonce=2 balance=0x56bc75e2d631df638
PAR addr=0x...D002                         nonce=0 balance=0x3e8
PAR addr=0x...dd  (shared recipient)       nonce=0 balance=0x3e8
PAR addr=coinbase (0x1111...)              nonce=0 balance=0x40a81
```

**Every field of every account is identical between SEQ and PAR** — balance, nonce (code hash and
storage are irrelevant here, no contracts). `require.Equal` on a per-account, per-field diff would
find **zero differing accounts and zero differing fields**.

With no `RootComputer` wired:
```
seqRoot = 0xc841b58dffb0283f52a462702faaba020460ab1da7ae8b70c303113c20674d01
parRoot = 0xc5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470
```
`parRoot` is `hash.NilHash` — the fixed sentinel `GenerateRootHash()` returns when its dirty-account
set is empty (modules/state/intra_block_state.go:1495-1497: `if len(s.stateObjectsDirty) == 0 {
return hash.NilHash }`). This exact value recurred, byte-for-byte, as `parRoot` in **every** one of
cases A through G above and in the original fixture test, regardless of which accounts, amounts, or
sender/recipient combinations were used — the strongest signal that the "divergence" is not a
wrong value computed from the real post-state, but a hash of *nothing*, because `ProcessParallel`'s
writes never reached the enumeration `GenerateRootHash()` walks.

With a real `commitment.MPTRootComputer` wired (same test, same transactions):
```
seqRoot = parRoot (byte-identical) for every case A-G and the original repro shape.
```

## 3. Mechanism, file:line by file:line

1. `ProcessParallel` executes every transaction against a **per-worker** `IntraBlockState`
   (`wc.ibs`, internal/parallel_processor.go:318-371), and each worker calls
   `ibs.FinalizeTx(rules, stateWriter)` on *that* per-tx state after applying the transaction
   (internal/parallel_processor.go:687, inside `parallelApplyTx`). `FinalizeTx`
   (modules/state/intra_block_state.go:1269-1321) is what moves an address from
   `sdb.journal.dirties` into `sdb.stateObjectsDirty` (lines 1287-1293 and 1296-1311) — the set
   `GenerateRootHash()` later hashes.

2. The validated results are then replayed onto a **different, block-level** `IntraBlockState` — the
   `ibs` argument `ProcessParallel`/`runParallel` received from its caller — via
   `applyMVSToIBS(executor.MVS(), numTxs, ibs)` (internal/parallel_processor.go:561).
   `applyMVSToIBS` (internal/parallel_processor.go:728-845) calls the **raw** mutators directly:
   `ibs.AddBalance` (delta accounts, line 803), `ibs.SetBalance`/`ibs.SetNonce` (full-write accounts,
   lines 817-818), `ibs.SetState` (storage, line 833), `ibs.SetCode` (line 841), `ibs.Selfdestruct`
   (line 811), `ibs.CreateAccount` (wipe recreation, line 795). None of these calls is followed by
   `ibs.FinalizeTx` or `ibs.SoftFinalise` on this block-level `ibs` — unlike the per-tx worker state
   in step 1, and unlike the sequential `StateProcessor.Process`, which calls `ibs.FinalizeTx` once
   per transaction on the SAME `ibs` that will later compute the root (standard `ApplyTransaction`
   flow).

3. For the specific shape in this bug (a plain-transfer recipient that is never balance-read),
   `ParallelStateWriter.UpdateAccountData` (internal/parallel/state_writer.go:57-77) records the
   credit as a `Delta` write (line 68, via `RecordDeltaWrite`) rather than a full value, because the
   per-tx `deltaEligible` hook (set from the executor's per-tx `observed` balance-read map,
   internal/parallel_processor.go:367-368) reports the address was never read for balance. `MVS.ApplyAll`
   (internal/parallel/mvs.go:294-342) correctly composes that delta: for an address with no full write
   ever, it returns `fn(key, nil, delta)` (line 331) with the *summed* delta across all contributing
   transactions — this part of the fold is correct, confirmed by the identical final balances in the
   dump above. `applyMVSToIBS`'s account pass then calls `ibs.AddBalance(ae.addr, ae.delta)`
   (internal/parallel_processor.go:803).

4. `IntraBlockState.AddBalance` (modules/state/intra_block_state.go:882-909): if the address has no
   already-loaded `stateObject` in *this* `ibs` (`needAccount` false at line 888 — true here, since
   the block-level `ibs` never loaded the recipient for anything else), the credit is **not** applied
   to a state object at all. It is parked in `sdb.balanceInc[addr]` (lines 897-902) and a
   `balanceIncrease` journal entry is pushed (lines 893-896) — `sdb.stateObjectsDirty` is **not**
   touched by this call, by design (it is meant to be picked up later by whichever root-computation
   path handles `pendingIncreaseAddrs()`).

5. `IntraBlockState.IntermediateRoot()` (modules/state/intra_block_state.go:1536-1545) branches on
   whether a `RootComputer` was wired via `SetRootComputer` (modules/state/intra_block_state.go:322-327).
   - **If wired** (production: `internal/blockchain.go:2228-2245`, always true for both `Process`
     and `ProcessParallel` callers in the real import/build path), it calls
     `computeRootViaComputer()` (modules/state/intra_block_state.go:1558 onward), which explicitly
     materializes `pendingIncreaseAddrs()` into `stateObjectsDirty` (lines 1566-1569) **and** merges
     `s.journal.dirties` directly into `stateObjectsDirty` (lines 1570-1574), with the comment
     "Finalize (block rewards) and post-block system calls write to journal but there's no
     FinalizeTx after them" — i.e. this exact gap was already anticipated and closed for the
     tree-based root path.
   - **If not wired** (the test fixture: `coreTBuildChainFixture`,
     internal/coreT_chain_fixture_test.go, never calls `SetRootComputer` on the chain or on any
     `ibs` it builds), it calls the legacy `GenerateRootHash()`
     (modules/state/intra_block_state.go:1494-1531), which has no such merge: it reads
     `s.stateObjectsDirty` alone (line 1500), and short-circuits to the constant `hash.NilHash`
     when that set is empty (line 1495-1497).

6. Net effect in the test: on the block-level `ibs` after `applyMVSToIBS` plus the deferred fee
   credit loop (internal/parallel_processor.go:594-613, which also calls raw `ibs.AddBalance`,
   same gap — not exercised here since the gate at `touchesAny`, internal/parallel_processor.go:
   204-208/222-224, routes any block touching a fee recipient to the sequential path instead),
   `stateObjectsDirty` ends up empty (every account in this test's shapes is either an
   `AddBalance`-only delta recipient, routed through step 4, or — for sender accounts — a full
   `SetBalance`/`SetNonce` write; `SetBalance`/`SetNonce` go through `GetOrNewStateObject` →
   `createObject`, which also only pushes a journal entry, modules/state/intra_block_state.go:
   1063-1080, never touching `stateObjectsDirty` directly either). `GenerateRootHash()` therefore
   returns `hash.NilHash` for every `ProcessParallel` run in the no-RootComputer fixture, which is
   exactly the constant `0xc5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470` observed
   across every case. The sequential `Process()` path populates `stateObjectsDirty` correctly via its
   per-transaction `FinalizeTx` calls (standard `ApplyTransaction`), so its `GenerateRootHash()` call
   returns a real, content-dependent root — hence the "divergence".

## 4. Which side is right, and what is / isn't affected

- **The sequential root is the correct Ethereum post-state** in the no-RootComputer fixture: its
  account-by-account values (section 2) match what hand computation gives (sender balances debited
  by value + gas, shared recipient credited by the sum of the two transfers, coinbase credited by
  the priority fee sum) and reflect that state through a properly populated `stateObjectsDirty`.
- **The parallel root in the no-RootComputer fixture is wrong** — it is `hash.NilHash`, the hash of
  an empty dirty set, not a hash derived from any real (even incorrect) composition of balances. It
  is wrong by construction, not by a small delta.
- **The parallel root under production's RootComputer-wired path is correct** and matches the
  sequential root exactly (verified for every case in section 1's table, including the original
  test's shape) — because `computeRootViaComputer()` already merges `journal.dirties` and
  `pendingIncreaseAddrs()`, independent of whether `FinalizeTx`/`SoftFinalise` was called on this
  `ibs`.
- (a) **Same-sender nonce ordering**: not affected. Every sender's nonce in every dump matched
  between SEQ and PAR exactly (e.g. case D: sender0 nonce 3, sender1 nonce 2, sender2 nonce 6,
  identical in both runs). `executor.SetAffinity` pins one sender's transactions to one worker
  (internal/parallel_processor.go:418-429), so nonce chains execute and fold correctly; this was
  never the mechanism.
- (b) **Fee-recipient credits**: not exercised by this bug. `touchesAny`
  (internal/parallel_processor.go:145-157) routes any block that sends from or to a configured fee
  recipient to the sequential `Process` instead (internal/parallel_processor.go:204-208, 222-224),
  so `ProcessParallel`'s deferred fee-credit loop (lines 594-613) never ran in any of the reproducer
  cases. It shares the identical `ibs.AddBalance`-without-`FinalizeTx` gap described above and would
  show the same no-RootComputer symptom if it were ever exercised with a coinbase matching a
  configured fee recipient under the legacy hash path — but production always has a RootComputer,
  so this is the same non-issue as the main finding, not a second bug.
- (c) **Balance checks of a sender that is also a recipient**: not affected (case C: A→B, B→A diverged
  identically to every other case under no RootComputer, and matched identically once a RootComputer
  was wired — the sender-is-recipient shape adds nothing beyond cases A/B/D).

## 5. Proposed fix and pinning test (no code in this change)

Two complementary, no-code-yet changes:

1. **Primary fix** — in `runParallel` (internal/parallel_processor.go), call `ibs.SoftFinalise()`
   once on the block-level `ibs` after `applyMVSToIBS` and after the deferred fee-credit loop
   (i.e., right before the strict-mode gas/`ProcessExecutionBlockEnd`/`Finalize` block at
   internal/parallel_processor.go:615-630), so `stateObjectsDirty` is populated the same way the
   sequential path populates it via per-tx `FinalizeTx`. This removes `ProcessParallel`'s implicit
   dependency on `computeRootViaComputer()`'s journal-merge special case and makes the legacy
   `GenerateRootHash()` fallback correct too, closing the landmine for any future caller that
   constructs an `IntraBlockState` without a `RootComputer` (embedded tooling, a trimmed-down test
   harness, or a future refactor that makes `SetRootComputer` conditional).
2. **Fixture hygiene** — wire a real `RootComputer` (e.g. `commitment.NewMPTRootComputer()`, the
   same type used for `bc.mptRootComputer`) into `coreTBuildChainFixture`
   (internal/coreT_chain_fixture_test.go) so this fixture exercises the same `IntermediateRoot()`
   code path production uses, rather than the legacy fallback no production caller ever hits.

**Pinning test**: extend `TestCoreTProcessParallelMatchesSequential` (or add a sibling test) that
constructs the `ibs` twice per run — once as today (no RootComputer, asserting the fix makes
`GenerateRootHash()` agree) and once with a `commitment.MPTRootComputer` wired (asserting the
already-correct production path stays correct) — for the case D shape (same sender, two transactions,
one shared, never-balance-read recipient, no fee-recipient touch). The first assertion is a true
regression pin for fix (1); the second guards against a future change to `computeRootViaComputer()`'s
journal merge silently breaking the path that currently masks this gap.
