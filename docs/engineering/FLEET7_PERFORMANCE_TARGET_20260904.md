# 七节点对齐 n42-rs：目标与实测准备

日期：2026-09-04。**目标尚未达到。已完成多轮七节点诊断，继续修复实测瓶颈。**

## 开放执行权限后的实测进展

此前 socket 权限限制已解除。独立七节点成功组网，在共同高度 98 校验了
相同区块哈希与状态根，随后进入 3.423G gas / 163,000 笔每块负载。
主机同时有其他 Rust 与 n42-node 集群运行；以下是瓶颈诊断，不能当作隔离硬件下
的正式配对验收。现有其他集群尚未收到接管确认，因此未停止。

`flagship-baseline-01` 在资金准备阶段发现 RPC 硬编码的 1000 gwei 上限拒绝
Rust 旗舰使用的 100000 gwei，未进入测量。已增加 `--rpc.maxgasprice` 显式
配置（零保留原上限），单笔和批量 RPC 共同遵守；交易池和共识验证保持有效。
该上限只是 RPC 接纳策略，不是共识规则。

`flagship-baseline-02` 使用显式上限后完成资金准备与 3600 万笔签名交易预生成：

| 窗口 | 高度变化 | 规范链收录 TPS | gas 占用率 | 判定 |
|---|---|---:|---:|---|
| 1 | 321 → 324 | 16,299.8 | 100% | 七节点终点根一致 |
| 2 | 324 → 325 | 5,431.8 | 100% | 七节点终点根一致 |
| 3 | 325 → 325 | — | — | 停滞，整轮失败 |

终点使用 `eth_blockNumber` 的规范链高度，不能据此宣称所有交易已有独立校验的
最终提交证据。失败时脚本退出 1，七个节点全部优雅停止。

日志显示满块导入约 3.4 秒（发送者验证 0.86 秒、执行 0.94 秒、状态根
0.56 秒、写入 0.89 秒），后续连续超时、分支回退、重建进一步放大开销。
发现 `Pacemaker.ObserveCommitLatency` 原先只有测试调用：真实提交没有提供
样本，自适应超时从未生效。现已在成功视图推进、重设下一轮 deadline 之前
接入有效的本地提交耗时；无提交、缺失或倒序时间戳不训练估算器。

新增实际引擎提交路径测试与 race 检查通过；HotStuff 和 API 包完整测试通过。
`flagship-adaptive-03` 从同一创世种子的七份新目录再次运行，保留 6 秒基础超时
与 30 秒最大超时，检验自适应修复的实际效果。最终结果以 JSON 与本节后续记录为准。

证据位于 `build/perf7/bench-result-flagship-baseline-02.json`、对应
`bench-config-*.json`、`node0..6/log/n42.log` 和 `/tmp/n42-perf7-baseline-02.log`。
新轮次数据位于 `build/perf7/adaptive/`。CPU profile 安排在测量窗口之后。

后续诊断结果：

| 轮次 | 窗口 1 TPS | 窗口 2 TPS | 窗口 3 TPS | 结果 |
|---|---:|---:|---:|---|
| adaptive-03（ext4/LVM） | 16,297 | 10,866 | 10,866 | 三个窗口完整，七节点一致 |
| nvme-04（NVMe/XFS） | 16,300 | 16,297 | 16,298 | 三个窗口完整，七节点一致 |
| cap-05（同一 NVMe，队列优化） | 5,432 | 10,864 | 5,433 | 一致性通过，吞吐回退 |
| flow-06（限制在途输入） | 14,213 | 5,638 | 13,996 | 七节点一致，9 笔收据通过 |
| broadcast-07（七节点直送、关闭交易 gossip） | 14,093 | 13,080 | 9,398 | 七节点一致，发现陈旧 pending 计数 |
| history-08（规范提交通知、索引优化） | 29,886 | 16,300 | 16,298 | 七节点一致，9 笔收据通过 |
| reuse-09（EVM 复用） | 27,158 | 16,294 | 16,298 | 链测量通过；脚本后处理失败，非完整轮次 |
| stream-10（按需签名） | 27,166 | 21,733 | 16,298 | 七节点一致，9 笔收据通过 |
| cache-11（区块缓存限制为 4） | 21,733 | 27,166 | 16,300 | 七节点一致，9 笔收据通过；总吞吐无提升 |
| qmdb-12（接入批量叶更新） | 32,599 | 21,733 | 21,729 | 七节点一致，9 笔收据通过 |
| push-13（提前推送候选块） | 32,599 | 27,166 | 21,729 | 七节点一致，9 笔收据通过 |
| history-14（热点 bitmap 直接编码） | 32,599 | 27,163 | 21,729 | 七节点一致，9 笔收据通过；吞吐未改善 |
| memory-15（构建缓存移出 tmpfs） | 32,599 | 27,166 | 21,728 | 七节点一致，9 笔收据通过；吞吐未改善 |
| undo-16（回滚回收列表单次过滤） | 32,599 | 27,166 | 27,163 | 七节点一致，9 笔收据通过；第三窗口多一个满块 |
| binary-17（有界批量二进制注入） | 32,599 | 27,162 | 27,163 | 七节点一致，9 笔收据通过；吞吐未改善 |
| memo-18（复用相同历史位图合并） | 38,033 | 32,599 | 27,166 | 七节点一致，9 笔收据通过；90 秒多 2 个满块 |

权限开放后检查真实挂载发现：`/home` 在 `/dev/mapper/ubuntu--vg-home`
（底层 nvme0），Rust 的 `/data` 在 `/dev/nvme1n1p1`，两者不是同一设备。
nvme-04 使用新的独立目录 `/data/blockchain/gov5-perf7-20260904-2017`，
满块 `commit` 从 ext4 轮次的数秒降到约 45–90 ms。其他运行集群仍会造成干扰，
这些单轮数字不能替代正式配对统计。

nvme-04 另抽查三个窗口终点的首、中、末交易，共 9 笔，收据均成功、gasUsed
均为 21000、value 为 1 wei、gas price 为 1e14 wei，并匹配规范区块哈希。
证据为该目录的 `receipt-samples-flagship-nvme-04.json`。

15 秒 CPU profile 显示：约 49% 采样在 secp256k1 恢复，约 12% 在
`TxsPool.truncatePending`。goroutine 栈同时捕获该函数在 `txsSortedMap.Cap`
中反复 `heap.Init`。公平淘汰循环每删一笔都重新排序、重建整条账户队列。
现保留 Cap 产生的升序索引（本身就是有效最小堆），仅在堆发生变动后重新排序，
不改变最高 nonce 淘汰顺序或公平策略。

6000 笔队列连续裁剪到 32 笔的微基准中位数：57.77 ms → 0.305 ms。
覆盖插入、替换、Forward、Ready、大小队列 Remove、Filter 后再次 Cap 的回归
检查及交易池完整测试通过。**这不是端到端 TPS 提升证明。**
`cap-05` 已在同一 NVMe、新创世目录运行对照，数据位于上述目录的 `cap05/`；
旧二进制保存在 `build/perf7/n42-before-cap`，以便反向复测。

cap-05 已结束，9 笔收据抽查也通过，但无限速注入下端到端性能**没有改善**。
profile 中 secp256k1 恢复从 15 秒采样的 39.9 CPU 秒增至 81.5 CPU 秒，
主机使用内存达约 130/136 GiB。队列裁剪加速移除了部分偶然限流，更多输入处理
争用执行资源；这是与观察一致的解释，仍需流控对照验证，不能把微基准收益当作
吞吐收益。下一步应对齐 Rust 的有界输入深度，同时保持满块负载。

已修复 `txflood -target-depth` 的信用积压：补发时扣除已排队和进行中的请求，
饱和或任何节点状态探测失败时撤销未使用额度；队列按批次数而非交易数定容。
缺失、格式错误或溢出的 pending 值不再按零处理。测试覆盖连续 100 次慢请求
探测、额度回收和部分节点响应无效。脚本按需开放 loopback `txpool` namespace。
批量 RPC 的 `-broadcast` 现会将完整批次提交给全部节点，七个 HTTP 测试服务器
核对收到相同的签名交易字节。新增 `--p2p.tx-gossip`（默认仍为 true）和
压测 `--no-tx-gossip`，配置记录中保留该选择。

flow-06 的 gas 占用率仅 65.4% / 34.6% / 42.9%；采样 CPU 由 cap-05 的
135.8 CPU 秒降为 35.4 CPU 秒，TPS 未明显改善。broadcast-07 同样有空块，
日志显示 pending 长时间停在 407600，而实际规范链持续消费交易。
代码审计定位：`ChainHighestBlock` 唯一发送方原为 miner，跟随节点提交别人的
块没有通知交易池，只有轮到自己封块才清理陈旧 pending。现改为在
`CommitToCanonicalWith` 数据库提交、内存 head 更新后发送事件；HotStuff miner
不再对推测性封块发送该事件。重复/失败提交不发事件。完整 internal、miner、
txspool 测试及提交路径 race 检查通过。此修改亦恢复跟随节点的头部订阅和
gas-price 缓存失效通知，需继续观察七节点稳定性。

broadcast-07 的临时收据脚本假设窗口终点非空，遇到空块发生 IndexError，
**该轮没有完成收据抽查**。脚本现选择每窗口最后一个非空规范块，再验证
区块哈希、首中末交易的成功状态、gas、value 和 gas price。

历史索引也有明确的串行开销：flow-06 一个 120184 笔交易的块，`chgHist`
耗时 2.729 秒。`GetAccountChanges` 重新遍历 map，丢失 changeset 编码时的
排序；`writeIndex` 又为每个新接收账户重复开游标、构造同内容 roaring bitmap。
现按键排序、复用游标，并将本块新账户的 singleton bitmap 编码复用。
仍在同一数据库写事务内维护完整索引，没有启用 history-off/deferred。
163000 个随机顺序新键的微基准中位数 334.4 ms → 86.1 ms，分配约
123.9 MB / 472.7 万次 → 5.2 MB / 16.3 万次。字节级差分测试覆盖账户和
存储键、新旧键、已有多分片 bitmap、跨 uint32 高度；历史查询和聚合测试通过。
这仍是局部结果，端到端收益由 history-08 及后续配对轮次验证。

最新运行目录为 `/data/blockchain/gov5-perf7-20260904-2017/flow06`、
`broadcast07`、`history08`，各自保留 `bench-config` / `bench-result` / CPU profile。
history-08 的 gas 占用率为 91.7% / 100% / 100%，9 笔收据通过；70 次
逐节点深度观测中，65 次七节点计数完全相同，最大 pending 为 407800，
提交后计数及时下降并补充。早期满块 `chgHist` 为 183–293 ms，但后期又升至
1.5–1.7 秒；吞吐后两个窗口仍仅约 16k，未达到持续性能目标。

进一步改为每个区块构造一个 EVM、每笔交易 Reset，复用解释器缓冲区。
差分测试覆盖普通转账、状态写入、返回数据、日志、REVERT、预编译和合约创建，
逐笔比较收据、gas、余额、nonce、代码和存储；internal/miner/vm/witness 测试通过。
1000 笔分散转账微基准中位数 4.112 ms → 3.739 ms，约 33073 → 23083 次分配。
reuse-09 的三个链测量窗口通过，但运行期间原地修改 shell 脚本导致 Bash
继续读取时发生命令偏移（`-tps.sh: command not found`），提前触发清理，
收据与 profile 未完成；**该轮不作为完整通过记录，也未证明 TPS 收益**。

从 stream-10 起，每轮运行独立快照 `build/perf7/harness-stream10/`，不修改正在
执行的快照。配置还记录 txflood 二进制 SHA-256 与七个 harness 文件的 SHA-256。
新增 `txflood -stream` / `bench-run --stream`，仅在有提交额度时按需签名，
保留原来的发送账户、nonce 和接收账户顺序，每批在全部节点复用相同字节。
差分测试证实与预签名输出相同，race 检查通过；未改变签名或执行验证规则。
stream-10 使用同一节点二进制与有界深度，三个窗口均满块且完成收据验证。它取消了预签名期间的
空块等待，因此起始区块高度和 base fee 也不同，需要在最终配对前固定预热条件。

## 参考基线与验收口径

用户目标：运行七节点旗舰负载，持续优化至性能对齐 n42-rs。

参考仓库 `/home/n42/src/n42/n42-rs`，提交
`48263495dd5783ca946cb740340405b0dddfd911`，
`docs/NATIVE_FLEET7.md` 的 round 39 / `loop33H400a` 记录：

| 30 秒窗口 | TPS | 区块数 | 平均周期 | gas 占用率 |
|---|---:|---:|---:|---:|
| 1 | 316,289 | 70 | 0.429 s | 83.2% |
| 2 | 304,541 | 71 | 0.423 s | 78.9% |
| 3 | 302,823 | 70 | 0.429 s | 79.6% |

这是仓库中的历史记录，不是本次重新测得。相邻同二进制轮次存在约 1–4%
噪声，不能把小于 5% 的单次变化直接认定为优化收益。

采用以下工作验收口径，需真实测量满足后才能宣布目标完成：

1. 同一硬件，顺序运行两个客户端，统一初始状态输入、有效分叉、gas tier、
   转账分布、费用和每节点资源预算；记录数据库持久化策略与执行保证。
2. 至少三组配对轮次，每轮三个连续 30 秒窗口；Go 每个窗口至少 300k TPS，
   且配对轮次中位数不低于 Rust 的 95%。供给不足、缺块、RPC 错误、节点
   停止或状态不一致的轮次不计作达标。
3. 七个独立节点都推进到窗口终点，校验共同高度的区块哈希、状态根、交易根、
   收据根；补充转账执行结果、账户余额/nonce 与提交证据检查。
4. 保留原始 JSON、二进制哈希、配置和日志；性能修改必须有前后配对结果。

旗舰工作负载：3,423,000,000 gas（163,000 笔基础转账/块），6,000 发送账户，
每账户 6,000 笔，2,000,000 个接收账户，gas price `100000000000000` wei，
400 ms 请求出块间隔。参考文档部分文字仍写 480M，应以实际命令为准。

## 本轮落实

- 三个月风险审计及修复见 [审计报告](THREE_MONTH_AUDIT_20260904.md)。
- 构建节点、`txflood`、`txpool-journal-reset` 到 `build/perf7/`。
- 修复 `bench-run.sh` 的 480M 固定启动校验，开放窗口长度、gas price、chain ID；
  运行前核对七个 RPC 的 chain ID，写出配置和二进制 SHA-256。
- `bench-7node.sh` 按 gas tier 推导传播容量；3.423G 默认 58 MiB，保留显式覆盖。
  已有七份数据库时不再要求默认种子目录存在。
- `txflood` 拒绝超过 200 的 RPC 批量大小，修复 worker 静默截到 200、限速器
  却按原始批量计算的供给误差。Rust 的 500 不能直接照搬到 Go HTTP API。
- 测量器先采集连续时间边界，再枚举区块，使用单调时钟和严格 JSON 校验。
  缺块、错误 gas tier、父链变化、空负载、滞后或状态根不一致均失败。
  保存逐块计数及根、每个节点观察值和失败原因；`--min-tps` 检查每个窗口。
- 启动前检测目录写权限和 TCP/UDP 能力；失败发生在停止节点、清交易池之前。
  启动之后发生错误或中断会停止发压并尝试优雅停止本轮节点。
- 修复四个互相调用的 shell 脚本缺少执行权限的问题。

## 已准备的独立数据

从 Rust 的 `crates/chainspec/res/genesis/n42_fleet7_bench.json` 读取公开配置，
仅将 genesis gas limit 改为 3.423G，生成 `build/perf7/genesis-3423g.json`。
Go `n42 init --chain private --profile n42` 已成功初始化 `init-check` 和 `node0..6`。
该配置 chain ID 为 **1143**、14 个 alloc 条目、7 个初始验证者。

Rust 创世分配没有预充值 Go 原来的默认开发 faucet。发压器现支持
`N42_DEV_FAUCET_KEY`，独立环境选择 Rust 已预充值的公开 Hardhat 开发账户，
从忽略的 0600 文件读取；保持创世 alloc 不变。密钥不会写入压测配置 JSON，
也不会出现在 CLI 帮助的默认值中。

使用 Rust `h2_keygen` 的公开开发种子 `n42-fleet7-validator` 生成测试密钥，
逐一核对七个公钥与创世配置一致。密钥仅保存在忽略的 `build/perf7/` 下，
权限 0600，没有放入源码或报告。未改写 `/data/blockchain/` 的现有节点。
Go 侧离线校验程序 `build/perf7/check-fixture.go` 也已成功解析这些密钥，
验证七个公钥及发压账户的创世余额。

`build/perf7/env.sh` 已指定独立数据目录、chain private、32 GOMAXPROCS/节点，
TCP 31000–31006、UDP 30000–30006、HTTP 22012–22018、mobile 23012–23018、
pprof 6100–6106。独立端口已在上述诊断轮次中实际使用。

在允许 socket 的执行环境中，首轮诊断命令为：

```bash
source build/perf7/env.sh
scripts/qs/bench-run.sh --tag flagship-baseline --chainid 1143 \
  --gasceil 3423000000 --interval-ms 400 \
  --senders 6000 --pertx 6000 --recipients 2000000 \
  --gasprice 100000000000000 --conc 64 --rpcbatch 200 \
  --rpc-maxgasprice 100000000000000 \
  --windows 3 --window-sec 30 --pool-slots 489000 --pool-queue 163000
```

重复轮次必须恢复独立初始状态并使用正确的发送账户 nonce；不能把上述命令
直接在已经消耗发送账户的数据库上重复运行。脚本会清交易池日志，但不会清链状态。

## 离线准备阶段的验证与阻塞记录

- `make -o version-build n42 BUILD_PATH=./build/perf7/` 成功。
- 两个辅助 CLI 构建成功，`go test ./cmd/txflood` 通过。
- `make -o version-build build` 全包构建通过。
- `python3 -m unittest discover -s scripts/qs -p 'test_*.py' -v`：19 项通过。
  包含七节点一致性、缺块/分叉/滞后、阈值失败退出、传播容量和预检不修改节点。
- shell 语法检查、`git diff --check` 通过；`txflood -rpcbatch 500` 正确退出 2。
- 七份新创世数据库离线初始化成功。
- 真实压测命令已尝试，启动前预检退出 1：TCP 和 UDP 均为
  `[Errno 1] Operation not permitted`，没有启动节点或发压。

最新证据：`/tmp/n42-perf7-preflight.json`、`/tmp/n42-perf7-round-attempt.log`、
`/tmp/n42-perf7-init-node0.log` 至 `node6.log`、`/tmp/n42-perf7-build.log`。
该 socket 阻塞在后续开放执行权限后已解除，当前状态见文首实测记录。

## 仍需完成

初始化同一份 JSON 只说明 Go 能解析并写入，不能证明两个客户端有效分叉与全部
配置语义完全一致。Rust 旗舰还有 leader tenure 16、binary ingest、direct push、
fast-transfer 等路径；Go 的 HTTP 发压上限、轮换领导者及 CPU 亲和性存在差异。
这些必须记录并逐步对照，不能通过跳过签名验证、状态计算或持久化保证获得表面达标。

测量器当前证明规范链收录量与节点间根一致，部分轮次另有成功收据抽查，但尚未
独立重放全部交易。仍需补齐账户余额/nonce、全量执行与最终提交证据，并继续比较
执行、提交、传播和供给阶段。微基准与工具测试均不代表七节点 TPS。

## 后续内存与 QMDB 诊断（21:07 UTC 更新）

stream-10 发压器实测 RSS 128252 KiB（约 125 MiB），显著小于预生成的约 16 GB。
节点仍有较高内存保留。21:01:25 的 node 0 heap profile（未强制 GC，采样与测量
边缘重叠）显示约 3854 MiB 堆内存，其中规范区块解码约 1303 MiB。
cache-11 保持同一节点二进制，设置既有 `N42_BLOCK_CACHE_BLOCKS=4`，默认 512
不变。该轮完成测量后的堆采样约 2250 MiB，区块解码约 390 MiB；采样时点与
GC 时机不同，不能精确归因全部差值。两轮都是 90 秒内 12 个满块，未测出 TPS
增益。逐秒观测到的七节点 RSS 峰值约 67.84 / 65.61 GiB，证据在两轮
`resources-*.jsonl` 和 `heap-*.pprof`，这不是隔离资源下的配对证明。

QMDB 根日志把主要耗时定位在 Apply：每条 Set 原来立即重算 11 层 twig 祖先及
liveness bitmap。`lib/qmdb` 已有 Begin/EndLeafBatch，但 `QMDBRootComputer`
没有调用它。现仍按同样的 keyHash 顺序应用操作，只在整批结束时合并重算。
根值、slot、活动位、undo 和持久化格式不变；新增差分测试直接对照 eager Tree，
比较每块 root 和 undo 字节，执行回退、再应用和 proof 验证，跨多个 twig。
完整 commitment/QMDB 测试、针对性 race 与禁用 AVX-512/AVX2 的路径均通过。

163000 个独立账户（完整 64-bit 地址编号）的微基准中位数：433.8 ms →
159.4 ms；分配次数约 652329 → 489475，分配字节约 138.6 → 146.6 MB。
减少重复哈希会增加少量批处理 scratch；这是明确的时间/空间取舍。
旧节点二进制保存在 `build/perf7/n42-before-qmdb-batch`。qmdb-12 保持 cache-11
的输入与缓存限制，三个窗口均满块且通过 9 笔收据。日志中 Apply 由之前约
600 ms 降到约 64–216 ms，晚期仍有波动。

push-13 仅开启既有 `N42_PUSH_BEFORE_WRITE=1`（同一节点二进制，默认不变），
日志确认 `pushedEarly=true`。先检查候选块的 applied parent，再推送供跟随者
提前执行；Proposal 仍在本地写入成功之后。三个窗口同样满块并通过收据检查。
目前只有诊断轮次，没有三组隔离资源配对；不能用这些点估计宣称达标。

重复接收账户的历史 bitmap 仍有明显分配：163000 个已有历史键的微基准
中位数约 243.3 ms、95.3 MB、489 万次分配。对能放入一个 chunk 的 bitmap，
现在直接 RunOptimize/编码，避免通用 CutLeft64 构造范围再求交集的副本。
按优化前大小决定是否分片，保持原有表字节。相同微基准约 208.5 ms、49.7 MB、
293 万次分配；账户/存储键差分、array/run/bitmap 表示、chunk 阈值和跨 uint32
高度检查通过。该热点改动尚未进入 push-13 的节点二进制。


## 全仓验证与下一轮条件（21:20 UTC）

- `make -o version-build build`：通过。
- `GOMAXPROCS=16 make test-short`：通过，250 个有测试的包、356 个无测试包。
- `GOMAXPROCS=16 make race-core`：vm、state、internal、sync 全部通过。
- `GOMAXPROCS=16 make vet`：全仓通过。
- harness 的 19 项 Python 测试、shell 语法与 `git diff --check` 通过。
- `make lint`：退出 2，环境未安装 golangci-lint，没有将其记作通过。

证据：`/tmp/n42-perf7-build-current.log`、`test-short-current.log`、
`race-core-current.log`、`vet-all-current.log`、`lint-current.log`
（后四项同样带 `n42-perf7-` 文件名前缀）。

history-14 保持 push-13 的参数，仅在节点代码中增加热点 bitmap 直接编码，三个窗口
几乎相同，故没有证明端到端收益。下一轮继续保持相同节点二进制和参数，先移除
压测准备阶段遗留的自身内存占用：本次自建 Go 缓存 8.9 GiB 原在 `/tmp` tmpfs，
已在所有本轮节点和 Go 编译测试退出后迁移至
`/data/blockchain/gov5-perf7-20260904-2017/go-build-cache`。
原 `/tmp/n42-gov5-audit-gocache` 保留为指向磁盘路径的软链接，防止旧命令重新
占用 tmpfs；`build/perf7/build-env.sh` 也记录新位置。没有改动其他任务的缓存。
memory-15 已完成，三个窗口为 32,599 / 27,166 / 21,728 TPS，均为满块，七节点
窗口末哈希与状态、收据、交易根一致，9 笔收据抽样通过，所有节点正常停止。
与上一轮几乎相同，移除这部分内存占用没有证明吞吐改善。

该轮 15 秒 CPU 采样包含 37.30 CPU 秒，其中 `qmdb.Tree.ApplyUndo` 为
3.89 CPU 秒（10.43%）。检查发现恢复每个旧槽位时都会扫描整个 `deadFlushed`
队列，形成回滚条目数乘待删除条目数的复杂度。下一轮优先验证这一候选块回滚
瓶颈；普通转账执行优化尚未落地，不能把未来预期计作收益。

## 固定 Rust 参考版本的负载偏差

在 `48263495dd5783ca946cb740340405b0dddfd911` 的
`crates/n42/h2-node/examples/tx_flood.rs:419`，二进制注入函数
`flood_over_ingest` 以线程内 `index * per_tx + nonce` 选择收款人，没有纳入
worker 的全局发送者偏移；同文件 RPC 路径第 288 行则使用全局编号。
因此 `6000 senders / 64 workers / 6000 pertx` 中，64 个线程复用同一收款
序列，最多只有 `ceil(6000/64) * 6000 = 564000` 个不同输入编号，而不是
全局 3600 万个输入编号。对固定参数完整枚举确认实际也只有 564,000 个不同
收款地址（这一编号区间内，32 位乘法再模两百万没有额外碰撞）。

当前 Go 生成器以全局编号模两百万，再用独立域 Keccak 派生地址；还采用
sender-major 提交，而 Rust 注入按线程内发送者轮转。因此设置值相同不代表
每块不同账户数和热账户重用率相同。Rust 300k 仍是用户要求的目标数值，但
**现有资料不能证明它与本轮 Go 负载等价**。正式配对前必须统一生成器的编号、
地址映射与提交调度，并记录实际每块不同 sender/recipient 数；不能把 Go
负载悄悄缩减为较少收款人来宣称达标。本次仅检查参考代码，没有修改正在被
其他任务使用的 Rust 工作区或集群。

## undo-16：移除候选回滚的二次复杂度

只变更 QMDB ApplyUndo 的待回收列表过滤，其他参数与 memory-15 相同。
90 秒提交 16 个满块（上一轮 15 个），三个窗口 32,599 / 27,166 / 27,163 TPS。
七节点窗口末哈希与各根一致，9 笔收据抽样通过，所有节点正常停止。
CPU 采样 34.28 CPU 秒，ApplyUndo 已不再位于热点前列；这仍是受共享主机负载
影响的单轮结果，不能把微基准约 170 倍加速作为 TPS 加速比。

新增 `lib/qmdb/revert_reclamation_test.go` 验证回滚后落盘的值不被错误回收，
并提供 20,000 / 163,000 项基准。测试数据使用分散键；最初采用前八字节大端
递增的 `rvKey` 导致 flatIndex 桶碰撞，旧的 `/tmp/n42-perf7-undo-{before,after}.log`
被作废，不能引用。有效对照为 `undo-before-uniform.log` / `undo-after-uniform.log`
（同目录且带 `n42-perf7-` 前缀）：163,000 项中位数 4.308 秒 → 25.34 毫秒。
旧算法通过 Go overlay 仅替换 revert.go 复测，其他源代码与测试完全相同。

验证：QMDB / commitment 全部测试及完整 race 测试通过（race 分别 205.076 /
49.764 秒）；internal、miner、sync 测试通过，新节点二进制构建成功。
证据位于 `/tmp/n42-perf7-undo-{tests,race,consumers,build}.log`。

## undo-16 的完整收款余额、nonce 和签名核对

停机后只读扫描了七份 MDBX。每个节点均通过：

- 独立校验 applied 祖先链上 3,869,600 笔交易的发送者签名；
- 其中 3,863,600 笔为 value=1 的压测转账，另有 6,000 笔资金分发交易；
- 两百万个收款账户的余额等于该祖先链上的累计转入值，nonce 均为零；
- 647 个已发出交易的账户，其逐笔 nonce 连续，最终 nonce 与落盘状态相符。

七份 applied 标记均为高度 43、哈希
`0x069739b6f10a37412d3d441e26c66e4eb02d5ca23a637e6cb71bea0f09562ac4`。
**该高度停机时尚不属于规范链**，所以这是 applied 状态核对，不能冒充新增的
规范链窗口结果。工具按 applied 标记回溯父链，并拒绝收款人和 genesis、资金
分发、发送者或区块奖励账户重叠的情形。没有重放费用/奖励账户余额，也没有
进行独立 EVM 全量重执行。

测量窗口另行扫描得到 2,608,000 笔交易、437 个发送者、两百万个不同收款人；
16 个满块每块均有 163,000 个不同收款人，发送者数为 28–30。证据为该轮目录的
`workload-audit.json`、`balance-audit-node0.json` 至 `balance-audit-node6.json`。
本机只读工具源码保存在 `build/perf7/_workload-audit-src/main.go` 和
`build/perf7/_balance-audit-src/main.go`，用 `go build -o build/perf7/balance-audit
./build/perf7/_balance-audit-src` 构建，逐节点传入 `--datadir` 和
`--genesis build/perf7/genesis-3423g.json`。首版工具拒绝缺失 wire From 的交易；
修正为从签名恢复缺失地址后，七节点全部通过，原始拒绝不是节点执行失败。

## binary ingest 验证边界的补充核对

不能根据 `internal/ingest/server.go` 旧注释推断实际跳过验签。实际调用链为
`SetFrom → TxsPool.AddLocal → validateSender → transaction.Sender`；SetFrom
仅写入声明地址，并不填充验签缓存。节点的交易池会用本链 signer 恢复 V/R/S，
拒绝错误声明及其他链的交易。新增 native decode + trailer 赋值回归确认有效
交易通过，伪造 sender、错误 chain ID、未签名交易拒绝，相关 race 测试通过。
已纠正文档注释；目前仅 mock pool 的 ingest 传输测试本身不能证明验签，若替换
自定义 TxPool，仍必须提供验证。后续优化可评估二进制批量提交；截至 undo-16，
所有有效集群测量仍通过 JSON RPC 注入，未启用 binary ingest。

本阶段末 `make -o version-build build` 再次通过，日志为
`/tmp/n42-perf7-build-after-undo-audit-fixed.log`。前一次构建失败由本次临时
Go overlay 对照文件落在 `go build ./...` 扫描范围内引起；已将对照文件改为
`_revert.go`、只读审计工具源码目录改为 `_` 开头，不把准备产物作为仓库包构建。
没有把失败记录删除或记成通过。七份离线结果及工具源码、二进制 SHA-256 汇总
于 `undo16/offline-audit-summary.json`。

## binary-17：批量入口实测

新增可选批量池适配，每组最多 256 笔或 1 MiB 原始交易，保留每笔验证结果、
截断帧已完整交易的原有处理语义。TCP 读取使用缓冲，Stop 关闭活动连接。
同时修正节点适配器 Stats 返回顺序：原先把 pending 地址数作为交易数传给
hardcap，实际不能按交易数限流。该缺陷来自 2026-03-21 的 d066b650d，
早于三个月窗口，属于本次性能路径追踪发现的关联问题。

CLI 提供 `--ingest.enabled/addr/hardcap`，默认仍关闭。压测脚本显式绑定
127.0.0.1:34000–34006，先用空帧验证七个入口。生成器新增 `-ingest`，
资金分发及池深监测继续使用 RPC，负载交易使用原生编码。每个线程复用连接，
同批编码后发送到七节点；部分接收的 count-only ACK 会报错，不冒充整批成功。
7 RPC + 7 TCP 端到端回归逐笔恢复签名并转回 Ethereum 编码，确认签名交易及
全局收款人分布完全相同，包括跨批次和不足一批的尾部。

race 测试通过：ingest、node、txspool、txflood；CLI 包测试、21 项脚本测试及
bash 语法检查通过。节点构建 ID 为 f80d01bb10aa5012aca65e74ca572eed58d9b61c，
也包含 undo 条目数边界修复。保留旧二进制及本轮独立脚本副本。

90 秒提交 16 个满块，32,599 / 27,162 / 27,163 TPS；七节点窗口末各根一致，
9 笔收据抽查成功，注入错误日志为空，七节点正常停止。结果与 undo-16 基本相同，
不能声称二进制入口提升了集群 TPS。窗口外 15 秒 CPU 样本为 34.76 CPU 秒，
包含收据核对请求，因此不能把其中的 RPC 占比当作负载注入占比。块阶段日志仍
显示状态执行、计算根和历史写入较重。证据目录为
`/data/blockchain/gov5-perf7-20260904-2017/binary17`。

停机后逐块扫描确认窗口内 2,608,000 笔普通转账、438 个发送者，
1,994,000 个不同收款人，每块仍为 163,000 个不同收款人。配置中的两百万是
生成器地址空间，具体窗口覆盖量受提交顺序和窗口边界影响，不能写成每轮都
恰好覆盖两百万。该扫描不重复验签；原始结果见 `workload-audit.json`。

下一步重点评估 n42-rs 的普通转账执行优化及本地持久化路径，并统一双方生成器
的收款地址与发送调度。每窗 300k TPS、正式隔离配对和完整 EVM 重执行仍未完成。

## memo-18：有界复用同一块中的历史合并

`writeIndex` 在一次调用内缓存最多 128 份相同输入尾位图的合并结果。输入与
输出各不超过 1950 字节 chunk 门槛，键和值均独立持有；MDBX Put 前复制原输入，
避免其页视图失效。每个账户仍独立查询数据库、写入自己的索引，跨块不复用缓存，
超过容量或需要拆分的位图继续正常处理。没有关闭或延后历史索引。

16.3 万个已有历史账户的三次微基准中位数 185.11 → 100.26 ms，
49.65 → 5.25 MB、约 293 万 → 16.3 万次分配；新账户基准 64.78 → 65.85 ms。
旧版本通过仅撤销缓存的 Go overlay 对照，保留此前索引优化。
重复输入、多于 128 种不同历史、账户/存储两类键、重复块高和 32 位高度边界的
逐表字节对照通过；历史测试 race 63.259 秒通过，state、internal、miner、sync
完整包测试通过。证据日志为 `/tmp/n42-perf7-history-memo-*.log`。

七节点参数与 binary-17 相同，90 秒提交 18 个满块、2,934,000 笔交易，
38,033 / 32,599 / 27,166 TPS。七节点窗口末各根一致，9 笔收据抽查通过，
注入错误日志为空，全部正常停止。节点 0 的测量块历史写入中位数为
213.7 ms（上一轮 388.9 ms），总写入 985.8 ms（上一轮 1370.1 ms）；
同轮 fsync、QMDB flush 也有变化，不能将所有收益归于缓存。单轮结果仍受
共享主机影响，后段吞吐下降仍未解决。

停机后扫描窗口交易：490 个发送者、两百万个不同收款人，每个满块仍为
163,000 个不同收款人。另增加只读双向历史检查工具：每条 changeset 的块高
必须在对应历史位图中，每个历史位图条目必须有对应 changeset，且两侧总数
一致，用于检测缺项、串项及重复项；这不等于独立 EVM 重执行。源码在
`build/perf7/_history-audit-src/main.go`。轮次目录为
`/data/blockchain/gov5-perf7-20260904-2017/memo18`，检查结果逐节点保存为
`history-audit-nodeN.json`。

七份历史检查全部通过：每节点 4,026,808 条账户 changeset 与历史项逐一对应，
涉及 2,006,013 个历史 chunk；131 条存储 changeset 与历史项对应。
applied 标记均为高度 44、哈希
`0x87ca5e288c83815432dc3dfa87537f735be9669cc59801c5adc97db8e4f679b3`。
这只证明落盘 changeset 与索引的对应，不证明该 applied 候选已经规范提交。
结果和本机审计工具的源码、二进制 SHA-256 汇总于 `offline-audit-summary.json`。

第 18 轮后补强入口停机顺序：Stop 关闭 socket 后等待正在执行的 AddLocals
退出，随后节点才关闭交易池。新增阻塞池回归验证等待边界；此修改不属于
memo-18 的测量二进制。普通转账快速路径尚未实现：Go 的移动验证 read-log
和 witness 还要求审查读取顺序，不能仅照搬余额算术来宣称执行等价。

阶段末 `make -o version-build build` 全仓构建通过，日志为
`/tmp/n42-perf7-build-after-memo18.log`；21 项脚本测试与差异空白检查通过。
入口/节点停机 race 分别 1.225 / 2.974 秒通过，日志为
`/tmp/n42-perf7-ingest-shutdown-race.log`。当前 `build/perf7/n42` 仍为 memo-18
测量版本，下一轮前需要重新构建以包含停机等待修复。


## 统一生成器与 Rust-19 对照

`txflood -ingest-protocol n42-rs` 使用同一套签名交易、发送者顺序和收款地址公式，
只切换入口帧编码。Rust 帧为 u32 数量、u32 长度及 Ethereum 原始交易；完整消费
accepted/pending 两个 u32 ACK 字段。异步 ACK 仅证明入队，TPS 仍由规范区块计数。
Gov5 原生入口及默认参数保持兼容。双协议均通过 7 RPC + 7 TCP 逐交易对照、
签名恢复、非零 pending ACK、部分接收失败回归；txflood race 通过。

固定 Rust 参考 checkout 为 `48263495dd5783ca946cb740340405b0dddfd911`，
位于 `build/perf7/_n42-rs-reference`。复制其现有 release 二进制至本任务的
`build/perf7/rs-bin`，使用独立目录和端口，不修改原工作区或管理其他人的集群。
启动脚本 `build/perf7/run-rust-unified19.sh` 及结果 manifest 保存工具 SHA-256。

Rust-19 连续三个 30 秒窗口为 81,499 / 65,199 / 59,766 TPS，38 个满块，
6,194,000 笔转账；七节点根一致，9 笔收据通过，全部正常停止。停止验证者后
单独重启自己的 node0 执行层，从 RPC 重读全部窗口区块，核对规范哈希、状态根、
交易内容、nonce 连续性及 gas：1,119 个发送者、2,000,000 个不同收款人，
每块 163,000 个不同收款人。该扫描不重复独立验签。结果位于
`/data/blockchain/gov5-perf7-20260904-2017/rust-unified19/workload-audit.json`。

这是诊断轮，不是正式配对：Rust 使用物理核心分区、30 秒空块衰减、内部队列
背压和异步入队 ACK，旧 Go 轮没有同样 CPU 分区与衰减；双方该轮 tenure 均为 1。
Rust 移动验证关闭，Go 移动验证管线仍存在。历史 Rust 旗舰生成器还存在收款地址
调度差异，不能用缩减 Go 的地址空间来对齐旧成绩。每窗 300k TPS 的目标不变。

## tenure-20：连续领导者与 CPU 分区

新增共识配置 `hotstuff.leaderTenure`，默认缺省/0 为 1；领导者公式为
`(view / max(tenure, 1)) % validatorCount`，与固定 Rust 参考一致。提案验证、
投票、超时、出块服务全部使用同一策略。16 视图配置必须由全网一致采用，不能
作为单节点运行中开关。七节点模拟覆盖 113 个视图、111 次提交、超时与领导者
切换，包含旧签名域及 H2-v4；配置往返、错误领导者、整数边界测试和 race 通过。

Go-20 使用新 genesis 的 tenure=16、每节点 16 个物理核及其 SMT 线程的请求
分区、生成器独立分区、30 秒空块衰减。结果 38,032 / 32,599 / 27,166 TPS，
18 个满块，2,934,000 笔转账，七节点根一致、9 笔收据通过、注入错误为空，
七节点正常停止。离线扫描为 493 个发送者、两百万个不同收款人，每块 163,000。
本轮未在进程存活时保存实际 CPU affinity，只保存请求配置，不能声称已独立核验。
日志中 node0 在 view 112–127 连续担任领导者，证明 tenure 实际生效。
结果位于 `/data/blockchain/gov5-perf7-20260904-2017/tenure20`。

节点 0 测量块写入中位数：state 249.34 ms、changeset 125.45 ms、history
247.35 ms、MDBX commit 201.21 ms、QMDB flush 70.96 ms，总计 1038.29 ms。
吞吐与 memo-18 基本相同，没有证明 tenure 单独提升性能；本轮还同时改变 CPU
分区和衰减，不能当作只改一个变量的 A/B。窗口外 CPU 样本中验签较重，但并非
测量窗口内的关键路径耗时分解。

进一步检查发现，成功本地封块后只有 `NotifyBlockSealed`，缺少本地持久化的
import 证据，导致连续领导者的提前构建提示被检查挡住。正在补齐成功写入后的
通知；不把已有 body 或被选中的 sibling 当成已执行证明。这项修复不属于 Go-20。
`make -o version-build build` 在 Go-20 版本通过，21 项脚本测试通过；后续通知
修复仍需另行验证。Rust-21 已用相同 tenure=16 启动，结果待记录。


## Rust tenure-21 与 Go 通知修复

Rust-21 使用同一统一生成器、tenure=16、30 秒衰减、物理核心分区；保存全部
14 个 EL/CL 进程实际 CPU affinity。三个窗口为 107,886 / 79,404 / 54,332 TPS，
49 个块、7,248,800 笔转账，前两窗占用率 90.3% / 86.0%，末窗为 100%。
七节点根一致、9 笔收据通过、集群正常停止。独立重启 node0 执行层重新扫描
全部窗口区块通过，覆盖两百万不同收款人，见 `rust-tenure21/workload-audit.json`。
吞吐后段下降仍存在；不能把这一个诊断轮当成三轮正式配对验收。

补齐本地成功写入后的 `NotifyBlockPersisted(hash, txRoot, parentHash)`，随后才
发送封块提案通知。只放在 `WriteBlockWithState` 成功的新封块路径，已有 body、
最低哈希 sibling 或重提旧块不产生新执行证明。共识引擎因此可以验证封块父哈希
并提示下一视图的提前构建。新增适配器回归证明缺少持久化通知时不提示、正确
通知时提示正确父块、LockedQC 移动导致父块不符时不提案/不投票。
HotStuff race 9.835 秒、miner race 1.191 秒通过；节点二进制构建通过。
日志 `/tmp/n42-perf7-persisted-hint-{race,build}.log`。Go-22 正在验证实际命中效果。
首次 Go-22 preflight 因未创建空结果目录退出，尚未启动节点；建目录后重试，
失败记录保留在 `/tmp/n42-perf7-go-hint22-preflight-failed.log`。


## hint-22：提前构建实际生效

保持 Go-20 的 tenure=16、CPU 分区、30 秒衰减和负载参数，测量二进制仅新增
成功持久化通知。三个窗口为 48,899 / 43,466 / 38,033 TPS，24 个满块、
3,912,000 笔交易；相较 Go-20 的 18 个满块多 33.3%。七节点窗口末根一致，
9 笔收据通过，全部正常停止。独立读取 `/proc` 核验并保存七节点和生成器的
实际 CPU affinity；轮次目录 `hint22`，启动命令为
`bash build/perf7/run-go-hint22.sh`，使用独立 harness 副本。

测量规范块高 125–148 中有 22 次提前构建命中，其余位于领导者轮换边界。
`measured-phase-summary.json` 保存逐节点命中块号和写入阶段中位数。节点 0
写入总耗时中位数 1333.03 ms，比上一轮更高，但构建与导入开始重叠，集群
提交吞吐仍提高。单轮共享主机成绩不能替代正式重复配对。

全仓 `make -o version-build build` 通过，日志
`/tmp/n42-perf7-build-after-hint22.log`。停机后增加全节点独立验签、收款余额、
发送者 nonce 及历史双向检查。首节点已通过，applied 高度 156 属于规范链，
恢复 5,925,000 笔签名，核对 2,000,000 个收款账户和 989 个发送者 nonce。
这些检查不包含费用/奖励余额重放，也不等于独立 EVM 重执行；其余节点结果待汇总。


hint-22 七节点余额/nonce/验签与历史双向检查均已通过，结果及工具源码、二进制
SHA-256 汇总于 `hint22/offline-audit-summary.json`。node2 的 applied 高度为 157，
属于未提交候选，其余节点 applied 为规范高度 156；分别按实际 applied 祖先链
核验，不能把候选多出的交易算作 TPS。规范高度 156 的节点各恢复 5,925,000 笔
签名，核对 2,000,000 个收款账户与 989 个发送者 nonce；账户 changeset 与历史
项各 5,926,366 条、存储项 467 条。窗口交易扫描确认 655 个发送者、两百万个
不同收款人，每个满块 163,000 个不同收款人。

目标仍未完成。下一步追踪进口区块的 sender 验证：当前 `verifyBlockSenders`
先于池提示执行，而池提示跳过带 wire From 的交易；全局直接映射缓存被覆盖后，
这类交易仍可能重复恢复签名。可以评估复用池内已验证对象的签名缓存，但必须
逐笔保留 wire From 比较、按实际 fork signer 校验、哈希匹配及错误路径回归，
不能把“在池中找到”当成无需验证的授权。该优化尚未实现，也没有记入当前成绩。


## pool-23：复用交易池对象的签名缓存

带 wire From 的区块交易在 `verifyBlockSenders` 中先验签，后续池提示会跳过
这类交易。全局直接映射缓存被覆盖后，即使池中仍持有已验签对象，也会重做
曲线恢复。现在按完整签名交易哈希取得池对象，再以区块实际 signer 调用
`transaction.Sender`，利用该对象的签名缓存；仍逐笔比较导入交易声明的 From，
仍检查 V/R/S 存在。池对象哈希不符或未找到时走原恢复路径。

新增回归包括全局缓存失效但对象缓存有效、伪造 wire From、伪造池对象 From
字段、错误池哈希、池未命中和错误链 ID，覆盖串行及并行门槛；错误保持最低
交易索引，验证不修改 wire From。internal、txspool、transaction 的整包 race
通过。163,000 笔微基准（16 Ps、全局缓存关闭、模拟池全命中）三次中位数
436.367 → 5.994 ms，94.56 MB → 6.7 KB、约 244 万 → 41 次分配。该最坏
全局缓存对照不含真实交易池查找锁成本，不能当作线上幅度，日志
`/tmp/n42-perf7-pool-verify-bench.log`。

同参数七节点实测为 48,899 / 43,465 / 38,033 TPS，仍是 24 个满块，未证明
集群吞吐提高。七节点根一致、9 笔收据通过、全部正常停止；实际节点/生成器
CPU affinity 通过。窗口只读扫描确认 3,912,000 笔转账、654 个发送者、两百万
不同收款人，每块 163,000 个不同收款人。结果目录 `pool23`。该轮不包含下述
恢复边界修复和投票日志诊断。

## 投票日志的串行等待与恢复边界

pool-23 窗口外 goroutine 快照捕获 `NotifyBlockExecuted → onBlockImported →
journalPrepareVote → Service.JournalVote → MDBX BeginRw → mdbx_txn_begin`。
当前投票日志与区块状态写入共用 MDBX，因此提前投票仍可能等待写锁。新增可选
`N42_JOURNAL_TRACE=1`，记录 Update 进入回调前的等待、保存记录耗时及总耗时，
没有跳过同步持久化。第 24 轮量化了该等待；独立投票日志的后续实现与第 25 轮证据见下文。

恢复路径另有缺陷：短持久化记录被当作缺失、epoch 恢复错误被忽略、服务在
共识恢复错误后继续启动。已区分缺键与空/短记录，传播 active/staged epoch
错误，拒绝 QC 视图超过持久化视图的记录，并在任何恢复失败时返回启动错误。
验证者数量受记录剩余字节数约束，公钥长度用无符号剩余长度比较，避免畸形
本地记录的大分配或窄平台整数溢出。缺键仍允许新节点启动，旧合法 v1/v2
记录继续兼容。这不表示已复现网络攻击或重复投票；是持久化损坏时的停止保护。

新增恢复测试覆盖 0–15 字节记录、最大数量/公钥长度、损坏 active/staged 集合、
超前 QC（包括内存视图已更高）、正常投票承诺恢复。HotStuff/node race 通过，
日志 `/tmp/n42-perf7-journal-recovery-final-race.log`。首次测试揭示短记录仍被
解码器当作缺失，修复解码器后重新通过；没有把早期失败记作通过。


pool-23 的七节点独立验签、余额和 nonce 审计全部通过：node2 applied 候选
高度 158 尚未规范提交，其余为规范高度 157；逐节点证据和工具 SHA 见
`pool23/offline-audit-summary.json`，没有将候选交易计入吞吐。

## journal-24：投票写锁等待的量化

该轮只增加诊断及恢复保护，未改变持久化方式。三个窗口 43,466 / 43,466 /
38,033 TPS，23 个满块、3,749,000 笔转账。七节点根一致、9 笔收据通过、
实际 CPU 分区核验通过、全部正常停止。窗口交易扫描为 627 个发送者、两百万
不同收款人，每块 163,000 个不同收款人。结果目录 `journal24`。

每节点均找到对应 23 个测量规范块的 R1、R2 日志。node3–6 的全部 R1 都等待
超过 100 ms：取得写事务的中位耗时分别 1353.592 / 1436.489 / 1372.204 /
1344.594 ms，而保存记录的中位耗时只有 0.060 / 0.067 / 0.057 / 0.061 ms。
R2 取得写事务中位数均约 0.004 ms。node1 在本窗口主要担任领导者，R1 中位
等待仅 0.208 ms。该差异与 leader 已写完块才提案、follower 的投票记录等待
状态写事务相符，不应把所有节点混成一个“共识耗时”。

详细结果及逐条记录见 `journal-phase-summary.json`。24 的日志将 hash 格式化
为首尾缩写，汇总脚本确认其在测量块集合中一一对应；这是诊断关联，不是额外的
完整哈希验证。测量工具仍验证完整规范哈希和七节点根。后续源码日志改用 Hex
完整哈希，harness manifest 也增加 journal trace 和 sender-cache 配置记录。

进一步补齐恢复长度前缀的剩余空间比较，避免窄平台 pos+length 溢出；此补充
不属于 24 的二进制。新增 MaxInt32/MaxUint32 长度和负偏移回归，HotStuff/node
race 通过，完整 `make -o version-build build` 通过，21 项脚本测试及差异空白
检查通过。日志 `/tmp/n42-perf7-recovery-prefix-final-race.log` 与
`/tmp/n42-perf7-build-after-journal24.log`。没有声称实际完成 32 位运行测试。

下一步优先分离投票与区块状态的写入等待，并验证同步落盘、重启后的双投票
保护、新旧状态的单调合并、chain/validator 绑定及 epoch 切换。还需处理一个
关联性能问题：`TriggerBlockProduction` 当前会打断所有进行中的提前构建，
即使其父块与新视图完全相同；提前投票变快后，这种重复构建可能抵消收益。
以上为第 24 轮结束时的待办；后续实现见下文，目标保持未完成。


## votestore-25：同步独立投票日志与提前构建取消

新增 opt-in `N42_SEPARATE_VOTE_DB=1`，每节点使用 `hotstuff-votes` MDBX 保存
投票承诺及同一引擎快照的 active/staged epoch，仍同步提交后才能发送投票。
主库 marker 将独立库绑定到 genesis、chain ID 和本节点 BLS 公钥；完成日志
同步提交及目录 fsync 后才发布 marker。以后即使删除环境变量，也必须加载
独立库。缺失、身份错误、损坏记录或同一视图投票冲突会拒绝启动。启动时将
两个库按单调规则合并，保留较新的 QC、投票及验证者集合，已激活的 staged
集合不再重复恢复。active epoch 领先于轮次 checkpoint 时，恢复视图至少到
该 epoch 首个视图。正常 Stop 等待引擎投票后关闭独立库。

特性默认关闭。启用后应将主库与独立库共同管理；**不能使用忽略 marker 的旧
二进制进行恢复或降级**。当前两个 HotStuff reset 工具拒绝对这种节点只重置
主库。没有实现降级迁移，也没有声称支持任意不一致备份的恢复。

回归包括：持有真实主 MDBX 写事务时独立投票仍可完成；同步写失败必须弃权；
主库/日志较新字段分别合并；同视图冲突拒绝启动；缺目录/错误身份拒绝启动；
staged/active epoch 恢复；epoch 记录领先轮次 checkpoint；移除 opt-in 后恢复。
子进程在发出投票后直接 `os.Exit(0)`，跳过 Stop/Close，父进程仍能恢复承诺并
拒绝冲突投票。这证明进程退出场景，**不等同于实际断电试验**。HotStuff/node/
reset race 与目录同步后独立日志 race 通过，完整 build 通过（目录 fsync 补充
前）；补充后主节点二进制重新编译通过。日志为 `/tmp/n42-perf7-votestore-*.log`。

第 25 轮相同 6000 发送者、两百万收款人、163000 tx/block、16-view tenure、
CPU 分区与连续窗口参数，结果 **38,033 / 43,466 / 38,033 TPS**，22 个满块、
3,586,000 笔测量交易。7 节点根一致、9 笔收据及实际 CPU affinity 核验通过，
全部正常停止。结果目录 `votestore25`，未证明吞吐提升。

完整 hash 对应日志显示，每节点 R1 acquire 中位数 0.004–0.005 ms，R1 同步
总耗时中位数 0.098–0.226 ms，测量样本没有超过 100 ms 的 acquire。各节点
实际记录 R1 为 11–20 个，R2 均为 22 个：有节点先收到 QC 再完成本地执行，
不能要求所有节点都曾给每个块投 R1。另有 R2 后补 R1 的情况，故汇总通过
相邻日志中变化的投票承诺区分轮次，不能简单用 `commitView == view` 判断。
逐条证据 `journal-phase-summary.json` 与 `summarize-journal25.py`。

测量块没有提前构建 hit；日志显示提前构建开始后，匹配父块的新视图仍触发
中断，随后正式请求从头执行。修复以一个原子指针同时发布不可变的父块和
interrupt，仅父块不同/未指定时中断；相同父块由串行工作循环完成并停放，
正式请求仍经过既有 applied-head 检查、节奏限制和 seal 流程。新测试确保
匹配工作保留且仍需真实触发，旧的不同父块中断/队列驱逐测试继续通过。
miner/HotStuff/node race 通过，日志 `/tmp/n42-perf7-specconfirm26-race.log`。
这项调度修复不在第 25 轮二进制中，尚待第 26 轮实测。


第 25 轮停止后的完整审计现已通过：每节点 applied ancestry 的 5,667,600 笔
签名、两百万收款账户余额、947 个发送者 nonce，以及账户/存储 changeset 与
history 双向索引。node0–5 applied 高度 150 已规范提交；node6 同一 applied
块尚未规范提交，没有将其额外算入吞吐。范围仍不含费用/奖励余额独立重放及
完整 EVM 重执行。逐节点结果和工具 SHA：`votestore25/offline-audit-summary.json`。

随后使用第 25 轮原二进制、原七节点数据移除 `N42_SEPARATE_VOTE_DB` 后启动，
核实七个进程环境都没有该变量。起始规范高度 150/149，最终全部推进到 162，
该高度完整 hash 与状态根一致，之后全部正常停止。证据
`restart-without-opt-in.json` 和 `/tmp/n42-perf7-restart25.log`；原测量日志保留
为各节点 `log/n42.benchmark25.log`，避免将重启活动混入测量。

独立日志方向参考固定 n42-rs `48263495` 的
`crates/n42/h2-node/src/persistence.rs`：投票日志独立于 checkpoint，在签名前
同步持久化，恢复采用更保守的承诺。Go 继续使用 MDBX 事务，并额外绑定本节点
身份及完整投票 hash/epoch 快照，没有采用 Rust 的诊断 nosync 开关。
调度修复后的完整 `make -o version-build build`、21 项 harness 测试、差异检查
通过。第 26 轮首次 preflight 因新输出目录未创建而停止（未启动节点）；创建
空目录后重跑，失败日志单独保留，不计为性能轮次。


## specconfirm-26：构建复用生效，领导者轮换仍空等超时

第 26 轮连续窗口为 **37,866 / 43,461 / 27,166 TPS**，20 个测量块、
3,255,000 笔交易。首个块 158000 笔，其余 163000；544 个发送者、两百万
不同收款人。7 节点完整规范哈希/根一致，9 笔收据、实际 CPU 分区通过，全部
正常停止。18/20 个测量块命中提前构建（25 为 0），证据
`specconfirm26/speculative-hit-summary.json`。调度行为已改变，但没有吞吐提升。

第 3 个窗口发生一次轮换停顿：node2 在 00:05:24 UTC 成为 view144 领导者，
父块高度140尚未落到其已应用状态，gate 返回 `parent-not-applied`。父块随后
成功导入，但生产只在 00:05:32 的 view145 超时转换后重新触发，造成约8秒
空等。不能去掉父块 gate；问题是 `NotifyBlockImported` 没有恢复这一轮被
暂缓的生产，且它与提前执行通知共享去重，使完成持久化不会再次触发引擎通知。

新增 output loop 拥有的 pending production 请求及容量1的导入唤醒通道。
持久化导入通过应用证据检查后，在投票去重之前发出唤醒；只有原视图、当前
领导者、LockedQC父块仍匹配且服务未停止时才重试，重试仍执行原全部 gate。
成功后清除请求，新视图或失效授权清除旧请求。没有从导入 goroutine 直接
启动构建，也没有把早期执行通知当作落盘证据。

回归复现“提前执行 → 领导者被 gate 暂缓 → 去重的落盘导入”顺序，证明同视图
恢复生产、重复通知不重复生产；覆盖父块尚未落盘、落后门槛、视图/锁/领导者
变化、移除成员及取消服务。目标测试和完整 HotStuff/miner/node race 通过，
日志 `/tmp/n42-perf7-retry27-tests.log`、`/tmp/n42-perf7-retry27-race.log`。
这项重试不在第 26 轮二进制中，第 27 轮再测。300k 与三轮配对验收仍未完成。


第26轮停止后的七节点独立验签/接收账户余额/发送者nonce/双向history审计全部
通过，证据 `specconfirm26/offline-audit-summary.json`。node0–1 applied149为
规范块（5,380,200笔验签）；node2–6 applied150为尚未规范提交的候选块
（5,543,200笔验签），未将这些候选额外计入测量TPS。

## retry-27：持久化导入后的同视图重试

第27轮 **47,399 / 43,466 / 32,593 TPS**，23个测量块、3,704,000笔交易；
7节点规范哈希/根一致、9笔收据和实际CPU分区通过，全部正常停止。
22/23个测量块复用提前构建。node1 view128 在00:13:01遇到parent-not-applied，
00:13:02 同一view已恢复trigger；测量段没有跳到下一视图才触发的8秒空等。
这证明重试行为生效，没有证明端到端已达标，也不能用单轮差值作隔离配对结论。
主节点二进制、完整build、HotStuff/miner/node race通过；目录 `retry27`。

下一步对照QMDB与plain账户/存储读取。第26轮窗口外CPU采样中，ApplyMessage
累计2.23 CPU秒，其中 getStateObject 的相关调用约1.54 CPU秒；这提示状态
读取是执行部分的主要候选热点，不是全部进程CPU的多数。现有 verify 模式
曾忽略读取/解码错误，现将任何一侧错误计作失败比较（进入 mismatch），保留
plain结果和错误作为实际返回值。周期报告在本次比较计数后输出，关机新增
最终计数，避免最后不足500k次的样本没有最终汇总。只记录零差异仍不能证明
所有状态或fork语义，模式默认继续关闭。第28轮将用 `N42_STATE_READ_QMDB=verify`
读两套来源、仍从plain执行；这是一致性诊断，不是读源切换后的性能测量。


第27轮窗口交易扫描确认620个发送者、两百万不同收款人。停止后的七节点完整
验签、接收账户余额、发送者nonce与history双向检查全部通过，逐节点applied/
canonical区别及工具SHA见 `retry27/offline-audit-summary.json`，不含独立费用/
奖励重放与完整EVM重执行。QMDB错误计数回归和commitment/node race通过
（50.627 / 2.371秒），全仓build、21项harness测试和差异检查通过。第28轮
verify二进制已启动，固定脚本/环境与证据目录 `qread28`；同一负载不变。


## qread-28：现有QMDB点读与plain逐次对照

连续窗口 **43,465 / 43,059 / 35,846 TPS**，3,671,200笔测量交易、613个发送者、
两百万不同收款人。7节点根/哈希、9笔收据、实际CPU分区通过，全部正常停止。
node0–6 最终比较次数分别为 4,469,813 / 3,378,165 / 4,094,360 / 5,970,305 /
5,970,305 / 5,970,305 / 5,970,305，account/storage mismatch 均为0。
结果 `qread28/qmdb-verify-summary.json`。该统计包含上层解码/plain错误，但
下述冷存储/索引接口问题尚未在28中修复，不能把这个结果解释为完整故障覆盖。

随后增加 `Tree.GetChecked`：区分缺键与索引错误、已索引条目缺失/截断、
错误完整key、内存条目非活跃、冷reader未挂载；冷读取与MDBX索引错误向上
传播，截短前缀索引无法解析holder时返回错误而不是“键不存在”。点读包装器
优先采用该接口：on模式返回错误，verify模式仍返回plain结果并计失败比较。
正常缺键保持nil/false；旧Get/ColdEntry接口及树更新路径未整体迁移，不能
声称所有QMDB存储操作都已具备相同错误处理。既有持久化格式未改变。

故障注入覆盖冷库I/O错误、索引I/O错误、条目丢失/截断/错key、越界slot、
非活跃条目、前缀碰撞与无法解析holder、空/短/超长MDBX索引记录、合法slot0，
以及verify/on/off三种模式的返回结果。跨8轮写入/删除/驱逐对照正常Get并验证
root不变。针对性race通过，日志 `/tmp/n42-perf7-checked29-tests.log`。
第29轮拟使用同样负载、带错误传播的QMDB实际点读；矿工仍使用plain reader，
因此只衡量跟随者读源变化，仍写入原全部状态/历史表。


第28轮停止后的七节点独立审计全部通过：各节点applied ancestry验签5,966,800笔，
两百万接收账户余额和996个发送者nonce通过，双向history对应通过。applied均
153，node0/6该块尚未规范提交，其余已规范提交；没有将候选额外计入吞吐。
逐节点数据和工具SHA见 `qread28/offline-audit-summary.json`。第29轮的完整build
通过；全量QMDB/commitment/node race随后通过（206.334 / 50.644 / 2.376秒），
日志 `/tmp/n42-perf7-checked29-race.log`。第29轮现已启动。


## qread-29：带错误传播的QMDB实际点读

连续窗口 **27,166 / 36,759 / 32,599 TPS**，18块、2,895,800笔交易，
485个发送者、两百万不同收款人。7节点规范哈希/根、9笔收据及CPU分区通过，
全部正常停止。窗口外CPU采样确认QMDBStateReader/GetChecked路径实际执行。
本轮没有显示性能收益，后续Go性能轮次恢复plain读取，checked接口保留默认关闭。

停止后的七节点独立审计全部通过：均applied148且规范，逐节点验签4,498,800笔，
两百万接收账户余额、751个发送者nonce和双向history对应通过。证据
`qread29/offline-audit-summary.json`；仍不包括费用/奖励重放与完整EVM重执行。

固定Rust参考的fleet7-env.sh将persistence threshold设为8、memory block buffer
target设为6；Rust21未覆盖这些默认值。Go当前逐块同步提交状态与历史，因此追加
Rust30阈值1/缓冲0诊断，量化配置敏感性。Rust底层仍是异步持久化架构，此设置
不能证明其与Go具有相同崩溃耐久性。旗舰参考仍是Rust21；不降低300k或95%目标。

## rust-persist-30：持久化配置诊断

固定Rust二进制、相同tenure16/两百万收款人和CPU分区，将阈值8/缓冲6改成1/0。
连续窗口 **54,332 / 76,638 / 81,499 TPS**；46块、6,374,193笔测量交易。
七节点规范哈希/根与9笔收据通过，实际14个EL/validator进程CPU分区和1/0命令行
已核对；全部正常停止。单独重启node0 EL后，完整窗口规范块正文、gas、交易字段
和nonce连续性检查通过，确认两百万不同收款人。证据目录 `rust-persist30`。
该正文审计未独立重复验签或EVM执行，不能当作两客户端状态转移等价证明。

本轮中位数76.6k接近Rust21默认8/6的79.4k，窗口波动明显且不是三组正式配对。
结果不支持将Go与Rust的大部分差距归因于这两个持久化参数；旗舰参考保持不变。


## cursor-31：历史索引复用查找游标和键缓冲

writeIndex在MDBX上复用同一个读写游标，通过事务PutWithCursor保留事务写入计数、
字节数与可选write probe归因；其他后端继续调用事务Put。固定长度尾键缓冲
每次调用只分配一次，所有roaring格式、分片选择、同步写入范围保持不变。

独占微基准（163k账户，5次/样本、3样本，中位数）新历史60.634→54.028ms，
已有历史94.323→79.461ms；已有历史分配从约163,030次/5.2MB降至26次/约1.3KB。
日志 `/tmp/n42-perf7-cursor31-final-micro-isolated.log`；先前带并行race的微基准不用于
此结论。候选版本对旧磁盘格式oracle的账户/存储分片、32位边界等价race通过；
PutWithCursor成功覆盖、失败不计数及write probe检查通过。完整MDBX/state race通过（1.329/136.264秒）；随后将优化入口从游标收紧到事务，
防止绕过包装数据库的Put钩子；新增包装事务、跨事务游标拒绝与错误计数回归，
最终针对性race通过（1.019/62.756秒），全仓build和21项harness测试通过。

这些微基准不是七节点吞吐结论，第31轮恢复plain读取后验证实际收益。


第31轮 **47,499 / 43,466 / 38,033 TPS**，24块、3,870,000笔测量交易，
648个发送者、两百万不同收款人；7节点根/哈希、9笔收据、实际CPU分区通过，
全部正常停止。目录 `cursor31`。独立七节点验签/接收账户余额/发送者nonce/
双向history审计全部通过：node2 applied153尚未规范，验签6,035,000笔；
其余applied152已规范，验签5,872,000笔。候选块未额外计入吞吐。

按规范窗口高度筛选的满块日志（每轮132条跟随者导入样本，不等价于日志本身
逐条绑定规范hash）：第27→31轮跟随者history写入中位数267.5→232.3ms，
写入总计1398.9→1343.0ms，但commit 504.3→583.6ms，总导入3159.4→3144.2ms。
此结果支持局部热点改善，没有证明显著端到端提升或正式配对达标。
证据 `cursor31/block-phase-summary.json` 与 `retry27/block-phase-summary.json`。

第31轮窗口外15秒CPU采样中68.49 CPU秒：secp256k1恢复调用27.86秒；
StateTransition约2.12秒，其中GetCode的1.37秒几乎全部用于第一次加载接收账户，
并不是执行合约代码。后续将评估保持同一只读快照的有序账户预读，先测整体
收益再接入；不能启用已有共享MDBX游标的并行预取/执行路径。
300k每窗口和三组Go中位数≥95% Rust的验收仍未达成。


## prefetch-32：独立快照事务的账户读取实验（尚非集群轮次）

在停止的cursor31/node0数据库上，取规范块152的163k收款地址，以当前同一
只读快照交替比较6组，计时包括排序、分配、预读、按原交易顺序消费。完整
账户值逐项核对，并验证调用者修改返回账户不会污染后续读取。顺序预读使用
结果只交给首次读取者的缓存，后续同地址读取回到原快照。并行版本每工作线程
单独BeginRo和PlainStateReader，强制其ViewID与基准事务相同，各自关闭事务。
没有共享MDBX读游标，没有让不同快照的数据混入缓存。

中位耗时：plain186.49ms；带复制的有序缓存175.46ms；首次读取移交结果的
有序缓存149.24ms；独立事务2/4/8/16线程107.34/89.25/80.98/75.10ms。
分配量plain约22.17MB，并行约37.24–37.26MB。此数据包括缓存建设，仍不是
EVM执行或七节点TPS；尚未将预读功能接入节点。固定源码、二进制、测量与SHA
在 `prefetch32-experiment`；包含全部7种模式和逐项值检查的race运行已通过，
后续接入必须另测快照变更回退、
读取错误、执行/收据/见证等价和七节点端到端表现。

## prefetch-33：串行导入前的有界账户预读

`N42_ACCOUNT_PREFETCH_WORKERS`默认0，最多16；仅直接MDBX、plain读源且没有
启用旧并行执行/共享读游标预取时接入。串行StateProcessor验证发送者后，
对2048至262144笔交易的收款地址排序、去重，各工作线程打开独立只读事务；
ViewID必须严格等于执行事务，任何版本变化、打开/读取/解码失败或取消都丢弃
整个预读结果。缓存只将拥有的账户值交给首次读取，重复读取回到原快照；
缺账户正常缓存为nil。代码/存储读取和StorageEnumerator继续由plain实现。
所有交易仍按原顺序执行，矿工尚未启用预读。预读成本单列prefetch，并包含于
Process及总导入耗时；实际命中数单独记录。

1/2/4/8/16线程逐项值和调用者修改隔离、重复/缺失账户、快照推进、事务打开/
读取/解码错误、取消、工作量上限、关闭工作事务与失败回退检查通过。混合
转账、存储/日志合约、REVERT、预编译、创建合约的逐交易收据/return/gas、
账户和存储状态，以及实际mobile ReadLogRecorder记录/字节码对照通过。
读取记录对照通过外部测试包调用测试桥接，未新增生产导出接口。存储枚举通过。

完整internal/miner/node race通过（2.711/1.198/2.442秒），完整build与21项harness
测试通过，日志 `/tmp/n42-perf7-prefetch33-race.log`。第33轮将以16线程、同一
两百万收款人负载测试真实收益；尚不能用离线75.10ms宣称集群已改善或达标。

第33轮 **48,406 / 43,466 / 38,033 TPS**，24块、3,897,200笔测量交易，
651个发送者和两百万不同收款人。七节点规范哈希/根、9笔收据和CPU分区通过，
全部正常停止。离线七节点独立验签、接收余额、发送者nonce与双向history全部
通过：均applied153且已规范，各验签6,062,000笔、1011个发送者nonce。
证据 `prefetch33/offline-audit-summary.json`；不包括独立费用/奖励重放或全EVM重执行。

满块跟随者导入日志中位数，第31→33轮：exec957.0→709.0ms，新增prefetch68.5ms，
Process1609.2→1480.8ms，总导入3144.2→2990.8ms；写入1343.0→1351.5ms。
局部读取优化在真实导入中生效，但三个窗口块数仍9/8/7，没有显著吞吐提升。
日志时间分解不是正式隔离配对结论。预读命中/回退记录见`prefetch33/prefetch-summary.json`。

下一步候选是QMDB热点保留：当前EvictFlushed每次清除全部已落盘entry记录和
sealed twig节点，Tree.Set更新旧键仍会经过冷数据读取。窗口外CPU样本中
QMDB ComputeRoot累计1.08 CPU秒、Tree.Set0.71秒，提示可评估有界保留缓存；
尚未修改驱逐策略，任何实验都必须保留完整落盘与根/undo/reload一致性。


## retain-34：有界QMDB驻留窗口与独立提交边界

新增默认关闭的`N42_QMDB_RETAIN_ENTRIES`，上限4,194,304槽；计划测量值
2,621,440槽。逐块写入/历史和fsync不变，只延后释放最近entry值和sealed twig
节点。该上限是槽数而非字节数，需要另看实际内存。原策略把已驱逐边界当作
落盘边界；保留实验在第3块出现未回收旧行，虽root/undo相同但四表字节不等。
已修复独立提交边界，详见审计A12，禁止以更少回收工作制造性能收益。

10块两种保留策略的四表字节/root/undo、点读/证明、磁盘重载、跨窗口回退后
分叉重执行全部一致；成功flush后事务撤销、metadata末尾失败、Abort提交边界、
无leaf blob时保留恢复数据和不增加驻留回收队列的针对性race通过。全量
qmdb/commitment/internal/node race正在进行；通过后再启动七节点，并采样
进程RSS和I/O，避免把局部冷读收益当成完整性能结论。

全量race已通过：qmdb206.944秒、commitment51.872秒、internal2.791秒、
node2.508秒；完整build与21项harness测试通过。第34轮已运行并正常结束，
三个窗口 **43,466 / 43,466 / 38,032 TPS**，23个满块、3,749,000笔交易。
七节点规范哈希/根、9笔收据、CPU分区检查通过，七节点全部干净退出。
离线独立验签/余额/history审计尚未运行，等待共享机器占用协调；当前不能称
第34轮完整审计通过，也没有证明该保留策略提高端到端吞吐。

`retain34/memory-io-summary.json`记录整个观察期（含启动与窗口外工作）：
各节点峰值RSS约13.9–19.3GiB，未观察到进程swap，主机最低MemAvailable
约19.87GiB；每进程观察到的写入约26.4–27.0GiB。各节点峰值并非同时发生，
不可相加作为集群峰值；这些不是Go堆大小或设备写放大，亦未采集逐进程major
fault，不能据此给性能回退归因。

## 共享机器协调（2026-09-05 01:58 UTC 起）

用户要求perf7驱动遵循 `/data/blockchain/wr-logs/BOX-CLAIM-PROTOCOL.md`。
第34轮及其观察器均已停止；当时我方没有claim，已告知用户可安排20槽A-B-A。
后续启动集群、编译、离线审计等重任务前，重新读取协议：间隔30秒连续三次
确认没有其他驱动的集群/重任务、1分钟load<8、MemAvailable>=80GB、没有
其他90分钟内的新claim；以epoch秒写入两处
`/data/blockchain/.box-claim-codex` 和
`/data/blockchain/wr-logs/.box-claim-codex`，随机等20–50秒后复查所有竞争claim，
遇更早claim删除自身两文件、至少等待60秒后重试。占用期间至少每30分钟续期，
任务结束立即删除自身两文件；不终止其他驱动的进程。不修改正在运行或用于
历史证据的固定harness副本。

共享机器状态变化后的第一轮只作预热，不作为比较证据；之后以A-B-A包围对比，
报告窗口1及整轮总量，同时保留本任务的三个连续30秒窗口验收。后续采样应
记录每进程 `/proc/<pid>/stat` 第12字段major faults，避免仅凭主机内存推断。
既有第34轮结果保留为探索记录，未重新满足该预热/配对规则的结果不升级为
正式性能收益结论。七节点旗舰目标仍未达到。

后续我方重任务统一通过已准备的入口启动（历史固定launcher保持原样）：

```bash
python3 build/perf7/box-claim.py --record /tmp/unique-claim.jsonl -- bash build/perf7/run-go-ROUND.sh
```

该入口也包裹编译和离线审计，记录协议SHA、三次间隔30秒的资源/进程检查、
claim取得/退让/释放和子进程退出码，不记录可能含密钥的argv/env。两处文件
独占创建，同一Codex实例另有flock互斥；不覆盖已有同名claim、不删除被其他
实例替换的文件。占用每60秒续期，命令必须等待其工作进程退出并处理停止信号；
入口同时等待自身进程组中的残留工作进程，不能用于启动后即退出的后台启动器。
其他任何新claim在竞争等待后仍存在时均退让（包括更早claim）。未知归属n42
进程默认仍阻止取得机器；不自动豁免，也不终止。

入口10项轻量检查通过，包含真实短子进程、模拟时钟的三次检查与竞争等待、
取消后不启动和释放、同名碰撞、部分取得失败、替换文件保留及资源阈值；日志
`/tmp/n42-perf7-box-claim-tests.log`。后续 `watch-memory-io35.py` 增加独立进程
minor/major faults计数与PID/start_ticks检查；尚未用于新轮次。原第34轮采样
脚本和证据未改写，不能追补当时未观测的fault。

第34轮进一步的轻量日志分析：七节点各154条热点驻留记录均在2,621,440槽
上限内，sealed/active twig峰值1281。满块跟随者138条样本的root中位330.48ms、
exec692.45ms、写入1423.66ms、总导入2937.92ms；时间分解仍不是hash绑定的
正式A-B-A对比。证据分别为`retain34/retention-residency-summary.json`与
`retain34/block-phase-summary.json`。Rust一方已持有claim并运行其七节点时，
我方只运行轻量源码/日志分析与等待入口，第34轮独立审计仍处于排队状态。

后续确认PID2838448实际只有`n42 version`参数，为版本查询而非节点；占用入口
已将这一明确的轻量调用排除，其他未知n42调用仍阻止取得机器。旧等待入口已
显式终止并确认退出130，然后才修改入口；没有修改运行中的harness或guard。
新的等待命令包裹`build/perf7/audit34-check35.sh`：依次运行第34轮离线审计、
A13新回归和rawdb/internal/HotStuff/node完整race、独立二进制及全量build。
claim事件记录`/tmp/n42-perf7-audit34-check35-claim.jsonl`，工作尚未启动时不
取得claim。A13修复草稿及测试范围见三个月审计文档，目前不能计为已验证修复。

该队列于02:24:54 UTC取得claim，完成三次间隔30秒的安静检查后，又等待约
36秒确认无竞争claim，02:25:30启动；全部成功，02:30:36释放两处claim。
记录中途每60秒续期，退出码0，前台任务和进程组均已终止。未启动集群测量。

第34轮七节点独立审计全部通过：各验签6,216,600笔、核对1037个发送者nonce
和两百万收款人余额，双向history一致。七节点applied均154且hash相同；node4
尚未将该块规范化，其他六节点已规范，因此不宣称停止时七节点规范头完全一致。
测量窗口内的七节点规范hash/root检查仍通过。证据为
`retain34/offline-audit-summary.json`；不包括费用/奖励的独立重放或全EVM重执行。

A13针对性race、rawdb/internal/HotStuff/node完整race及节点/全量build均通过，
详见三个月审计A13。后续集群必须用已验证的新二进制检验启动/导入/规范提交；
缓存策略尚无正式A-B-A收益证明，新一轮测量不能直接当作达标证据。

### 共享占用交叉与第35轮排队

后续检查发现Rust的另一claim时间为02:27:29 UTC（文件内容和mtime一致），
早于我方02:30:36释放时间。文件时间只能证明claim时段交叉，不能倒推出其
集群进程实际何时启动；我方该时段运行的是正确性审计和构建，没有性能测量。
已向用户说明交叉并请求确认另一驱动的规则，未终止其他驱动进程。

占用入口补充运行期每5秒的claim检查：新竞争claim或自身claim缺失/替换时，
记录runtime_claim_conflict、停止自身前台进程组并将入口结果置为冲突（退出75），
即使内部harness退出0也不可作为隔离轮次。前台supervisor负责等待并清理setsid
节点。此检查观察claim，不能证明不存在未声明的短暂外部负载。

初次第35轮排队在02:44:00取得claim，Rust于02:44:02声明新claim；当时我方
采取更保守的“任何竞争claim均退让”，随机等待约37秒后释放，未启动节点。
现已按时间优先调整：遇到更早claim立即退让；较新/同时claim可额外等待最多
60秒让其完成竞争等待并撤回，仍未撤回则退让，始终不带冲突启动。
运行期冲突、旧claim优先、较新claim撤回等13项轻量检查通过。

第35轮是重建后的预热，排除正式性能对比。二进制
`n42-qmdb-commit-guard35`，account prefetch16、retention0，仍为两百万收款人；
固定harness与launcher已经准备。`run-commit35-managed.py`同时管理集群、
收据/CPU分区/内存和major faults观察器，所有自有工作退出后才归还claim。
当前排队记录为`/tmp/n42-perf7-commit35-warmup-priority-claim.jsonl`，尚未启动。

### 后续候选：与n42-rs对齐MDBX WRITEMAP（未运行）

固定Rust参考48263495的`crates/storage/db/src/implementation/mdbx/mod.rs`
在RW打开时明确调用write_map，默认sync_mode为Durable；Go
`lib/kv/mdbx/kv_mdbx_opts.go`默认为NoReadahead|Durable，现有launcher未设置
WRITE_MAP。Go的debug布尔开关只接受字面量`true`，**`WRITE_MAP=1`不会启用**。
后续应先运行`check-writemap36.sh`验证WRITE_MAP=true下的状态/MVCC/rollback
兼容性，再以同一二进制做预热+A-B-A，保持完整history与durable模式；尚无收益数据。
Rust同时设置coalesce等选项，本实验先只改变WRITEMAP，避免一次改动多个变量。

源码harness的环境记录已增加WRITE_MAP、NO_SYNC、N42_MDBX_SYNC、dirty limit和
readahead字段，21项Python harness检查通过；第35轮固定副本未更改。
`check-live-db-mode.py ROOT BINARY true|false`可核对七节点实际进程环境与
chaindata/mdbx.dat映射权限（libmdbx按WRITEMAP选择PROT_READ|PROT_WRITE），
避免仅凭启动命令断言开关生效。该采样脚本已通过语法检查，尚未用于活跃节点；
不是断电持久性测试。

第35轮后置脚本`audit-commit35.py`已准备并通过Python语法检查，尚未运行。
它要求预热supervisor成功且目标节点已停止，随后对七节点逐一执行签名/余额/
nonce审计和双向history审计，并核对node0测量交易组成；失败会写明失败状态，
不能以部分节点通过替代全七节点通过。该离线任务也必须取得机器claim后执行。

`run-writemap36-batch.sh`已准备但未执行：在同一个claim内先运行WRITEMAP
兼容性race，然后连续执行warmup、A1（关闭）、B（开启）、A2（关闭）。各轮
使用同一第35轮二进制、独立新数据库目录、相同两百万收款人负载和CPU分区。
节点/观察器全部退出后才能进入下一轮；中途发生claim冲突时整组不可作为
隔离对照。七节点数据库实际映射观察器加入每轮supervisor。

固定副本`harness-writemap36`的21项测试通过。启动前校验
`writemap36-orchestration-manifest.json`中的脚本和二进制哈希，并要求第35轮
七节点离线审计通过。所有轮次初始均标为待验证；预热永久排除，A/B结果仍需
整组隔离、窗口完整性及七节点离线状态审计通过。这只是WRITEMAP优化对照，
不替代最终三组Go/Rust配对达标验收。

### 第35轮预热及七节点离线审计完成

03:15:32 UTC取得两处claim，竞争等待后03:16:10启动，03:19:29七节点全部
正常停止并释放claim。入口结束码0、isolated=true；无运行期claim冲突记录。
三个30秒连续窗口42,205.86 / 43,465.88 / 43,465.48 TPS，24块、23满块，
合计3,874,200笔。七节点窗口末规范hash/root、9笔收据、CPU分区均通过。
实际MDBX进程环境和映射验证WRITEMAP=false、未放宽durability。
此轮是预热，**不计性能对齐或A-B-A收益**。

`commit35-warmup/window-fault-summary.json`将进程采样与测量monotonic边界
对齐。各窗口每节点major faults包围采样增量范围为292–519、926–3122、
52–85；最大边界余量1.471秒。相邻窗口包围样本会重叠，不能相加；并非精确
窗口计数或延迟因果证据。整个观察期每节点1038–3256 major faults，无观察到
process swap，宿主最低MemAvailable27.33GiB，写入23.53–25.28GiB/节点。
这些是进程观察期数值，包含启动和窗口后工作，不是设备写放大或Go堆指标。

满块高度筛选的138份follower样本中位数：exec705.39ms、prefetch66.53ms、
root383.74ms、write1400.73ms、total2954.46ms；写阶段commit中位546.43ms。
`block-phase-summary.json`仍只是高度筛选的时序证据，不能替代hash绑定或
隔离A-B-A。后置离线审计由`/tmp/n42-perf7-audit35-claim.jsonl`记录，重新完成
三次安静检查和claim竞争，03:22:48启动、03:26:09成功结束并释放claim，
isolated=true。各节点独立验签6,021,800笔、核对1004个发送者nonce和两百万
收款人余额，双向history全部通过；七节点applied均153且均已规范化，hash为
`0x574d2b35ebe1b641932956e817fa09713dde85d9e3b6f653b9485dd571872703`。
不包含费用/奖励的完整余额重放或全EVM独立重执行。

第36轮整组入口现已排队，记录`/tmp/n42-perf7-writemap36-batch-claim.jsonl`。
入口在所有四轮测量结束后才执行`audit-writemap36.py`核验各轮七个数据库，
全部审计进程退出后归还claim；不会在A1/B/A2之间插入离线审计。当前仅准备和
轻量语法/harness测试完成，WRITEMAP兼容性race及四轮集群实验尚未执行。

### 第36轮测量及四轮七节点离线审计完成

03:28:19 UTC取得claim，03:28:59启动整组任务。WRITEMAP=true、NO_SYNC=false、
N42_MDBX_SYNC=durable下的race检查全部通过：mdbx1.330s、qmdb190.354s、
rawdb1.756s、state131.136s、commitment51.152s、internal1.511s、
HotStuff10.993s、node2.375s。部分测试使用InMem夹具，这不是断电持久性证明。

预热三个窗口约42,099 / 43,466 / 43,466 TPS，保持排除。A1（关闭）约
43,466 / 43,466 / 38,032，B（开启）43,465 / 43,465 / 43,466，
A2（关闭）48,899 / 43,466 / 38,033。B与A2整轮均3,912,000笔，A1为
3,749,000笔；B首窗口未优于任一A，当前数据不支持稳定吞吐收益。
七节点实际进程映射和环境证实各轮开关状态；排除tag/node_root后，配置记录
只有WRITE_MAP不同。各轮运行监督、七节点窗口末规范hash/root、9笔收据、
CPU分区及内存观察通过，均正常停止，整组后置审计也已全部通过。

B的宿主最低MemAvailable72.01GiB，A1/A2为29.84/25.90GiB；每节点RSS峰值
范围分别为7.45–13.15GiB、12.44–18.57GiB、11.92–20.11GiB，非同一时点之和。
完整进程观察期major faults：B每节点9–14，A1为333–2094，A2为5200–11360；
测量窗口包围样本内A1和B均为0，A2仅第三窗1613–3126。完整观察期包括启动/
窗口后阶段，不能把其缺页计数套到首窗口，也不能把进程write_bytes差异解释
为设备物理写放大下降。三个轮次均未观察到process swap。

高度筛选满块follower的write中位数A1/B/A2为1325.78/1268.73/1337.38ms，
total为2921.67/2850.05/2910.51ms，写事务commit为531.21/530.93/518.66ms。
内存余量和少量阶段用时变化是后续调优线索，不替代A-B-A吞吐结论。

新增`compare-writemap36.py`只在整组claim成功且释放、四轮全部观察器及28个
数据库审计通过后汇总首窗口和整轮总量，并检查只有WRITEMAP这一项记录配置
不同。已验证未完成批次会被拒绝，不提前生成比较结果。当前正式Go/Rust三组
配对验收及每窗口300k TPS目标仍未达到。

整组于04:04:04 UTC成功结束并释放两处claim，isolated=true、退出0。28个
数据库的签名/账户和双向history审计通过，各轮node0测量负载组成均确认两百万
收款人。`writemap36-comparison.json`已生成：B首窗相对A1约0%、相对A2为
-11.11%，整轮总量相对A1为+4.35%、相对A2为0%；不支持稳定吞吐收益。

停止时的执行头并非都已规范化：warmup/A1/B的node2比其他节点多执行一个
候选块，分别为154/153/154；A2七节点applied均153且hash相同，但都尚未
规范化该块。审计按每节点实际applied祖先执行账本核验，不能把通过扩大为
停止时七节点规范头和执行头完全相同。测量窗口末七节点规范hash/root检查通过。

### 第37轮：原生MDBX提交耗时拆分及七节点审计完成

在第36轮测量全部结束、我方仍持有claim的离线审计阶段解析了已保存的15秒
node0 CPU profile。B轮75.80CPU秒中，入池prewarmSenders累计35.26秒，
transaction.Sender累计34.70秒，99.51%的Sender采样调用来自入池prewarm。
txLookup.Get累计约1.18秒。这些是窗口后CPU采样，不是区块关键路径的锁等待
时间，不足以把吞吐归因到查池锁或发送者恢复。输出位于
`/tmp/n42-perf7-writemap36-{a1,b,a2}-cpu-top.log`和B的sender-callers/cumulative日志。

Go所带mdbx-go v0.41.0的原生头文件明确说明COALESCE从libmdbx v0.12起始终
启用。因此Rust显式coalesce=true不构成当前Go尚缺的可切换优化，不能为这个
已默认生效的行为安排一次声称“启用coalesce”的收益测试。

下一项准备是默认关闭的`N42_MDBX_COMMIT_TRACE`：记录原生Commit返回的
preparation、GC wall/CPU、audit、write、sync、ending、whole耗时。ChainDB
RW提交成功后才记录；区块写日志在直接MDBX路径上附带相同事务ID及区块hash，
便于精确关联。没有更改提交顺序、写入内容或durability。原始计时分辨率为
1/65536秒，输出纳秒仅是单位；GC CPU计时可能因原生构建/平台不可用。
既有blockwrite的commit字段只是聚合时间，不能直接称为fsync耗时。

源码manifest为`build/perf7/commit37-source-manifest.json`。检查入口
`check-commit-trace37.sh`于04:09:48–04:10:27 UTC运行并成功释放claim：
启用trace和WRITEMAP的race通过（mdbx1.326s、internal1.498s、node2.379s），
新节点二进制`n42-commit-trace37`、全量build及21项harness检查通过。
验证后哈希见`commit37-validation-manifest.json`。

诊断整组于04:14:31取得claim，04:15:15启动，04:21:58成功结束并释放，
isolated=true。预热窗口约48,299 / 43,466 / 43,466 TPS，25块、24满块、
4,057,000笔；七节点窗口末规范hash/root、9笔收据、CPU分区、实际WRITEMAP
及观察器均通过，节点正常停止。预热数值不计作性能提升或达标证据。

`commit37-trace/native-commit-summary.json`通过每节点MDBX事务ID和精确规范
区块hash关联24个满块，得到24份leader和144份follower样本。follower原生
sync中位517.16ms，GC wall0.656ms，whole517.94ms，外层commit518.12ms；
leader对应sync168.91ms、GC wall0.664ms、whole169.64ms。当前主要提交等待
落在存储同步，未见GC搜索占据这半秒。WRITEMAP下write计时为0不表示没有
数据写入，映射页的写回可能计入msync/sync阶段。

七节点签名、账户及双向history审计通过，各核对两百万收款人。node0/3/4/5
applied153且已规范，各验签6,058,000笔、1011个发送者nonce；node1/2/6
applied154为相同的未规范候选块，各验签6,221,000笔、1039个nonce。不能将
此结果扩大为停止时七节点规范/执行头完全相同；不含费用/奖励独立重放和全EVM
重执行。证据为该轮`offline-audit-summary.json`。

后续候选是提前请求脏数据页写回，再保留原有MDBX最终完整同步，观察能否把
部分存储工作与写事务中的其余工作重叠。Linux的SYNC_FILE_RANGE_WRITE只是
发起写回，不能替代数据完整性同步，也不保证调用绝不阻塞；因此必须保留最终
同步、处理写回错误，并以整轮吞吐和回滚/重启测试判断，不能仅报告commit计时
下降。接口依据：[Linux man-pages](https://www.man7.org/linux/man-pages/man2/sync_file_range.2.html)。
该候选现已实现并通过下面的正确性检查；每窗口300k TPS及三组Go/Rust配对目标继续保留。


提前写回第38轮：默认关闭的`N42_MDBX_EARLY_WRITEBACK`仅在Linux、直接MDBX、
WRITEMAP且至少2048笔交易时使用；状态、history、QMDB数据阶段后各请求一次
SYNC_FILE_RANGE_WRITE，最多三次，最终MDBX Commit保持不变。ENOSYS/EOPNOTSUPP
回退普通提交，其他错误中止写事务。未添加后台线程或改变历史保留策略。

04:35:36取得检查claim，04:36:25启动，04:39:04完成并释放，isolated=true。
磁盘MDBX的快照隔离、回滚、重新打开、EIO/ENOSPC/EINTR回滚、能力不支持回退、
未提交页强制落盘后子进程直接退出恢复均通过；后者是进程退出测试，不是断电证明。
相关包race通过（mdbx1.388s、state131.958s、commitment50.540s、internal1.501s、
node2.371s；qmdb命中既有缓存），独立二进制、全量build及21项harness检查通过。
验证记录为`build/perf7/writeback38-validation-manifest.json`。

固定实验为预热、A1关闭、B开启、A2关闭，所有腿均使用同一n42-early-writeback38、
WRITEMAP=true、NO_SYNC=false、durable、prefetch16、retention0、两百万接收地址。
固定脚本及哈希在writeback38-orchestration-manifest.json；运行前重新遵循claim协议。
第38轮现已完成，结果如下；四轮测量结束后才运行28个数据库的离线审计。


第38轮整组04:42:11取得claim，04:42:51启动，05:11:34完成并释放，
isolated=true、returncode=0。独立比较器compare-writeback38.py确认完整单组
A-B-A有效，见数据根目录writeback38-comparison.json；它不是正式Go/Rust配对验收。
预热48,899 / 43,466 / 43,466 TPS剔除。A1与A2均约43,466 / 43,466 / 43,466，
各3,912,000笔；B约48,899 / 43,466 / 43,466，4,075,000笔。
B相对A1/A2的window1约+12.5007%/+12.4999%，整轮均+4.1667%，也就是多了
一个163,000笔满块。中位窗口TPS基本不变；只有一组结果，不能宣称稳定提升。

四轮七节点窗口末规范hash/root、9笔收据、CPU分区、实际MDBX进程配置、正常
停止及全部28库的签名/账户/双向history审计通过，每库核对两百万接收地址。
停止头明细：预热所有applied153同hash，node1尚未规范；A1所有applied153
且已规范同hash，各验签6,069,800笔、1013个nonce。B六节点applied153且规范，
各验签6,072,400笔、1014个nonce；node2 applied154为未规范候选，6,235,400笔、
1040个nonce。A2六节点applied153且规范，6,050,000笔、1009个nonce；node2
applied154为未规范候选，6,213,000笔、1037个nonce。不能扩大为每轮停止时
所有节点的规范/执行头完全一致；离线审计仍不含费用/奖励独立重放或全EVM重执行。
详细hash均保存在各轮offline-audit-summary.json及总比较文件。

通过每节点txid及精确规范区块hash关联，A1/B/A2 follower完整blockWrite中位
1258.48 / 1082.74 / 1287.44ms，外层commit535.99 / 12.57 / 567.59ms；
B提前写回调用本身338.84ms。leader完整write为1302.79 / 1322.55 / 1340.06ms，
B提前写回169.99ms，未见leader稳定改善。B的每节点每个满块实际三次写回请求
均已验证。原生B sync follower11.60ms、leader4.58ms，GC wall均约0.66ms。
收益评估必须包含提前写回调用时间，不能仅把commit的降幅报告为提速。

三条测量腿所有窗口的七进程major faults均0，无观察到的process swap。
window-fault-summary.json按前后样本包络计算，边界最大误差约1.46秒，不能
累加相邻窗的共享计数或据此作绝对因果判断。A1/B/A2主机最低MemAvailable约
71.76 / 72.19 / 71.74GiB，单节点峰值RSS范围分别7.60–13.20、7.53–13.02、
7.58–13.16GiB（范围并非同一时刻求和）。

下一轮39优先验证审计A14的错误传播与提交拒绝修复，使用独立新二进制，并以
预热集群进行集成检查。提前写回仍默认关闭；每窗300k TPS和三组Go/Rust的95%
目标均未达到，继续保留完整历史、最终同步和两百万接收地址条件。
