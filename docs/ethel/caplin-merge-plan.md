# Caplin Merge Plan — Sync from `../erigon/cl`

**Date:** 2026-05-22
**Status:** Plan only — actual merge tracked separately

User asked to sync the latest Caplin code from `../erigon/cl` into
N42's `internal/cl/`. This doc captures scope, risks, and
recommended approach. **No code changes made by this commit.**

## Current state

| Metric | `../erigon/cl` | `internal/cl` (N42) |
|:--|--:|--:|
| Go files (no tests) | 558 | 183 |
| LOC | 95,940 | 35,200 |
| Last touched | 2 days ago (419521c1b3) | several months ago (f7fba4ab, `cmd/eth-el` initial wire) |
| Top-level packages | 24 | 14 |

## Package-level diff

Packages present in `../erigon/cl` but NOT in `internal/cl`:

```
aggregation       — attestation aggregation
agents.md         — markdown doc
clstages          — staged CL pipeline
coverage          — beacon state coverage
das               — Data Availability Sampling
gossip            — beacon gossip topics
p2p               — beacon-specific libp2p layer
persistence       — beacon block persistence
pool              — operations pool (attestations, slashings, etc.)
rpc               — beacon RPC layer
sentinel          — peer-management sentinel
spectest          — Eth2 spec conformance tests
validator         — block builder + duty manager
```

Packages present in `internal/cl` but NOT in `../erigon/cl` (N42
adapters added when bringing Caplin in originally):

```
depshim           — dependency shim (lets Caplin compile against N42 base libs)
eladapter         — execution-layer adapter (binds Caplin to internal/ethel)
kvadapter         — KV adapter (binds Caplin to N42's lib/kv instead of Erigon's kv)
```

Packages present in both with likely substantial drift:
```
abstract  beacon  clparams  cltypes  fork  merkle_tree  monitor
phase1    ssz     transition  utils
```

## Scope estimate

| Bucket | Work |
|:--|:--|
| Import path rewrites | ~5,000–10,000 lines change (`github.com/erigontech/erigon/cl/...` → `github.com/n42blockchain/N42/internal/cl/...`) |
| New package onboarding (12 packages above) | each needs depshim/kvadapter wiring; ~60,000 LOC to triage |
| Existing-package drift resolution | per-package 3-way merge: take erigon, layer N42 adapter changes back on top |
| Dependency closure | erigon pulls in modules N42 doesn't have yet — for each, decide replace vs port |
| Build-tag preservation | every new file must be guarded by `// +build n42el` so default `cmd/n42` build still has zero Caplin deps |
| Compile + test | full `make ci` cycle, fix breakage |
| `cmd/eth-el` re-wire | new Caplin APIs may have changed; adapt the n42el-tagged glue |

**Realistic effort: 5–10 working days** (sequential, given how
intertwined the import-path rewrites + dep-shim glue can be).

## Three viable strategies

### Strategy A — Full overwrite (replace `internal/cl` wholesale)

1. `rm -rf internal/cl`
2. `cp -r ../erigon/cl internal/cl`
3. Add depshim, kvadapter, eladapter back as overlay layer
4. Run `goimports -w -local github.com/n42blockchain/N42` for path rewrites
5. Iterate compile errors

**Pros:** straight-line work, ends with a known-good state matching
upstream
**Cons:** loses any N42-specific patches we made inside Caplin packages
(audit needed before deletion)
**Risk:** if N42 had ANY non-adapter modifications to `abstract/`,
`beacon/`, `phase1/`, etc., they're silently dropped

### Strategy B — Selective per-package merge

For each of the 11 shared packages, do a 3-way diff:
- common-ancestor = the f7fba4ab snapshot of erigon at that time
- ours = current N42 `internal/cl/<pkg>`
- theirs = current erigon `cl/<pkg>`

Resolve conflicts package by package. For the 13 new packages,
copy them in and add depshim/kvadapter as needed.

**Pros:** preserves N42-local patches; principled
**Cons:** much more careful work; need to identify the common ancestor
**Risk:** package dependency cycles between old + new can surface late

### Strategy C — Selective import (only the bits we use)

Identify which of the 13 new packages `cmd/eth-el` actually needs.
Many (sentinel, p2p, gossip, validator) only matter if N42 wants to
be a full beacon node — but `cmd/eth-el` is an EL, so it might only
need cltypes / beacon / clparams updates.

If we don't run a full beacon node, we can skip ~70% of the new
packages.

**Pros:** smallest delta; ship fast
**Cons:** falls behind upstream more on the next sync
**Risk:** if scope creeps to "we want a full beacon", we redo the
work anyway

## Recommended path

**Strategy C first (this week), Strategy A in 6-12 months when
Caplin needs major catch-up.**

Justification:
- Current scope of `cmd/eth-el -tags n42el` is "EL + minimal embedded
  CL for testnet experimentation". We don't run a beacon node.
- The packages we actually consume (verified by import graph) are
  probably `cltypes`, `beacon` (state), `clparams`, `fork`,
  `transition`, `phase1` — maybe 7 of the 14 we have.
- Erigon's beacon evolves fast (DAS landing, post-Pectra hardforks)
  — selective imports let us pull just the parts that matter when
  they matter.
- Full sync (Strategy A) is justified when we commit to running a
  full beacon node, which would be a strategic product decision, not
  a casual sync.

## Concrete proposal — Strategy C steps

1. **Audit import graph** (1 day):
   ```bash
   go list -deps ./cmd/eth-el/... | grep "internal/cl" | sort -u
   ```
   Produces the actual set of consumed packages.

2. **Per-consumed-package sync** (1-2 days each):
   - Diff erigon's version vs ours
   - Apply upstream changes to N42 copy
   - Preserve depshim/kvadapter wiring
   - Re-run `go build ./cmd/eth-el/... -tags n42el`
   - Fix compile errors

3. **Skip unused packages** (no work):
   - aggregation, agents.md, clstages, coverage, das, gossip,
     p2p, persistence, pool, rpc, sentinel, spectest, validator
     remain not-imported.

4. **CI gate** (1 day):
   - Ensure `make ci` still passes for default build
   - Ensure `make ci -tags n42el` builds + tests pass

Total: **5-8 working days** for Strategy C, lands sustainable
incremental sync model.

## What I need from you before starting

| Question | Why |
|:--|:--|
| Is the goal "stay current with erigon Caplin updates" or "we want feature X from latest erigon"? | Drives Strategy C vs A choice |
| Are we running `cmd/eth-el -tags n42el` in production or is it still experimental? | If production, every sync needs a regression run; if experimental, we can be looser |
| Is there a target erigon SHA to sync to, or just "latest"? | Latest erigon has DAS pre-Fusaka; we may want a stable tag instead |

## Not in scope of this doc

- Actual code changes — pending direction confirmation
- Erigon side: any changes WE need to upstream (none expected; we
  are a downstream consumer)
- `cmd/n42` (the native chain binary) — has 0 internal/cl deps by
  design; unaffected by any Caplin merge

## Companion documents

- `docs/ethel/devlog-eth-el-node.md` — original Caplin import history
- `docs/ethel/sync-protocol-comparison.md` — eth/68 + 12s sync analysis
- `docs/ethel/post-bootstrap-sync-plan.md` — catch-up + live sync flow

---

## 2026-06-06 更新 — 决策:A 关键路径 + 逐包增量(≈ Strategy B 子集)

**完整性基线(已测):** `go build -tags n42el ./internal/cl/` ✅;**67 测试包全 PASS**(transition-eth2/forkchoice/phase1-core-state/ssz/bls/cltypes/das/merkle_tree…)→ 核心共识健全,缺的是 sync-loop wiring + 版本追平,不是算法。N42 现 ~63K LOC(5-22 时 35K,已涨)。

**关键发现:A 的核心 `phase1/stages`(N42 完全缺,erigon 2,395 LOC)级联依赖整个落后集** —— import antiquary、beacon/{synced_data,beaconevents}、das、persistence/{beacon_indicies,blob_storage}、phase1/network、execution_client/block_collector、validator/attestation_producer、forkchoice(✓)、rpc、freezeblocks。⇒ 即使只走 A,stage loop 也要 bottom-up 移植大部分落后包。

**Import 重写映射(depshim 层,已对照 N42 已移植包确认):**
- `erigon/cl/*` → `internal/cl/*`
- `erigon-lib/{common,clonable,hexutil,log/v3,ssz}` → `internal/cl/depshim/*`
- `erigon-lib/kv` → `lib/kv`;`erigon/db/{datadir,snapshotsync/freezeblocks}` → N42 对应/shim
- 移植 = 拷贝 + sed import + 补 depshim 缺口 + `go build -tags n42el` + 测试。

**Bottom-up 逐包顺序(底→顶,每步 build+test 绿了再下一包):**
1. persistence(beacon_indicies、blob_storage)
2. beacon(synced_data、beaconevents)
3. das、antiquary 补全
4. phase1/network、validator/attestation_producer、execution_client/block_collector
5. **phase1/stages**(ConsensusClStages,核心)
6. sentinel 服务(5→34,缺顶层 Sentinel + StartSentinelService)+ p2p(0→12)
7. wiring:仿 erigon cmd/caplin/caplin1/run.go 把 forkchoice+sentinel+ClStages 接进 service.go Start()(现只开 MDBX)
8. #32 对接各 EL(eladapter 14-method ✓ → Engine API → catchup→12s live)
9. 完整性测试:checkpoint sync E2E(+ 可选 spectest)

**规模:~33–43K LOC,多 session。** spectest 可延后。

**mobile(#33)独立**:去 header、仅 anchors + IDC witness,不自验 beacon 共识 → **不依赖 caplin 全量追平**,可与 #31 并行。
