# N42 整体架构框架 + 逐步完成 Plan

> 整理日期 2026-05-29。本文汇总 N42 双模式(自研公链 + ETH 兼容)的下载/清洗/启用、
> caplin-CL、eth-el 三模式(minimal/full/archive)、catchup→live(12s)的现状,
> 区分 **已实现 / stub / blocker**,并给出逐步完成缺口的 Plan。
> 数字区分 **[文档]**(docs 规划/实测)与 **[实测]**(本会话亲测)。

---

## 0. 双模式总览

```
                    ┌─────────────────────────────────────────────┐
                    │  共用基础设施 (cmd/n42 与 cmd/eth-el 共享)      │
                    │  MDBX(lib/kv) · Freezer · libp2p P2P · RPC ·  │
                    │  TxPool · ContentStore                        │
                    └─────────────────────────────────────────────┘
              ┌──────────────────────────┴───────────────────────────┐
   ┌──────────────────────────┐                ┌──────────────────────────────┐
   │  A. N42 自研公链           │                │  B. ETH 兼容 (n42-eth)         │
   │  binary: n42              │                │  binary: eth-el  (+caplin)     │
   │  profile=n42              │                │  profile=eth / --hashed-canonical│
   │  共识 APoS/HotStuff        │                │  共识 EtHash/Faker             │
   │  承诺 JMT/BMT/Verkle       │                │  承诺 MPT(标准, 字节兼容)       │
   │  + AI/分布式运行时          │                │  caplin-CL + eth-el(3 模式)    │
   └──────────────────────────┘                └──────────────────────────────┘
```

区分键:`--profile` + `--chain`;数据目录强制隔离(`*.datadir.lock`)。
共识解析:`internal/node/node.go:1212-1285` resolveConsensusEngine。

---

## A. N42 自研公链:下载 / 清洗 / 启用

| 阶段 | 机制 | 代码 |
|---|---|---|
| **下载历史** | libp2p initialsync(round-robin RPC) + snapsync(选 pivot 下全量状态树) + checkpoint 断点续传 | `internal/sync/initialsync/`, `snapsync/`, `rpc_blocks_by_range.go` |
| **清洗数据** | JMT 迁移(PlainState→Blake3 JMT, 可崩溃恢复) · 状态分层(热→冷 freezer) · JMT 压缩 · 导出 | `cmd/n42/migratecmd.go`, `jmt_compact_cmd.go`, `internal/node/storage_tier.go` |
| **启用新链** | `n42 init --profile n42 --chain mainnet/testnet/private <genesis.json>` → WriteGenesisBlock → 共识 resolve | `cmd/n42/initcmd.go`, `internal/node/node.go:1272` |

快速启动:`n42`(主网 APoS) / `n42 --testnet` / `n42 --dev`(HotStuff-2 私链)。
N42 独占 AI/分布式/ZK 运行时(`internal/ai/`, `internal/distributed/`)。

---

## B. ETH 兼容侧 = caplin-CL + eth-el(三模式)

### B.1 eth-el 三模式对比 [文档]

| | **minimal ~39 GB** | **full ~682 GB** | **archive ~849 GB(默认)** |
|---|---|---|---|
| **启动** | `--snapshot.mode minimal --snapshot.source <URL>` | `--snapshot.mode full ...` | `--bootstrap.enabled=true --bootstrap.manifest <URL>` |
| **冷启动** | snapshot-direct(无重建) | snapshot-direct(无重建) | leaves-journal → RebuildState(~1h) |
| **下载** | accounts+storage snapshot(RecSplit/EF/zstd 28GB)+headerc+codes | +bodyc(567)+receipts(63)+accthist/storhist(41)+txindex(13) | +witness(167) |
| **协议** | HTTP / BitTorrent v1+v2 / WebRTC / libp2p(多源, manifest blake2b 校验) | 同 | 同 |
| **存储** | PlainState(warm,H₀+1起)+snapshot(H₀冷) | +history index+txindex+bodies/receipts | +完整 PlainState(genesis..tip)+witness(+本地可衍生 acctcs/storcs) |
| **能力** | tip 状态;无 tx/历史/proof | +历史状态+tx+收据;无历史 proof/trace | 全语义:任意高度 proof + trace + unwind + 欺诈证明 |

> ⚠️ **Blocker(task #94)**:目前 `--bootstrap.enabled` 只 wire 了 archive 的 leaves-rebuild;
> **minimal/full 的 snapshot-direct 续跑尚未接入**(需 snapshotreader + warm-tier overlay)。
> `cmd/eth-el/main.go:57-330`, `docs/ethel/real-chain-three-mode-runbook.md`。

### B.2 caplin CL 现状

| 层 | 状态 |
|---|---|
| Engine API in-process(NewPayloadV4 / ForkchoiceUpdated / body 读 / GetPayload) | ✅ **已实现**(Phase 7.1.1.b, `cmd/eth-el/beacon_backend.go`, 需 `-tags n42el`) |
| `cl.Service.Start()` sync loop / forkchoice / sentinel(beacon p2p) | ❌ **stub**(只开 MDBX, `internal/cl/service.go:72-90`; Phase 7.4-7.6 未做) |

现阶段两条可用路径:
- **External Lighthouse CL** → eth-el Engine API(`--engine.jwt`),生产就绪,`docs/ethel/external-cl-runbook.md`
- **eldevp2p**(本会话用):`--eldevp2p.enabled` 直连 mainnet devp2p 绕过 CL

### B.3 catchup → live(12 秒)[本会话实测 + 文档]

**Catchup(staged, `N42_STAGED=1`)**:
- writeOnly 执行(跳过 per-block root)+ 批量下载 reorder buffer + 每 sub-batch(默认 8192)一次 `MerkleStageIncremental` 增量算根验证
- 原子:块状态 + head marker 同 tx(commitInterval=256),kill 不裂口
- **性能**:dRoot 82ms→6ms;tExec 300→120ms/块(2.5×);**~270 Mgas/s** [文档]
- `internal/ethel/eldevp2p/downloader.go`, `modules/state/commitment/merkle_stage.go`

**切 live(`local >= target` → sleep 12s 轮询)**:
- 每 12s 一块走 `ExecutePayload`(非 staged, per-block full root + fsync, 3-5s/slot 预算内),由 CL(newPayload+FCU)或 eldevp2p 驱动
- `downloader.go:365-369`

---

## 现状速查

| 能力 | 状态 |
|---|---|
| N42 自研链 init/共识/AI 运行时 | ✅ |
| eth-el **archive** bootstrap + catchup | ✅ 实测 |
| eth-el **minimal/full** snapshot-direct | ❌ 未 wire(task #94) |
| caplin Engine API | ✅(`-tags n42el`) |
| caplin 自主 beacon sync | ❌ stub → 用 external CL / eldevp2p |
| staged catchup ~270 Mgas/s | ✅ |
| 12s live(CL/eldevp2p 驱动) | ✅ |
| parallel EVM 生产集成 | ⚠️ PoC(读可见性 blocker) |
| sub-batch unwind/bisect | ❌ stub(失败靠人工) |

### 本会话(2026-05-29)已完成基线

- **#150 FULLY CLOSED**:reth→N42 migration"不重算 root"遗留的 cached IH stale 修复
  (rebuild-trie verify-before-clear, cached 0x37469732→0x85bfede5)。根因=reth 节点
  无 root_hash/无 keylen-32 根记录,verbatim 拷贝触发 cursor reth-shape 兼容路径残留。
- **eth-el 实测追平 tip**:从 25,191,536 经 eldevp2p 追到 **25,199,323(~7,787 块, 1h23m, 零 mismatch)**,现 12s live。
- **migration 加固**:`cmd/n42-migrate-reth-hashed` 新增 `rtrie` phase(从 leaves 重建 trie,
  verify-before-clear),默认 phases `acc,sto,code,rtrie,head`,废弃 verbatim 拷贝的 tacc/tsto。

---

# Plan:逐步完成缺口

按依赖与价值排序。每项含 目标/前置/步骤/验收/估算。

## P1 — eth-el minimal/full snapshot-direct 接入(task #94) ★最高

**目标**:让 minimal/full 模式能 snapshot-direct 续跑(当前只有 archive 的 leaves-rebuild 可用)。
**前置**:无(snapshot 文件格式 RecSplit/EF/zstd 已存在)。
**步骤**:
1. 新增 `--bootstrap.mode = snapshot|leaves|none` flag(替代现在二元的 `--bootstrap.enabled`)。
2. 写 `internal/ethel/snapshotreader`:RecSplit MPHF + Elias-Fano + zstd 的 PlainState 只读 reader(读 snapshot/accounts.*、storage.*)。
3. warm-tier overlay(读三态 + tombstone):MDBX warm(H₀+1..tip)叠加 snapshot(H₀ 只读 immutable)。读:warm 有值→warm;warm 删除标记→不存在**不 fallback**;warm 无 key→fallback snapshot。模板=`modules/state/snapshot/SnapshotStateReader`(snapshot_reader.go:56-78,layer-first,`(nil,true)`=删除不下沉/`(nil,false)`=fallback);`Layer` 接口 types.go:35。
   - **storage tombstone 必须**:SSTORE slot→0(清零,独立于 SELFDESTRUCT,EIP-6780 未改,post-Cancun 仍频繁)删 H₀ 存在的非零 slot;不记 tombstone 则 fallback 读 snapshot 旧非零值=错。writer:storage 清零写 tombstone(empty-value marker,reader cursor.SeekExact 区分 found-empty=删除 vs not-found=fallback)而非 tx.Delete。
   - **account tombstone 仅逻辑完整/防御**:EIP-6780(Cancun 2024-03-13)后 SELFDESTRUCT 对非同-tx-创建合约只转 balance 不删 → H₀ 存在的 account 在 post-H₀ catchup **不会被删**;同-tx create+destruct 的 account 本就不在 snapshot→也不需 tombstone。account 删除路径保留仅为防御 + 万一 pre-Cancun 块重放。
4. bootstrap dispatcher:mode=snapshot 时跳过 RebuildState,直接挂 snapshotreader + warm overlay。
**验收**:`--snapshot.mode minimal --bootstrap.mode snapshot` 冷启动后,从 snapshot H₀ 续跑 catchup 到 tip,state root 每 sub-batch 验证通过;`eth_getBalance @ tip` 正确。
**估算**:3-5 天(snapshotreader 是主体)。

## P2 — caplin 自主 beacon sync(Phase 7.4-7.6) ★高

**目标**:`--caplin.enabled` 能独立 beacon sync,摆脱 external Lighthouse / eldevp2p 依赖。
**前置**:Engine API in-process 已就绪(Phase 7.1.1.b ✅)。参见 `docs/ethel/caplin-phase-7-plan.md`。
**步骤**:
1. **7.4 sentinel**:接入 discv5 + libp2p beacon p2p mesh(最大 blocker,缺大量 devp2p 子包)。
2. **7.5 network handlers + antiquary**:beacon block/blob 请求响应 + 历史归档。
3. **7.6 service.Start() stage loop**:把 clstages 状态机接进 `cl.Service.Start()`(当前只开 MDBX),驱动 checkpoint sync → forkchoice → 调 Engine API。
**验收**:`eth-el --caplin.enabled --caplin.checkpoint.url <url> -tags n42el` 无 external CL 下能 checkpoint-sync + 跟随 mainnet 12s live;头与公链一致。
**估算**:数周(sentinel + stages 是大工程,见 Phase 7.5 计划 ~43,779 LOC scope)。可拆 5 个 session(7.4a sync internal/cl → b sentinel → c antiquary/network → d stages/wiring → e E2E)。

## P3 — migration rtrie 端到端验证 ★中(收尾本会话加固)

**目标**:确认新 `rtrie` phase 在一次真实全量迁移中产出对齐 mainnet 的 trie。
**前置**:rtrie 代码已落地(本会话)。
**步骤**:
1. 在新 dst 跑 `n42-migrate-reth-hashed --reth D:/reth2k/db --dst <new> --head-block <H> --expect-root <header stateRoot> --phases acc,sto,code,rtrie,head`。
2. 确认 rtrie verify 通过(root==expect)、persisted;`n42-verify-root` 复核。
3. 该 dst 起 eth-el 续跑数百块,零 mismatch。
**验收**:全新迁移库无需事后 rebuild-trie 即可 incremental 不 drift。
**估算**:1 天(主要是迁移耗时, 代码已就绪)。

## P4 — parallel EVM 生产集成 ★中(性能)

**目标**:把 Block-STM parallel EVM 从影子 PoC 接进 catchup 主路径,突破 ~270 Mgas/s。
**前置**:PoC 已存在(`parallel_evm.go`, `N42_PEVM_BENCH` 影子)。
**核心 blocker**(`docs/ethel/exec-perf-plan.md:150-175`):**读可见性** — 串行路径靠同一 batch RwTx 让 block N+1 读到 N 的未提交写;并行 worker 各需 RoTx(cgo 禁共享 RwTx),只见上次 commit,miss batch 未提交。
**步骤**:
1. in-memory hashed-state overlay:batch 内未提交写放内存 overlay,worker RoTx + overlay 读。
2. 并行-aware changeset writer(staged Merkle 需 AccountChangeSet/StorageChangeSet)。
3. Prague+ 收据在 executeBlockParallel 构建;hashed-canonical 基读接 HashedStateReader。
**验收**:`--parallel-evm` 全量 catchup state root 与串行逐块一致;Mgas/s 显著提升。
**估算**:1-2 周(overlay 是长期架构件,与 P2-state-tiering 融合)。

## P5 — sub-batch unwind/bisect ★中(运维健壮性)

**目标**:catchup 中某 sub-batch Merkle 验证失败时,自动 unwind + 二分定位坏块(当前停下靠人工)。
**前置**:staged Merkle 失败已能 stop(`downloader.go:411-413`)。
**步骤**:
1. sub-batch unwind:用 AccountChangeSet/StorageChangeSet 回滚该 batch 脏键到 batch 前状态。
2. 二分:对半缩小 [from,to] 重跑 MerkleStageIncremental 定位首个 root mismatch 块。
3. 报告坏块 + 自动 reorg 或停在坏块前。
**验收**:人为注入坏块,catchup 自动定位到该块并干净停下,datadir 一致。
**估算**:2-3 天。

---

## 建议执行顺序

1. **P3**(1 天,收尾本会话加固,低风险)
2. **P1**(3-5 天,让三模式可用 — 用户明确关心)
3. **P5**(2-3 天,运维健壮,独立)
4. **P4**(1-2 周,性能,与状态分层架构融合)
5. **P2**(数周,最大工程,摆脱外部 CL 依赖;可与其余并行推进)

> 关联 memory:`150-hph-cache-stale`, `reth-trie-design-perf`, `n42-hashed-aligned`,
> `phase75-sentinel-plan`, `reth22-hashed-canonical`, `minimal-client-design`, `eth-el-bootstrap-paths`。
