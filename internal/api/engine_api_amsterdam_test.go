package api

import (
	"context"
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

func amsterdamTestPayload(ts uint64) *ExecutionPayloadAmsterdam {
	slot := hexutil.Uint64(7)
	return &ExecutionPayloadAmsterdam{
		ExecutionPayloadV4: ExecutionPayloadV4{
			BlockNumber: 22_000_001, Timestamp: hexutil.Uint64(ts), GasLimit: 36_000_000,
			BaseFeePerGas: hexBigFromUint64(1), Withdrawals: []*Withdrawal{},
			BlobGasUsed: hexUint64Ptr(0), ExcessBlobGas: hexUint64Ptr(0),
			LogsBloom: make(hexutil.Bytes, block.BloomByteLength),
		},
		BlockAccessList: hexutil.Bytes{0xc0},
		SlotNumber:      &slot,
	}
}

// newPayloadV5 takes Amsterdam payloads only, and every Amsterdam field is
// required.
func TestEngineNewPayloadV5ForkAndParams(t *testing.T) {
	t.Parallel()

	amsterdamTS := mainnetOsakaTS + 10_000_000
	api, _ := newMainnetEngineTestAPI(t, mainnetOsakaTS)
	api.chainConfig.GlamsterdamTime = new(big.Int).SetUint64(amsterdamTS)
	v4 := NewEngineAPIv4(NewBlockChainAPI(api))
	root := types.Hash{0x05}
	ctx := context.Background()

	_, err := v4.NewPayloadV5(ctx, amsterdamTestPayload(amsterdamTS-1), []types.Hash{}, &root, []hexutil.Bytes{})
	requireEngineErrorCode(t, err, -38005)
	_, err = v4.NewPayloadV5(ctx, amsterdamTestPayload(amsterdamTS), []types.Hash{}, &root, []hexutil.Bytes{})
	requireNotUnsupportedFork(t, err)

	for name, mutate := range map[string]func(p *ExecutionPayloadAmsterdam){
		"slot number":       func(p *ExecutionPayloadAmsterdam) { p.SlotNumber = nil },
		"block access list": func(p *ExecutionPayloadAmsterdam) { p.BlockAccessList = nil },
		"withdrawals":       func(p *ExecutionPayloadAmsterdam) { p.Withdrawals = nil },
		"blob gas":          func(p *ExecutionPayloadAmsterdam) { p.BlobGasUsed = nil },
	} {
		t.Run(name, func(t *testing.T) {
			p := amsterdamTestPayload(amsterdamTS)
			mutate(p)
			_, err := v4.NewPayloadV5(ctx, p, []types.Hash{}, &root, []hexutil.Bytes{})
			requireEngineErrorCode(t, err, -32602)
		})
	}
	_, err = v4.NewPayloadV5(ctx, amsterdamTestPayload(amsterdamTS), nil, &root, []hexutil.Bytes{})
	requireEngineErrorCode(t, err, -32602)
	_, err = v4.NewPayloadV5(ctx, amsterdamTestPayload(amsterdamTS), []types.Hash{}, nil, []hexutil.Bytes{})
	requireEngineErrorCode(t, err, -32602)
	_, err = v4.NewPayloadV5(ctx, amsterdamTestPayload(amsterdamTS), []types.Hash{}, &root, nil)
	requireEngineErrorCode(t, err, -32602)
}

// The payload's block access list enters the block hash as keccak256 of its
// encoding, and the slot number as itself.
func TestAmsterdamPayloadFieldsEnterBlockHash(t *testing.T) {
	t.Parallel()

	amsterdamTS := mainnetOsakaTS + 10_000_000
	api, _ := newMainnetEngineTestAPI(t, mainnetOsakaTS)
	api.chainConfig.GlamsterdamTime = new(big.Int).SetUint64(amsterdamTS)
	cfg := api.chainConfig

	p := amsterdamTestPayload(amsterdamTS)
	blk, err := executionPayloadV4ToBlock(&p.ExecutionPayloadV4)
	require.NoError(t, err)
	opts := enginePayloadHashOptions{includeWithdrawals: true, includeBlobFields: true, parentBeaconRoot: &types.Hash{}}

	fields := &amsterdamPayloadFields{blockAccessList: []byte{0xc0}, slotNumber: 7}
	withFields := fields.apply(blk)
	header := blockHeader(withFields)
	require.Equal(t, crypto.Keccak256Hash([]byte{0xc0}), *header.BlockAccessListHash)
	require.Equal(t, uint64(7), *header.SlotNumber)

	h := ethCompatibleEngineBlockHash(withFields, cfg, opts)
	require.Equal(t, ethCompatibleHeaderHash(header, cfg), ethCompatibleEngineBlockHash(withFields, cfg, enginePayloadHashOptions{
		includeWithdrawals: true, includeBlobFields: true, parentBeaconRoot: &types.Hash{}, requestsHash: header.RequestsHash,
	}))
	other := (&amsterdamPayloadFields{blockAccessList: []byte{0xc1, 0xc0}, slotNumber: 7}).apply(blk)
	require.NotEqual(t, h, ethCompatibleEngineBlockHash(other, cfg, opts))
	otherSlot := (&amsterdamPayloadFields{blockAccessList: []byte{0xc0}, slotNumber: 8}).apply(blk)
	require.NotEqual(t, h, ethCompatibleEngineBlockHash(otherSlot, cfg, opts))
}
