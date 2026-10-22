package sharedvalidator

import (
	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

var _ consensus.ChainHeaderReader = (*Replica)(nil)

func (r *Replica) Config() *params.ChainConfig { return r.cfg }
func (r *Replica) CurrentBlock() block.IBlock {
	var b *block.Block
	_ = r.db.View(r.ctx, func(tx kv.Tx) error {
		var err error
		b, err = rawdb.ReadBlockByHash(tx, rawdb.ReadHeadBlockHash(tx))
		return err
	})
	if b == nil {
		return r.genesis
	}
	return b
}

// currentHeader reads canonical head metadata in one snapshot, without decoding
// the availability body. Callers that serve a block still use the full readers.
func (r *Replica) currentHeader() *block.Header {
	var header *block.Header
	_ = r.db.View(r.ctx, func(tx kv.Tx) error {
		var err error
		header, err = rawdb.ReadHeaderByHash(tx, rawdb.ReadHeadBlockHash(tx))
		return err
	})
	if header == nil {
		return r.genesis.Header().(*block.Header)
	}
	return header
}

func (r *Replica) GetHeader(hash types.Hash, number *uint256.Int) block.IHeader {
	if number == nil || !number.IsUint64() {
		return nil
	}
	var header *block.Header
	_ = r.db.View(r.ctx, func(tx kv.Tx) error { header = rawdb.ReadHeader(tx, hash, number.Uint64()); return nil })
	if header == nil {
		return nil
	}
	return header
}
func (r *Replica) GetHeaderByNumber(number *uint256.Int) block.IHeader {
	b, err := r.GetBlockByNumber(number)
	if err != nil || b == nil {
		return nil
	}
	return b.Header()
}
func (r *Replica) GetHeaderByHash(hash types.Hash) (block.IHeader, error) {
	var header *block.Header
	err := r.db.View(r.ctx, func(tx kv.Tx) error { var err error; header, err = rawdb.ReadHeaderByHash(tx, hash); return err })
	if header == nil {
		return nil, err
	}
	return header, err
}
func (r *Replica) GetTd(hash types.Hash, number *uint256.Int) *uint256.Int {
	if r.GetHeader(hash, number) == nil {
		return nil
	}
	return uint256.NewInt(0)
}
func (r *Replica) GetBlockByNumber(number *uint256.Int) (block.IBlock, error) {
	if number == nil || !number.IsUint64() {
		return nil, nil
	}
	var b *block.Block
	err := r.db.View(r.ctx, func(tx kv.Tx) error {
		hash, err := rawdb.ReadCanonicalHash(tx, number.Uint64())
		if err != nil {
			return err
		}
		b, err = rawdb.ReadBlockByHash(tx, hash)
		return err
	})
	if b == nil {
		return nil, err
	}
	return b, err
}
