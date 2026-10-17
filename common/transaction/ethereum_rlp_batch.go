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

package transaction

import (
	"time"
)

// EthBatchDecoder decodes the Ethereum-encoded transactions of one
// immutable buffer, such as a frame (P14).
//
// DecodeEthereumTransaction pays, per transaction, a Transaction, its body
// with integers and recipient, a copy of the encoding and the encoding's
// slice header: four allocations and a memmove, plus a clock read. A batch
// decoder carves the Transaction and the body of the two transfer shapes the
// fast decoder handles out of one slab per batch, aliases the encoding into
// the caller's buffer instead of copying it, and reads the clock once.
//
// Aliasing is only correct when the buffer is never modified while any
// decoded transaction lives; frames satisfy that (the frame store, block
// bodies and the Frames table keep them as given). A transaction from the
// slab keeps the whole slab reachable: it suits batches whose transactions
// live and die together (a block body), not ones whose transactions are
// retained one by one.
//
// The decoded values are exactly DecodeEthereumTransaction's: the same fast
// decoder fills the slab entries, and anything it declines goes through the
// general decoder.
type EthBatchDecoder struct {
	now  time.Time
	left int // expected transactions not decoded yet (slab sizing)
	dyn  []dynamicFeeEntry
	leg  []legacyEntry
	nDyn int
	nLeg int
}

type dynamicFeeEntry struct {
	tx Transaction
	a  dynamicFeeAlloc
}

type legacyEntry struct {
	tx Transaction
	a  legacyAlloc
}

// NewEthBatchDecoder returns a decoder for about n transactions.
func NewEthBatchDecoder(n int) *EthBatchDecoder {
	d := new(EthBatchDecoder)
	d.Reset(n)
	return d
}

// Reset prepares d for a new batch of about n transactions, reusing its
// slabs. It must only be called once no transaction d decoded is reachable
// any more (the slabs are overwritten).
func (d *EthBatchDecoder) Reset(n int) {
	clear(d.dyn[:d.nDyn])
	clear(d.leg[:d.nLeg])
	d.nDyn, d.nLeg = 0, 0
	d.left = n
	d.now = time.Now()
}

// DecodeAliased decodes one canonical Ethereum transaction encoding.
// data must not be modified while the transaction lives: its cached
// encoding aliases data.
func (d *EthBatchDecoder) DecodeAliased(data []byte) (*Transaction, error) {
	d.left--
	if len(data) > 0 && !reflectDecodeOnly {
		switch {
		case data[0] >= 0xc0:
			e := d.nextLegacy()
			if fastDecodeLegacyInto(&e.a, data) {
				d.nLeg++
				return d.publish(&e.tx, &e.a.tx, data), nil
			}
			e.a = legacyAlloc{}
		case data[0] == DynamicFeeTxType:
			e := d.nextDynamicFee()
			if fastDecodeDynamicFeeInto(&e.a, data[1:]) {
				d.nDyn++
				return d.publish(&e.tx, &e.a.tx, data), nil
			}
			e.a = dynamicFeeAlloc{}
		}
	}
	tx, err := decodeEthereumTransaction(data)
	if err != nil {
		return nil, err
	}
	tx.cacheEncodedOwned(data)
	return tx, nil
}

func (d *EthBatchDecoder) publish(tx *Transaction, inner TxData, data []byte) *Transaction {
	tx.inner = inner
	tx.time = d.now
	tx.cacheEncodedOwned(data)
	return tx
}

// slabSize is the entries a new slab gets: what the batch still expects
// (the one being decoded included), at least one.
func (d *EthBatchDecoder) slabSize(have int) int {
	n := d.left + 1
	if n < 1 {
		n = 1
	}
	if have > 0 && n < have {
		// Unexpectedly many: grow geometrically.
		n = have
	}
	return n
}

// nextDynamicFee returns the next free, zeroed dynamic-fee entry. A full
// slab is replaced, never grown in place: decoded transactions point into
// it.
func (d *EthBatchDecoder) nextDynamicFee() *dynamicFeeEntry {
	if d.nDyn == len(d.dyn) {
		d.dyn = make([]dynamicFeeEntry, d.slabSize(d.nDyn))
		d.nDyn = 0
	}
	return &d.dyn[d.nDyn]
}

func (d *EthBatchDecoder) nextLegacy() *legacyEntry {
	if d.nLeg == len(d.leg) {
		d.leg = make([]legacyEntry, d.slabSize(d.nLeg))
		d.nLeg = 0
	}
	return &d.leg[d.nLeg]
}
