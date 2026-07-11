// n42-migrate-reth-hashed copies a reth 2.2 hashed-canonical state
// (HashedAccounts, HashedStorages, AccountsTrie, StoragesTrie, Bytecodes) into
// a FRESH N42 MDBX, re-encoding values to N42 conventions.
//
// DEFAULT (P7-A, 2026-05-29): the trie is imported VERBATIM (`tacc`/`tsto`
// phases byte-copy reth's AccountsTrie/StoragesTrie), then verified in place by
// `vtrie` against --expect-root. reth trie nodes are byte-compatible with N42's
// MarshalTrieNode (same mask order; reth's missing "+1" own-hash and missing
// keylen-0/keylen-32 empty-path roots are handled by UnmarshalTrieNode's
// own-hash detection and the P6 StorageTrieCursor lvl-0 synthesis). This is far
// faster than rebuilding (no descent to leaves, no HashBuilder over 24M state).
//
// Verified before flipping the default: cmd/n42-reth-eip2935-repro replays the
// REAL reth EIP-2935 storage nodes across 2755 incremental rounds spanning the
// once-"wedging" block 25,191,537, and verbatim-incremental == full-rebuild
// exactly. The prior #150 deprecation of tacc/tsto was a pre-P6 artifact: the
// on-disk stale lived in D:/N42-hashed because a pre-P6 binary wrote stale
// nodes, not because verbatim import is wrong on the current binary.
//
// FALLBACK: if `vtrie` ever reports a mismatch, re-run with
// `--phases acc,sto,code,rtrie,head --expect-root=<root>` to rebuild
// TrieAccount/TrieStorage from the hashed leaves (verify-before-clear).
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
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
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
	// Default: VERBATIM trie import (tacc/tsto byte-copy) + vtrie verify
	// (P7-A). Far faster than rebuilding. rtrie (rebuild-from-leaves) remains
	// available as a fallback via explicit --phases if vtrie ever mismatches.
	// For the fastest possible import (no verify), drop vtrie:
	//   --phases acc,sto,tacc,tsto,code,head
	phases := flag.String("phases", "acc,sto,tacc,tsto,code,vtrie,head", "comma list of phases to run (default verbatim: acc,sto,tacc,tsto,code,vtrie,head; rebuild fallback: acc,sto,code,rtrie,head)")
	expectRoot := flag.String("expect-root", "", "expected stateRoot hex of --head-block; vtrie/rtrie verify against it (vtrie: read-only check; rtrie: verify-before-clear). empty = skip verify, not recommended")
	tmpdir := flag.String("tmpdir", `D:/N42-trie-tmp`, "rtrie: ETL spill dir (same fast drive as dst)")
	accBufGB := flag.Uint64("acc-buf-gb", 4, "rtrie: ETL account buffer GB")
	stoBufGB := flag.Uint64("sto-buf-gb", 16, "rtrie: ETL storage buffer GB")
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

	// Graceful stop: SIGINT/SIGTERM cancels ctx; phases commit their current tx and
	// return context.Canceled so the partial output is durable and a re-run resumes
	// (acc/sto Append-resume; tacc/tsto/code re-run cheaply). NEVER hard-kill.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	handle := func(name string, err error) {
		if err == nil {
			return
		}
		if errors.Is(err, context.Canceled) {
			say("interrupted during %s — progress committed; re-run with same flags to RESUME", name)
			dstDB.Close()
			rethDB.Close()
			os.Exit(0)
		}
		fail(name, err)
	}

	t0 := time.Now()
	if run("acc") {
		handle("hashed accounts", migrateHashedAccounts(ctx, rethDB, dstDB, *commitEvery, *limit, logger))
	}
	if run("sto") {
		handle("hashed storages", migrateHashedStorages(ctx, rethDB, dstDB, *commitEvery, *limit, logger))
	}
	if run("tacc") {
		handle("accounts trie", migrateAccountsTrie(ctx, rethDB, dstDB, *commitEvery, *limit, logger))
	}
	if run("tsto") {
		handle("storages trie", migrateStoragesTrie(ctx, rethDB, dstDB, *commitEvery, *limit, logger))
	}
	if run("code") {
		handle("bytecodes", migrateBytecodes(ctx, rethDB, dstDB, *commitEvery, *limit, logger))
	}
	if run("rtrie") {
		if err := rebuildTrieFromLeaves(dstDB, *expectRoot, *tmpdir, *accBufGB, *stoBufGB, logger); err != nil {
			fail("rebuild trie from leaves", err)
		}
	}
	if run("vtrie") {
		if err := verifyVerbatimTrie(dstDB, *expectRoot, logger); err != nil {
			fail("verify verbatim trie", err)
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
