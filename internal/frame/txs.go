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
	"math"
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

// EnvFrameDecodeSlab is N42_FRAME_DECODE_SLAB (default off): a block's
// frames decode, per decode worker, into one run of slabs
// (EthBatchDecoder.Continue) instead of fresh slabs per frame. A frame's
// slab is a large object (500 transactions: ~150-240 KB), allocated under
// the runtime heap lock; with 620 frames per block on 48 workers that lock
// was the node's largest mutex wait (r93, run 77). The worker's slabs hold
// only that block's transactions, which live and die together, so no slab
// is ever reused or released early: nothing can alias a live transaction.
// The cost is coarser retention: one transaction kept alive keeps its
// worker's slab (all of that worker's frames of the block) reachable.
const EnvFrameDecodeSlab = "N42_FRAME_DECODE_SLAB"

var frameDecodeSlabOn atomic.Bool

func init() {
	switch os.Getenv(EnvFrameDecodeSlab) {
	case "1", "true", "TRUE", "on", "ON", "yes":
		frameDecodeSlabOn.Store(true)
	}
}

// FrameDecodeSlabEnabled reports N42_FRAME_DECODE_SLAB.
func FrameDecodeSlabEnabled() bool { return frameDecodeSlabOn.Load() }

// SetFrameDecodeSlabForTest switches N42_FRAME_DECODE_SLAB and returns the restore.
func SetFrameDecodeSlabForTest(on bool) func() {
	prev := frameDecodeSlabOn.Swap(on)
	return func() { frameDecodeSlabOn.Store(prev) }
}

// slabAheadFrames is how many frames past the current one a new slab is
// sized for (N42_FRAME_DECODE_SLAB).
const slabAheadFrames = 3

// workerDecode is one decode worker's state for a block under
// N42_FRAME_DECODE_SLAB: its batch decoder and a slab of transaction
// pointers the frames' result slices are cut from.
type workerDecode struct {
	dec  *transaction.EthBatchDecoder
	ptrs []*transaction.Transaction
}

// decodeTxsWorker is DecodeTxs on a worker's shared slabs; more is the
// transactions the worker still expects after this frame (slab sizing).
func (w *workerDecode) decodeTxsWorker(raw []byte, more int) ([]*transaction.Transaction, int, error) {
	var f Frame
	if err := decodeEnvelope(raw, &f); err != nil {
		return nil, 0, err
	}
	n := int(f.TxCount)
	if !(f.TxType == TxTypeEthereum && TxDecodePoolEnabled()) {
		return decodeFrameInto(&f, nil, make([]*transaction.Transaction, 0, n))
	}
	if w.dec == nil {
		w.dec = transaction.NewEthBatchDecoder(n + more)
	} else {
		w.dec.Continue(n, more)
	}
	if cap(w.ptrs)-len(w.ptrs) < n {
		w.ptrs = make([]*transaction.Transaction, 0, n+more)
	}
	// Three-index cut: the decode cannot write past this frame's window.
	l := len(w.ptrs)
	txs, dropped, err := decodeFrameInto(&f, w.dec, w.ptrs[l:l:l+n])
	if err != nil {
		return nil, 0, err
	}
	w.ptrs = w.ptrs[:l+len(txs)]
	// The next frame starts right after the last decoded entry: the slice
	// handed out must not reach past it (dropped entries leave capacity).
	return txs[:len(txs):len(txs)], dropped, nil
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
	slab := FrameDecodeSlabEnabled()
	// moreFor estimates the transactions a worker still decodes after frame
	// i: its share of the frames left, at MaxTxs each (slab sizing only).
	// Capped at slabAheadFrames: a worker's share is only an estimate (the
	// frames go to whichever worker is free), and a slab's unused tail is
	// waste, so a slab covers a few frames, not the whole share.
	moreFor := func(i int) int {
		left := len(frames) - i - 1
		return min((left+workers-1)/workers, slabAheadFrames) * MaxTxs
	}
	one := func(i int, w *workerDecode) {
		if w != nil {
			per[i], drops[i], errs[i] = w.decodeTxsWorker(frames[i], moreFor(i))
		} else {
			per[i], drops[i], errs[i] = DecodeTxs(frames[i])
		}
		if after != nil && errs[i] == nil {
			after(i, per[i])
		}
	}
	newWorker := func() *workerDecode {
		if slab {
			return new(workerDecode)
		}
		return nil
	}
	if workers <= 1 {
		w := newWorker()
		for i := range frames {
			one(i, w)
		}
	} else {
		var wg sync.WaitGroup
		var next atomic.Int64
		n := int64(len(frames))
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				wd := newWorker()
				for {
					i := next.Add(1) - 1
					if i >= n {
						return
					}
					one(int(i), wd)
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

// GasWalk is the saturating sum of the gas limits of the transactions one
// encoded frame decodes to, read without decoding them (N42_E1_LIGHT_LAZY_
// RESOLVE: a light node's availability check). The frame's envelope and
// payload layout are checked exactly as DecodeTxs checks them: a frame
// DecodeTxs refuses is refused here with the same error. txCount is the
// header's transaction count, undecodable entries included.
//
// Per entry it adds the gas field of the Ethereum RLP when the walk can read
// it, and otherwise the gas of a full decode (zero when the entry does not
// decode, as DecodeTxs drops it). A blob transaction is always decoded: its
// network form wraps the transaction in an outer list, where the gas field
// is not at the walk's index. So:
//   - every entry DecodeTxs keeps adds exactly its decoded Gas(): the walk
//     reads the same canonical field the decoder reads, and anything the
//     walk cannot read is decoded;
//   - an entry DecodeTxs drops adds zero or the gas field the walk read.
//
// The sum therefore equals the decoded transactions' saturating gas sum when
// no entry is dropped, and is never below it: a walked sum within a limit
// proves the decoded sum is within it too.
func GasWalk(raw []byte) (gas uint64, txCount int, err error) {
	var f Frame
	if err := decodeEnvelope(raw, &f); err != nil {
		return 0, 0, err
	}
	err = walkPayload(f.Payload, int(f.TxCount), func(_ int, b []byte) {
		gas = saturatingAdd(gas, entryGasExact(f.TxType, b))
	})
	if err != nil {
		return 0, 0, err
	}
	return gas, int(f.TxCount), nil
}

// GasWalkFrames is GasWalk over frames across up to workers goroutines
// (<= 0 means GOMAXPROCS): the saturating sum over all frames and the
// header transaction counts' total. The error is that of the lowest-index
// frame GasWalk refuses.
func GasWalkFrames(frames [][]byte, workers int) (gas uint64, txCount int, err error) {
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	if workers > len(frames) {
		workers = len(frames)
	}
	gs := make([]uint64, len(frames))
	ns := make([]int, len(frames))
	errs := make([]error, len(frames))
	one := func(i int) { gs[i], ns[i], errs[i] = GasWalk(frames[i]) }
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
	for i := range frames {
		if errs[i] != nil {
			return 0, 0, errs[i]
		}
		gas = saturatingAdd(gas, gs[i])
		txCount += ns[i]
	}
	return gas, txCount, nil
}

// entryGasExact is one entry's share of GasWalk.
func entryGasExact(txType uint8, b []byte) uint64 {
	if txType == TxTypeEthereum && len(b) > 0 && b[0] != transaction.BlobTxType {
		if g, ok := ethGasLimit(b); ok {
			return g
		}
	}
	if tx, err := decodeTx(txType, b, nil); err == nil {
		return tx.Gas()
	}
	return 0
}

func saturatingAdd(a, b uint64) uint64 {
	if s := a + b; s >= a {
		return s
	}
	return math.MaxUint64
}
