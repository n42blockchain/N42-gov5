# Erigon 近三个月（2026-05-27 → 08-27，v3.4.3 → v3.6.0）复盘与对 N42 的映射

对照对象：同事的《Erigon 启示》备忘（2026-08-27）。本文核对了它引用的 8 个 Erigon 链接与 6 处 N42 代码位置，
并把 Erigon 这三个月 1453 个合并 PR 按主题筛了 ~70 个逐一读过（附录 `erigon-3month-digest-2026-08-28.md`）。

## 1. 同事备忘的结论：成立，但有四处要修正

**事实层面全部对得上**：v3.5.0 默认 Block-STM（#21591）；v3.6.0 分片 LRU、合并压缩 2–3×、每 slot 拷贝
266 MB→18 MB；#23515 串行化后 >40 ms 停顿 ~70→~18、总时长不变、`changesetMu` 事实上已串行化；#22533 overlay
与 committed 两个视图混用；#23423 snappy 142 KB/次；#23435 zstd 解码器 335 KB→1.7 KB/次；#23479 CALL 返回
数据拷贝 59.4M 对象/5.8 GB；#20955 历史 `.ef` 2.08 GiB vs MDBX 32 MiB。

**代码层面的六个点也全部成立**，但：

| 备忘说 | 实际 |
|---|---|
| 代码在 n42-rs | 六处全在 **N42-26**（`crates/n42-node/src/qmdb_state_root.rs`、`bin/n42-node/src/main.rs`、`crates/n42-network/src/gov5_rpc.rs`）。n42-rs 没有这些文件，也没有并行 EVM/lanes；它的 QMDB `on_canonical` 把 `write_snapshot` 放在锁外，反而比 N42-26 好 |
| 行号 297 / 2043 / 775 / 731 | 308 / 2097（和 2257 一份复制）/ 775 / 731，偏 0–44 行 |
| QMDB 全局 mutex 覆盖重建+插入+WAL append | **更糟**：持锁期间含 `sync_all()` fsync，WAL 每块 `create_dir_all`+重开文件；speculative `compute_candidate` 走同一把锁。已有 `n42_qmdb_lock_wait_ms/lock_hold_ms/wal_append_ms/commit_outcomes_total`，缺独立 fsync 指标和 tip-tree 命中率指标 |
| hash 与 body 跨 reorg 混用 | 读法确实是两次独立 provider 调用（一个 range 最多 2048 次），但 `gov5_block.rs:185` 的 `hash_slow()` 复验 + 块号/parent 校验使其 **fail-closed**（截断/报错，不会送出脏块）。另一个真问题：这些同步 DB 读 + RLP 重编码 + snappy 压缩跑在 libp2p codec 的 `async fn` 里，无 `spawn_blocking` |
| chunk 全收进 Vec，1024 块 + 64 MiB | 成立，最坏 64 GiB 无总预算、无背压；但 N42-26 目前**只 serve 不 request**，该 decoder 尚无生产调用方。原生 `finalized_range.rs` 倒是有 256 MiB 总预算可照抄 |
| 每块新建 encoder | 成立，是 **snappy** 不是 zstd（gov5 `ssz_snappy` 线协议）；zstd 在 direct-push 路径同样每消息 `zstd::bulk::compress`，devlog-139 实测 leader 压缩 55–60 ms/块 |
| "6–8 lane、sender-sharded drain、Block-STM 已实现" | lanes 与 drain 成立且指标齐全；**Block-STM 不在 live 路径**（`executor.rs:152-160` 明写"must not be used as the live execution path"，`n42_parallel_evm_blocks_total=0`），8 lanes 今天只作用于 txpool snapshot；abort/re-exec/非收敛回退串行全部无计数 |
| "异步提交状态机、QUIC 分块是方向" | 已是成品：`AsyncCommitStage{Committed,ExecutionReady,Finalized}` + `/n42/block-direct/3`（2–4 MiB 分块、manifest、ack 延迟等十余个指标） |
| 163 ms / 78 MB/s / 12.7 MB | 全部来自 devlog-139 的推导目标（1M TPS ÷ 163K tx/满块），不是配置；实测 ~1.0 s/块，差 6.3× |

**一句话判断**：备忘的技术判断可信，优先级排序基本对；最大的偏差是把"Block-STM 已上线"当前提——
N42-26 的 live 执行仍是 reth 串行 revm，所以"并行执行已成熟，瓶颈转到一致性边界"这句对 Erigon 成立，
对 N42-26 只成立一半：一致性边界要修，但执行本身也还没并行。

## 2. 我从 witness-replay 这一轮得到的、与备忘互相印证的数据

- **"不是继续加线程"**：gov5 Go 节点 128w→256w（SMT）全量只快个位数（perf stat：IPC 2.72→2.15，解释器
  issue-bound）；把 CPU-s 从 456,881 降到 305,426 的全是分配削减（journal 值类型、log arena、balance 临时量、
  存储 map 单表）和指令数削减（内联 dispatch、静态跳转融合、SHA3 memo、modexp/bn256 换 gnark）。
- **Erigon #23515 的教训在 N42-26 上会原样重演**：QMDB 提交在 reth `StateRootJob::finish` 里同步跑，
  且 speculative 与 commit 共一把锁——"exec/commitment 重叠"今天是零，先量 `lock_wait/hold` 再谈异步。
- **一个 Erigon 没有、N42 独有的硬约束**：witness 是顺序访问流，重放必须在同一 opcode、同样的读序列后失败
  （含注定失败的帧）。任何"提前失败"的优化都会让 reader 错位（§5.33，基本块预检因此只能"预测成功才用"）。

## 3. 对 N42-26（Rust）：在备忘的顺序上加四条

备忘的 1→6 顺序保留（单一读视图 → 增量导入+字节预算 → QMDB 分段指标+串行/重叠 A/B → 差分测试 →
codec 复用 → 两阶段提交）。补充：

1. **WAL 写出锁外、持久 fd、`sync_data`**（`qmdb_state_root.rs:661-701`）：这是锁内最贵的一段，改法与
   n42-rs `on_canonical` 一致（锁内只算、锁外写）；Erigon #23520 也是把 calculator 的锁收窄到只换 domain。
2. **先给 Block-STM 加 `exec_repeats_total / exec_execs_total`（Erigon #23054）和非收敛回退计数**，
   量出冲突率再决定 live 化；Erigon 无 BAL 时冲突率 46.8%、n5 生产 17.2%，靠值感知校验（#22190）和
   Estimate 刷写（#21667）才把 re-exec 降下来——这两条是 N42-26 Block-STM 上线前的必修。
3. **tip-tree 命中率指标**（`reconstruct_tree_locked` 的 `resumed` 分支）：文档说无缓存 15 ms@300 →
   68 ms@1200，却没有命中/未命中计数——是备忘"tip cache hit/miss"里最值钱的一项。
4. **`mobile_packet.rs:268` 的 `history_by_block_hash(parent)`** 是仅剩的未分类历史读（Erigon #20955 的
   同类问题）；`ExecutionPath` 那套 fail-closed 分类（HEAD `d35014a`）已经把其余 ~25 处管住了。
5. **压缩**：留 zstd，但用 `zstd::bulk::Compressor` 常驻上下文 + snappy `FrameEncoder` 池（Erigon
   #23423/#23435 的 Rust 版）；`trim_gov5_live_cache` 全 protected 时 `unwrap_or(0)` 会驱逐正在用的块。

## 4. 对 N42-gov5（Go）：可以直接抄的（已核对代码位置）

| # | Erigon | gov5 现状 | 预期 |
|---|---|---|---|
| 1 | #22489 `Keccak256` 走具体类型，不经 `hash.Hash` 接口池 | `crypto/crypto.go:105` 经 `NewKeccakState()` 接口 + defer 归还 | 32B 输入 1→0 alloc、−23% 时间；bloom/tx hash/code hash 全受益 |
| 2 | #23479 CALL 系去掉返回数据拷贝 | `instructions.go:776/813/845/877` 四处 `types.CopyBytes(ret)` | 0.22% 分配；需先加"预编译输出不别名输入"测试 |
| 3 | #22578 `Reset` 用 `clear()` 保桶 | `intra_block_state.go:481-483` 三张 map 每块 `make` | Reset −38%；每块一次 |
| 4 | #21503 删 `Hash.Bytes()/Address.Bytes()` 值接收者 | `common/types/hash.go:49`、`address.go:100` 存在 | 逃逸点批量消除，grep 可做 |
| 5 | #22743 解释器循环：tracer defer 条件注册、jump table 局部化 | 本轮已做大半（meta 表、内联、`debug` 提前） | 剩余小 |
| 6 | #22185 承诺层 last-address 缓存 nibblized `keccak(addr)`；#21789 新分支跳过 `GetLatest` | `lib/commitment` 是同源移植，未做 | Erigon whale −46%、PutBranch −30% |
| 7 | #22758 SELFDESTRUCT 不枚举全部 slot 发 DeleteUpdate | `persistent_context.go:230-250` 需核对 | 早期 mainnet 自毁垃圾块峰值 40→16 GB |
| 8 | #23526 `Memory.Resize` 4 KiB 对齐再翻倍 | runFrame 保留 1 MiB，冷帧仍走 append | 端到端持平（Erigon 自报），低优先 |
| 9 | #21516 pprof label 分阶段 + block/mutex profile 开关 | 本轮手工做过 mutex/block profile | 制度化即可 |

已经做过、与 Erigon 殊途同归的：journal 值类型 tagged union（#22561/#22739 ⇔ `journalRecord`）、
logs 值存储/arena（#22723 ⇔ `logArena`）、stateObject 池/单条目缓存（#22949 ⇔ `lastAddr/lastObj`）、
typed tx hash 直接编码（#21858 ⇔ `typed_tx_hash.go`）、modexp/bn254 快路径（#22940/#22931 ⇔ 本轮 gnark）。

不建议照搬：BAL 驱动承诺（依赖 EIP-7928，且三轮 wrong-trie-root 回归）、`FILES_ASYNC_IO` io_uring
预热（作者自评收益有限）、CodeCache 的 MDBX 持久层（评审指出内存安全问题）。

## 5. 执行记录（2026-08-28）：做了什么、不做什么、为什么

### 5.1 做了并合入（`perf/erigon-borrow`，合入 main）

| 项 | 改动 | 验证 | 结果 |
|---|---|---|---|
| HPH last-address 缓存（#22185） | `Updates` 键哈希与 HPH 账户 cell 共用单条目 keccak(addr) nibble 缓存；生产 `mpt_root_computer` 改走 `KeyToHexNibbleHash` | 缓存/非缓存等价测试（命中/未命中/近似地址/全深度）、3 个 ProcessUpdates fuzzer 各 20 s、`lib/commitment` 全测 | 1000 slot 键哈希 −30%，200 账户×100 slot 端到端 −8.2%；1×1000 端到端噪声内（Erigon 的 −46% 是另一条流水线） |
| 新分支跳过前值查找（#21789） | `CollectUpdate/CollectDeferredUpdate` 收到 `isNew`，`branchBefore[row]==false` 时不查 warmup cache / `ctx.Branch` | 计数 context：新 trie 200×100 的 `ctx.Branch` 读 6503 → 1 | mock 上是 map 命中，时间不变；mptContext 上省一次 mutex+MDBX get |
| pprof phase 标签（#21516） | worker `phase=exec`、reader `phase=read` 的 goroutine 标签 | 0–4M 窗口 2×2 对照持平 | `go tool pprof -tagfocus=phase=read` 分相；零成本 |

全量 gate（eb6 = main + 上三项，128w/6r/gc300）：41m49s / 307,369 CPU-s / failed=0 / RSS 25.6 GiB，
对当日重跑的 main 基线 41m28s / 305,146（+0.7%，噪声内）。witness-replay 不算状态根，HPH 两项的安全网
是等价测试 + fuzz；`isNew` 依赖"`branchBefore[row]==false` ⇒ 该前缀在库里没有分支记录"的不变量，与 Erigon 相同。

### 5.2 做了、测了、撤回（数据留档）

| 项 | 做法 | 数据 | 结论 |
|---|---|---|---|
| Keccak 具体类型（#22489） | `crypto/keccak`：x/crypto legacy 海绵做成值类型，`Keccak256Hash`/SHA3 opcode/trie/receipt hasher 全切；等价测试通过、0 alloc | 微基准 32 B：298 ns/1 alloc → 293 ns/0 alloc；1M 切片 r10 +0.7%、r11 −1.3%（噪声）；**0–4M reader-bound 窗口墙钟 +5%、CPU 持平**（二分确认是它，不是标签）；eb4 全量 43m07s / 308,987 vs 305,146 | x/crypto 的 state 直接把输入 XOR 进 lane、只拷 32 B 输出；自写海绵两头都过 rate buffer，reader 侧（tx hash 100–300 B）更慢。每 hash 省 32 B 分配在 CPU-s 上看不见。**撤回** |
| fastkeccak 汇编置换 | 仓库已依赖 `erigontech/fastkeccak`，一次性摘要走它 | 机器有负载时微基准"快 23%"；**空载重测所有长度都更慢**（32 B 309 vs 298 ns，1 KiB 2519 vs 2049）；eb4 全量含它 +1.3% CPU | 与更早的"Keccak asm 更慢"记录一致。**撤回**；教训：微基准必须在空载机器上做 |
| `IBS.Reset` 用 `clear()`（#22578） | 三张 map 不再每块重建 | eb3 全量 43m23s / 317,793、RSS +3.2 GiB（vs 305,146 / 26.7 GiB）；改回 make 后 eb4 RSS 回到 26.6 GiB | 每个 worker 保留见过的最大块的桶，GC 每轮扫带指针的空桶；Erigon 的收益来自按 tx 的 Reset。**按块 Reset 的带指针 map 不要 clear()** |

### 5.2b 评估后不做（原因）

| 项 | 原因 |
|---|---|
| CALL 系去掉返回数据拷贝（#23479） | 本仓库 `opReturn/opRevert` 用 `GetPtr` 不拷贝，`opCall` 的 `CopyBytes` 是唯一一次拷贝（callee 的 runFrame 内存会被同深度下一次调用复用，必须拷）。Erigon 省的是"RETURN 已拷一次 + CALL 再拷一次"的第二次；我们没有第二次。只剩预编译输出的一次冗余拷贝（且 identity 预编译返回输入别名，去掉会出错），收益 <0.1% |
| 删 `Hash.Bytes()/Address.Bytes()` 值接收者（#21503） | 逃逸的根因是把切片传进接口方法；没有 profile 证据，批量改 80+ 处只为理论收益，不做 |
| `Memory.Resize` 4 KiB 对齐再翻倍（#23526） | Erigon 自报端到端持平；runFrame 已按深度保留 1 MiB 缓冲 |
| 解释器循环清理（#22743） | 本轮已做等价物（meta 表、内联、`debug` 提前、tracer 路径分离） |
| SELFDESTRUCT 不枚举全部 slot（#22758） | 本仓库 `persistent_context.go` 的 DeleteUpdate 路径不做 `RangeAsOf` 全量枚举，问题不存在 |
| 基本块预检（evmone advanced） | 已做、已测、更慢（§5.33），且 witness 顺序读流约束使其只能"预测成功才用" |
| BAL 驱动承诺、`COMMITMENT_PARALLEL` | 依赖 EIP-7928，Erigon 自己三轮 wrong-trie-root 回归（#22354/#22362/#22495） |
| `FILES_ASYNC_IO` io_uring 预热 | 作者自评收益有限；本仓库热路径是 witness 流不是快照文件 |
| CodeCache 的 MDBX 持久层（#22154） | 评审指出 unmapped memory 缓存的内存安全问题 |
| 块级并行执行改造（Block-STM 值感知校验等） | `internal/parallel_processor.go` 是按 sender/access-list 分批的保守并行，不是 OCC；先加冲突/重执行计数再谈 |
| SMT 256 线程 | 已量：IPC 2.72→2.15，全量只快个位数 |

### 5.3 对 N42-26（Rust，另一位研发的仓库，本轮不改）

按 §3 的清单执行；最先做的两件：把 QMDB WAL 写出锁外（持久 fd、`sync_data`），给 Block-STM 加
`exec_repeats/execs` 与非收敛回退计数——没有这两个数，"并行执行已成熟"无法成立。
