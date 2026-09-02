# DATC v2 genesis-range build on the Windows box: blocks [0, 17,900,000).
# Pairs with the Linux upper-range build [17,900,000, 25,864,982) (datc-25m-v2-hi);
# both are merged on Linux with `n42-datc merge`. See docs/ethel/datc-windows-genesis-runbook.md.
#
# Resume: re-run this script (--start auto-loads from DatcMeta/progress).
# Stop:   Ctrl+C ONCE (graceful: finishes the batch, cuts the spill). Never kill the process.
$bin = "C:\N42\N42-gov5\build\bin\n42-datc.exe"
$cs  = "D:/N42-eth1177/chain/freezer"
$hd  = "D:/n42-eth1/chain/freezer"
$out = "D:/n42-datc-v2-lo"
& $bin build --src mainnet --changesets $cs --headers $hd --out $out --end 17900000 `
  --sched 1024,16384,1024,1,4194304,4194304 --acc-root-epoch 1 --window=false --concurrent-root `
  --leaf-seg --batch 8192 --map.gb 2048 --dirty.gb 16 --stocache.m 32 --gogc 150 --mem.gb 40 `
  --decode-workers 8 --prefetch --pprof.port 6072 2>&1 | Tee-Object -FilePath "$out.build.log" -Append
