# Caplin Phase 7 — Implementation Plan

**Status:** Planning + Phase 7.1 sub-task started 2026-05-23
**Goal:** N42 自己跑 Ethereum CL — `cmd/eth-el --caplin.enabled=true`
能 checkpoint-sync mainnet beacon → 推 newPayload → eth-el 真链 catch-up
+ 12s live，不依赖外部 Lighthouse/Prysm。
**Effort estimate:** 6-12 weeks (multi-session work).

## 现状盘点（2026-05-23）

### N42 `internal/cl/` 已有

```
abstract/    beacon/    clparams/    cltypes/    coverage/    depshim/
eladapter/   fork/      kvadapter/   merkle_tree/ monitor/    phase1/{core,execution_client}
service.go (Phase 6 stub — Start() 只开 MDBX)
ssz/         transition/ utils/
```

### 缺失（Strategy A wholesale 时被删）

```
phase1/forkchoice/   — head 决策 + LMD-GHOST + checkpoint
phase1/network/      — beacon p2p protocol handlers + sync committee
phase1/stages/       — sync 状态机
p2p/                 — libp2p config + gater + transport
gossip/              — gossipsub topic + 序列化
sentinel/            — discv5 + ENR + beacon p2p mesh entry point
clstages/            — stage loop framework
antiquary/           — beacon historical antiquary (segment 化历史 archive)
aggregation/         — attestation aggregation
pool/                — operations pool (slashings, exits, attestations)
das/                 — data availability sampling
persistence/         — beacon block + state persistence
gossip + rpc + sentinel_proto + ...
```

### eladapter stub 现状

| Method | 状态 | 真实化优先级 |
|---|---|---|
| `IsCanonicalHash` | ✓ wired | — |
| `HasBlock` | ✓ wired | — |
| `Ready` | ✓ wired | — |
| `NewPayload` | stub | **P0** (sync 关键路径) |
| `ForkChoiceUpdate` | stub | **P0** (head 决策) |
| `CurrentHeader` | stub | **P0** (Caplin 读 EL head) |
| `InsertBlock(s)` | stub | P1 (历史 import) |
| `GetBodiesByRange/Hashes` | stub | P1 (backfill) |
| `GetAssembledBlock` | stub | P2 (block production；validator 自跑才用) |
| `GetBlobs` | stub | P2 (4844 blob store) |

## N42 ↔ Erigon 架构分歧（影响 InsertBlocks 实装）

`../erigon` 上游 `ExecutionClientDirect.InsertBlocks` 调
`chainRW.InsertBlocks` → `executionModule.InsertBlocks` — **纯存储**
（写 block 到 chaindata，状态 stage 后续异步执行）。靠 staged-sync 的
execution stage 解耦。

N42 没有 staged-sync 这层解耦：
- `internal/ethel/executor` 是 batch replayer（freezer changeset → state）
- `EngineStateAdapter` 一站式 execute+commit
- 没有 "header/body 写好但 state 还没算" 的中间态

务实决策：
- **N42 InsertBlocks 走 NewPayloadV4 loop**（在 ethELBackend 内每 block
  转 ExecutionPayloadV4 + 调 `EngineAPIv4.NewPayloadV4`）
- 与 erigon 行为不同（更严格：每 block 必须 spec-compliant + state
  root 必须匹配）；与 N42 现有 single-step 架构一致
- 如未来要支持 "存储不执行" 历史 import 加速 backfill，需要先建 stages
  框架（Phase 7.3+）

`SupportInsertion()` 保留 `false` 直到 InsertBlocks 经 mainnet 验证
（强制 Caplin 走 NewPayload 路径不冒重复执行风险）。

## EL 侧实装参考来源

按 user direction `2026-05-23`:
- CL（Caplin）— 严格参考 `../erigon/cl/*` 实现
- EL — N42 现有 `internal/api/*` + `internal/ethel/*` 为主；
  `../reth` (Rust) 提供 staged-sync 替代设计参考；
  `../erigon/execution/*` 提供 executionModule 借鉴

## Phase 拆分

### Phase 7.1 — eladapter 全实装 (1-2 weeks)

不依赖 phase1/* 子包，先用现有 `internal/api/EngineAPIv4` + `EngineStateAdapter` 实现 in-process Engine API call。

**子任务**:
- [ ] 7.1.1 `NewPayload(blocks)` → `EngineStateAdapter.ExecutePayload` per block
- [ ] 7.1.2 `ForkChoiceUpdate(head, safe, finalized)` → `EngineStateAdapter.ForkchoiceUpdated`
- [ ] 7.1.3 `CurrentHeader()` → `modules/rawdb.ReadCurrentHeader + ReadHeader`
- [ ] 7.1.4 `InsertBlock(s)` → bridge to `internal/ethel/executor` historical import
- [ ] 7.1.5 `GetBodiesByRange/Hashes` → `rawdb.ReadBodies` decode
- [ ] 7.1.6 `GetAssembledBlock` → `EngineAPIv4.GetPayloadV3` (defer if no producer)
- [ ] 7.1.7 `GetBlobs` → blob store (defer — Phase 7.2.5)
- [ ] 7.1.8 单元测试 + 集成测试 (mock CL → eladapter → real EL)

完成后效果：phase1 stage loop 一旦回来，就有 working ExecutionEngine 接口。

### Phase 7.2 — 拉回 phase1/forkchoice (2-4 weeks, multi-session)

`../erigon/cl/phase1/forkchoice` 总规模：**36 文件 / 8,531 行**。
forkchoice.go 主入口 988 行。依赖图（cherry-pick 顺序按依赖深度）：

**Tier 0（叶子，无 forkchoice 子包内依赖）**：
- `types.go` (38 行) — LatestMessage / ForkChoiceNode struct
- `interface.go` (144 行) — ForkChoiceStorage{Reader,Writer} 接口

**Tier 1（依赖 Tier 0 + N42 外部包）**：
- `latest_messages_store.go` — 投票 store
- `weight_store.go` / `weight_store_indexed.go` — block weight 缓存
- `checkpoint_state.go` — checkpoint cache
- `payload_vote.go` — payload vote tracking
- `optimistic/` — optimistic block tracking
- `public_keys_registry/` — validator pubkey cache

**Tier 2（依赖 Tier 1）**：
- `on_attestation.go` — Caplin 收 attestation 时调
- `on_attester_slashing.go`
- `on_block.go` — 收新 block 时调
- `on_execution_payload.go` — 收 EL payload 时调
- `on_payload_attestation_message.go` (Gloas)
- `on_tick.go` — slot tick 时调
- `get_head.go` — LMD-GHOST head 计算
- `blob_sidecars.go`
- `timing.go`

**Tier 3（顶层入口）**：
- `forkchoice.go` (988 行) — 综合所有上面

**Tier 4（fork graph 子包）**：
- `fork_graph/` — 11 文件，beacon state diff graph

**N42 外部依赖（需先确认存在或一起 cherry-pick）**：
- ✓ `internal/cl/cltypes` + `cltypes/solid` (已有)
- ✓ `internal/cl/clparams` (已有, 注意 Gloas 字段差异)
- ✓ `internal/cl/phase1/core/state` (已有)
- ✓ `internal/cl/phase1/execution_client` (已有)
- ✓ `internal/cl/transition/impl/eth2` (已有)
- ⊘ `internal/cl/das` — **N42 缺**；is required by interface.go (GetPeerDas). 需先 cherry-pick `../erigon/cl/das` (估 5-10 文件) 或临时从接口移除 PeerDAS 相关方法
- ⊘ depshim/common.Hash vs cl/common.Hash — 所有 cherry-pick 文件需 import 路径替换

**Cherry-pick 子任务**（按依赖顺序）：

| Sub | 工作 | 估时 |
|---|---|---|
| 7.2.0 | cherry-pick `../erigon/cl/das` → `internal/cl/das` + 适配 | 3-5 天 |
| 7.2.1 | Tier 0: types.go + interface.go cherry-pick + import 适配 | 半天 |
| 7.2.2 | Tier 1: 6 个 store/cache 文件 cherry-pick | 2-3 天 |
| 7.2.3 | Tier 2: 9 个 on_*/get_head/timing 文件 cherry-pick | 4-5 天 |
| 7.2.4 | Tier 3: forkchoice.go (988 行) cherry-pick | 2-3 天 |
| 7.2.5 | Tier 4: fork_graph 子包 (11 文件) cherry-pick | 3-4 天 |
| 7.2.6 | 所有 *_test.go cherry-pick + 跑通 | 3-5 天 |
| 7.2.7 | MDBX 表 schema 加 forkchoice store 持久化 | 2 天 |
| 7.2.8 | 单元测试 PASS gate + lint | 1-2 天 |

**总估 18-29 天（4-6 周持续 work，跨多个 session）**。

### Phase 7.2 启动建议

不要一次性 cherry-pick 36 文件然后期望编译过 — 会 stuck 在 import cycle/type drift。
反而：**Tier-by-tier**，每 Tier 一个 commit + 单元测试 + 编译验证。Tier 0
是 1 day work，可以作为下一 session 第一步。Tier 0 commit 后我们就有
`internal/cl/phase1/forkchoice/{types.go,interface.go}` baseline，后续 Tier 1+
工作者可以照葫芦画瓢。

### Phase 7.3 — 拉回 phase1/stages (2-3 weeks)

CL sync 状态机：BeaconChainStage, HeadersStage, BeaconStateStage, SyncCommitteeStage 等。

**子任务**:
- [ ] 7.3.1 cp -r ../erigon/cl/phase1/stages → internal/cl/phase1/stages
- [ ] 7.3.2 cp -r ../erigon/cl/clstages → internal/cl/clstages
- [ ] 7.3.3 适配 staged_sync 框架到 N42 service 风格
- [ ] 7.3.4 接 forkchoice (Phase 7.2)
- [ ] 7.3.5 接 eladapter (Phase 7.1)
- [ ] 7.3.6 单元测试

### Phase 7.4 — 拉回 p2p + sentinel (2-3 weeks)

libp2p beacon p2p mesh + discv5 + ENR + GossipSub。

**子任务**:
- [ ] 7.4.1 cp -r ../erigon/cl/p2p → internal/cl/p2p
- [ ] 7.4.2 cp -r ../erigon/cl/gossip → internal/cl/gossip
- [ ] 7.4.3 cp -r ../erigon/cl/sentinel → internal/cl/sentinel
- [ ] 7.4.4 适配 N42 libp2p 配置（复用 internal/p2p 已有 libp2p 选项）
- [ ] 7.4.5 端口 9000 TCP + 9000 UDP 真 listen
- [ ] 7.4.6 mainnet bootnode ENR list 配置
- [ ] 7.4.7 单元测试 (mock peer mesh)

### Phase 7.5 — 拉回 phase1/network (1-2 weeks)

beacon p2p protocol handlers + sync committee + 投票 + attestation。

**子任务**:
- [ ] 7.5.1 cp -r ../erigon/cl/phase1/network → internal/cl/phase1/network
- [ ] 7.5.2 cp -r ../erigon/cl/{aggregation,pool,persistence,antiquary} (按需)
- [ ] 7.5.3 配 RPC protocol handler (beacon_chain_v1, status, goodbye, ping)
- [ ] 7.5.4 配 gossip 主题 (beacon_block, attestation_*, sync_committee_*)

### Phase 7.6 — Service.Start wire 起来 + E2E mainnet test (1 week)

**子任务**:
- [ ] 7.6.1 service.go Start() 加 stage loop 启动
- [ ] 7.6.2 checkpoint sync URL config + checkpoint sync 启动
- [ ] 7.6.3 E2E：从 mainnet beacon checkpoint sync → catch up beacon tip
- [ ] 7.6.4 E2E：beacon head 推 newPayload → eth-el catch up EL tip
- [ ] 7.6.5 12s live loop 验证
- [ ] 7.6.6 文档：cmd/eth-el --caplin.enabled 正式 production-ready

## 测试目标

- **Phase 7.1**：unit + integration tests 跑 mock CL → eladapter → real EL，验 NewPayload/FCU 端到端
- **Phase 7.2-7.5**：每子包拉回时跑 erigon 上游对应 unit tests
- **Phase 7.6**：E2E mainnet sync test —— 与外部 Lighthouse 对比 sync 速度 + final state root 一致

## 风险点

1. **N42 fork 与 erigon 漂移**：cltypes/clparams 已有差异（Gloas 等新 fork），cherry-pick 时要适配
2. **MDBX 版本差异**：erigon Caplin 用最新 mdbx-go，N42 锁定老版本（don't upgrade holiman/uint256 类规则）
3. **依赖循环**：phase1/{forkchoice, stages, network} 互相依赖；拉的顺序要按 dep graph 走
4. **测试基础设施**：erigon 用 ginkgo / testify，N42 主用 testing — 选一种风格统一
5. **mainnet bootnode + peer 发现**：discv5 网络可能 IP rate-limit，需用稳定 outbound

## 第一步交付（本 session）

Phase 7.1.1 — `eladapter.NewPayload` 真实化（最优先且无 phase1/* 依赖）。
设计：用 `internal/api/EngineStateAdapter`（已有真实现）做 in-process call，
不走 HTTP / JWT 一层，直接 Go 方法调用。

接下来 Sub-task 列在 task tracker。
