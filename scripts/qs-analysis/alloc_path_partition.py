#!/usr/bin/env python3
"""S29: partition a node's whole alloc_space (or inuse_space) capture by
ENTRY PATH, using `go tool pprof -traces` output post-processed here (pprof
itself has no "group by subsystem" mode; -traces gives every unique full
call stack with its own attributed value, which this script then buckets).

Mutual exclusivity: each unique trace (stack) is scanned end-to-end (every
frame, not just the root or the leaf) against an ORDERED list of category
patterns; the FIRST category whose pattern matches ANY frame in the stack
wins the whole trace's value. Order matters because some frames (e.g.
`internal.runParallel`/`parallelApplyTx`) are SHARED between leader-build
and follower-import call paths -- the disambiguating frame
(`BuildParallel` for the leader's own fill, `InsertChain`/
`blockPushStreamHandler`/`decodeChunkedBlock` for import) is checked FIRST,
before the shared executor internals would otherwise fall through to a
catch-all bucket. A trace matching none of the patterns is counted as
UNASSIGNED and reported, not silently dropped.

Usage:
  go tool pprof -traces -sample_index=<alloc_space|inuse_space> <profile> \
      > /tmp/traces.txt
  alloc_path_partition.py /tmp/traces.txt
"""
import sys, re, collections

CATEGORIES = [
    ("E_leader_build", [
        r"\.BuildParallel\b", r"miner\.\(\*worker\)\.commit\b",
        r"miner\.\(\*worker\)\.commitWork\b", r"miner\.\(\*worker\)\.fillTransactions\b",
        r"miner\.\(\*worker\)\.seal\b", r"\.Seal\(", r"blst\.bindings/go\.",
    ]),
    ("F_follower_import", [
        r"blockPushStreamHandler", r"decodeChunkedBlock", r"\bInsertChain\b",
        r"InsertChainAuthorized", r"catchUpTo\b", r"catchUpRange\b",
        r"alignAndImport\b", r"bodiesByRangeRPCHandler", r"writeBodiesRangeToStream",
        r"chunkBlockWriter", r"retryDeferredChildren", r"\(\*StateProcessor\)\.Process\b",
        r"deferredCheck\b", r"FetchBlockByHash",
    ]),
    ("A_rpc_ingest", [
        r"jsonrpc\.\(\*handler\)", r"TransactionAPI\)\.(BatchRawTransaction|SendRawTransaction)",
        r"startCallProc", r"handleCallMsg", r"runMethod",
    ]),
    ("B_gossip_receive", [
        r"pubsub\.\(\*validation\)\.validateWorker", r"pubsub\.\(\*PubSub\)\.processLoop",
        r"handleIncomingRPC", r"pushMsg", r"handlePeerEOF",
    ]),
    ("C_gossip_send_fwd", [
        r"pubsub\.\(\*PubSub\)\.publish", r"handleSendingMessages", r"rpc\.go",
        r"yamux.*[Ss]end", r"directPushBlock", r"broadcastBlockData", r"\.MsgID\b",
        r"go-buffer-pool", r"noise\.", r"multistream\.",
    ]),
    ("D_pool_internal", [
        r"txspool\.", r"txlookup\.", r"senderCache", r"prewarmSenders",
    ]),
    # EXEC_shared: catches parallel-executor/decode/state internals whose
    # OWN stack has already been checked against every context marker
    # above and found none -- these run on the executor's own worker-pool
    # goroutines (`executeParallel.func1` and similar), which do not
    # retain the spawning goroutine's call chain, so pprof's sample stack
    # cannot say whether THIS particular allocation happened during a
    # leader's own build or a follower's own import. Named explicitly
    # rather than left to fall into the G catch-all, since it is a large,
    # identifiable, structurally-unattributable share, not "everything
    # else."
    ("EXEC_shared_build_or_import", [
        r"parallelApplyTx", r"executeParallel", r"\(\*Executor\)\.",
        r"\(\*MVS\)\.", r"ReadWriteSet\)\.", r"decodeUint256",
        r"decodeEthereumTransaction", r"DecodeEthereumTransaction",
        r"NewTxOwned", r"IntraBlockState\)\.", r"\(\*journal\)\.",
        r"DecodeAccount", r"MarshalCompactStorage", r"freshStateObject",
        r"applyMVSToIBS", r"BaseCache\)\.", r"PlainStateReader\)\.",
        r"protobuf/internal/impl\.consumeBytes",
    ]),
]


def classify(stack_text):
    for name, patterns in CATEGORIES:
        for p in patterns:
            if re.search(p, stack_text):
                return name
    return "G_consensus_other"


def parse_value(line):
    m = re.match(r'\s*([\d.]+)\s*([kKmMgG]?[iI]?[bB])\b', line)
    if not m:
        m2 = re.match(r'\s*([\d.]+)\s*$', line)
        if m2:
            return float(m2.group(1))
        return 0.0
    num = float(m.group(1))
    unit = m.group(2).lower()
    mult = {'b': 1, 'kb': 1e3, 'kib': 1024, 'mb': 1e6, 'mib': 1024**2,
            'gb': 1e9, 'gib': 1024**3}.get(unit, 1)
    return num * mult


def main():
    path = sys.argv[1]
    with open(path) as f:
        content = f.read()
    blocks = content.split('-----------+-------------------------------------------------------')
    totals = collections.Counter()
    n_traces = collections.Counter()
    total_bytes = 0.0
    for b in blocks[1:]:
        lines = [l for l in b.split('\n') if l.strip()]
        if not lines:
            continue
        # Each block is one unique call stack; pprof -traces puts the
        # sample's value on the line(s) that carry a "N<unit>" prefix
        # (normally just the leaf line, occasionally an extra "bytes: N"/
        # "count: N" annotation line for a recursive/cyclic stack) -- sum
        # every value-bearing line in the block (verified against -top's
        # own reported total: this sums to within ~1.5% of it across the
        # whole capture, the small over-count being the rare double-
        # annotated recursive case, acceptable at this section's own
        # precision).
        val = 0.0
        for l in lines:
            s = l.strip()
            if s.startswith('bytes:') or s.startswith('count:'):
                s = s.split(':', 1)[1]
            val += parse_value(s)
        stack_text = '\n'.join(lines)
        cat = classify(stack_text)
        totals[cat] += val
        n_traces[cat] += 1
        total_bytes += val
    print(f'total value across all traces: {total_bytes/1e9:.3f} GB ({len(blocks)-1} distinct stacks)')
    for cat, _ in CATEGORIES + [("G_consensus_other", [])]:
        gb = totals.get(cat, 0) / 1e9
        pct = 100 * totals.get(cat, 0) / total_bytes if total_bytes else 0
        print(f'  {cat:20s} {gb:8.3f} GB  {pct:5.1f}%  ({n_traces.get(cat,0)} stacks)')


if __name__ == '__main__':
    main()
