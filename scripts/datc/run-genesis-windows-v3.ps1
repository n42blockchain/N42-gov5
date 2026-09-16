# DATC v3 genesis-range build on the Windows box: blocks [0, 17,900,000).
# Pairs with the Linux upper-range build [17,900,000, head) (datc-v3-hi); both
# are merged on Linux with `n42-datc merge`. See docs/ethel/datc-v3-rebuild-runbook.md.
#
# v3 = per-contract storage record depth. The depth map MUST be byte-identical
# on both machines, or a contract's recorded depth and the depth the reader
# looks up disagree after the merge.
#
# Resume: re-run this script (--start auto-loads from DatcMeta/progress).
# Stop:   Ctrl+C ONCE (graceful: finishes the batch, cuts the spill). Never kill the process.
$bin = "C:\N42\N42-gov5\build\bin\n42-datc.exe"
$cs  = "D:/N42-eth1177/chain/freezer"
$hd  = "D:/n42-eth1/chain/freezer"
$out = "D:/n42-datc-v3-lo"
$map = "D:/sto-depth-map-b1024.txt"
$wantMd5 = "39CC2139D9F3157F7F8E44AF4C16C9BF"

$md5 = (Get-FileHash -Algorithm MD5 $map).Hash
if ($md5 -ne $wantMd5) {
  Write-Error "depth map md5 $md5 != $wantMd5 — both ranges must use the same file"
  exit 1
}
& $bin build --src mainnet --changesets $cs --headers $hd --out $out --end 17900000 `
  --sched 1024,16384,1024,1,4194304,4194304 --acc-root-epoch 1 --window=false --concurrent-root `
  --leaf-seg --batch 8192 --map.gb 2048 --dirty.gb 16 --stocache.m 32 --gogc 150 --mem.gb 40 `
  --decode-workers 8 --prefetch --pprof.port 6072 --sto-depth-map $map 2>&1 |
  Tee-Object -FilePath "$out.build.log" -Append
