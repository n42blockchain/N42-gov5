package api

// apix_chain_fixture_test.go builds a real, EXECUTED chain fixture for
// internal/api tests: a genuine *internal.BlockChain over lib/kv/memdb whose
// genesis funds a few accounts, followed by several blocks of signed value
// transfers (plus one block with a tiny contract creation and a call that
// emits a log) inserted through the real import path (BlockChain.InsertChain).
// That populates PlainState, receipts, logs, TxLookup and the canonical chain
// exactly as a live node would, which is what unlocks meaningful coverage of
// the engine/debug/otterscan/eth handlers: they read real execution
// artifacts instead of hand-built stubs.
//
// The fixture is built once per test binary (sync.Once) and must be treated
// as read-only by every caller.

import (
	"context"
	"crypto/ecdsa"
	"math/big"
	"sync"
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
	internalcore "github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// apiXFakerEngine is a minimal stand-in for apos.Faker: a consensus engine
// that accepts everything and performs no rewards. internal/consensus/apos
// cannot be imported from internal/api tests (apos imports internal/api for
// its RPC surface, which would be an import cycle), so the handful of
// methods actually exercised by the fixture are reproduced here instead.
type apiXFakerEngine struct{}

func (apiXFakerEngine) Author(header block.IHeader) (types.Address, error) {
	h, ok := header.(*block.Header)
	if !ok {
		return types.Address{}, nil
	}
	return h.Coinbase, nil
}

func (apiXFakerEngine) IsServiceTransaction(types.Address, consensus.SystemCall) bool { return false }
func (apiXFakerEngine) Type() params.ConsensusType                                    { return params.Faker }

func (apiXFakerEngine) VerifyHeader(consensus.ChainHeaderReader, block.IHeader, bool) error { return nil }

func (apiXFakerEngine) VerifyHeaders(chain consensus.ChainHeaderReader, headers []block.IHeader, seals []bool) (chan<- struct{}, <-chan error) {
	abort := make(chan struct{})
	results := make(chan error, len(headers))
	for range headers {
		results <- nil
	}
	return abort, results
}

func (apiXFakerEngine) VerifyUncles(consensus.ConsensusChainReader, block.IBlock) error { return nil }

func (apiXFakerEngine) Prepare(chain consensus.ChainHeaderReader, header block.IHeader) error {
	h, ok := header.(*block.Header)
	if !ok {
		return nil
	}
	parent, _ := chain.GetHeaderByHash(h.ParentHash)
	if parent != nil {
		h.Difficulty = apiXFakerEngine{}.CalcDifficulty(chain, h.Time, parent)
	} else {
		h.Difficulty = uint256.NewInt(1)
	}
	return nil
}

func (apiXFakerEngine) Finalize(consensus.ChainHeaderReader, block.IHeader, *state.IntraBlockState, []*transaction.Transaction, []block.IHeader) ([]*block.Reward, map[types.Address]*uint256.Int, error) {
	return nil, nil, nil
}

func (apiXFakerEngine) FinalizeAndAssemble(_ consensus.ChainHeaderReader, header block.IHeader, _ *state.IntraBlockState, txs []*transaction.Transaction, _ []block.IHeader, receipts []*block.Receipt) (block.IBlock, []*block.Reward, map[types.Address]*uint256.Int, error) {
	return block.NewBlockFromReceipt(header, txs, nil, receipts, nil), nil, nil, nil
}

func (apiXFakerEngine) Seal(chain consensus.ChainHeaderReader, blk block.IBlock, results chan<- block.IBlock, stop <-chan struct{}) error {
	select {
	case results <- blk:
	case <-stop:
	}
	return nil
}

func (apiXFakerEngine) SealHash(header block.IHeader) types.Hash { return header.Hash() }

func (apiXFakerEngine) CalcDifficulty(chain consensus.ChainHeaderReader, t uint64, parent block.IHeader) *uint256.Int {
	parentNumber, err := requireHeaderNumberAlias(parent)
	if err != nil {
		return uint256.NewInt(0)
	}
	return uint256.NewInt(0).Add(parentNumber, uint256.NewInt(2))
}

func (apiXFakerEngine) APIs(consensus.ConsensusChainReader) []jsonrpc.API { return nil }

func (apiXFakerEngine) Close() error { return nil }

// requireHeaderNumberAlias extracts a header's number without depending on
// the unexported helper of the same shape in the internal package.
func requireHeaderNumberAlias(h block.IHeader) (*uint256.Int, error) {
	n := h.Number64()
	if n == nil {
		return nil, errAliasHeaderNumberUnavailable
	}
	return n, nil
}

var errAliasHeaderNumberUnavailable = &apiXHeaderNumberErr{}

type apiXHeaderNumberErr struct{}

func (*apiXHeaderNumberErr) Error() string { return "header number unavailable" }

// apiXChainFixture bundles a real, executed BlockChain plus the material a
// test needs to make assertions about it.
type apiXChainFixture struct {
	DB     kv.RwDB
	Chain  *internalcore.BlockChain
	Config *params.ChainConfig

	Genesis *block.Block
	Blocks  []*block.Block // genesis excluded, in insertion order

	SenderKeys []*ecdsa.PrivateKey
	Senders    []types.Address
	Recipients []types.Address

	// ValueTransferTxs is every signed value-transfer transaction inserted,
	// in insertion order, flattened across blocks.
	ValueTransferTxs []*transaction.Transaction

	// ContractAddr is the address of the tiny contract created in the
	// contract block; CreateTx deployed it and CallTx invoked it (emitting
	// one LOG0 entry).
	ContractAddr        types.Address
	CreateTx             *transaction.Transaction
	CallTx               *transaction.Transaction
	ContractBlockNumber  uint64
}

var (
	apiXFixtureOnce  sync.Once
	apiXFixtureValue *apiXChainFixture
)

// apiXGetChainFixture returns the shared executed-chain fixture, building it
// once per test binary. Safe to call from multiple tests/subtests; the
// fixture must not be mutated by callers.
func apiXGetChainFixture(tb testing.TB) *apiXChainFixture {
	tb.Helper()
	apiXFixtureOnce.Do(func() {
		apiXFixtureValue = apiXBuildChainFixture(tb)
	})
	if apiXFixtureValue == nil {
		tb.Fatal("apix chain fixture failed to build")
	}
	return apiXFixtureValue
}

// apiXTinyLogRuntime is the runtime code of the fixture's tiny contract: it
// just emits one topic-less, data-less LOG0 and stops. Bytes:
//
//	60 00  PUSH1 0x00   (size)
//	60 00  PUSH1 0x00   (offset)
//	a0     LOG0
//	00     STOP
var apiXTinyLogRuntime = []byte{0x60, 0x00, 0x60, 0x00, 0xa0, 0x00}

// apiXTinyLogInitCode wraps apiXTinyLogRuntime in a standard CODECOPY+RETURN
// deployer so a plain contract-creation transaction installs it.
func apiXTinyLogInitCode() []byte {
	runtime := apiXTinyLogRuntime
	runtimeLen := byte(len(runtime))
	// Init code: PUSH1 len, PUSH1 runtimeOffset, PUSH1 0, CODECOPY,
	// PUSH1 len, PUSH1 0, RETURN, then the runtime bytes.
	init := []byte{
		0x60, runtimeLen, // PUSH1 len
		0x60, 0x00, // PUSH1 runtimeOffset (patched below)
		0x60, 0x00, // PUSH1 0 (dest)
		0x39,             // CODECOPY
		0x60, runtimeLen, // PUSH1 len
		0x60, 0x00, // PUSH1 0 (offset)
		0xf3, // RETURN
	}
	init[3] = byte(len(init)) // runtime starts right after the init prefix
	return append(init, runtime...)
}

// apiXBuildChainFixture does the actual construction: real genesis, real
// BlockChain, real signed transactions, real StateProcessor execution, real
// InsertChain. Not parallel-safe with itself (guarded by apiXFixtureOnce).
func apiXBuildChainFixture(tb testing.TB) *apiXChainFixture {
	tb.Helper()
	ctx := context.Background()

	modules.N42Init()
	prevTables := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	tb.Cleanup(func() { kv.ChaindataTablesCfg = prevTables })

	db := memdb.NewTestDB(tb)

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

	coinbase := types.HexToAddress("0x1111111111111111111111111111111111111111")
	genesis := &internalcore.GenesisBlock{
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

	engine := apiXFakerEngine{}
	bcIface, err := internalcore.NewBlockChain(ctx, genesisBlock, engine, db, nil, cfg)
	require.NoError(tb, err)
	bc := bcIface.(*internalcore.BlockChain)

	signer := transaction.NewLondonSigner(cfg.ChainID)
	fixture := &apiXChainFixture{
		DB:         db,
		Chain:      bc,
		Config:     cfg,
		Genesis:    genesisBlock,
		SenderKeys: keys,
		Senders:    addrs,
	}

	// Per-sender nonce tracking across the whole fixture build.
	nonces := make(map[types.Address]uint64, numAccounts)

	buildAndInsert := func(txs []*transaction.Transaction) *block.Block {
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
		require.NoError(tb, engine.Prepare(bc, header))

		// Discovery pass: execute the candidate transactions read-only to
		// learn gas used / receipts / resulting state root, exactly as the
		// real stateful payload builder does (see engine_payload_stateful.go).
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
			for i, txn := range txs {
				ibs.Prepare(txn.Hash(), types.Hash{}, i)
				receipt, _, aerr := internalcore.ApplyTransaction(cfg, nil, engine, nil, gp, ibs, state.NewNoopWriter(), header, txn, &usedGas, vm2.Config{})
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

		builtIface, _, _, ferr := engine.FinalizeAndAssemble(bc, header, ibs, txs, nil, receipts)
		require.NoError(tb, ferr)
		builtBlock, ok := builtIface.(*block.Block)
		require.True(tb, ok)

		n, ierr := bc.InsertChain([]block.IBlock{builtBlock})
		require.NoError(tb, ierr)
		require.Equal(tb, 1, n)

		fixture.Blocks = append(fixture.Blocks, builtBlock)
		return builtBlock
	}

	nextTx := func(from int, to types.Address, value *uint256.Int, data []byte, create bool) *transaction.Transaction {
		fromAddr := addrs[from]
		nonce := nonces[fromAddr]
		nonces[fromAddr] = nonce + 1
		var toPtr *types.Address
		if !create {
			toPtr = &to
		}
		inner := &transaction.DynamicFeeTx{
			ChainID:   uint256.MustFromBig(cfg.ChainID),
			Nonce:     nonce,
			GasTipCap: uint256.NewInt(1),
			GasFeeCap: uint256.NewInt(2),
			Gas:       500_000,
			To:        toPtr,
			Value:     value,
			Data:      data,
		}
		txn := transaction.NewTx(inner)
		signed, serr := transaction.SignTx(txn, signer, keys[from])
		require.NoError(tb, serr)
		return signed
	}

	// Blocks 1-3: plain value transfers, round-robin among the funded accounts.
	for b := 0; b < 3; b++ {
		from := b % numAccounts
		to := addrs[(b+1)%numAccounts]
		txn := nextTx(from, to, uint256.NewInt(1_000_000_000_000_000), nil, false)
		buildAndInsert([]*transaction.Transaction{txn})
		fixture.ValueTransferTxs = append(fixture.ValueTransferTxs, txn)
		fixture.Recipients = append(fixture.Recipients, to)
	}

	// Block 4: a tiny contract creation, in the same block as another value
	// transfer so the block exercises both paths.
	creator := 0
	createTx := nextTx(creator, types.Address{}, uint256.NewInt(0), apiXTinyLogInitCode(), true)
	filler := nextTx(1, addrs[2], uint256.NewInt(1_000_000), nil, false)
	contractAddr := crypto.CreateAddress(addrs[creator], createTx.Nonce())
	blk4 := buildAndInsert([]*transaction.Transaction{createTx, filler})
	fixture.ContractAddr = contractAddr
	fixture.CreateTx = createTx
	fixture.ValueTransferTxs = append(fixture.ValueTransferTxs, filler)
	blk4Num := blk4.Header().(*block.Header).Number.Uint64()

	// Block 5: call the freshly deployed contract so it emits its LOG0.
	callTx := nextTx(0, contractAddr, uint256.NewInt(0), nil, false)
	buildAndInsert([]*transaction.Transaction{callTx})
	fixture.CallTx = callTx
	fixture.ContractBlockNumber = blk4Num

	return fixture
}
