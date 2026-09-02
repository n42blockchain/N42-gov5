# DATC v2 双机并行构建 runbook（Windows 跑下段，Linux 跑上段）

日期：2026-09-02。目标：25.86M 全链 DATC v2 归档。Linux（n42dev，192.168.0.166）内存被其他基准挤占，
两个进程并行会被 OOM 反复杀；改为 **Windows 跑 [0, 17,900,000)**，**Linux 跑 [17,900,000, 25,864,982)**（基底是
`n42-datc-cont-25864981` 里 17.9M 的状态表），最后在 Linux 上 `merge`。

## 0. 前提

- 代码：origin/main ≥ 本文所在 commit（含 `--mem.gb`、`--prefetch`、方向无关的 `merge`）。
- Windows 上重新编译：`go build -tags "nosqlite,noboltdb" -o build\bin\n42-datc.exe .\cmd\n42-datc\`（需要 CGO/MDBX，和以前一样）。
- 输入：`D:/N42-eth1177/chain/freezer`（acctcs+storcs）、`D:/n42-eth1/chain/freezer`（headerc）。
- 输出盘：`D:/n42-datc-v2-lo`，按 2M 实测外推，下段全部产物约 **150–250 GB**（段文件）+ mdbx（17.9M 状态表 + 存储节点记录，约 150–300 GB）。
- **参数必须和 Linux 上段完全一致**（`--sched 1024,16384,1024,1,4194304,4194304 --acc-root-epoch 1 --window=false --leaf-seg`），`merge` 会校验 format/sched/depths/cadence 一致，不一致直接拒绝。`--end 17900000` 必须等于上段起点。

## 1. 启动（PowerShell）

```
C:\N42\N42-gov5\scripts\datc\run-genesis-windows.ps1
```
（脚本内容见 `scripts/datc/run-genesis-windows.ps1`；日志 `D:/n42-datc-v2-lo.build.log`。）

- 心跳每 10 秒一行：`[datc] leaf x% ... | block y% N/17900000 blk/s ETA`。**看 leaf% 不看 block%**（叶变更是真实工作量：下段约 7.13B 次）。
- 中断：**Ctrl+C 一次**，等它打印 `graceful stop at block N (committed; spill cut at frame boundary)` 再关窗口。**绝不能 kill 进程**（会截断 spill 帧；虽然 finalize 现在能跳过截断帧且不会误删 spill，但会丢一批）。
- 续跑：原样重跑脚本，`--start` 从 `DatcMeta/progress` 自动加载。
- 内存：`--mem.gb 40` 是 Go 软上限；实测 2M 阶段堆 4–14 GB，EIP-158 清理区（2.675M–2.72M）和 DeFi 密集区会到 20–30 GB。
- 预计：Linux 上稀疏区 0→4.67M 用了约 3 小时（含一次 DoS 区 4 小时慢段），4.67M→17.9M 密集区按 25–35K lf/s 估 **2–2.5 天**。

## 2. 结束后的自检（Windows）

```
n42-datc.exe verify --out D:/n42-datc-v2-lo --headers D:/n42-eth1/chain/freezer --samples 20
n42-datc.exe bench  --out D:/n42-datc-v2-lo --headers D:/n42-eth1/chain/freezer --changesets D:/N42-eth1177/chain/freezer --samples 200 --mode mixed
```
verify 必须 20/20 且日志里不能出现 `WARNING: skipped N corrupt frame(s)`（出现即 spill 目录被保留、段不完整，先别传）。
参考值（2M，Linux）：verify 根重建 <1 ms；账户 proof p50 12 ms / p99 35 ms；含槽 proof 100% ≤1 s。

## 3. 传回 Linux

只需要输出目录本身（`D:/n42-datc-v2-lo/`：`mdbx.dat` + `leafseg/`），不要传 `leafspill/`（正常结束时已删除）。
目标：`n42@192.168.0.166:/data/blockchain/datc-out/datc-25m-v2-lo/`。传完请核对文件大小/md5。

## 4. Linux 上合并 + 全链验证（由 Linux 侧执行）

```
BIN=/data/blockchain/datc-out/n42-datc-25m-hi4.bin
$BIN merge --into /data/blockchain/datc-out/datc-25m-v2-hi --from /data/blockchain/datc-out/datc-25m-v2-lo
$BIN verify --out /data/blockchain/datc-out/datc-25m-v2-hi --headers /data/blockchain/datc-input/n42-eth1/chain/freezer --samples 50
$BIN bench  --out /data/blockchain/datc-out/datc-25m-v2-hi --headers ... --changesets ... --samples 500 --mode mixed --parallel 8
```
`merge` 方向无关：下段并入上段时，下段在边界 epoch 的"部分"节点记录只有在上段已有同键记录时才丢弃（`TestE2E_SplitMerge` 两个方向都覆盖）。合并后 `datc-25m-v2-hi` 就是全链归档。

## 5. 常见问题

- `--end` 被自动缩到可用块数：正常（headerc/acctcs 的上限）。
- `ROOT MISMATCH`：立即停，别续跑；把块号和最后 50 行日志发给 Linux 侧。
- 日志出现 `storage root hook missed contract`：build 会直接报错退出，同上处理。
