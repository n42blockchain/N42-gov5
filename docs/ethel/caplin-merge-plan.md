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

---

## 2026-06-06 (c) — Caplin 数据量评估(决定 minimal 是否嵌入共识)

用户问题:minimal 档(snapshot 25.7 GB)是否值得嵌入 caplin 来做 **Sync to tip / Consensus
validation / P2P**;eth_getBalance(latest)/eth_call(latest) 已由 EL snapshot 提供。
核心要先量化 **caplin 要多少盘**,再决定。

### 实测/规格数字

| 项目 | 数字 | 来源 |
|:--|--:|:--|
| **全历史 beacon 区块归档**(caplin snapshots,genesis→tip,压缩) | **20.1 GB / 128 文件** | erigon `seg du` 实测(`docs/plans/.../seg-du-command.md`) |
| 单 validator 在 BeaconState 占用 | ~137 B(pubkey48+wc32+effbal8+slashed1+4×epoch32 = 121,+balances8+inactivity8) | cltypes.Validator SSZ |
| mainnet validator 数 | ~1.06–1.08M | 公开链上 |
| → registry+balances+inactivity 小计 | ~145 MB | 上两行 |
| **单个 finalized BeaconState**(SSZ 全量) | ~180–230 MB;**snappy ≈ 100–150 MB** | 上 + historical_roots/randao 等定长向量 |
| snapshot 退休安全边界 | finalized 后 `safetyMargin = 20_000` 块 | `cl/antiquary/antiquary.go:43` |

**关键区分:checkpoint-sync ≠ 全历史归档。**
- **全历史 caplin = 20 GB**(每个 beacon 区块 genesis→今)。只有"共识档案"用途才需要。**任何 EL 档(minimal/full/archive)都不需要它。**
- **Checkpoint-sync(弱主观性)只需:** 1 个 finalized BeaconState(**~150 MB**)+ 未 finalize 窗口的最近区块(~2 epoch,极小)+ 之后每 12s 一个 slot(可剪枝)。热盘稳态 ≈ **几百 MB**。

### 能力 → 需求 → 数据增量(minimal 视角)

| 能力 | 需要什么 | 数据增量 | minimal 结论 |
|:--|:--|--:|:--|
| eth_getBalance/eth_call(latest) | EL snapshot(已在 minimal) | 0 | **已具备** |
| Sync to tip(确定 head) | caplin checkpoint-sync **或** 外部可信 head | ~150 MB **运行时拉取**(不进 torrent) | 可嵌入(数据可忽略),**卡在 #31 代码** |
| Consensus validation(自验 head,零信任) | caplin(fork choice + state transition) | 同 ~150 MB | #31 落地后 **YES**;之前用可信/finalized checkpoint head |
| P2P(beacon gossip/req-resp) | sentinel + gossip(#31 步骤 6) | 盘 ~0,主要是带宽 | 可选;checkpoint-sync 也能走 req/resp 取块 |

### 决策

1. **数据不是瓶颈。** 把 caplin checkpoint-sync 嵌进 minimal/full,只增 **~150 MB 运行时** beacon
   state(从 checkpoint-sync 端点拉,**不打进 BitTorrent 分发包**),相对 25.7 GB snapshot 可忽略。
   ⇒ **manifest/torrent 选择器无需为 caplin 加任何文件**(已在 (a) 完成,无需改)。
2. **20 GB 全历史 beacon 归档** 仅服务"共识档案"用途,**不纳入任何 EL 档**;若将来要,做成独立可选
   add-on(类比 senders +38 GB)。
3. **共识验证的真实成本是 #31 代码移植(~33–43K LOC),不是盘。** #31 落地前,minimal 跟 tip 的方式
   = EL devp2p + 一个可信/finalized head(checkpoint hash),与 mobile 同模型(信任锚 + 流式跟随)。
4. **配置面:** minimal/full 的 config 增一个可选 `beaconCheckpointURL`(弱主观性 state 来源)。无 URL
   时退化为"信任 head 源"模式(当前行为)。

**一句话:** caplin 嵌入 minimal 在数据上几乎免费(~150 MB 运行时,0 分发包增量),值得做;门槛是
#31 的代码移植而非存储。EL 三档分发包维持 (a) 的组成不变。

---

## 2026-06-06 用户决策 + #31 移植进度

**用户拍板(超越上面 (c) 的 #1/#2 运行时方案):**
- **minimal/full 把 caplin checkpoint-sync seed 打进分发包**(finalized BeaconState,~150 MB)→ 选择器
  加 `beacon-checkpoint` 段(`caplin/checkpoint/state.*.ssz.zst`,provisional)。
- **archive 装全历史 beacon 归档(极致压缩)** → 选择器加 `beacon-archive` 段
  (`caplin/beacon-archive.*.zst`+idx)。20 GB erigon caplin 快照再压。
- 顺序:**#29 完成 → #31 → #32**。(#29 torrent 工具已完成并实测,见 RELEASE.md。)
- 文件由 #31 产出;在产出前 manifest 把这两段记为已知 gap(selector test 已更新)。

**依赖排序的移植清单(Explore 走 erigon `cl/phase1/stages` import 图得出,bottom-up):**

| Phase | 包/文件 | N42 现状 | 状态 |
|:--|:--|:--|:--|
| 1 | `persistence/state/{slot_data,epoch_data}.go` | 缺 | **✅ DONE**(commit c77d7bb2,测试绿) |
| 0/2 | **snapshotsync 基础**:`lib/seg`、`lib/chain/snapcfg`、`lib/downloader/snaptype`(主仓缺,仅在 agent worktree)+ `db/snapshotsync/{caplin_state_snapshots,caplin_snap_schema}.go` + `freezeblocks/caplin_snapshots.go`(~1637 LOC) | 缺(`lib/recsplit` 已有) | **下一步,硬地基** |
| 3 | `persistence/state/state_accessors.go`(依赖 `snapshotsync.CaplinStateView`)+ `historical_states_reader/*` | 缺 | 待 Phase 2 |
| 3 | `persistence/blob_storage/{blob_db,data_column_db}.go`(N42 只有 interface.go) | 缺实现 | 可与 Phase 2 并行(依赖少) |
| 4 | `phase1/execution_client/block_collector/*`、`phase1/network/{beacon,backward_beacon,blob}_downloader.go` | 缺/不全 | 待 Phase 2 |
| 5 | `antiquary/{antiquary,beacon_states_collector,state_antiquary}.go`(N42 只有 utils.go) | stub | 待 3+4 |
| 6 | `phase1/stages/*.go`(ConsensusClStages 同步循环,~2711 LOC)**关键路径** | 缺 | 最后,待全部 |
| 7 | wiring:仿 erigon `cmd/caplin/caplin1/run.go` 把 forkchoice+sentinel+ClStages 接进 `service.go Start()`(现只开 MDBX) | — | #32 |

**关键判断:** `db/snapshotsync`(caplin 部分)是 state_accessors / 下载器 / antiquary 的**共同硬地基**,
且它依赖 `lib/seg`/`snapcfg`/`snaptype`(主仓尚无,需先从 worktree/erigon 落地到主仓)。这是下一个 session
的主攻点,规模最大、风险最高。移植纪律:**每包 `go build -tags n42el` + 测试绿再下一包**。
