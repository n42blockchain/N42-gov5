# QMDB / Ed25519 native 七节点实测补充

2026-10-07 已在相邻 `n42-rs` 仓库完成源代码核对、修复、预热和正式测量。
此前只检查旧 Go qs / n42-26 路径，遗漏了 Rust 的 `0x50` Ed25519 性能实现；本记录更正该范围判断。

## 正式结果

- 七个独立执行层、七个验证器；QMDB 二叉树状态、native 帧树及描述块/紧凑块传输。
- 高性能交易为 `0x50` Ed25519；普通资金准备交易为 ECDSA，共识 BLS 是另一层机制。
- 预先生成 1,600 万笔签名交易、32 个文件（2.59 GB），测量期间回放。
- 20 秒窗口确认 **8,120,696 笔，406,035 TPS**；54 个不同高度，无重复高度计数。
- 完整正式轮回放及排空确认 9,361,408 笔 Ed25519 转账。
- 七节点 hash/stateRoot/receiptsRoot/transactionsRoot 一致，后续三秒七节点均继续出块。
- 七节点实际均使用 `N42_INGEST_VERIFY=all`；无网关跳验、无分片跳验。
  正式回放进度 `sign 0s`、`rejected 0`；节点入口验签计数大于 933 万。
- 抽样七节点收款余额均为 6 wei，`0x50` 回执成功、gasUsed 21,000；
  篡改签名经 RPC 得到 `-32602: invalid transaction signature`。

普通转账使用 EVM 语义的 native 仿真快路径，非适用交易回退解释器。
没有以 MPT proof/trie 的测试代替该性能路径的验证。

## 修复及验证

Rust 审计分支 `audit/fleet7-verify-20261007` 分段提交修复帧布局长度溢出，
二进制新旧检查，所有发送账户 nonce 检查及 RPC 失败时拒绝回放，
Reth state-masking 参数兼容，进程/RPC 就绪判断、基础费读取失败，
初始领导节点启动顺序，以及帧布局的 native 传输配置保护。

105 项 Rust 定向测试及 6 项脚本回归通过；release 构建通过。
失败启动、停链及后续活性检查 FAIL 的窗口均作废。
有效预热（10 秒、432,046 TPS）与正式测量分别保存，不将预热数当作正式成绩。

完整源码修复、配置与证据见：

- [Rust 实测报告](https://github.com/n42blockchain/N42-rs/blob/338666321/docs/FLEET7_VERIFIED_AUDIT_20261007.md)
- [机器可读证据](https://github.com/n42blockchain/N42-rs/blob/338666321/docs/evidence/fleet7-verified-20261007.json)
- 本机完整日志、预签名文件、运行脚本与独占记录：`/data/blockchain/fleet7-audit-20261007/`

此结果属于 Rust native 路径，不代表 Go Gov5 已实现 `0x50` 交易支持或达到相同吞吐。
这是单机单个 20 秒正式窗口，不是持续容量、跨机器网络或拜占庭故障验收。
Go 最近三个月代码审计的修复和验证继续以
[原审计报告](THREE_MONTH_AUDIT_20261006.md) 为准。
运行结束已停止本轮进程并释放独占 claim；原 Go 工作区及原 Rust 工作区的未提交修改未被覆盖。
