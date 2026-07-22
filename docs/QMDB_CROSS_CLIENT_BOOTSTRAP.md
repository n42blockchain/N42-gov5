# QMDB Cross-Client Bootstrap v1

`cmd/n42-qmdb-export` exports an existing replay-v2 QMDB database without
modifying it. The portable stream binds chain ID, genesis hash, canonical replay
checkpoint, QMDB root, append cursor, every positional slot, and a trailing
Blake3 content digest.

The exporter fails if historical dead-slot rows are missing. Production exports
therefore require replay data created with `--qmdb-history`; a live-key-only
snapshot cannot reproduce the split commitment.

```bash
go run ./cmd/n42-qmdb-export \
  --db <replay-target>/chaindata \
  --out <checkpoint>.n42qmdb \
  --map.gb 512
```

The Rust verifier consumes the file sequentially, checks chain/checkpoint
identity and the content digest, then recomputes all frozen leaf roots,
active-bit commitments, twig roots, and the upper root.

Existing full replay acceptance on 2026-07-21:

- source replay: 13,070,722 blocks and 24,847,544 transactions;
- target checkpoint: block 13,109,968,
  `b54b8d55b9229deac09f009d958433103e651dd1ec23e9089516b74cf8b89789`;
- 87,786,434 append slots, 5,892,945 live keys;
- portable size: 5,480,812,584 bytes;
- Go and Rust root:
  `be01f8f834d7b203170dc6127a8b3f2582a4fe90c621df48db8213593161084a`.

The original replay and seven-node datadirs were opened read-only and retained.

## Private/custom chain replay

Private chains are not registered in the binary, so `--chain private` alone
cannot supply their chain ID, fork schedule, genesis alloc, or HotStuff
validator set. Pass the exact genesis JSON used to initialize the source. The
replay computes its QMDB genesis hash and refuses to create the target if it
does not match source block 0.

```bash
n42 replay-v2 \
  --source <custom-source-datadir> \
  --target <custom-qmdb-archive-datadir> \
  --chain private \
  --genesis <custom-genesis.json> \
  --tree qmdb \
  --qmdb-history \
  --from 0 --to <checkpoint>
```

Live custom-chain acceptance on 2026-07-21 used chain ID 1143 and a seven-node
HotStuff v4 network. All seven nodes converged on a transaction-bearing block,
then replay-v2 processed blocks 1 through 49 with 247/247 transactions,
`txFailed=0`, and 49/49 matching receipts. The portable checkpoint contained
437 positional slots and 34 live keys. The independent Rust verifier
recomputed the exact root
`6fb33357c8db5eb206f506af271cf5fff885fc11bbd82b405b74a42943c98314`.
