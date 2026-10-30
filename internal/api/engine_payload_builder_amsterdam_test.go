package api

import (
	"context"
	"crypto/ecdsa"
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/bal"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	internalcore "github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/params"
	"github.com/n42blockchain/N42/tests/eest"
)

// amsterdamBuildTest is an Amsterdam chain at genesis with three pending
// transfers, the second of which cannot pay for itself.
type amsterdamBuildTest struct {
	api      *API
	engine   *EngineAPIV1
	cfg      *params.ChainConfig
	genesis  *block.Block
	headHash types.Hash
	coinbase types.Address
	keys     []*amsterdamBuildKey
}

func newAmsterdamBuildTest(t *testing.T) *amsterdamBuildTest {
	t.Helper()
	modules.N42Init()
	prevTables := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() {
		kv.ChaindataTablesCfg = prevTables
	})

	cfg, err := eest.ChainConfig("Amsterdam", nil)
	require.NoError(t, err)
	db := memdb.NewTestDB(t)

	keys := make([]*amsterdamBuildKey, 3)
	alloc := conf.GenesisAlloc{}
	for i := range keys {
		k, err := crypto.GenerateKey()
		require.NoError(t, err)
		keys[i] = &amsterdamBuildKey{key: k, addr: crypto.PubkeyToAddress(k.PublicKey)}
	}
	// The second sender cannot pay for its transaction, so it fails
	// between two successful ones.
	alloc[keys[0].addr] = conf.GenesisAccount{Balance: "0x3635c9adc5dea00000"}
	alloc[keys[1].addr] = conf.GenesisAccount{Balance: "0x1"}
	alloc[keys[2].addr] = conf.GenesisAccount{Balance: "0x3635c9adc5dea00000"}
	for _, d := range internalcore.PragueSystemContractDeployments() {
		alloc[d.Address] = d.Account
	}
	alloc[params.BeaconRootsAddress] = conf.GenesisAccount{Balance: "0x0", Nonce: 1, Code: params.BeaconRootsCode}
	// The EIP-8282 queues only need to return no requests here.
	alloc[vm.BuilderDepositRequestsAddress] = conf.GenesisAccount{Balance: "0x0", Nonce: 1, Code: []byte{0x00}}
	alloc[vm.BuilderExitRequestsAddress] = conf.GenesisAccount{Balance: "0x0", Nonce: 1, Code: []byte{0x00}}
	coinbase := types.HexToAddress("0x1111111111111111111111111111111111111111")

	genesis := &internalcore.GenesisBlock{
		GenesisConfig: &conf.Genesis{
			Config:     cfg,
			Alloc:      alloc,
			GasLimit:   60_000_000,
			Difficulty: uint256.NewInt(0),
			Timestamp:  1,
			BaseFee:    uint256.NewInt(7),
			Coinbase:   coinbase,
		},
	}
	var genesisBlock *block.Block
	require.NoError(t, db.Update(context.Background(), func(tx kv.RwTx) error {
		blk, _, err := genesis.Write(tx)
		genesisBlock = blk
		return err
	}))

	// Each successful transaction sends value to a fresh account, so it
	// carries state gas as well as execution gas. Tips order the build:
	// sender 0, then the failing sender 1, then sender 2.
	signer := transaction.LatestSignerForChainID(big.NewInt(1))
	pending := map[types.Address][]*transaction.Transaction{}
	for i, k := range keys {
		to := types.Address{0xa0, byte(i)}
		tx, err := transaction.SignTx(transaction.NewTx(&transaction.DynamicFeeTx{
			ChainID:   uint256.NewInt(1),
			GasTipCap: uint256.NewInt(uint64(30 - 10*i)),
			GasFeeCap: uint256.NewInt(1000),
			Gas:       1_000_000,
			To:        &to,
			Value:     uint256.NewInt(1),
		}), signer, k.key)
		require.NoError(t, err)
		tx.SetFrom(k.addr)
		pending[k.addr] = []*transaction.Transaction{tx}
	}

	chain := &canonicalCheckChainStub{
		header: genesisBlock.Header().(*block.Header),
		blk:    genesisBlock,
		config: cfg,
		db:     db,
	}
	api := &API{
		bc:            chain,
		db:            db,
		engine:        &apiTestEngine{},
		txspool:       &mockEngineTxPool{pending: pending},
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	return &amsterdamBuildTest{
		api:      api,
		engine:   NewEngineAPIV1(NewBlockChainAPI(api)),
		cfg:      cfg,
		genesis:  genesisBlock,
		headHash: ethCompatibleBlockHash(genesisBlock, cfg),
		coinbase: coinbase,
		keys:     keys,
	}
}

// An Amsterdam payload's header gas used is the EIP-8037 block figure,
// max(cumulative execution gas, cumulative state gas), and a transaction
// that fails during the build must not reset those counters. The built
// block is replayed through the import-side execution check, which
// compares the header's gas used with the re-executed figure.
func TestStatefulPayloadBuildAmsterdamGasUsed(t *testing.T) {
	bt := newAmsterdamBuildTest(t)
	engine, genesisBlock, headHash, coinbase, keys := bt.engine, bt.genesis, bt.headHash, bt.coinbase, bt.keys
	beaconRoot := types.Hash{0x42}
	attrs := &PayloadAttributesV1{Timestamp: 2, PrevRandao: types.Hash{0x11}, SuggestedFeeRecipient: coinbase}
	result := engine.buildExecutionPayloadStateful(genesisBlock, headHash, attrs, []*Withdrawal{}, &beaconRoot)
	require.NotNil(t, result)
	require.Len(t, result.block.Transactions(), 2)

	header := result.block.Header().(*block.Header)
	var cumulative uint64
	for _, r := range result.receipts {
		cumulative = r.CumulativeGasUsed
	}
	require.NotEqual(t, cumulative, header.GasUsed, "an Amsterdam header must not carry the receipts' cumulative gas")

	require.NoError(t, engine.validatePayloadExecution(result.block, headHash, &beaconRoot, result.executionRequests))

	// The header commits to the recorded EIP-7928 list, which leaves out
	// the sender whose transaction failed, and to the slot number.
	require.NotNil(t, header.BlockAccessListHash)
	require.Equal(t, crypto.Keccak256Hash(result.blockAccessList), *header.BlockAccessListHash)
	require.NotNil(t, header.SlotNumber)
	require.Equal(t, uint64(0), *header.SlotNumber)
	list, err := bal.Decode(result.blockAccessList)
	require.NoError(t, err)
	listed := map[types.Address]bool{}
	for _, acc := range list {
		listed[acc.Address] = true
	}
	require.True(t, listed[keys[0].addr])
	require.True(t, listed[keys[2].addr])
	require.True(t, listed[params.BeaconRootsAddress])
	require.False(t, listed[keys[1].addr], "the failed transaction's sender must not be listed")

	// Validation rebuilds the list and rejects a header committing to
	// another one.
	tampered := block.CopyHeader(header)
	otherHash := crypto.Keccak256Hash([]byte{0xc0})
	tampered.BlockAccessListHash = &otherHash
	err = engine.validatePayloadExecution(result.block.WithSeal(tampered), headHash, &beaconRoot, result.executionRequests)
	require.ErrorContains(t, err, "block access list hash mismatch")

	slot := uint64(9)
	result = engine.buildExecutionPayloadStatefulWithSlot(genesisBlock, headHash, attrs, []*Withdrawal{}, &beaconRoot, &slot)
	require.NotNil(t, result)
	require.Equal(t, slot, *result.block.Header().(*block.Header).SlotNumber)
}

type amsterdamBuildKey struct {
	key  *ecdsa.PrivateKey
	addr types.Address
}

// forkchoiceUpdatedV4 starts an Amsterdam build from attributes carrying the
// slot number; getPayloadV6 returns the payload with its block access list
// and slot number, and newPayloadV5 accepts it as built. The Prague and
// Osaka getPayload methods refuse it.
func TestAmsterdamForkchoiceUpdatedV4GetPayloadV6RoundTrip(t *testing.T) {
	bt := newAmsterdamBuildTest(t)
	v4 := NewEngineAPIv4(NewBlockChainAPI(bt.api))
	ctx := context.Background()
	beaconRoot := types.Hash{0x42}
	attrs := &PayloadAttributesV4{
		Timestamp:             2,
		PrevRandao:            types.Hash{0x11},
		SuggestedFeeRecipient: bt.coinbase,
		Withdrawals:           []*Withdrawal{},
		ParentBeaconBlockRoot: &beaconRoot,
	}
	state := &ForkchoiceStateV1{HeadBlockHash: bt.headHash}

	_, err := v4.ForkchoiceUpdatedV4(ctx, state, attrs)
	requireEngineErrorCode(t, err, -38003)

	slot := hexutil.Uint64(5)
	attrs.SlotNumber = &slot
	resp, err := v4.ForkchoiceUpdatedV4(ctx, state, attrs)
	require.NoError(t, err)
	require.NotNil(t, resp.PayloadID)

	got, err := v4.GetPayloadV6(ctx, *resp.PayloadID)
	require.NoError(t, err)
	payload := got.ExecutionPayload
	require.NotNil(t, payload.SlotNumber)
	require.Equal(t, slot, *payload.SlotNumber)
	require.NotEmpty(t, payload.BlockAccessList)
	_, err = bal.Decode(payload.BlockAccessList)
	require.NoError(t, err)
	require.Len(t, payload.Transactions, 2)
	require.NotNil(t, got.BlobsBundle)
	require.NotNil(t, got.ExecutionRequests)

	_, err = v4.GetPayloadV4(ctx, *resp.PayloadID)
	requireEngineErrorCode(t, err, -38005)
	_, err = v4.GetPayloadV5(ctx, *resp.PayloadID)
	requireEngineErrorCode(t, err, -38005)

	// The payload hashes as newPayloadV5 rebuilds it, and imports.
	status, err := v4.NewPayloadV5(ctx, payload, []types.Hash{}, &beaconRoot, got.ExecutionRequests)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusValid, status.Status, statusString(status))

	// A different access list breaks the block hash.
	tampered := *payload
	tampered.BlockAccessList = append(hexutil.Bytes(nil), payload.BlockAccessList...)
	tampered.BlockAccessList[len(tampered.BlockAccessList)-1] ^= 0x01
	status, err = v4.NewPayloadV5(ctx, &tampered, []types.Hash{}, &beaconRoot, got.ExecutionRequests)
	require.NoError(t, err)
	require.Equal(t, PayloadStatusInvalid, status.Status)
}
