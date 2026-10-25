package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/params"
)

// ErrSpeculativeChildMismatch permits the owner to discard an advisory EVM
// execution and build the actual request normally. It does not authenticate
// or authorize that actual request.
var ErrSpeculativeChildMismatch = errors.New("speculative child does not match actual candidate")

const maxSpeculativeChildTransactions = 200_000
const maxSpeculativeChildBytes = 64 << 20

// SpeculativeAppliedChild owns a completed EVM execution against one durable
// applied parent, before the child's commitment root has been computed. Its
// read transaction stays open until CompleteSpeculativeAppliedChild or Close.
// It is single-use and cannot grant an Import ACK or a canonical credit.
type SpeculativeAppliedChild struct {
	mu         sync.Mutex
	chain      *BlockChain
	tx         kv.Tx
	state      *state.IntraBlockState
	header     *block.Header
	txs        []*transaction.Transaction
	receipts   block.Receipts
	parentHash types.Hash
	parentNum  uint64
	parentRoot types.Hash
	binding    types.Hash
	view       hotstuff.ViewNumber
	config     types.Hash
	key        []byte
	closed     bool
}

func (child *SpeculativeAppliedChild) closeLocked() {
	if child.closed {
		return
	}
	child.closed = true
	if child.tx != nil {
		child.tx.Rollback()
		child.tx = nil
	}
	child.state = nil
	child.header = nil
	child.txs = nil
	child.receipts = nil
	child.key = nil
}

// Close discards the advisory execution. It may be called after a failed or
// successful completion and is safe to call more than once.
func (child *SpeculativeAppliedChild) Close() {
	if child == nil {
		return
	}
	child.mu.Lock()
	defer child.mu.Unlock()
	child.closeLocked()
}

// speculativeExecutionKey includes every body and header field except
// HotStuff Extra. Extra carries the view/QC/seal, which may change between
// the advisory hint and the actual native request but is not read by EVM or
// block-start/end execution. The actual header is publicly validated before
// this key authorizes reuse; any other field or transaction change misses.
func speculativeExecutionKey(template *block.Block) ([]byte, error) {
	if template == nil {
		return nil, errors.New("speculative template missing")
	}
	raw, err := template.Marshal()
	if err != nil {
		return nil, err
	}
	var copy block.Block
	if err := copy.Unmarshal(raw); err != nil {
		return nil, err
	}
	header, ok := copy.Header().(*block.Header)
	if !ok || header == nil {
		return nil, errors.New("speculative header missing")
	}
	header.Extra = nil
	header.ResetHashCache()
	copy.WithSeal(header)
	return copy.Marshal()
}

// copySpeculativeSenderMemos preserves only same-signer memos on an owned
// transaction copy whose signed fields re-encode to the exact same bytes.
// The source must be private owner-selected data: the strict-ingress worker
// derives its memo solely from signature-verified queue evidence.
func copySpeculativeSenderMemos(config *params.ChainConfig, header *block.Header, originalTxs, copiedTxs []*transaction.Transaction) {
	if config == nil || header == nil || header.Number == nil || len(originalTxs) != len(copiedTxs) {
		return
	}
	signer := transaction.MakeSignerWithTimestamp(config, header.Number.ToBig(), header.Time)
	for i, original := range originalTxs {
		copied := copiedTxs[i]
		if original == nil || copied == nil || !original.HasSenderMemo(signer) {
			continue
		}
		originalWire, originalErr := transaction.EncodeEthereumTransaction(original)
		copiedWire, copiedErr := transaction.EncodeEthereumTransaction(copied)
		if originalErr != nil || copiedErr != nil || !bytes.Equal(originalWire, copiedWire) || crypto.Keccak256Hash(originalWire) != copied.Hash() {
			continue
		}
		from, senderErr := transaction.Sender(signer, original)
		if senderErr == nil {
			copied.SetSenderMemo(signer, from)
		}
	}
}

// BeginSpeculativeAppliedChild executes a fully selected advisory candidate
// over a fixed PlainState MVCC snapshot at the exact durable applied parent.
// It does not take bc.lock, use the live QMDB tree, compute a root, or publish
// a block. The caller must later bind the result to the real native request.
// BuildParallel cannot be interrupted mid-wave; cancellation is checked
// before and after that bounded (200k transactions, 64 MiB input) run, and
// the owner must join its worker before closing or replacing its lease.
func (bc *BlockChain) BeginSpeculativeAppliedChild(ctx context.Context, parentHash types.Hash, parentNumber uint64, binding types.Hash, advisoryTemplate *block.Block) (_ *SpeculativeAppliedChild, err error) {
	if bc.chainConfig == nil || bc.chainConfig.Performance == nil || !bc.qmdbEnabled || !bc.canonicalByCommitOnly() || bc.chainConfig.DeferredExecutionTime != nil || advisoryTemplate == nil || parentNumber == ^uint64(0) || binding == (types.Hash{}) {
		return nil, errors.New("speculative applied-child execution unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	engine, ok := bc.engine.(*hotstuff.HotStuff)
	if !ok {
		return nil, errors.New("speculative child requires public HotStuff engine")
	}
	owned, err := advisoryTemplate.Marshal()
	if err != nil || len(owned) > maxSpeculativeChildBytes {
		return nil, errors.New("speculative child template exceeds owned byte limit")
	}
	var template block.Block
	if err := template.Unmarshal(owned); err != nil {
		return nil, err
	}
	header, headerOK := template.Header().(*block.Header)
	body, bodyOK := template.Body().(*block.Body)
	if !headerOK || header == nil || header.Number == nil || !header.Number.IsUint64() || header.Number.Uint64() != parentNumber+1 || header.ParentHash != parentHash || !bodyOK || body == nil || body.IsFrameBody() || len(template.Transactions()) > maxSpeculativeChildTransactions || len(body.Verifiers) > 0 || len(body.Rewards) > 0 || len(body.ZkProof) > 0 || template.TxHash() != block.BodyTxRoot(body, header.Time) {
		return nil, errors.New("speculative child parent/body mismatch")
	}
	if header.Root != (types.Hash{}) || header.ReceiptHash != (types.Hash{}) || header.Bloom != (block.Bloom{}) || header.GasUsed != 0 || header.Difficulty == nil || !header.Difficulty.IsZero() || header.Nonce != (block.BlockNonce{}) {
		return nil, errors.New("speculative child execution fields are set")
	}
	if bc.chainConfig.IsBAL(header.Time) || bc.chainConfig.IsMobileAnchor(header.Time) || bc.chainConfig.IsDAOFork(header.Number.Uint64()) {
		return nil, errors.New("speculative child scheduled fork adapter is not wired")
	}
	if err := engine.ValidateUnsignedCandidateHeader(bc, header); err != nil {
		return nil, err
	}
	// Strict ingress may have attached a sender memo to each privately
	// selected transaction after checking its signed bytes against the live,
	// signature-verified queue. The ownership copy above drops those memos.
	// Carry one across only for the same fork signer and exactly identical
	// freshly encoded signed fields, never from a wire-declared From address.
	// A miss leaves the normal signature-recovery path intact.
	originalTxs, copiedTxs := advisoryTemplate.Transactions(), template.Transactions()
	if len(originalTxs) != len(copiedTxs) {
		return nil, ErrSpeculativeChildMismatch
	}
	copySpeculativeSenderMemos(bc.chainConfig, header, originalTxs, copiedTxs)
	view, err := hotstuff.ExtractViewFromExtra(header.Extra)
	if err != nil {
		return nil, err
	}
	key, err := speculativeExecutionKey(&template)
	if err != nil {
		return nil, err
	}
	cfg, err := json.Marshal(bc.chainConfig)
	if err != nil {
		return nil, err
	}
	tx, err := bc.ChainDB.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	child := &SpeculativeAppliedChild{chain: bc, tx: tx, header: header, txs: copiedTxs, parentHash: parentHash, parentNum: parentNumber, binding: binding, view: view, config: crypto.Keccak256Hash(cfg), key: key}
	transferred := false
	defer func() {
		// A panic also retains no lease. The owner-side worker recovers, but
		// Begin has not returned its child to that worker yet.
		if !transferred {
			child.Close()
		}
	}()
	an, ah, applied, err := rawdb.ReadQMDBApplied(tx)
	if err != nil {
		return nil, err
	}
	if !applied || an != parentNumber || ah != parentHash {
		return nil, ErrPerformanceParentNotHead
	}
	parent := rawdb.ReadHeader(tx, parentHash, parentNumber)
	if parent == nil || parent.Hash() != parentHash || parent.Number == nil || !parent.Number.IsUint64() || parent.Number.Uint64() != parentNumber {
		return nil, errors.New("speculative applied parent header unavailable")
	}
	child.parentRoot = parent.Root
	reader := state.NewPlainStateReader(tx)
	prefetch := state.NewAccountPrefetch(reader)
	ibs := state.New(prefetch)
	ibs.SetAccountPrefetch(prefetch)
	ibs.SetDetachedPendingCredits(true)
	ibs.SetSpeculativePlainReaders(parentHash, parentNumber)
	child.state = ibs
	getHash := GetHashFn(header, func(hash types.Hash, number uint64) *block.Header { return rawdb.ReadHeader(tx, hash, number) })
	executionStart := startExecutionInterval(ctx)
	included, receipts, gas, failed, err := bc.BuildParallel(header, child.txs, ibs, getHash)
	observeExecutionInterval(ctx, executionStart, err)
	if err != nil {
		return nil, err
	}
	if failed != 0 || len(included) != len(child.txs) {
		return nil, errors.New("speculative child rejected selected candidates")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	header.GasUsed = gas
	child.txs, child.receipts = included, receipts
	transferred = true
	return child, nil
}

// CompleteSpeculativeAppliedChild accepts the EVM result only for the exact
// actual child execution key and still validates the full public header/QC.
// It waits for the mutation lock, rechecks the applied parent, then owns the
// miner QMDB computer through root computation. All paths consume child.
func (bc *BlockChain) CompleteSpeculativeAppliedChild(ctx context.Context, child *SpeculativeAppliedChild, binding types.Hash, actualTemplate *block.Block, prepare bool) (_ *block.Block, _ rawdb.ExecutedResult, _ *PreparedUnsignedCandidate, err error) {
	var zero rawdb.ExecutedResult
	if child == nil || actualTemplate == nil {
		return nil, zero, nil, ErrSpeculativeChildMismatch
	}
	child.mu.Lock()
	defer child.mu.Unlock()
	defer child.closeLocked()
	if child.closed || child.chain != bc || child.state == nil || binding == (types.Hash{}) || child.binding != binding {
		return nil, zero, nil, ErrSpeculativeChildMismatch
	}
	if err := ctx.Err(); err != nil {
		return nil, zero, nil, err
	}
	raw, err := actualTemplate.Marshal()
	if err != nil || len(raw) > maxSpeculativeChildBytes {
		return nil, zero, nil, ErrSpeculativeChildMismatch
	}
	var actual block.Block
	if err := actual.Unmarshal(raw); err != nil {
		return nil, zero, nil, ErrSpeculativeChildMismatch
	}
	actualKey, err := speculativeExecutionKey(&actual)
	if err != nil || !bytes.Equal(actualKey, child.key) {
		return nil, zero, nil, ErrSpeculativeChildMismatch
	}
	header, ok := actual.Header().(*block.Header)
	if !ok || header == nil {
		return nil, zero, nil, ErrSpeculativeChildMismatch
	}
	// The advisory header is private and may carry a provisional Extra. Root
	// finalization must see the actual request's publicly verified Extra.
	actualHeader := block.CopyHeader(header)
	actualHeader.GasUsed = child.header.GasUsed
	bc.lock.Lock()
	defer bc.lock.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, zero, nil, err
	}
	engine, ok := bc.engine.(*hotstuff.HotStuff)
	if !ok {
		return nil, zero, nil, errors.New("speculative child HotStuff engine unavailable")
	}
	actualView, err := hotstuff.ExtractViewFromExtra(header.Extra)
	if err != nil || actualView != child.view {
		return nil, zero, nil, ErrSpeculativeChildMismatch
	}
	if err := engine.ValidateUnsignedCandidateHeader(bc, header); err != nil {
		return nil, zero, nil, err
	}
	if err := bc.joinLagWrite(); err != nil {
		return nil, zero, nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, zero, nil, err
	}
	tx, err := bc.ChainDB.BeginRo(ctx)
	if err != nil {
		return nil, zero, nil, err
	}
	defer tx.Rollback()
	an, ah, applied, err := rawdb.ReadQMDBApplied(tx)
	if err != nil {
		return nil, zero, nil, err
	}
	if !applied || an != child.parentNum || ah != child.parentHash {
		return nil, zero, nil, ErrPerformanceParentNotHead
	}
	parent := rawdb.ReadHeader(tx, ah, an)
	if parent == nil || parent.Root != child.parentRoot || parent.Hash() != child.parentHash {
		return nil, zero, nil, ErrSpeculativeChildMismatch
	}
	if bc.qmdbRootComputer == nil || bc.qmdbRootComputer.Root() != child.parentRoot {
		return nil, zero, nil, ErrSpeculativeChildMismatch
	}
	cfg, err := json.Marshal(bc.chainConfig)
	if err != nil || crypto.Keccak256Hash(cfg) != child.config {
		return nil, zero, nil, ErrSpeculativeChildMismatch
	}
	rc, err := bc.NewMinerRootComputer(tx, child.parentRoot)
	if err != nil {
		return nil, zero, nil, err
	}
	qmdb, ok := rc.(*commitment.QMDBRootComputer)
	if !ok {
		return nil, zero, nil, errors.New("speculative child QMDB computer unavailable")
	}
	defer qmdb.SetCold(nil)
	child.state.SetRootComputer(rc)
	if err := child.state.FinalizeTx(bc.chainConfig.RulesWithTimestamp(actualHeader.Number.Uint64(), actualHeader.Time), state.NewNoopWriter()); err != nil {
		return nil, zero, nil, err
	}
	if _, err := ProcessExecutionBlockEnd(nil, bc.chainConfig, child.state, actualHeader, engine); err != nil {
		return nil, zero, nil, err
	}
	built, _, nopay, err := engine.FinalizeAndAssemble(bc, actualHeader, child.state, child.txs, nil, child.receipts)
	if err != nil {
		return nil, zero, nil, err
	}
	root, computed := child.state.LastIntermediateRoot()
	if !computed {
		return nil, zero, nil, errors.New("speculative child produced no execution root")
	}
	result := ExecutedResultFor(bc.chainConfig, actualHeader.Number.Uint64(), root, child.receipts)
	b, ok := built.(*block.Block)
	if !ok {
		return nil, zero, nil, errors.New("speculative child assembled unexpected block type")
	}
	h := b.Header().(*block.Header)
	if h.Root != result.Root || h.ReceiptHash != result.ReceiptHash || h.GasUsed != result.GasUsed || h.Bloom != result.Bloom {
		return nil, zero, nil, errors.New("speculative child assembled inconsistent result")
	}
	var candidate *PreparedUnsignedCandidate
	if prepare {
		candidate, err = bc.prepareUnsignedCandidate(b, result, child.state, child.receipts, nopay, child.parentRoot)
		if err != nil {
			return nil, zero, nil, fmt.Errorf("prepare speculative child: %w", err)
		}
	}
	return b, result, candidate, nil
}
