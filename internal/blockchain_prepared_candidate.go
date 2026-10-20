package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"unsafe"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
)

const MaxPreparedCandidateBytes = 128 << 20

type preparedReward struct {
	address types.Address
	value   uint256.Int
}

// PreparedUnsignedCandidate is an owner-local, single-use execution write. It
// owns no read transaction or full IBS and is never serialized to a validator.
type PreparedUnsignedCandidate struct {
	chain      *BlockChain
	config     types.Hash
	unsigned   []byte
	sealHash   types.Hash
	parentRoot types.Hash
	result     rawdb.ExecutedResult
	write      *state.PreparedStateWrite
	root       *state.PreparedRootReplay
	computer   state.RootComputer
	receipts   []*block.Receipt
	rewards    []preparedReward
	bytes      int
	used       bool // guarded by its chain's mutation lock
}

// PerformanceExecutionAttempts counts actual builder/import processor entries,
// including failed attempts. It is diagnostic, never a committed TPS counter.
func (bc *BlockChain) PerformanceExecutionAttempts() uint64 {
	return bc.performanceExecutionAttempts.Load()
}

func (p *PreparedUnsignedCandidate) RetainedBytes() int {
	if p == nil {
		return 0
	}
	return p.bytes
}

// CopyUnsignedRLP returns an owned copy of the already encoded candidate.
// The private bytes remain immutable for ImportPreparedCandidate authentication.
func (p *PreparedUnsignedCandidate) CopyUnsignedRLP() []byte {
	if p == nil {
		return nil
	}
	return append([]byte(nil), p.unsigned...)
}

func (bc *BlockChain) prepareUnsignedCandidate(b *block.Block, result rawdb.ExecutedResult, ibs *state.IntraBlockState, receipts []*block.Receipt, nopay map[types.Address]*uint256.Int, parentRoot types.Hash) (*PreparedUnsignedCandidate, error) {
	root, err := state.PrepareRootReplay(ibs)
	if err != nil {
		return nil, err
	}
	// CommitBlock may fault pending credits and storage. Finish it before the
	// build's RO transaction closes, then drop the full IBS/account prefetcher.
	write, err := state.PrepareStateWrite(ibs, bc.chainConfig.Rules(b.Number64().Uint64()), b.Number64().Uint64())
	if err != nil {
		return nil, err
	}
	raw, err := b.Marshal()
	if err != nil {
		return nil, err
	}
	cfg, err := json.Marshal(bc.chainConfig)
	if err != nil {
		return nil, err
	}
	engine := bc.engine.(*hotstuff.HotStuff)
	p := &PreparedUnsignedCandidate{chain: bc, config: crypto.Keccak256Hash(cfg), unsigned: raw, sealHash: engine.SealHash(b.Header()), parentRoot: parentRoot, result: result, write: write, root: root, computer: ibs.GetRootComputer(), receipts: receipts}
	for address, value := range nopay {
		if value != nil {
			p.rewards = append(p.rewards, preparedReward{address: address, value: *value})
		}
	}
	p.bytes = int(unsafe.Sizeof(*p)) + cap(raw) + write.RetainedBytes() + root.RetainedBytes() + cap(receipts)*int(unsafe.Sizeof((*block.Receipt)(nil))) + cap(p.rewards)*int(unsafe.Sizeof(preparedReward{}))
	for _, r := range receipts {
		p.bytes += int(unsafe.Sizeof(*r)) + cap(r.PostState) + cap(r.Logs)*int(unsafe.Sizeof((*block.Log)(nil))) + 32
		for _, l := range r.Logs {
			p.bytes += int(unsafe.Sizeof(*l)) + cap(l.Topics)*int(unsafe.Sizeof(types.Hash{})) + cap(l.Data) + 32
		}
	}
	if p.bytes > MaxPreparedCandidateBytes {
		return nil, nil
	} // optimization miss; normal import remains available
	return p, nil
}

// ImportPreparedCandidate reuses EVM output only after public seal/body checks
// and an exact unsigned-candidate comparison. The existing live dirty replay
// still recomputes/checks the root, and finality remains CommitQC-only.
func (bc *BlockChain) ImportPreparedCandidate(ctx context.Context, b *block.Block, p *PreparedUnsignedCandidate) (bool, error) {
	bc.lock.Lock()
	defer bc.lock.Unlock()
	if p == nil || p.chain != bc || p.used {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if bc.chainConfig == nil || bc.chainConfig.Performance == nil || bc.chainConfig.DeferredExecutionTime != nil || !bc.qmdbEnabled || b == nil {
		return false, errors.New("prepared performance import unavailable")
	}
	engine, ok := bc.engine.(*hotstuff.HotStuff)
	if !ok {
		return false, errors.New("prepared import requires public HotStuff verifier")
	}
	h, ok := b.Header().(*block.Header)
	if !ok || h == nil || h.Number == nil || !h.Number.IsUint64() || h.Number.IsZero() {
		return false, errors.New("prepared import header invalid")
	}
	if engine.SealHash(h) != p.sealHash {
		return false, nil
	}
	if err := engine.VerifyPerformanceSealedHeader(bc, h); err != nil {
		return false, err
	}
	if err := bc.validator.ValidateBody(b); err != nil {
		return false, err
	}
	if len(h.Extra) < 96 {
		return false, errors.New("prepared import seal missing")
	}
	unsignedHeader := block.CopyHeader(h)
	clear(unsignedHeader.Extra[len(unsignedHeader.Extra)-96:])
	unsignedHeader.ResetHashCache()
	body, ok := b.Body().(*block.Body)
	if !ok || body == nil {
		return false, errors.New("prepared import concrete body missing")
	}
	unsigned := block.NewBlockFromStorage(unsignedHeader.Hash(), unsignedHeader, body)
	raw, err := unsigned.Marshal()
	if err != nil {
		return false, err
	}
	if !bytes.Equal(raw, p.unsigned) {
		return false, errors.New("prepared import changed unsigned candidate")
	}
	cfg, err := json.Marshal(bc.chainConfig)
	if err != nil {
		return false, err
	}
	if crypto.Keccak256Hash(cfg) != p.config || h.Root != p.result.Root || h.ReceiptHash != p.result.ReceiptHash || h.GasUsed != p.result.GasUsed || h.Bloom != p.result.Bloom {
		return false, errors.New("prepared import config/result mismatch")
	}
	if err := bc.joinLagWrite(); err != nil {
		return false, err
	}
	if err := bc.ChainDB.View(ctx, func(tx kv.Tx) error {
		number, hash, err := bc.performanceAppliedHead(tx)
		if err != nil {
			return err
		}
		if number == ^uint64(0) || number+1 != h.Number.Uint64() || hash != h.ParentHash || bc.qmdbRootComputer.Root() != p.parentRoot {
			return ErrPerformanceParentNotHead
		}
		return nil
	}); err != nil {
		return false, err
	}
	p.used = true // every write attempt consumes ownership, including rollback
	receipts := make([]*block.Receipt, len(p.receipts))
	for i, original := range p.receipts {
		r := new(block.Receipt)
		*r = *original
		r.BlockHash = b.Hash()
		r.BlockNumber = new(uint256.Int).Set(h.Number)
		r.TransactionIndex = uint(i)
		r.Logs = make([]*block.Log, len(original.Logs))
		for j, log := range original.Logs {
			l := new(block.Log)
			*l = *log
			l.BlockHash = b.Hash()
			r.Logs[j] = l
		}
		receipts[i] = r
	}
	rewards := make(map[types.Address]*uint256.Int, len(p.rewards))
	for _, r := range p.rewards {
		v := r.value
		rewards[r.address] = &v
	}
	ibs := p.root.State(p.computer)
	written := false
	defer func() {
		if !written {
			bc.revertUncommittedQMDBAppends(h.Number.Uint64())
		}
	}()
	_, err = bc.writeBlockWithStateOpt(b, receipts, ibs, rewards, nil, p.write)
	if err != nil {
		return false, err
	}
	written = true
	bc.futureBlocks.Remove(b.Hash())
	return true, nil
}
