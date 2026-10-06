# QMDB residency and the fillTx cold path

Follow-up to docs/QS_FILLTX_DEPTH.md (6ed42aa2): the win2 profile shows
`fillTransactions`/`BuildParallel` time dominated by `lib/qmdb` getterCold.ColdEntry
and `lib/kv/mdbx` MdbxTx.GetOne. This note answers what makes an entry cold, what
cache (if any) sits in front of it on the build path, what the kept logs show, and
what the recipient pattern does to the leg's working set — then ranks rounds.

## 1. What decides resident vs. cold

There is **no size/age threshold at all** — residency is binary and tied to the
flush cursor, not to a working-set budget:

- `lib/qmdb/qmdb.go:316-317` — `entries []entry` is the whole resident window;
  `entriesBase` is the absolute slot of `entries[0]`. Everything `< entriesBase`
  is cold by construction.
- `lib/qmdb/evict.go:64-87` `EvictThrough(through)` — drops entry records for
  slots `[entriesBase, through)` from RAM unconditionally; no capacity check, no
  "keep last N" window. Comment at evict.go:14-17: resident cost is **~104 B/slot
  (entry+leaf)**, falling to **~32 B/slot (leaf only)** after entry eviction, plus
  the live-key index entry (kept forever, never evicted — `lib/qmdb/qmdb.go:431-448`
  `entryAt` always consults `t.idx.Get(kh)` for liveness).
- `modules/state/commitment/qmdb_root_computer.go:738-743` `EvictFlushed()` calls
  `EvictThrough(r.flushedThrough)` / `EvictTwigsThrough(r.flushedThrough)` — i.e.
  evict **everything already flushed**, every single time.
- Call sites: `internal/blockchain_write.go:605` — every committed block, right
  after flush; `internal/replay/engine_v2.go:460,1255` — same, in replay. So in
  steady state the resident window is only the **current, not-yet-flushed
  build's appends**; one block (or one speculative/undo chain) deep. Anything a
  transaction reads that was written in an earlier block — i.e. almost all of
  the working set in a long leg — is cold by the time the next block builds.
- No `N42_QMDB_*` env or `conf/*.go` field gates this. The only QMDB env knobs
  that exist are unrelated to residency: `N42_QMDB_INDEX_PRESIZE` (index.go:92,
  presize the flat index), `N42_QMDB_TRUNC_INDEX` (index_trunc.go:234), and
  `N42_QMDB_PARALLEL_APPLY` (apply_parallel.go:38, apply-phase worker count).
  `scripts/qs/qs-env.sh` sets none of these and no `GOMEMLIMIT` — grep for
  `GOMEMLIMIT|N42_QMDB` across `scripts/qs/*.sh` is empty. The fleet runs on
  the unmodified "evict everything flushed" default.

## 2. The cold read path and the cache the build uses

- `getterCold.ColdEntry` (`lib/qmdb/evict.go:32-49`) does **one `GetOne` per
  cold entry**, keyed by `BE8(slot)` into `EntryTable` — no batching, no
  multi-get. `lib/kv/mdbx/kv_mdbx_tx.go` `GetOne` is a single MDBX cursor
  lookup; nothing coalesces repeated cold lookups across a build.
- Build path reader selection: `internal/parallel_processor.go:348-354` and
  `:902-904` — when QMDB commitment is active, `base := state.NewPlainStateReader(tx)`
  and then `base = commitment.NewQMDBStateReader(commitment.NewLookupSourceLocked(p.bc.qmdbRootComputer, tx), base, mode)`;
  `wc.reader = parallel.NewParallelStateReader(base, ...)`. `QMDBStateReader.Get`
  (`modules/state/commitment/qmdb_state_reader.go:118,186,230`) calls
  `qmdb.Tree.Get` directly — which is `entryAt` → cold fault on miss. **There is
  no LRU/cache stage between the QMDB tree and MDBX on this path.**
- The byte-budgeted read caches that do exist — `readAccounts`/`readStorage`
  s3FIFO LRUs and `readCode` byteLRU in `modules/state/buffered_plain_state.go`
  (CacheBudget defaults: 4 GB / 8 GB / 1 GB, comment at line 150-171 citing
  88% hit rate on a 128 GB host) — sit in `PlainStateBuffer`, which is the
  **plain-state/MPT (eth-el) path**, not the QMDB path. `internal/miner/worker.go:1643-1651`
  builds a `PlainStateReader` + optional `CachedStateReader`/`PostStateReader`
  stack for its own use (trace/RPC-style reads), but the actual fillTx build
  reader for the n42 native (QMDB) chain is the uncached `QMDBStateReader`
  wired in `parallel_processor.go`. **The build path on the QMDB chain bypasses
  every existing read cache in the codebase.**

## 3. Kept-log evidence (r35zzzax node1)

`/data/blockchain/wr-logs/r35zzzax-keep/node1/n42.log` (178,159 lines) has **no
per-block cold-read or cache-hit counters for QMDB**. The only matching fields
are:
- `cacheHits`/`cacheMisses` — these are the unrelated *sender-recovery* cache
  ("sender cache probe" log line), not QMDB state reads.
- `liveKeys`/`livePctOfSlots` — only 4 occurrences total, all from "qmdb index
  loaded" at process/reload boundaries (e.g. `liveKeys:6274773 livePctOfSlots:8.3%
  nextSlot:75546992`), not a per-block series, so no win1-vs-win2 trend can be
  read from this leg's logs.

**Finding in itself: there is no instrumentation today that would let anyone
see the cold-read growth directly from logs** — the win2 slowdown was found by
CPU profile (docs/QS_FILLTX_DEPTH.md), not by a counter. Any round should add a
cheap counter (cold-read count / cache hit-miss, logged per N blocks) alongside
the fix, or the next leg will be just as blind.

## 4. txflood's recipient pattern — is the working set bounded?

`cmd/txflood/main.go`:
- `-recipients N` (flag at line 608): "spread transfers over N **derived**
  recipients (0 = the single 0x..dEaD sink)". `deriveRecipient` (line
  470-479) derives the i-th recipient from `Keccak256("n42-txflood-recipient-v1", i)`
  — a **fixed, deterministic set of N addresses reused for the whole leg**, not
  fresh addresses per tx and not drawn from the sender set (comment: "a
  separate domain from deriveKey's, so a recipient can never collide with a
  sender").
- Senders are likewise a fixed derived set sized by `-senders` (8,000 in the
  fleet's standard config), funded once at leg start.
- So the touched-account set within a leg is **bounded**: senders + recipients,
  both fixed-size and reused every transaction — it does not grow unboundedly
  through the leg. The win2 slowdown is therefore not "state scanning past a
  growing frontier" but simply that the same bounded working set, once
  flushed, is *immediately* evicted every block and re-fetched cold on its next
  touch — a residency problem, not a working-set-size problem. That is good
  news for a fix: the working set that needs to stay resident is small and
  known (≈ senders + recipients + their touched storage slots), not open-ended.

## Rounds, ranked

### Round 1 (config/threshold, recommended first) — delay `EvictFlushed` by K blocks
Change: gate the existing unconditional `EvictFlushed()` call
(`internal/blockchain_write.go:605`) behind a slot-count or block-count
threshold (new env, e.g. `N42_QMDB_EVICT_LAG_BLOCKS`, default 0 = current
behavior), so the resident window covers the last K blocks' appends instead of
only the open one. This is the minimal change that turns "evict everything
flushed" into a tunable residency window — no existing knob already does this,
so it is a small, bounded code change masquerading as a config knob (one
`if` + one env read, no algorithm change).
- Sizing: with senders=8,000 + recipients=N touched roughly every block in a
  flood workload, keeping ~50-100 blocks resident costs on the order of
  (touched-accounts-per-block × ~104 B) × K — for a working set in the tens of
  thousands of accounts/slots this is tens of MB, not GB; cheap relative to the
  11 GiB GOMEMLIMIT and the current 8-9.6 GB live heap.
- Expected effect: once K blocks exceeds the time between repeat touches of
  the same hot sender/recipient, win2 cold-fault rate should fall back toward
  win1's, recovering most of the ~2x fillTx growth seen through the leg.
  Prediction: fillTx at win2 moves from its current elevated value back toward
  the win1 baseline (docs/QS_FILLTX_DEPTH.md numbers), i.e. close to the ~1x
  ratio instead of ~2x, modulo whatever share of the slowdown is twig-leaf
  eviction (`EvictTwigsThrough`, not addressed by this round) rather than entry
  eviction.
- Risk: low — eviction still happens, just later; no behavior change to
  correctness (cold path still exists and is correct, just hit less). Main
  risk is picking K too small (no effect) or interacting with
  `AdoptOwnAppends`/undo bookkeeping if K spans a reorg window — needs a check
  that `EvictFlushed` lag doesn't retain undo-relevant state past its revert
  horizon.

### Round 2 (code) — tenure-scoped read cache in front of QMDBStateReader
Add an s3FIFO/LRU cache keyed by `qmdb.Hash` in front of `QMDBStateReader.Get`
(`modules/state/commitment/qmdb_state_reader.go:118`), sized and reset per
tenure (mirroring the pattern already proven in `buffered_plain_state.go`,
which reports 88% hit rate at 4-8 GB budgets on the MPT path). This fixes the
problem regardless of the eviction policy, and also covers twig-leaf misses
that Round 1 doesn't touch.
- Expected effect: similar or larger recovery than Round 1 since it caches at
  the value level (post-derivation), independent of which slots QMDB has
  evicted; could also absorb MDBX-side cold reads for keys QMDB itself evicted
  before this tenure started.
- Risk: medium — new cache invalidation surface (writes within the same
  tenure must invalidate/update the cache entry; currently `PlainStateBuffer`
  solves this for the plain-state path and that logic would need to be
  replicated or shared for QMDB keys), and another few GB of heap budget to
  plan against GOMEMLIMIT.

### Round 3 (code) — prefetch next block's touched keys
Before building block N+1, prefetch (parallel `GetOne` or a batched read) the
keys the pool's head transactions are expected to touch (senders + their
likely recipients from `txspool`), warming `entryAt` lookups ahead of
`fillTransactions`. This overlaps cold-read latency with the previous block's
tail instead of removing cold reads.
- Expected effect: smaller than Round 1/2 (hides latency rather than avoiding
  it) and workload-dependent (prefetch accuracy is high for txflood's fixed
  sender/recipient set and likely lower for mixed-fee-market traffic).
- Risk: highest — adds prediction logic and a new source of profile noise
  (wasted prefetch work if the predicted set is wrong), and competes for the
  same CPU budget it's trying to save during the build window.

**Recommendation**: run Round 1 first — it is the smallest change, has a clear
mechanism tied directly to the profiled hot path (getterCold.ColdEntry /
MdbxTx.GetOne), and its risk is bounded and inspectable (undo-horizon check).
Add the per-block cold-read/hit counter from §3 in the same round so the next
leg's logs can confirm the mechanism instead of requiring another profile.
