# Reth 近期更新 → N42 专项审计清单(2026-06-04)

对照 Reth 近两月集中推进的 BAL/并行执行、Engine API/blob、RPC simulate、P2P 消息边界、Trie/MDBX,审 N42 自己的代码。方法同 Erigon 审计:用 Reth 修过的 bug 类别审 N42 实现(可落地)。与 [[erigon-borrow-audit]] 重叠部分复用其结论。

## 总决策表(按 必要性×风险 排序)

| # | 项 | N42 现状 | 风险 | 动作 |
|---|---|---|---|---|
| **R1** | native-P2P 握手 256MiB 无界分配(pre-auth) | `internal/network/node.go:208` 缺 MaxPayloadSize 检查 | **高(安全)** | **修:加检查 + 调小 MaxPayloadSize** |
| **R2** | RPC simulate 无超时/无并发上限 | SimulateV1 无 timeout/evm.Cancel/块数·call 上限;RPC 层无全局信号量 | **高(DoS)** | **修:超时 + 信号量 + 上限** |
| **R3** | witness 单字节长度前缀无守卫 | `witness.go:48,62` `byte(len)` 可静默截断 | 中(踩 byte()-wrap 事故史) | **修:len>255 fail-loud** |
| **R4** | n42-mpt-migrate 单大事务 | `cmd/n42-mpt-migrate/main.go:268-323` 无 commit interval | 中(Windows OOM) | **修:加 --commit-every** |
| **R5** | Engine 端点止步 V4/Pectra,VM 已支持 Osaka/Fusaka/Amsterdam | `engine_api_v1.go:117,141` 无 V5;Amsterdam SLOTNUM 读不到 slot 恒 0 | 中(Osaka 激活前必补) | **排期:V5 端点 + slot 注入 + getBlobsV4** |
| **R6** | BAL/EIP-7928 EL 未实现(仅 CL SSZ 空壳) | `internal/cl/cltypes/eth1_block.go:59` 占位,EL 不生成/不校验 | 低(未激活)/高(未来) | **记账:将来照 Reth admission+post-exec validation** |
| R7 | eth-el reorder buffer 无字节上限 | `eldevp2p/buffer.go` 靠 syncWindow 隐式约束 | 中 | 排期:加累计字节 ceiling |
| R8 | WriteMap 仅软默认无 Windows 硬守卫 | `--writemap`/`dbg.WriteMap()` 仍可触发 80GB OOM | 低-中 | 排期:windows WARN/拒 |
| — | Engine blob versioned-hash 校验 | **正确**(validateBlobGasAndHashes + ValidateBlobTransactions)| — | 无需借鉴 |
| — | getLogs 范围/条数/ctx 取消 | **齐全**(10000 范围+10000 条+ctx)| — | 无需借鉴 |
| — | 订阅 panic recover | **齐全**(deferRecover 覆盖全订阅)| — | 无需借鉴 |
| — | getBlobsV1 别名 | **无 bug**(每条独立)| — | 无需借鉴 |
| — | txpool 字节/容量边界 | **齐全**(128KiB/tx + slot caps)| — | 无需借鉴 |
| — | 大导入分块(其它 3 工具)| **已对**(state-import/migrate-reth-hashed/ethexec 都 chunk + RpAugmentLimit/LifoReclaim 照搬 reth)| — | 无需借鉴 |

---

## 1. 并行执行 / BAL / Block-STM

- 并行执行正确性:**见 [[erigon-borrow-audit]] §1**(path2 缺 wipe 隔离=已建任务 #6;path2 门控警告=已做 commit cc98e49d)。Reth 的 parallel/state-root-recovery 修复与该结论一致。
- **BAL/EIP-7928:EL 完全未实现**。`modules/state/parallel_executor.go`/`internal/parallel_processor.go` 只收集 MVHashMap read/write set 做冲突检测,**无 BAL 收集/承诺/校验**。仅 CL SSZ(`eth1_block.go:59` `BlockAccessList *ByteListSSZ`)有空壳字段(不从 EL 填充,注释明说 RawBody 不带 BAL 字节)。
  - **现在:特性缺位,非 bug**(EIP-7928 主网未激活,N42 EL 到 Pectra)。
  - **将来加 BAL 必照 Reth 两关**:① newPayload 入口 BAL 条目数硬上限(admission cap,防 DoS);② 执行后用 **STM 已有的 read/write set**(N42 现成杠杆)重建实际访问集与声明 BAL 严格比对,不符即 INVALID。并行下 BAL 须**确定性聚合**(排序后承诺,不受 worker 调度影响)。

## 2. Engine API / Payload V4 / Blob / SSZ

- **blob versioned-hash 校验:正确**(`engine_api_v4.go:189` validateBlobGasAndHashes + `:199` ValidateBlobTransactions 逐序比对 payload 声明 vs tx blob hashes,数量/内容不符报错)。fork-aware。**无需借鉴。**
- N42 EL 走 **JSON-RPC newPayload**(非 SSZ)→ Reth 的 SSZ-newPayload versioned-hash 修复 **N/A**。
- **getBlobsV1 无别名 bug**(每个 `&BlobAndProofV1` 独立)。缺 **getBlobsV4/BlobAndProofV2**(Osaka cell proofs)。
- **payload attrs 无 targetGasLimit**(`PayloadAttributesV4` 只到 TargetBlobsPerBlock/EIP-7840)。
- **关键不对齐(R5)**:Engine 端点 `supportedEngineMethods` 止步 **V4/Pectra**,但 VM 已实现 **Osaka/Fusaka/Amsterdam**(`params/config_rules.go` IsOsaka/IsFusaka/IsGlamsterdam;`eips_amsterdam.go` SLOTNUM 0x4b)。
  - 标准 CL 在 Osaka+ 发 `engine_newPayloadV5` → **method not found**,握手 exchangeCapabilities 不通告 V5 → CL 判 EL 不支持 Osaka+。
  - **Amsterdam SLOTNUM 静默错值**:opcode 已实现但 `PayloadAttributesV4` 无 slot 字段 → `Context().Slot` 恒 0 → SLOTNUM 永远返 0 而不报错(最难排查类)。
  - **Osaka 激活前必补**:newPayloadV5/getPayloadV5/forkchoiceUpdatedV5 + 通告 osaka/fusaka + getBlobsV4 + PayloadAttributes 加 slot 注入 BlockContext.Slot + targetGasLimit。
- Engine fail-closed 两逃生口(N42_GAS161 / validate-only):**见 [[erigon-borrow-audit]] §2**(任务 #7)。

## 3. RPC simulate / trace / getLogs(R2,新)

**做得好(无需动)**:getLogs 范围上限 10000(`filters/api.go:349`)+ 条数 10000(`filter.go:33`)+ ctx 取消;订阅全有 deferRecover;debug_traceCall/Tx 有 5s 超时 + evm.Cancel。
**gap(借鉴 Reth blocking-IO semaphore + simulate 加固)**:
- **高:RPC 层无全局并发信号量** —— 重方法(simulate/trace/debug)可被并发刷爆,每请求开 RoTx + 全量 EVM replay 无上限。→ 加 `x/sync/semaphore` 全局 in-flight 限。
- **高:SimulateV1 无执行超时**(`blockscout.go:845-917` 不设 timeout/不开 evm.Cancel,对比 traceTx 有 5s)→ 单个长循环合约吊死 goroutine 直到 gas 耗尽。→ 加 context.WithTimeout + evm.Cancel。
- **中:SimulateV1 无块数/call 数上限**(只判空)→ 加 maxBlocks/maxCalls;calldata 也套 maxCallDataSize(目前只 DoCall 套了)。
- **中:多块模拟无 gap-fill / parentHash 不链式**(`:798` 每块 parent 硬编码 baseHeader.Hash())+ BlockOverrides.Number 无单调校验 → 照 Reth 链式推进 number/time/parentHash + 校验递增。
- 中:traceBlock 无整块总超时(单 tx 5s,大块累计数百秒)。
- traceTransfers/Validation 字段声明但**未实现**(N42 simulate 不产 transfer logs)→ 若补,照 Reth 处理 precompile self-move。

## 4. P2P / txpool 消息边界(R1/R7,新)

**做得好**:in-flight 有界(maxInflightPerPeer=4,chan 深度 1);请求打包有界(192 headers/32 bodies);txpool 边界齐全(128KiB/tx + GlobalSlots 5120 等);libp2p gossip 1MiB + req/resp chunk 1MiB。
**gap**:
- **最高(R1,安全)**:native-P2P **握手路径 `network/node.go:208` `make([]byte, payloadLen)` 缺 `payloadLen > MaxPayloadSize` 检查**(消息循环路径 `:324` 有,握手没)→ 单连接 pre-auth 强制 256MiB 分配;且 `MaxPayloadSize=256MiB-1`(`package.go:30`)远超实际(gossip/req-resp 都 ≤1MiB)。→ **加握手检查 + 大幅调小 MaxPayloadSize**。
- **中(R7)**:eth-el reorder buffer(`buffer.go:30`)**无字节/条数上限**,仅靠 coordinator syncWindow=2048 隐式约束(emergent,非强制)→ 执行 stall + 多 peer 快取时可涨。→ 加累计字节 ceiling(Reth memory-bound 类),超预算停取。
- **中**:响应解码(cases 4/6/16)无条目数预算(peer 可在 16MiB 帧内塞任意条数)→ 加 `len(resp) <= 请求量` sanity check。
- 单帧 16MiB 上限来自 **go-ethereum 库**(非 N42 代码)→ 建议加 N42 侧显式 per-eth-message cap(别依赖 vendored 常量)。
- tx-request packing overflow:N42 **未实现 tx 传播**(case 9 no-op)→ N/A,但将来实现须照 Reth 加数量+字节上限 + 溢出安全算术。

## 5. Trie / state-root / witness / MDBX(R3/R4/R8,新)

**做得好**:库默认 WriteMap=OFF + durable;SafeNoSync 有注释;RpAugmentLimit/LifoReclaim 照搬 reth;三大迁移/replay 工具已分块提交;WriteMap OOM 有回归测试钉死;有序导入用 cursor.Append(对应 reth ordered builder)。
**gap**:
- **R4(中,OOM)**:`n42-mpt-migrate` 整表单事务(`main.go:268`BeginRw→全量 Append→`:323`单 Commit),**无 commit interval**,是唯一没跟上同仓分块模式的迁移工具 → 大 StoragesTrie 单事务 dirty page 爆。→ 加 `--commit-every`(重开 RwTx 后 Append 需重 seek 末尾)。
- **R3(中,wrap 事故史)**:witness `byte(len(MarshalV2))`/`byte(len(val))`(`witness.go:48,62`)对 >255 字节**静默截断**。当前安全(account<256、storage≤32)但无守卫 → 加 `if len>255 { error }` fail-loud(符合 MEMORY 反复强调"byte()/uint16() wrap 必须加守卫")。
- **witness 无版本/无 spec**:裸 length-prefixed 流,无 magic/version/crc → 格式若变,旧流 + 新 reader 静默错位(读到垃圾 nonce)。→ 写 `witness-wire-spec.md`(标 v1);长期可选首部加版本字节。符合用户"只用 stream v1"决策但补可引用规范。
- **R8(低-中)**:WriteMap 仅"软默认 + 测试钉死",无 `GOOS==windows` 硬守卫 → 手传 `--writemap` 或 `dbg.WriteMap()` env 仍触发 80GB WS OOM → windows 上 WARN/硬拒。
- ZFS/MDBX 写放大:N42 主力 Windows NVMe,未见 ZFS 部署;如有,补 recordsize 对齐 pageSize 文档。
- DupSort `TrieOfStorage` 导入:`n42-migrate-reth-hashed` 碰它须 delete-before-put(MEMORY 有 dup 堆积事故史),未深读其迁移函数,建议单核。

---

## 落地优先级(综合 Reth + Erigon)

**安全/DoS(优先)**:R1(握手 256MiB,security)、R2(simulate 超时+信号量,DoS)。
**事故史模式(cheap+high-value)**:R3(witness byte wrap 守卫)、R4(mpt-migrate 分块)。
**未来 fork(Osaka 前必补)**:R5(V5 端点 + SLOTNUM slot + getBlobsV4 + targetGas)。
**记账(将来)**:R6(BAL 照 Reth 两关)、R7(reorder buffer ceiling)、R8(WriteMap windows 守卫)。
**共识相邻(已建 Erigon 任务)**:path2 wipe(#6)、Engine fail-closed(#7)、WS/devp2p 限速(#8)、commitment -race(#9)、BLOCKHASH 测试(#10)。
**无需借鉴**:blob 校验、getLogs、订阅 recover、getBlobsV1、txpool 边界、3 工具分块 —— N42 已对或已优。
