# Codex 任务:自研链生产化 P0/P1 工程项

> 这份文档是给 **OpenAI Codex** 的任务说明,自包含。总差距评估见同目录
> `maturity-gap-consensus-2026-07.md`;本文只收录**可代码化**的任务
> (P0-2 投票门活性 soak 与 P0-4 断层源清零观察需要真机 7 节点长跑,不在此列,留本机执行)。
>
> 仓库:`github.com/n42blockchain/N42`,分支基线 `feat/eth-el-snapshot-direct`(9a81f053+)。
> 构建:`go build -tags nosqlite,noboltdb ./...`(CGO 必须开启,MDBX 依赖)。
> 测试:`go test ./internal/ ./internal/sync/... -count=1`;
> `internal/consensus/hotstuff` 包有 **10 个预存红测试**(见 Task D),在 Task A/B 中
> 以"不新增失败"为准。
>
> **提交规范**:git 提交**不要包含 "Claude" / "Codex" / "Co-Authored-By" 等 AI 署名**;
> 一个任务一个 commit,禁止混合无关变更。

---

## Task A — 坏分支缓存接入 catch-up / fetch-on-miss(P0-1)

**背景。** 2026-07-11 第 37 轮实弹:一个节点持有已被判 BAD 的分叉
(块 13013193 携带 stale-nonce 交易),其余 6 台的 hotstuff catch-up 每 8s 重复
"unwind 自己的应用头 → 导入坏分叉前缀 → 在 BAD 块上执行失败 → 下一轮从头再来",
无限循环,每轮浪费一次 unwind+re-execute 且抖动 applied head
(日志形态:`hotstuff catch-up: insert failed … nonce too low … imported=2` 每 8s 一条)。

`internal/sync/service.go:96-97` 已有 `badBlockCache`(LRU 1000,`setBadBlock`/`hasBadBlock`,
gossip 校验路径 `validate_blocks.go:81-82` 在用,含 parent→child 传染),
但 **catch-up(`rpc_catchup.go`)、fetch-on-miss(`rpc_block_by_hash.go` alignAndImport)、
direct-push(`rpc_block_push.go`)三条路径既不查也不设它** —— 这就是死循环的缺口。

**期望改动。**
1. catch-up range insert 失败时(`rpc_catchup.go:169-188`),若错误是**执行级共识错误**
   (nonce/balance/gas/root mismatch 类,即 BAD BLOCK),对失败的那个块 `setBadBlock`;
   下一轮 fetch 到的 range 里凡 `hasBadBlock`(或其祖先 BAD,传染逻辑复用
   `validate_blocks.go`)的块直接剔除,不再进入 insert。
2. `alignAndImport` 与 `blockPushStreamHandler` 入口:`hasBadBlock(blk.Hash())` 直接丢弃。
3. **红线(最重要)**:以下错误**绝不能**标 BAD——它们是本地暂态,标了会永久拒真块:
   - `unknown ancestor` / `pruned ancestor`(`isAncestorError`,subscriber_blocks.go:87);
   - `errRevertUnavailable`("branch-switch revert unavailable",internal/blockchain_types.go:71);
   - `errQMDBBehindParent` 及任何 future-queue 情形(insert 返回 nil 的都不是错误);
   - "sibling parent not applied"(unwindForReimportTx 的预检拒绝)。
   判定建议:只有 insertChain 返回的错误链中含共识执行失败特征
   (`could not apply tx` / `invalid gas used` / `state root mismatch` /
   ValidateState 错误类型)才标 BAD。为此在 internal 侧给这类错误加一个可
   `errors.Is`/`errors.As` 的哨兵或类型(如 `ErrExecutionInvalid` wrap),
   比字符串匹配可靠——subscriber_blocks.go 现有的字符串匹配可一并迁移。
4. peer 降权(可选加分项):向我们 serve 过 BAD 块的 peer 调 scorer 减分
   (`internal/p2p` 的 peer scoring 入口),幅度温和,不 ban。

**验收。**
- 新单测(internal/sync):构造 insert 返回执行级错误 → 断言该块进 badBlockCache,
  第二次 catch-up 轮次它被剔除、insert 不再被调用;
- 反向单测:insert 返回 unknown-ancestor/errRevertUnavailable → 断言**没有**进缓存;
- `go test ./internal/ ./internal/sync/... -count=1` 全绿。

## Task B — commit 处理的可执行性防线(P0-3,与 Rust 侧 Task 1 同一命题)

**背景。** `ef5781d5` 已把投票门收紧到 applied lineage(块没执行不投票),坏块理论上
拿不到 QC 了。但纵深不足:若一个 CommitQC 仍然到达(多数节点被同一 bug 骗过、或未来
回归),`OutputBlockCommitted` 处理(`internal/consensus/hotstuff/service.go:296-315`)
会 `CommitToCanonical`,失败仅 Debug 级 `pendingCommit` 挂起重试——没有任何一处
在"本地从未执行过该块"时发出高可见信号或拒绝在其上建块。

**期望改动。**
1. `OutputBlockCommitted` 处理中,用 `BlockApplied`(`s.blockFetcher` 断言,
   ef5781d5 引入的探针,internal/sync/rpc_block_by_hash.go:106)检查 committed 块:
   - 未 applied → `log.Error`("committed block not executed locally")+ 新 Prometheus
     counter `hotstuff_committed_unexecuted_total`(metrics 注册处:
     internal/consensus/hotstuff 的 metrics 文件);照旧走 pendingCommit 重试
     (它可能只是还没到/没轮到执行——这不是拒绝,是高可见告警);
   - **连续 N 次(建议 3)对同一 hash 重试仍未 applied** → leader 侧
     `TriggerBlockProduction` 的 sync-gate(service.go:81 附近 `HeightBehind` 逻辑)
     应拒绝以该块为父出块,宁可 view 超时。
2. 保持 CommitToCanonical 语义不变(canonical 推进只需块体,这是既定设计)。

**验收。** 单测:mock BlockFetcher 的 BlockApplied=false → 断言 error 日志路径与
counter 递增、且触发 build 的 parentHash 不等于该块;BlockApplied=true 行为不变。
hotstuff 包不新增红测试。

## Task C — 优雅停产品化:`n42 stop`(P1-6)

**背景。** Windows 下 bash kill = 硬杀,截断 MDBX spill 曾造成多日构建损失。
验证过的正确姿势:P/Invoke `AttachConsole(pid)` + `SetConsoleCtrlHandler(NULL,TRUE)` +
`GenerateConsoleCtrlEvent(CTRL_BREAK_EVENT=1, 0)`(每次一个 PID)。目前靠运维手搓
PowerShell,应产品化。

**期望改动。** `cmd/n42` 新增 `stop` 子命令:
1. 节点启动时在 datadir 写 `n42.pid`(退出时删除;启动发现残留 pid 且进程存活则拒绝
   双开——顺手修双开保护);
2. `n42 stop --data.dir <dir>`(或 `--pid N`):Windows 用上述 console API 发
   CTRL_BREAK,等待进程退出(默认 120s 超时,`--timeout` 可调),超时提示但**绝不**
   自动升级为 TerminateProcess;非 Windows 发 SIGINT。
3. Windows console API 走 `golang.org/x/sys/windows`(kernel32 proc),放
   `cmd/n42/stop_windows.go` + `stop_unix.go` 构建标签分平台。

**验收。** Windows 真机:`n42 stop` 后节点日志尾部出现 "All services stopped";
pid 文件生命周期正确;单测覆盖 pid 文件读写与双开拒绝。

## Task D — hotstuff 包 10 个预存红测试(P1-7,独立提交)

**背景。** `go test ./internal/consensus/hotstuff/ -count=1` 现有 10 个失败
(TestPrepareEmbedsLastCommittedQCInExtra、TestByzantine_DoubleVoteSuppressed、
TestByzantine_NetworkPartition、TestChaos7Node_*×6、TestFollowerReceivesProposal),
是共识实弹演进期间与实现脱节的 WIP 测试,长期红使该包失去回归保护——
Task A/B 的"不新增失败"都只能靠肉眼比对失败清单。

**期望改动。** 逐个诊断:测试断言过时(实现已按实弹修正)→ 更新断言使其锚定当前
正确行为;测试暴露真 bug → **停下,不要改实现**,在测试旁注释记录发现并在 PR 描述
里单独列出,交人裁决。每个测试一个小 commit,提交信息说明它锚定的行为。

**验收。** `go test ./internal/consensus/hotstuff/ -count=1` 全绿,或余下失败均附
"疑似真 bug,待裁决"清单。

---

## 优先顺序与依赖

A(独立)→ B(依赖 ef5781d5 已在基线)→ C(独立)→ D(建议最先或最后整批做,
避免与 A/B 的失败清单混淆)。全部完成后,P0 剩余两项(soak 观察)由本机执行。
