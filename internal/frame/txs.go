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

// HashOf returns keccak256(header || payload) of an encoded frame without a
// full decode. It only checks the bounds needed to locate the signature.
func HashOf(raw []byte) (types.Hash, error) {
	h, err := ParseHeader(raw)
	if err != nil {
		return types.Hash{}, err
	}
	if len(raw) != h.Size() {
		return types.Hash{}, ErrShort
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
	return decodeTxsInto(raw, nil)
}

// decodeTxsInto is DecodeTxs appending to dst (nil: a slice of the frame's
// capacity is made).
func decodeTxsInto(raw []byte, dst []*transaction.Transaction) ([]*transaction.Transaction, int, error) {
	var f Frame
	if err := decodeEnvelope(raw, &f); err != nil {
		return nil, 0, err
	}
	var dec *transaction.EthBatchDecoder
	if f.TxType == TxTypeEthereum && TxDecodePoolEnabled() {
		dec = transaction.NewEthBatchDecoder(int(f.TxCount))
	}
	if dst == nil {
		dst = make([]*transaction.Transaction, 0, f.TxCount)
	}
	return decodeFrameInto(&f, dec, dst)
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
	return decodeFramesEach(frames, workers, nil, after)
}

// EnvIngestRecycle is N42_INGEST_RECYCLE: a block's frames decode straight
// into the block's one transaction slice (DecodeFramesFlatWith) instead of
// into a slice per frame that is copied into the block's and dropped.
// Default on; "0" decodes per frame and concatenates, as before.
const EnvIngestRecycle = "N42_INGEST_RECYCLE"

var ingestRecycleOn atomic.Bool

func init() { ingestRecycleOn.Store(os.Getenv(EnvIngestRecycle) != "0") }

// IngestRecycleEnabled reports N42_INGEST_RECYCLE.
func IngestRecycleEnabled() bool { return ingestRecycleOn.Load() }

// SetIngestRecycleForTest switches N42_INGEST_RECYCLE and returns the
// restore.
func SetIngestRecycleForTest(on bool) func() {
	prev := ingestRecycleOn.Swap(on)
	return func() { ingestRecycleOn.Store(prev) }
}

// DecodeFramesFlatWith is DecodeFramesEachWith that also returns the
// frames' transactions concatenated in frame order (flat), without a copy:
// every frame decodes into its own window of one slice sized from the
// frame headers, and per[i] is that window. flat is nil when some frame
// dropped an undecodable entry (its window has a hole); the caller then
// concatenates per as DecodeFramesEachWith's callers do. Nothing writes
// into flat or per after the call; per[i] is capped at its window, so an
// append to it cannot reach the next frame's transactions.
func DecodeFramesFlatWith(frames [][]byte, workers int, after func(i int, txs []*transaction.Transaction)) (flat []*transaction.Transaction, per [][]*transaction.Transaction, dropped int, err error) {
	offs := make([]int, len(frames)+1)
	for i, raw := range frames {
		h, herr := ParseHeader(raw)
		if herr != nil {
			// The decode reports the error for the lowest bad frame.
			per, dropped, err = DecodeFramesEachWith(frames, workers, after)
			return nil, per, dropped, err
		}
		offs[i+1] = offs[i] + int(h.TxCount)
	}
	all := make([]*transaction.Transaction, offs[len(frames)])
	per, dropped, err = decodeFramesEach(frames, workers, func(i int) []*transaction.Transaction {
		return all[offs[i]:offs[i]:offs[i+1]]
	}, after)
	if err != nil {
		return nil, nil, 0, err
	}
	if dropped != 0 {
		return nil, per, dropped, nil
	}
	for i, p := range per {
		if len(p) != offs[i+1]-offs[i] || (len(p) > 0 && &p[0] != &all[offs[i]]) {
			return nil, per, dropped, nil
		}
	}
	return all, per, dropped, nil
}

// decodeFramesEach decodes frames across workers; into(i), when non-nil,
// is the zero-length slice frame i's transactions are appended to.
func decodeFramesEach(frames [][]byte, workers int, into func(i int) []*transaction.Transaction, after func(i int, txs []*transaction.Transaction)) ([][]*transaction.Transaction, int, error) {
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
		var dst []*transaction.Transaction
		if into != nil {
			dst = into(i)
		}
		per[i], drops[i], errs[i] = decodeTxsInto(frames[i], dst)
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
