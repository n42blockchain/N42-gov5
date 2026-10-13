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
	"runtime"
	"sync"

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

// decodeTx decodes one frame entry according to the frame's tx type.
func decodeTx(txType uint8, b []byte) (*transaction.Transaction, error) {
	if txType == TxTypeNative {
		tx := new(transaction.Transaction)
		if err := tx.Unmarshal(b); err != nil {
			return nil, err
		}
		return tx, nil
	}
	return transaction.DecodeEthereumTransaction(b)
}

// DecodeTxs decodes the transactions of one encoded frame, in payload
// order. An entry that does not decode is dropped and counted: every node
// decodes the same bytes with the same codec, so the dropped set is the
// same everywhere and the block's transaction list stays deterministic.
// Only a frame whose layout is invalid is an error.
func DecodeTxs(raw []byte) ([]*transaction.Transaction, int, error) {
	f, err := Decode(raw)
	if err != nil {
		return nil, 0, err
	}
	txs := make([]*transaction.Transaction, 0, f.TxCount)
	dropped := 0
	_ = walkPayload(f.Payload, int(f.TxCount), func(_ int, b []byte) {
		tx, err := decodeTx(f.TxType, b)
		if err != nil {
			dropped++
			return
		}
		txs = append(txs, tx)
	})
	return txs, dropped, nil
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
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	if workers > len(frames) {
		workers = len(frames)
	}
	per := make([][]*transaction.Transaction, len(frames))
	drops := make([]int, len(frames))
	errs := make([]error, len(frames))
	if workers <= 1 {
		for i, raw := range frames {
			per[i], drops[i], errs[i] = DecodeTxs(raw)
		}
	} else {
		var wg sync.WaitGroup
		next := make(chan int, len(frames))
		for i := range frames {
			next <- i
		}
		close(next)
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range next {
					per[i], drops[i], errs[i] = DecodeTxs(frames[i])
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
	add := func(g uint64) {
		if g > ^uint64(0)-gas {
			gas = ^uint64(0)
		} else {
			gas += g
		}
	}
	_ = walkPayload(f.Payload, int(f.TxCount), func(_ int, b []byte) {
		if f.TxType == TxTypeEthereum {
			if g, ok := ethGasLimit(b); ok {
				add(g)
				return
			}
		}
		if tx, err := decodeTx(f.TxType, b); err == nil {
			add(tx.Gas())
		}
	})
	return gas
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
