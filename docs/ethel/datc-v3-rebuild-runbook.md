# DATC v3 全链重建 runbook（两机并行，格式 3）

2026-09-16。目标：用格式 3（按合约的存储记录深度）重建全链归档，修掉带槽证明最坏 23 分钟的尾部。
分工与 v2 相同：**Windows 跑下段 `[0, 17,900,000)`，Linux 跑上段 `[17,900,000, head)`**，最后在 Linux 合并。

## 0. 为什么要重建

- 构建端缺陷 `73779b51`：存储节点记录在 0 层以下被写成了**零 nibble 路径**，每个合约只有 0 号子节点有记录，
  读取端只能把另外 15 棵子树整个折叠出来——USDT 一次带槽证明 1.927 亿次读里 1.82 亿次（94%）来自于此。
- 深度策略 `2ebcafde`：深度按合约的历史键数决定，而不是全网一个常数。
- **记录无法事后补写**（历史节点字节没有保存），所以只能重建。现有归档在重建期间继续服务，且已验收。

## 1. 前提

| 项 | 值 |
|---|---|
| 代码 | `origin/main` ≥ `a8775a72`（路径键修复、动态深度、独立存储阶梯、dense 陈旧修复、按合约截断层、64 KiB 帧、格式 3） |
| Windows 编译 | `go build -tags "nosqlite,noboltdb" -o build\bin\n42-datc.exe .\cmd\n42-datc\`（需要 CGO/MDBX） |
| Linux 二进制 | `/data/blockchain/datc-out/n42-datc-25m-hi19.bin` |
| 深度映射 | `sto-depth-map-v3.txt`（四列 `<addrHash> <depth> <level> <shift>`；折叠宽度 512、fold-target 300：28 个合约截断，24 个在 L0、4 个在 L1），144,361 行，**md5 `2c188263b4d0b05081fc3bf9325046c8`** |
| 上段起点状态 | Windows `D:\n42-datc-cont-25864981`（`--records-only` 续建库，状态停在 17,900,000） |

**两台机器必须用同一份映射文件**：两边的脚本都会自校 md5，不一致直接退出。映射由
`n42-datc segcount --out <旧归档> --fold-width 512 --fold-target 300 --blocks <链长> --sto-sched 1024,1024,1024,1024,4096,4096 --map <file>` 生成，只需生成一次并复制。

## 1.5 重建参数（两台机器完全一致，2026-09-16 审核后定稿）

```
--acc-depth 5 --acc-root-epoch 1
--sched     1024,16384,1024,4096,4096,4194304     # 账户树：e1..e4 = 16384,1024,4096,4096
--sto-sched 1024,1024,1024,1024,4096,4096         # 存储树：深层保持短 epoch
--sto-depth-map sto-depth-map-b512.txt            # 折叠宽度 512
--window=false --concurrent-root --leaf-seg
```

为什么是这三处（详见 `datc-dynamic-storage-depth.md` 第 8 节"审核"）：

- **折叠宽度 512**：带槽证明读放大 ≤ 33×2×512 ≈ 3.4 万次读，8 并发下 100–140 ms；空间与 1024 几乎相同。
- **账户 accDepth 5、level 3/4 epoch 4096**：账户折叠宽度从约 1 万键降到约 600 键，账户证明从 p50 190 ms（8 并发）降到约 25 ms；
  同时把 level 3 从逐块改为 4096，账户节点段（现 270 GB）预计降到 50 GB 量级。
- **独立的存储阶梯**：不再让存储树深层沿用账户树的 e3=1（逐块）与 e4=4194304（400 万块窗口）。

这套形状（缩放版）在 e2e 里逐高度校验过根与证明：`TestCfgAcc5Deep4096`/`TestShapeAcc5Growing`（账户）、
`TestShapeStoGrowing`（存储 5 层）均通过。

## 2. 启动

**Windows（下段）**

```powershell
# 1) 把映射复制到 D:\sto-depth-map-v3.txt，核对 md5
Get-FileHash -Algorithm MD5 D:\sto-depth-map-v3.txt
# 2) 开跑（脚本会先自校 md5）
C:\N42\N42-gov5\scripts\datc\run-genesis-windows-v3.ps1
```

输出 `D:/n42-datc-v3-lo`，日志同名 `.build.log`。停机只用 **Ctrl+C 一次**，等它打印
`graceful stop at block N`；**绝不能 kill**。续跑＝原样重跑脚本。

**Linux（上段）**

```bash
# 1) 收下 Windows 的中段状态库（含 17.9M 当前态表），拷成上段输出目录
#    目标：/data/blockchain/datc-out/datc-v3-hi
# 2) 清成干净的 v3 输出（清空全部 Datc* 表，保留状态表）
/data/blockchain/datc-out/n42-datc-25m-hi19.bin prep-state --out /data/blockchain/datc-out/datc-v3-hi
#    必须打印 progress=17900000
# 3) 开跑（脚本自校映射 md5）
setsid nohup /data/blockchain/datc-out/run-rebuild-upper.sh > /data/blockchain/datc-out/datc-v3-hi.build.log 2>&1 < /dev/null &
```

## 3. 中途检查点：上段过 20M 就先 bench

2M 原型只验证了机制，**收益要到 DeFi 密集区才出现**。上段跑过 20,000,000 后立刻跑一次：

```bash
n42-datc-25m-hi19.bin bench --out datc-v3-hi --headers <hd> --changesets <cs> \
  --samples 300 --seed 3 --mode mixed --parallel 8 --json bench-v3-partial.json
```

对照现有归档的同类数字：账户 p50 164ms / ≤1s 98.4%；带槽 p50 177ms / p90 148s / p99 22min。
**带槽 p90 必须显著下降**（预期进入亚秒）。若没有，停下来查 `DatcStoDepth` 是否按合约写出、
以及慢查询的折叠深度（`DATC_FOLD_TRACE=1 n42-datc proof ...`），不要闷头跑完一周。

## 4. 合并与验收

```bash
# 下段传回 Linux（只传 mdbx.dat + leafseg/，不要传 leafspill/）
n42-datc-25m-hi19.bin merge --into datc-v3-hi --from datc-v3-lo   # 下段并入上段
n42-datc-25m-hi19.bin verify --out datc-v3-hi --headers <hd> --samples 50          # 须 50/50
n42-datc-25m-hi19.bin bench  --out datc-v3-hi --headers <hd> --changesets <cs> \
  --samples 500 --mode mixed --parallel 8                                          # 真正的验收
```

`verify` **不读 leaf 段**（`accroot=1` 时每块都有根记录），所以验收以 bench 为准。
`merge` 会校验两段 meta 的 format/sched/深度一致，不一致直接拒绝。

## 5. 规矩

- 收尾（`finalize-leaves`）**绝不能挂在 `supervise.sh` 或任何自动重启下**：2026-09-15 的重复行事故就是这么来的；
  新版守护脚本会在构建到达终点后拒绝重启。
- 大合约的收尾用外排序版本（hi10 及以后），内存约 2×`DATC_FINALIZE_RUN_BYTES`（默认 1 GiB）。
- 重建期间**保留现有归档**（1011 GB），验收通过前不要删。
- 磁盘：Linux `/data` 2.6 TB 可用；上段（含状态表）按上一轮外推 700–900 GB，中段状态拷贝约 435 GB。
  下段放 Windows，合并前再传回。
