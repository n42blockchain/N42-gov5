# 一个块的 EVM 执行剖面与可省略/复用/预计算清单（2026-08-26）

数据来源：`24,000,000..24,200,000` 四分片合并 CPU profile（15,338 CPU 秒），
逐行 `-list`；heap profile 同一次运行（累计分配 682 GiB / 110 亿对象）。
所有百分比 = 占全部 replay CPU 的份额。密集区形状与 0–1000 万段不同，
稀疏段里下面"每 tx"项的占比更高、interpreter 更低。

## 0. 一个块在 worker 上实际发生什么

```
replayWitnessBlock
 ├─ reader.Reset(witness) / ibs.Reset()                         1.15%   ← 每块
 ├─ ProcessBlock
 │   ├─ ProcessExecutionBlockStart（4788/2935 系统调用）        0.03%
 │   ├─ SetFrom(precomputedSenders)  MakeSigner  NewEVM(block)  ~0.05%
 │   └─ for tx:
 │       ├─ ibs.Prepare(txn.Hash(), ...)                        2.08%   ← 1.74% 是 Hash 本身
 │       ├─ AsMessage + MakeSignerWithTimestamp                 0.26%
 │       ├─ evm.Reset(txCtx)                                    0.01%
 │       ├─ TransitionDb
 │       │   ├─ preCheck（nonce/EIP-3607/buyGas）               0.66%
 │       │   ├─ IntrinsicGas + FloorDataGas（各扫一遍 calldata）0.34%
 │       │   ├─ PrepareAccessList（预编译地址逐个插入）         0.78%
 │       │   ├─ ParseDelegation(GetCode(to))                    0.35%
 │       │   ├─ evm.Call → EVMInterpreter.Run                  82.97%  ← 含预编译 9.55%
 │       │   └─ refund / coinbase AddBalance                    0.25%
 │       ├─ ibs.FinalizeTx（dirty→origin 拷贝、wipes、flags）   2.50%
 │       └─ receipt 构造 + CreateBloom                          3.21%   ← bloom 已记忆化
 ├─ applyEthelWithdrawals / ProcessExecutionBlockEnd / Finalize ~0.02%
 └─ EthReceiptHash（receipt trie）                              1.94%   ← 每块
```

按层汇总：

| 层 | CPU | 说明 |
|---|---:|---|
| interpreter 循环 + opcode（不含预编译、不含状态访问） | ≈ 60% | 已做：派发表扁平化、jumpdest 位图缓存、frame 复用 |
| 预编译（ecrecover cgo 2.95%、bn256 4.5%、modexp 1.9%） | 9.6% | 共识必需，除非换实现 |
| 状态访问（IBS/stateObject/accessList 的 map） | ≈ 8% | mapaccess 3.97% + mapassign 2.29% + mapclear 1.26% |
| Go map 键哈希（aeshash + memhash） | 3.35% | 20/32 字节 key 全走 AES 哈希 |
| 每 tx 串行（setup + finalize + receipt） | ≈ 10% | 上面树里 tx 级各项之和 |
| 每块串行（Reset + receipt trie） | 3.1% | |
| 分配器（mallocgc）+ sync.Pool | 4.9% | 682 GiB / 200k 块的 churn |

关键判断：**每块级的串行开销只有 3%**，所以 reth 式"一个 worker 连跑多块"能摊薄的
只有 NewEVM/signer/BlockHashFn 这种 0.05% 的东西；`ibs.Reset` 和 receipt trie 每块
都必须做。多块批处理的价值只可能来自 cache 局部性，不是摊薄初始化。真正的钱在
**每 tx** 和 **每 opcode** 两层。

## 1. 可以直接跳过的

| # | 项 | 现在花 | 为什么能跳 | 改法 | 共识风险 |
|---|---|---:|---|---|---|
| S1 | `txn.Hash()`（Prepare 参数） | **1.74%** | replay 里 tx hash 只用于 `logs[thash]` 分组、`receipt.TxHash`、`Log.TxHash`——三者都不进 receipt root RLP | `--no-output` 下用 tx index 作 logs 的 key，Hash 改惰性；或至少给 `DynamicFeeTx.hash` 写非反射编码（它占 Hash 成本 87%，Legacy 已改过，`334 ns/1 alloc` vs `713 ns/5 alloc`） | 无：receipt RLP = [status, cumGas, bloom, logs]，logs RLP = [addr, topics, data] |
| S2 | `PrepareAccessList` 里逐个插入预编译地址 | **0.62%**（95 s + 11.5 亿次分配的一部分） | EIP-2929 规定预编译从 tx 开始就是 warm，且 tx 内永不移除、revert 也不会移除（它们在任何 snapshot 之前加入） | `accessList.ContainsAddress` 先查静态预编译表（addr[0:19]==0 且 addr[19] 在当前 fork 的集合内）再查 map；不再插入、不再记 journal | 无：语义等价 |
| S3 | `FloorDataGas` 与 `IntrinsicGas` 各扫一遍 calldata 数零字节 | 0.34% → 0.17% | 同一份 data 扫两次 | 扫一次得 (zeros, nonzeros)，两处共用 | 无 |
| S4 | `GetCode(to)` 只为 `ParseDelegation` | 0.35% | 每笔 Prague 后的 tx 取整段 code 只看前 3 字节 | stateObject 上缓存 `delegation` 三态（首次 Code() 时算好）；`delegationCallAccessCost`（每次 CALL，1.0%）同理 | 无 |
| S5 | `sortedAddresses` 每 tx 排序 | 0.06%（8.5 GiB 分配） | 排序本身**不能**去掉（witness 读取顺序必须与录制一致），但 slice 可以复用 | 复用 per-IBS buffer | 无 |

小计约 **3.1%**，全部无共识风险。

## 2. 预初始化 / 复用（每 tx 或每帧的重复构造）

| # | 项 | 现在花 | 改法 |
|---|---|---:|---|
| R1 | 每次 `Run` 取 `Memory`/`Stack` 出 `sync.Pool`、`new(ScopeContext)` | **0.95%**（Pool.Get 172 s 中的 99 s + newobject 46 s） | 和 `evm.contractFrames` 一样按调用深度复用：`evm.frames[depth] = {Contract, Memory, Stack, ScopeContext}`；深度优先执行保证同深度不重叠 |
| R2 | `stateObject` 池回收时 `clear()` 三个 storage map | **0.90%**（putStateObject 170 s，其中 `clear(originStorage)` 119 s） | `clear` 是 O(bucket 容量) 不是 O(len)：一个曾经装过 1 万 slot 的对象被池化后，**以后每块都付 1 万桶的清空**。规则改成 `len > 64 → 置 nil 惰性重建，否则 clear` |
| R3 | `MakeSignerWithTimestamp(config, number.ToBig(), time)` 每 tx | 0.26% 的一部分 | 每块算一次传入 |
| R4 | `NewEVM` 每块 + `ResetBetweenBlocks` 仍 `NewEVMInterpreter` | ~0.05% | 每 worker 持有 EVM，rules 未变时不重建 interpreter（`contractFrames`、`jumpdests` map 也随之跨块保活） |
| R5 | `c.jumpdests` per-tx map + `GlobalCodeAnalysisCache` 二级查找 | 0.51%（isCode 里 60 s + 19 s + clear 4.8 s） | 把 jumpdest 位图挂在 `BytecodeCache` 的条目上：取 code 时一并拿到分析结果，一次查找、两个缓存合一、`c.jumpdests` 删除 |
| R6 | `IntraBlockState.Reset` 每块 5 个 `make(map)` | 小 | 复用（clear 或按 R2 规则） |

小计约 **2.3%**。

## 3. 预计算后引用

| # | 项 | 现在花 | 改法 | 代价 |
|---|---|---:|---|---|
| P1 | `PUSHn`：`SetBytes(RightPadBytes(code[a:b]))` | **3.18%**（255 s flat，PUSH1/PUSH2 为主） | (a) `pushByteSize ≤ 8` 走 `SetUint64(bigEndian(code[a:b]))` 快路径，去掉 32 字节通用 `SetBytes` 的分支；(b) 对已缓存合约在代码分析时把 PUSH 立即数预解成 `[]uint256`，PUSH 变成 32 字节拷贝 | (b) 内存：每 PUSH 32 B，24 KB 合约约 128 KB，需按 LRU 限额；建议先只做 (a) |
| P2 | `validJumpdest`：`code[udest] != JUMPDEST` 再查位图 | **0.87%**（134 s 是随机访问 code 字节的 cache miss） | 位图语义从"是代码"改成"是合法 JUMPDEST"（分析时把 `code[i]==0x5b && isCode` 折进位）→ 一次内存访问 | 位图定义变了，`codeBitmap` 的所有消费者要同步；有单元测试可护 |
| P3 | `accessList.Contains`：先查 addresses map 再查 slots map | 0.77%（118 s） | warm 标志和 warm slot 集合放进 `stateObject`（SLOAD/SSTORE 本来就已拿到对象）→ 少一次 20 字节 key 哈希 | journal 的 revert 条目要跟着改 |
| P4 | `foldBalanceIncrease` 在**每次** `getStateObject` 查 `balanceInc` map | **0.70%**（108 s） | `balanceInc` 绝大多数时候是空的：`if len(sdb.balanceInc)==0 { return }` 放在 map 查找前——哈希 20 字节 key 的代价在查空 map 时也照付 | 无 |
| P5 | `ReadAccountCode` 每次过 `BytecodeCache.Get`（分片 RWMutex + map） | 0.40%（61 s） | `stateObject.code` 已在对象上缓存，问题是 R5 那条：合并两个缓存后这里只剩一次 | 无 |
| P6 | receipt trie `DeriveShaErigon` | 1.94% | 共识必需；`EncodeIndex` 已是零反射（45 s），其余 246 s 是 keccak 与 trie 构造，**不可省** | — |

小计约 **5.5%**（P1(a)+P2+P3+P4+P5）。

## 4. 减少内存复制 / 用指针

heap profile 里 200k 块分配 682 GiB、110 亿对象。GC 的 mark 成本随**对象数**走，
所以对象数那一列比字节更重要。

| # | 项 | 分配 | 根因 | 改法 |
|---|---|---:|---|---|
| M1 | `journal.append` 的接口装箱：`AddAddressToAccessList` **11.5 亿对象** / 25.8 GiB，`journal.append` 16 GiB，`SetState`、`SetBalance`、`AddBalance` 同类 | ≈ **60 GiB / 25 亿对象** | 每个 journal 条目是一个小 struct 装进 `interface{}`，每次一次堆分配 | journal 改成 `[]journalEntry` 的**带标签联合体**（一个固定大小 struct + kind 字段），revert 用 switch；零分配 |
| M2 | SSTORE gas 函数：`slot`、`current`、`original` 三个局部变量逃逸 | **36 GiB / 12 亿对象**（分配对象数第一名） | 以 `*types.Hash`/`*uint256.Int` 传给接口方法 → 逃逸分析判定逃逸 | 三个 scratch 放进 interpreter/frame struct；或 IBS 提供按值返回的 `GetStateAndCommitted(addr, slot) (cur, orig uint256.Int)` |
| M3 | `AddBalance`/`SubBalance`/`SetBalance` 的 `new(uint256.Int).Add(...)` | 24 GiB + 18 GiB | 每次转账两次堆分配 | 栈上临时值 + `setBalance(&tmp)`（内部是 `Set` 拷贝，指针不逃逸） |
| M4 | `stateObject.setState`/`cacheCommittedState`：三张 map 的增长 | 24 GiB + 16 GiB + 28 GiB | 见 §5 T1 —— 三张 map 合一 | |
| M5 | LOG opcode：`Log` struct + topics slice + data 拷贝 | 36.5 GiB / 2.9 亿对象 | data 必须拷贝（memory 会被复用）；struct 和 topics 可 arena | 每块一个 log arena（block 末 receipt hash 消费完即整体丢弃） |
| M6 | CALL 参数 `Memory.GetCopy` | 12.2 GiB / 2.1 亿对象 | 调用方 memory 在被调期间不会变（被调有自己的 memory） | 输入用 `GetPtr` 切片视图（geth 即如此）；`RETURNDATA` 仍需拷贝 |
| M7 | `types.CopyBytes` | 13 GiB / 3.3 亿对象 | 分散在返回数据/code 拷贝 | 逐个看调用点，多数可用视图 |
| M8 | `DynamicFeeTx.hash` 反射 RLP | 10.3 GiB / 1.8 亿对象 | 见 S1 | 直接编码 |
| M9 | `decodeBodySegment` + zstd | 88 GiB（12.9%）| reader 侧整段物化 | P2 惰性解码（另立项） |
| M10 | `math/big.nat.make`（modexp 预编译） | 21 GiB | big.Int 天然如此 | 除非换 modexp 实现（有 uint256/固定宽度版本可选），否则不动 |

M1–M3 合计去掉约 **120 GiB / 40 亿对象**（约 36% 的对象数），这直接降低 GC mark
成本与 mallocgc 的 3.8%。

## 5. 结构性改造（收益最大，动到数据结构）

| # | 项 | 现在花 | 设计 |
|---|---|---:|---|
| T1 | `FinalizeTx` 每 tx 把 `dirtyStorage` 整表拷进 `originStorage`（`updateTrie` 246 s：mapassign 108 s + 迭代 87 s） | **1.6%** + M4 的 68 GiB | 三张 map（origin/blockOrigin/dirty）合成**一张** `map[Hash]slotEntry{cur, committed uint256; epoch int32}`：SSTORE 时 `if e.epoch < curTx { e.committed = e.cur; e.epoch = curTx }; e.cur = v`；`GetCommittedState` = `e.epoch < curTx ? e.cur : e.committed`；tx 结束只做 `curTx++`，**O(1)**。SSTORE gas 一次查找同时得到 current 与 original（现在是两次）。journal revert 存整个 entry |
| T2 | 所有状态 map 的键哈希 | **3.35%**（aeshashbody 244 s + memhash 270 s） | Address/Hash 已经是均匀随机字节，不需要再过 AES 哈希：自定义开放寻址表，`hash = LE64(key[0:8])`。适用于 `stateObjects`、storage map、accessList、`nilAccounts`、`logs`。Go 内建 map 无法指定哈希，只能换实现 |
| T3 | 状态 map 的 `Address`/`Hash` 值拷贝 | 隐含在 T2 里 | 自定义表里 key 存指针或索引到 arena |

T1 + T2 ≈ **5%**，是剩余空间里最大的一块，也是唯一需要正经设计评审的改动。

## 6. 尽快验证

- gas 验证已在块末；**可以更早**：每 tx 后若 `cumulativeGas > header.GasUsed` 立刻失败
  （失败路径省时间，成功路径不省）。
- receipt root 需要全部 receipt，块内无法提前。
- 唯一能"提前"的是把 `EthReceiptHash` 从 worker 挪到旁路 goroutine——但机器已 CPU 饱和，
  挪动不产生吞吐，不做。

## 7. reth 式多块执行：数据说什么

每块串行开销 3.1%，其中 `ibs.Reset`（1.15%）和 receipt trie（1.94%）**不能**跨块摊薄
（witness 是每块独立的，状态对象必须丢弃；receipt root 每块一个）。可摊薄的只剩
NewEVM/signer/BlockHashFn 约 0.05%。所以多块批处理若有收益只会来自 L1/L2 局部性
（同一物理核连续执行同一批热合约），上限估 0–5%，必须 `K∈{1,8,32}` 实测；
在 P1/T1/T2 之前不值得做。

## 8. 排序（按 收益/风险）

| 序 | 项 | 预估 | 风险 |
|---|---|---:|---|
| 1 | S1 非反射 `DynamicFeeTx.hash`（+ no-output 惰性） | 1.5–1.7% | 无 |
| 2 | P4 `balanceInc` 空表短路 | 0.7% | 无 |
| 3 | R2 大 map 置 nil 代替 clear | 0.8% | 无 |
| 4 | S2 预编译静态 warm | 0.6% | 无（需等价性测试） |
| 5 | M2 SSTORE gas 三个 scratch 不逃逸 | 12 亿对象 | 无 |
| 6 | R1 按深度复用 Memory/Stack/Scope | 0.95% | 无 |
| 7 | P1(a) PUSH≤8 字节快路径 | 1–2% | 无（有 opcode 测试） |
| 8 | M1 journal 去装箱 | 25 亿对象 | 中：journal 所有 revert 路径 |
| 9 | S3/S4/R3/R5/P5 小项 | 合计 ~1.2% | 无 |
| 10 | P2 位图折入 JUMPDEST 判定 | 0.9% | 低 |
| 11 | T1 单表 storage + epoch | 1.6% + 68 GiB | 中：GetCommittedState 语义、journal |
| 12 | T2 自定义哈希表 | 3.3% | 中：范围大 |
| 13 | M5/M6 log arena、CALL 输入视图 | 48 GiB | 低 |

1–7 全部无共识风险、每项几十行、可独立 A/B，合计约 **7–8%** CPU 与 ~15 亿对象。
8–12 是第二梯队，合计再约 **6–7%**。两梯队做完，interpreter 之外的开销从 ~17% 降到
~5%，剩下的就是 EVM 本体和预编译。
