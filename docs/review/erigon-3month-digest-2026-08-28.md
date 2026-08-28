# Erigon v3.4.3 → v3.6.0（2026-05-27 ~ 2026-08-27）技术摘要

范围：1453 个合并 PR，筛出与执行/承诺/内存/一致性/快照/见证相关的 ~70 个；~75 个 PR 页面逐一核实机制与数据。数字均为 PR 作者自报（benchstat / 生产 profile），未独立复现。

发布节奏：3.5.0（06-26，并行执行默认开）→ 3.5.1~3.5.5（并行执行/承诺 step 边界、unwind、overlay 一致性修复）→ 3.6.0（08-24，plain commitment 快照、sharded LRU、合并压缩 2–3×）。

## 1. 并行执行（Block-STM / exec3）

| PR | 日期 | 机制 | 实测效果 |
|---|---|---|---|
| #21591 | 06-05 | `EXEC3_PARALLEL` 默认 `true`；`EXEC3_PARALLEL=false` 退回串行。前置 #21590 修复 SD-revival / metamorphic CREATE2 | Chiado/Sepolia/Hoodi/mainnet 全同步；仍有 ~4/300K 块的非确定性 race 未解 |
| #21590 | 06-03 | 并行确定性修复：`normalizeWriteSet` 扫版本历史找先前 `SelfDestructPath=true`；同 tx SD+CREATE2 判断 `>`→`>=`；`CreateAccount` 合成读；`calcFees` 不让 nil 前态短路 EIP-161 | 修复 Chiado 7,221,794 wrong trie root、Sepolia CallNewAccountGas 漏计 |
| #21667 | 06-08 | OCC 不变量：`VersionInvalid` 的 tx 写集之前以 `Done` 刷入 versionMap，下游读到幻影值。无效 tx 刷为 **Estimate**，读者得到 `MVReadResultDependency` 触发重执行 | Gnosis 18,483,405：80K 块后 gas mismatch 根因 |
| #22336 | 07-14 | 调度器每收一个结果就全量 take-all/push-back，O(n²) 且 worker 饿死（有效并发 ~0.5/6 核）。改 bool 成员数组 + `maxComplete` O(1)，派发只填空槽 | ether_transfers 300M-gas 块 **58.8 → 253 MGas/s** |
| #22409 | 07-22 | "cache-free" 并行：并行路径去掉常驻 `stateObject`，IBS 状态 = versionMap + journal；读侧 `sync.Map` + per-address RWMutex 取代全局锁 | warm-extcodehash 6.37×，warm-staticcall 3.03×，warm-call 2.33× |
| #22190 | 08-06 | BAL 驱动 0 重执行：值感知校验（version 变但值不变 ≠ 依赖）；provisional read；SELFDESTRUCT 不记录读 | bal-devnet-7 0→407,538 块 0 re-exec；无 BAL 基线 re-exec 率 46.8% |
| #21516 | 05-30 | `ERIGON_PERF_PROFILES=1` → block/mutex profile；pprof label `phase=pe-exec`, `sub=exec-worker/exec-loop/calculator` | catch-up：pe-exec 86.3% CPU，worker 63.2%、loop 17.4%、calculator 1.9%；tip 仅 10.7% |
| #23054 | 08-06 | 导出 `exec_repeats_total / exec_execs_total`；冲突率 = rate(repeats)/rate(execs) | n5 冲突率 ~17.2% |
| #21659 | 06-07 | batch > 96 块整批无 changeset → `-38006 Too deep reorg`；改 `changesetWindowStart` 逐块开窗 | gd5 5000 块 FCU batch 通过 |
| #22408 | 07-22 | ≥32 核死锁：32 槽 RoTx semaphore 被 NumCPU 个 worker 占满；下限 `max(cfg, execWorkers+1+reserved)`，warmup 非阻塞获取 | 142 个 race CI 失败→0 |
| #23134 | 08-11 | `committedStorage` write-once 前块视图改 `sync.Map` | read_warm 47.8→3.6 ns |
| #22883 | 08-04 | `WriteSet.Merge` 深拷贝改 `MergeInto` 原地合并 + map 池 | allocs −33.9%、时间 −13.5% |
| #23053 | 08-07 | `calcFees` 四次 versioned 更新合并为一次 | −19.3% |
| #23348 | 08-26 | 去掉进程级 4096 槽 channel 异步回收，改每块确定性归还 + use-after-release tripwire | 结构性 |
| #22949 | 08-12 | 并行路径 stateObject 每 tx slab arena（64×32），`clearJournalAndRefund` 回卷 | allocs 876→108，时间 −28.5% |
| #22733 | 08-11 | ExecV3 剥离 `StageState/Unwinder`；新 `blockreplay` 包：witness membatch 前态 + 内存 `FullBlockReader`，无 MDBX 单块并行重放 | mainnet 25604144 重放 ~88 ms/op |
| #22057 | 06-30 | 延迟 fee 计算复活了同 tx SELFDESTRUCT 的 coinbase 合约（pre-Cancun） | 共识修复 |
| #21962 | 06-23 | worker `chainTx` 统一包成 `BlockOverlay.NewReadView` | 修复 Gnosis 三周 `unknown ancestor` |

## 2. 承诺（commitment）与执行的重叠、锁、step 边界、unwind

| PR | 日期 | 机制 | 实测效果 |
|---|---|---|---|
| #20805 | 3.5.0 | 三段流水：worker 执行 → apply loop → **calculator 独立 goroutine** 持自己的 roTx 折叠 trie | 27h 无 panic；DB 稳定 5.5–6.6 GB |
| #23515 | 08-27 | `COMMITMENT_AFTER_EXEC`：exec loop 等 calculator 完成块 N 再开 N+1。calculator 全程持 `changesetMu`，apply 侧每个 `DomainPut` 也要它 | >40 ms 停顿 71→16；**wall 不变**（7.44 vs 7.51 s）——"现有重叠约等于零收益" |
| #23520 | 08-24 | calculator 的 mutex swap 收窄到只 `CommitmentDomain` | `changesetMu` 占 67% mutex wait；单次 `DomainPut` lock=28.78 ms vs getLatest 280 ns |
| #23535 | 08-24 | `TemporalMemBatch.latestStateLock` 拆 per-domain `paddedRWMutex` | n/a |
| #21416 | 07-09 | BAL 驱动并行承诺：从 BAL 叶集合播种，先于执行折叠；`BAL_SHADOW_COMPUTE` 双路核对 | hive 377/0 |
| #22354 | 07-12 | 并行承诺 worker 各自 `BeginTemporalRo` 钉住了更新的文件代 → 撕裂读；`AggregatorRoTx.Pin` 同一快照 | 修复 code-hash panic |
| #22092/#22111 | 07-01 | 块跨 step 边界时并行路径不写承诺 checkpoint → `eth_getProof`/witness 错 | 3.5.1 |
| #21981 | 06-24 | unwind 跨 step 时同 key 多 dup，`getLatestFromDb` 取到空墓碑；按 key 只恢复最低 step | n/a |
| #21825/#21847 | 06-16 | mid-batch 失败块 unwind 早退不调 `sd.Unwind`，脏 overlay 被重执行读到（SSTORE 少收 20,000 gas） | Hoodi 3004265 |
| #22402 | 07-12 | `SharedDomains.Unwind` 未接 StateCache，reorg 后缓存脏值 → 确定性 trie root 错；Unwind 内 `StateCache.Clear()` / epoch 失效 | 3.5.2 |
| #23061/#23064 | 08-06 | 内存 reorg unwind 手写 domain 列表漏 receipt 域 → logIndex/cumulativeGas 错；改遍历全部 domain | 3.5.5 |
| #22444/#23005/#22467/#22146 | 07–08 | StateCache 填充一致性：填充绑定 tx 视图 frontier；unwind 推进 `readViewEpoch`；读填充 `PutIfAbsent`；warmup 不得覆盖更新值 | #22146 修复 eest wrong trie root |
| #21982 | 06-29 | `BranchCache` 单锁 LRU 改 256 分片 `ShardedLRU`，根分支 lock-free | mutex profile 82.9% → 0；34→45 MGas/s |
| #21454 | 05-31 | 并行路径重新启用 trie warmup | mainnet tip NewPayload 273.9→158.7 ms；SSTORE 重块 8× |
| #21789/#21798 | 06-14 | 新分支节点跳过 warmup/`ctx.Branch()` 查找，`DomainPut` 跳过 `GetLatest()` | PutBranch ~30% 快 |
| #22185 | 07-03 | 单条目 last-address 缓存 `keccak(addr)` 的 nibble 前缀 | 1×1000 whale −46%；200K slot −15.3% |
| #22758 | 07-27 | SELFDESTRUCT 不再 `RangeAsOf(StorageDomain)` 枚举全部 slot 发 DeleteUpdate | mainnet 2.6–2.7M 峰值内存 ~40 GB → ~16 GB |
| #23141 | 08-10 | `deepStorageThreshold` 1000→128 | whale 块 warm 35→16 ms；tip commit mean 66.8→27.5 ms |
| #23401 | 08-20 | `newPrefixArena` 每块 1.38 MiB 预分配改几何增长 + 保留峰值 slab | 每块分配 −53~98% |
| #22839 | 07-31 | `workerPool` 每次 `Reset()` 换新 `sync.Pool` 丢弃 worker；改复用 | 943 MB profile |
| #21737 | 06-11 | commitment domain existence filter 进 RAM | 承诺 +30% |
| 议题 #20955 | — | `HistoryStateReader.Read` 兄弟查找走 `GetAsOf`，miss 后先走历史文件；应 `GetLatest` | 多读 ~2.08 GiB history vs MDBX 32 MiB |
| #22617 | 07-21 | 构建器共享 BranchCache 被兄弟块推进 → 封出执行会拒绝的 root；`WithoutSharedBranchCache()` | ePBS reorg 修复 |

## 3. 内存 / 分配

| PR | 日期 | 机制 | 实测效果 |
|---|---|---|---|
| #21386 | 06-30 | StateCache 从"命中率低整表清空"改 sharded LRU + `(txNum, epoch)` 惰性失效 | 工作集不再周期清空 |
| #22154 | 07-07 | 全部缓存统一 `freelru.ShardedLRU`；两层 `CodeStore`；BranchCache 常驻 trunk | CALL/EXTCODE* 1.5–1.87×，geomean 1.156× |
| #21875/#21893 | 06-19 | `.bt` pivot 缓存节点 32B → 8B（keysBlob + 偏移） | 堆 ~1.3 GB→~340 MB；bloatnet 1.18 G→0.028 G |
| #22561 | 07-23 | journal 从 interface 装箱改 72B 值类型 tagged union | SSTORE B/op −94.4%，geomean allocs −52.7% |
| #22739 | 07-27 | 删 `journal.append` 里的 `panic(fmt.Sprintf)` 让其可内联 | storageChange −45.1%，geomean −29.4% |
| #22723 | 08-04 | `logs` 改 `[][]Log` 值存储，`AllocLog` 复用底层 | ERC20Transfer allocs 135,199→10 |
| #22578 | 07-20 | IBS.Reset 五张 map 用 `clear()` 保桶 | −38.3% 时间、−33.2% 字节；7.86M 分配 |
| #22574/#22573/#22777/#22847 | 07–08 | 各路径 IBS 用完 `Release()` 回池 | `state.New` 5.75 GB/17.94 GB |
| #22776 | 07-28 | `revisionsPool` 内嵌 16 项 inline buffer | 16 层 push/pop 0 alloc |
| #21503 | 05-29 | 删 `Hash.Bytes()/Address.Bytes()` 值接收者（返回 `h[:]` 使数组逃逸） | n/a |
| #21510 | 05-29 | trace 行 `fmt.Printf(&uint256)` 逃逸；改 `String()` | 读路径 allocs −33% |
| #22489 | 07-17 | `Keccak256/Sha256` 经 interface 池化 hasher 使参数逃逸；改具体类型 `keccak.Sum256` | 32B 160.9→124.5 ns 1→0 alloc；16 核 SSZ 19.5→4.3 ns |
| #23479 | 08-27 | CALL/CALLCODE/DELEGATECALL 去掉 `bytes.Clone(ret)` | 59.4M 对象/5.8 GB；402→202 allocs |
| #22899/#23012 | 08-01 | `ReceiptWriter` 长寿 scratch，logs 原地 RLP | 每 tx 9→0 alloc，−39.9% |
| #21858 | 06-18 | typed tx `Hash()` 不再 `[]any` + 反射 | Legacy 8→1 allocs |
| #22498 | 07-16 | `rlp.Stream.ViewBytes()` 零拷贝读 envelope | 同步节点 24% 分配空间 |
| #23423 | 08-21 | `common/snappypool` | 每 reader ~142 KB；64 块响应 ~8.7 MB 垃圾 |
| #23435 | 08-22 | zstd decoder 池；encoder `WithLowerEncoderMem` | decoder 334,692 B→1,714 B；encoder −40% |
| #22270 | 07-08 | 承诺 leaf hash 缓冲 per-goroutine scratch，`Update` 无指针 chunk slab | allocs −62% |
| #22685 | 07-23 | 热结构指针字段挪到大数组之前（GC 只扫到最后一个指针） | hasher 少扫 ~1 MB |
| #23518 | 08-26 | ETL sortable buffer 1 MB 池化 chunk | 10k −77% 时间/−92% 内存 |
| #21374 | 05-29 | 5 项 cherry-pick（warmuper 可取消、ChangeSets3 prune 上限、bufio 池） | bloatnet 3h 涨 40 GB 问题 |
| #21553 | 06-01 | MDBX pageSize 4 KB | flush 1.7× |

## 4. reorg / overlay 与已提交视图一致性、receipts、payload 时序

| PR | 日期 | 机制 | 实测效果 |
|---|---|---|---|
| #22533 | 08-25 | overlay-backed（head 敏感）与 committed（replay/proof/witness/history 同一 tx）两种显式读策略；拒非规范块 | n/a |
| #22511/#22951 | 07-17 | receipt 生成绕过 overlay 直查 DB → logIndex 错；`DomainReader` 先问 overlay | 3.5.5 |
| #22110/#22155 | 07-03 | 并行 mid-block 恢复把 logIndex/cumulativeGas 归零；改 `finalize(cumulativeGasUsed, firstLogIndex)` | 3.5.1 |
| #23279 | 08-16 | `eth_getLogs` tag 与扫描同一 committed 视图 | n/a |
| #23046/#23164 | 08-08 | 后台 commit `ClearRam()` 清空 RPC 仍引用的 domain map | 3.6.0 |
| #23102 | 08-12 | 构建预算从 attributes 到达改为 payload timestamp | 3.5.5 |
| #21990/#23437/#23172 | 06–08 | builder 再停；proposer slot 前开始建 payload；先发布 head 再拷贝状态 | 3.6.0 头条 |
| #22437 | 07-20 | `DomainDel` 对已缺失 key 跳过记录 | 历史行 249→2 |

## 5. 快照 / 压缩

| PR | 日期 | 机制 | 实测效果 |
|---|---|---|---|
| #21625 | 06-22 | merge 压缩模式匹配换 Aho–Corasick；cover DP 快路径 | bloatnet 382.6→135.0 s（2.8×）；matcher 68× |
| #22050 | 07-07 | SAIS 输入单 `uint16` 符号 | merge 103.7→59.5 s |
| #21482→#22963→#23382 | 05–08 | merge/索引用独立 mmap + `MADV_SEQUENTIAL`，共享映射保持 `MADV_RANDOM`（首版误清 RANDOM 造成 RPC 尖峰被禁） | 收益体现在 #21625 |
| #21376/#21452/#21933 | 07-13 | plain commitment（分支值直接存）；`integration commitment convert` | 读/merge 更快，文件更大 |
| #22901 | 07-31 | `.bt` 分支因子 256→64 | 冷访问 −35%；索引 +60% |
| #23316 | 08-17 | Huffman 解码消 bounds check | −10% |
| #23328 | 08-17 | 唯一字节码 literal 区 io_uring 预取 | EXTCODECOPY +65.6% |
| #22876 | 08-03 | `FILES_ASYNC_IO` io_uring 预热（默认关） | 作者称收益有限 |
| #21746/#21773/#21776 | 06-12 | existence filter `ShardedFuse`；可配 mmap/RAM | n/a |

## 6. 见证 / 无状态

| PR | 日期 | 机制 | 实测效果 |
|---|---|---|---|
| #21518 | 05-30 | `codes` 只含 pre-state 加载的 | EEST 通过率 94.6%→98.5% |
| #21529 | 05-30 | `headers` 改连续区间 `[oldest-accessed .. parent]` | zkevm 7 修 6 |
| #21491 | 05-30 | zero→zero SSTORE 不作为 `DeleteUpdate` | 多余节点归零 |
| #22077 | 07-02 | 见证在 fold 中同步捕获，删第二次自顶向下 walk | ~2× 快、−17% 内存、~4× 少分配 |
| #22384 | 07-28 | `--witness.cache.blocks` 后台预建 + JSON 预序列化 | 15 MB 见证 TTFB 1.2 ms |
| #22320/#22003/#22022/#22000 | 06–07 | `keys[]` 完整性门控、排除证明、省略 <32B 内联节点 | 3.5.1 |
| #22663/#22099/#23136 | 07–08 | minimal 节点 `debug_executionWitness`；二叉 trie 见证 | n/a |

## 7. EVM 解释器 / keccak / 预编译 / stateObject / journal

| PR | 日期 | 机制 | 实测效果 |
|---|---|---|---|
| #22743 | 07-27 | tracer defer 条件注册；删每 opcode 三次 IBS 接口调用；jump table 局部化 | geomean −7.6% |
| #21954 | 06-24 | JUMPDEST 分析 SWAR 跳过无 PUSH 的 8 字节字 | 24 KB 全 JUMPDEST 2.3×；真实合约中性 |
| #22748 | 07-31 | `InternKey` 每 EVM 1024 项直接映射缓存 | SLOADWarm −25~32%，geomean −9.1% |
| #22900 | 08-18 | `InternAddress` 256 项 | DelegateCallProxy −22~29% |
| #23526 | 08-26 | `Memory.Resize` 4 KiB 对齐再翻倍 | 冷帧 −60%；端到端持平 |
| #22940 | 08-07 | MODEXP 256-bit 模数走 uint256 Barrett | 1.5–1.6× |
| #22931 | 08-01 | `bn254ScalarMul` 无穷远点直接返回 | 838× |
| #23268 | 08-14 | gnark-crypto v0.21.0 | n/a |
