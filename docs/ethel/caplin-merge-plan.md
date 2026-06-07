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
| 2 | **snapshotsync 地基(改用 DB-fallback shim,见下)** + `persistence/state/state_accessors.go` | 缺 | **✅ DONE**(commit 4063d76a;+ build-tag 修复 35c58de2) |
| 3a | `persistence/state/historical_states_reader/*`(~1800 LOC)+ 10 个 Gloas/ePBS kv 表 + freezeblocks `BeaconSnapshotReader` 接口 | 缺 | **✅ DONE**(commit b916ef08,cl 全绿) |
| 3b | `persistence/blob_storage/{blob_db,data_column_db}.go` 真实现 | 只有 interface.go + Noop | **已延后**:Noop 对 follower 关键路径足够(同快照 shim 逻辑;真实现需 sentinel ssz_snappy) |
| 5 | `antiquary/{antiquary,beacon_states_collector,state_antiquary}.go`(~72KB)+ downloader/snaptype shim + Dump* fail-loud stub | stub | **✅ DONE**(commit 98266346;依赖 3a 满足,提前于 4) |
| 4a | `phase1/network/{beacon,backward_beacon,blob}_downloader.go` + blob_storage Verify… stub | 不全 | **✅ DONE**(commit d8cb154d) |
| 4b | `phase1/execution_client/block_collector/*` + depshim/types.Block 2 accessor + mdbx 适配 | 缺 | **✅ DONE**(commit 08e87433) |
| 6 | `phase1/stages/*.go`(ConsensusClStages 同步循环,~2711 LOC,6 文件)**关键路径** | 缺 | **✅ DONE**(commit 75dec3e5):8 个版本漂移符号全补齐,stages 编译通过,full cl 绿 |
| 7 | wiring:仿 erigon `cmd/caplin/caplin1/run.go` 把 forkchoice+sentinel+ClStages 接进 `service.go Start()`(现只开 MDBX) | 缺 | **下一步** → #32 |
| 7 | wiring:仿 erigon `cmd/caplin/caplin1/run.go` 把 forkchoice+sentinel+ClStages 接进 `service.go Start()`(现只开 MDBX) | — | #32 |

**关键策略转变(Phase 2):放弃整体移植 erigon 的 snapshot-distribution 子系统,改用 DB-fallback shim。**
erigon `db/snapshotsync`(caplin 部分)依赖 `snaptype`/`snapcfg`/`db.state SnapNameSchema`/`version`/
`node/ethconfig` —— 一大坨与 N42 自有 freezer/columnar 平行的外来机制。但 **所有 caplin 消费者
(state_accessors / antiquary / phase1/stages / 下载器)本就对"空快照"容错**:都 guard `BlocksAvailable()==0` /
`SegmentsMax()==0` / nil View,空时退回 MDBX + 网络读。⇒ 写一个 ~150 行的 shim
(`internal/cl/depshim/{snapshotsync,freezeblocks,ethconfig}`)处处报"无快照",把所有读路由到 DB,即
**解锁 checkpoint-sync + live-follow 关键路径,且不引入外来快照机制**。真正的历史快照
读取/构建器(archive 档 beacon-archive 用)是**后续独立移植**,关键路径不需要。

**已就位的 N42 基础库:** `lib/seg`(30 文件)、`lib/recsplit`(39 文件)、`lib/common/datadir`、
`lib/common/background`、`lib/kv/dbutils`、`lib/kv`(含全部 caplin 表:SlotData/EpochData/StaticValidators…)。
移植纪律:**每包 `go build -tags n42el` + 测试绿再下一包**。

### Phase 6 (stages) 版本漂移缺口清单(8 符号,2026-06-06)

stages 6 文件 import 重写干净,卡在 8 个符号(N42 子系统比 erigon 当前 stages 落后):

- **瘦类型/字段(低风险):**
  1. `lib/common/datadir.Dirs.CaplinHistory` — 加字段 + 构造处填充。
  2. `depshim/dbg.ReadMemStats` — 包 runtime.ReadMemStats。
  3. `depshim/engineapi/engine_types.PayloadAttributes.{SlotNumber,TargetGasLimit}` — 加 2 字段(Gloas)。
  4. forkchoice.go PayloadAttributes 的 `[]*depshim/types.Withdrawal` vs `[]*engine_types.Withdrawal` 类型不符 — 统一或转换。
- **真实共识逻辑(对照 N42 既有 API 谨慎加,误则共识 bug):**
  5. `forkchoice.ForkChoiceStore.GetFinalizedExecutionHash()` — finalized checkpoint 的 execution payload hash。
  6. `beacon_indicies.ReadCanonicalHead()` — 从 DB 读 canonical head root/slot。
  7. `checkpoint_sync.FetchFinalizedEnvelope()` — 取 finalized state envelope(Gloas,新)。

补齐这 8 个即可编译 stages,然后 Phase 7 wiring 进 `service.go Start()`(现只开 MDBX)。stages 文件已暂移出树以保持 build 绿;下个 session 先加 8 符号再落 stages。

### Phase 7 (wiring) 现实:卡在 sentinel/p2p/gossip 子系统(2026-06-07)

stages 已编译,但 `RunCaplinService`(erigon cmd/caplin/caplin1/run.go)把 ClStages **跑起来**需要的依赖链:
`ConsensusClStages` ← `ClStagesCfg(beaconRpc, …)` ← `beaconRpc = rpc.NewBeaconRpcP2P(sentinel, …)` ← `sentinel = service.StartSentinelService(…)` ← `cl/p2p.NewP2Pmanager` + gossip + services。

**N42 缺口(本质是整个 beacon P2P 栈,merge plan step 6):**
| 包 | N42 | erigon | 状态 |
|:--|--:|--:|:--|
| `sentinel` | 0 | 4 | 缺 |
| `sentinel/service`(StartSentinelService) | 0 | 2 | 缺 |
| `cl/p2p`(NewP2Pmanager) | 0 | 9 | 缺 |
| `phase1/network/services`(BlockService 等 16 个 gossip 服务) | 1 | 16 | 缺 15 |
| `phase1/network/gossip` | 2 | 6 | 半 |
| aggregation / das / rpc / attestation_producer | 2/2/2/2 | — | ✓ |

⇒ **ClStages 没有区块来源就跑不起来**(forward sync 经 beaconRpc 走 sentinel req/resp 取 beacon 区块)。sentinel/p2p 依赖 libp2p,是最大未移植子系统(~数万 LOC),不是"编辑 service.go"能完成的。

**架构岔路(需用户拍板):**
- **A. 移植完整 beacon P2P 栈**(sentinel + p2p + 15 gossip services):得到真正的 caplin beacon 节点;工作量大,多 session。
- **B. EL-follower 轻量接法**:EL 已用自有 eth/68 devp2p 同步 execution payload;caplin 只需提供 fork-choice/finality(喂 Engine API forkchoiceUpdated 的 finalized/safe)。不跑完整 ClStages beacon-gossip 循环,而是用 finalized checkpoint(checkpoint-sync HTTP,已就位)+ 轻量 forkchoice 驱动。这更贴合 minimal/full EL 档的"checkpoint-sync 嵌入"目标(~150MB beacon state),且避开整个 beacon p2p 栈。

stages 文件已就位(编译通过),两条路都用得上;先定方向再动手。

### Phase 7-B 完成(2026-06-07):轻量 EL-follower 已接驳

用户选 B。实现 `internal/cl/follower.go` + service.go 接线:`BeaconCfg.CheckpointSyncURL` 设了就在 `Service.Start` 起 `runFinalityFollower` —— HTTP checkpoint-sync 取 finalized beacon state → 读 execution payload block hash → 经 eladapter 驱动 EL Engine API `forkChoiceUpdated(finalized=safe=head)`,变化时才更新。EL 自己用 eth/68 devp2p 同步 payload,caplin 只钉 finality。**零 beacon P2P。** commit efc3a053(+ payload_convert 回归修复 一起)。

- 用 GetLatestBeaconState(全 SSZ state)→ execution header 跨所有 fork(含 Gloas)正确;刷新走保守间隔,TODO 换轻量 `/eth/v1/beacon/headers/finalized` poll 省带宽。
- full cl + eth-el 在 -tags n42el 编译通过;cl 测试(forkchoice/eladapter/beaconevents/transition/state…)全绿。

**#31 结论(option B 架构下完成):** 整个 caplin 组件栈(Phase 1-6)已移植+编译+测试,8 个漂移符号补齐,follower 接驳 EL(Phase 7-B)。完整 beacon-gossip 栈(sentinel/p2p/15 gossip services)按 B 决策**有意不移植**。剩 #32 = 把 CheckpointSyncURL 配进 minimal/full 档(mobile 走自有 SDK)+ 真实 endpoint 联调。

---

## 2026-06-07 — #34 对抗环境下的独立 fork choice(B+ 块-gossip 优先)

**动因:** option B(follower.go)在对抗环境下不安全 —— 两个可信单点:finality 信任单个 checkpoint endpoint;**head 盲跟 EL 自己 eth/68 devp2p 的 tip**(`driveForkChoice` 里 `head = eng.CurrentHeader()`),**完全不验证 attestation 权重**。控制你 EL peer 的攻击者可喂"执行有效但非 canonical(低 attestation 权重)"的链,follower 直接把 EL 驱动上去。runbook 末尾自承 "No independent fork choice"。

**已具备(不缺算法):** `get_head.go` 是真以太坊 fork choice —— LMD-GHOST + proposer boost + Casper FFG `filter_block_tree`(justified/finalized 过滤)+ Gloas,配 `on_block`/`on_attestation`/`fork_graph`/state transition,全移植 + 67 测试包绿。

**用户决策(2026-06-07):走 B+ 块-gossip 优先**(非完整 A,也非 C 多端点交叉验证)。只订阅 `beacon_block` gossip + 块 req/resp 回填 → `forkchoice.OnBlock` → `GetHead` → 用**独立选出的 head** 驱动 EL(替换盲跟 tip)。块内嵌 attestation 已足够跑 LMD-GHOST(finality + 近 tip 安全);活 attestation 子网(其余 14 gossip services)延后。约完整 A 的 40% 工作拿 ~80% 鲁棒性。

**传输层选型决策:路线 A(移植 erigon `cl/sentinel`+`cl/p2p`),非在 `internal/p2p` 上写 adapter。** 证据:
- 已移植消费侧 `phase1/network/{beacon,backward_beacon}_downloader.go` 已期望 `rpc.BeaconRpcP2P`;`internal/cl/rpc/rpc_stub.go:4-11` 明确写 "Phase 7.5 lands the sentinel subtree" —— 架构早已锁定 A。
- 已移植 sentinel 碎片(`communication/ssz_snappy`、`handshake`、`httpreqresp`、`peers`、`communication/topics`)与 erigon 源 >95% 兼容。
- `internal/p2p` 硬编码 N42 原生链 fork digest(`fork.go` n42ENRKey)/topic(`/n42/` 前缀)+ 用 protobuf 非 SSZ;改造逆水行舟。

**地基已核实:**
- libp2p:N42 `go-libp2p v0.47.0`/`pubsub v0.15.0` vs erigon `v0.48.0`/`pubsub v0.11.0` —— **N42 pubsub 更新**。erigon gossip/sentinel 写在 0.11 API,移植须适配到 0.15;**参照** = N42 自有 `internal/p2p/pubsub.go`(已在 0.15)。这是主要 friction 点。
- in-process:erigon `node/direct/sentinel_client.go` `NewSentinelClientDirect(SentinelServer)` 把 sentinel.Service 包成 in-process `SentinelClient`,**无需真起 gRPC server**。
- discv5/enode/enr:`internal/p2p/{discover(v4+v5+pq),enode,enr}` 齐全,depshim 可映射。
- seam 边界 = `sentinelproto.SentinelClient`(14 方法);块-only 路径只用 `SendRequest`(req/resp 取块)/`SubscribeGossip`(收 beacon_block)/`SetStatus`/`GetPeers`/`BanPeer`。`rpc.go:420` `SendRequest(&RequestData{Topic, Data})`。

**Bottom-up 移植顺序(每包 `go build -tags n42el` + 测试绿再下一包):**
1. `depshim/sentinelproto`:边界消息类型(RequestData/ResponseData/GossipData/Status/Peer/EmptyMessage/PeerCount…)+ `SentinelClient`/`SentinelServer` 接口(保留 `grpc.CallOption`,grpc 已在 go.mod)。**自包含,解锁 rpc + sentinel 两侧。**
2. `depshim/direct`:`SentinelClientDirect`(in-process 包装,~50 行)。
3. `cl/p2p`:libp2p host + discv5 + ENR(p2p.go/p2p_discovery.go/p2p_localnode.go/libp2p_setting.go/gater.go/msg_id.go/config.go/utils.go,~1224 行)。depshim 映射 `p2p/{discover,enode,enr,nat}`→`internal/p2p/*`;pubsub 0.15 适配。
4. `cl/sentinel` 核心:sentinel.go/gossip.go/discovery.go/config.go + `handlers/{handlers,blocks}.go`(块-only,排除 attestation/sync-committee/blob/data-column handlers ~3300 行)+ `service/{service,start}.go`(实现 SentinelServer)。
5. `cl/rpc/rpc.go`:真 `BeaconRpcP2P`(替换 rpc_stub.go),`SendBeaconBlocksBy{Range,Root}Req` 经 SendRequest。
6. `phase1/network/services/block_service.go`:gossip 收块验证 → `forkchoice.OnBlock`。
7. wiring `service.go Start()`:起 sentinel(direct)+ ClStages forward-sync + gossip → `GetHead` → 驱动 EL(替换 follower 盲跟 tip)。catch-up→12s live。
8. E2E 对抗验证:EL devp2p 喂低权重链时,caplin 用 fork-choice head 覆盖/拒绝。

**规模:~3K LOC 新增 + 多 session。** 主风险 = pubsub 0.11→0.15 漂移(有 internal/p2p 参照)+ depshim discover/enode/enr API 对齐。follower.go 保留为无 beacon-peer 时的退化路径。

### 进度(2026-06-07,session 1)

- **步骤 1 ✅** `internal/cl/depshim/sentinelproto/`:手写边界 shim(13 消息类型 + `SentinelClient`/`SentinelServer` 14 方法接口 + `Sentinel_SubscribeGossip{Client,Server}` 流接口)。简化:`Status.{Finalized,Head}Root` 用 `common.Hash` 不用 H256 wrapper;保留 `grpc.CallOption`(grpc v1.79.3 已在 go.mod)。`go build -tags n42el` ✅。
- **步骤 2 ✅** `internal/cl/depshim/direct/sentinel_client.go`:移植 erigon `node/direct/sentinel_client.go`,`NewSentinelClientDirect(SentinelServer)→SentinelClient`,SubscribeGossip 走 buffered-channel 桥接流。`go build -tags n42el` ✅。
- **步骤 3 cl/p2p 映射已勘定(降风险):** erigon `cl/p2p` 只从 `p2p/{discover,enode,enr,nat}` 用这些符号:`discover.{UDPv5,Config,ListenV5}`、`enode.{Node,LocalNode,NewLocalNode,OpenDBEx,Parse,ValidSchemes}`、`enr.{WithEntry,TCP,UDP,IP,IsNotFound}`、`nat.Interface`。核对 N42 `internal/p2p/{discover,enode,enr}` —— **95%+ 已有**,仅两缺口:① `enode.OpenDBEx`(N42 只有 `OpenDB`,加 1 行 wrapper 或改用 OpenDB);② N42 无 `p2p/nat` 包(stub `nat.Interface` 或映射)。`internal/p2p` 非 n42el-tag、始终编译,cl/p2p 可把 erigon import 直接重写到 `internal/p2p/*`,无需中间 depshim 层。
- **下一步:** 移植 `cl/p2p`(8 文件 ~1224 行,补 2 缺口 + pubsub 0.15 适配)→ `cl/sentinel` 核心 + `handlers/{handlers,blocks}.go` → `service/{service,start}.go`(SentinelServer)→ 真 `cl/rpc/rpc.go`(替换 rpc_stub)→ `block_service` → wiring → E2E。
