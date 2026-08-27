// Copyright 2022-2026 The N42 Authors
// witness_replay_source.go — abstracts how the witness-replay reader
// fetches headers + bodies. Two backends:
//
//   - gethFreezerSource wraps freezer.Freezer reading raw RLP from
//     headers.NNNN.dat / bodies.NNNN.dat (geth ancient store).
//   - n42CompactSource wraps HeaderCompactReader + BodyCompactReader
//     reading N42's columnar 8192-block-segment .cdat files.
//
// Auto-detected at pipeline open by probing for headers.cidx — N42's
// columnar header index. Fallback to geth Freezer if absent.

package ethel

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// headersBodiesSource is the read-side abstraction used by feedBlocks
// and the BLOCKHASH resolver.
type headersBodiesSource interface {
	header(blockNum uint64) (*block.Header, error)
	body(blockNum uint64) (*GethBodyResult, error)
	maxBlock() uint64
	close()
}

// consumingHeadersBodiesSource is implemented by sources that can release a
// body from their read cache after handing ownership to the sequential replay
// pipeline. The ordinary body method remains non-destructive for random-access
// and exported callers.
type consumingHeadersBodiesSource interface {
	headersBodiesSource
	takeBody(blockNum uint64) (*GethBodyResult, error)
}

// parallelConsumingHeadersBodiesSource releases already-handed-off BODYC cache
// slots without arming sequential +1-segment read-ahead. Parallel readers take
// dynamically assigned (and process-interleaved) segments, so +1 is generally
// owned by a different reader/process and would be wasted duplicate work.
type parallelConsumingHeadersBodiesSource interface {
	headersBodiesSource
	takeBodyNoAhead(blockNum uint64) (*GethBodyResult, error)
}

// HeadersBodiesSource is the exported handle for cross-package callers
// (cmd/ethexec sender-recovery, future tools). Same interface as the
// internal one but exported method names. Constructed via
// OpenHeadersBodiesSource.
type HeadersBodiesSource interface {
	Header(blockNum uint64) (*block.Header, error)
	Body(blockNum uint64) (*GethBodyResult, error)
	MaxBlock() uint64
	Close()
}

// exportedSource adapts the unexported headersBodiesSource to the
// exported interface. Keeps internal call sites untouched while giving
// external callers a stable API.
type exportedSource struct{ s headersBodiesSource }

func (e *exportedSource) Header(n uint64) (*block.Header, error) { return e.s.header(n) }
func (e *exportedSource) Body(n uint64) (*GethBodyResult, error) { return e.s.body(n) }
func (e *exportedSource) MaxBlock() uint64                       { return e.s.maxBlock() }
func (e *exportedSource) Close()                                 { e.s.close() }

// internalSource adapts the exported HeadersBodiesSource back to the
// unexported interface so SenderStage (which uses the unexported one
// internally for shared close lifecycle) can accept either.
type internalSource struct{ s HeadersBodiesSource }

func (i *internalSource) header(n uint64) (*block.Header, error) { return i.s.Header(n) }
func (i *internalSource) body(n uint64) (*GethBodyResult, error) { return i.s.Body(n) }
func (i *internalSource) maxBlock() uint64                       { return i.s.MaxBlock() }
func (i *internalSource) close()                                 { i.s.Close() }

// OpenHeadersBodiesSource picks the right backend by probing dir:
// N42 columnar (headerc.cidx present) or geth ancient (default).
// The returned source's Close() releases all underlying handles.
func OpenHeadersBodiesSource(dir string) (HeadersBodiesSource, error) {
	src, err := openHeadersBodiesSource(dir)
	if err != nil {
		return nil, err
	}
	return &exportedSource{s: src}, nil
}

// openHeadersBodiesSource picks the reader implementation by probing
// the input directory.
//
//   - N42 columnar (n42CompactSource, header_compact.go +
//     body_compact.go): stored as headerc.cidx + headerc.NNNN.cdat /
//     bodyc.cidx + bodyc.NNNN.cdat. Each block-field is its own column,
//     8192-block zstd segments. The trailer of each segment carries
//     the canonical Hash() per block so readers don't reconstruct
//     ParentHash + Bloom from receipts.
//
//   - geth ancient / standard freezer (gethFreezerSource): full RLP
//     per block, 64-block zstd batches at headers.NNNN.cdat /
//     bodies.NNNN.cdat. DecodeGethHeader returns canonical Header
//     directly.
//
// The earlier shared filename layout (both formats using
// headers.NNNN.cdat) was bug-prone — renaming the columnar archive
// to headerc/bodyc makes the two formats unambiguous.
func openHeadersBodiesSource(dir string) (headersBodiesSource, error) {
	if _, err := os.Stat(filepath.Join(dir, "headerc.cidx")); err == nil {
		return openN42CompactSource(dir)
	}
	return openGethFreezerSource(dir)
}

// gethFreezerSource adapts freezer.Freezer to headersBodiesSource.
type gethFreezerSource struct {
	f *freezer.Freezer
}

func openGethFreezerSource(dir string) (*gethFreezerSource, error) {
	f, err := freezer.NewReadOnly(dir)
	if err != nil {
		return nil, fmt.Errorf("open geth freezer %s: %w", dir, err)
	}
	return &gethFreezerSource{f: f}, nil
}

func (s *gethFreezerSource) header(n uint64) (*block.Header, error) {
	data, err := s.f.Ancient(freezer.TableHeaders, n)
	if err != nil {
		return nil, err
	}
	return DecodeGethHeader(data)
}

func (s *gethFreezerSource) body(n uint64) (*GethBodyResult, error) {
	data, err := s.f.Ancient(freezer.TableBodies, n)
	if err != nil {
		return nil, err
	}
	return DecodeGethBody(data)
}

func (s *gethFreezerSource) maxBlock() uint64 { return s.f.Frozen() }

func (s *gethFreezerSource) close() { s.f.Close() }

// freezer returns the underlying freezer for callers that still need
// direct Ancient access (e.g. witness/senders tables that share the dir).
func (s *gethFreezerSource) freezer() *freezer.Freezer { return s.f }

// n42CompactSource adapts the N42 columnar readers. Each per-block access
// decodes a segment if not cached; sequential reads stay hot. The segment
// trailer carries the canonical Hash() per block, so reading is O(1) — no
// parent-chain walk, no bloom recompute, no external receipts dependency.
// ParentHash is restored from the previous trailer because EIP-2935 consumes
// it during execution; Bloom remains omitted.
type n42CompactSource struct {
	hr            *HeaderCompactReader
	br            *BodyCompactReader
	lastHeaderNum uint64
	lastHeader    *block.Header
}

func openN42CompactSource(dir string) (*n42CompactSource, error) {
	hr, err := OpenHeaderCompact(dir)
	if err != nil {
		return nil, fmt.Errorf("open header compact %s: %w", dir, err)
	}
	br, err := OpenBodyCompact(dir)
	if err != nil {
		hr.Close()
		return nil, fmt.Errorf("open body compact %s: %w", dir, err)
	}
	// EIP-4444: a Full node keeps only a recent window of bodies hot; if a
	// cold-read resolver is installed, trimmed (cold) segments are fetched on
	// demand instead of failing with ErrBodyTrimmed.
	if defaultColdResolver != nil {
		br.SetColdResolver(defaultColdResolver)
	}
	return &n42CompactSource{hr: hr, br: br}, nil
}

func (s *n42CompactSource) header(n uint64) (*block.Header, error) {
	// Reader populates Header.hash atomic.Value from the segment trailer
	// (hfStoredHash flag), so hdr.Hash() returns canonical directly. The
	// columnar format does not store ParentHash, so it comes back zero — but
	// execution needs it, not just Hash(): EIP-2935 writes header.ParentHash
	// into the history ring buffer every block (internal/blockhelp.go, via
	// vm.StoreParentBlockHash). Replaying with a zero there poisons the buffer,
	// so a later BLOCKHASH read returns 0, the contract takes a different path
	// and the block's gas no longer matches its header. That is exactly how
	// block 24000022 replayed at 16,980,501 gas against a header saying
	// 17,009,241 — while the geth-ancient source, which carries ParentHash,
	// replayed the same block clean.
	//
	// The parent's canonical hash is one O(1) trailer read away, so fill it in
	// rather than leaving a field that execution silently misreads.
	hdr, err := s.hr.ReadHeader(n)
	if err != nil {
		return nil, err
	}
	if n > 0 && hdr.ParentHash == (types.Hash{}) {
		// feedBlocks walks block numbers in order on ONE goroutine, so the
		// parent is almost always the header returned by the previous call.
		// Reuse it rather than asking the reader again: within a segment that
		// second lookup is cheap, but at a segment boundary block n-1 lives in
		// the PREVIOUS segment, so asking for it evicts the segment just loaded
		// and the next block reloads it. That trades one cached hit for two
		// full headerc segment decodes at every one of the store's boundaries.
		var parentHash types.Hash
		if s.lastHeader != nil && s.lastHeaderNum == n-1 {
			parentHash = s.lastHeader.Hash()
		} else {
			parent, perr := s.hr.ReadHeader(n - 1)
			if perr != nil {
				return nil, fmt.Errorf("parent header %d (needed for ParentHash of %d): %w", n-1, n, perr)
			}
			parentHash = parent.Hash()
		}
		hdr.ParentHash = parentHash
	}
	s.lastHeaderNum, s.lastHeader = n, hdr
	return hdr, nil
}

func (s *n42CompactSource) body(n uint64) (*GethBodyResult, error) {
	return s.readBody(n, false)
}

func (s *n42CompactSource) takeBody(n uint64) (*GethBodyResult, error) {
	return s.readBody(n, true)
}

func (s *n42CompactSource) takeBodyNoAhead(n uint64) (*GethBodyResult, error) {
	db, err := s.br.TakeBodyNoAhead(n)
	if err != nil {
		return nil, err
	}
	return s.bodyResult(n, db)
}

func (s *n42CompactSource) readBody(n uint64, consume bool) (*GethBodyResult, error) {
	var (
		db  *DecodedBlock
		err error
	)
	if consume {
		db, err = s.br.TakeBody(n)
	} else {
		db, err = s.br.ReadBody(n)
	}
	if err != nil {
		return nil, err
	}
	return s.bodyResult(n, db)
}

func (s *n42CompactSource) bodyResult(n uint64, db *DecodedBlock) (*GethBodyResult, error) {
	// bodyc historically collapsed every authorization V other than 1 to 0.
	// This is lossy for invalid legacy 27/28 tuples found in canonical Ethereum
	// history: N42 correctly rejects V > 1, while the collapsed V=0 tuple can
	// become valid and mutate state. Current segments declare bfAuthVFull and
	// carry the true value; only older ones need the canonical transaction root
	// from headerc to restore it.
	//
	// The gate is the segment's own format flag, not a property of the decoded
	// transactions. Keying it on "does any authorization have V == 0" looks
	// cheaper and is not: y_parity 0 is a perfectly ordinary value, so on a
	// lossless archive that test fires for a large share of the 2.2M post-Prague
	// SetCode blocks and pays a full transaction-root derivation for each, on
	// the single goroutine feeding every worker. The flag is O(1) and exact, so
	// a regenerated archive pays nothing and an old one keeps its safety net.
	if s.br.SegmentNeedsAuthVRepair() {
		hdr := s.lastHeader
		if hdr == nil || s.lastHeaderNum != n {
			loaded, err := s.hr.ReadHeader(n)
			if err != nil {
				return nil, fmt.Errorf("header %d needed to verify bodyc authorization values: %w", n, err)
			}
			hdr = loaded
		}
		if repaired, rerr := repairCompactLegacyAuthorizationV(hdr.TxHash, db.Txs); rerr != nil {
			return nil, fmt.Errorf("block %d: %w", n, rerr)
		} else if repaired {
			fmt.Fprintf(os.Stderr, "bodyc: restored legacy EIP-7702 authorization V using canonical tx root at block %d\n", n)
		}
	}
	var uncles []*block.Header
	if len(db.UncleRLP) > 0 {
		uncles = make([]*block.Header, len(db.UncleRLP))
		for i, raw := range db.UncleRLP {
			h, err := DecodeUncleHeader(raw)
			if err != nil {
				return nil, fmt.Errorf("uncle %d of block %d: %w", i, n, err)
			}
			uncles[i] = h
		}
	}
	return &GethBodyResult{
		Transactions: db.Txs,
		Uncles:       uncles,
		Withdrawals:  db.Withdrawals,
	}, nil
}

type compactAuthLocation struct {
	tx, auth int
}

// repairCompactLegacyAuthorizationV repairs the one-byte V values lost by the
// original bodyc encoder. It first tries one changed authorization, then two;
// canonical blocks observed so far need one. The bounded search prevents a
// malformed block with a huge authorization list from causing exponential work.
func repairCompactLegacyAuthorizationV(want types.Hash, txs []*transaction.Transaction) (bool, error) {
	var locs []compactAuthLocation
	for ti, tx := range txs {
		if tx.Type() != transaction.SetCodeTxType {
			continue
		}
		for ai, auth := range tx.AuthList() {
			if auth.V == nil || auth.V.IsZero() {
				locs = append(locs, compactAuthLocation{tx: ti, auth: ai})
			}
		}
	}
	if len(locs) == 0 {
		return false, nil
	}
	got := hash.DeriveShaErigon(transaction.EthTransactions(txs))
	if got == want {
		return false, nil
	}

	// Try the overwhelmingly common case first: one legacy V was collapsed.
	for _, loc := range locs {
		for _, v := range []uint64{27, 28} {
			candidate := cloneSetCodeTxWithAuthV(txs[loc.tx], map[int]uint64{loc.auth: v})
			orig := txs[loc.tx]
			txs[loc.tx] = candidate
			match := hash.DeriveShaErigon(transaction.EthTransactions(txs)) == want
			txs[loc.tx] = orig
			if match {
				txs[loc.tx] = candidate
				return true, nil
			}
		}
	}

	// Also cover two lossy tuples, including two authorizations in one tx.
	// This remains O(n^2) and runs only after the current root mismatches.
	for i := 0; i < len(locs); i++ {
		for j := i + 1; j < len(locs); j++ {
			for _, vi := range []uint64{27, 28} {
				for _, vj := range []uint64{27, 28} {
					li, lj := locs[i], locs[j]
					origI := txs[li.tx]
					if li.tx == lj.tx {
						txs[li.tx] = cloneSetCodeTxWithAuthV(origI, map[int]uint64{li.auth: vi, lj.auth: vj})
						if hash.DeriveShaErigon(transaction.EthTransactions(txs)) == want {
							return true, nil
						}
						txs[li.tx] = origI
						continue
					}
					origJ := txs[lj.tx]
					txs[li.tx] = cloneSetCodeTxWithAuthV(origI, map[int]uint64{li.auth: vi})
					txs[lj.tx] = cloneSetCodeTxWithAuthV(origJ, map[int]uint64{lj.auth: vj})
					if hash.DeriveShaErigon(transaction.EthTransactions(txs)) == want {
						return true, nil
					}
					txs[li.tx], txs[lj.tx] = origI, origJ
				}
			}
		}
	}
	return false, fmt.Errorf("bodyc transaction root mismatch after legacy authorization recovery: got %s want %s (zero-V candidates=%d)", got.Hex(), want.Hex(), len(locs))
}

func cloneSetCodeTxWithAuthV(tx *transaction.Transaction, overrides map[int]uint64) *transaction.Transaction {
	auths := tx.AuthList().Copy()
	for i, v := range overrides {
		auths[i].V = uint256.NewInt(v)
	}
	v, r, s := tx.RawSignatureValues()
	return transaction.NewTx(&transaction.SetCodeTx{
		ChainID:    tx.ChainId(),
		Nonce:      tx.Nonce(),
		GasTipCap:  tx.GasTipCap(),
		GasFeeCap:  tx.GasFeeCap(),
		Gas:        tx.Gas(),
		To:         tx.To(),
		Value:      tx.Value(),
		Data:       tx.Data(),
		AccessList: tx.AccessList(),
		AuthList:   auths,
		V:          v,
		R:          r,
		S:          s,
	})
}

func (s *n42CompactSource) maxBlock() uint64 {
	m := s.hr.MaxBlock()
	if b := s.br.MaxBlock(); b < m {
		m = b
	}
	return m
}

func (s *n42CompactSource) close() {
	s.hr.Close()
	s.br.Close()
}

// BlockHashWindowSize is the EVM BLOCKHASH look-back window per
// yellow paper H.2 (1..256 ancestors only).
const BlockHashWindowSize = 256
const blockHashWindowSize = BlockHashWindowSize // internal alias retained for existing callers

// MakeBlockHashFn builds a BLOCKHASH resolver from a snapshot of the
// most recent canonical hashes. recent[i] = hash of block (currentBlock
// - len(recent) + i). The closure does pure index lookup — safe to
// call from any goroutine, no source access. Exported so single-block
// tools (e.g. witness-block-trace) can build the same resolver shape
// the parallel pipeline uses.
func MakeBlockHashFn(currentBlock uint64, recent []types.Hash) func(uint64) types.Hash {
	return makeBlockHashFn(currentBlock, recent)
}

// makeBlockHashFn — see MakeBlockHashFn doc.
func makeBlockHashFn(currentBlock uint64, recent []types.Hash) func(uint64) types.Hash {
	snap := make([]types.Hash, len(recent))
	copy(snap, recent)
	base := currentBlock - uint64(len(snap))
	return func(n uint64) types.Hash {
		if n < base || n >= currentBlock || currentBlock-n > blockHashWindowSize {
			return types.Hash{}
		}
		return snap[n-base]
	}
}
