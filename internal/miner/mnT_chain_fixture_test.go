// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package miner

// mnT_chain_fixture_test.go builds a real, executed chain fixture for the
// miner package's tests: a genuine *internal.BlockChain over lib/kv/memdb
// whose genesis funds a few secp256k1 accounts, followed by a block of
// signed value transfers inserted through the real import path
// (BlockChain.InsertChain), using the real apos.Faker consensus engine. This
// is a package-local port of internal/coreT_chain_fixture_test.go (test
// files cannot be imported across packages), trimmed to what the miner
// package's tests need. Helpers use the mnT… prefix per the coverage-task
// convention for new test helpers.

import (
	"context"
	"crypto/ecdsa"
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus/apos"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// mnTChainFixture bundles a real, executed BlockChain plus the material a
// miner test needs to build on top of it (a chain head, funded senders, a
// signer, a working consensus engine).
type mnTChainFixture struct {
	DB     kv.RwDB
	Chain  *internal.BlockChain
	Config *params.ChainConfig
	Engine *apos.Faker
	Signer transaction.Signer

	Coinbase types.Address

	SenderKeys []*ecdsa.PrivateKey
	Senders    []types.Address

	// nonces tracks the next free nonce per sender across calls to
	// mnTSignedTransfer, so successive tests on the same fixture keep
	// producing valid (non-colliding) transactions.
	nonces map[types.Address]uint64
}

// mnTNewChainFixture builds a fresh executed-chain fixture over a brand new
// memdb instance. Each test gets its own: the fixture is mutated by
// inserting blocks, so it must not be shared across parallel tests.
func mnTNewChainFixture(tb testing.TB) *mnTChainFixture {
	tb.Helper()
	ctx := context.Background()

	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg

	db := memdb.New(tb.TempDir())

	cfg := &params.ChainConfig{
		ChainID:               big.NewInt(1337),
		Consensus:             params.Faker,
		HomesteadBlock:        big.NewInt(0),
		TangerineWhistleBlock: big.NewInt(0),
		SpuriousDragonBlock:   big.NewInt(0),
		ByzantiumBlock:        big.NewInt(0),
		ConstantinopleBlock:   big.NewInt(0),
		PetersburgBlock:       big.NewInt(0),
		IstanbulBlock:         big.NewInt(0),
		BerlinBlock:           big.NewInt(0),
		LondonBlock:           big.NewInt(0),
	}

	const numAccounts = 4
	keys := make([]*ecdsa.PrivateKey, 0, numAccounts)
	addrs := make([]types.Address, 0, numAccounts)
	alloc := conf.GenesisAlloc{}
	for i := 0; i < numAccounts; i++ {
		k, err := crypto.GenerateKey()
		require.NoError(tb, err)
		a := crypto.PubkeyToAddress(k.PublicKey)
		keys = append(keys, k)
		addrs = append(addrs, a)
		alloc[a] = conf.GenesisAccount{Balance: "0x56bc75e2d63100000"} // 100 ETH
	}

	coinbase := types.HexToAddress("0x2222222222222222222222222222222222222222")
	genesis := &internal.GenesisBlock{
		GenesisConfig: &conf.Genesis{
			Config:     cfg,
			Alloc:      alloc,
			Number:     0,
			GasLimit:   30_000_000,
			Difficulty: uint256.NewInt(0),
			Timestamp:  1,
			BaseFee:    uint256.NewInt(1),
			Coinbase:   coinbase,
		},
	}

	var genesisBlock *block.Block
	err := db.Update(ctx, func(tx kv.RwTx) error {
		blk, _, werr := genesis.Write(tx)
		if werr != nil {
			return werr
		}
		genesisBlock = blk
		return nil
	})
	require.NoError(tb, err)
	require.NotNil(tb, genesisBlock)

	engine := &apos.Faker{}
	bcIface, err := internal.NewBlockChain(ctx, genesisBlock, engine, db, nil, cfg)
	require.NoError(tb, err)
	bc := bcIface.(*internal.BlockChain)

	return &mnTChainFixture{
		DB:         db,
		Chain:      bc,
		Config:     cfg,
		Engine:     engine,
		Signer:     transaction.NewLondonSigner(cfg.ChainID),
		Coinbase:   coinbase,
		SenderKeys: keys,
		Senders:    addrs,
		nonces:     make(map[types.Address]uint64, numAccounts),
	}
}

// mnTSignedTransfer returns a signed DynamicFeeTx value transfer from
// senderIdx to "to", using and consuming that sender's next nonce.
func (f *mnTChainFixture) mnTSignedTransfer(tb testing.TB, senderIdx int, to types.Address, value *uint256.Int) *transaction.Transaction {
	tb.Helper()
	from := f.Senders[senderIdx]
	nonce := f.nonces[from]
	f.nonces[from] = nonce + 1
	inner := &transaction.DynamicFeeTx{
		ChainID:   uint256.MustFromBig(f.Config.ChainID),
		Nonce:     nonce,
		GasTipCap: uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(2),
		Gas:       500_000,
		To:        &to,
		Value:     value,
	}
	txn := transaction.NewTx(inner)
	signed, err := transaction.SignTx(txn, f.Signer, f.SenderKeys[senderIdx])
	require.NoError(tb, err)
	return signed
}

// mnTBuildAndInsert builds a block on top of the current head executing txs
// with the real state-transition path, finalizes it via the fixture's
// engine, and inserts it through BlockChain.InsertChain. Mirrors
// coreTBuildChainFixture's buildAndInsert helper (package internal cannot be
// imported from here, so the logic is duplicated, not shared).
func (f *mnTChainFixture) mnTBuildAndInsert(tb testing.TB, txs []*transaction.Transaction) *block.Block {
	tb.Helper()
	ctx := context.Background()
	bc := f.Chain

	parent := bc.CurrentBlock()
	parentHeader, ok := parent.Header().(*block.Header)
	require.True(tb, ok)

	header := &block.Header{
		ParentHash: parent.Hash(),
		Number:     uint256.NewInt(0).Add(parentHeader.Number, uint256.NewInt(1)),
		GasLimit:   parentHeader.GasLimit,
		Time:       parentHeader.Time + 10,
		Coinbase:   f.Coinbase,
		BaseFee:    uint256.NewInt(1),
		Difficulty: uint256.NewInt(1),
	}
	require.NoError(tb, f.Engine.Prepare(bc, header))

	var (
		receipts block.Receipts
		usedGas  uint64
		ibs      *state.IntraBlockState
	)
	roErr := f.DB.View(ctx, func(tx kv.Tx) error {
		reader := state.NewPlainStateReader(tx)
		ibs = state.New(reader)
		gp := new(common.GasPool)
		gp.AddGas(header.GasLimit)
		for i, txn := range txs {
			ibs.Prepare(txn.Hash(), types.Hash{}, i)
			receipt, _, aerr := internal.ApplyTransaction(f.Config, nil, f.Engine, nil, gp, ibs, state.NewNoopWriter(), header, txn, &usedGas, vm2.Config{})
			if aerr != nil {
				return aerr
			}
			receipts = append(receipts, receipt)
		}
		return nil
	})
	require.NoError(tb, roErr)

	header.GasUsed = usedGas
	header.Root = ibs.IntermediateRoot()
	header.ReceiptHash = hash.DeriveSha(receipts)
	header.Bloom = block.CreateBloom(receipts)

	builtIface, _, _, ferr := f.Engine.FinalizeAndAssemble(bc, header, ibs, txs, nil, receipts)
	require.NoError(tb, ferr)
	builtBlock, ok := builtIface.(*block.Block)
	require.True(tb, ok)

	n, ierr := bc.InsertChain([]block.IBlock{builtBlock})
	require.NoError(tb, ierr)
	require.Equal(tb, 1, n)

	return builtBlock
}

// mnTStubTxsPool is a minimal common.ITxsPool stub whose Pending() returns a
// fixed, pre-signed set of transactions grouped by sender. Everything else
// is unused by the code paths under test and returns zero values.
type mnTStubTxsPool struct {
	pending map[types.Address][]*transaction.Transaction
}

func (p *mnTStubTxsPool) Stop() error         { return nil }
func (p *mnTStubTxsPool) Has(types.Hash) bool { return false }
func (p *mnTStubTxsPool) Pending(enforceTips bool) map[types.Address][]*transaction.Transaction {
	return p.pending
}
func (p *mnTStubTxsPool) GetTransaction() ([]*transaction.Transaction, error) { return nil, nil }
func (p *mnTStubTxsPool) GetTx(types.Hash) *transaction.Transaction           { return nil }
func (p *mnTStubTxsPool) AddRemotes(txs []*transaction.Transaction) []error {
	return make([]error, len(txs))
}
func (p *mnTStubTxsPool) AddLocal(*transaction.Transaction) error { return nil }
func (p *mnTStubTxsPool) AddLocals(txs []*transaction.Transaction) []error {
	return make([]error, len(txs))
}
func (p *mnTStubTxsPool) Stats() (int, int, int, int) { return 0, 0, 0, 0 }
func (p *mnTStubTxsPool) Nonce(types.Address) uint64  { return 0 }
func (p *mnTStubTxsPool) Content() (map[types.Address][]*transaction.Transaction, map[types.Address][]*transaction.Transaction) {
	return p.pending, nil
}

var _ common.ITxsPool = (*mnTStubTxsPool)(nil)
