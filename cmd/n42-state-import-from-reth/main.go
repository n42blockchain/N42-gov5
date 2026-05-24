// n42-state-import-from-reth replaces N42's live PlainState tables
// (Account, Storage, Code) with the verified-correct state from a
// reth MDBX at its HEAD. Used to recover from chaindata corruption
// (silent witness-replay writes, consensus drift, etc.) without
// re-running the slow catchup pipeline from genesis.
//
// Pipeline (each phase is independently restartable — set --skip-* to
// resume after a successful phase):
//
//   1. Clear N42 Account/Storage/Code tables (current state is junk).
//   2. Scan reth PlainAccountState → re-encode → N42 Account.
//   3. Scan reth PlainStorageState → N42 Storage (composite-key plain).
//   4. Scan reth Bytecodes → N42 Code (same key/value layout).
//   5. ClearBucket CommitmentBranches so HPH rebuilds from scratch.
//   6. Write ethel-last-block = reth's HEAD block (--head-block).
//
// CommitmentBranches needs a separate bootstrap pass after this tool
// finishes — run cmd/n42-commitment-bootstrap before retrying sync.
//
// Encoding:
//   - reth Account = compact (nonce_len:4|bal_len:6|hashFlag:1 || nonce|bal|hash)
//   - N42 Account  = StateAccount.MarshalV2 (Erigon-style varlen)
//   - Storage value: both sides store trimmed uint256 as raw bytes,
//     no transform needed.
//   - Bytecodes: both sides are codeHash → bytecode, no transform.

package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

const (
	n42Acct = "Account"
	n42Sto  = "Storage"
	n42Code = "Code"
	n42Cmt  = "CommitmentBranches"
	n42Prog = "SyncStageProgress"

	rethAcct = "PlainAccountState"
	rethSto  = "PlainStorageState"
	rethCode = "Bytecodes"
)

var emptyCodeHash = crypto.Keccak256Hash(nil)

func n42Cfg(d kv.TableCfg) kv.TableCfg {
	d[n42Acct] = kv.TableCfgItem{}
	// Storage flags must match lib/kv/tables.go ChaindataTablesCfg["Storage"]
	// or the cursor/Put semantics in this tool's tx will diverge from what
	// the rest of the codebase expects on the same MDBX file.
	d[n42Sto] = kv.TableCfgItem{
		Flags:                     kv.DupSort,
		AutoDupSortKeysConversion: true,
		DupFromLen:                52,
		DupToLen:                  20,
	}
	d[n42Code] = kv.TableCfgItem{}
	d[n42Cmt] = kv.TableCfgItem{}
	d[n42Prog] = kv.TableCfgItem{}
	return d
}

func rethCfg(d kv.TableCfg) kv.TableCfg {
	d[rethAcct] = kv.TableCfgItem{}
	d[rethSto] = kv.TableCfgItem{Flags: kv.DupSort}
	d[rethCode] = kv.TableCfgItem{}
	return d
}

// decodeRethAccount mirrors cmd/acct-diff-vs-reth + cmd/acct-probe.
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
	n42Dir := flag.String("n42", `D:/N42-eth1177-test/chaindata`, "n42 chaindata MDBX path (WILL BE MUTATED)")
	rethDir := flag.String("reth", `D:/reth2k/db`, "reth db path (read-only)")
	headBlock := flag.Uint64("head-block", 25096155, "reth HEAD block to record as ethel-last-block")
	doClear := flag.Bool("clear", true, "ClearBucket Account/Storage/Code before importing")
	skipAcct := flag.Bool("skip-account", false, "skip account phase")
	skipSto := flag.Bool("skip-storage", false, "skip storage phase")
	skipCodeFlag := flag.Bool("skip-code", false, "skip code phase")
	skipCmt := flag.Bool("skip-commitment-clear", false, "skip ClearBucket CommitmentBranches")
	skipHead := flag.Bool("skip-head-write", false, "skip writing ethel-last-block")
	commitEvery := flag.Uint64("commit-every", 1_000_000, "tx commit interval (entries per txn)")
	flag.Parse()

	logger := log.New()
	logger.Info("n42-state-import-from-reth starting",
		"n42", *n42Dir, "reth", *rethDir, "headBlock", *headBlock,
		"clear", *doClear, "commitEvery", *commitEvery)

	t0 := time.Now()

	n42DB, err := mdbx.NewMDBX(logger).
		Path(*n42Dir).Label(kv.ChainDB).PageSize(4096).
		MapSize(4 * datasize.TB).WriteMap().Accede().
		WithTableCfg(n42Cfg).Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open n42:", err)
		os.Exit(1)
	}
	defer n42DB.Close()

	rethDB, err := mdbx.NewMDBX(logger).
		Path(*rethDir).Label(kv.ChainDB).PageSize(4096).
		MapSize(4 * datasize.TB).Readonly().Accede().
		WithTableCfg(rethCfg).Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open reth:", err)
		os.Exit(1)
	}
	defer rethDB.Close()

	if !*skipAcct {
		if err := importAccounts(n42DB, rethDB, *doClear, *commitEvery, logger); err != nil {
			fmt.Fprintln(os.Stderr, "importAccounts:", err)
			os.Exit(1)
		}
	}
	if !*skipSto {
		if err := importStorage(n42DB, rethDB, *doClear, *commitEvery, logger); err != nil {
			fmt.Fprintln(os.Stderr, "importStorage:", err)
			os.Exit(1)
		}
	}
	if !*skipCodeFlag {
		if err := importCode(n42DB, rethDB, *doClear, *commitEvery, logger); err != nil {
			fmt.Fprintln(os.Stderr, "importCode:", err)
			os.Exit(1)
		}
	}
	if !*skipCmt {
		if err := clearCommitmentBranches(n42DB, logger); err != nil {
			fmt.Fprintln(os.Stderr, "clearCommitmentBranches:", err)
			os.Exit(1)
		}
	}
	if !*skipHead {
		if err := writeHeadPointer(n42DB, *headBlock, logger); err != nil {
			fmt.Fprintln(os.Stderr, "writeHeadPointer:", err)
			os.Exit(1)
		}
	}

	logger.Info("all phases complete", "elapsed", time.Since(t0))
	runtime.GC()
}
