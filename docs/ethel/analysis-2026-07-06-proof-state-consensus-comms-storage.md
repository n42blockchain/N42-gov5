# N42 全栈分析汇总 — Proof / 状态 / 共识 / 通讯 / 存储

日期:2026-07-04 ~ 07-06 连续三天的实测与分析整理。
数据标注:**[实测]** = 真机测量;**[估算]** = 由实测外推;**[设计]** = 推演,未验证。

数据源:D:/n42-datc-bprime2-25m(DATC B′ 25.4M 构建,写作时 progress 13.66M)、
D:/reth2k(reth 全节点,tip 25,439,434)、D:/mainnet/mainnet(N42 自研链 block-only,
tip 12,973,899)、E:/n42-replay-v2-qmdb-staggered-hist(QMDB 全历史重放)、
D:/n42-snapshot(本次导出的 25.4M 全态快照)。

---

## 1. Proof 证明系统

### 1.1 当前分层能力与延迟 [实测]

| 场景 | 路径 | 延迟 |
|---|---|---|
| tip 账户 proof | internal/mptproof + RethHashedLeafSource | <1ms(端到端 20ms) |
| tip 大合约存储 proof(USDC级) | 同上 | **14-35s(未解)**,解法=commitment-domain(未开工) |
| 历史任意高度,普通账户 | DATC B′ | 2M 矩阵:随机高度中位 281ms / 最大 574ms;边界 ≤4ms |
| 历史任意高度,大合约存储 | DATC B′ + DatcStoRoot | WETH 388ms(边界)/ 1.7s(off=16384);USDT/USDC 级未测,预计数秒 |

**结论:普通账户"秒级"有强依据;个别海量合约存储不能承诺全体 <1s**(M4 逐槽 dense 层
已 descope,300-617GB 成本换不回边际收益;B′ 全链延迟要等构建完成后 re-measure)。

### 1.2 空间-时间曲线(核心权衡)[实测锚点]

哈希 = 叶历史的**计算缓存**,不是信息。记录层的唯一作用是省查询时的重算:

| 存多少缓存 | 记录层体积(packed) | proof 延迟 |
|---|---|---|
| 0(纯叶折叠) | 0 | 分钟级(v5 灾难数据:32s~十几分钟) |
| 深层每 64 块一版 | ~80-120GB [估算] | 中位 24ms(2M off=64 实测) |
| 深层逐块 dense(当前 e[3]=1) | ~200GB(账户侧)[估算] | ≤4ms |

**dense 是 sparse 的超集**:逐块记录可事后离线删薄成任意稀疏档,反之不行 →
当前构建跑最贵档,选点权后置。

约束:32B keccak 是以太坊共识强制,不可截断/换哈希;N42 自研链可自选承诺(QMDB 已用)。

### 1.3 块证明(witness)与极致压缩 [实测 + 估算]

100 个真实 25M 时代块(块内 trie 已合并去重):

| 形态 | 大小/块 |
|---|---|
| 执行 witness(仅值) | 7.8 KB |
| 完整 MPT 证明(值+全部兄弟哈希) | 276.7 KB(p99 451KB),**97% 是哈希** |
| zstd | 不压(≈1.0×) |

推广到 25.4M 块的去重阶梯:
- 朴素:25.4M × 277KB ≈ **7.0 TB**
- 值跨块去重 → 叶历史:13.33B 变更 × 25-35B ≈ **0.35-0.45TB**(信息论地板)
- **哈希全局字典是陷阱**:历史上不同节点哈希版本 ~50B 个 × 32B ≈ 1.6TB,字典比数据大
- 可重算哈希不存(越近 root 越该重算:版本最多、重算最便宜)→ 稀疏边界记录层(§1.2 曲线)

### 1.4 静态档案终局 [设计,组件多数已建]

| 组件 | 现状 |
|---|---|
| 叶历史静态段(键序 as-of) | ✅ leafseg(构建中) |
| 稀疏节点哈希段 | 🔨 packed-segment 计划(唯一缺口,构建后离线做) |
| 每块证明骨架(可选) | ✅ 半成品:witness freezer 全链 + bpp 块证明 25M + 链锚 106MB |
| MPHF 索引 | ✅ 实测 1.71 bits/key |
| 分发(CAS/torrent) | ✅ 冷段 1-of-N 实测 PASS |
| Domain 文件族形态 | ✅ commitment-domain-plan 已写(erigon .kv/.kvi/.kvei) |

终局:**~0.6-0.75TB 全静态**(叶历史 450 + 哈希段 100-150 + 可选骨架 150),
不可变、内容寻址、CDN/torrent 可分发,验证端零信任(只需 headers)。

### 1.5 休眠/活跃分离评估 [实测比例 + 设计]

- 热数据实测(reth 0-7M):账户 10% / 存储槽 2.3%;月活跃集估算 3-4%(~7000万叶 vs 20亿)
- **树深只省 ~1.3 层(log!),proof 大小仅 -15%,这条收益容易被高估**
- 真收益:①fold 叶读 ×30 降 ②活跃态 5-8GB 全进 RAM ③休眠 proof 变永久静态可 CDN
  ④初始同步 4-6× 轻
- ETH 侧不能改共识结构 → 落点=查询侧 fold-skip + 休眠静态缓存;
  N42 自研侧 QMDB frozen twig **已实现**(死亡只翻 256B 位图)
- 代价:复活协议(以太坊 state expiry 多年未上线的原因)+ 休眠/复活边界历史本身是新存储

---

## 2. 状态

### 2.1 规模 [实测]

- ETH tip(25,439,434):398.8M 账户(24GB plain)+ 1.60B 槽(132.5GB plain);
  全历史变更 4.73B 账户 + 8.60B 存储 = 13.33B 次
- N42 自研链(12.97M 块):**全链活跃 key 仅 605万** —— 状态问题主要在 ETH 侧
- DATC @12.66M(叶 27.4%):mdbx 564GB(AccNode 213.6 + StorNode 186 + StoRoot 69.2 +
  Hashed* 61 + Trie* 13.8)+ spill 147GB;**154B/叶变更 = packed 设计值 40B × 3.85**

### 2.2 体积投影 [估算,斜率实测]

| 形态 | 全链体积 |
|---|---|
| MDBX 行式跑完(现状) | ~2.4-2.6TB |
| packed-segment 转换后 | ~1.0-1.1TB |
| + 年龄分层压实(推荐终点) | **~0.7-0.79TB**,中位延迟 25-300ms |

offline compact 不值得:表占文件 96%、页填充 85-93%,只能找回 20-50GB。

### 2.3 快照 [实测,2026-07-06]

reth 25.4M 全态 → RecSplit MPHF + zstd:账户 4.05GB + 存储 20.3GB = **~25GB**
(MDBX raw 的 21.6-34.5%),总耗时 5.5h,峰值内存 83.6GB。

### 2.4 QMDB [实测]

- 承诺分离:frozenLeafRoot + 活跃位图,休眠不再水化
- N42 全链重放:44min(无历史)/ 1h32m(--qmdb-history);产物 27GB / **79GB**
  (全历史层 +52GB = 任意高度 proof 能力的代价)
- 对照:QMDB 78.7MB vs JMT 4116MB(1.65M keys,52×)

---

## 3. 共识

### 3.1 四轮 replay-v2 对照(N42 链 12.97M 块,24.58M tx)[实测]

| chainspec | txFail | receiptMismatch | 说明 |
|---|---|---|---|
| mainnet_compat(与历史一致) | **1** | **7**(0.00005%) | 共识正确性基线,极干净 |
| mainnet_v2(全 EIP 创世激活) | 1 | 11,004,390(85%) | 新规则验旧块必然分叉,无信息量 |
| staggered(日历对齐,未补丁) | 级联增长 | 增长 | 暴露 apos×Shanghai 交互问题 |
| staggered + 6 地址补丁 + history | 1,505 | 4,320(0.033%) | 补丁有效但**不收敛**(新 10 地址又冒出) |

### 3.2 日历对齐分叉表(N42 创世 2023-03-07,period 8s)[实测块时间]

| 分叉 | ETH 真实日期 | N42 对应 |
|---|---|---|
| Shanghai | 2023-04-12 | block ~305,000 |
| Cancun | 2024-03-13 | block ~3,935,000 |
| Prague/Pectra | 2025-05-07 | time 1746612311 |
| Osaka/Fusaka | ~2025-12-09 | time 1765238400 |
| Glamsterdam | 未上线 | time 1798761600(超出 tip,不激活) |

chainspec = `mainnet_v2_staggered`(params/chainspecs/mainnet_v2_staggered.json +
internal/allocs/mainnet_v2_staggered.json,补了 6 地址各 5 万 ETH)。

### 3.3 未解:apos 奖励 × Shanghai 交互 [未根因]

分阶段激活 Shanghai 后,不断有新地址在不同高度差 0.02-15 ETH(先 insufficient funds,
后该地址 nonce 永久错位级联)。补地址=打地鼠。根因方向:apos 奖励/资金发放路径与
Shanghai 时代 gas/EVM 规则变化的交互。**要清零必须查根因,不能再补余额。**

### 3.4 共识组件能力 [实测/已落地]

- BLS:48B 签名;512 委员会/块,重封管线 9.6× 加速(scalar-sum,仅模拟器合法);
  聚合后共识开销 **114B/块固定**;聚合可破 body 压缩瓶颈(签名占 55%→3%)
- hotstuff(mainnet_qmdb):period 3s、fastPropose 200ms、委员会池 20 万/每块 512、
  ramp 1M 块;7 validator 真机出过块
- QMDB 树在全链重放中零错误(twigs 29,188 / liveKeys 581 万)

---

## 4. 通讯(含 1M TPS 推演)

### 4.1 现状 [实测/已落地]

- 自研链:libp2p + gossipsub;同步实测 **500 blk/s**(legacy 追平 tip 10 分钟);
  限速修复后 1024 blk/s/burst 4096
- ETH 侧:eldevp2p(snap/1);archive live-sync 有已知 reorg 死循环 bug(25462234,待查)
- 消息层:8-shard gossip topics、RLN 反垃圾、65536 dedup 环、E2E(X25519+XChaCha20)、
  MLS 群组、SSE 流 —— 完整栈在 internal/distributed/messaging
- DAS:internal/peerdas(EIP-7594)已在库
- 分布式计算:coprocessor(ZK/乐观+挑战/TEE 三级验证、市场、罚没)已在库

### 4.2 1M TPS 的需求数学 [估算,锚点实测]

| 维度 | 需求 | 单机实测锚点 | 缺口 |
|---|---|---|---|
| 执行(纯 EVM) | ≥21 Ggas/s(转账下限) | **10.8 Ggas/s**(32 线程 Block-STM,4.7M tx/27s = 174K tps) | ~2-6 台/分片组 |
| 端到端(执行+承诺+持久化) | 1M tps | **~9.2K tps**(QMDB 全流程重放实测) | **~110×** → 必须分片 |
| 签名验证 | 1M sig/s | secp256k1 recover ~70µs/核 → 需 ~70 核;或 BLS 聚合后每块 1 次验证 | 聚合是唯一现实解 |
| 数据入口 | 100-150MB/s(100B/tx)或 12MB/s(Vitalik 12B/tx 压缩) | gossip 单节点全复制不可行 | 分片 + DAS |
| 状态写入 | ~5M writes/s | QMDB O(changes) 每块 O(twigs) | 按 key 前缀分片 |

### 4.3 分片 [设计]

- 执行分片:按地址前缀切 O(100) 个执行组,每组 = 一台 32 核(174K tps 纯执行实测支撑
  单组 10-50K tps 端到端的现实假设,含承诺/持久化开销)
- 数据分片:块体不全复制,PeerDAS 取样(库内已有 EIP-7594 实现)+ 冷段 torrent
  (1-of-N 实测 PASS)
- 网络分片:gossip topic 按 shard 切(messaging 层的 8-shard 模式即原型,可推广)

### 4.4 池化 [设计,原型已有]

- txpool 按 sender 前缀分片,入池验签在池边缘并行(work-stealing sender-recovery
  已实测:126K 块 4 分钟)
- 跨分片消息走 relay(8-shard topic + RLN 限速已落地),池间 gossip 去重靠 65536 环

### 4.5 聚合 [实测支撑]

- 每分片每块:512 委员会 BLS 聚合签名 → **114B/块固定开销**(设计实测);
  签名从 tx 体中剥离(F2 去签名实测 -45% body)
- 分片头 + 聚合签名上全局链:1M tps 摊到 100 分片 × 3s 块 = 每块 3 万 tx,
  全局链每 3s 收 100 条 114B 级摘要 ≈ 可忽略带宽

### 4.6 验证与执行 [实测支撑]

- 执行:Block-STM + lazy-coinbase(1.9× @8 workers,同 sender nonce 级联 bug 已修);
  见 exec-perf-plan(erigon 级 → reth2.0 1.7Ggas/s 的路线图)
- 验证 ≠ 重执行:witness 重放 10.8 Ggas/s = 验证吞吐远高于出块吞吐,
  一台验证机可覆盖多个分片的抽查

### 4.7 快速互验 [设计,组件实测]

分片 A 验证分片 B 的三级阶梯:
1. **聚合签名验证**(µs 级/块):信任 B 的委员会 → 只验 BLS 聚合(114B)
2. **witness 抽查**(ms 级/块):B 随块发布 7.8KB 执行 witness → A 无状态重放抽查
   (stateless 消费端 21 测试全绿,已落地);抽查率按风险调
3. **争议升级**(挑战期):coprocessor 的乐观-挑战-罚没机制(库内已有 challenge.go/
   slashing.go);DATC 式时序记录可把争议定位到单块单键;ZK 路线(zkprover/SP1 +
   cmd/zkguest RISC-V)做终局仲裁
- 静态 proof(§1.4)让历史互验零计算:块证明是永久有效的静态文件,可 CDN

### 4.8 诚实边界

以上 1M TPS 为**设计推演**:单点数字全部有实测,但系统级(跨分片原子性、全局排序、
委员会轮转下的网络抖动、DAS 恢复延迟)从未整测。跨分片原子事务是最大的未设计项。

---

## 5. 存储

### 5.1 静态段哲学 [已落地]

时间(月度)分段、不可变、内容寻址;freezer/columnar 双格式;派生数据可再生
(每周补齐流程:freezer-heads 盘点 → 四小件 → witness auto-resume)。

### 5.2 压缩矩阵 [实测]

| 数据 | 原始 | 压后 | 手段 |
|---|---|---|---|
| body(全链) | 567GB | 91.5GB 热(-84%) | F2 去签名(-45%)+ EIP-4444 冷段外移 |
| receipts | 169.9GB | 19GB(9×) | 紧凑编码(consensus 字段+logs,Bloom 读时重算) |
| header | — | 2.5× | 紧凑编码 |
| tx | — | 1.6× | 紧凑编码 |
| 全态快照 | 156GB plain | 25GB | MPHF+zstd(本次实测) |
| freezer 复压 | — | 1.0× | 已压尽,剩余全是语义级 |

### 5.3 磁盘经济学(2026-07-06 现势)

D: 14TB,清理后空闲 2.5TB;构建余需 ~1.9TB,余量 ~600GB。
不可动:reth2k 3.7TB(权威源)、geth 1.6TB(权威块)、N42-eth1177 908GB(changeset 源,
构建正在读)、n42-eth1 877GB(header 源)、N42-hashed-25311、mainnet-bls-full。
用户指示:其余未删目录暂不能动。

### 5.4 关键工程经验(近三天新增)

- tidwall/btree `Copy()` 会弹原树 isoid → 后续每次写付 COW 级联(曾占全进程分配 44%);
  live cursor + close-before-write 契约修复,2M A/B 四表 sha 逐字节验证
- 单调 seek 快路径**会算错根**(块 687,649 确定性复现,根因未定位,已撤销)——
  验证此类优化必须把"serial fallback 治愈次数>0"判为失败
- overlayLess u64 前缀快比:安全,已上生产
- Windows 双进程陷阱:被超时杀死的 bash 脚本其子进程可成孤儿继续执行 →
  双 DATC 实例并发写(未提交窗口内硬杀安全,crash-recovery 按设计吞掉未提交 spill 帧)

---

## 6. 行动项(优先级序)

1. **DATC 构建跑完**(ETA ~4 天)→ finalize → `verify --samples`(含非边界)+
   proof 基准(USDT/WETH 级 × 老 offset,RAM 看门狗)
2. **packed-segment 离线转换**(2.4TB→1.0TB→分层压实 0.7TB),做成 Domain 文件族形态
3. **apos×Shanghai 根因**(要清零 replay 失败必须查,补余额不收敛)
4. eldevp2p reorg 死循环(block 25462234)
5. commitment-domain G1/G2(若 tip 大合约存储 proof 需要 ms 级;2-3 周,-170GB)
6. 1M TPS 单点补测:BLS 聚合验签吞吐、PeerDAS 恢复延迟、跨分片原子性设计稿
