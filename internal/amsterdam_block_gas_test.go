package internal

import (
	"context"
	"crypto/ecdsa"
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// amsterdamGasTestBlock writes a Glamsterdam genesis and returns a block of
// transfers to fresh accounts whose header carries the EIP-8037 gas used,
// max(cumulative execution, cumulative state), as a serial run computes it.
// The transfers carry state gas, so that figure differs from the receipts'
// cumulative gas.
func amsterdamGasTestBlock(t *testing.T) (kv.RwDB, *StateProcessor, *block.Block, func(uint64) types.Hash) {
	t.Helper()
	modules.N42Init()
	prevTables := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prevTables })

	zero := big.NewInt(0)
	cfg := &params.ChainConfig{
		ChainID:               big.NewInt(1337),
		Consensus:             params.Faker,
		HomesteadBlock:        zero,
		TangerineWhistleBlock: zero,
		SpuriousDragonBlock:   zero,
		ByzantiumBlock:        zero,
		ConstantinopleBlock:   zero,
		PetersburgBlock:       zero,
		IstanbulBlock:         zero,
		BerlinBlock:           zero,
		LondonBlock:           zero,
		ShanghaiTime:          zero,
		CancunTime:            zero,
		PragueTime:            zero,
		OsakaTime:             zero,
		GlamsterdamTime:       zero,
	}

	const senders = 6
	keys := make([]*ecdsa.PrivateKey, senders)
	alloc := conf.GenesisAlloc{}
	for i := range keys {
		k, err := crypto.GenerateKey()
		require.NoError(t, err)
		keys[i] = k
		alloc[crypto.PubkeyToAddress(k.PublicKey)] = conf.GenesisAccount{Balance: "0x56bc75e2d63100000"}
	}
	for _, d := range PragueSystemContractDeployments() {
		alloc[d.Address] = d.Account
	}
	alloc[params.BeaconRootsAddress] = conf.GenesisAccount{Balance: "0x0", Nonce: 1, Code: params.BeaconRootsCode}
	alloc[vm2.BuilderDepositRequestsAddress] = conf.GenesisAccount{Balance: "0x0", Nonce: 1, Code: []byte{0x00}}
	alloc[vm2.BuilderExitRequestsAddress] = conf.GenesisAccount{Balance: "0x0", Nonce: 1, Code: []byte{0x00}}

	db := memdb.NewTestDB(t)
	genesis := &GenesisBlock{GenesisConfig: &conf.Genesis{
		Config:     cfg,
		Alloc:      alloc,
		GasLimit:   60_000_000,
		Difficulty: uint256.NewInt(0),
		Timestamp:  1,
		BaseFee:    uint256.NewInt(1),
	}}
	var genesisBlock *block.Block
	require.NoError(t, db.Update(context.Background(), func(tx kv.RwTx) error {
		blk, _, err := genesis.Write(tx)
		genesisBlock = blk
		return err
	}))

	// Transfers to fresh accounts carry state gas, so the block figure
	// differs from the receipts' cumulative gas.
	signer := transaction.LatestSignerForChainID(cfg.ChainID)
	txs := make([]*transaction.Transaction, 0, senders)
	for i, k := range keys {
		to := types.Address{0xe0, byte(i)}
		tx, err := transaction.SignTx(transaction.NewTx(&transaction.DynamicFeeTx{
			ChainID:   uint256.MustFromBig(cfg.ChainID),
			GasTipCap: uint256.NewInt(1),
			GasFeeCap: uint256.NewInt(10),
			Gas:       1_000_000,
			To:        &to,
			Value:     uint256.NewInt(1),
		}), signer, k)
		require.NoError(t, err)
		txs = append(txs, tx)
	}

	parent := genesisBlock.Header().(*block.Header)
	beaconRoot := types.Hash{0x42}
	excess, blobUsed := uint64(0), uint64(0)
	header := &block.Header{
		ParentHash:       genesisBlock.Hash(),
		Number:           uint256.NewInt(1),
		GasLimit:         parent.GasLimit,
		Time:             parent.Time + 12,
		BaseFee:          uint256.NewInt(1),
		Difficulty:       uint256.NewInt(0),
		ParentBeaconRoot: &beaconRoot,
		ExcessBlobGas:    &excess,
		BlobGasUsed:      &blobUsed,
	}
	engine := coreTFakerEngine{}
	blockHashFunc := func(uint64) types.Hash { return types.Hash{} }

	// The figure import expects, from a serial run.
	require.NoError(t, db.View(context.Background(), func(tx kv.Tx) error {
		ibs := state.New(state.NewPlainStateReader(tx))
		gp := new(common.GasPool).AddGas(header.GasLimit)
		var cumulative uint64
		for i, txn := range txs {
			ibs.Prepare(txn.Hash(), types.Hash{}, i)
			if _, _, err := ApplyTransaction(cfg, blockHashFunc, engine, nil, gp, ibs, state.NewNoopWriter(), header, txn, &cumulative, vm2.Config{}); err != nil {
				return err
			}
		}
		header.GasUsed = HeaderGasUsed(cfg, header, gp, cumulative)
		require.NotEqual(t, cumulative, header.GasUsed, "test needs state gas")
		return nil
	}))
	blk := block.NewBlockFromReceipt(header, txs, nil, nil, nil).(*block.Block)

	bcIface, err := NewBlockChain(context.Background(), genesisBlock, engine, db, nil, cfg)
	require.NoError(t, err)
	return db, NewStateProcessor(cfg, bcIface.(*BlockChain), engine), blk, blockHashFunc
}

// Block import checks a Glamsterdam header against the EIP-8037 figure, not
// the receipts' cumulative gas.
func TestProcessAmsterdamChecksBlockGasFigure(t *testing.T) {
	db, proc, blk, blockHashFunc := amsterdamGasTestBlock(t)
	require.NoError(t, db.View(context.Background(), func(tx kv.Tx) error {
		reader := state.NewPlainStateReader(tx)
		_, _, _, usedGas, err := proc.Process(blk, state.New(reader), reader, state.NewNoopWriter(), blockHashFunc)
		if err != nil {
			return err
		}
		require.Equal(t, blk.GasUsed(), usedGas)
		return nil
	}))
}

// The Block-STM executor meters each transaction with a gas pool of its own
// and sums the receipts, so it cannot reproduce the EIP-8037 figure;
// ProcessParallel and BuildParallel leave Glamsterdam blocks to the serial
// path.
func TestProcessParallelAmsterdamUsesBlockGasFigure(t *testing.T) {
	db, proc, blk, blockHashFunc := amsterdamGasTestBlock(t)
	header := blk.Header().(*block.Header)
	require.NoError(t, db.View(context.Background(), func(tx kv.Tx) error {
		reader := state.NewPlainStateReader(tx)
		_, _, _, usedGas, err := proc.ProcessParallel(blk, state.New(reader), reader, state.NewNoopWriter(), blockHashFunc)
		if err != nil {
			return err
		}
		require.Equal(t, header.GasUsed, usedGas)
		return nil
	}))

	_, err := proc.BuildParallel(header, blk.Transactions(), nil, blockHashFunc)
	require.ErrorIs(t, err, ErrParallelNotApplicable)
}
