// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// QMDB-native eth_getProof provider. The QMDB world root is a Blake3 twig-forest
// commitment (not an MPT), so it serves QMDB membership proofs: each proof is a
// single self-describing blob (qmdb.Proof.Marshal) carried in the EIP-1186
// accountProof / storageProof arrays. A client detects the QMDB backend from the
// proof descriptor and verifies with qmdb.VerifyEncodedProofForKey against the block's
// stateRoot.
//
// The running node does not keep a live QMDB commitment (QMDB is built offline by
// the replay engine), so this provider lazily reloads the twig forest from the
// persisted entry log and caches it, rebuilding only when the head root changes.
//
// QMDB is history-dependent, so arbitrary past states are NOT provable — but the
// last W heights are: the replay engine records a tiny per-block undo record
// (QMDBUndoWindow table), and proofs at a recent height replay those records on
// scratch copies (qmdb.Tree.ProofAt), verifying the reconstructed root against
// the target block's header before serving anything.

package internal

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/params"
)

// errQMDBOutsideWindow marks a height outside the recent-blocks undo window
// (QMDB cannot prove arbitrary past states from the window alone — the root is
// history-dependent). The provider turns it into a StateProofUnavailableError
// carrying the provable range; it is never swallowed into a placeholder proof.
var errQMDBOutsideWindow = errors.New("height outside the recent-blocks QMDB proof window")

// QMDBStateProofProvider serves QMDB-native membership proofs for eth_getProof.
type QMDBStateProofProvider struct {
	mu         sync.Mutex
	rc         *commitment.QMDBRootComputer
	loadedRoot types.Hash
	// history: older heights come from the full-history journal.
	history atomic.Bool
	// config decides what a header's Root means (its own state before the
	// deferred-execution fork, the parent's after it).
	config *params.ChainConfig
}

// NewQMDBStateProofProvider builds the provider (tree loaded lazily on first use).
func NewQMDBStateProofProvider(config *params.ChainConfig) *QMDBStateProofProvider {
	return &QMDBStateProofProvider{config: config}
}

// Descriptor reports the QMDB proof semantics so clients route verification to
// qmdb.VerifyEncodedProofForKey and do not expect MPT/EIP-1186 node lists.
func (p *QMDBStateProofProvider) Descriptor() StateProofDescriptor {
	if p.history.Load() {
		return StateProofDescriptor{
			Backend:            StateProofBackendQMDB,
			ProofRootScheme:    state.RootSchemeQMDB,
			Semantics:          StateProofSemanticsHashOnly,
			StorageHash:        StorageHashSemanticsNone,
			SupportsHistorical: true, // every height from the journal start
			HistoricalMode:     StateProofHistoricalFull,
			RecentWindow:       QMDBUndoWindow(),
		}
	}
	return StateProofDescriptor{
		Backend:            StateProofBackendQMDB,
		ProofRootScheme:    state.RootSchemeQMDB,
		Semantics:          StateProofSemanticsHashOnly, // not EIP-1186 node lists
		StorageHash:        StorageHashSemanticsNone,    // QMDB has no per-account storage trie
		// Only the recent undo window is provable: not "historical" in the
		// archive sense a client would assume from a bare true.
		SupportsHistorical: false,
		HistoricalMode:     StateProofHistoricalRecent,
		RecentWindow:       QMDBUndoWindow(),
	}
}

// AccountProof returns a QMDB membership proof blob for the account, or an empty
// array when the account is absent from the committed set.
func (p *QMDBStateProofProvider) AccountProof(tx kv.Tx, address types.Address, blockNrOrHash jsonrpc.BlockNumberOrHash) ([]string, error) {
	return p.proofFor(tx, qmdb.Hash(commitment.AccountKeyHash(address)), blockNrOrHash)
}

// StorageProof returns a QMDB membership proof blob for the storage slot, or an
// empty array when the slot is zero/absent.
func (p *QMDBStateProofProvider) StorageProof(tx kv.Tx, address types.Address, slot types.Hash, blockNrOrHash jsonrpc.BlockNumberOrHash) ([]string, error) {
	return p.proofFor(tx, qmdb.Hash(commitment.StorageKeyHash(address, slot)), blockNrOrHash)
}

// proofFor serves a membership proof for keyHash at the requested height:
// directly off the loaded forest for the latest block, via the recent-blocks
// undo window (qmdb.Tree.ProofAt) for an older height within the window.
func (p *QMDBStateProofProvider) proofFor(tx kv.Tx, kh qmdb.Hash, blockNrOrHash jsonrpc.BlockNumberOrHash) ([]string, error) {
	// Hold the lock across the whole proof, not just the (re)load. The cached
	// tree's cold/leaf stores are bound to a request tx, and its per-twig
	// hydration mutates shared node state — two concurrent requests sharing the
	// tree would race (corrupt hydration) and, worse, the second request would
	// fault against the first request's already-rolled-back tx (nil-deref
	// panic, recovered per-method but crashing every proof after the first at a
	// given head). Serializing proof generation is cheap once the forest is
	// loaded and removes both hazards.
	p.mu.Lock()
	defer p.mu.Unlock()

	heads, err := readQMDBProofHeads(p.config, tx)
	if err != nil {
		return nil, err
	}
	tree, err := p.ensureTreeLocked(tx, heads.appliedRoot)
	if err != nil {
		return nil, err
	}

	target, ok := resolveProofTarget(tx, blockNrOrHash, heads.committed)
	if !ok {
		return nil, &StateProofUnavailableError{
			Reason: "block is unknown, not canonical or above the committed head",
			Latest: heads.committed, Earliest: p.earliestLocked(tx, heads), HasRange: true,
		}
	}
	if target == heads.applied {
		proof, found := tree.GetProof(kh)
		if !found {
			return []string{}, nil
		}
		return []string{hexutil.Encode(proof.Marshal())}, nil
	}

	// Recent-height path: replay the undo window from the actually-applied
	// state down to the requested committed height. HotStuff may have one or
	// more executed candidates above the committed head; loading the live tree
	// against the committed root would reject every latest proof in that
	// normal speculative window.
	undos, err := rawdb.ReadQMDBUndos(tx, target, heads.applied)
	if err != nil {
		return nil, err
	}
	var (
		proof *qmdb.Proof
		root  qmdb.Hash
		found bool
	)
	switch {
	case undos != nil:
		proof, root, found, err = tree.ProofAt(kh, undos)
	case p.history.Load():
		// Older than the window: the full-history journal.
		proof, root, found, err = p.historyProofLocked(tx, tree, kh, target, heads)
	default:
		return nil, &StateProofUnavailableError{
			Requested: target, HasRequested: true,
			Reason:   errQMDBOutsideWindow.Error() + " (full history needs --state.qmdb-history)",
			Earliest: p.earliestRecentLocked(tx, heads), Latest: heads.committed, HasRange: true,
		}
	}
	if err != nil {
		return nil, err
	}
	// The reconstructed root must equal the target block's executed root — a
	// mismatch means corrupt window/journal data and must never serve a proof.
	if err := p.checkExecutedRoot(tx, target, root); err != nil {
		return nil, err
	}
	if !found {
		return []string{}, nil
	}
	return []string{hexutil.Encode(proof.Marshal())}, nil
}

// checkExecutedRoot compares a reconstructed root with the canonical block's
// executed state root (the header's own Root before the deferred-execution
// fork, the stored executed result after it).
func (p *QMDBStateProofProvider) checkExecutedRoot(tx kv.Tx, target uint64, root qmdb.Hash) error {
	targetHash, err := rawdb.ReadCanonicalHash(tx, target)
	if err != nil {
		return err
	}
	targetHeader := rawdb.ReadHeader(tx, targetHash, target)
	if targetHeader == nil {
		return fmt.Errorf("qmdb proof: header %d unavailable", target)
	}
	targetExec, terr := ExecutedResultOfHeader(p.config, tx, targetHeader)
	if terr != nil {
		return terr
	}
	if types.Hash(root) != targetExec.Root {
		return fmt.Errorf("qmdb proof: reconstructed root %x != executed root %x at block %d",
			root[:8], targetExec.Root[:8], target)
	}
	return nil
}

// historyProofLocked proves keyHash at target from the full-history journal
// (qmdb.Tree.ProofAtHeightFrom). Only heights inside the journal's valid range
// are attempted; the caller root-checks the result.
func (p *QMDBStateProofProvider) historyProofLocked(tx kv.Tx, tree *qmdb.Tree, kh qmdb.Hash, target uint64, heads qmdbProofHeads) (*qmdb.Proof, qmdb.Hash, bool, error) {
	start, haveStart, broken, haveBroken, err := commitment.QMDBHistoryRange(tx)
	if err != nil {
		return nil, qmdb.Hash{}, false, err
	}
	unavailable := func(reason string) error {
		return &StateProofUnavailableError{
			Requested: target, HasRequested: true, Reason: reason,
			Earliest: p.earliestLocked(tx, heads), Latest: heads.committed, HasRange: true,
		}
	}
	if !haveStart {
		return nil, qmdb.Hash{}, false, unavailable("the QMDB history journal is not initialised")
	}
	if target < start {
		return nil, qmdb.Hash{}, false, unavailable(fmt.Sprintf("below the QMDB history journal start (block %d)", start))
	}
	if haveBroken && target >= broken {
		return nil, qmdb.Hash{}, false, unavailable(fmt.Sprintf("the QMDB history journal is broken from block %d (rebuild or --state.qmdb-history.reseed)", broken))
	}
	// The journal must reach the state the loaded forest holds: a journal
	// that stopped earlier (flag off for a while) resolves every later height
	// to a stale cursor.
	store := commitment.NewMDBXQMDBHistoryStore(tx)
	if e, ok, err := store.FloorBlockPos(heads.applied); err != nil {
		return nil, qmdb.Hash{}, false, err
	} else if !ok || e != tree.NextSlot() {
		return nil, qmdb.Hash{}, false, unavailable("the QMDB history journal does not reach the applied head")
	}
	return tree.ProofAtHeightFrom(store, kh, target)
}

// EnableHistory serves heights older than the undo window from the
// full-history journal (--state.qmdb-history). Call before serving.
func (p *QMDBStateProofProvider) EnableHistory() {
	p.history.Store(true)
}

// earliestLocked is the lowest provable committed height: the journal start
// when the journal is usable, otherwise the undo window's reach.
func (p *QMDBStateProofProvider) earliestLocked(tx kv.Tx, heads qmdbProofHeads) uint64 {
	recent := p.earliestRecentLocked(tx, heads)
	if !p.history.Load() {
		return recent
	}
	start, haveStart, _, haveBroken, err := commitment.QMDBHistoryRange(tx)
	if err != nil || !haveStart || haveBroken || start >= recent {
		return recent
	}
	return start
}

// earliestRecentLocked is the lowest committed height the undo window can
// still prove: ProofAt for target t needs the records (t, applied].
func (p *QMDBStateProofProvider) earliestRecentLocked(tx kv.Tx, heads qmdbProofHeads) uint64 {
	first, ok, err := rawdb.FirstQMDBUndoBlock(tx)
	if err != nil || !ok || first == 0 || first > heads.applied {
		return heads.committed // only the applied head itself
	}
	if e := first - 1; e < heads.committed {
		return e
	}
	return heads.committed
}

// ProvableRange reports the committed heights the provider can prove now.
func (p *QMDBStateProofProvider) ProvableRange(tx kv.Tx) (StateProofRange, error) {
	heads, err := readQMDBProofHeads(p.config, tx)
	if err != nil {
		return StateProofRange{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return StateProofRange{Earliest: p.earliestLocked(tx, heads), Latest: heads.committed}, nil
}

// StorageHash returns the zero hash: QMDB is a unified flat commitment with no
// per-account storage trie root.
func (*QMDBStateProofProvider) StorageHash(_ kv.Tx, _ types.Address, _ []types.Hash, _ []*uint256.Int, _ jsonrpc.BlockNumberOrHash) (types.Hash, error) {
	return types.Hash{}, nil
}

// ensureTreeLocked returns the loaded QMDB tree for the latest state, reloading
// from the persisted entry log when the head root has changed. The caller must
// hold p.mu. It always re-points the tree's cold/leaf stores at the current
// request's tx: the cached tree was loaded against an earlier request's tx that
// has since been rolled back, and a cold fault against an expired tx is a
// delayed nil-deref panic (see QMDBRootComputer.SetCold).
func (p *QMDBStateProofProvider) ensureTreeLocked(tx kv.Tx, headRoot types.Hash) (*qmdb.Tree, error) {
	if p.rc != nil && p.loadedRoot == headRoot {
		p.rc.SetCold(tx)
		return p.rc.Tree(), nil
	}
	rc := commitment.NewQMDBRootComputer()
	if err := rc.LoadFrom(tx); err != nil {
		return nil, fmt.Errorf("qmdb proof: load forest: %w", err)
	}
	if rc.Root() != headRoot {
		return nil, fmt.Errorf("qmdb proof: reloaded root %x != head root %x", rc.Root(), headRoot)
	}
	rc.SetCold(tx)
	p.rc = rc
	p.loadedRoot = headRoot
	return rc.Tree(), nil
}

type qmdbProofHeads struct {
	applied     uint64
	appliedRoot types.Hash
	committed   uint64
}

// readQMDBProofHeads separates the state the live QMDB forest reflects from
// the RPC-visible committed head. Under HotStuff, importing a proposal executes
// it before the QC commits it, so applied can legitimately be ahead by a small
// speculative window. The undo records bridge that gap for latest proofs.
func readQMDBProofHeads(config *params.ChainConfig, tx kv.Tx) (qmdbProofHeads, error) {
	committedPtr := rawdb.ReadCurrentFullBlockNumber(tx)
	if committedPtr == nil {
		return qmdbProofHeads{}, fmt.Errorf("qmdb proof: head block number unavailable")
	}
	committedHash, err := rawdb.ReadCanonicalHash(tx, *committedPtr)
	if err != nil {
		return qmdbProofHeads{}, err
	}
	committedHeader := rawdb.ReadHeader(tx, committedHash, *committedPtr)
	if committedHeader == nil {
		return qmdbProofHeads{}, fmt.Errorf("qmdb proof: committed header %d unavailable", *committedPtr)
	}

	committedExec, cerr := ExecutedResultOfHeader(config, tx, committedHeader)
	if cerr != nil {
		return qmdbProofHeads{}, cerr
	}
	heads := qmdbProofHeads{
		applied:     *committedPtr,
		appliedRoot: committedExec.Root, // the state AFTER the committed block (its header carries the parent's under deferred execution)
		committed:   *committedPtr,
	}
	appliedNum, appliedHash, ok, err := rawdb.ReadQMDBApplied(tx)
	if err != nil {
		return qmdbProofHeads{}, err
	}
	if !ok {
		return heads, nil
	}
	if appliedNum < heads.committed {
		return qmdbProofHeads{}, fmt.Errorf("qmdb proof: applied head %d behind committed head %d", appliedNum, heads.committed)
	}
	appliedHeader := rawdb.ReadHeader(tx, appliedHash, appliedNum)
	if appliedHeader == nil {
		return qmdbProofHeads{}, fmt.Errorf("qmdb proof: applied header %d/%x unavailable", appliedNum, appliedHash[:8])
	}
	appliedExec, aerr := ExecutedResultOfHeader(config, tx, appliedHeader)
	if aerr != nil {
		return qmdbProofHeads{}, aerr
	}
	heads.applied = appliedNum
	heads.appliedRoot = appliedExec.Root
	return heads, nil
}

// resolveProofTarget maps the request to a concrete block number ≤ head.
// ok=false for unresolvable requests (future blocks, unknown hashes).
func resolveProofTarget(tx kv.Tx, blockNrOrHash jsonrpc.BlockNumberOrHash, head uint64) (uint64, bool) {
	if blockNr, ok := blockNrOrHash.Number(); ok {
		switch blockNr {
		case jsonrpc.LatestBlockNumber, jsonrpc.PendingBlockNumber,
			jsonrpc.SafeBlockNumber, jsonrpc.FinalizedBlockNumber:
			return head, true
		case jsonrpc.EarliestBlockNumber:
			// Genesis always resolves; whether it is provable is the
			// prover's call (it reports the provable range if not).
			return 0, true
		}
		n := uint64(blockNr.Int64())
		return n, blockNr.Int64() >= 0 && n <= head
	}
	if hash, ok := blockNrOrHash.Hash(); ok {
		header, err := rawdb.ReadHeaderByHash(tx, hash)
		if err != nil || header == nil || header.Number == nil {
			return 0, false
		}
		n := header.Number.Uint64()
		if n > head {
			return 0, false
		}
		canonical, err := rawdb.ReadCanonicalHash(tx, n)
		return n, err == nil && canonical == hash
	}
	return 0, false
}
