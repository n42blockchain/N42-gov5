# 近一个月代码审计修复（2026-10-14）

统一报告：[三工程审计、提交与测试清单](https://github.com/n42blockchain/n42-26/blob/main/docs/monthly-code-audit-20261014.md)。历史范围为 2026-09-14 至 2026-10-14，冻结提交 `44592a0536d849c06392437e019bfcc9077c8207`；也验证了当前脏工作区的代码快照。

## 可发布修复分支

`fix/monthly-audit-20261014` 从冻结提交建立，在隔离 worktree 中逐段修复、测试、提交并推送。

| 提交 | 内容 |
| --- | --- |
| `71444d7c` | 发布 anchor 工具缺少的独立投票存储 marker 读取函数；损坏 marker 仍视为存在，保持 fail-closed |
| `3d0c64e5` | 导出路径不得在源库内或互相覆盖；处理 symlink 父路径，改用随机临时文件 |
| `a2631cd7` | 空 key 与游标错误同时出现时不得当作 EOF；失败导出清理临时文件 |
| `458e85f0` | 审查、采纳并发布现有工作区的 QMDB checked-read helper 与测试 |
| `2ca87d83` | 审查、采纳并发布现有工作区的 live entry/index/frozen leaf 认证与测试 |
| `792c495b` | 审查、采纳并发布现有 exporter 链 ID/高度、plain state/code 认证和完整覆盖检查；补失败导出不残留临时文件回归 |

后三项来自本次开始时已存在的未发布工作，保留了来源说明，没有将其他会话的全部改动带入。

## 测试与原工作区

- 原始 clean-base 修复：五个工具相关包通过。
- 当前工作区的隔离快照：state-dump、consensus-anchor、txflood、QMDB、state、commitment、hotstuff、internal、ingest 九个包普通测试通过；QMDB、commitment、hotstuff 三个包竞态检测通过。
- 最终可发布分支：state-dump、consensus-anchor、QMDB、commitment 四个包普通测试通过；QMDB、state-dump 竞态检测通过。
- 测试覆盖损坏/缺失 entry、冷读取错误、错链 ID、遗漏 plain account、游标错误、不发布无效快照，以及健康源库和缺少 applied marker 的 replay v2 源库。

导出路径和临时文件清理修复也应用到了原工作区；已有游标/认证实现保持原样。
原 `qs/block-time-budget` 分支及其他会话的脏改动没有 reset、批量 merge 或批量提交。
该分支用于交付审计后的代码；并不声称整个 Gov5 workspace 的所有包都完成了测试，也不将历史七节点运行结果改写为该新源码的长期验收。
