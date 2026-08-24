# DATC Historical-Proof Serving — cost model & rate-limiting spec

Status: design (2026-06). Implementation spec for the future historical-proof
RPC. DATC is positioned as **"archive plus"**: geth/reth/erigon archive nodes do
NOT serve full-history EIP-1186 proofs by default; DATC does. Today only the
`n42-datc proof` CLI exists (no RPC) — so there is no live exposure yet. This
doc is the spec to follow when the RPC is built, AFTER the full build is
generated and accepted (lands together with the segment format,
[[datc-segment-format]]).

## 1. How one historical proof is computed (cost model)

`runProof → proofPath` (`cmd/n42-datc/proof.go:225`) has two phases:

**Phase A — node-record walk (top).** From the trie root, descend level by level
calling `branchSlotsAt` / `nodeHashAt`; each does an **as-of-N floor lookup** on
the temporal node records (`DatcAccNode` / `DatcStorNode`). Count ≈ trie depth
(account trie ~7–9 levels at 25M-block state; storage trie depth depends on the
contract). ~1–2 reads per level → ~10–20 reads.

**Phase B — leaf fold (bottom).** Where the node record stops being usable
(`!usable`), fold the subtree from the leaf history: `subtreeLeaves → asOfLeaves`
(`cmd/n42-datc/verify.go:757`) enumerates **every distinct key under the fold
path, as of N** (per key: `Seek(key|n+1)` then `Prev` → the floor version),
then builds an in-memory MPT subtree (`mptNodeRLP`) and keccaks every node.
Account leaves additionally call `nodeHashAt` for each account's storage root
(`verify.go:803`) — **one extra read per account leaf**.

Total ≈ `O(depth)` node reads + `O(foldKeys)` leaf reads + `O(foldKeys)` keccaks
+ `O(foldKeys)` transient memory.

## 2. Per-proof cost is extremely variable (the core fact)

| case | fold size | cost |
|------|-----------|------|
| EOA / small contract / recent N / shallow fold | a few leaves | ~20–40 reads + ~10 keccak → **sub-ms…tens of ms** (more if read-back hits disk) |
| big-contract storage proof (USDT/USDC-class, ≫10⁵ slots) / old N / fold shrinks | K distinct keys | **K×~2 reads + K keccak + O(K) memory** → **seconds…minutes + up to GBs transient** |

Two amplifiers:
- **Old N deepens the fold.** For historical N, fewer deep epochs are materialized
  in the node records, so `!usable` triggers higher up → a *larger* subtree is
  folded from leaves. **The older the queried block, the potentially heavier.**
- **Cost spread is 4–6 orders of magnitude** between a light and a heavy proof.
  ⇒ **QPS-only limiting is insufficient** — one heavy request equals tens of
  thousands of light ones.

## 3. Resource risk

- **Read-bound**, same substrate as the build. Proofs run on a read-only RoTx
  (MDBX MVCC snapshot) so they do **not block** the single writer, but they
  **compete for the same disk / page cache** — which the build already saturates
  (read-back is its bottleneck). Unbounded proof serving directly starves the
  build (and vice-versa).
- A handful of heavy proofs (big contract × old N) can saturate CPU, exhaust the
  page cache, and spike memory — a cheap DoS vector if unbounded.

## 4. Rate-limiting design (layered; the lever is concurrency + per-request budget, not QPS)

### 4.1 Concurrency cap — primary gate
A global semaphore: at most `maxConcurrent` proofs in flight (suggest 2–4;
**auto-drop to 1 while the build is active**). Independent of per-request cost,
this bounds total CPU/disk that proof serving can take. This is the main control
*because* per-request cost is unbounded-variable.

### 4.2 Per-request cost budget — mandatory runaway guard
Abort a single proof before it can hurt the box:
- `maxFoldLeaves`: if `asOfLeaves` would enumerate more than M distinct keys,
  **abort and return an error** (`proof too large — narrow the query: use a more
  recent block, or a contract/range with fewer live slots`) instead of OOM/stall.
  Enforce by counting in the `asOfLeaves` walk and bailing past the threshold.
- `maxFoldDepth` and a `perRequestTimeout` (e.g. 2–5 s): cut on either.
- This specifically tames the only tail (big-contract × old-N) that can take down
  the node.

### 4.3 Per-client QPS — secondary
Token bucket per client IP. Reuse an existing primitive:
`internal/cl/sentinel/handlers/rate_limiter.go` (token-bucket), or the
stateless-serve `TrustedProxies` + `jsonrpc.ClientIP` secure-by-default path
(commit 930eb708, [[project_stateless_serve_security]]) so a forged
`X-Forwarded-For` cannot bypass it.

### 4.4 Bounded queue + backpressure
Fixed-depth request queue; when full, return **429** rather than spawning
unbounded goroutines. Backpressure, not collapse.

### 4.5 Build coexistence
- Proofs use an independent read-only RoTx snapshot (MVCC) — never block the
  writer.
- While the build is active, drop `maxConcurrent` to 1 (or pause non-urgent
  proofs): the build is a finite batch job with an end; the proof RPC is a
  long-lived service. Letting the finite job finish first is the better global
  trade. Gate on the build's `progress`/running flag.

## 5. Config knobs (proposed)

```
proof.maxConcurrent      = 4      # auto → 1 while building
proof.maxFoldLeaves      = 50000  # exceed → reject ("proof too large")
proof.requestTimeoutSec  = 5
proof.perIPQPS           = 5
proof.queueDepth         = 64     # full → 429
proof.pauseWhileBuilding = true
```

## 6. Error semantics

| condition                         | response |
|-----------------------------------|----------|
| fold exceeds `maxFoldLeaves`/depth| 400 `proof too large` (deterministic; advise narrower query) |
| per-request timeout               | 504 / 503 `proof timeout` |
| queue full                        | 429 `busy, retry` |
| per-IP QPS exceeded               | 429 `rate limited` |
| block N beyond DATC head          | 400 `out of range` (recent blocks → serve from live node, §8) |

## 7. Wiring (when the RPC is built)

- A read-only handler holding the semaphore + token bucket + RoTx, calling the
  existing `querier.proofPath` (the CLI path) unchanged — only wrapped with the
  budget/limit envelope.
- Counters in the budget: thread a `leavesSeen` counter into `asOfLeaves` and
  return a typed `ErrProofTooLarge` past the threshold (small, localized change
  to the existing fold).
- Expose Prometheus: proof latency histogram, in-flight gauge, reject/timeout
  counters, fold-size histogram — to tune the knobs against real traffic.

## 8. Two-tier serving (recall)

DATC serves **historical** proofs (genesis → last update). **Recent/tip** proofs
should be served from the live node's current trie (`internal/mptproof`
`LatestProof`, on-demand) — far cheaper (hot state) and always fresh. The RPC
front routes `N ≥ checkpoint` to the live node and `N < checkpoint` to DATC. This
also keeps the heaviest DATC case (old N) off the hot path.

## 9. Interaction with the segment format

Under [[datc-segment-format]] each leaf/node read becomes: bloom probe → RecSplit
idx → zstd frame decode, scanning periods newest-first. The cost model is
unchanged in shape, but **old N scans more periods** — reinforcing §4.2 (per-request
budget) and the `key → latest-period` hint index (O(1) for tip, bounded for
historical). `maxFoldLeaves` also caps the number of period scans a single proof
can trigger.

## 10. Landing order

- **Now**: CLI only, no exposure — nothing to limit; the build runs undisturbed.
- **After full generation + acceptance**, when the proof RPC is built:
  §4.1 (concurrency cap), §4.2 (per-request budget), §4.5 (build coexistence) are
  **mandatory** (the cost tail can otherwise take down the node); §4.3/§4.4 are
  standard RPC hygiene. Ship them with the handler, not after.

---

## 11. 2026-07 investigation — measured findings & correctness bugs

Deep session on the v5 build (`D:/n42-datc-eth25m-v5`, head 25311094). Repro address `0x1111…1111`, headerc oracle (`D:/n42-eth1/chain/freezer`, covers ≤24.998M).

### 11.1 Two "boundary vs non-boundary" facts
- **A block N is an epoch boundary at level d iff `(N+1) % e[d] == 0`.** At an ALL-level boundary (N+1 divisible by every e[d]; e.g. 20971519, 8388607) every node is read from a pure record — proof correct + fast (~166ms; account walk 1.6ms + fold of a small on-path depth-4 subtree). At a non-boundary N the reconstruction window-replays (step-back + `changedChildren` + recurse), which is both **slow** and, currently, **wrong** (see 11.3).
- **50 historical proofs verified**: 50 heights `N = k·262144 − 1` (all boundaries), each account proof verified against `header[N].Root`, 50/50 in 43s (~0.86s each incl. process start). Boundaries only — see 11.3 for why non-boundaries fail.

### 11.2 Performance — what makes a proof slow (measured, not guessed)
- **cbar=0.25 (α=16) ⇒ `E_d = clamp(α·16^d/C̄,1,2²²) = 64·16^d = [64,1024,16384,262144,4194304,4194304]`.** Coarse ⇒ at a non-boundary N the shallow window has thousands of changes ⇒ all children dirty ⇒ the root-synthesis cascade reconstructs ~the whole account trie ⇒ **>10 min** for a big-contract/old-N proof.
- **CPU profile of the cascade (offset+1 ≈ 32 s):** MDBX cgocall ~20%, GC ~15-20%, segment decode ~16% (of which **zstd only ~5%**), keccak/compare/uvarint ~12%. **⇒ dropping zstd is NOT the lever (~5%); cbar tuning is NOT the lever** (window=0 → 153ms, window=1 → 32s; nothing in between — the cascade is node-count-bound).
- **Offset-from-boundary curve** (from an all-level boundary, foldDepth 4): window 0 → 153ms; depth-1 window ≥1 → 32s…>100s; depth-2-only window → 26s; depth-3-only window (65536) → 1.4s. Cost = the subtree size at the SHALLOWEST non-zero-window level.
- **Two-tier serving stands**: recent/tip → `internal/mptproof` `RethHashedLeafSource` (account proof **<1ms**, storage seconds; 50 storage proofs verified in 121ms, ~2.4ms each — **production-ready today**). Historical → DATC.

### 11.3 Correctness bugs
- **Bug #1 — FIXED** (worktree `dd81913e`): storage-trie root rebuilt from the persisted depth-0 record, but `recordChangeStorage` writes no depth-0 change rows (`continue` at d==0) ⇒ stale child hashes at non-boundary N. Fix: `branchSlotsAt` returns `!usable` at d==0, storage root synthesized from its depth-1 children (like the account trie). Verified `--at 4194304/4194305` PASS.
- **Bug #2 — SOLVED 2026-07-01: DATA LOSS in the leaf history, not a logic bug.** `foldAt`/GenStructStep and the window-replay are both exonerated. Root cause chain: (1) `finalizeBucket` resyncs on the 4-byte zstd magic `28 B5 2F FD`; (2) a spill ROW starts `uvarint(keyLen)+key`, and account keyLen 40 = 0x28, so **every row in bucket a.b5 begins `28 B5`** — any key starting `b5 2f fd` stored as raw literals inside a compressed frame forges a FALSE magic; (3) the naive start→next-candidate decode then splits the healthy frame, BOTH halves fail, and the whole real frame's rows are dropped as "corrupt". The v5 rescue finalize logged `a.b5.zspill: skipped 61 corrupt frame(s)` (vs 2 kill-tail frames for other buckets) = 27 false-magic splits = **425,074 rows lost from bucket a.b5 only** → b50-region folds read incomplete leaf history → wrong subtree hashes; records (written straight to MDBX, no spill) stayed correct, which is why boundary proofs passed. The nailed instance: leaf `b5000794…` was missing its block-5416733 version, so the fold floored to the older 4729358 value (wrong balance) — a wrong-VALUE leaf, invisible to membership hypotheses (EIP-158-empty refuted: `leaf-audit b50007 @8388607` = 6 EOAs, 0 empty). **Fix (code)**: `finalizeBucket`/`finalizeBucketExternal` heal-walk — extend the frame end across candidates until the slice decodes; only genuinely truncated kill-tail frames remain corrupt. **Fix (data)**: re-finalize damaged buckets from the retained spill (`D:/n42-datc-eth25m-v5-leafspill-bak`, = `D:/datc-spill-done`, byte-identical) — a.b5 healed to 19,837,751 rows (0 lost), segment swapped (old kept as `a.b5.seg.pre-heal-bak`). **Verified**: `leaf-audit` fold==record at b50007/b500/b50 (16,395 leaves), and a **non-boundary account proof at 8388608 VERIFIED against header root** (the original failing class). Remaining sweep: re-finalize the other 123 backed-up buckets (a.08/10/58/cb/da/dd/e0, s.*, cs.*/ca.*) and swap any whose row count grew. Tools: `n42-datc leaf-audit` (per-node leaf provenance + subset folds vs record), `n42-datc spill-heal` (heal-decode a .zspill, diff vs segment, emit healed copy). NOTE: earlier "root cause is foldAt" (below, kept for history) was a misread of the same evidence — record≠fold at a boundary means the DATA under the fold is wrong, not the fold. The account **leaf-fold** (`foldAt` → `trie.GenStructStep` + `HashBuilder`, verify.go:734) yields a subtree hash ≠ the build's HPH record **for specific subtrees**. **Decisive test** (`xcheck --dump-acc <path> --at 8388607`, an all-level BOUNDARY, `recEpoch==curEpoch`, `steppedBack=false`, `len(changed)=0` — pure record, no window-replay): for **b50** every depth-4 child shows `base` (record = HPH, correct — the proof verifies via it) **≠** `fold` (leaf-fold). So the leaf-fold is wrong **at the boundary too**; boundary proofs pass only because they read records and never fold. Non-boundary proofs fold ⇒ hit the wrong fold ⇒ fail. It is **b50-specific, not systematic**: `--dump-acc 000/500/e00/abc @8388607` all show `base==fold` (fold correct). So NOT a leaf-value/encoding bug (would fail everywhere), NOT window-replay, NOT storage roots (identical boundary vs non-boundary), NOT deletions (block 8388608 changed 1 EOA under b50). **Divergence localization**: `b500` (depth-4) has 9/16 children fold-wrong (0,3,6,7,8,9,c,d,f) and 7/16 correct; `b50007` (depth-6) = 6 EOAs. **Remaining nail**: some specific leaf/structure under b50 makes `GenStructStep` build a different trie shape than HPH. Top hypotheses: an **EIP-158 empty account** (nonce=0/bal=0/empty-code) that `asOfLeaves` includes but canonical HPH excludes, or a value/structural edge case. Confirm by printing `nodeHashAt(sd)` (reconstructed storage root) + flagging empty accounts per leaf under b50 (leafdump's `root(rec)` shows only the uninformative decode default 61d9d84b). Also flag: the tool's `dumpAssemblyDom` panics at d≥6 (`epochOf` on a 6-entry `s.e`) — guard for d≥len(sched.e) to descend to leaf level.
- **Latent bug (flagged, NOT fixed — needs audit):** `common/account/state_account.go:60` `emptyRoot = types.BytesHash(crypto.Keccak256(nil))` is wrong (not the standard empty-trie root `56e81f17…`). It does NOT leak into the DATC fold, but it IS used by `IsEmptyRoot` and the `StateAccount` default across `modules/state`, `lib/commitment`, etc. — the tree currently produces canonical roots WITH this value, so changing it risks breaking empty-storage detection. Do not change without a full usage audit.

### 11.4 Node-history redesign — feasibility MEASURED (the path to fast arbitrary-N proofs)
`node-hist-size` (1/1000 sample, ×40B/version) over OUR forward changesets (`D:/N42-eth1177/chain/freezer/{acctcs,storcs}` — these carry NEW values; reth's carry OLD):
- **Shallow node-history is CHEAP, deep is EXPENSIVE** (opposite of the naive assumption): depth-1 **27 GB**, depth-2 +173, depth-3 +251, depth-4+ ~260/level. Shallow nodes are few (16, 256…) so few versions; deep nodes are per-change (saturate at the 13.3B leaf-change count).
- **⇒ dense SHALLOW node-history + fold the small deep subtree.** Cumulative account-side: depths 1-3 ≈ **311 GB** (→ ~seconds), depths 1-4 ≈ **500 GB** (→ **~166ms at ANY N**, cascade eliminated). Feasible (< the 774 GB tree), **NOT the TB** a dense-all-depths layer would cost.
- Reuse what exists: `internal/history` (by-key packed timeline, `AsOf` O(log) — the LEAF temporal layer already done), `internal/cscompact` (block→key changeset inversion), the current trie. Missing = the same for NODES, clustered by (subtree, block).
- Physical target: set MDBX page + NTFS cluster to 8 KB; a 16-way branch ≈206B avg (~38/8KB), account leaf ~80-110B (~90/8KB); pack "branch + its 16 children" (~2.6KB) or a leaf-prefix run per page. Co-location alone is only ~1.5-2× (removes I/O + cgocall, not the node count); the win needs co-location + versioned reads (read O(depth) node-versions at N, no cascade).

### 11.5 "A" roadmap (fast arbitrary-N proofs) — sequenced
- **M1 (fold correctness)** = repair the leaf history (bug #2 = finalize false-magic data loss, NOT window-replay/foldAt — both exonerated; see 11.3). Status 2026-07-01: heal-walk landed in finalize, a.b5 healed + verified (non-boundary proof @8388608 PASSES), sweep of the remaining 123 backed-up buckets running. Exit gate: sweep done + `verify` sampling that includes NON-boundary heights.
- **M2 — SMALL-RANGE DONE 2026-07-01; design locked as B′.** The 32s cascade's ACTUAL mechanism (mismeasured before): a changed-child recursion hits a record with `hasHash ⊂ hasState` (erigon collector convention: a child with its own deeper record — hasTree — does not store its hash in the parent; a state-only child is left for the loader to recompute), and `branchSlotsAt` treated ANY mixed mask as `!usable` → folded the ENTIRE depth-3 subtree per changed child. Two query-side fixes (verify.go): (1) hasTree-without-hash children recurse `nodeHashAt`; (2) account-trie state-only children fold PER CHILD (any account-trie node RLP ≥33B ⇒ always a hashed ref — safe; storage tries can inline <32B leaves, so they keep the whole-node fold). Diagnosis via `DATC_FOLD_STATS=1`. **These fixes alone take v5 off=1 from 52s → 11.4s VERIFIED, no rebuild.** B′ design: account trie dense at depth-3 only (`--sched 64,1024,16384,1,4194304,4194304`, ~183 GB full-chain) + **DatcStoRoot** dense storage-root history (`ah32|blk8→root32`, ~70 GB; `HashBuilder.AccRootEmitter` hook surfaces every folded contract's storage root per block; meta `stoRootFrom=0` makes a MISS authoritative "no storage" — zero probes for EOAs). Total ≈ **253 GB**, vs 611 GB for naive all-dense (which was flat only because e[0..1]=1 accidentally densified shallow STORAGE records too). 2M validation: build 18.5min/12 GB (per-block gold check), full offset matrix **36/36 PASS, random-height median 281ms max 574ms**. Constraints surfaced: dense e[3]=1 is incompatible with window mode (W=e[1] divisibility) ⇒ full chain needs per-block ComputeRoot (bpp measured ~6-10h for 25M) with `--window=false`; MDBX row overhead ~3-4× ⇒ full-chain record/StoRoot layers should be packed segments. **fastEOA is UNSOUND** (verified failure at v5 8388608): a contract whose init SSTOREs then returns empty code has empty codeHash WITH storage — diagnostic use only.
- **M3** proof reads shallow via records + dense depth-3 + StoRoot point lookups (no whole-subtree folds) — validated at 2M; full-chain re-measure after M2 build.
- **M4 — DESCOPED per slot-level measurement**: slot-level dense sdepth-0 (the DatcStoRoot layer, 70 GB) is IN M2; deeper slot-level (sdepth 0..1 = 300 GB, 0..2 = 617 GB) is not built — WETH storage proofs measured 388ms(boundary)/1.7s(off=16384) without it.

### 11.6 Diagnostic tooling added (cmd/n42-datc)
`fold-bench`, `node-hist-size`, `chg-at`; proof flags `--time --want-root --cpuprofile` + `N42_DATC_PROOF_DEBUG`; `internal/mptproof/acceptance_50_test.go` (50 reth-native storage proofs). Fork worktrees carry `xcheck` (`--fd-diff --dump-acc --leafdump --stor-prefix --fold-self …`) + fix #1 — branches `worktree-agent-a72760a4e824728aa` (latest, has fix #1 + full xcheck) etc.
