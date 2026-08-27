# Linux witness-replay correctness and performance record (2026-08-24)

This record is for Ethereum historical witness replay. It is unrelated to the
seven-node N42 custom-chain fleet. Do not run the fleet concurrently with these
measurements.

## Required semantics

- Input range for the complete archive: blocks `0..25,765,565`, therefore the
  CLI end (exclusive) is `--end 25765566`.
- Use `--no-output`: do not encode or write account/storage changesets,
  receipts, or witness copies.
- Keep default verification enabled. Every post-Byzantium block verifies both
  `GasUsed` and `ReceiptHash`; pre-Byzantium receipt roots cannot be reproduced
  from the compact status-only receipt representation, so those blocks verify
  gas as documented by the tool.
- Never use `--skip-verify` or `--continue-on-error` for the complete gate.
- Inputs are the columnar `headerc/bodyc`, witness and senders tables under
  `/data/blockchain/witness`; bytecode comes from the complete content-addressed
  Code table in `/data/blockchain/code-mdbx`. The seven bodyc segments affected
  by the old EIP-7702 V encoder must be replaced with regenerated `bfAuthVFull`
  segments before the complete gate can pass.

## Correctness issue found while profiling

Block `24,231,367` failed after transaction 53 with transaction 54 reporting a
nonce-too-high error. The same block passed with the geth ancient comparison
slice. `body-cmp` showed that transaction 53 was EIP-7702 type 4 and all fields
matched except authorization `V`:

```text
geth: V=27
bodyc: V=0
```

The original bodyc encoder stored authorization V as one byte but mapped only
the value 1 to 1; every other value became zero. Invalid legacy values 27/28
exist in canonical Ethereum history. N42 deliberately rejects V greater than
1. Changing 27 to 0 can turn an invalid authorization into a valid one and
therefore change execution state.

The fix has two parts:

1. New bodyc segments set `bfAuthVFull` and preserve authorization V as a
   trimmed uint256, so future values are not limited to one byte.
2. Existing bodyc data can be repaired on read only for the observed legacy
   27/28 values. For a block with a zero-V EIP-7702 authorization, the reader
   compares the reconstructed transaction root with the canonical `headerc`
   `TxHash`; on mismatch it tries 27/28 and accepts a repair only when the
   canonical root matches. Ethereum authorization validation rules are not
   changed.

The compatibility path is not a complete substitute for regenerated data. The
old encoder mapped every value other than 1 to zero, so an arbitrary uint256 V
is information-theoretically unrecoverable from bodyc. The canonical geth scan
found 21 affected blocks. The full Linux run recovered 20 whose old values were
27/28 and then correctly stopped at block `24,993,792`, whose five zero-V
candidates cannot reproduce the canonical transaction root with 27/28.

The formerly failing block now prints the compatibility repair and completes:

```text
bodyc: restored legacy EIP-7702 authorization V using canonical tx root at block 24231367
Replay complete blocks=1 failed=0 gas=29400429 txs=241
```

Evidence:

- `/data/blockchain/wr-logs/w192-fix-gate-24231367-20260824.log`
- `/data/blockchain/wr-logs/w5-24231367-geth-senders.log`
- `/data/blockchain/wr-logs/w5-24231367-bodyc-senders.log`

## Hardware and worker sweep

Host: AMD EPYC 9B45, 128 physical cores / 256 SMT threads, one NUMA node,
approximately 136 GiB RAM.

The 256-worker 200,000-block dense test was correct but over-threaded:

```text
range=24,000,000..24,199,999
failed=0, 1,169 blk/s, CPU=90.7 logical cores, MaxRSS=96.2 GiB
major faults=559,343, voluntary context switches=45,489,873
```

A warm 20,000-block sweep first suggested 192 workers, but the longer
100,000-block dense comparison showed the stable optimum lower. All rows used
the same range `24,100,000..24,199,999`, `--no-output`, default verification and
the same inputs:

| Workers | GOGC | limit | blk/s | MaxRSS | CPU | voluntary context switches |
|---:|---:|---:|---:|---:|---:|---:|
| 96 | 300 | 48 GiB | 1,359 | 48.5 GiB | 64.6 cores | 5.19 M |
| 112 | 300 | 56 GiB | 1,413 | 57.0 GiB | 71.6 cores | 6.72 M |
| **128** | **300** | **64 GiB** | **1,429** | **64.1 GiB** | **76.8 cores** | **8.23 M** |
| 192 | 300 | 80 GiB | 1,333 | 78.7 GiB | 83.0 cores | 14.90 M |
| 128 | 100 | 48 GiB | 1,350 | 35.9 GiB | 74.1 cores | 8.37 M |

The warm short-window optimum was 128/300/64, but the cold full-archive run
showed that this is not the safe long-run default: it reached a large heap high
water mark and paused for almost seven minutes in one whole-heap collection.
Use **112/100/48** for the next full gate unless a new long-run comparison proves
a better setting. Compared with 128 workers in the 100k dense sweep, 112 gave up
only 1.1% throughput while using 7.2 GiB less RSS, 6.8% less CPU and 18.3% fewer
voluntary context switches. Use 96 workers if minimizing memory/scheduler load
matters more than the roughly 5% dense-range throughput cost.
`GOMAXPROCS` stays at the machine default 256; capping it to 128/160/192 did not
improve throughput.

The cold early-chain control (`1,000,000..1,099,999`) completed 100,000 blocks
in 2.9 seconds at 35,360 blk/s, with about 68 MiB filesystem input. Disk I/O is
not the dominant full-run cost; transaction-dense EVM execution is.

Logs are under `/data/blockchain/wr-logs/`, notably:

- `w128-long-241m-20260824.log`
- `w112-long-241m-20260824.log`
- `w96-long-241m-20260824.log`
- `w128-gc100-241m-20260824.log`
- `w128-cold-1m-20260824.log`

## pprof result and CPU optimization

The initial 192-worker 30-second CPU profile showed the EVM interpreter as the
dominant cost (78.5% cumulative). Keccak was 9.7% flat; stack push was 8.0%.
Disk decoding was not a material CPU hotspot. Heap profiling showed large
short-lived bodyc segment buffers plus per-worker storage maps, explaining why
more workers reduced throughput.

`Contract.isCode` already cached the JUMPDEST bitmap in `c.analysis`, but every
later JUMP/JUMPI still performed a `jumpdests` map lookup. Using the immutable
per-frame bitmap directly after the first lookup changed no execution result and
reduced profile costs:

| CPU node | before | after | change |
|---|---:|---:|---:|
| `Contract.validJumpdest` cumulative | 5.03% | 2.79% | -44% |
| `runtime.mapaccess2` cumulative | 6.09% | 4.27% | -30% |

The post-change dense gate completed with `failed=0`, 1,434 blk/s. User CPU for
the timed A/B fell from 5,534s to 5,408s (about 2.3%). Profiles:

- `/data/blockchain/wr-pprof/w192-241m-20260824/cpu.pprof`
- `/data/blockchain/wr-pprof/w192-241m-20260824/heap.pprof`
- `/data/blockchain/wr-pprof/w128-jumpfast-20260824/cpu.pprof`
- `/data/blockchain/wr-pprof/w128-jumpfast-20260824/heap.pprof`

The post-change heap snapshot sampled 19.7 GB of live objects. The dominant
retainers were storage origin maps (`GetCommittedState`, 39.6%), the zstd
decoded segment buffer (23.3%), dirty storage maps (`setState`, 13.5%) and bodyc
decode objects (8.3%). This explains both successful control levers:

- fewer workers directly reduce the number of simultaneously retained
  per-block storage maps;
- GOGC 100 collects the multi-gigabyte segment decode buffers earlier instead
  of allowing them to accumulate toward a high soft limit.

The sequential witness pipeline now uses a consuming body read: after a block
is handed to a worker, its slot in the cached 8192-block bodyc segment is set to
nil. The general `ReadBody` random-access API remains unchanged. This bounds
body objects retained by the cache to the in-flight window rather than every
already-dispatched block in the current segment. The unit regression passes;
its performance and RSS impact must be measured in the required short A/B after
the regenerated bodyc segments arrive, before using the binary for the full
gate.

The repository-wide targeted race run also exposed an unrelated shutdown race
in `StreamingFullStateRoot`: after cancelling its two ETL stream producers it
read their error variables before joining the goroutines. A `WaitGroup` now
joins both producers before those reads. The reproducer
`go test -race ./internal/ethel -run TestStreamingFullStateRootMatchesFlatDB`
passes after the fix. This path is not used by the no-output witness benchmark,
but the issue was retained in the audit rather than hidden.

## Cold full-archive attempt

The first cold full attempt used 128 workers, GOGC 300 and a 64 GiB soft memory
limit. It processed blocks `0..24,993,791` with default GasUsed + ReceiptHash
verification before the unrecoverable old-bodyc block stopped the reader:

```text
elapsed wall             2:16:59
average CPU              54.82 logical cores
maximum RSS              70,378,200 KiB (67.1 GiB)
filesystem input         1,660,365,304 blocks (~792 GiB at 512 B/block)
major page faults        7,621,971
voluntary ctx switches   1,592,625,421
process swaps            0
compatibility recoveries 20
final safe stop          block 24,993,792, zero-V candidates=5
```

At head `10,148,666`, progress stopped from 09:06:39 to 09:13:33 UTC while the
process remained CPU-active. RSS had climbed to roughly 58--63 GiB and fell to
about 28 GiB after progress resumed. No process swap or memory/I/O pressure was
reported. This is consistent with an expensive whole-heap GC at the soft-limit
high water mark, and is why GOGC 300 is rejected for the next long run even
though it won the warm 100k-block sweep.

The full run's lower average CPU occupancy and approximately 792 GiB of cold
filesystem input also qualify the earlier warm-range conclusion: EVM execution
dominates CPU profiles, but a true cold archive scan is materially affected by
storage/page-fault latency. Raising workers beyond the execution optimum only
adds memory residency and scheduler traffic; it does not fix the cold-read
path. `/data` is XFS with `noatime` on a 7 TB NVMe using the `none` scheduler,
with 1023 requests and 128 KiB kernel read-ahead. The body/header/witness streams
are sequential but Code MDBX reads are random, so increasing device-wide
read-ahead is not accepted without a cold A/B; it could evict useful Code pages
while providing no benefit to already-large bodyc `ReadAt` calls.

The log is `/data/blockchain/wr-logs/full-w128-gc300-m64-20260824.log`. Five
historical invalid-jump warnings were non-fatal. The `--no-output` directory is
empty (zero bytes), as required.

## Full-run command

Run only after the regenerated affected bodyc segments have replaced the old
ones, and after other host workloads and the N42 fleet are stopped:

```bash
ulimit -n 65536
/data/blockchain/bin/witness-replay \
  --input-headers-bodies /data/blockchain/witness \
  --input-witness /data/blockchain/witness \
  --senders /data/blockchain/witness \
  --datadir /data/blockchain/code-mdbx \
  --output /data/blockchain/wr-out/full-no-output-20260824 \
  --no-output \
  --start 0 --end 25765566 \
  --workers 112 --gogc 100 --mem-limit-gb 48
```

While the run is in a representative transaction-dense region, collect the
same evidence with the existing helper (witness-replay exposes pprof on 6061):

```bash
cd /home/n42/src/n42/N42-gov5
PPROF_HOST=127.0.0.1:6061 \
PPROF_OUT=/data/blockchain/wr-pprof/full-w112-gc100-m48-20260824 \
  bash scripts/pprof.sh all 60
```

The Ubuntu Go package on this host does not ship `go tool pprof`. The adapted
helper still captures CPU, heap, alloc, mutex, block and goroutine artifacts and
does not fail merely because local top rendering is unavailable; render the raw
profiles later with a standard Go toolchain.

Acceptance is a final `Replay complete` with `head=25765565`,
`blocks=25765566`, and `failed=0`. Any body-root recovery failure, gas mismatch,
receipt-root mismatch, witness read error, or process error stops the run; do not
paper over it with continue-on-error. With regenerated `bfAuthVFull` segments,
the complete log should not need any legacy authorization recovery lines.
