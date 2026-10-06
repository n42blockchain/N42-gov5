// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package replay

// rpT_fixture_test.go builds a real SOURCE chain datadir (genesis + a handful
// of blocks of signed value transfers, inserted through the real import path)
// directly on disk via MDBX, the same way internal/coreT_chain_fixture_test.go
// does with memdb. EngineV2 opens its source/target databases by filesystem
// path (Accede mode for the source), so the fixture must live on disk rather
// than in an in-memory KV. Helpers here use the rpT… prefix per the
// coverage-task convention for new test helpers.

import (
	"context"
	"crypto/ecdsa"
	"math/big"
	"testing"

	"github.com/c2h5oh/datasize"
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
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// rpTFakerEngine is a minimal always-accept consensus engine, mirroring
// internal's coreTFakerEngine (not importable across packages — test files
// cannot be shared between packages).
type rpTFakerEngine struct{}

func (rpTFakerEngine) Author(header block.IHeader) (types.Address, error) {
	h, ok := header.(*block.Header)
	if !ok {
		return types.Address{}, nil
	}
	return h.Coinbase, nil
}

func (rpTFakerEngine) IsServiceTransaction(types.Address, consensus.SystemCall) bool { return false }
func (rpTFakerEngine) Type() params.ConsensusType                                    { return params.Faker }

func (rpTFakerEngine) VerifyHeader(consensus.ChainHeaderReader, block.IHeader, bool) error {
	return nil
}

func (rpTFakerEngine) VerifyHeaders(chain consensus.ChainHeaderReader, headers []block.IHeader, seals []bool) (chan<- struct{}, <-chan error) {
	abort := make(chan struct{})
	results := make(chan error, len(headers))
	for range headers {
		results <- nil
	}
	return abort, results
}

func (rpTFakerEngine) VerifyUncles(consensus.ConsensusChainReader, block.IBlock) error { return nil }

func (rpTFakerEngine) Prepare(chain consensus.ChainHeaderReader, header block.IHeader) error {
	h, ok := header.(*block.Header)
	if !ok {
		return nil
	}
	parent, _ := chain.GetHeaderByHash(h.ParentHash)
	if parent != nil {
		h.Difficulty = rpTFakerEngine{}.CalcDifficulty(chain, h.Time, parent)
	} else {
		h.Difficulty = uint256.NewInt(1)
	}
	return nil
}

func (rpTFakerEngine) Finalize(consensus.ChainHeaderReader, block.IHeader, *state.IntraBlockState, []*transaction.Transaction, []block.IHeader) ([]*block.Reward, map[types.Address]*uint256.Int, error) {
	return nil, nil, nil
}

func (rpTFakerEngine) FinalizeAndAssemble(_ consensus.ChainHeaderReader, header block.IHeader, _ *state.IntraBlockState, txs []*transaction.Transaction, _ []block.IHeader, receipts []*block.Receipt) (block.IBlock, []*block.Reward, map[types.Address]*uint256.Int, error) {
	return block.NewBlockFromReceipt(header, txs, nil, receipts, nil), nil, nil, nil
}

func (rpTFakerEngine) Seal(chain consensus.ChainHeaderReader, blk block.IBlock, results chan<- block.IBlock, stop <-chan struct{}) error {
	select {
	case results <- blk:
	case <-stop:
	}
	return nil
}

func (rpTFakerEngine) SealHash(header block.IHeader) types.Hash { return header.Hash() }

func (rpTFakerEngine) CalcDifficulty(chain consensus.ChainHeaderReader, t uint64, parent block.IHeader) *uint256.Int {
	n := parent.Number64()
	if n == nil {
		return uint256.NewInt(0)
	}
	return uint256.NewInt(0).Add(n, uint256.NewInt(2))
}

func (rpTFakerEngine) APIs(consensus.ConsensusChainReader) []jsonrpc.API { return nil }

func (rpTFakerEngine) Close() error { return nil }

// rpTChainConfig is the shared chain config used by both the on-disk source
// chain and the EngineV2 replay target — all forks active from genesis, a
// fixed test chain ID, Faker consensus.
func rpTChainConfig() *params.ChainConfig {
	return &params.ChainConfig{
		ChainID:               big.NewInt(99901),
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
}

// rpTSourceFixture is a real, executed chain persisted on disk (MDBX) that
// EngineV2 can read as its SourcePath.
type rpTSourceFixture struct {
	Dir    string // datadir root (chaindata lives at Dir+"/chaindata")
	Config *params.ChainConfig

	SenderKeys []*ecdsa.PrivateKey
	Senders    []types.Address

	Blocks []*block.Block // genesis excluded, insertion order
}

// rpTBuildSourceChain builds a fresh on-disk source chain with `numBlocks`
// single-transfer blocks on top of genesis. The DB is closed before return
// so EngineV2 (Accede mode) can reopen it cleanly.
func rpTBuildSourceChain(tb testing.TB, numBlocks int) *rpTSourceFixture {
	tb.Helper()
	ctx := context.Background()

	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg

	dir := tb.TempDir()
	db, err := mdbx.NewMDBX(log2.New()).
		Path(dir + "/chaindata").
		Label(kv.ChainDB).
		WithTableCfg(func(_ kv.TableCfg) kv.TableCfg { return kv.ChaindataTablesCfg }).
		MapSize(1 * datasize.GB).
		Open(ctx)
	require.NoError(tb, err)
	defer db.Close()

	cfg := rpTChainConfig()

	const numAccounts = 3
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
	err = db.Update(ctx, func(tx kv.RwTx) error {
		blk, _, werr := genesis.Write(tx)
		if werr != nil {
			return werr
		}
		genesisBlock = blk
		return nil
	})
	require.NoError(tb, err)
	require.NotNil(tb, genesisBlock)

	engine := rpTFakerEngine{}
	bcIface, err := internal.NewBlockChain(ctx, genesisBlock, engine, db, nil, cfg)
	require.NoError(tb, err)
	bc := bcIface.(interface {
		CurrentBlock() block.IBlock
		InsertChain([]block.IBlock) (int, error)
	})

	signer := transaction.NewLondonSigner(cfg.ChainID)
	fixture := &rpTSourceFixture{
		Dir:        dir,
		Config:     cfg,
		SenderKeys: keys,
		Senders:    addrs,
	}

	nonces := make(map[types.Address]uint64, numAccounts)

	for b := 0; b < numBlocks; b++ {
		parent := bc.CurrentBlock()
		parentHeader, ok := parent.Header().(*block.Header)
		require.True(tb, ok)

		header := &block.Header{
			ParentHash: parent.Hash(),
			Number:     uint256.NewInt(0).Add(parentHeader.Number, uint256.NewInt(1)),
			GasLimit:   parentHeader.GasLimit,
			Time:       parentHeader.Time + 10,
			Coinbase:   coinbase,
			BaseFee:    uint256.NewInt(1),
			Difficulty: uint256.NewInt(1),
		}
		require.NoError(tb, engine.Prepare(bc.(consensus.ChainHeaderReader), header))

		from := b % numAccounts
		to := addrs[(b+1)%numAccounts]
		nonce := nonces[addrs[from]]
		nonces[addrs[from]] = nonce + 1
		inner := &transaction.DynamicFeeTx{
			ChainID:   uint256.MustFromBig(cfg.ChainID),
			Nonce:     nonce,
			GasTipCap: uint256.NewInt(1),
			GasFeeCap: uint256.NewInt(2),
			Gas:       100_000,
			To:        &to,
			Value:     uint256.NewInt(1_000_000_000_000_000),
		}
		txn := transaction.NewTx(inner)
		signed, serr := transaction.SignTx(txn, signer, keys[from])
		require.NoError(tb, serr)
		txs := []*transaction.Transaction{signed}

		var (
			receipts block.Receipts
			usedGas  uint64
			ibs      *state.IntraBlockState
		)
		roErr := db.View(ctx, func(tx kv.Tx) error {
			reader := state.NewPlainStateReader(tx)
			ibs = state.New(reader)
			gp := new(common.GasPool)
			gp.AddGas(header.GasLimit)
			for i, t := range txs {
				ibs.Prepare(t.Hash(), types.Hash{}, i)
				receipt, _, aerr := internal.ApplyTransaction(cfg, nil, engine, nil, gp, ibs, state.NewNoopWriter(), header, t, &usedGas, vm2.Config{})
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

		builtIface, _, _, ferr := engine.FinalizeAndAssemble(bc.(consensus.ChainHeaderReader), header, ibs, txs, nil, receipts)
		require.NoError(tb, ferr)
		builtBlock, ok := builtIface.(*block.Block)
		require.True(tb, ok)

		n, ierr := bc.InsertChain([]block.IBlock{builtBlock})
		require.NoError(tb, ierr)
		require.Equal(tb, 1, n)

		fixture.Blocks = append(fixture.Blocks, builtBlock)
	}

	if closer, ok := bcIface.(interface{ Stop() }); ok {
		closer.Stop()
	}

	return fixture
}
