package api

import (
	"context"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"unicode"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
	"github.com/stretchr/testify/require"
)

// Ethereum mainnet fork timestamps used by the fork-gate tests. Mainnet
// schedules Prague through PragueTime and leaves the N42-native PectraTime
// unset, so every gate must key on IsPrague, not IsPectra.
var (
	mainnetCancunTS = params.EthereumMainnetChainConfig.CancunTime.Uint64()
	mainnetPragueTS = params.EthereumMainnetChainConfig.PragueTime.Uint64()
	mainnetOsakaTS  = params.EthereumMainnetChainConfig.OsakaTime.Uint64()
)

func newMainnetEngineTestAPI(t *testing.T, headTime uint64) (*API, types.Hash) {
	t.Helper()
	cfg := *params.EthereumMainnetChainConfig
	require.Nil(t, cfg.PectraTime, "mainnet must not set PectraTime")
	header := &block.Header{
		Number:        uint256.NewInt(22_000_000),
		Difficulty:    uint256.NewInt(0),
		GasLimit:      36_000_000,
		Time:          headTime,
		BaseFee:       uint256.NewInt(1),
		BlobGasUsed:   ptrToUint64(0),
		ExcessBlobGas: ptrToUint64(0),
	}
	api := &API{
		bc:            &canonicalCheckChainStub{header: header, blk: block.NewBlock(header, nil)},
		chainConfig:   &cfg,
		engineOverlay: newEngineOverlay(),
	}
	return api, NewEngineAPIV1(NewBlockChainAPI(api)).currentHeadHash()
}

func TestEngineForkGatesFollowPragueOnEthereumMainnet(t *testing.T) {
	t.Parallel()

	api, _ := newMainnetEngineTestAPI(t, mainnetCancunTS)
	v1 := NewEngineAPIV1(NewBlockChainAPI(api))
	v3 := NewEngineAPIBlob(NewBlockChainAPI(api))
	v4 := NewEngineAPIv4(NewBlockChainAPI(api))
	root := types.Hash{0x01}

	payloadV3 := func(ts uint64) *ExecutionPayloadV3 {
		return &ExecutionPayloadV3{
			BlockNumber:   hexutil.Uint64(22_000_001),
			Timestamp:     hexutil.Uint64(ts),
			GasLimit:      hexutil.Uint64(36_000_000),
			BaseFeePerGas: hexBigFromUint64(1),
			Withdrawals:   []*Withdrawal{},
			BlobGasUsed:   hexUint64Ptr(0),
			ExcessBlobGas: hexUint64Ptr(0),
		}
	}
	payloadV4 := func(ts uint64) *ExecutionPayloadV4 {
		return &ExecutionPayloadV4{
			BlockNumber:   hexutil.Uint64(22_000_001),
			Timestamp:     hexutil.Uint64(ts),
			GasLimit:      hexutil.Uint64(36_000_000),
			BaseFeePerGas: hexBigFromUint64(1),
			Withdrawals:   []*Withdrawal{},
			BlobGasUsed:   hexUint64Ptr(0),
			ExcessBlobGas: hexUint64Ptr(0),
		}
	}

	// newPayloadV2 is Paris/Shanghai only.
	_, err := v1.NewPayloadV2(context.Background(), &ExecutionPayloadV2{
		ExecutionPayloadV1: ExecutionPayloadV1{
			BlockNumber: hexutil.Uint64(22_000_001),
			Timestamp:   hexutil.Uint64(mainnetCancunTS + 12),
		},
		Withdrawals: []*Withdrawal{},
	})
	requireEngineErrorCode(t, err, -38005)

	// newPayloadV3 is Cancun only.
	_, err = v3.NewPayloadV3(context.Background(), payloadV3(mainnetPragueTS), []types.Hash{}, &root)
	requireEngineErrorCode(t, err, -38005)
	_, err = v3.NewPayloadV3(context.Background(), payloadV3(mainnetOsakaTS), []types.Hash{}, &root)
	requireEngineErrorCode(t, err, -38005)

	// newPayloadV4 is Prague and Osaka.
	_, err = v4.NewPayloadV4(context.Background(), payloadV4(mainnetPragueTS-1), []types.Hash{}, &root, []hexutil.Bytes{})
	requireEngineErrorCode(t, err, -38005)
	for _, ts := range []uint64{mainnetPragueTS, mainnetOsakaTS, params.EthereumMainnetChainConfig.BPO2Time.Uint64()} {
		_, err = v4.NewPayloadV4(context.Background(), payloadV4(ts), []types.Hash{}, &root, []hexutil.Bytes{})
		requireNotUnsupportedFork(t, err)
	}

	// getPayloadV3 is Cancun only, getPayloadV4 Prague only.
	store := func(id PayloadID, ts uint64) {
		api.engineOverlay.storeBuiltPayload(id, &engineBuiltPayload{
			v1: &ExecutionPayloadV1{Timestamp: hexutil.Uint64(ts)},
			v2: &ExecutionPayloadV2{ExecutionPayloadV1: ExecutionPayloadV1{Timestamp: hexutil.Uint64(ts)}},
			v3: payloadV3(ts),
			v4: payloadV4(ts),
		})
	}
	cancunID, pragueID, osakaID := PayloadID{0x0c}, PayloadID{0x0d}, PayloadID{0x0e}
	store(cancunID, mainnetCancunTS+12)
	store(pragueID, mainnetPragueTS+12)
	store(osakaID, mainnetOsakaTS+12)

	_, err = v3.GetPayloadV3(context.Background(), cancunID)
	require.NoError(t, err)
	_, err = v3.GetPayloadV3(context.Background(), pragueID)
	requireEngineErrorCode(t, err, -38005)
	_, err = v4.GetPayloadV4(context.Background(), cancunID)
	requireEngineErrorCode(t, err, -38005)
	_, err = v4.GetPayloadV4(context.Background(), pragueID)
	require.NoError(t, err)
	_, err = v4.GetPayloadV4(context.Background(), osakaID)
	requireEngineErrorCode(t, err, -38005)
}

// forkchoiceUpdatedV3 is the method a consensus client uses to start building
// for Cancun, Prague and Osaka alike; a Prague build must come back through
// getPayloadV4 with its execution requests.
func TestEngineForkchoiceUpdatedV3BuildsPraguePayloadForGetPayloadV4(t *testing.T) {
	t.Parallel()

	api, headHash := newMainnetEngineTestAPI(t, mainnetPragueTS)
	v3 := NewEngineAPIBlob(NewBlockChainAPI(api))
	v4 := NewEngineAPIv4(NewBlockChainAPI(api))
	root := types.Hash{0x02}

	resp, err := v3.ForkchoiceUpdatedV3(context.Background(), &ForkchoiceStateV1{HeadBlockHash: headHash}, &PayloadAttributesV3{
		Timestamp:             hexutil.Uint64(mainnetPragueTS + 12),
		Withdrawals:           []*Withdrawal{},
		ParentBeaconBlockRoot: &root,
	})
	require.NoError(t, err)
	require.NotNil(t, resp.PayloadID)

	got, err := v4.GetPayloadV4(context.Background(), *resp.PayloadID)
	require.NoError(t, err)
	require.NotNil(t, got.ExecutionPayload)
	require.NotNil(t, got.ExecutionRequests)
	require.Equal(t, hexutil.Uint64(mainnetPragueTS+12), got.ExecutionPayload.Timestamp)

	_, err = v3.GetPayloadV3(context.Background(), *resp.PayloadID)
	requireEngineErrorCode(t, err, -38005)
}

// engine_exchangeCapabilities must list only methods the node serves, must not
// list itself, and must not advertise the N42-specific forkchoiceUpdatedV4 that
// shares its name with the Amsterdam method of a different shape.
func TestEngineExchangeCapabilitiesListsOnlyServedStandardMethods(t *testing.T) {
	t.Parallel()

	served := map[string]bool{}
	for _, svc := range EngineAPIs(nil) {
		typ := reflect.TypeOf(svc.Service)
		for i := 0; i < typ.NumMethod(); i++ {
			name := []rune(typ.Method(i).Name)
			served["engine_"+string(unicode.ToLower(name[0]))+string(name[1:])] = true
		}
	}

	caps, err := NewEngineAPIV1(nil).ExchangeCapabilities(context.Background(), nil)
	require.NoError(t, err)
	seen := map[string]bool{}
	for _, m := range caps {
		require.True(t, served[m], "advertised method %s is not served", m)
		require.False(t, seen[m], "duplicate method %s", m)
		seen[m] = true
	}
	require.False(t, seen["engine_exchangeCapabilities"])
	require.False(t, seen["engine_forkchoiceUpdatedV4"])
	for _, m := range []string{
		"engine_newPayloadV3", "engine_newPayloadV4",
		"engine_getPayloadV3", "engine_getPayloadV4",
		"engine_forkchoiceUpdatedV3",
		"engine_getPayloadBodiesByHashV1", "engine_getPayloadBodiesByRangeV1",
		"engine_getBlobsV1",
	} {
		require.True(t, seen[m], "missing %s", m)
	}
}

func requireNotUnsupportedFork(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	if coded, ok := err.(interface{ ErrorCode() int }); ok && coded.ErrorCode() == -38005 {
		t.Fatalf("unexpected unsupported-fork error: %v", err)
	}
	require.False(t, strings.Contains(err.Error(), "Unsupported fork"))
}

// getBlobsV1 serves Cancun and Prague heads only; Osaka moved to cell proofs.
func TestEngineGetBlobsV1RejectsOsakaHead(t *testing.T) {
	t.Parallel()

	hashes := []types.Hash{testVersionedHash(1)}
	pragueAPI, _ := newMainnetEngineTestAPI(t, mainnetPragueTS)
	_, err := NewEngineAPIv4(NewBlockChainAPI(pragueAPI)).GetBlobsV1(context.Background(), hashes)
	require.NoError(t, err)

	osakaAPI, _ := newMainnetEngineTestAPI(t, mainnetOsakaTS)
	_, err = NewEngineAPIv4(NewBlockChainAPI(osakaAPI)).GetBlobsV1(context.Background(), hashes)
	requireEngineErrorCode(t, err, -38005)
}

// N42 native chains schedule the same Engine API generation through
// PectraTime, with no PragueTime; the V3/V4 split must hold there too.
func TestEngineForkGatesTreatPectraAsPrague(t *testing.T) {
	t.Parallel()

	api, _ := newEnginePayloadTestAPI()
	api.chainConfig.PragueTime = nil
	api.chainConfig.OsakaTime = nil
	api.chainConfig.PectraTime = new(big.Int).SetUint64(1_000)
	v3 := NewEngineAPIBlob(NewBlockChainAPI(api))
	v4 := NewEngineAPIv4(NewBlockChainAPI(api))
	root := types.Hash{0x03}
	at := func(ts uint64) *ExecutionPayloadV3 {
		return &ExecutionPayloadV3{
			BlockNumber: 1, Timestamp: hexutil.Uint64(ts), GasLimit: 30_000_000,
			BaseFeePerGas: hexBigFromUint64(1), Withdrawals: []*Withdrawal{},
			BlobGasUsed: hexUint64Ptr(0), ExcessBlobGas: hexUint64Ptr(0),
		}
	}
	_, err := v3.NewPayloadV3(context.Background(), at(1_000), []types.Hash{}, &root)
	requireEngineErrorCode(t, err, -38005)
	_, err = v3.NewPayloadV3(context.Background(), at(999), []types.Hash{}, &root)
	requireNotUnsupportedFork(t, err)

	_, err = v4.NewPayloadV4(context.Background(), &ExecutionPayloadV4{
		BlockNumber: 1, Timestamp: 999, GasLimit: 30_000_000,
		BaseFeePerGas: hexBigFromUint64(1), Withdrawals: []*Withdrawal{},
		BlobGasUsed: hexUint64Ptr(0), ExcessBlobGas: hexUint64Ptr(0),
	}, []types.Hash{}, &root, []hexutil.Bytes{})
	requireEngineErrorCode(t, err, -38005)
}
