// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package transaction

import (
	"fmt"

	"github.com/n42blockchain/N42/params"
)

// DecodePerformanceTransactionOffline is an explicit chain/activation-aware
// parser. It checks encoding/chain binding for Ed25519 carriers, not signature
// authorization. Ordinary ETH decoding retains its existing parser semantics.
// No runtime caller is switched to this parser in the offline codec stage.
func DecodePerformanceTransactionOffline(data []byte, cfg *params.ChainConfig, number uint64) (*Transaction, error) {
	if err := cfg.ValidatePerformanceEncoding(); err != nil {
		return nil, err
	}
	if len(data) == 0 || data[0] != AltSigTxType {
		return DecodeEthereumTransaction(data)
	}
	if cfg == nil || cfg.Performance == nil || cfg.Performance.Version != params.PerformanceEd25519Version || number < cfg.Performance.Ed25519ActivationBlock {
		return nil, fmt.Errorf("Ed25519 transaction encoding is not active")
	}
	tx, err := DecodeAltSigTransactionOffline(data)
	if err != nil {
		return nil, err
	}
	if tx.ChainId().Uint64() != cfg.ChainID.Uint64() {
		return nil, fmt.Errorf("performance transaction chain ID mismatch")
	}
	return tx, nil
}
