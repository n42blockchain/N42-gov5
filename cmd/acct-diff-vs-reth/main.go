// acct-diff-vs-reth walks the n42 Account table and for each (addr, n42_val)
// looks up reth's PlainAccountState[addr], decodes both to (nonce, balance,
// codeHash), and reports the first N differences.
//
// n42 storcs row count diff was already shown to be SELFDESTRUCT-context
// noise (account deleted on both sides → PlainState matches → no root impact).
// diff-cs only counts account rows, never compares values, so a per-(nonce,
// balance, codeHash) diff is the next blind spot to close before rolling
// another 4-hour rebuild-state + bisect.
//
// Reth Account compact layout (reverse-engineered from reth_codecs::Compact
// macro output; verified empirically against PlainAccountState samples):
//
//	[flags_lo:1B] [flags_hi:1B]
//	  bit 0-3: nonce_len (0-8)
//	  bit 4-9: balance_len (0-32)
//	  bit 10:  bytecode_hash present
//	[nonce nonce_len bytes BE]
//	[balance balance_len bytes BE]
//	[bytecode_hash 32B if flag set]
//
// If decode fails (length mismatch), the raw hex is logged so the user can
// patch the layout assumption.
package main

import (
	"bytes"
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
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

const (
	n42Acct  = "Account"
	rethAcct = "PlainAccountState"
)

func n42Cfg(d kv.TableCfg) kv.TableCfg {
	d[n42Acct] = kv.TableCfgItem{}
	return d
}
func rethCfg(d kv.TableCfg) kv.TableCfg {
	d[rethAcct] = kv.TableCfgItem{}
	return d
}

// decodeRethAccount parses reth's compact Account format. Returns ok=false
// when the byte length contradicts the flag bits (decoder needs patching).
func decodeRethAccount(v []byte) (nonce uint64, balance uint256.Int, codeHash types.Hash, ok bool) {
	if len(v) < 2 {
		// Empty account allocation (default) — encode as 2 zero flag bytes
		// in some reth versions, or simply nothing. Both decode to zeros.
		return 0, uint256.Int{}, types.Hash{}, len(v) == 0
	}
	flags := uint16(v[0]) | (uint16(v[1]) << 8)
	nonceLen := int(flags & 0x0f)
	balanceLen := int((flags >> 4) & 0x3f)
	hasCode := flags&(1<<10) != 0
	expected := 2 + nonceLen + balanceLen
	if hasCode {
		expected += 32
	}
	if len(v) != expected {
		return 0, uint256.Int{}, types.Hash{}, false
	}
	pos := 2
	for i := 0; i < nonceLen; i++ {
		nonce = (nonce << 8) | uint64(v[pos+i])
	}
	pos += nonceLen
	if balanceLen > 0 {
		balance.SetBytes(v[pos : pos+balanceLen])
		pos += balanceLen
	}
	if hasCode {
		copy(codeHash[:], v[pos:pos+32])
	}
	return nonce, balance, codeHash, true
}

func main() {
	n42Dir := flag.String("n42", `d:\rebuilt-state`, "n42 MDBX datadir (Account table)")
	rethDir := flag.String("reth", `d:\reth2k\db`, "reth MDBX path (PlainAccountState)")
	maxDiffs := flag.Int("max-diffs", 20, "stop after this many diffs")
	progress := flag.Duration("progress", 10*time.Second, "log progress interval")
	skipUnknownReth := flag.Bool("skip-unknown-reth", false, "ignore decode-failed reth values (log only)")
	flag.Parse()

	logger := log.New()
	n42DB, err := mdbx.NewMDBX(logger).
		Path(*n42Dir).Label(kv.ChainDB).PageSize(4096).
		MapSize(4 * datasize.TB).Readonly().Accede().
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

	n42Tx, _ := n42DB.BeginRo(context.Background())
	defer n42Tx.Rollback()
	rethTx, _ := rethDB.BeginRo(context.Background())
	defer rethTx.Rollback()

	n42Cur, err := n42Tx.Cursor(n42Acct)
	if err != nil {
		fmt.Fprintln(os.Stderr, "n42 cursor:", err)
		os.Exit(1)
	}
	defer n42Cur.Close()

	var (
		scanned    uint64
		diffs      int
		decodeFail int
		t0         = time.Now()
		lastLog    = t0
	)

	for k, v, err := n42Cur.First(); k != nil; k, v, err = n42Cur.Next() {
		if err != nil {
			fmt.Fprintln(os.Stderr, "iter:", err)
			break
		}
		if len(k) != 20 {
			continue
		}
		scanned++

		var n42Acc account.StateAccount
		if err := n42Acc.DecodeForStorage(v); err != nil {
			fmt.Printf("n42 decode error addr=%x: %v\n", k, err)
			continue
		}

		rv, err := rethTx.GetOne(rethAcct, k)
		if err != nil {
			fmt.Fprintln(os.Stderr, "reth get:", err)
			os.Exit(1)
		}
		if rv == nil {
			// n42 has addr, reth doesn't → MISSING in mainnet view = stale n42 entry
			fmt.Printf("[n42 EXTRA   ] addr=%x n42=(nonce=%d bal=%s codeHash=%x)\n",
				k, n42Acc.Nonce, n42Acc.Balance.Hex(), n42Acc.CodeHash[:])
			diffs++
			if diffs >= *maxDiffs {
				break
			}
			continue
		}

		rethNonce, rethBalance, rethCodeHash, ok := decodeRethAccount(rv)
		if !ok {
			decodeFail++
			if !*skipUnknownReth {
				fmt.Printf("reth decode FAIL addr=%x rawLen=%d raw=%x\n", k, len(rv), rv)
			}
			continue
		}

		// CodeHash empty vs explicit-emptyHash — N42 always stores emptyHash
		// for EOAs (DecodeForStorage substitutes when CodeHash is zero), so
		// normalize reth's "absent" case to emptyHash for comparison.
		rethCH := rethCodeHash
		if rethCH == (types.Hash{}) {
			copy(rethCH[:], emptyCodeHash[:])
		}
		n42CH := n42Acc.CodeHash
		if n42CH == (types.Hash{}) {
			copy(n42CH[:], emptyCodeHash[:])
		}

		nonceDiff := n42Acc.Nonce != rethNonce
		balDiff := n42Acc.Balance.Cmp(&rethBalance) != 0
		codeDiff := n42CH != rethCH
		if nonceDiff || balDiff || codeDiff {
			fmt.Printf("[VAL DIFF    ] addr=%x\n", k)
			fmt.Printf("  n42  nonce=%d bal=%s codeHash=%x\n",
				n42Acc.Nonce, n42Acc.Balance.Hex(), n42CH[:])
			fmt.Printf("  reth nonce=%d bal=%s codeHash=%x\n",
				rethNonce, rethBalance.Hex(), rethCH[:])
			diffs++
			if diffs >= *maxDiffs {
				break
			}
		}

		if time.Since(lastLog) > *progress {
			lastLog = time.Now()
			rate := float64(scanned) / time.Since(t0).Seconds()
			fmt.Fprintf(os.Stderr, "  scanned=%d diffs=%d decodeFail=%d rate=%.0f/s elapsed=%v\n",
				scanned, diffs, decodeFail, rate, time.Since(t0).Truncate(time.Second))
		}
	}

	fmt.Fprintf(os.Stderr, "\n=== done ===\nscanned=%d diffs=%d decodeFail=%d elapsed=%v\n",
		scanned, diffs, decodeFail, time.Since(t0).Truncate(time.Millisecond))
}

// emptyCodeHash is keccak256(nil).
var emptyCodeHash = [32]byte{
	0xc5, 0xd2, 0x46, 0x01, 0x86, 0xf7, 0x23, 0x3c,
	0x92, 0x7e, 0x7d, 0xb2, 0xdc, 0xc7, 0x03, 0xc0,
	0xe5, 0x00, 0xb6, 0x53, 0xca, 0x82, 0x27, 0x3b,
	0x7b, 0xfa, 0xd8, 0x04, 0x5d, 0x85, 0xa4, 0x70,
}

// avoid unused import errors from boilerplate
var _ = binary.LittleEndian
var _ = bytes.Equal
