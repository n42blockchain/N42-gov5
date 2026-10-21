// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package frame

import (
	"errors"
	"os"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/rlp"
)

// ErrHashMismatch: a frame's bytes do not hash to the FrameHash it was
// requested under.
var ErrHashMismatch = errors.New("frame: bytes do not match frame hash")

// HashOf returns the FrameHash of an encoded frame without a full decode.
// Under v1 (keccak256(header || payload)) it only checks the bounds needed
// to locate the signature; under v2 it walks the payload, whose layout it
// checks, to hash the entries.
func HashOf(raw []byte) (types.Hash, error) {
	h, err := ParseHeader(raw)
	if err != nil {
		return types.Hash{}, err
	}
	if len(raw) != h.Size() {
		return types.Hash{}, ErrShort
	}
	if HashV2Enabled() {
		hs, err := EntryHashesOf(raw)
		if err != nil {
			return types.Hash{}, err
		}
		return hashV2(raw, hs), nil
	}
	return crypto.Keccak256Hash(raw[:HeaderSize+int(h.PayloadLen)]), nil
}

// EnvTxDecodePool is N42_TX_DECODE_POOL: frame transactions decode into
// per-frame slabs with their encodings aliasing the frame (P14), and
// admission recovery reuses its decode objects. Default on; "0" decodes
// every transaction on its own, as before.
const EnvTxDecodePool = "N42_TX_DECODE_POOL"

var txDecodePoolOn atomic.Bool

func init() { txDecodePoolOn.Store(os.Getenv(EnvTxDecodePool) != "0") }

// TxDecodePoolEnabled reports N42_TX_DECODE_POOL.
func TxDecodePoolEnabled() bool { return txDecodePoolOn.Load() }

// SetTxDecodePoolForTest switches the slab decode and returns the restore.
func SetTxDecodePoolForTest(on bool) func() {
	prev := txDecodePoolOn.Swap(on)
	return func() { txDecodePoolOn.Store(prev) }
}

// decodeTx decodes one frame entry according to the frame's tx type. With
// dec (an Ethereum frame under N42_TX_DECODE_POOL) the transaction comes
// from dec's slab and its cached encoding aliases b, which frames allow:
// a frame's bytes are never modified once encoded.
func decodeTx(txType uint8, b []byte, dec *transaction.EthBatchDecoder) (*transaction.Transaction, error) {
	if txType == TxTypeNative {
		tx := new(transaction.Transaction)
		if err := tx.Unmarshal(b); err != nil {
			return nil, err
		}
		return tx, nil
	}
	if dec != nil {
		return dec.DecodeAliased(b)
	}
	return transaction.DecodeEthereumTransaction(b)
}

// DecodeTxs decodes the transactions of one encoded frame, in payload
// order. An entry that does not decode is dropped and counted: every node
// decodes the same bytes with the same codec, so the dropped set is the
// same everywhere and the block's transaction list stays deterministic.
// Only a frame whose layout is invalid is an error.
//
// The transactions may share one allocation and alias raw (see
// transaction.EthBatchDecoder): raw must not be modified afterwards.
func DecodeTxs(raw []byte) ([]*transaction.Transaction, int, error) {
	var f Frame
	if err := decodeEnvelope(raw, &f); err != nil {
		return nil, 0, err
	}
	var dec *transaction.EthBatchDecoder
	if f.TxType == TxTypeEthereum && TxDecodePoolEnabled() {
		dec = transaction.NewEthBatchDecoder(int(f.TxCount))
	}
	txs := make([]*transaction.Transaction, 0, f.TxCount)
	return decodeFrameInto(&f, dec, txs)
}

// decodeFrameInto decodes f's entries into txs while it checks the payload
// layout, so a frame's payload is walked once. A layout error returns nil
// transactions: whatever was decoded before it is discarded.
func decodeFrameInto(f *Frame, dec *transaction.EthBatchDecoder, txs []*transaction.Transaction) ([]*transaction.Transaction, int, error) {
	dropped := 0
	err := walkPayload(f.Payload, int(f.TxCount), func(_ int, b []byte) {
		tx, err := decodeTx(f.TxType, b, dec)
		if err != nil {
			dropped++
			return
		}
		txs = append(txs, tx)
	})
	if err != nil {
		return nil, 0, err
	}
	return txs, dropped, nil
}

// HashTxs memoises the hash of every transaction of txs (nil entries are
// skipped). Called on a decode worker right after the decode (see
// DecodeFramesEachWith) it hashes encodings and objects still in cache.
func HashTxs(txs []*transaction.Transaction) {
	// tx.Hash() on each, single-block encodings four at a time.
	transaction.HashBatch(txs)
}

// DecodeFrames decodes frames across up to workers goroutines (<= 0 means
// GOMAXPROCS) and returns their transactions concatenated in frame order,
// plus the number of undecodable entries dropped. The error is that of the
// lowest-index frame whose layout is invalid.
func DecodeFrames(frames [][]byte, workers int) ([]*transaction.Transaction, int, error) {
	per, dropped, err := DecodeFramesEach(frames, workers)
	if err != nil {
		return nil, 0, err
	}
	total := 0
	for _, p := range per {
		total += len(p)
	}
	out := make([]*transaction.Transaction, 0, total)
	for _, p := range per {
		out = append(out, p...)
	}
	return out, dropped, nil
}

// DecodeFramesEach is DecodeFrames keeping each frame's transactions apart.
func DecodeFramesEach(frames [][]byte, workers int) ([][]*transaction.Transaction, int, error) {
	return DecodeFramesEachWith(frames, workers, nil)
}

// DecodeFramesEachWith is DecodeFramesEach that hands each frame's
// transactions to after (when non-nil) on the worker that decoded them,
// right after the decode: per-transaction work done there (attaching cached
// senders, hashing) finds the transactions still in the worker's cache
// instead of walking the whole block again afterwards. after is called
// concurrently for different frames and only for frames whose layout is
// valid.
func DecodeFramesEachWith(frames [][]byte, workers int, after func(i int, txs []*transaction.Transaction)) ([][]*transaction.Transaction, int, error) {
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	if workers > len(frames) {
		workers = len(frames)
	}
	per := make([][]*transaction.Transaction, len(frames))
	drops := make([]int, len(frames))
	errs := make([]error, len(frames))
	one := func(i int) {
		per[i], drops[i], errs[i] = DecodeTxs(frames[i])
		if after != nil && errs[i] == nil {
			after(i, per[i])
		}
	}
	if workers <= 1 {
		for i := range frames {
			one(i)
		}
	} else {
		var wg sync.WaitGroup
		var next atomic.Int64
		n := int64(len(frames))
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					i := next.Add(1) - 1
					if i >= n {
						return
					}
					one(int(i))
				}
			}()
		}
		wg.Wait()
	}
	dropped := 0
	for i := range frames {
		if errs[i] != nil {
			return nil, 0, errs[i]
		}
		dropped += drops[i]
	}
	return per, dropped, nil
}

// GasEstimate is the sum of the gas limits of a frame's transactions,
// computed once at admission so a block builder can pack whole frames
// against the gas ceiling without decoding them. For Ethereum-encoded
// frames it walks each entry's RLP only as far as the gas field; anything
// it cannot walk (native codec, unknown type) is fully decoded. An entry
// that does not decode at all counts as zero, matching DecodeTxs, which
// drops it.
func GasEstimate(f *Frame) uint64 {
	var gas uint64
	_ = walkPayload(f.Payload, int(f.TxCount), func(_ int, b []byte) { gas += entryGas(f.TxType, b) })
	return gas
}

// entryGas is one entry's share of GasEstimate.
func entryGas(txType uint8, b []byte) uint64 {
	if txType == TxTypeEthereum {
		if g, ok := ethGasLimit(b); ok {
			return g
		}
	}
	if tx, err := decodeTx(txType, b, nil); err == nil {
		return tx.Gas()
	}
	return 0
}

// ethGasLimit reads the gas limit of an Ethereum-encoded transaction:
// legacy [nonce, gasPrice, gas, ...], 0x01 [chainId, nonce, gasPrice, gas,
// ...], 0x02/0x03/0x04 [chainId, nonce, tip, feeCap, gas, ...].
func ethGasLimit(b []byte) (uint64, bool) {
	if len(b) == 0 {
		return 0, false
	}
	idx := 2
	if b[0] < 0xc0 {
		switch b[0] {
		case 0x01:
			idx = 3
		case 0x02, 0x03, 0x04:
			idx = 4
		default:
			return 0, false
		}
		b = b[1:]
	}
	content, _, err := rlp.SplitList(b)
	if err != nil {
		return 0, false
	}
	for i := 0; i < idx; i++ {
		_, _, rest, err := rlp.Split(content)
		if err != nil {
			return 0, false
		}
		content = rest
	}
	g, _, err := rlp.SplitUint64(content)
	if err != nil {
		return 0, false
	}
	return g, true
}
