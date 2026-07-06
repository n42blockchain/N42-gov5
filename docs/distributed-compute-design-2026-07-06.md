# N42 分布式计算系统设计 — 调度/分发/验收/超时/容错 × 手机+IDC 异构算力 × 链上验证

日期:2026-07-06。标注:**[已落地]** = 仓库内已有;**[设计]** = 本文推演;
**[边界]** = 诚实的能力边界。姊妹篇:`distributed-storage-design-2026-07-06.md`
(数据就地计算与本文共用 CAS/市场/仲裁底座)。

---

## 0. 定位与家底

链提供**合约(托管/结算/仲裁)+ 验证(三级)**,算力来自两类截然不同的供给:
- **手机舰队**(CPU/GPU/NPU):量大、便宜、每瓦性能好(int8 NPU),但单点不可信、
  随时掉线、受电池/发热/系统后台限制约束
- **IDC 服务器**(GPU 集群):可靠、快,但贵且有中心化风险

设计原则:**手机吃并行可验证的小任务,IDC 吃重任务与协调;验证成本必须远小于计算成本;
手机永不直接发链上交易**(gas 与延迟都不允许)。

**可复用家底** [已落地]:
| 组件 | 位置 | 本设计中的角色 |
|---|---|---|
| 任务状态机(validTransition,原子转移防 TOCTOU) | coprocessor/task.go | 任务生命周期骨架 |
| 三级验证:ZK / 乐观+押金+挑战 / TEE | coprocessor/verification.go | 验收分层 |
| provider 注册(stake/能力/信誉)+ 反向拍卖 | coprocessor/{provider,marketplace}.go | 供给侧市场 |
| Verify-or-Slash | coprocessor/slashing.go | 经济惩罚 |
| WASM 引擎(fuel 计量、CAS host 函数、编译缓存) | compute/wasm | 确定性执行沙箱 |
| MapReduce(切分/调度/有序归约/panic 恢复) | compute/batch | 数据并行骨架 |
| opML 推理 + 欺诈证明 + 结果缓存 | compute/inference | AI 推理验收 |
| ZKML 证明/验证(电路生成、逐层 trace) | zkprover/zkml*, zkverifier | 高价值推理终局验证 |
| ZK 训练证明(治理门控、hash-chain、epoch trace) | ai/training | 训练可审计性 |
| 推理归因链(签名、多跳流水线、SafetyLevel) | ai/attestation | 结果溯源 |
| Agent 钱包(session key、限额、paymaster) | ai/wallet | 手机端免 gas 计费身份 |
| 任务协商(request→bid→accept→complete/dispute+托管) | ai/coord | 点对点任务协议 |
| 推理预编译 0x0301 | vm/contracts_ai_inference.go | 合约调用推理入口 |
| BLS 聚合(512 委员会,114B/块) | crypto/bls | 工作回执批量结算 |
| RLN 反垃圾 + 8-shard gossip + E2E | distributed/messaging | 任务分发/回执传输 |
| **evmsdk 移动 SDK(3 年生产,n42appv2 在用)** | cmd/evmsdk | 手机端运行时的落点 |
| 20 万手机委员会池(BLS 投票) | mainnet_qmdb 配置 | 手机舰队的组织先例 |

---

## 1. 架构:两级调度 [设计]

```
用户/合约 ──StoreTask tx(托管+需求描述)──▶ 链上市场(反向拍卖)[已落地]
                                            │ 粗粒度:任务→"调度域"
                    ┌───────────────────────┴───────────────────────┐
              IDC 调度域(有质押)                             IDC 直执行域
              │ 微调度器:把任务切片派给手机群                │ 大模型/ZK证明等重活
              ▼                                              ▼
        手机群(evmsdk,session key 身份)                GPU 集群
        NPU int8 推理 / proof 验证 / MapReduce 分片      训练 / LLM / SP1 证明
```

- **一级(链上)**:任务 → 调度域,拍卖按价格/ETA/信誉选择 [已落地];托管锁定
- **二级(链下)**:调度域(由质押 IDC 运营)把任务微分发到手机;手机的一切通信走
  messaging(RLN 限速防垃圾)[已落地];**微调度器本身是被验证对象**——它对分发/汇总
  结果签名担责,作弊被罚没
- 手机身份 = Agent 钱包 session key [已落地]:限额、限合约、可撤销;
  结算走 paymaster,手机零 gas

## 2. 任务生命周期与状态机 [设计,骨架已落地]

```
Submitted → Auctioned → Assigned → Executing → ResultSubmitted
   → Verifying(三级之一) → Accepted → Settled
                        ↘ Challenged → Arbitrated → Slashed/Accepted
   任何执行态 ──超时──▶ Reassigned(hedged)
```

- 复用 coprocessor 的 `validTransition` + 原子 `TransitionToProving/Challenged` [已落地]
- 任务清单(manifest)内容寻址存 CAS:代码(WASM 模块哈希)、输入 CID、fuel 上限、
  截止时间、冗余度 r、验证等级 —— **执行完全确定性**(WASM+fuel)[已落地],
  这是一切验收手段的前提

## 3. 调度与分发 [设计]

**设备画像**(注册时申报+运行时实测):算力档(NPU TOPS / GPU / CPU)、内存、电池/充电
状态、网络(WiFi/蜂窝)、历史在线率、信誉分 [注册表已落地,字段扩展]

**分派策略**:
| 任务类 | 目标设备 | 冗余 r | 理由 |
|---|---|---|---|
| int8 小模型推理(≤~3B 量化) | 手机 NPU | 3-5(quorum) | 单机不可信,靠多数裁决 |
| proof 验证(ZK verify、BLS verify) | 手机 CPU | 2-3 | 验证便宜,天然适合大群抽查 |
| MapReduce 分片(数据就地) | 手机+边缘 | 2 | 分片失败重派即可 [批处理骨架已落地] |
| LLM 推理 / 训练 / SP1 证明生成 | IDC GPU | 1 + 重验证 | 复制太贵,靠押金+挑战/TEE/ZK |

**手机侧现实约束** [边界]:
- iOS/Android 后台执行限制 → 任务须支持"充电+WiFi 时机窗口"调度(BOINC 模式);
  evmsdk 三年生产经验就是这个约束下攒出来的,复用其唤醒/心跳模式
- 上行带宽窄 → 任务输入走 CDN/就近 CAS 拉取(存储篇 §6),结果只传摘要+按需取回
- NAT → libp2p relay [已落地]

## 4. 验收(核心)[设计,三级机制已落地]

**原则:验证成本 << 计算成本,且与设备信任级匹配。**

| 等级 | 机制 | 适用 | 成本 |
|---|---|---|---|
| L0 quorum | r 份独立执行,多数一致即验收;WASM 确定性保证可比对 | 手机群小任务 | r× 计算(小任务可承受) |
| L1 乐观 | 单执行+押金+挑战窗口;挑战者重放 WASM/opML 欺诈证明定位分歧步 [已落地] | IDC 常规 | ~0(无争议时) |
| L2 TEE | 远程证明 + 结果签名 [已落地] | 低延迟高价值 | 硬件信任假设 |
| L3 ZK | ZKML/SP1 证明,链上验证 [已落地] | 终局仲裁/高价值小模型 | 证明生成贵(见边界) |

- **争议路径**:L0 分歧 → 升 L1(调度域重放);L1 挑战 → opML 二分定位到单算子 →
  链上仲裁 [欺诈证明机制已落地];终局 → L3
- **抗女巫**(手机 quorum 的命门):①同一 quorum 的 r 台设备按信誉/ASN/地理强制分散
  ②设备证明(Play Integrity / App Attest)作为注册门槛 ③RLN 限制单身份任务速率
  [已落地] ④结果一致性异常模式检测(全对但总同组 = 共谋信号,降信誉拆组)
- **结果投毒**:输入输出全 CAS 寻址,验收前结果不进缓存;推理缓存只收已验收结果
  [已落地,ResultCache]

## 5. 超时与容错 [设计]

**超时层级**(嵌套,内层远小于外层):
```
心跳 lease(30s-2min,手机) < 任务截止(分钟-小时) < 挑战窗口(小时-天) < 托管过期
```
- 手机任务:lease 断 → 立即重派(不罚,手机掉线是常态,只记在线率);
  **hedged dispatch**:截止前 20% 仍未归 → 并行加派一份,先到先用(尾延迟保险)
- IDC 任务:超时 = 违约 → 押金部分罚没 + 重拍卖
- 长任务(训练)**强制分段检查点**:epoch 边界提交 hash-chain trace [已落地,
  training/EpochTrace] → 失败从最近检查点续,而不是重头;检查点本身可被抽查验证
- MapReduce:失败分片重派,归约端有序+panic 恢复 [已落地]
- 结算:每 epoch 调度域把已验收工作回执 **BLS 聚合**(114B 模式)批量上链,
  手机按 session key 分账 —— 百万级小任务不产生百万级 tx

## 6. 工作负载专章

### 6.1 AI 推理 [多数已落地]
- 入口:合约调 0x0301 预编译 → 请求进市场 → 按模型大小路由(手机 quorum / IDC 乐观)
- 已有:模型注册、opML 欺诈证明、结果缓存、归因链(attestation,多跳流水线
  output[i]==input[i+1],SafetyLevel 分级:Critical 要求已验证训练记录)
- 手机 NPU 的现实甜点 [边界]:int8 量化 ≤3B 参数级;更大的模型分层切分到多机的
  通信开销在蜂窝网络下不成立,不要尝试

### 6.2 训练 [部分已落地,边界要诚实]
- IDC 集中训练 + **ZK 训练可审计性**:治理门控(数据集须过伦理委员会 [已落地])+
  hash-chain epoch trace + 结构校验 [已落地] —— 这是"可审计+防篡改",
  **不是**对每个梯度的完整 ZK 证明
- [边界] 大模型全量 ZK 训练证明当前不可行(业界同此);现实验收 = 检查点抽查重放
  (随机抽 epoch,quorum 重算该段)+ 最终模型在保留集上的性能验收
- 手机侧训练 = 联邦聚合场景(小模型/个性化),隐私红利,但拜占庭鲁棒聚合
  (median/Krum 类)必须在聚合器实现,否则投毒防不住

### 6.3 ZK 证明 [已落地组件,分工是新设计]
- **反直觉分工:IDC 生成证明,手机群验证证明** —— 验证便宜(ms 级),
  正是大规模低信任设备的完美负载;证明生成(SP1/STARK GPU)留在 IDC
- zkguest(RISC-V64)是通用 ZK 任务的载体 [已落地];电路/后端三选一 [已落地]
- 手机验证群 = 给全网所有 L3 验收提供去中心化的"最后一双眼睛",
  与 20 万 BLS 投票池 [已落地] 是同一种组织形态,可共用委员会抽样逻辑

## 7. 经济与安全闭环 [设计]

- 定价:反向拍卖 [已落地];手机侧按"任务类 × 设备档"出厂价目,微调度器竞价打包
- 质押分层:IDC 重押金(乐观模式的安全来源);手机零质押(靠 quorum+信誉+设备证明),
  信誉 = 完成率 40%/争议 30%/响应 20%/质押 10% 加权衰减 [已落地]
- 罚没流向:失职押金 → 重派成本 + 挑战者赏金 [已落地,Verify-or-Slash]
- 支付:用户托管 → 验收后流式解锁;手机端 session key + paymaster 全程免 gas [已落地]

## 8. 参考系(简) [公开资料]

| 项目 | 可借鉴 | 我们的不同 |
|---|---|---|
| BOINC | 手机/志愿设备的时机窗口调度、quorum 验收(20 年经验) | 加上经济层与链上仲裁 |
| Akash/Render/io.net | GPU 市场撮合 | 我们有原生三级验证而非纯信誉 |
| Gensyn | 训练验证的概率抽查思路 | 我们用检查点 hash-chain [已落地] + 抽查重放 |
| Bittensor | 子网激励设计的教训(评分博弈) | 验收基于确定性重放/证明,不靠主观评分 |
| EigenLayer AVS | 再质押安全共享 | coprocessor 本就按 AVS 模型建 [已落地] |

## 9. MVP 切片与下一步

1. **切片一(全链路最短闭环)— ✅ 已落地(2026-07-06)**:
   `internal/distributed/compute/scheduler` 新包,把 marketplace→执行→验收→结算串成
   真实任务流。实现范围:两种验收模式(乐观拍卖单执行+挑战窗口+referee 重放+罚没;
   quorum r 副本严格多数+分散约束+一次升级轮)、嵌套超时(lease<deadline)、重派、
   hedged dispatch、托管账本(锁定/支付/退款守恒)、信誉/罚没回写。
   **验收测试先行,13 个用例全绿**(scheduler_test.go:乐观 happy path、挑战
   upheld/rejected、quorum 多数/无多数升级、lease 超时重派、hedge 对冲、截止过期、
   分散约束失败即失败、托管守恒、worker 崩溃重派、并发压测、参数校验),
   `-race -count=3` 干净。顺带修复 coprocessor 包两个**预存数据竞争**
   (SlashManager.Slash 无锁读 Stake、marketplace 经活指针读 Reputation;
   新增 `ProviderRegistry.GetSnapshot` 快照读)。
   `LocalWASMExecutor` 已接 wasm.Engine(IDC 单机/referee 路径);远程 executor
   (messaging 分发)留给切片二。
2. **切片二(设备侧运行时 + 远程分发)— ✅ 核心已落地(2026-07-06)**:
   `internal/distributed/compute/worker` 新包:`Transport` 抽象(内存 `LocalBus` 已实现,
   messaging 生产适配器留待接线)、版本化 wire 协议(签名盖在规范化 keccak 摘要上而非
   JSON 字节,编码可换不破签)、`Worker`(**可用性窗口门控**——手机"充电+WiFi"策略的
   插槽;typed 拒绝让调度器立刻重派而不烧完 lease;结果/拒绝一律 secp256k1 签名,
   摘要绑定 reqID+worker+输出,不可伪造不可移植)、`RemoteExecutor`(实现
   scheduler.Executor,验签+身份匹配+ctx 超时)。**8 个验收测试全绿**:签名往返/
   伪造签名拒收/可用性门控/离线静默超时/迟到响应丢弃不串台/切片一调度器全管线过总线
   (quorum 3副本投毒者被拒)/并发请求隔离/spec 线上保真。`-race -count=3` 干净。
2b. **messaging Transport 适配器 — ✅ 已落地(2026-07-06)**:`MessagingTransport`
   把地址路由的 `worker.Transport` 映射到 topic 化 messaging 服务(每个端点地址一个
   topic,Register 订阅、Send 发布到目标 topic);进程内走 `Service.Publish` 本地派发,
   跨节点走 relay + `Service.DeliverFromRelay` 派发到同一批 handler。5 个集成测试
   (真实 messaging.Service,含双实例跨节点 relay 桥接)。**关键结论:无需给 compute
   加 P2P topic 或改 pubsub_filter** —— relay 把任意 Service topic 字符串当作
   Envelope 内部字段(`env.Topic`),再哈希分片到已有的 8 个 `/n42/msg/shard/N`
   gossip topic 传输(`topicShard` 对任意字符串哈希);P2P 层只见 `/n42/msg/` 前缀
   (CanSubscribe 早已放行),compute topic 只是 envelope 里的字符串,接收端
   DecodeEnvelope 还原后按原 topic 本地派发。故适配器对真实 P2P relay **已生产就绪**,
   `relayBridge` 跨节点测试验证的正是这条路径。
3. **切片三(AI)— ✅ 已落地(2026-07-06)**:`SchedulerInferenceExecutor` 实现
   `inference.InferenceExecutor`,把每次推理调度成 scheduler 验证任务(quorum/乐观)
   而非单机可信 WASM 执行;模型 WASM 随 TaskSpec 下发,任何 worker 可执行。
   precompile 0x0301 → InferenceService → 此执行器 → scheduler → workers 在执行器
   接缝以上一行未动。5 测试:单元(fake scheduler 的 spec 构建+verified/failed/expired/
   unknown-model 映射)+ 全栈(真 scheduler+3 签名 worker 过总线,拜占庭 worker 被
   honest quorum 否决,经真实 InferenceService 驱动如 precompile)。
4. **剩余(依赖外部工程,非本仓库核心)**:evmsdk 手机端 gomobile 集成(独立代码库)、
   session key 支付结算实际接线(`ai/wallet.ValidateSessionKey` 已有,等支付路径)、
   ZK/训练任务类型的 Runner 实现、opML 挑战路径接 `zkprover`/`compute/inference`
   欺诈证明。**传输层跨节点已完整,不欠 P2P topic 注册项。**
5. **待测数字**:手机 NPU int8 吞吐/瓦、quorum r 的正确率-成本曲线、opML 挑战真机耗时、
   BLS 聚合回执的结算吞吐上限
