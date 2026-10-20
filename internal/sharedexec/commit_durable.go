package sharedexec

import (
	"errors"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// durableStoredCommit requires exclusive execute ownership. Unlike durable's
// import path, its block is read here from the database, never supplied by a
// client. The same snapshot supplies its body, senders and checked receipts.
func (s *Server) durableStoredCommit(request CommitRequest) (transactions int, err error) {
	if !s.chain.HasAppliedBlock(request.BlockHash, request.Number) {
		return 0, errors.New("commit requires actual durable execution")
	}
	err = s.chain.DB().View(s.ctx, func(tx kv.Tx) error {
		number := rawdb.ReadHeaderNumber(tx, request.BlockHash)
		if number == nil || *number != request.Number {
			return errors.New("commit stored block number missing or inconsistent")
		}
		stored, readErr := rawdb.ReadBlockByHash(tx, request.BlockHash)
		if readErr != nil {
			return readErr
		}
		if stored == nil || stored.Number64() == nil || !stored.Number64().IsUint64() || stored.Number64().Uint64() != request.Number || stored.Hash() != request.BlockHash || stored.ParentHash() != request.ParentHash {
			return errors.New("commit stored block identity mismatch or missing block")
		}
		header, ok := stored.Header().(*block.Header)
		if !ok || header == nil {
			return errors.New("commit stored execution header missing")
		}
		_, _, present, readErr := rawdb.ReadQMDBApplied(tx)
		if readErr != nil {
			return readErr
		}
		if !present {
			return errors.New("commit durable applied marker missing")
		}
		// Receipts are keyed by height. A new applied block may not yet have
		// a canonical row; a row belonging to a sibling must never authorize it.
		canonical, readErr := rawdb.ReadCanonicalHash(tx, request.Number)
		if readErr != nil {
			return readErr
		}
		if canonical != (types.Hash{}) && canonical != request.BlockHash {
			return errors.New("commit durable receipts belong to a different canonical block")
		}
		senders, readErr := rawdb.ReadSenders(tx, request.BlockHash, request.Number)
		if readErr != nil {
			return readErr
		}
		receipts, readErr := rawdb.ReadReceiptsWithError(tx, stored, senders)
		if readErr != nil {
			return readErr
		}
		executed := internal.ExecutedResultFor(s.chain.Config(), request.Number, header.Root, receipts)
		if executed.ReceiptHash != header.ReceiptHash || executed.GasUsed != header.GasUsed || executed.Bloom != header.Bloom {
			return errors.New("durable execution result does not match stored header")
		}
		transactions = len(stored.Transactions())
		return nil
	})
	return transactions, err
}
