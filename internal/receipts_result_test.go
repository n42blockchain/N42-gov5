// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

func receiptsForResultTest() []*block.Receipt {
	var rs []*block.Receipt
	var cum uint64
	for i := 0; i < 3000; i++ {
		r := &block.Receipt{Status: block.ReceiptStatusSuccessful, Type: uint8(i % 3)}
		cum += 21000
		switch i % 11 {
		case 3:
			r.Status = block.ReceiptStatusFailed
			cum += 9000
		case 5:
			r.ContractAddress = types.Address{0xc0, byte(i)}
		case 7:
			r.Logs = []*block.Log{{
				Address: types.Address{byte(i), 0x01},
				Topics:  []types.Hash{{0xdd, byte(i)}, {byte(i >> 8)}},
				Data:    bytes.Repeat([]byte{byte(i)}, i%70),
			}}
		}
		r.CumulativeGasUsed = cum
		rs = append(rs, r)
	}
	return rs
}

// TestReceiptsResultAndRowMatchesSeparateDerivation: the one-pass receipts
// result and row the write uses equal the separate derivations — native
// root, bloom, gas used and the stored row — and, on an Ethereum-EL config,
// the Ethereum receipt trie root.
func TestReceiptsResultAndRowMatchesSeparateDerivation(t *testing.T) {
	ethCfg := &params.ChainConfig{StateScheme: string(params.StateCommitmentPresetEthereumMPT), ByzantiumBlock: big.NewInt(0)}
	for name, cfg := range map[string]*params.ChainConfig{"native": nil, "nativeCfg": {}, "ethereum": ethCfg} {
		for _, rs := range [][]*block.Receipt{nil, receiptsForResultTest()} {
			wantRoot := DeriveSha(block.Receipts(rs))
			if cfg.EthereumReceiptEncoding() {
				wantRoot = block.EthereumReceiptRoot(rs, true)
			}
			wantRow, err := rawdb.EncodeReceiptsForStorage(rs)
			if err != nil {
				t.Fatal(err)
			}
			var wantGas uint64
			if len(rs) > 0 {
				wantGas = rs[len(rs)-1].CumulativeGasUsed
			}

			got, row := receiptsResultAndRow(cfg, 1, rs, true)
			if got.ReceiptHash != wantRoot || got.Bloom != block.CreateBloom(rs) || got.GasUsed != wantGas {
				t.Fatalf("%s/%d: result %+v differs from separate derivation (root %x)", name, len(rs), got, wantRoot)
			}
			if !bytes.Equal(row, wantRow) {
				t.Fatalf("%s/%d: row differs from EncodeReceiptsForStorage", name, len(rs))
			}
			if r := ExecutedResultFor(cfg, 1, types.Hash{1}, rs); r.Root != (types.Hash{1}) || r.ReceiptHash != wantRoot || r.Bloom != got.Bloom {
				t.Fatalf("%s/%d: ExecutedResultFor %+v", name, len(rs), r)
			}
			if ReceiptsRootFor(cfg, 1, rs) != wantRoot {
				t.Fatalf("%s/%d: ReceiptsRootFor differs", name, len(rs))
			}
			f := startReceiptsResult(cfg, 1, types.Hash{2}, rs)
			if r, ok := f.resultFor(types.Hash{2}, types.Hash{3}, rs); !ok || r.ReceiptHash != wantRoot || r.Root != (types.Hash{3}) {
				t.Fatalf("%s/%d: future result %+v", name, len(rs), r)
			}
			if !bytes.Equal(f.encodedFor(types.Hash{2}, rs), wantRow) {
				t.Fatalf("%s/%d: future row differs", name, len(rs))
			}
		}
	}
}
