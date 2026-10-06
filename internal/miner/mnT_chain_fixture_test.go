// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package miner

// mnT_chain_fixture_test.go builds a real, executed chain fixture for the
// miner package's tests: a genuine *internal.BlockChain over lib/kv/memdb
// whose genesis funds a few secp256k1 accounts, followed by a block of
// signed value transfers inserted through the real import path
// (BlockChain.InsertChain), using a local mnTFakerEngine consensus engine
// that computes a real state root, receipt hash and logs bloom. This
// is a package-local port of internal/coreT_chain_fixture_test.go (test
// files cannot be imported across packages), trimmed to what the miner
// package's tests need. Helpers use the mnT… prefix per the coverage-task
// convention for new test helpers.

import (
	"context"
	"crypto/ecdsa"
	"errors"
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
	"github.com/n42blockchain/N42/internal/consensus"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

var errMnTNotAHeader = errors.New("mnT fixture: header is not *block.Header")

// mnTFakerEngine is a minimal stand-in consensus engine for miner tests: it
// accepts everything, issues no rewards, and — unlike
// internal/consensus/apos.Faker (which leaves Root/ReceiptHash/Bloom to the
// caller and sleeps 2s in Seal to simulate block time) — computes a real
// state root, receipt hash and logs bloom in FinalizeAndAssemble and seals
// immediately, so commit()'s output is a genuinely checkable block. Package-
// local port of internal/coreT_chain_fixture_test.go's coreTFakerEngine
// (test files cannot be imported across packages).
type mnTFakerEngine struct{}

func (mnTFakerEngine) Author(header block.IHeader) (types.Address, error) {
	h, ok := header.(*block.Header)
	if !ok {
		return types.Address{}, nil
	}
	return h.Coinbase, nil
}

func (mnTFakerEngine) IsServiceTransaction(types.Address, consensus.SystemCall) bool { return false }
func (mnTFakerEngine) Type() params.ConsensusType                                    { return params.Faker }

func (mnTFakerEngine) VerifyHeader(consensus.ChainHeaderReader, block.IHeader, bool) error {
	return nil
}

func (mnTFakerEngine) VerifyHeaders(chain consensus.ChainHeaderReader, headers []block.IHeader, seals []bool) (chan<- struct{}, <-chan error) {
	abort := make(chan struct{})
	results := make(chan error, len(headers))
	for range headers {
		results <- nil
	}
	return abort, results
}

func (mnTFakerEngine) VerifyUncles(consensus.ConsensusChainReader, block.IBlock) error { return nil }

func (mnTFakerEngine) Prepare(chain consensus.ChainHeaderReader, header block.IHeader) error {
	h, ok := header.(*block.Header)
	if !ok {
		return nil
	}
	parent, _ := chain.GetHeaderByHash(h.ParentHash)
	if parent != nil {
		h.Difficulty = mnTFakerEngine{}.CalcDifficulty(chain, h.Time, parent)
	} else {
		h.Difficulty = uint256.NewInt(1)
	}
	return nil
}

func (mnTFakerEngine) Finalize(consensus.ChainHeaderReader, block.IHeader, *state.IntraBlockState, []*transaction.Transaction, []block.IHeader) ([]*block.Reward, map[types.Address]*uint256.Int, error) {
	return nil, nil, nil
}

func (mnTFakerEngine) FinalizeAndAssemble(_ consensus.ChainHeaderReader, header block.IHeader, ibs *state.IntraBlockState, txs []*transaction.Transaction, _ []block.IHeader, receipts []*block.Receipt) (block.IBlock, []*block.Reward, map[types.Address]*uint256.Int, error) {
	h, ok := header.(*block.Header)
	if !ok {
		return nil, nil, nil, errMnTNotAHeader
	}
	h.Root = ibs.IntermediateRoot()
	h.ReceiptHash = hash.DeriveSha(block.Receipts(receipts))
	h.Bloom = block.CreateBloom(receipts)
	return block.NewBlockFromReceipt(h, txs, nil, receipts, nil), nil, nil, nil
}

func (mnTFakerEngine) Seal(chain consensus.ChainHeaderReader, blk block.IBlock, results chan<- block.IBlock, stop <-chan struct{}) error {
	select {
	case results <- blk:
	case <-stop:
	}
	return nil
}

func (mnTFakerEngine) SealHash(header block.IHeader) types.Hash { return header.Hash() }

func (mnTFakerEngine) CalcDifficulty(chain consensus.ChainHeaderReader, t uint64, parent block.IHeader) *uint256.Int {
	n := parent.Number64()
	if n == nil {
		return uint256.NewInt(0)
	}
	return uint256.NewInt(0).Add(n, uint256.NewInt(2))
}

func (mnTFakerEngine) APIs(consensus.ConsensusChainReader) []jsonrpc.API { return nil }

func (mnTFakerEngine) Close() error { return nil }

// mnTChainFixture bundles a real, executed BlockChain plus the material a
// miner test needs to build on top of it (a chain head, funded senders, a
// signer, a working consensus engine).
type mnTChainFixture struct {
	DB     kv.RwDB
	Chain  *internal.BlockChain
	Config *params.ChainConfig
	Engine mnTFakerEngine
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

	engine := mnTFakerEngine{}
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
		GasTipCap: uint256.NewInt(2_000_000_000),
		GasFeeCap: uint256.NewInt(1_000_000_000_000),
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
