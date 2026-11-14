# Consolidated command-line tools

One-off probe binaries under `cmd/` were merged into a few multi-subcommand tools. Run a tool without arguments to list its subcommands; `<tool> <sub> -h` shows the flags (identical to the old binary). Build with `go build -tags nosqlite,noboltdb -o build/bin/<tool> ./cmd/<tool>`.

## n42-reth

Analysis probes for a reth MDBX database (codehash-rewrite and snapshot-export write output files) (changesets, history indices, plain state, snapshot export). Make target: `make n42-reth` (aliases: reth-tools, reth-cs-head, reth-cs-timeseries, reth-cs-cumulative, reth-history-stats).

| Old binary | New invocation | Purpose |
|---|---|---|
| `reth-acct-dump` | `n42-reth acct-dump` | print raw PlainAccountState bytes for an address |
| `reth-acct-history` | `n42-reth acct-history` | walk AccountChangeSets for an address, print (block, prevValue) |
| `reth-acct-history-bitmap` | `n42-reth acct-history-bitmap` | dump AccountsHistory block list (roaring bitmap) for an address |
| `reth-addr-block` | `n42-reth addr-block` | count rows / sample slots for (block, addr) in StorageChangeSets |
| `reth-codehash-rewrite` | `n42-reth codehash-rewrite` | rewrite PlainAccountState with dict-indexed codeHash, measure size delta |
| `reth-codehash-uniq` | `n42-reth codehash-uniq` | unique codeHash count, skew and dictionary savings analysis |
| `reth-cs-cumulative` | `n42-reth cs-cumulative` | rolling cumulative unique active addresses / slots over N days |
| `reth-cs-dict-reuse` | `n42-reth cs-dict-reuse` | address / codeHash reuse rate over rolling block windows |
| `reth-cs-dump-block` | `n42-reth cs-dump-block` | dump all AccountChangeSet entries for one block |
| `reth-cs-head` | `n42-reth cs-head` | last block number in Account/StorageChangeSets |
| `reth-cs-range` | `n42-reth cs-range` | earliest and latest block in Account/StorageChangeSets |
| `reth-cs-timeseries` | `n42-reth cs-timeseries` | per-day changeset activity over a recent block window |
| `reth-history-stats` | `n42-reth history-stats` | modification-count distribution of Accounts/StoragesHistory |
| `reth-probe` | `n42-reth probe` | dump first N entries of a MDBX table to inspect layout |
| `reth-slot-history` | `n42-reth slot-history` | scan StorageChangeSets for one (addr, slot) over a block range |
| `reth-snapshot-export` | `n42-reth snapshot-export` | export PlainAccount/StorageState into RecSplit-indexed snapshot files |
| `reth-state-at-snapshot` | `n42-reth state-at-snapshot` | reconstruct account state at the snapshot block from changesets |
| `reth-tables-list` | `n42-reth tables-list` | list every MDBX table with entry counts |
| `reth-value-stats` | `n42-reth value-stats` | value-length histograms and layout size estimates |
| `scan-reth-cs` | `n42-reth scan-cs` | changeset update-count distribution (hot data threshold) |

## n42-trie

MDBX trie-table analysis and rebuild/diff probes (commitment-compression research).

| Old binary | New invocation | Purpose |
|---|---|---|
| `n42-trie-stat` | `n42-trie stat` | TrieAccount/TrieStorage record counts, bytes and node-shape classes |
| `n42-trie-probe` | `n42-trie probe` | structural shape of a migrated chaindata's TrieOfAccounts/TrieOfStorage |
| `n42-trie-fulldiff` | `n42-trie fulldiff` | full N42-native trie rebuild over a reth-migrated DB, diff vs stored nodes |
| `n42-trie-recorddiff` | `n42-trie recorddiff` | rebuild one account's storage subtree from HashedStorage, diff records |
| `n42-trie-compress` | `n42-trie compress` | measure compressibility of reth AccountsTrie/StoragesTrie tables |
| `n42-trie-branch-anatomy` | `n42-trie branch-anatomy` | byte-level dissection of reth trie branch nodes |

## n42-progress

Read/write the ethel progress markers stored in MDBX. `show` and `set` keep their positional `--datadir`/`--block` argument parsing (no `-h`).

| Old binary | New invocation | Purpose |
|---|---|---|
| `read-progress` | `n42-progress read` | print the ethel progress marker for a datadir (flags) |
| `show-progress` | `n42-progress show` | read the ethel progress marker from MDBX (--datadir D) |
| `set-progress` | `n42-progress set` | write the ethel progress marker to MDBX (--datadir D --block N [--key K]) |
| `write-progress` | `n42-progress write` | set the ethel progress marker for a datadir (flags) |
| `n42-read-ethel-progress` | `n42-progress read-ethel` | print all known progress markers in an MDBX env (read-only) |

## n42-table

MDBX table inspection (counts, sizes, diffs, checksums) plus `clear`, which wipes a table. diff/keydiff/stat/sum take positional arguments and have no `-h`.

| Old binary | New invocation | Purpose |
|---|---|---|
| `tabdiff` | `n42-table diff` | compare one table between two MDBX dirs (positional: dirA dirB table) |
| `tabkeydiff` | `n42-table keydiff` | compare key sets of a table between two MDBX dirs (positional) |
| `tabstat` | `n42-table stat` | per-table entry/size stats of an MDBX dir (positional: dir) |
| `tabsum` | `n42-table sum` | checksum of the named tables of an MDBX dir (positional: dir table...) |
| `table-size` | `n42-table size` | chaindata table entry counts via cursor walk |
| `mdbx-table-stat` | `n42-table mdbx-stat` | read-only entries / page-count / byte-size per table of an MDBX database |
| `clear-table` | `n42-table clear` | ClearBucket a named table in a chaindata MDBX env (writes) |

## n42-freezer

Readers for freezer (ancient) directories; `retrim` rewrites a table index. heads/items take positional arguments and have no `-h`.

| Old binary | New invocation | Purpose |
|---|---|---|
| `freezer-heads` | `n42-freezer heads` | print every table's coverage in a freezer directory (arg: dir) |
| `freezer-info` | `n42-freezer info` | dump items() per table of a freezer dir, raw and compressed |
| `freezer-items` | `n42-freezer items` | print item counts of freezer tables (positional) |
| `freezer-retrim` | `n42-freezer retrim` | rewrite a per-item freezer table's cidx to trim it (writes) |
| `block-senders-list` | `n42-freezer block-senders-list` | ecrecover a geth-ancient block's txs and print (idx, sender, nonce) |
| `receipts-probe` | `n42-freezer receipts-probe` | time reading N receipts from a compressed freezer table |
| `dump-tx` | `n42-freezer dump-tx` | print a single tx from a geth ancient block body |

## n42-code

Code-table inspection and repair tools (`copy` and `fix` write). `copy` takes positional arguments and has no `-h`.

| Old binary | New invocation | Purpose |
|---|---|---|
| `check-code` | `n42-code check` | print the Code table row count of a datadir's MDBX |
| `check-account-code` | `n42-code check-account` | look up an account's codeHash and check its bytecode in the Code table |
| `get-code` | `n42-code get` | look up bytecode by codeHash or via an account in PlainState |
| `copy-code` | `n42-code copy` | copy the Code table from one MDBX to another (writes; positional --src/--dst) |
| `code-fix` | `n42-code fix` | detect and unwrap reth LegacyAnalyzedBytecode wrappers in the Code table (writes) |
| `n42-check-code` | `n42-code check-hashed` | verify every HashedAccount codeHash has a Code-table entry |
