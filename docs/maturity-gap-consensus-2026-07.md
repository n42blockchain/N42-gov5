# N42 自研链(HotStuff-2 + QMDB)距成熟产品的差距评估

> 日期:2026-07-11(qmdb_staggered 战役第 38 轮收口时点)
> 范围:自研链共识/状态/运维生产化成熟度。全局功能矩阵(vs geth/reth/Erigon/Monad/Aptos)见 `GAP_ANALYSIS.md`;Rust 侧(n42-26)共识对照见 `n42-26-h2-consensus-audit.md`。
> 结论先行:**协议层(view/QC/TC/commit)从未出过错;全部欠账在共识↔执行↔状态的耦合层和运维层**。防线体系已从"静默腐化"升级为"显式拒绝+自动自愈",但离"无人值守数月"还有明确可列举的距离。

---

## 一、已达成的底线(本战役战果)

这些是成熟产品的**前提**,2026-07 之前并不具备,现在具备:

| 防线 | 内容 | 落地 |
|------|------|------|
| 逐块 state root 校验 | build/verify 分裂,root 不符块直接拒绝(年初"稳定数周"=不验根的假稳定) | 75351eaa |
| 失败执行揭除 | 失败/丢弃候选的树追加原子揭除(TakeUndo/peel),不再毒化活树 | 19ebbf70 等 |
| canonical 单一权威 | 只有 2-chain commit 写 canonical,import/miner 无权 | hotstuff-canonical 战役 |
| 执行前不变量 | 每块执行前断言活树根==parent header root,断层第一现场报警 | bd733cdd |
| 三态原子自愈 | 断层恢复时树/PlainState/marker 同事务回卷(changeset 头=Plain 真实高度);启动半愈态检测 | 09e6e1be |
| 投票证据分级 | import-gated vote 只认 applied lineage,块体存在/insert 无错不再算"已导入" | ef5781d5 |
| unwind 前置 peel | branch switch 揭树前先揭 dangling 候选,stale undo 回卷后作废 | 42f7614a |
| 零腐化传播 | 以上叠加的效果:坏块最多让单节点卡滞,**不再能获得 QC、不再能传播、不再能腐化盘面** | 第 37 轮实弹验证 |

**实测基线**(第 38 轮,qs12,7 validator + 512 BLS committee 仿真):空载 ~3.75s/块;txgen 43-111 tx/块持续上链;node3/node4 两类坏库手术后全网 7/7 高度与哈希逐块一致。

## 二、距成熟产品的差距(按优先级)

### P0 — 不解决就不能宣称生产可用

1. **坏块/坏分支缓存缺失**。第 37 轮实测:catch-up 每 8s 反复拉同一条已判 BAD 的分叉(unwind 自己的 13013192 → 导入坏 13013193 → nonce 拒绝 → 循环),浪费执行且抖动 applied head。geth 有 badBlocks 缓存;我们需要:BAD 判定后按 (hash) 记忆,catch-up/future-queue/fetch-on-miss 拒绝再导入,并把持有坏分支的 peer 降权。
2. **投票门活性长测**。ef5781d5 把投票证据收紧到 applied lineage,乱序场景依赖 fetch-on-miss 每-proposal 重试兜底(最坏损失一个 view 超时)。需要 24h+ soak 验证 view 超时率没有回升;若回升,补 future-block 转正后的主动通知。
3. **committed-on-inexecutable 的协议级免疫**。本次靠 qs-hsreset 人工手术(全网停机清 HotStuffState)。投票门修复后理论上不再发生,但**没有第二道闸**:commit 处理时若本地无法验证块可执行性,应拒绝推进 committed 标记(与 Rust 侧 audit Task 1 同一命题,两边都要修)。
4. **断层新生源清零**。stale-undo 组合路径已修(42f7614a)但只经过单测,未经受长时间实弹;第一现场 Warn 已埋,需持续跑到"连续 N 天零 discontinuity Warn"才算关账。

### P1 — 影响无人值守运维

5. **hotstuff 遗留专项**(hotstuff-canonical 战役移交):tx topic 订阅未激活(依赖 direct-push 兜底)、leader build 超窗 drop(浪费轮次)、限流计分风暴与重启 proposal 恢复(L6 移交)。
6. **运维工具链**:优雅停靠手搓 CTRL_BREAK(应产品化为 `n42 stop`/服务化);无健康监控/告警(断层 Warn、view 超时率、高度发散应有 exporter+告警);库手术(克隆/hsreset)应有 runbook 文档化。
7. **测试成熟度**:internal/consensus/hotstuff 有 10 个预存红测试(WIP);无 CI 级多节点 soak(现在全靠真机 7 节点人肉);混沌测试(TestChaos7Node_*)与实现脱节。
8. **观察者/轻节点路径**:非 validator 节点跟随 committed 链的路径(catch-up)在坏分支场景的行为未系统测试。

### P2 — 性能与规模

9. **出块延迟**:3.75s/块(view timeout 驱动)vs Aptos Baby Raptr 亚 50ms 级。收敛路径:降低 view timeout+pipelined 提案(协议层支持,工程未做)。
10. **单 leader 吞吐**:828-111 tx/块受 build 窗口限制;QMDB 执行层 2.26M upd/s 的能力远未被共识层喂饱。
11. **验证者规模**:7 validator 实测;512 committee 是仿真投票;200k 池轮转、真机手机投票(evmsdk 链路)未在新共识栈上端到端验证。
12. **长历史运维**:重放/追平工具齐备,但节点连续运行数月的库膨胀、undo 窗口裁剪、changeset 增长没有实测数据。

## 三、量化一句话

**协议核心是健全的;从"实验室 7 节点能自愈"到"生产可用",剩余工作 ≈ 4 个 P0 专项(每个 0.5-1 session 代码 + 数天 soak)+ P1 的运维工具化(1-2 周)。** 性能(P2)是其后的独立战役。

## 四、交叉引用

- 全局功能矩阵与竞品对照:`GAP_ANALYSIS.md`(2026-06-12 修订版)
- Rust 侧同类问题与可借鉴设计:`n42-26-h2-consensus-audit.md`(2026-07-11)——注意其 Task 1(乐观投票无执行下限)与本文 P0-3 是同一命题的两侧实现
- 7 节点部署与手术记录:`mainnet_qmdb_staggered-7node-status.md`
