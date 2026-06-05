# go-ethereum 近 3 月更新 → N42 借鉴审计(2026-06-05)

审 `../geth`(本地 fork,`origin`=ethereum/go-ethereum,已 fetch 到上游最新;本地 master 落后 origin/master 4 commit,审 `origin/master`)。3 月 286 commits。N42 是 **geth 衍生**(txpool/vm/state/types 移植),但已显著分叉(state 重写为 buffered_plain_state、txpool 为 txspool 自有结构),故行级借鉴有限。方法同 reth 审计:读 geth 实际 diff + grep N42 + 判真bug/已正确/不适用。

## 已借鉴落地(1 个真 gap)
- **SetCode tx 池准入结构校验**(geth #35094,commit `fix(txpool): reject structurally-invalid SetCode txs`):N42 engine/payload 校验已拒 nil-To/空-authlist 的 SetCode(`engine_payload_validation.go:314-318`),但 **txpool validateTx 不拒** → 无效 SetCode tx 可占池槽 + 被传播,仅在包含时才拒。补 `validateSetCodeStructure`(Prague-gate + To 非 nil + authlist 非空),从 validateTx 调。4 分支单测。**与本会话 EIP-7702 authority 限制(#17)同区,自然延伸。**

## 核查后 N42 已正确 / 不适用(不借)
- **StateDB reader error commit 后吞掉**(geth #34899 `s.reader,_=` → `err`):N42 用 StateReader 接口 + 错误传播,**无惰性创建 reader 吞 err 的模式** → 架构不同,N/A。
- **blob versioned-hash 不能为 0**(geth #35065 JSON unmarshal 拒空):N42 **已校验**(`engine_payload_validation.go:301 len(tx.BlobHashes())==0`)→ N/A。
- **txpool/locals 数据竞争**(geth #35096):geth 新 `tx_tracker` 结构;N42 用旧 `accountSet`(无 tx_tracker)→ 不对应。
- **reorg txLookupLock mutex leak**(geth #34039 改 defer):N42 chain reorg 无手动 Unlock 模式 → N/A。
- **SetCode To 非 nil 的 RPC args 校验**(geth #35094 ethapi 侧):N42 `transaction_args.go:toTransaction` **只构造 DynamicFee/AccessList/Legacy,不从 authorizationList 构造 SetCode** → RPC-create 路径不存在,N/A(已在池侧补结构校验覆盖 raw-tx 路径)。
- **simulate withdrawals 回归 / estimateGas blockOverrides / eth/filters 订阅 race**(geth #34939/#34081/#33990):N42 simulate 不产 withdrawals、estimateGas 不支持 blockOverrides、订阅用 Go `event.Feed` → 特性缺位或架构不同,N/A。
- core/vm `stack arena`、`gas vector/budget`、EIP-7708/7610/7954/7840:forward-fork / 大型重构,N42 未到 → 记账。

## 落地
- 本轮唯一真改动 = SetCode 池准入结构校验(已提交 + 测试)。
- **总体**:N42 geth-衍生区已显著分叉,近期 geth fix 多为 N42 已对 / 特性缺位 / geth 新结构专属;借鉴面比预期小,挖到的 1 个 gap 已补。
