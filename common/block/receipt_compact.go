// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Compact (V2-style) receipts STORAGE codec — companion to the compact header
// and transaction codecs. The proto encoding averaged ~345 B/receipt (3.7 GB
// Receipts table at 12.7M blocks), most of it spent on DERIVABLE fields: the
// 256-byte Bloom (a pure function of the logs, recomputed on decode), TxHash /
// BlockHash / BlockNumber / TransactionIndex / GasUsed (block-context fields the
// raw read path documents as "not guaranteed to be populated"). ContractAddress
// is NOT derivable from the receipt alone (it needs the creating tx's sender +
// nonce) and the read path does not re-derive it, so it is stored explicitly
// when present — contract-creation receipts only, a small minority. The compact
// record keeps the consensus content plus ContractAddress:
//
//	[0] 0xFF  — format marker (a valid protobuf message can never begin with
//	            0xFF: field 31 / wire type 7 is illegal)
//	[1] 0x01  — codec version
//	[2..] uvarint count, then per receipt:
//	      type byte
//	      flag byte: bit0 = PostState present (32 B follows; else Status uvarint),
//	                 bit1 = ContractAddress present (20 B)
//	      [PostState 32 B | Status uvarint]
//	      [ContractAddress 20 B if bit1]
//	      CumulativeGasUsed uvarint
//	      logs: uvarint count; per log: address 20 B, uvarint topic count,
//	            topics 32 B each, uvarint data len + data
//
// Bloom is recomputed from the logs on decode (identical to how it was created
// at execution time), so the RLP receipt root derived from decoded receipts is
// byte-identical. Remaining context fields are left zero on the raw path — same
// contract as the proto path's documentation; ReadReceipts fills them.

package block

import (
	"encoding/binary"
	"fmt"

	"github.com/n42blockchain/N42/common/types"
)

const (
	compactReceiptsMarker  = 0xFF
	compactReceiptsVersion = 0x01

	rcfPostState = 1 << 0 // PostState (32 B) instead of Status
	rcfContract  = 1 << 1 // ContractAddress (20 B) present (contract-creation tx)
)

func rcAppendUvarint(b []byte, v uint64) []byte {
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], v)
	return append(b, tmp[:n]...)
}

// MarshalCompact encodes the receipts list in the compact storage format.
func (rs *Receipts) MarshalCompact() []byte {
	out := make([]byte, 0, 64*len(*rs))
	out = append(out, compactReceiptsMarker, compactReceiptsVersion)
	out = rcAppendUvarint(out, uint64(len(*rs)))
	for _, r := range *rs {
		out = append(out, r.Type)
		var flags byte
		if len(r.PostState) == 32 {
			flags |= rcfPostState
		}
		hasContract := r.ContractAddress != (types.Address{})
		if hasContract {
			flags |= rcfContract
		}
		out = append(out, flags)
		if flags&rcfPostState != 0 {
			out = append(out, r.PostState...)
		} else {
			out = rcAppendUvarint(out, r.Status)
		}
		if hasContract {
			out = append(out, r.ContractAddress[:]...)
		}
		out = rcAppendUvarint(out, r.CumulativeGasUsed)
		out = rcAppendUvarint(out, uint64(len(r.Logs)))
		for _, lg := range r.Logs {
			out = append(out, lg.Address[:]...)
			out = rcAppendUvarint(out, uint64(len(lg.Topics)))
			for _, tp := range lg.Topics {
				out = append(out, tp[:]...)
			}
			out = rcAppendUvarint(out, uint64(len(lg.Data)))
			out = append(out, lg.Data...)
		}
	}
	return out
}

// IsCompactReceipts reports whether data is in the compact storage format.
func IsCompactReceipts(data []byte) bool {
	return len(data) >= 3 && data[0] == compactReceiptsMarker && data[1] == compactReceiptsVersion
}

// unmarshalCompact decodes the compact storage format. Bloom is recomputed
// from the logs; block-context fields stay zero (the raw-read contract).
func (rs *Receipts) unmarshalCompact(data []byte) error {
	if !IsCompactReceipts(data) {
		return fmt.Errorf("not compact receipts")
	}
	p := data[2:]
	take := func(n int) ([]byte, error) {
		if len(p) < n {
			return nil, fmt.Errorf("compact receipts truncated: need %d, have %d", n, len(p))
		}
		b := p[:n]
		p = p[n:]
		return b, nil
	}
	uvarint := func() (uint64, error) {
		v, n := binary.Uvarint(p)
		if n <= 0 {
			return 0, fmt.Errorf("compact receipts: bad uvarint")
		}
		p = p[n:]
		return v, nil
	}

	count, err := uvarint()
	if err != nil {
		return err
	}
	out := make(Receipts, count)
	for i := range out {
		r := new(Receipt)
		b, err := take(2)
		if err != nil {
			return err
		}
		r.Type = b[0]
		flags := b[1]
		if flags&rcfPostState != 0 {
			ps, err := take(32)
			if err != nil {
				return err
			}
			r.PostState = make([]byte, 32)
			copy(r.PostState, ps)
		} else {
			if r.Status, err = uvarint(); err != nil {
				return err
			}
		}
		if flags&rcfContract != 0 {
			ca, err := take(20)
			if err != nil {
				return err
			}
			copy(r.ContractAddress[:], ca)
		}
		if r.CumulativeGasUsed, err = uvarint(); err != nil {
			return err
		}
		nLogs, err := uvarint()
		if err != nil {
			return err
		}
		r.Logs = make([]*Log, nLogs)
		for j := range r.Logs {
			lg := new(Log)
			ab, err := take(20)
			if err != nil {
				return err
			}
			copy(lg.Address[:], ab)
			nTopics, err := uvarint()
			if err != nil {
				return err
			}
			lg.Topics = make([]types.Hash, nTopics)
			for k := range lg.Topics {
				tb, err := take(32)
				if err != nil {
					return err
				}
				copy(lg.Topics[k][:], tb)
			}
			nData, err := uvarint()
			if err != nil {
				return err
			}
			db, err := take(int(nData))
			if err != nil {
				return err
			}
			lg.Data = make([]byte, nData)
			copy(lg.Data, db)
			r.Logs[j] = lg
		}
		// Bloom is a pure function of the logs — recompute exactly as execution did.
		r.Bloom = CreateBloom(Receipts{r})
		out[i] = r
	}
	if len(p) != 0 {
		return fmt.Errorf("compact receipts: %d trailing bytes", len(p))
	}
	// Populate the fields derivable WITHIN the record (no block context needed):
	// per-tx GasUsed is the cumulative-gas delta; positional indices follow from
	// record order. Only TxHash/BlockHash/BlockNumber stay zero on the raw path
	// (they need the block — ReadRawReceipts' documented contract).
	logIdx := uint(0)
	for i, r := range out {
		if i == 0 {
			r.GasUsed = r.CumulativeGasUsed
		} else {
			r.GasUsed = r.CumulativeGasUsed - out[i-1].CumulativeGasUsed
		}
		r.TransactionIndex = uint(i)
		for _, lg := range r.Logs {
			lg.TxIndex = uint(i)
			lg.Index = logIdx
			logIdx++
		}
	}
	*rs = out
	return nil
}
