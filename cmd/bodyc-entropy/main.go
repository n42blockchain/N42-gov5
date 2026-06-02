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

	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/params"
)

var zenc, _ = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))

// zlen returns the zstd-compressed size of b (the per-column on-disk footprint).
func zlen(b []byte) int { return len(zenc.EncodeAll(b, nil)) }

func appendVar(buf []byte, v uint64) []byte {
	for v >= 0x80 {
		buf = append(buf, byte(v)|0x80)
		v >>= 7
	}
	return append(buf, byte(v))
}

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

var big10 = big.NewInt(10)

// sciU256Len: encode v as mantissa×10^exp (scientific/financial notation, à la
// Vitalik rollup compression). Layout: [ctrl][mantissa-trimmed]. ctrl high bit =
// sci flag, low 7 bits = exp. mantissa = v with trailing decimal zeros stripped.
// Falls back to plain trimmed (exp=0) when sci isn't smaller. Returns min bytes.
func sciU256Len(v *big.Int) int {
	if v == nil || v.Sign() == 0 {
		return 1
	}
	plain := 1 + len(v.Bytes()) // trimmed fallback (ctrl with exp=0 + bytes)
	m := new(big.Int).Set(v)
	exp := 0
	q := new(big.Int)
	r := new(big.Int)
	for exp < 127 {
		q.QuoRem(m, big10, r)
		if r.Sign() != 0 {
			break
		}
		m.Set(q)
		exp++
	}
	sci := 1 + len(m.Bytes()) // ctrl(exp in low 7 bits) + mantissa trimmed
	if sci < plain {
		return sci
	}
	return plain
}

func main() {
	dir := flag.String("dir", "", "bodyc freezer dir")
	start := flag.Uint64("start", 0, "start block")
	count := flag.Uint64("count", 100000, "blocks to scan")
	senderSample := flag.Int("sendersample", 200000, "max txs to ecrecover for sender cardinality (0=skip)")
	zstdCols := flag.Bool("zstd", false, "accumulate per-column buffers and report POST-ZSTD footprint per field (memory-heavy; use small --count)")
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
		szValueSci                                                     int64
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

	// Per-column raw buffers (only filled when --zstd) to measure POST-ZSTD footprint.
	var colSig, colTo, colValue, colGasCap, colTip, colGas, colNonce, colCalldata, colAccess []byte
	// Two-pass sender dictionary for the F2 from-ID column: pass-1 collects senders
	// (txID order) here, then we assign global dict IDs and emit the varint column.
	var fromOrder []types.Address // sender per tx, in scan order (only when --zstd)
	var toOrder []types.Address   // to per non-create tx, in scan order (only when --zstd)

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
			szValueSci += int64(sciU256Len(tx.Value().ToBig()))
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

			if *zstdCols {
				v, r, s := tx.RawSignatureValues()
				var rB, sB [32]byte
				if r != nil {
					rB = r.Bytes32()
				}
				if s != nil {
					sB = s.Bytes32()
				}
				colSig = append(colSig, rB[:]...)
				colSig = append(colSig, sB[:]...)
				if v != nil {
					colSig = append(colSig, byte(v.Uint64()))
				} else {
					colSig = append(colSig, 0)
				}
				if tx.To() != nil {
					colTo = append(colTo, tx.To()[:]...)
					toOrder = append(toOrder, *tx.To())
				}
				colValue = encVarBytes(colValue, tx.Value().ToBig())
				colGasCap = encVarBytes(colGasCap, tx.GasFeeCap().ToBig())
				if tx.Type() >= transaction.DynamicFeeTxType {
					colTip = encVarBytes(colTip, tx.GasTipCap().ToBig())
				}
				colGas = appendVar(colGas, tx.Gas())
				colNonce = appendVar(colNonce, tx.Nonce())
				colCalldata = appendVar(colCalldata, uint64(len(d)))
				colCalldata = append(colCalldata, d...)
				for _, t := range tx.AccessList() {
					colAccess = append(colAccess, t.Address[:]...)
					for _, k := range t.StorageKeys {
						colAccess = append(colAccess, k[:]...)
					}
				}
				// Sender for the from-ID column (ecrecover every tx in --zstd mode).
				signer := transaction.MakeSignerWithTimestamp(params.EthereumMainnetChainConfig, big.NewInt(int64(n)), 0)
				from, _ := transaction.Sender(signer, tx)
				fromOrder = append(fromOrder, from)
				senderSeen[from]++
				continue
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
	fmt.Printf("  %-12s %14d B  (sci/财务计数法: %.2f B/tx vs trimmed %.2f → −%.1f%% on value col)\n",
		"value-sci", szValueSci, float64(szValueSci)/float64(max1(nTx)), float64(szValue)/float64(max1(nTx)),
		100*(1-float64(szValueSci)/float64(max1(szValue))))
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

	if *zstdCols {
		reportPostZstd(nTx, colSig, colTo, colValue, colGasCap, colTip, colGas, colNonce, colCalldata, colAccess, fromOrder, toOrder)
	}
}

// reportPostZstd compresses each column independently and reports the real
// on-disk (post-zstd) footprint per field — the map of what's actually left to
// attack after zstd has already crushed calldata's zero-padding.
func reportPostZstd(nTx int64, colSig, colTo, colValue, colGasCap, colTip, colGas, colNonce, colCalldata, colAccess []byte, fromOrder, toOrder []types.Address) {
	per := func(z int) float64 { return float64(z) / float64(max1(nTx)) }
	zSig := zlen(colSig)
	zTo := zlen(colTo)
	zVal := zlen(colValue)
	zCap := zlen(colGasCap)
	zTip := zlen(colTip)
	zGas := zlen(colGas)
	zNon := zlen(colNonce)
	zCd := zlen(colCalldata)
	zAcc := zlen(colAccess)
	diskTotal := zSig + zTo + zVal + zCap + zTip + zGas + zNon + zCd + zAcc

	fmt.Println("=== POST-ZSTD per-column footprint (the real on-disk map) ===")
	pz := func(name string, z int) {
		fmt.Printf("  %-12s %12d B  %6.1f%%  %7.2f B/tx\n", name, z, 100*float64(z)/float64(max1(int64(diskTotal))), per(z))
	}
	pz("sig(R+S+V)", zSig)
	pz("calldata", zCd)
	pz("to(20B)", zTo)
	pz("accessList", zAcc)
	pz("gasFeeCap", zCap)
	pz("gasTipCap", zTip)
	pz("value", zVal)
	pz("gas", zGas)
	pz("nonce", zNon)
	fmt.Printf("  %-12s %12d B  100.0%%  %7.2f B/tx  (sum of per-column zstd)\n", "DISK-SUM", diskTotal, per(diskTotal))

	// F2: replace sig column with a from-ID column; replace to-20B with a to-ID column.
	fromCol, fromDict, fromUniq := buildDictColumn(fromOrder)
	toCol, toDict, toUniq := buildDictColumn(toOrder)
	zFrom := zlen(fromCol)
	zToID := zlen(toCol)
	fmt.Println("=== F2 column swaps (post-zstd) ===")
	fmt.Printf("  from-ID col : %7.2f B/tx (zstd of %d varint IDs; %d unique senders, dict=%d B raw)\n",
		per(zFrom), len(fromOrder), fromUniq, fromDict)
	fmt.Printf("  to-ID col   : %7.2f B/tx (zstd of %d varint IDs; %d unique, dict=%d B raw) vs to-20B %7.2f B/tx\n",
		per(zToID), len(toOrder), toUniq, toDict, per(zTo))
	f2Disk := diskTotal - zSig - zTo + zFrom + zToID + zlen([]byte(nil)) // sig→from, to→toID
	// dict sidecars (shared store-wide; amortized — shown separately)
	fmt.Printf("  F2 per-block-data DISK-SUM: %.2f B/tx (was %.2f) → %.1f%% smaller; + shared from/to dict sidecars (~%d B raw, one-time)\n",
		per(f2Disk), per(diskTotal), 100*(1-float64(f2Disk)/float64(max1(int64(diskTotal)))), fromDict+toDict)
	fmt.Printf("  F1 (keep 32B hash): add ~32 B/tx incompressible hash back → F2 + %.2f B/tx\n", 32.0)
}

// encVarBytes mirrors encodeTrimmedU256: 1 length byte + trimmed big-endian bytes.
func encVarBytes(buf []byte, v *big.Int) []byte {
	if v == nil || v.Sign() == 0 {
		return append(buf, 0)
	}
	b := v.Bytes()
	return append(append(buf, byte(len(b))), b...)
}

// buildDictColumn assigns a global dict ID (first-seen order) to each address and
// emits a varint-ID column; returns (idColumnBytes, dictRawBytes, uniqueCount).
func buildDictColumn(order []types.Address) (idCol []byte, dictBytes int, uniq int) {
	id := map[types.Address]uint64{}
	for _, a := range order {
		x, ok := id[a]
		if !ok {
			x = uint64(len(id))
			id[a] = x
		}
		idCol = appendVar(idCol, x)
	}
	return idCol, len(id) * 20, len(id)
}

func max1(v int64) int64 {
	if v < 1 {
		return 1
	}
	return v
}
