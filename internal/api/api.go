// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package api

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/accounts"
	"github.com/n42blockchain/N42/common"
	avmtypes "github.com/n42blockchain/N42/common/avmtypes"
	avmcommon "github.com/n42blockchain/N42/common/avmutil"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/api/filters"
	"github.com/n42blockchain/N42/internal/api/rpchelper"
	"github.com/n42blockchain/N42/internal/avm/abi"
	"github.com/n42blockchain/N42/internal/consensus"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	event "github.com/n42blockchain/N42/modules/event/v2"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

const (
	// baseFee is the minimum base fee for gas price estimation in Wei
	baseFee       = 5000000
	rpcEVMTimeout = 5 * time.Second
	rpcGasCap     = 50000000

	// maxCallDataSize is the maximum allowed size for eth_call calldata (128KB).
	maxCallDataSize = 128 * 1024
)

// P2PAdmin is the minimal interface for P2P peer management exposed via admin_* RPC methods.
// Implementations must be safe for concurrent use.
type P2PAdmin interface {
	// PeerInfos returns a snapshot of currently connected peer information.
	PeerInfos() []*PeerInfo
	// SelfNodeID returns the local node's libp2p peer ID string.
	SelfNodeID() string
	// SelfEnode returns the local node's devp2p enode URL when available.
	SelfEnode() string
	// SelfENR returns the local node's serialised ENR string (empty if unavailable).
	SelfENR() string
	// SelfListenAddrs returns the multiaddrs the node is listening on.
	SelfListenAddrs() []string
	// AddPeer connects to the peer at the given multiaddr string.
	AddPeer(addr string) error
	// RemovePeer disconnects from the peer with the given peer ID string.
	RemovePeer(peerID string) error
	// HighestPeerBlock returns the highest block number reported by any connected peer.
	// Returns 0 when no peers are connected or no chain state is available.
	HighestPeerBlock() uint64
}

// MinerAdmin is the minimal interface for miner control exposed via miner_* RPC methods.
type MinerAdmin interface {
	// Mining reports whether the node is currently producing blocks.
	Mining() bool
	// SetCoinbase sets the coinbase (reward) address for block production.
	SetCoinbase(addr types.Address)
}

// API provides an Ethereum-compatible JSON-RPC API to access blockchain data.
type API struct {
	db      kv.RwDB
	bc      common.IBlockChain
	engine  consensus.Engine
	txspool common.ITxsPool

	accountManager *accounts.Manager
	chainConfig    *params.ChainConfig

	gpo   *Oracle
	p2p   P2PAdmin   // optional; nil until SetP2P is called
	miner MinerAdmin // optional; nil until SetMiner is called

	engineOverlay *engineOverlay
}

// NewAPI creates a new protocol API.
func NewAPI(bc common.IBlockChain, db kv.RwDB, engine consensus.Engine, txspool common.ITxsPool, accountManager *accounts.Manager, config *params.ChainConfig) *API {
	return &API{
		db:             db,
		bc:             bc,
		engine:         engine,
		txspool:        txspool,
		accountManager: accountManager,
		chainConfig:    config,
		engineOverlay:  newEngineOverlay(),
	}
}

func (api *API) SetGpo(gpo *Oracle) {
	api.gpo = gpo
}

// SetP2P wires a P2P backend into the API so that admin_* methods can return real peer data.
func (api *API) SetP2P(p P2PAdmin) {
	api.p2p = p
}

// SetMiner wires a miner backend into the API so that miner_* methods reflect actual state.
func (api *API) SetMiner(m MinerAdmin) {
	api.miner = m
}

func (api *API) Apis() []jsonrpc.API {
	nonceLock := new(AddrLocker)
	return []jsonrpc.API{
		{
			Namespace: "eth",
			Service:   NewBlockChainAPI(api),
		}, {
			Namespace: "eth",
			Service:   NewN42API(api),
		}, {
			Namespace: "eth",
			Service:   NewTransactionAPI(api, nonceLock),
		}, {
			Namespace: "web3",
			Service:   &Web3API{api},
		}, {
			Namespace: "net",
			Service:   NewNetAPI(api, chainIDUint64(api.GetChainConfig())),
		},
		{
			Namespace: "debug",
			Service:   NewDebugAPI(api),
		},
		{
			Namespace: "txpool",
			Service:   NewTxsPoolAPI(api),
		}, {
			Namespace: "eth",
			Service:   filters.NewFilterAPI(api, 5*time.Minute),
		},
	}
}

func chainIDUint64(cfg *params.ChainConfig) uint64 {
	if cfg == nil || cfg.ChainID == nil {
		return 0
	}
	return cfg.ChainID.Uint64()
}

func (n *API) TxsPool() common.ITxsPool       { return n.txspool }
func (n *API) Database() kv.RwDB              { return n.db }
func (n *API) Engine() consensus.Engine       { return n.engine }
func (n *API) BlockChain() common.IBlockChain { return n.bc }
func (n *API) GetEvm(ctx context.Context, msg internal.Message, ibs evmtypes.IntraBlockState, header block.IHeader, vmConfig *vm2.Config) (*vm2.EVM, func() error, error) {
	vmError := func() error { return nil }
	if vmConfig == nil {
		vmConfig = &vm2.Config{}
	}

	concreteHeader, ok := header.(*block.Header)
	if !ok {
		return nil, nil, errors.New("GetEvm: invalid header type assertion")
	}

	txContext := internal.NewEVMTxContext(msg)
	context := internal.NewEVMBlockContext(concreteHeader, internal.GetHashFn(concreteHeader, nil), n.engine, nil)

	return vm2.NewEVM(context, txContext, ibs, n.GetChainConfig(), *vmConfig), vmError, nil
}

func (n *API) State(tx kv.Tx, blockNrOrHash jsonrpc.BlockNumberOrHash) evmtypes.IntraBlockState {
	if ibs := n.overlayState(tx, blockNrOrHash); ibs != nil {
		return ibs
	}
	_, blockHash, err := rpchelper.GetCanonicalBlockNumber(blockNrOrHash, tx)
	if err != nil {
		return nil
	}

	blockNr := rawdb.ReadHeaderNumber(tx, blockHash)
	if nil == blockNr {
		return nil
	}

	stateReader := state.NewPlainState(tx, *blockNr+1)
	return state.New(stateReader)
}

func (n *API) blockByEngineHash(hash types.Hash) block.IBlock {
	if n == nil || n.bc == nil || hash == (types.Hash{}) {
		return nil
	}
	if n.engineOverlay != nil {
		if blk := n.engineOverlay.blockByHash(hash); blk != nil {
			return blk
		}
	}
	blk, _ := n.bc.GetBlockByHash(hash)
	return blk
}

// ForkchoiceHeadBlock returns the latest block visible through the Engine overlay.
func (n *API) ForkchoiceHeadBlock() block.IBlock {
	return n.resolveForkchoiceTaggedBlock(jsonrpc.LatestBlockNumber)
}

// ForkchoiceBlockByNumber returns the canonical block visible through the Engine overlay.
func (n *API) ForkchoiceBlockByNumber(number uint64) block.IBlock {
	if n == nil || n.bc == nil {
		return nil
	}
	if n.engineOverlay != nil {
		if blk := n.engineOverlay.blockByNumber(number); blk != nil {
			return blk
		}
	}
	blk, _ := n.bc.GetBlockByNumber(uint256.NewInt(number))
	return blk
}

// ForkchoiceBlockByHash returns a block visible through the Engine overlay or base chain.
func (n *API) ForkchoiceBlockByHash(hash types.Hash) block.IBlock {
	return n.blockByEngineHash(hash)
}

// ForkchoiceBlockHash returns the Engine-visible hash for the given block.
func (n *API) ForkchoiceBlockHash(blk block.IBlock) types.Hash {
	if blk == nil {
		return types.Hash{}
	}
	if n != nil && n.engineOverlay != nil {
		return n.engineOverlay.hashForBlock(blk, n.chainConfig)
	}
	return ethCompatibleBlockHash(blk, n.chainConfig)
}

func (n *API) resolveForkchoiceTaggedBlock(number jsonrpc.BlockNumber) block.IBlock {
	if n == nil || n.bc == nil {
		return nil
	}
	current := n.bc.CurrentBlock()
	if n.engineOverlay == nil {
		if number == jsonrpc.LatestBlockNumber || number == jsonrpc.PendingBlockNumber || number == jsonrpc.SafeBlockNumber || number == jsonrpc.FinalizedBlockNumber {
			return current
		}
		return nil
	}
	switch number {
	case jsonrpc.LatestBlockNumber, jsonrpc.PendingBlockNumber:
		return n.engineOverlay.headBlock(current)
	case jsonrpc.SafeBlockNumber:
		safeHash, _ := n.engineOverlay.forkchoiceHashes()
		return n.blockByEngineHash(safeHash)
	case jsonrpc.FinalizedBlockNumber:
		_, finalizedHash := n.engineOverlay.forkchoiceHashes()
		return n.blockByEngineHash(finalizedHash)
	default:
		return nil
	}
}

func (n *API) resolveForkchoiceTaggedHeader(number jsonrpc.BlockNumber) *block.Header {
	blk := n.resolveForkchoiceTaggedBlock(number)
	if blk == nil {
		return nil
	}
	return blockHeader(blk)
}

func (n *API) overlayState(tx kv.Tx, blockNrOrHash jsonrpc.BlockNumberOrHash) *state.IntraBlockState {
	if n == nil || n.engineOverlay == nil || n.bc == nil || n.db == nil || tx == nil {
		return nil
	}
	var (
		blk  block.IBlock
		hash types.Hash
	)
	if blockNr, ok := blockNrOrHash.Number(); ok {
		switch blockNr {
		case jsonrpc.LatestBlockNumber, jsonrpc.PendingBlockNumber:
			blk = n.resolveForkchoiceTaggedBlock(blockNr)
		case jsonrpc.SafeBlockNumber, jsonrpc.FinalizedBlockNumber:
			blk = n.resolveForkchoiceTaggedBlock(blockNr)
		default:
			if blockNr >= 0 {
				blk = n.engineOverlay.blockByNumber(uint64(blockNr.Int64()))
			}
		}
		if blk == nil {
			return nil
		}
		hash = n.engineOverlay.hashForBlock(blk, n.chainConfig)
	} else if blockHash, ok := blockNrOrHash.Hash(); ok {
		hash = blockHash
		blk = n.engineOverlay.blockByHash(blockHash)
	} else {
		return nil
	}
	if blk == nil {
		return nil
	}
	overlayState := n.engineOverlay.stateOverlayByHash(hash)
	if overlayState == nil {
		return nil
	}
	_, ibs, err := newOverlayStateView(n.db, tx, uint64FromUint256OrZero(blk.Number64()), overlayState)
	if err != nil {
		return nil
	}
	return ibs
}

func (n *API) GetChainConfig() *params.ChainConfig {
	return n.chainConfig
}

// doWeb3Call executes a read-only EVM call for the web3:// gateway.
// Returns the raw output bytes from the contract execution.
func (n *API) doWeb3Call(ctx context.Context, tx kv.Tx, to types.Address, calldata []byte, blockNrOrHash jsonrpc.BlockNumberOrHash) ([]byte, error) {
	ibs := n.State(tx, blockNrOrHash)
	if ibs == nil {
		return nil, fmt.Errorf("state not available")
	}

	concreteHeader, ok := n.bc.CurrentBlock().Header().(*block.Header)
	if !ok || concreteHeader == nil {
		return nil, fmt.Errorf("header not available")
	}

	msg := transaction.NewMessage(types.Address{}, &to, 0, uint256.NewInt(0), 50000000,
		uint256.NewInt(0), uint256.NewInt(0), uint256.NewInt(0), nil, nil,
		calldata, nil, false, true)

	vmCfg := vm2.Config{NoBaseFee: true}
	blockCtx := internal.NewEVMBlockContext(concreteHeader, internal.GetHashFn(concreteHeader, nil), n.engine, nil)
	txCtx := internal.NewEVMTxContext(msg)
	evm := vm2.NewEVM(blockCtx, txCtx, ibs, n.chainConfig, vmCfg)

	gp := new(common.GasPool).AddGas(50000000)
	result, err := internal.ApplyMessage(evm, msg, gp, true, false)
	if err != nil {
		return nil, err
	}
	if result.Err != nil {
		return nil, result.Err
	}
	return result.Return(), nil
}

func (n *API) RPCGasCap() uint64 {
	return rpcGasCap
}

// BlockChainAPI provides an API to access Ethereum blockchain data.
type BlockChainAPI struct {
	api *API
}

// NewBlockChainAPI creates a new blockchain API.
func NewBlockChainAPI(api *API) *BlockChainAPI {
	return &BlockChainAPI{api}
}

// ChainId returns the chain ID used for signing replay-protected transactions.
func (api *BlockChainAPI) ChainId() *hexutil.Big {
	if api == nil || api.api == nil {
		return nil
	}
	cfg := api.api.GetChainConfig()
	if cfg == nil || cfg.ChainID == nil {
		return nil
	}
	return (*hexutil.Big)(cfg.ChainID)
}

// GetBalance returns the amount of wei for the given address at the given block.
func (s *BlockChainAPI) GetBalance(ctx context.Context, address avmcommon.Address, blockNrOrHash jsonrpc.BlockNumberOrHash) (*hexutil.Big, error) {
	tx, err := s.api.db.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	state := s.api.State(tx, blockNrOrHash)
	if state == nil {
		return nil, nil
	}
	balance := state.GetBalance(*avmtypes.ToastAddress(&address))
	return (*hexutil.Big)(balance.ToBig()), nil
}

func (s *BlockChainAPI) BlockNumber() hexutil.Uint64 {
	currentBlock := s.api.BlockChain().CurrentBlock()
	if s.api != nil && s.api.engineOverlay != nil {
		currentBlock = s.api.engineOverlay.headBlock(currentBlock)
	}
	if currentBlock == nil {
		return hexutil.Uint64(0)
	}
	header := currentBlock.Header()
	if header == nil {
		return hexutil.Uint64(0)
	}
	num := header.Number64()
	if num == nil {
		return hexutil.Uint64(0)
	}
	return hexutil.Uint64(num.Uint64())
}

// EarliestBlock returns the earliest block number that still has full data
// available. After EIP-4444 style history expiry, older blocks are pruned
// and this value advances. Returns 0x0 when all history is available.
func (s *BlockChainAPI) EarliestBlock() hexutil.Uint64 {
	return hexutil.Uint64(s.api.BlockChain().EarliestBlock())
}

func normalizeBlockNumberForHistory(number jsonrpc.BlockNumber, bc common.IBlockChain) (jsonrpc.BlockNumber, error) {
	if number == jsonrpc.PendingBlockNumber || number == jsonrpc.LatestBlockNumber || number == jsonrpc.SafeBlockNumber || number == jsonrpc.FinalizedBlockNumber {
		return number, nil
	}
	if number < 0 {
		return 0, fmt.Errorf("invalid block number: %d", number)
	}
	if number == jsonrpc.EarliestBlockNumber && bc != nil {
		if earliest := bc.EarliestBlock(); earliest > 0 {
			return jsonrpc.BlockNumber(earliest), nil
		}
	}
	return number, nil
}

// GetCode returns the code stored at the given address in the state for the given block.
func (s *BlockChainAPI) GetCode(ctx context.Context, address avmcommon.Address, blockNrOrHash jsonrpc.BlockNumberOrHash) (hexutil.Bytes, error) {
	tx, err := s.api.db.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	state := s.api.State(tx, blockNrOrHash)
	if state == nil {
		return nil, nil
	}
	code := state.GetCode(*avmtypes.ToastAddress(&address))
	return code, nil
}

// GetStorageAt returns the storage from the state at the given address, key and
// block number. The rpc.LatestBlockNumber and rpc.PendingBlockNumber metadata block
// numbers are also allowed.
func (s *BlockChainAPI) GetStorageAt(ctx context.Context, address types.Address, key string, blockNrOrHash jsonrpc.BlockNumberOrHash) (hexutil.Bytes, error) {
	tx, err := s.api.db.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	state := s.api.State(tx, blockNrOrHash)
	if state == nil {
		return nil, nil
	}
	var va uint256.Int
	k := types.HexToHash(key)
	state.GetState(address, &k, &va)
	return va.Bytes(), nil
}

// GetStorageValues returns multiple storage values for a contract in a single call.
// EIP-7834: batch storage value retrieval.
func (s *BlockChainAPI) GetStorageValues(ctx context.Context, address types.Address, keys []string, blockNrOrHash *uint64) ([]string, error) {
	const maxKeys = 1024
	if len(keys) > maxKeys {
		return nil, fmt.Errorf("too many keys: %d, max %d", len(keys), maxKeys)
	}

	tx, err := s.api.db.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Convert *uint64 to BlockNumberOrHash; nil means latest.
	var bnOrHash jsonrpc.BlockNumberOrHash
	if blockNrOrHash != nil {
		bn := jsonrpc.BlockNumber(int64(*blockNrOrHash))
		bnOrHash = jsonrpc.BlockNumberOrHashWithNumber(bn)
	} else {
		bnOrHash = jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber)
	}

	ibs := s.api.State(tx, bnOrHash)
	if ibs == nil {
		return nil, fmt.Errorf("state not available")
	}

	results := make([]string, len(keys))
	for i, key := range keys {
		var va uint256.Int
		k := types.HexToHash(key)
		ibs.GetState(address, &k, &va)
		b32 := va.Bytes32()
		results[i] = hexutil.Encode(b32[:])
	}
	return results, nil
}

// GetUncleCountByBlockHash returns number of uncles in the block for the given block hash
func (s *BlockChainAPI) GetUncleCountByBlockHash(ctx context.Context, blockHash avmcommon.Hash) *hexutil.Uint {
	if block, _ := s.getBlockByHash(avmtypes.ToastHash(blockHash)); block != nil {
		// POA/POS consensus does not have uncles
		n := hexutil.Uint(0)
		return &n
	}
	return nil
}

// GetUncleByBlockHashAndIndex returns the uncle block for the given block hash and index.
// POA/POS consensus does not have uncle blocks, always returns nil.
func (s *BlockChainAPI) GetUncleByBlockHashAndIndex(ctx context.Context, blockHash avmcommon.Hash, index hexutil.Uint) (map[string]interface{}, error) {
	return nil, nil
}

// Result structs for GetProof
type AccountResult struct {
	Address      types.Address   `json:"address"`
	AccountProof []string        `json:"accountProof"`
	Balance      *hexutil.Big    `json:"balance"`
	CodeHash     types.Hash      `json:"codeHash"`
	Nonce        hexutil.Uint64  `json:"nonce"`
	StorageHash  types.Hash      `json:"storageHash"`
	StorageProof []StorageResult `json:"storageProof"`
}

type StorageResult struct {
	Key   string       `json:"key"`
	Value *hexutil.Big `json:"value"`
	Proof []string     `json:"proof"`
}

// OverrideAccount indicates the overriding fields of account during the execution
// of a message call.
// Note, state and stateDiff can't be specified at the same time. If state is
// set, message execution will only use the data in the given state. Otherwise
// if stateDiff is set, all diff will be applied first and then execute the call
// message.
type OverrideAccount struct {
	Nonce      *hexutil.Uint64                    `json:"nonce"`
	Code       *hexutil.Bytes                     `json:"code"`
	Balance    **hexutil.Big                      `json:"balance"`
	StatsPrint *map[avmcommon.Hash]avmcommon.Hash `json:"state"`
	StateDiff  *map[avmcommon.Hash]avmcommon.Hash `json:"stateDiff"`
}

// StateOverride is the collection of overridden accounts.
type StateOverride map[avmcommon.Address]OverrideAccount

// Apply overrides the fields of specified accounts into the given state.
func (diff *StateOverride) Apply(state *state.IntraBlockState) error {
	if diff == nil {
		return nil
	}
	for addr, account := range *diff {
		// Override account nonce.
		if account.Nonce != nil {
			state.SetNonce(*avmtypes.ToastAddress(&addr), uint64(*account.Nonce))
		}
		// Override account(contract) code.
		if account.Code != nil {
			state.SetCode(*avmtypes.ToastAddress(&addr), *account.Code)
		}
		// Override account balance.
		if account.Balance != nil {
			balance, _ := uint256.FromBig((*big.Int)(*account.Balance))
			state.SetBalance(*avmtypes.ToastAddress(&addr), balance)
		}
		if account.StatsPrint != nil && account.StateDiff != nil {
			return fmt.Errorf("account %s has both 'state' and 'stateDiff'", addr.String())
		}
		if account.StatsPrint != nil {
			statesPrint := make(map[types.Hash]uint256.Int)
			for k, v := range *account.StatsPrint {
				d, _ := uint256.FromBig(v.Big())
				statesPrint[avmtypes.ToastHash(k)] = *d
			}
			state.SetStorage(*avmtypes.ToastAddress(&addr), statesPrint)
		}
		// Apply state diff into specified accounts.
		if account.StateDiff != nil {
			for key, value := range *account.StateDiff {
				k := avmtypes.ToastHash(key)
				v, _ := uint256.FromBig(value.Big())
				state.SetState(*avmtypes.ToastAddress(&addr), &k, *v)
			}
		}
	}
	return nil
}

// BlockOverrides is a set of header fields to override.
type BlockOverrides struct {
	Number     *hexutil.Big
	Difficulty *hexutil.Big
	Time       *hexutil.Uint64
	GasLimit   *hexutil.Uint64
	Coinbase   *types.Address
	Random     *types.Hash
	BaseFee    *hexutil.Big
}

// Apply overrides the given header fields into the given block context.
func (diff *BlockOverrides) Apply(blockCtx *evmtypes.BlockContext) error {
	if diff == nil {
		return nil
	}
	if diff.Number != nil {
		number, err := uint256FromBig(diff.Number.ToInt())
		if err != nil {
			return fmt.Errorf("invalid block override number: %w", err)
		}
		if number != nil {
			blockCtx.BlockNumber = number.Uint64()
		}
	}
	if diff.Difficulty != nil {
		blockCtx.Difficulty = diff.Difficulty.ToInt()
	}
	if diff.Time != nil {
		blockCtx.Time = uint64(*diff.Time)
	}
	if diff.GasLimit != nil {
		blockCtx.GasLimit = uint64(*diff.GasLimit)
	}
	if diff.Coinbase != nil {
		blockCtx.Coinbase = *diff.Coinbase
	}
	if diff.Random != nil {
		blockCtx.PrevRanDao = diff.Random
	}
	if diff.BaseFee != nil {
		baseFee, err := uint256FromBig(diff.BaseFee.ToInt())
		if err != nil {
			return fmt.Errorf("invalid block override base fee: %w", err)
		}
		blockCtx.BaseFee = baseFee
	}
	return nil
}

func DoCall(ctx context.Context, api *API, args TransactionArgs, blockNrOrHash jsonrpc.BlockNumberOrHash, overrides *StateOverride, timeout time.Duration, globalGasCap uint64) (*internal.ExecutionResult, error) {
	defer func(start time.Time) { log.Debug("Executing EVM call finished", "runtime", time.Since(start)) }(time.Now())

	// Reject oversized calldata to prevent memory abuse
	if args.Data != nil && len(*args.Data) > maxCallDataSize {
		return nil, fmt.Errorf("calldata size %d exceeds maximum allowed %d bytes", len(*args.Data), maxCallDataSize)
	}

	var header block.IHeader
	var err error
	if blockNr, ok := blockNrOrHash.Number(); ok {
		resolvedBlockNr, resolveErr := normalizeBlockNumberForHistory(blockNr, api.BlockChain())
		if resolveErr != nil {
			return nil, resolveErr
		}
		if resolvedBlockNr < jsonrpc.EarliestBlockNumber {
			header = api.resolveForkchoiceTaggedHeader(resolvedBlockNr)
		} else {
			header = api.BlockChain().GetHeaderByNumber(uint256.NewInt(uint64(resolvedBlockNr.Int64())))
		}
	}
	if hash, ok := blockNrOrHash.Hash(); ok {
		header, err = api.BlockChain().GetHeaderByHash(hash)
	}
	if err != nil {
		return nil, err
	}
	if header == nil {
		return nil, errors.New("block not found")
	}
	tx, err := api.db.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	ibs := api.State(tx, blockNrOrHash)
	if ibs == nil {
		return nil, errors.New("cannot load state")
	}
	if concreteState, ok := ibs.(*state.IntraBlockState); ok {
		if err := overrides.Apply(concreteState); err != nil {
			return nil, err
		}
	} else if overrides != nil && len(*overrides) > 0 {
		return nil, errors.New("state overrides require *state.IntraBlockState")
	}
	// Setup context so it may be cancelled the call has completed
	// or, in case of unmetered gas, setup a context with a timeout.
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	// Make sure the context is cancelled when the call has completed
	// this makes sure resources are cleaned up.
	defer cancel()

	// Get a new instance of the EVM.
	msg, err := args.ToMessage(globalGasCap, headerBaseFeeBig(header))
	if err != nil {
		return nil, err
	}

	evm, vmError, err := api.GetEvm(ctx, msg, ibs, header, &vm2.Config{NoBaseFee: true})
	if err != nil {
		return nil, err
	}
	// Wait for the context to be done and cancel the evm. Even if the
	// EVM has finished, cancelling may be done (repeatedly)
	go func() {
		<-ctx.Done()
		evm.Cancel()
	}()

	// Execute the message.
	gasCap := globalGasCap
	if gasCap == 0 {
		gasCap = rpcGasCap
	}
	gp := new(common.GasPool).AddGas(gasCap)
	result, err := internal.ApplyMessage(evm, msg, gp, true, false)
	if err := vmError(); err != nil {
		return nil, err
	}

	if evm.Cancelled() {
		return nil, fmt.Errorf("execution aborted (timeout = %v)", timeout)
	}
	if err != nil {
		return result, fmt.Errorf("err: %w (supplied gas %d)", err, msg.Gas())
	}
	return result, nil
}

func newRevertError(result *internal.ExecutionResult) *revertError {
	reason, errUnpack := abi.UnpackRevert(result.Revert())
	err := errors.New("execution reverted")
	if errUnpack == nil {
		err = fmt.Errorf("execution reverted: %v", reason)
	}
	return &revertError{
		error:  err,
		reason: hexutil.Encode(result.Revert()),
	}
}

// revertError is an API error that encompasses an EVM revert with JSON error
// code and a binary data blob.
type revertError struct {
	error
	reason string // revert reason hex encoded
}

// ErrorCode returns the JSON error code for a revert.
// See: https://github.com/ethereum/wiki/wiki/JSON-RPC-Error-Codes-Improvement-Proposal
func (e *revertError) ErrorCode() int {
	return 3
}

// ErrorData returns the hex encoded revert reason.
func (e *revertError) ErrorData() interface{} {
	return e.reason
}

// Call executes the given transaction on the state for the given block number.
//
// Additionally, the caller can specify a batch of contract for fields overriding.
//
// Note, this function doesn't make any changes in the state/blockchain and is
// useful to execute and retrieve values.
func (s *BlockChainAPI) Call(ctx context.Context, args TransactionArgs, blockNrOrHash jsonrpc.BlockNumberOrHash, overrides *StateOverride) (hexutil.Bytes, error) {
	result, err := DoCall(ctx, s.api, args, blockNrOrHash, overrides, rpcEVMTimeout, rpcGasCap)
	if err != nil {
		return nil, err
	}
	// If the result contains a revert reason, try to unpack and return it.
	if len(result.Revert()) > 0 {
		return nil, newRevertError(result)
	}
	return result.Return(), result.Err
}

func BlockByNumber(ctx context.Context, number jsonrpc.BlockNumber, n *API) (block.IBlock, error) {
	// Pending and latest both resolve to the current block
	resolvedNumber, err := normalizeBlockNumberForHistory(number, n.BlockChain())
	if err != nil {
		return nil, err
	}
	if resolvedNumber < jsonrpc.EarliestBlockNumber {
		return n.resolveForkchoiceTaggedBlock(resolvedNumber), nil
	}
	return n.BlockChain().GetBlockByNumber(uint256.NewInt(uint64(resolvedNumber)))
}

func BlockByNumberOrHash(ctx context.Context, blockNrOrHash jsonrpc.BlockNumberOrHash, api *API) (block.IBlock, error) {
	if blockNr, ok := blockNrOrHash.Number(); ok {
		if blockNr == jsonrpc.PendingBlockNumber {
			return api.BlockChain().CurrentBlock(), nil
		}
		return BlockByNumber(ctx, blockNr, api)
	}
	if hash, ok := blockNrOrHash.Hash(); ok {
		iblock, err := api.BlockChain().GetBlockByHash(types.Hash(hash))
		if err != nil {
			return nil, err
		}
		if iblock == nil {
			return nil, errors.New("header found, but block body is missing")
		}
		return iblock, nil
	}
	return nil, errors.New("invalid arguments; neither block nor hash specified")
}
func DoEstimateGas(ctx context.Context, n *API, args TransactionArgs, blockNrOrHash jsonrpc.BlockNumberOrHash, gasCap uint64) (hexutil.Uint64, error) {
	// Binary search the gas requirement, as it may be higher than the amount used
	var (
		lo  = params.TxGas - 1
		hi  uint64
		cap uint64
	)
	// Use zero address if sender unspecified.
	if args.From == nil {
		args.From = new(avmcommon.Address)
	}
	// Determine the highest gas limit can be used during the estimation.
	if args.Gas != nil && uint64(*args.Gas) >= params.TxGas {
		hi = uint64(*args.Gas)
	} else {
		// Retrieve the block to act as the gas ceiling
		iblock, err := BlockByNumberOrHash(ctx, blockNrOrHash, n)
		if err != nil {
			return 0, err
		}
		if iblock == nil {
			return 0, errors.New("block not found")
		}
		hi = iblock.GasLimit()
	}

	var feeCap *big.Int
	if args.GasPrice != nil && (args.MaxFeePerGas != nil || args.MaxPriorityFeePerGas != nil) {
		return 0, errors.New("both gasPrice and (maxFeePerGas or maxPriorityFeePerGas) specified")
	} else if args.GasPrice != nil {
		feeCap = args.GasPrice.ToInt()
	} else if args.MaxFeePerGas != nil {
		feeCap = args.MaxFeePerGas.ToInt()
	} else {
		feeCap = avmcommon.Big0
	}
	// Recap the highest gas limit with account's available balance.
	if feeCap.BitLen() != 0 {
		tx, err := n.db.BeginRo(ctx)
		if err != nil {
			return 0, err
		}
		defer tx.Rollback()
		statedb := n.State(tx, blockNrOrHash)
		if statedb == nil {
			return 0, errors.New("cannot load stateDB")
		}
		balance := statedb.GetBalance(*avmtypes.ToastAddress(args.From)) // from

		// can't be nil
		available := new(big.Int).Set(balance.ToBig())
		if args.Value != nil {
			if args.Value.ToInt().Cmp(available) >= 0 {
				return 0, errors.New("insufficient funds for transfer")
			}
			available.Sub(available, args.Value.ToInt())
		}
		allowance := new(big.Int).Div(available, feeCap)

		// If the allowance is larger than maximum uint64, skip checking
		if allowance.IsUint64() && hi > allowance.Uint64() {
			transfer := args.Value
			if transfer == nil {
				transfer = new(hexutil.Big)
			}
			log.Warn("Gas estimation capped by limited funds", "original", hi, "balance", balance,
				"sent", transfer.ToInt(), "maxFeePerGas", feeCap, "fundable", allowance)
			hi = allowance.Uint64()
		}
	}
	// Recap the highest gas allowance with specified gascap.
	if gasCap != 0 && hi > gasCap {
		log.Warn("Caller gas above allowance, capping", "requested", hi, "cap", gasCap)
		hi = gasCap
	}
	cap = hi

	// Create a helper to check if a gas allowance results in an executable transaction
	executable := func(gas uint64) (bool, *internal.ExecutionResult, error) {
		args.Gas = (*hexutil.Uint64)(&gas)
		result, err := DoCall(ctx, n, args, blockNrOrHash, nil, 0, gasCap)
		if err != nil {
			if errors.Is(err, internal.ErrIntrinsicGas) {
				return true, nil, nil // Special case, raise gas limit
			}
			return true, nil, err // Bail out
		}
		return result.Failed(), result, nil
	}
	// Execute the binary search and hone in on an executable gas limit
	for lo+1 < hi {
		mid := (hi + lo) / 2
		failed, _, err := executable(mid)

		// If the error is not nil(consensus error), it means the provided message
		// call or transaction will never be accepted no matter how much gas it is
		// assigened. Return the error directly, don't struggle any more.
		if err != nil {
			return 0, err
		}
		if failed {
			lo = mid
		} else {
			hi = mid
		}
	}
	// Reject the transaction as invalid if it still fails at the highest allowance
	if hi == cap {
		failed, result, err := executable(hi)
		if err != nil {
			return 0, err
		}
		if failed {
			if result != nil && !errors.Is(result.Err, vm2.ErrOutOfGas) {
				if len(result.Revert()) > 0 {
					return 0, newRevertError(result)
				}
				return 0, result.Err
			}
			// Otherwise, the specified gas cap is too low
			return 0, fmt.Errorf("gas required exceeds allowance (%d)", cap)
		}
	}
	return hexutil.Uint64(hi), nil
}

// EstimateGas returns an estimate of the amount of gas needed to execute the
// given transaction against the current pending block.
func (s *BlockChainAPI) EstimateGas(ctx context.Context, args TransactionArgs, blockNrOrHash *jsonrpc.BlockNumberOrHash) (hexutil.Uint64, error) {
	bNrOrHash := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.PendingBlockNumber)
	if blockNrOrHash != nil {
		bNrOrHash = *blockNrOrHash
	}
	return DoEstimateGas(ctx, s.api, args, bNrOrHash, rpcGasCap)
}

// GetBlockByNumber returns the requested canonical block.
//   - When blockNr is -1 the chain head is returned.
//   - When blockNr is -2 the pending chain head is returned.
//   - When fullTx is true all transactions in the block are returned, otherwise
//     only the transaction hash is returned.
func (s *BlockChainAPI) GetBlockByNumber(ctx context.Context, number jsonrpc.BlockNumber, fullTx bool) (map[string]interface{}, error) {
	block, err := s.getBlockByNumber(number)
	if block != nil && err == nil {
		response, err := RPCMarshalBlock(block, s.api.BlockChain(), s.api.GetChainConfig(), true, fullTx, s.overlayBlockHash(block))
		if err == nil && number == jsonrpc.PendingBlockNumber {
			// Pending blocks need to nil out a few fields
			for _, field := range []string{"hash", "nonce", "miner"} {
				response[field] = nil
			}
		}
		return response, err
	}

	return nil, err
}

// GetBlockByHash returns the requested block by hash, with full or hash-only transactions.
func (s *BlockChainAPI) GetBlockByHash(ctx context.Context, hash avmcommon.Hash, fullTx bool) (map[string]interface{}, error) {
	block, err := s.getBlockByHash(avmtypes.ToastHash(hash))

	if block != nil {
		return RPCMarshalBlock(block, s.api.BlockChain(), s.api.GetChainConfig(), true, fullTx, s.overlayBlockHash(block))
	}
	return nil, err
}

func (s *BlockChainAPI) MinedBlock(ctx context.Context, address types.Address) (*jsonrpc.Subscription, error) {
	notifier, supported := jsonrpc.NotifierFromContext(ctx)
	if !supported {
		return &jsonrpc.Subscription{}, jsonrpc.ErrNotificationsUnsupported
	}

	if is, err := IsDeposit(s.api.db, address); err != nil || !is {
		if err != nil {
			log.Errorf("IsDeposit(%s) failed, err= %v", address, err)
		}
		return &jsonrpc.Subscription{}, fmt.Errorf("unauthed address: %s", address)
	}

	rpcSub, err := notifier.CreateSubscription()
	if err != nil {
		return nil, fmt.Errorf("failed to create subscription: %w", err)
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Error("panic in MinedEntire subscription, recovered", "panic", r)
			}
		}()
		entire := make(chan common.MinedEntireEvent, 20)
		blocksSub, err := event.GlobalEvent.Subscribe(entire)
		if err != nil {
			log.Error("failed to subscribe to MinedEntireEvent", "err", err)
			return
		}
		for {
			select {
			case b := <-entire:
				// Type assert Entire from interface{} to *state.EntireCode
				entireCode, ok := b.Entire.(state.EntireCode)
				if !ok {
					log.Warn("MinedEntireEvent.Entire is not state.EntireCode")
					continue
				}
				var pushData state.EntireCode
				pushData.Entire = entireCode.Entire.Clone()
				pushData.Entire.Header.Root = types.Hash{}
				pushData.Headers = entireCode.Headers
				pushData.Codes = entireCode.Codes
				pushData.Rewards = entireCode.Rewards
				pushData.CoinBase = entireCode.CoinBase
				log.Trace("send mining block", "addr", address, "blockNr", entireCode.Entire.Header.Number.Hex(), "blockTime", time.Unix(int64(entireCode.Entire.Header.Time), 0).Format(time.RFC3339))
				notifier.Notify(rpcSub.ID, pushData)
			case <-rpcSub.Err():
				blocksSub.Unsubscribe()
				return
			case <-notifier.Closed():
				blocksSub.Unsubscribe()
				return
			}
		}
	}()
	return rpcSub, nil
}

func (s *BlockChainAPI) SubmitSign(sign AggSign) error {
	info := DepositInfo(s.api.db, sign.Address)
	if nil == info {
		return fmt.Errorf("unauthed address: %s", sign.Address)
	}
	sign.PublicKey.SetBytes(info.PublicKey.Bytes())
	select {
	case sigChannel <- sign:
	default:
		return errors.New("sign channel is full, please retry later")
	}
	return nil
}
