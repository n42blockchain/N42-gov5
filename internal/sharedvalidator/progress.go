package sharedvalidator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

var canonicalProgressKey = []byte("performance-canonical-progress-v1")

// CanonicalProgress counts canonical transactions backed by the trusted owner's
// durable execution evidence. It does not count candidate builds, accepted
// imports, votes, or repeated certificates, and is not independent execution proof.
type CanonicalProgress struct {
	Identity                      sharedexec.Identity `json:"identity"`
	Number                        uint64              `json:"number"`
	BlockHash                     types.Hash          `json:"block_hash"`
	CommittedExecutedTransactions uint64              `json:"committed_executed_transactions"`
}

func progressHead(tx kv.Tx) (types.Hash, uint64, error) {
	hash := rawdb.ReadHeadBlockHash(tx)
	number := rawdb.ReadHeaderNumber(tx, hash)
	if number == nil {
		return types.Hash{}, 0, errors.New("canonical progress head missing")
	}
	canonical, err := rawdb.ReadCanonicalHash(tx, *number)
	if err != nil {
		return types.Hash{}, 0, err
	}
	if canonical != hash {
		return types.Hash{}, 0, errors.New("canonical progress head inconsistent")
	}
	return hash, *number, nil
}

func readCanonicalProgress(tx kv.Tx, identity sharedexec.Identity) (CanonicalProgress, error) {
	var progress CanonicalProgress
	raw, err := tx.GetOne(modules.HotStuffState, canonicalProgressKey)
	if err != nil {
		return progress, err
	}
	if len(raw) == 0 || len(raw) > 1024 {
		return progress, errors.New("canonical progress record missing or oversized")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&progress); err != nil {
		return progress, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return progress, errors.New("canonical progress record has trailing data")
	}
	head, number, err := progressHead(tx)
	if err != nil {
		return progress, err
	}
	if progress.Identity != identity || progress.Number != number || progress.BlockHash != head || (number == 0 && (head != identity.GenesisHash || progress.CommittedExecutedTransactions != 0)) {
		return progress, errors.New("canonical progress identity/head mismatch")
	}
	return progress, nil
}

func writeCanonicalProgress(tx kv.RwTx, progress CanonicalProgress) error {
	raw, err := json.Marshal(progress)
	if err != nil {
		return err
	}
	return tx.Put(modules.HotStuffState, canonicalProgressKey, raw)
}

// Count the stored body metadata, excluding the two reserved system positions.
// No repeated full transaction-body decoding is needed on the commit hot path.
func storedTransactionCount(tx kv.Tx, hash types.Hash, number uint64) (uint64, error) {
	body, err := rawdb.ReadStorageBody(tx, hash, number)
	if err != nil {
		return 0, err
	}
	if body.TxAmount < 2 || body.TxAmount > sharedexec.MaxTransactions+2 {
		return 0, errors.New("canonical progress body count invalid")
	}
	return uint64(body.TxAmount - 2), nil
}

// Earlier metadata prototypes have no counter. Backfill only a small bounded
// history with matching durable evidence; larger migrations require an explicit
// offline adapter rather than silently reporting zero or scanning unbounded data.
func initializeCanonicalProgress(tx kv.RwTx, identity sharedexec.Identity) error {
	raw, err := tx.GetOne(modules.HotStuffState, canonicalProgressKey)
	if err != nil {
		return err
	}
	if len(raw) > 0 {
		_, err := readCanonicalProgress(tx, identity)
		return err
	}
	head, height, err := progressHead(tx)
	if err != nil {
		return err
	}
	if height > 256 {
		return errors.New("canonical progress requires explicit historical migration above 256 blocks")
	}
	progress := CanonicalProgress{Identity: identity, Number: height, BlockHash: head}
	previous := identity.GenesisHash
	for number := uint64(1); number <= height; number++ {
		hash, err := rawdb.ReadCanonicalHash(tx, number)
		if err != nil {
			return err
		}
		header := rawdb.ReadHeader(tx, hash, number)
		evidenceRaw, err := tx.GetOne(modules.HotStuffState, evidenceKey(hash))
		if err != nil {
			return err
		}
		var evidence sharedexec.ImportResult
		if len(evidenceRaw) == 0 || len(evidenceRaw) > 16384 || json.Unmarshal(evidenceRaw, &evidence) != nil || header == nil || header.ParentHash != previous || evidence.Identity != identity || evidence.BlockHash != hash || evidence.Number != number || evidence.ParentHash != header.ParentHash || !evidence.Durable || !evidence.Checked || !evidence.Executed || evidence.State != "durable" || evidence.Result.Root != header.Root || evidence.Result.ReceiptHash != header.ReceiptHash || evidence.Result.GasUsed != header.GasUsed || evidence.Result.Bloom != header.Bloom {
			return errors.New("canonical progress backfill lacks bound execution evidence")
		}
		count, err := storedTransactionCount(tx, hash, number)
		if err != nil {
			return err
		}
		progress.CommittedExecutedTransactions += count
		previous = hash
	}
	return writeCanonicalProgress(tx, progress)
}

func (r *Replica) CanonicalProgress(ctx context.Context) (CanonicalProgress, error) {
	var progress CanonicalProgress
	err := r.db.View(ctx, func(tx kv.Tx) error { var err error; progress, err = readCanonicalProgress(tx, r.identity); return err })
	return progress, err
}

// CanonicalProbeAnchor validates the retained local header as well as progress.
// It is a startup-only check: normal counter reads need no extra header decode.
// Read the local row directly so an ancient-store fallback cannot hide a missing
// metadata anchor. This does not repair metadata or authenticate execution.
func (r *Replica) CanonicalProbeAnchor(ctx context.Context) (CanonicalProgress, error) {
	var progress CanonicalProgress
	err := r.db.View(ctx, func(tx kv.Tx) error {
		candidate, err := readCanonicalProgress(tx, r.identity)
		if err != nil {
			return err
		}
		if candidate.BlockHash == (types.Hash{}) || candidate.Number == ^uint64(0) {
			return errors.New("canonical probe anchor is zero or overflows")
		}
		raw, err := tx.GetOne(modules.Headers, modules.HeaderKey(candidate.Number, candidate.BlockHash))
		if err != nil {
			return err
		}
		if len(raw) == 0 {
			return errors.New("canonical probe local header missing")
		}
		header, err := decodeCanonicalProbeHeader(raw)
		if err != nil {
			return err
		}
		if header.Number == nil || !header.Number.IsUint64() || header.Number.Uint64() != candidate.Number || header.Hash() != candidate.BlockHash {
			return errors.New("canonical probe local header does not match progress")
		}
		progress = candidate
		return nil
	})
	if err != nil {
		return CanonicalProgress{}, err
	}
	return progress, nil
}

func decodeCanonicalProbeHeader(raw []byte) (header *block.Header, err error) {
	// The native protobuf decoder assumes complete nested fixed-width fields.
	// A corrupt retained row must reject startup rather than panic the process.
	defer func() {
		if recover() != nil {
			header = nil
			err = errors.New("canonical probe local header malformed")
		}
	}()
	header = new(block.Header)
	if err = header.Unmarshal(raw); err != nil {
		return nil, err
	}
	return header, nil
}
