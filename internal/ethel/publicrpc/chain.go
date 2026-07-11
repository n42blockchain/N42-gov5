// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// ethelChain is a READ-ONLY common.IBlockChain over an eth-el datadir. eth-el
// writes blocks/headers/bodies/receipts through the standard rawdb schema (see
// engine_state_adapter.go), so the public Blockscout-facing RPC can reuse every
// internal/api block/tx/receipt/log handler by giving it this adapter — the
// handlers read chain data through IBlockChain, and state through API.State()
// (wired separately to the eth-el hashed/plain reader). Write and lifecycle
// methods are inert: this backend only serves queries.

package publicrpc

import (
	"context"
	"errors"

	"github.com/holiman/uint256"
	libp2ppeer "github.com/libp2p/go-libp2p/core/peer"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

var errReadOnly = errors.New("ethel public rpc: read-only chain backend")

// ethelChain adapts an eth-el DB to common.IBlockChain for read serving.
type ethelChain struct {
	db     kv.RwDB
	engine interface{} // consensus.Engine, kept opaque per the IBlockChain contract
	cfg    *params.ChainConfig
	quit   chan struct{}

	// stateReader builds the post-state reader for a block number (the eth-el
	// hashed/plain reader), shared with API.State() so debug_/trace_ (which reach
	// state via BlockChain().StateAt) and eth_call/getBalance read the same state.
	stateReader func(tx kv.Tx, blockNum uint64) (state.StateReader, error)
}

func newEthelChain(db kv.RwDB, engine interface{}, cfg *params.ChainConfig, stateReader func(tx kv.Tx, blockNum uint64) (state.StateReader, error)) *ethelChain {
	return &ethelChain{db: db, engine: engine, cfg: cfg, quit: make(chan struct{}), stateReader: stateReader}
}

// withTx runs fn under a fresh read-only tx (read serving opens short-lived
// snapshots; nil tx / error → zero value via fn's own nil handling).
func (c *ethelChain) withTx(fn func(tx kv.Tx)) {
	tx, err := c.db.BeginRo(context.Background())
	if err != nil {
		return
	}
	defer tx.Rollback()
	fn(tx)
}

// --- ChainHeaderReader -----------------------------------------------------

func (c *ethelChain) Config() *params.ChainConfig { return c.cfg }

func (c *ethelChain) CurrentBlock() block.IBlock {
	var out block.IBlock
	c.withTx(func(tx kv.Tx) {
		hash := rawdb.ReadHeadBlockHash(tx)
		if hash == (types.Hash{}) {
			if n := rawdb.ReadCurrentBlockNumber(tx); n != nil {
				if h, err := rawdb.ReadCanonicalHash(tx, *n); err == nil {
					hash = h
				}
			}
		}
		if hash == (types.Hash{}) {
			return
		}
		num := rawdb.ReadHeaderNumber(tx, hash)
		if num == nil {
			return
		}
		if b := rawdb.ReadBlock(tx, hash, *num); b != nil {
			out = b
		}
	})
	return out
}

func (c *ethelChain) GetHeader(hash types.Hash, number *uint256.Int) block.IHeader {
	if number == nil {
		return nil
	}
	var out block.IHeader
	c.withTx(func(tx kv.Tx) {
		if h := rawdb.ReadHeader(tx, hash, number.Uint64()); h != nil {
			out = h
		}
	})
	return out
}

func (c *ethelChain) GetHeaderByNumber(number *uint256.Int) block.IHeader {
	if number == nil {
		return nil
	}
	var out block.IHeader
	c.withTx(func(tx kv.Tx) {
		hash, err := rawdb.ReadCanonicalHash(tx, number.Uint64())
		if err != nil || hash == (types.Hash{}) {
			return
		}
		if h := rawdb.ReadHeader(tx, hash, number.Uint64()); h != nil {
			out = h
		}
	})
	return out
}

func (c *ethelChain) GetHeaderByHash(hash types.Hash) (block.IHeader, error) {
	var out block.IHeader
	c.withTx(func(tx kv.Tx) {
		num := rawdb.ReadHeaderNumber(tx, hash)
		if num == nil {
			return
		}
		if h := rawdb.ReadHeader(tx, hash, *num); h != nil {
			out = h
		}
	})
	return out, nil
}

func (c *ethelChain) GetTd(hash types.Hash, number *uint256.Int) *uint256.Int {
	if number == nil {
		return nil
	}
	var out *uint256.Int
	c.withTx(func(tx kv.Tx) {
		if td, err := rawdb.ReadTd(tx, hash, number.Uint64()); err == nil {
			out = td
		}
	})
	return out
}

func (c *ethelChain) GetBlockByNumber(number *uint256.Int) (block.IBlock, error) {
	if number == nil {
		return nil, nil
	}
	var out block.IBlock
	c.withTx(func(tx kv.Tx) {
		hash, err := rawdb.ReadCanonicalHash(tx, number.Uint64())
		if err != nil || hash == (types.Hash{}) {
			return
		}
		if b := rawdb.ReadBlock(tx, hash, number.Uint64()); b != nil {
			out = b
		}
	})
	return out, nil
}

// --- IHeaderChain ----------------------------------------------------------

func (c *ethelChain) InsertHeader(headers []block.IHeader) (int, error) { return 0, errReadOnly }

func (c *ethelChain) GetBlockByHash(h types.Hash) (block.IBlock, error) {
	var out block.IBlock
	c.withTx(func(tx kv.Tx) {
		num := rawdb.ReadHeaderNumber(tx, h)
		if num == nil {
			return
		}
		if b := rawdb.ReadBlock(tx, h, *num); b != nil {
			out = b
		}
	})
	return out, nil
}

// --- IBlockChain -----------------------------------------------------------

func (c *ethelChain) Blocks() []block.IBlock { return nil }
func (c *ethelChain) Start() error           { return nil }

func (c *ethelChain) GenesisBlock() block.IBlock {
	b, _ := c.GetBlockByNumber(uint256.NewInt(0))
	return b
}

func (c *ethelChain) NewBlockHandler(payload []byte, peer libp2ppeer.ID) error { return errReadOnly }
func (c *ethelChain) InsertChain(blocks []block.IBlock) (int, error)           { return 0, errReadOnly }
func (c *ethelChain) InsertBlock(blocks []block.IBlock, isSync bool) (int, error) {
	return 0, errReadOnly
}
func (c *ethelChain) SetEngine(engine interface{}) { c.engine = engine }

func (c *ethelChain) GetBlocksFromHash(hash types.Hash, n int) []block.IBlock {
	out := make([]block.IBlock, 0, n)
	cur := hash
	for i := 0; i < n; i++ {
		b, _ := c.GetBlockByHash(cur)
		if b == nil {
			break
		}
		out = append(out, b)
		cur = b.ParentHash()
	}
	return out
}

func (c *ethelChain) SealedBlock(b block.IBlock) error { return errReadOnly }
func (c *ethelChain) Engine() interface{}              { return c.engine }

func (c *ethelChain) GetReceipts(blockHash types.Hash) (block.Receipts, error) {
	var out block.Receipts
	c.withTx(func(tx kv.Tx) {
		num := rawdb.ReadHeaderNumber(tx, blockHash)
		if num == nil {
			return
		}
		b := rawdb.ReadBlock(tx, blockHash, *num)
		if b == nil {
			return
		}
		out = rawdb.ReadReceipts(tx, b, nil)
	})
	return out, nil
}

func (c *ethelChain) GetLogs(blockHash types.Hash) ([][]*block.Log, error) {
	receipts, err := c.GetReceipts(blockHash)
	if err != nil {
		return nil, err
	}
	logs := make([][]*block.Log, len(receipts))
	for i, r := range receipts {
		logs[i] = r.Logs
	}
	return logs, nil
}

func (c *ethelChain) SetHead(head uint64) error            { return errReadOnly }
func (c *ethelChain) AddFutureBlock(b block.IBlock) error  { return errReadOnly }

func (c *ethelChain) GetBlock(hash types.Hash, number uint64) block.IBlock {
	var out block.IBlock
	c.withTx(func(tx kv.Tx) {
		if b := rawdb.ReadBlock(tx, hash, number); b != nil {
			out = b
		}
	})
	return out
}

// StateAt returns the post-state of blockNr as a *state.IntraBlockState, built
// from the eth-el reader. Used by StateAtBlock/StateAtTransaction (the trace
// backend). Returns nil when no reader is wired or it errors.
func (c *ethelChain) StateAt(tx kv.Tx, blockNr uint64) interface{} {
	if c.stateReader == nil || tx == nil {
		return nil
	}
	reader, err := c.stateReader(tx, blockNr)
	if err != nil || reader == nil {
		return nil
	}
	return state.New(reader)
}

func (c *ethelChain) HasBlock(hash types.Hash, number uint64) bool {
	has := false
	c.withTx(func(tx kv.Tx) { has = rawdb.ReadHeader(tx, hash, number) != nil })
	return has
}

func (c *ethelChain) DB() kv.RwDB              { return c.db }
func (c *ethelChain) Quit() <-chan struct{}    { return c.quit }
func (c *ethelChain) EarliestBlock() uint64    { return 0 }

func (c *ethelChain) Close() error {
	select {
	case <-c.quit:
	default:
		close(c.quit)
	}
	return nil
}

func (c *ethelChain) WriteBlockWithState(b block.IBlock, receipts []*block.Receipt, ibs interface{}, nopay map[types.Address]*uint256.Int) error {
	return errReadOnly
}

// PeelDanglingQMDBAppends is a no-op: the ethel public-RPC chain is read-only
// and runs no QMDB root engine.
func (c *ethelChain) PeelDanglingQMDBAppends() {}

var _ common.IBlockChain = (*ethelChain)(nil)
