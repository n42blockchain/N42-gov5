// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"context"
	"math/big"
	"strings"
	"testing"

	"github.com/golang/snappy"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/params"

	iinternal "github.com/n42blockchain/N42/internal"
)

// byzantiumFromGenesis is mainnet with every block-numbered fork at 0, so the
// synthetic blocks below are post-Byzantium (receipts carry a status).
func byzantiumFromGenesis() *params.ChainConfig {
	cfg := *params.EthereumMainnetChainConfig
	zero := new(big.Int)
	cfg.HomesteadBlock, cfg.DAOForkBlock, cfg.TangerineWhistleBlock = zero, nil, zero
	cfg.SpuriousDragonBlock, cfg.ByzantiumBlock, cfg.ConstantinopleBlock = zero, zero, zero
	cfg.PetersburgBlock, cfg.IstanbulBlock, cfg.MuirGlacierBlock = zero, zero, zero
	cfg.BerlinBlock, cfg.LondonBlock, cfg.ArrowGlacierBlock, cfg.GrayGlacierBlock = zero, zero, zero, zero
	cfg.DAOForkSupport = false
	return &cfg
}

// buildTamperedFreezer writes three empty post-merge blocks, letting tamper
// alter block 1's header before it is stored.
func buildTamperedFreezer(t *testing.T, dir string, tamper func(*block.Header)) *freezer.Freezer {
	t.Helper()
	fz, err := freezer.New(dir, 0)
	if err != nil {
		t.Fatalf("freezer: %v", err)
	}
	var headers, bodies, receipts, hashes, diffs [][]byte
	var parent types.Hash
	for i := 0; i < 3; i++ {
		h := mkHeader(uint64(i), tsAnchor+uint64(i), parent, emptyTrieRoot(), EthReceiptHash(nil))
		if i == 1 && tamper != nil {
			tamper(h)
		}
		parent = h.Hash()
		headers = append(headers, encodeGethHeader(t, h))
		bodies = append(bodies, emptyGethBodyRLP(t))
		receipts = append(receipts, snappy.Encode(nil, mustRLP(t, []interface{}{})))
		hb := h.Hash()
		hashes = append(hashes, hb[:])
		diffs = append(diffs, []byte{0})
	}
	if err := fz.Freeze(0, &freezer.FreezeData{
		Headers: headers, Bodies: bodies, Receipts: receipts, Hashes: hashes, Difficulty: diffs,
	}); err != nil {
		t.Fatalf("freeze: %v", err)
	}
	return fz
}

// Batch replay checks a block's receipts root and logs bloom against its
// header, as geth's ValidateState does, not only its gas used.
func TestExecutorRejectsReceiptsRootAndBloomMismatch(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tamper  func(*block.Header)
		wantErr string
	}{
		{"untampered", nil, ""},
		{"receipts root", func(h *block.Header) { h.ReceiptHash = types.Hash{0xde, 0xad} }, "receipts root mismatch"},
		{"logs bloom", func(h *block.Header) { h.Bloom[7] = 0x10 }, "logs bloom mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fz := buildTamperedFreezer(t, t.TempDir(), tc.tamper)
			defer fz.Close()
			chainCfg := byzantiumFromGenesis()
			executor := NewExecutor(fz, memdb.NewTestDB(t), chainCfg, NewEthReplayEngine(chainCfg),
				ExecutorConfig{StartBlock: 0, EndBlock: 2, CommitInterval: 1}, nil)
			err := executor.Run(context.Background())
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Run: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Run err = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidateBlockOutputs(t *testing.T) {
	cfg := params.EthereumMainnetChainConfig
	receipt := &block.Receipt{Type: 2, Status: block.ReceiptStatusSuccessful, CumulativeGasUsed: 50_000,
		Logs: []*block.Log{{Address: types.Address{0x42}, Topics: []types.Hash{{1}}, Data: []byte{1, 2}}}}
	receipt.Bloom = block.CreateBloom(block.Receipts{receipt})
	requests := []hexutil.Bytes{{0x00, 0xaa}, {0x01}, {0x02, 0xbb}}
	requestsHash := iinternal.ExecutionRequestsHash(requests)

	byzantium := cfg.ByzantiumBlock.Uint64()
	prague := cfg.PragueTime.Uint64()
	header := func(num, time uint64) *block.Header {
		h := &block.Header{Number: uint256.NewInt(num), Time: time, ReceiptHash: EthReceiptHash(block.Receipts{receipt}), Bloom: receipt.Bloom}
		if cfg.IsPrague(time) {
			rh := requestsHash
			h.RequestsHash = &rh
		}
		return h
	}
	result := &BlockResult{Receipts: block.Receipts{receipt}, Requests: requests}

	for _, tc := range []struct {
		name    string
		header  *block.Header
		wantErr string
	}{
		{"prague ok", header(22_500_000, prague), ""},
		{"prague wrong requests hash", func() *block.Header {
			h := header(22_500_000, prague)
			h.RequestsHash = &types.Hash{1}
			return h
		}(), "requests hash mismatch"},
		{"prague missing requests hash", func() *block.Header {
			h := header(22_500_000, prague)
			h.RequestsHash = nil
			return h
		}(), "requests hash mismatch"},
		{"cancun ignores requests", header(20_000_000, prague-1), ""},
		{"zero header bloom is skipped", func() *block.Header {
			h := header(22_500_000, prague)
			h.Bloom = block.Bloom{}
			return h
		}(), ""},
		{"wrong bloom", func() *block.Header {
			h := header(22_500_000, prague)
			h.Bloom[0] ^= 0x01
			return h
		}(), "logs bloom mismatch"},
		{"wrong receipts root", func() *block.Header {
			h := header(22_500_000, prague)
			h.ReceiptHash = types.Hash{2}
			return h
		}(), "receipts root mismatch"},
		{"pre-byzantium receipts root not checked", func() *block.Header {
			h := header(byzantium-1, 1_500_000_000)
			h.ReceiptHash = types.Hash{2}
			return h
		}(), ""},
		{"pre-byzantium bloom still checked", func() *block.Header {
			h := header(byzantium-1, 1_500_000_000)
			h.Bloom[0] ^= 0x01
			return h
		}(), "logs bloom mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateBlockOutputs(cfg, tc.header, result)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}
}
