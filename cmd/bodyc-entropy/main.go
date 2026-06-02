// bodyc-entropy: scan a range of post-merge bodies and report the per-field
// byte breakdown + address/sender cardinality, to ground a body-compression
// redesign. Reports:
//   - per-field totals (sig R/S/V, to, value, gas, nonce, calldata, ...)
//   - To-address cardinality: unique vs total, singletons (used exactly once)
//   - sender cardinality via ecrecover (sampled), singletons
//   - projected sizes under candidate schemes
package main

import (
	"flag"
	"fmt"
	"math/big"
	"os"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/params"
)

func varintLen(v uint64) int {
	n := 1
	for v >= 0x80 {
		v >>= 7
		n++
	}
	return n
}

func trimU256Len(v *big.Int) int {
	if v == nil || v.Sign() == 0 {
		return 1
	}
	return 1 + len(v.Bytes())
}

func main() {
	dir := flag.String("dir", "", "bodyc freezer dir")
	start := flag.Uint64("start", 0, "start block")
	count := flag.Uint64("count", 100000, "blocks to scan")
	senderSample := flag.Int("sendersample", 200000, "max txs to ecrecover for sender cardinality (0=skip)")
	flag.Parse()
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "usage: bodyc-entropy --dir <freezer> --start N --count M")
		os.Exit(1)
	}
	br, err := ethel.OpenBodyCompact(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer br.Close()

	var (
		nTx                                                            int64
		szType, szSig, szTo, szValue, szGas, szNonce, szGasCap, szTip  int64
		szCalldata, szAccessList, szOther                              int64
		toSeen                                                         = map[types.Address]int{}
		senderSeen                                                     = map[types.Address]int{}
		senderRecovered                                                int64
		nCreate                                                        int64
		emptyCalldata                                                  int64
		cdZeroBytes                                                    int64 // zero bytes inside calldata
		cdWordBytes                                                    int64 // bytes in the word-aligned ABI args (len-4, when ≥4 and (len-4)%32==0)
		selSeen                                                        = map[[4]byte]int{}
		wordSeen                                                       = map[[32]byte]int{}
	)

	end := *start + *count
	for n := *start; n < end; n++ {
		body, err := br.ReadBody(n)
		if err != nil {
			fmt.Fprintf(os.Stderr, "stop at %d: %v\n", n, err)
			break
		}
		for _, tx := range body.Txs {
			nTx++
			szType++
			szSig += 65 // R(32)+S(32)+V(~1 bit, count as ~1B amortized for raw)
			if tx.To() == nil {
				nCreate++
			} else {
				szTo += 20
				toSeen[*tx.To()]++
			}
			szValue += int64(trimU256Len(tx.Value().ToBig()))
			szGasCap += int64(trimU256Len(tx.GasFeeCap().ToBig()))
			if tx.Type() >= transaction.DynamicFeeTxType {
				szTip += int64(trimU256Len(tx.GasTipCap().ToBig()))
			}
			szGas += int64(varintLen(tx.Gas()))
			szNonce += int64(varintLen(tx.Nonce()))
			d := tx.Data()
			szCalldata += int64(varintLen(uint64(len(d))) + len(d))
			if len(d) == 0 {
				emptyCalldata++
			}
			for _, bb := range d {
				if bb == 0 {
					cdZeroBytes++
				}
			}
			if len(d) >= 4 {
				var sel [4]byte
				copy(sel[:], d[:4])
				selSeen[sel]++
				args := d[4:]
				if len(args)%32 == 0 {
					cdWordBytes += int64(len(args))
					for i := 0; i+32 <= len(args); i += 32 {
						var w [32]byte
						copy(w[:], args[i:i+32])
						wordSeen[w]++
					}
				}
			}
			if al := tx.AccessList(); len(al) > 0 {
				for _, t := range al {
					szAccessList += int64(20 + len(t.StorageKeys)*32)
				}
			}

			if *senderSample > 0 && senderRecovered < int64(*senderSample) {
				signer := transaction.MakeSignerWithTimestamp(params.EthereumMainnetChainConfig, big.NewInt(int64(n)), 0)
				if from, err := transaction.Sender(signer, tx); err == nil {
					senderSeen[from]++
					senderRecovered++
				}
			}
		}
	}

	// To cardinality.
	toSingle := 0
	for _, c := range toSeen {
		if c == 1 {
			toSingle++
		}
	}
	sndSingle := 0
	for _, c := range senderSeen {
		if c == 1 {
			sndSingle++
		}
	}

	total := szType + szSig + szTo + szValue + szGas + szNonce + szGasCap + szTip + szCalldata + szAccessList + szOther
	pf := func(name string, v int64) {
		per := 0.0
		if nTx > 0 {
			per = float64(v) / float64(nTx)
		}
		fmt.Printf("  %-12s %14d B  %6.1f%%  %7.2f B/tx\n", name, v, 100*float64(v)/float64(total), per)
	}
	fmt.Printf("scanned blocks %d..%d, txs=%d (creates=%d, empty-calldata=%d)\n", *start, end-1, nTx, nCreate, emptyCalldata)
	fmt.Println("=== raw field bytes (pre-zstd) ===")
	pf("type", szType)
	pf("sig(R+S+V)", szSig)
	pf("to(20B)", szTo)
	pf("value", szValue)
	pf("gasFeeCap", szGasCap)
	pf("gasTipCap", szTip)
	pf("gas", szGas)
	pf("nonce", szNonce)
	pf("calldata", szCalldata)
	pf("accessList", szAccessList)
	fmt.Printf("  %-12s %14d B  100.0%%  %7.2f B/tx\n", "TOTAL", total, float64(total)/float64(max1(nTx)))

	fmt.Println("=== address/sender cardinality ===")
	fmt.Printf("  To: unique=%d / total-noncreate=%d   singletons(used once)=%d (%.1f%%)\n",
		len(toSeen), nTx-nCreate, toSingle, 100*float64(toSingle)/float64(max1(int64(len(toSeen)))))
	fmt.Printf("  Sender(sampled %d): unique=%d   singletons=%d (%.1f%%)\n",
		senderRecovered, len(senderSeen), sndSingle, 100*float64(sndSingle)/float64(max1(int64(len(senderSeen)))))

	// Projection: dict-ID scheme. ID width = varint over unique count.
	toIDW := varintLen(uint64(len(toSeen)))
	fmt.Println("=== projected per-tx under candidate schemes (raw, pre-zstd) ===")
	// Scheme A current: sig65 + to20 + value + gascap + tip + gas + nonce + type + calldata + al
	curPerTx := float64(total) / float64(max1(nTx))
	fmt.Printf("  A current        : %.2f B/tx  (sig kept, to=20B raw-ish, hash recomputed, sender=ecrecover)\n", curPerTx)
	// Scheme B1: drop sig, store hash(32) + from-ID + to-ID(varint)
	b1 := curPerTx - 65 + 32 + float64(toIDW) /*from-id*/ + float64(toIDW) - 20 /*to becomes id*/
	fmt.Printf("  B1 drop-sig+hash : %.2f B/tx  (−sig65 +hash32 +fromID%d +toID%d −to20) keeps getTxByHash, loses sig fields\n", b1, toIDW, toIDW)
	// Scheme B2: drop sig AND hash, store from-ID + to-ID
	b2 := curPerTx - 65 + float64(toIDW) + float64(toIDW) - 20
	fmt.Printf("  B2 drop-sig-hash : %.2f B/tx  (−sig65 +fromID%d +toID%d −to20) loses getTxByHash + canonical hash\n", b2, toIDW, toIDW)
	fmt.Printf("  (To dict: %d unique addrs → %d-byte varint IDs; %d singletons cost 20B each in the dict regardless)\n",
		len(toSeen), toIDW, toSingle)

	// Calldata structure — the dominant cost.
	wordSingle := 0
	for _, c := range wordSeen {
		if c == 1 {
			wordSingle++
		}
	}
	totalCd := int64(0)
	for _, bb := range []int64{szCalldata} {
		totalCd += bb
	}
	fmt.Println("=== calldata structure (the 71% bucket) ===")
	fmt.Printf("  zero bytes: %d (%.1f%% of calldata) — leading-zero pad in ABI words is the biggest single lever\n",
		cdZeroBytes, 100*float64(cdZeroBytes)/float64(max1(szCalldata)))
	fmt.Printf("  word-aligned ABI arg bytes: %d (%.1f%% of calldata)\n", cdWordBytes, 100*float64(cdWordBytes)/float64(max1(szCalldata)))
	fmt.Printf("  selectors: %d unique 4-byte selectors (dict ≪ 4B/tx)\n", len(selSeen))
	fmt.Printf("  32B words: %d unique / singletons=%d (%.1f%%) — singletons are irreducible entropy\n",
		len(wordSeen), wordSingle, 100*float64(wordSingle)/float64(max1(int64(len(wordSeen)))))
}

func max1(v int64) int64 {
	if v < 1 {
		return 1
	}
	return v
}
