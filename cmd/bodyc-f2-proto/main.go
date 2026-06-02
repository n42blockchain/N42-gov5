// bodyc-f2-proto: stage-1 prototype for the F2 body-compression tier.
//
// For each 8192-block segment in a range it builds TWO combined column buffers
// from the same decoded blocks and zstd-compresses each, to measure the real
// end-to-end ratio under identical encoding:
//
//	L  : current-style — sig(R+S+V 65B) + to(20B) + (shared columns)
//	F2 : drop sig, add from-ID varint + to-ID varint + (shared columns)
//
// The shared columns (type, nonce, gas, value, caps, calldata, accessList) are
// byte-identical in both, so the zstd delta isolates exactly the sig→from-ID and
// to-20B→to-ID swaps. It then does an encode→bytes→decode round-trip on the
// from-ID column and checks the decoded sender matches the ecrecover ground
// truth for every tx — validating the From-without-signature path.
package main

import (
	"encoding/binary"
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

func zlen(b []byte) int { return len(zenc.EncodeAll(b, nil)) }

func putVar(buf []byte, v uint64) []byte {
	for v >= 0x80 {
		buf = append(buf, byte(v)|0x80)
		v >>= 7
	}
	return append(buf, byte(v))
}

func encTrim(buf []byte, v *big.Int) []byte {
	if v == nil || v.Sign() == 0 {
		return append(buf, 0)
	}
	b := v.Bytes()
	return append(append(buf, byte(len(b))), b...)
}

// globalDict assigns first-seen IDs to addresses across the whole range.
type globalDict struct {
	id   map[types.Address]uint64
	list []types.Address
}

func newDict() *globalDict { return &globalDict{id: map[types.Address]uint64{}} }
func (d *globalDict) intern(a types.Address) uint64 {
	if x, ok := d.id[a]; ok {
		return x
	}
	x := uint64(len(d.list))
	d.id[a] = x
	d.list = append(d.list, a)
	return x
}

func main() {
	dir := flag.String("dir", "", "bodyc freezer dir")
	start := flag.Uint64("start", 0, "start block")
	count := flag.Uint64("count", 8192, "blocks to scan (rounded into 8192 segments)")
	flag.Parse()
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "usage: bodyc-f2-proto --dir <freezer> --start N --count M")
		os.Exit(1)
	}
	br, err := ethel.OpenBodyCompact(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer br.Close()

	const segSize = 8192
	fromDict, toDict := newDict(), newDict()

	var (
		nTx                       int64
		zL, zF2                   int64 // summed per-segment zstd sizes
		rtChecked, rtMismatch     int64
	)

	end := *start + *count
	for segStart := *start; segStart < end; segStart += segSize {
		segEnd := segStart + segSize
		if segEnd > end {
			segEnd = end
		}

		// Per-segment column buffers.
		var (
			colType                                              []byte
			colSig                                               []byte // L: R+S+V
			colFromID                                            []byte // F2: sender dict id
			colToRaw                                             []byte // L: 20B
			colToID                                              []byte // F2: to dict id
			colCreate                                            []byte // bitpack-ish: 1B per tx (proto simplicity)
			colNonce, colGas                                     []byte
			colValue, colCap, colTip                             []byte
			colCalldata                                          []byte
			colAccess                                            []byte
			segFrom                                              []types.Address // ground-truth sender per tx (scan order)
			segFromIDs                                           []uint64        // the ids we wrote, for round-trip check
		)

		for n := segStart; n < segEnd; n++ {
			body, err := br.ReadBody(n)
			if err != nil {
				fmt.Fprintf(os.Stderr, "stop at %d: %v\n", n, err)
				end = n
				break
			}
			signer := transaction.MakeSignerWithTimestamp(params.EthereumMainnetChainConfig, big.NewInt(int64(n)), 0)
			for _, tx := range body.Txs {
				nTx++
				colType = append(colType, tx.Type())

				// L: signature.
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

				// F2: from-ID (sender via ecrecover ground truth → global dict).
				from, _ := transaction.Sender(signer, tx)
				fid := fromDict.intern(from)
				colFromID = putVar(colFromID, fid)
				segFrom = append(segFrom, from)
				segFromIDs = append(segFromIDs, fid)

				// to: L raw vs F2 id.
				if tx.To() == nil {
					colCreate = append(colCreate, 1)
				} else {
					colCreate = append(colCreate, 0)
					colToRaw = append(colToRaw, tx.To()[:]...)
					colToID = putVar(colToID, toDict.intern(*tx.To()))
				}

				colNonce = putVar(colNonce, tx.Nonce())
				colGas = putVar(colGas, tx.Gas())
				colValue = encTrim(colValue, tx.Value().ToBig())
				colCap = encTrim(colCap, tx.GasFeeCap().ToBig())
				if tx.Type() >= transaction.DynamicFeeTxType {
					colTip = encTrim(colTip, tx.GasTipCap().ToBig())
				}
				d := tx.Data()
				colCalldata = putVar(colCalldata, uint64(len(d)))
				colCalldata = append(colCalldata, d...)
				for _, t := range tx.AccessList() {
					colAccess = append(colAccess, t.Address[:]...)
					for _, k := range t.StorageKeys {
						colAccess = append(colAccess, k[:]...)
					}
				}
			}
		}

		// Shared columns (identical in both layouts).
		shared := func() []byte {
			var b []byte
			b = append(b, colType...)
			b = append(b, colCreate...)
			b = append(b, colNonce...)
			b = append(b, colGas...)
			b = append(b, colValue...)
			b = append(b, colCap...)
			b = append(b, colTip...)
			b = append(b, colCalldata...)
			b = append(b, colAccess...)
			return b
		}
		sh := shared()

		// L buffer: sig + to-raw + shared.
		bufL := make([]byte, 0, len(colSig)+len(colToRaw)+len(sh))
		bufL = append(bufL, colSig...)
		bufL = append(bufL, colToRaw...)
		bufL = append(bufL, sh...)

		// F2 buffer: from-ID + to-ID + shared.
		bufF2 := make([]byte, 0, len(colFromID)+len(colToID)+len(sh))
		bufF2 = append(bufF2, colFromID...)
		bufF2 = append(bufF2, colToID...)
		bufF2 = append(bufF2, sh...)

		zL += int64(zlen(bufL))
		zF2 += int64(zlen(bufF2))

		// Round-trip: decode the from-ID column bytes and verify dict[id] == ecrecover.
		pos := 0
		for i := range segFrom {
			id, nn := binary.Uvarint(colFromID[pos:])
			pos += nn
			rtChecked++
			if id != segFromIDs[i] || fromDict.list[id] != segFrom[i] {
				rtMismatch++
			}
		}
	}

	per := func(z int64) float64 { return float64(z) / float64(max1(nTx)) }
	fromSidecar := len(fromDict.list) * 20
	toSidecar := len(toDict.list) * 20

	fmt.Printf("range %d..%d  txs=%d\n", *start, end, nTx)
	fmt.Println("=== end-to-end combined-buffer zstd (per segment, summed) ===")
	fmt.Printf("  L  (sig + to-20B + shared) : %12d B  %7.2f B/tx\n", zL, per(zL))
	fmt.Printf("  F2 (from-ID + to-ID + shared): %10d B  %7.2f B/tx\n", zF2, per(zF2))
	fmt.Printf("  reduction: %.1f%%  (%.2f → %.2f B/tx)\n", 100*(1-float64(zF2)/float64(max1(zL))), per(zL), per(zF2))
	fmt.Println("=== from/to dict sidecars (store-wide, amortized) ===")
	fmt.Printf("  from dict: %d unique senders × 20B = %d B  (%.3f B/tx amortized)\n", len(fromDict.list), fromSidecar, float64(fromSidecar)/float64(max1(nTx)))
	fmt.Printf("  to   dict: %d unique addrs   × 20B = %d B  (%.3f B/tx amortized)\n", len(toDict.list), toSidecar, float64(toSidecar)/float64(max1(nTx)))
	fmt.Println("=== from-ID round-trip vs ecrecover ===")
	fmt.Printf("  checked=%d  mismatch=%d  → %s\n", rtChecked, rtMismatch,
		map[bool]string{true: "PASS (every From recovered from stored ID)", false: "FAIL"}[rtMismatch == 0])
}

func max1(v int64) int64 {
	if v < 1 {
		return 1
	}
	return v
}
