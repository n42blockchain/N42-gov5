// One-shot probe: dump a single address's account state from both
// N42 and reth, side-by-side. Used to confirm whether N42's chaindata
// is in sync with mainnet at the supposed head, by spot-checking the
// EVM-reported "insufficient funds" address from the eth-el devp2p
// sync test.

package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"os"

	"github.com/c2h5oh/datasize"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

func n42Cfg(d kv.TableCfg) kv.TableCfg {
	d["Account"] = kv.TableCfgItem{}
	d["Storage"] = kv.TableCfgItem{Flags: kv.DupSort}
	return d
}

func rethCfg(d kv.TableCfg) kv.TableCfg {
	d["PlainAccountState"] = kv.TableCfgItem{}
	return d
}

// decodeRethAccount mirrors cmd/acct-diff-vs-reth.
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
	addr := flag.String("addr", "0x75193c3235b386DD7097E113b266633440a7da91", "address to probe")
	n42Dir := flag.String("n42", `D:/N42-eth1177-test/chaindata`, "n42 chaindata path")
	rethDir := flag.String("reth", `D:/reth2k/db`, "reth db path")
	flag.Parse()

	if len(*addr) >= 2 && (*addr)[:2] == "0x" {
		*addr = (*addr)[2:]
	}
	addrBytes := types.HexToAddress("0x" + *addr).Bytes()

	logger := log.New()
	n42DB, err := mdbx.NewMDBX(logger).Path(*n42Dir).Label(kv.ChainDB).
		PageSize(4096).MapSize(4 * datasize.TB).Readonly().Accede().
		WithTableCfg(n42Cfg).Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open n42:", err)
		os.Exit(1)
	}
	defer n42DB.Close()

	rethDB, err := mdbx.NewMDBX(logger).Path(*rethDir).Label(kv.ChainDB).
		PageSize(4096).MapSize(4 * datasize.TB).Readonly().Accede().
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

	fmt.Printf("addr = 0x%x\n\n", addrBytes)

	// N42 side
	v, _ := n42Tx.GetOne("Account", addrBytes)
	if v == nil {
		fmt.Println("N42  : ABSENT")
	} else {
		var a account.StateAccount
		if err := a.DecodeForStorage(v); err != nil {
			fmt.Printf("N42  : decode error %v (raw=%x)\n", err, v)
		} else {
			fmt.Printf("N42  : nonce=%d balance=%s codeHash=%x\n",
				a.Nonce, a.Balance.String(), a.CodeHash[:8])
		}
	}

	// reth side
	rv, _ := rethTx.GetOne("PlainAccountState", addrBytes)
	if rv == nil {
		fmt.Println("reth : ABSENT")
	} else {
		nonce, bal, ch, ok := decodeRethAccount(rv)
		if !ok {
			fmt.Printf("reth : decode error rawLen=%d raw=%x\n", len(rv), rv)
		} else {
			fmt.Printf("reth : nonce=%d balance=%s codeHash=%x\n",
				nonce, bal.String(), ch[:8])
		}
	}
}
