// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package block

import (
	"bytes"
	"fmt"
	"sync/atomic"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/lib/rlp"
	"github.com/n42blockchain/N42/params"
)

// UnmarshalPerformanceOffline parses the complete existing block wire format
// with explicitly selected performance rules and actual genesis metadata. It
// performs no seal, execution or signature authorization and enables no runtime.
// Like the other mutable decoders, the receiver requires exclusive caller access.
func (b *Block) UnmarshalPerformanceOffline(data []byte, cfg *params.ChainConfig, genesis *Block) error {
	if err := cfg.ValidatePerformanceEncoding(); err != nil {
		return err
	}
	if cfg == nil || cfg.Performance == nil || cfg.Performance.Version == 1 {
		return b.Unmarshal(data)
	}
	_, marker, err := cfg.PerformanceEncodingIdentity()
	if err != nil {
		return err
	}
	if genesis == nil || genesis.header == nil || genesis.header.Number == nil || !genesis.header.Number.IsZero() || genesis.header.Time != params.PerformanceGenesisTime || !bytes.Equal(genesis.header.Extra, marker[:]) {
		return fmt.Errorf("performance block codec requires actual protocol-bound genesis")
	}
	if b == nil {
		return fmt.Errorf("missing performance block receiver")
	}
	// DecodeBytes checks for trailing input only after the Decoder callback
	// returns. Keep all parsed state private until that final check succeeds.
	var parsed Block
	decoder := &performanceOfflineBlockDecoder{block: &parsed, config: cfg}
	if err := rlp.DecodeBytes(data, decoder); err != nil {
		return err
	}
	// Transfer only owned payload pointers. Block contains atomic.Value fields
	// and must not be copied, including on successful receiver reuse.
	b.header, b.body = parsed.header, parsed.body
	b.hash = atomic.Value{}
	b.size = atomic.Value{}
	b.decodeReused, b.decodeDecoded = 0, 0
	return nil
}

type performanceOfflineBlockDecoder struct {
	block  *Block
	config *params.ChainConfig
}

func (d *performanceOfflineBlockDecoder) DecodeRLP(s *rlp.Stream) error {
	return d.block.decodeRLPTransactions(s, func(dec *blockRLP) ([]*transaction.Transaction, error) {
		header, data := dec.Header, dec.TxData
		if len(dec.FrameHashes) > 0 {
			return nil, fmt.Errorf("performance offline frame decoding is not implemented")
		}
		if header == nil || header.Number == nil || !header.Number.IsUint64() {
			return nil, fmt.Errorf("invalid performance block height")
		}
		// Frame admission and config-aware frame resolution are not implemented.
		// This offline slice supports complete bodies only.
		txs := make([]*transaction.Transaction, len(data))
		for i, raw := range data {
			tx, err := transaction.DecodePerformanceTransactionOffline(raw, d.config, header.Number.Uint64())
			if err != nil {
				return nil, fmt.Errorf("performance transaction %d: %w", i, err)
			}
			txs[i] = tx
		}
		return txs, nil
	})
}
