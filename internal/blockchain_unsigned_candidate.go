package internal

import (
	"context"
	"errors"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// BuildUnsignedCandidate executes a prepared leader template on an isolated
// tree and returns an unsigned candidate. Nothing is inserted or canonicalized.
// The chain mutation lock pins PlainState and the parent snapshot through the
// entire build, including BlockChain's background importer and ordered writer.
func (bc *BlockChain) BuildUnsignedCandidate(ctx context.Context, template *block.Block) (*block.Block, rawdb.ExecutedResult, error) {
	bc.lock.Lock()
	defer bc.lock.Unlock()
	var zero rawdb.ExecutedResult
	if err := ctx.Err(); err != nil {
		return nil, zero, err
	}
	if bc.chainConfig == nil || bc.chainConfig.Performance == nil || !bc.qmdbEnabled || bc.chainConfig.DeferredExecutionTime != nil || template == nil {
		return nil, zero, errors.New("unsigned build requires independent performance QMDB executor")
	}
	engine, ok := bc.engine.(*hotstuff.HotStuff)
	if !ok {
		return nil, zero, errors.New("unsigned build requires actual public HotStuff engine")
	}
	body, bodyOK := template.Body().(*block.Body)
	if !bodyOK || body == nil {
		return nil, zero, errors.New("unsigned build concrete header/body required")
	}
	header, headerOK := template.Header().(*block.Header)
	if !headerOK || header == nil {
		return nil, zero, errors.New("unsigned build concrete header required")
	}
	if err := engine.ValidateUnsignedCandidateHeader(bc, header); err != nil {
		return nil, zero, err
	}
	if bc.chainConfig.IsBAL(header.Time) || bc.chainConfig.IsMobileAnchor(header.Time) || bc.chainConfig.IsDAOFork(header.Number.Uint64()) {
		return nil, zero, errors.New("unsigned build scheduled fork adapter is not wired")
	}
	if body.IsFrameBody() || len(body.Verifiers) > 0 || len(body.Rewards) > 0 || len(body.ZkProof) > 0 || template.TxHash() != block.BodyTxRoot(body, header.Time) {
		return nil, zero, errors.New("unsigned build body root/frame mismatch")
	}
	if err := bc.joinLagWrite(); err != nil {
		return nil, zero, err
	}
	tx, err := bc.ChainDB.BeginRo(ctx)
	if err != nil {
		return nil, zero, err
	}
	defer tx.Rollback()
	an, ah, applied, err := rawdb.ReadQMDBApplied(tx)
	if err != nil {
		return nil, zero, err
	}
	// Fresh n42 init predates the applied marker: genesis is represented by
	// its canonical/head rows and actual loaded QMDB root. Accept only that
	// exact initial state; a missing marker above genesis remains an error.
	if !applied && header.Number.Uint64() == 1 && header.ParentHash == bc.GenesisBlock().Hash() && rawdb.ReadHeadBlockHash(tx) == bc.GenesisBlock().Hash() && bc.qmdbRootComputer != nil && bc.qmdbRootComputer.Root() == bc.GenesisBlock().StateRoot() {
		an, ah, applied = 0, bc.GenesisBlock().Hash(), true
	}
	if !applied || header.Number.Uint64() != an+1 || header.ParentHash != ah {
		return nil, zero, errors.New("unsigned build parent is not actual applied head; retry")
	}
	parent := rawdb.ReadHeader(tx, ah, an)
	if parent == nil {
		return nil, zero, errors.New("unsigned build parent header missing")
	}
	rc, err := bc.NewMinerRootComputer(tx, parent.Root)
	if err != nil {
		return nil, zero, err
	}
	qmdb, ok := rc.(*commitment.QMDBRootComputer)
	if !ok {
		return nil, zero, errors.New("unsigned build isolated QMDB computer unavailable")
	}
	defer qmdb.SetCold(nil)
	prefetch := state.NewAccountPrefetch(state.NewPlainStateReader(tx))
	ibs := state.New(prefetch)
	ibs.SetAccountPrefetch(prefetch)
	ibs.SetDetachedPendingCredits(true)
	ibs.SetRootComputer(rc)
	getHash := GetHashFn(header, func(hash types.Hash, number uint64) *block.Header { return rawdb.ReadHeader(tx, hash, number) })
	included, receipts, gas, failed, err := bc.BuildParallel(header, template.Transactions(), ibs, getHash)
	if err != nil {
		return nil, zero, err
	}
	if failed != 0 || len(included) != len(template.Transactions()) {
		return nil, zero, errors.New("unsigned build has rejected candidates (including nonce gaps); template must be repacked")
	}
	if err := ctx.Err(); err != nil {
		return nil, zero, err
	}
	header.GasUsed = gas
	// BuildParallel already performs block-start operations. As in the miner,
	// block end precedes FinalizeAndAssemble (root and rewards), exactly once.
	if _, err := ProcessExecutionBlockEnd(nil, bc.chainConfig, ibs, header, engine); err != nil {
		return nil, zero, err
	}
	built, _, _, err := engine.FinalizeAndAssemble(bc, header, ibs, included, nil, receipts)
	if err != nil {
		return nil, zero, err
	}
	root, computed := ibs.LastIntermediateRoot()
	if !computed {
		return nil, zero, errors.New("unsigned build produced no execution root")
	}
	result := ExecutedResultFor(bc.chainConfig, header.Number.Uint64(), root, receipts)
	b := built.(*block.Block)
	h := b.Header().(*block.Header)
	if h.Root != result.Root || h.ReceiptHash != result.ReceiptHash || h.GasUsed != result.GasUsed || h.Bloom != result.Bloom {
		return nil, zero, errors.New("unsigned build assembled inconsistent result")
	}
	return b, result, nil
}
