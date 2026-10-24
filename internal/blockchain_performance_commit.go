package internal

import (
	"bytes"
	"errors"
	"slices"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// CertifiedCanonicalHead identifies the canonical prefix checked under bc.lock.
// It is an observation, not a reusable authorization or cached certificate.
type CertifiedCanonicalHead struct {
	HeadHash   types.Hash
	HeadNumber uint64
}

// VerifyCertifiedCanonicalPrefix freshly verifies the public CommitQC and all
// existing canonical, finality, applied-lineage and transaction-index evidence.
// A false result leaves legacy canonicalization/repair to the existing writer.
// This method never joins lag writes or mutates the database.
func (bc *BlockChain) VerifyCertifiedCanonicalPrefix(qc *hotstuff.QuorumCertificate) (CertifiedCanonicalHead, bool, error) {
	proof, existing, err := bc.verifyCertifiedCanonicalPrefix(qc, false)
	return CertifiedCanonicalHead{HeadHash: proof.HeadHash, HeadNumber: proof.HeadNumber}, existing, err
}

// CertifiedCanonicalExecution is a scalar observation from one fresh database
// snapshot. It contains no reusable execution capability or decoded block.
type CertifiedCanonicalExecution struct {
	HeadHash     types.Hash
	HeadNumber   uint64
	BlockHash    types.Hash
	ParentHash   types.Hash
	Number       uint64
	Transactions int
}

// VerifyCertifiedCanonicalExecution also verifies durable receipts using the
// block read by the chain itself. Decoded bodies and transaction hashes live
// only within this physical verification. The shared executor may coalesce
// overlapping identical full-certificate requests into its scalar observation;
// completed observations are never cached.
func (bc *BlockChain) VerifyCertifiedCanonicalExecution(qc *hotstuff.QuorumCertificate) (CertifiedCanonicalExecution, bool, error) {
	return bc.verifyCertifiedCanonicalPrefix(qc, true)
}

func (bc *BlockChain) verifyCertifiedCanonicalPrefix(qc *hotstuff.QuorumCertificate, checkExecution bool) (CertifiedCanonicalExecution, bool, error) {
	bc.lock.RLock()
	defer bc.lock.RUnlock()
	engine, ok := bc.engine.(*hotstuff.HotStuff)
	if !ok || bc.chainConfig == nil || bc.chainConfig.Performance == nil || !bc.qmdbEnabled || qc == nil {
		return CertifiedCanonicalExecution{}, false, errors.New("certified canonical prefix requires performance HotStuff QMDB executor")
	}
	if bc.lagWrite.inflight != nil {
		return CertifiedCanonicalExecution{}, false, nil
	}
	var header *block.Header
	if err := bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
		var err error
		header, err = rawdb.ReadHeaderByHash(tx, qc.BlockHash)
		return err
	}); err != nil {
		return CertifiedCanonicalExecution{}, false, err
	}
	if err := engine.VerifyPerformanceCommitCertificate(bc, header, qc); err != nil {
		return CertifiedCanonicalExecution{}, false, err
	}
	var proof CertifiedCanonicalExecution
	var existing bool
	err := bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
		b, err := rawdb.ReadBlockByHash(tx, qc.BlockHash)
		if err != nil {
			return err
		}
		if header == nil || header.Number == nil || !header.Number.IsUint64() || b == nil || b.Hash() != qc.BlockHash || b.Number64() == nil || !b.Number64().IsUint64() || b.Number64().Uint64() != header.Number.Uint64() || b.ParentHash() != header.ParentHash || header.Hash() != qc.BlockHash {
			return errors.New("certified canonical stored block identity differs")
		}
		number := header.Number.Uint64()
		head := rawdb.ReadHeadBlockHash(tx)
		hn := rawdb.ReadHeaderNumber(tx, head)
		if hn == nil || head == (types.Hash{}) {
			return errors.New("certified canonical head missing")
		}
		canonical, err := rawdb.ReadCanonicalHash(tx, *hn)
		if err != nil {
			return err
		}
		validHeader := func(hash types.Hash, n uint64) bool {
			h := rawdb.ReadHeader(tx, hash, n)
			return h != nil && h.Number != nil && h.Number.IsUint64() && h.Number.Uint64() == n && h.Hash() == hash
		}
		if canonical != head || !validHeader(head, *hn) {
			return errors.New("certified canonical head is inconsistent")
		}
		if number > *hn {
			return nil
		}
		canonical, err = rawdb.ReadCanonicalHash(tx, number)
		if err != nil {
			return err
		}
		if canonical != qc.BlockHash || !validHeader(qc.BlockHash, number) {
			return errors.New("certified block conflicts with canonical prefix")
		}
		finalized := rawdb.ReadHotStuffCommittedHead(tx)
		if finalized == (types.Hash{}) {
			return nil
		}
		fn := rawdb.ReadHeaderNumber(tx, finalized)
		if fn == nil || *fn > *hn || !validHeader(finalized, *fn) {
			return errors.New("certified canonical finality marker is inconsistent")
		}
		canonical, err = rawdb.ReadCanonicalHash(tx, *fn)
		if err != nil {
			return err
		}
		if canonical != finalized {
			return errors.New("certified canonical finality marker is off prefix")
		}
		if *fn < number {
			return nil
		}
		an, ah, present, err := rawdb.ReadQMDBApplied(tx)
		if err != nil {
			return err
		}
		if !present || an < number {
			return errors.New("certified canonical applied marker missing or behind block")
		}
		cur, curN := types.Hash(ah), an
		for curN > number && an-curN < 256 {
			h := rawdb.ReadHeader(tx, cur, curN)
			if h == nil || h.Number == nil || !h.Number.IsUint64() || h.Number.Uint64() != curN || h.Hash() != cur {
				return errors.New("certified canonical applied lineage header differs")
			}
			cur, curN = h.ParentHash, curN-1
		}
		if curN != number || cur != qc.BlockHash {
			return errors.New("certified block is outside applied lineage")
		}
		if checkExecution {
			senders, err := rawdb.ReadSenders(tx, b.Hash(), number)
			if err != nil {
				return err
			}
			receipts, err := rawdb.ReadReceiptsWithError(tx, b, senders)
			if err != nil {
				return err
			}
			if len(receipts) != len(b.Transactions()) {
				return errors.New("certified canonical execution receipt count mismatch")
			}
			executed := ExecutedResultFor(bc.chainConfig, number, header.Root, receipts)
			if executed.ReceiptHash != header.ReceiptHash || executed.GasUsed != header.GasUsed || executed.Bloom != header.Bloom {
				return errors.New("certified canonical durable execution does not match stored header")
			}
		}
		if bc.txIndexer == nil {
			want := b.Number64().Bytes()
			// Visit every index entry in key order for locality in the existing
			// transaction cursor. Keep duplicates and verify each value freshly.
			hashes := make([]types.Hash, len(b.Transactions()))
			for i, txn := range b.Transactions() {
				hashes[i] = txn.Hash()
			}
			slices.SortFunc(hashes, func(a, b types.Hash) int {
				return bytes.Compare(a[:], b[:])
			})
			for i := range hashes {
				entry, err := tx.GetOne(modules.TxLookup, hashes[i][:])
				if err != nil {
					return err
				}
				if !bytes.Equal(entry, want) {
					return nil
				}
			}
		}
		proof, existing = CertifiedCanonicalExecution{HeadHash: head, HeadNumber: *hn, BlockHash: b.Hash(), ParentHash: b.ParentHash(), Number: number, Transactions: len(b.Transactions())}, true
		return nil
	})
	if err != nil {
		return CertifiedCanonicalExecution{}, false, err
	}
	return proof, existing, nil
}

// CommitExecutedExtension is the performance executor's append-only commit.
// It serializes the final applied-lineage check and canonicalization with all
// state mutations, including BlockChain's own background future-block retries.
// The caller must authorize finality before calling; this method verifies no QC.
// Existing native/ETH CommitToCanonical callers retain their existing behavior.
func (bc *BlockChain) CommitExecutedExtension(hash types.Hash) error {
	bc.lock.Lock()
	defer bc.lock.Unlock()
	return bc.commitExecutedExtension(hash)
}

// CommitCertifiedExtension verifies a strict CommitQC before changing finality.
// This first bridge uses the fixed seven-member genesis validator set only.
func (bc *BlockChain) CommitCertifiedExtension(qc *hotstuff.QuorumCertificate) error {
	bc.lock.Lock()
	defer bc.lock.Unlock()
	engine, ok := bc.engine.(*hotstuff.HotStuff)
	if !ok || bc.chainConfig == nil || bc.chainConfig.Performance == nil || qc == nil {
		return errors.New("certified commit requires performance HotStuff executor")
	}
	var header *block.Header
	if err := bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
		var err error
		header, err = rawdb.ReadHeaderByHash(tx, qc.BlockHash)
		return err
	}); err != nil {
		return err
	}
	if err := engine.VerifyPerformanceCommitCertificate(bc, header, qc); err != nil {
		return err
	}
	return bc.commitExecutedExtension(qc.BlockHash)
}

// performanceCommitSnapshotReader prevents certificate verification from using
// a cached or differently timed parent header while the durable block and
// canonical lineage are checked in one database snapshot.
type performanceCommitSnapshotReader struct {
	*BlockChain
	tx kv.Tx
}

func (r performanceCommitSnapshotReader) GetHeaderByHash(hash types.Hash) (block.IHeader, error) {
	header, err := rawdb.ReadHeaderByHash(r.tx, hash)
	if err != nil || header == nil {
		return nil, err
	}
	if header.Number == nil || !header.Number.IsUint64() || header.Hash() != hash {
		return nil, errors.New("certified durable parent header identity mismatch")
	}
	return header, nil
}

// CommitCertifiedDurableExtension verifies a fresh certified block's durable
// execution and lineage in the canonical writer transaction. That transaction
// uses the decoded block for its canonical writes, then publishes it only after
// commit; neither a prior caller observation nor a hot block cache authorizes
// finality.
func (bc *BlockChain) CommitCertifiedDurableExtension(qc *hotstuff.QuorumCertificate, number uint64, parent types.Hash) (int, error) {
	bc.lock.Lock()
	defer bc.lock.Unlock()
	engine, ok := bc.engine.(*hotstuff.HotStuff)
	if !ok || bc.chainConfig == nil || bc.chainConfig.Performance == nil || !bc.qmdbEnabled || qc == nil {
		return 0, errors.New("certified durable commit requires performance HotStuff QMDB executor")
	}
	if err := bc.joinLagWrite(); err != nil {
		return 0, err
	}
	// Perform certificate crypto and sealed-header verification outside MDBX's
	// single writer. The writer preflight below re-reads the header and all
	// mutable authorization rows in its own transaction before any mutation.
	var certificateHeader *block.Header
	if err := bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
		var err error
		certificateHeader, err = rawdb.ReadHeaderByHash(tx, qc.BlockHash)
		if err != nil {
			return err
		}
		return engine.VerifyPerformanceCommitCertificate(performanceCommitSnapshotReader{bc, tx}, certificateHeader, qc)
	}); err != nil {
		return 0, err
	}
	var transactions int
	preflight := func(tx kv.RwTx) (*block.Block, error) {
		stored, err := rawdb.ReadBlockByHash(tx, qc.BlockHash)
		if err != nil {
			return nil, err
		}
		if stored == nil || stored.Number64() == nil || !stored.Number64().IsUint64() || stored.Number64().Uint64() != number || stored.Hash() != qc.BlockHash || stored.ParentHash() != parent {
			return nil, errors.New("certified durable stored block identity mismatch or missing block")
		}
		header, ok := stored.Header().(*block.Header)
		if !ok || header == nil || header.Number == nil || !header.Number.IsUint64() || header.Number.Uint64() != number || header.Hash() != qc.BlockHash || certificateHeader == nil || header.Hash() != certificateHeader.Hash() {
			return nil, errors.New("certified durable stored header identity mismatch")
		}
		head := rawdb.ReadHeadBlockHash(tx)
		hn := rawdb.ReadHeaderNumber(tx, head)
		if hn == nil {
			return nil, errors.New("executed extension canonical head missing")
		}
		canonical, err := rawdb.ReadCanonicalHash(tx, *hn)
		if err != nil {
			return nil, err
		}
		if canonical != head {
			return nil, errors.New("executed extension canonical head inconsistent")
		}
		headHeader := rawdb.ReadHeader(tx, head, *hn)
		if headHeader == nil || headHeader.Number == nil || !headHeader.Number.IsUint64() || headHeader.Number.Uint64() != *hn || headHeader.Hash() != head {
			return nil, errors.New("executed extension canonical head header inconsistent")
		}
		if *hn == ^uint64(0) {
			return nil, errors.New("executed extension canonical height overflow")
		}
		finalized := rawdb.ReadHotStuffCommittedHead(tx)
		if finalized != (types.Hash{}) {
			fn := rawdb.ReadHeaderNumber(tx, finalized)
			if fn == nil || *fn > *hn {
				return nil, errors.New("certified durable finality marker inconsistent")
			}
			canonical, err = rawdb.ReadCanonicalHash(tx, *fn)
			if err != nil {
				return nil, err
			}
			if canonical != finalized {
				return nil, errors.New("certified durable finality marker off canonical prefix")
			}
			finalHeader := rawdb.ReadHeader(tx, finalized, *fn)
			if finalHeader == nil || finalHeader.Number == nil || !finalHeader.Number.IsUint64() || finalHeader.Number.Uint64() != *fn || finalHeader.Hash() != finalized {
				return nil, errors.New("certified durable finality header inconsistent")
			}
		}
		if number != *hn+1 || parent != head {
			return nil, errors.New("executed extension requires direct canonical parent")
		}
		canonical, err = rawdb.ReadCanonicalHash(tx, number)
		if err != nil {
			return nil, err
		}
		if canonical != (types.Hash{}) && canonical != qc.BlockHash {
			return nil, errors.New("commit durable receipts belong to a different canonical block")
		}
		an, ah, applied, err := rawdb.ReadQMDBApplied(tx)
		if err != nil {
			return nil, err
		}
		if !applied || number > an {
			return nil, errors.New("executed extension applied marker missing")
		}
		cur, curN := types.Hash(ah), an
		for curN > number && an-curN < 256 {
			h := rawdb.ReadHeader(tx, cur, curN)
			if h == nil || h.Number == nil || !h.Number.IsUint64() || h.Number.Uint64() != curN || h.Hash() != cur {
				return nil, errors.New("executed extension applied lineage header differs")
			}
			cur, curN = h.ParentHash, curN-1
		}
		if curN != number || cur != qc.BlockHash {
			return nil, errors.New("executed extension is no longer on applied lineage; retry")
		}
		senders, err := rawdb.ReadSenders(tx, qc.BlockHash, number)
		if err != nil {
			return nil, err
		}
		receipts, err := rawdb.ReadReceiptsForExecutionWithError(tx, stored, senders)
		if err != nil {
			return nil, err
		}
		if len(receipts) != len(stored.Transactions()) {
			return nil, errors.New("certified durable receipt count mismatch")
		}
		executed := ExecutedResultFor(bc.chainConfig, number, header.Root, receipts)
		if executed.ReceiptHash != header.ReceiptHash || executed.GasUsed != header.GasUsed || executed.Bloom != header.Bloom {
			return nil, errors.New("durable execution result does not match stored header")
		}
		transactions = len(stored.Transactions())
		return stored, nil
	}
	if err := bc.commitToCanonicalWithPreflight(qc.BlockHash, nil, true, preflight); err != nil {
		return 0, err
	}
	return transactions, nil
}

func (bc *BlockChain) commitExecutedExtension(hash types.Hash) error {
	if bc.chainConfig == nil || bc.chainConfig.Performance == nil || !bc.qmdbEnabled {
		return errors.New("executed extension requires explicit performance QMDB chain")
	}
	if err := bc.joinLagWrite(); err != nil {
		return err
	}
	var number uint64
	err := bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
		b, err := rawdb.ReadBlockByHash(tx, hash)
		if err != nil {
			return err
		}
		if b == nil || b.Hash() != hash {
			return errors.New("executed extension block missing")
		}
		number = b.Number64().Uint64()
		head := rawdb.ReadHeadBlockHash(tx)
		hn := rawdb.ReadHeaderNumber(tx, head)
		if hn == nil {
			return errors.New("executed extension canonical head missing")
		}
		canonical, err := rawdb.ReadCanonicalHash(tx, *hn)
		if err != nil {
			return err
		}
		if canonical != head {
			return errors.New("executed extension canonical head inconsistent")
		}
		if number <= *hn {
			canonical, err := rawdb.ReadCanonicalHash(tx, number)
			if err != nil {
				return err
			}
			if canonical != hash {
				return errors.New("executed extension conflicts with canonical prefix")
			}
		} else if number != *hn+1 || b.ParentHash() != head {
			return errors.New("executed extension requires direct canonical parent")
		}
		_, _, applied, err := rawdb.ReadQMDBApplied(tx)
		if err != nil {
			return err
		}
		if !applied {
			return errors.New("executed extension applied marker missing")
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !bc.HasAppliedBlock(hash, number) {
		return errors.New("executed extension is no longer on applied lineage; retry")
	}
	return bc.commitToCanonicalWith(hash, nil, true)
}
