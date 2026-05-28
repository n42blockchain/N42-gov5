// n42-migrate-reth-hashed copies a reth 2.2 hashed-canonical state
// (HashedAccounts, HashedStorages, AccountsTrie, StoragesTrie, Bytecodes)
// into a FRESH N42 MDBX, re-encoding values to N42 conventions. It does
// NOT recompute the trie — reth's already-built intermediate nodes are
// copied verbatim (the BranchNodeCompact value bytes are identical to
// erigon-2.7/N42). The state root is then read directly from the copied
// account-trie root node and checked against the block header.
//
// Prereq: N42 must have incarnation removed from the storage trie key
// (32B addrHash, no 8B incarnation) so HashedStorages/StoragesTrie line
// up with reth's 32B layout.
//
// Transcoding per table:
//   HashedAccounts : key copy (keccak addr); value reth-Compact → MarshalV2
//   HashedStorages : DupSort; key+dup-value byte-copy (32B addrHash dup-key,
//                    dup-value = 32B slotHash + trimmed U256) — identical to N42
//   AccountsTrie   : key copy (StoredNibbles 1-nibble/byte == N42); value copy
//   StoragesTrie   : key = 32B addrHash + nibble-path; reth dup-value =
//                    65B StoredNibblesSubKey(64 padded + 1 len) + node →
//                    extract path = subkey[:subkey[64]], value = node bytes
//   Bytecodes      : key copy (codeHash); value strip 1B reth BytecodeType prefix
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

const (
	rethHashedAccounts = "HashedAccounts"
	rethHashedStorages = "HashedStorages"
	rethAccountsTrie   = "AccountsTrie"
	rethStoragesTrie   = "StoragesTrie"
	rethBytecodes      = "Bytecodes"

	n42HashedAccounts = "HashedAccount"  // kv.HashedAccounts
	n42HashedStorage  = "HashedStorage"  // kv.HashedStorage
	n42TrieAccounts   = "TrieAccount"    // kv.TrieOfAccounts
	n42TrieStorage    = "TrieStorage"    // kv.TrieOfStorage
	n42Code           = "Code"
	n42SyncStage      = "SyncStage"      // kv.SyncStageProgress
)

var emptyCodeHash = crypto.Keccak256Hash(nil)

func rethCfg(d kv.TableCfg) kv.TableCfg {
	d[rethHashedAccounts] = kv.TableCfgItem{}
	d[rethHashedStorages] = kv.TableCfgItem{Flags: kv.DupSort}
	d[rethAccountsTrie] = kv.TableCfgItem{}
	d[rethStoragesTrie] = kv.TableCfgItem{Flags: kv.DupSort}
	d[rethBytecodes] = kv.TableCfgItem{}
	return d
}

// n42Cfg declares the destination tables. HashedStorage uses the
// post-incarnation DupSort layout (64→32). Trie tables are plain.
func n42Cfg(d kv.TableCfg) kv.TableCfg {
	d[n42HashedAccounts] = kv.TableCfgItem{}
	d[n42HashedStorage] = kv.TableCfgItem{
		Flags:                     kv.DupSort,
		AutoDupSortKeysConversion: true,
		DupFromLen:                64,
		DupToLen:                  32,
	}
	d[n42TrieAccounts] = kv.TableCfgItem{}
	d[n42TrieStorage] = kv.TableCfgItem{Flags: kv.DupSort}
	d[n42Code] = kv.TableCfgItem{}
	d[n42SyncStage] = kv.TableCfgItem{}
	return d
}

// decodeRethAccount decodes a reth Compact-encoded Account value.
// Layout: 2-byte flag header (nonce_len 4b | bal_len 6b | hashFlag 1b),
// then nonce (trimmed BE), balance (trimmed BE), codeHash (32B if flagged).
func decodeRethAccount(v []byte) (nonce uint64, balance uint256.Int, codeHash types.Hash, ok bool) {
	if len(v) < 2 {
		return
	}
	flags := uint16(v[0]) | uint16(v[1])<<8
	nonceLen := int(flags & 0x0f)
	balLen := int((flags >> 4) & 0x3f)
	hasHash := (flags>>10)&1 == 1
	want := 2 + nonceLen + balLen
	if hasHash {
		want += 32
	}
	if len(v) != want {
		return
	}
	p := 2
	var nb [8]byte
	if nonceLen > 0 {
		copy(nb[8-nonceLen:], v[p:p+nonceLen])
		nonce = binary.BigEndian.Uint64(nb[:])
	}
	p += nonceLen
	var bb [32]byte
	if balLen > 0 {
		copy(bb[32-balLen:], v[p:p+balLen])
	}
	balance.SetBytes(bb[:])
	p += balLen
	if hasHash {
		copy(codeHash[:], v[p:p+32])
	}
	return nonce, balance, codeHash, true
}

func main() {
	rethDir := flag.String("reth", `D:/reth2k/db`, "reth 2.2 db dir (read-only)")
	dstDir := flag.String("dst", `D:/N42-hashed/chaindata`, "FRESH N42 chaindata dir (created)")
	headBlock := flag.Uint64("head-block", 25096155, "reth head block → ethel-last-block")
	dirtyGB := flag.Uint64("dirty-space-gb", 48, "MDBX dirty pool GB for dst")
	commitEvery := flag.Uint64("commit-every", 5_000_000, "commit interval (entries)")
	limit := flag.Uint64("limit", 0, "per-table cap for sampling (0=all)")
	phases := flag.String("phases", "acc,sto,tacc,tsto,code,head", "comma list of phases to run")
	flag.Parse()

	logger := log.New()
	rethDB, err := mdbx.NewMDBX(logger).Path(*rethDir).Label(kv.ChainDB).
		PageSize(4096).MapSize(4 * datasize.TB).Readonly().Accede().
		WithTableCfg(rethCfg).Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open reth:", err)
		os.Exit(1)
	}
	defer rethDB.Close()

	if err := os.MkdirAll(*dstDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "mkdir dst:", err)
		os.Exit(1)
	}
	dstDB, err := mdbx.NewMDBX(logger).Path(*dstDir).Label(kv.ChainDB).
		PageSize(4096).MapSize(4 * datasize.TB).GrowthStep(4 * datasize.GB).
		DirtySpace(uint64(datasize.ByteSize(*dirtyGB) * datasize.GB)).
		WithTableCfg(n42Cfg).Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open dst:", err)
		os.Exit(1)
	}
	defer dstDB.Close()

	run := func(p string) bool {
		for _, x := range splitComma(*phases) {
			if x == p {
				return true
			}
		}
		return false
	}

	t0 := time.Now()
	if run("acc") {
		if err := migrateHashedAccounts(rethDB, dstDB, *commitEvery, *limit, logger); err != nil {
			fail("hashed accounts", err)
		}
	}
	if run("sto") {
		if err := migrateHashedStorages(rethDB, dstDB, *commitEvery, *limit, logger); err != nil {
			fail("hashed storages", err)
		}
	}
	if run("tacc") {
		if err := migrateAccountsTrie(rethDB, dstDB, *commitEvery, *limit, logger); err != nil {
			fail("accounts trie", err)
		}
	}
	if run("tsto") {
		if err := migrateStoragesTrie(rethDB, dstDB, *commitEvery, *limit, logger); err != nil {
			fail("storages trie", err)
		}
	}
	if run("code") {
		if err := migrateBytecodes(rethDB, dstDB, *commitEvery, *limit, logger); err != nil {
			fail("bytecodes", err)
		}
	}
	if run("head") {
		if err := writeHead(dstDB, *headBlock); err != nil {
			fail("write head", err)
		}
	}
	logger.Info("migration done", "elapsed", time.Since(t0).Truncate(time.Second))
}

func writeHead(dstDB kv.RwDB, head uint64) error {
	return dstDB.Update(context.Background(), func(tx kv.RwTx) error {
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], head)
		return tx.Put(n42SyncStage, []byte("ethel-last-block"), b[:])
	})
}

func splitComma(s string) []string {
	var out []string
	cur := ""
	for _, c := range s {
		if c == ',' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
		} else {
			cur += string(c)
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func fail(what string, err error) {
	fmt.Fprintf(os.Stderr, "ERROR %s: %v\n", what, err)
	os.Exit(1)
}

var _ = account.StateAccount{}
