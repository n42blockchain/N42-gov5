#!/bin/bash
# DATC v2 upper-range build on Linux: [17,900,000, 25,864,982) over the state copied from
# n42-datc-cont-25864981 (prep-state cleared its v1 Datc* tables). Run under supervise.sh.
BIN=/data/blockchain/datc-out/n42-datc-25m-hi4.bin
CS=/data/blockchain/datc-input/N42-eth1177/chain/freezer
HD=/data/blockchain/datc-input/n42-eth1/chain/freezer
OUT=/data/blockchain/datc-out/datc-25m-v2-hi
exec $BIN build --src mainnet --changesets $CS --headers $HD --out $OUT --end 25864982 \
  --sched 1024,16384,1024,1,4194304,4194304 --acc-root-epoch 1 --window=false --concurrent-root \
  --leaf-seg --batch 4096 --map.gb 4096 --dirty.gb 16 --stocache.m 32 --gogc 150 --mem.gb 32 \
  --decode-workers 16 --prefetch --pprof.port 6073
