// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/params"
)

func TestConfigureBlockCommitmentRules(t *testing.T) {
	oldETH, oldBlake, oldDeferred := block.UseEthereumTxRoot, block.TxRootBlake3Time, block.DeferredExecutionTime
	t.Cleanup(func() {
		block.UseEthereumTxRoot = oldETH
		block.TxRootBlake3Time = oldBlake
		block.DeferredExecutionTime = oldDeferred
	})
	t.Setenv("N42_TXROOT_BLAKE3_TIME", "")
	t.Setenv("N42_DEFERRED_EXECUTION_TIME", "")
	for _, test := range []struct {
		name            string
		cfg             *params.ChainConfig
		eth             bool
		blake, deferred uint64
		reject          bool
	}{
		{name: "nil"}, {name: "native", cfg: &params.ChainConfig{StateScheme: "bmt"}},
		{name: "qmdb", cfg: &params.ChainConfig{StateScheme: "qmdb"}, eth: true},
		{name: "mpt", cfg: &params.ChainConfig{StateScheme: "ethereum-mpt"}, eth: true},
		{name: "forks", cfg: &params.ChainConfig{StateScheme: "qmdb", Consensus: params.HotStuffConsensus, TxRootBlake3Time: big.NewInt(123), DeferredExecutionTime: big.NewInt(456)}, eth: true, blake: 123, deferred: 456},
		{name: "invalid-deferred", cfg: &params.ChainConfig{StateScheme: "bmt", DeferredExecutionTime: big.NewInt(456)}, deferred: 456, reject: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			block.UseEthereumTxRoot = !test.eth
			block.TxRootBlake3Time = 99
			block.DeferredExecutionTime = 99
			err := ConfigureBlockCommitmentRules(test.cfg)
			if (err != nil) != test.reject {
				t.Fatalf("configuration error %v", err)
			}
			if block.UseEthereumTxRoot != test.eth || block.TxRootBlake3Time != test.blake || block.DeferredExecutionTime != test.deferred {
				t.Fatalf("wrong process rules: eth=%v blake=%d deferred=%d", block.UseEthereumTxRoot, block.TxRootBlake3Time, block.DeferredExecutionTime)
			}
		})
	}
}
