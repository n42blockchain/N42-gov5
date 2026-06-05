// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// witness_replay_reader.go — production witness-backed StateReader.
// Promoted from witness_verify_test.go's witnessReplayReader; identical
// wire format (length-prefixed bytes, code-from-MDBX). The witness
// stream encodes every state read in tx execution order, so the
// replayer just dequeues values in order — addresses and slot keys
// are reconstructed implicitly by re-running the same EVM path.

package ethel

import (
	"fmt"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
)

// WitnessReplayReader implements modules/state.StateReader against a
// witness byte stream + a code source. The code source is either an
// MDBX RoTx (codeHash → bytecode lookup) or a CodesFreezerReader
// (address → bytecode lookup, address-indexed cidx). Not goroutine-
// safe — each worker holds its own.
//
// codes (freezer) is preferred when set: it's address-indexed so it
// works even when account data is incomplete, and it's self-contained
// so witness-replay doesn't need a populated MDBX Code table.
type WitnessReplayReader struct {
	stream []byte
	pos    int
	codeTx kv.Tx               // MDBX Code table (codeHash → code), optional
	codes  *CodesFreezerReader // freezer codes.cidx (addr → code), optional
	// codeFetch is an optional on-demand bytecode source by codeHash (e.g. a minimal
	// client fetching from a producer's /code endpoint). Tried after the cache/
	// codes/codeTx misses and before failing loud. Returns nil to fall through.
	// keccak256(code)==codeHash is verified here, so it is content-addressed and
	// historically correct (unlike address-keyed code, which is only tip-accurate).
	codeFetch func(types.Hash) ([]byte, error)
	// freezerCoverage is the highest block the codes-freezer is guaranteed to
	// cover (from codes.Coverage()); 0 = unknown. It only sharpens the error
	// message when every tier misses — it does NOT gate reads, because code is
	// content-addressed: a freezer miss falls through to the hot MDBX tier
	// regardless of height (a contract deployed/redeployed past the freezer's
	// coverage lives only in MDBX). See SetFreezerCoverage.
	freezerCoverage uint64
	// scratch is reused across ReadAccountData calls. All call sites
	// in IntraBlockState do data.Copy(scratch) before the next read,
	// so the returned pointer never aliases stale state. Saves ~5-10K
	// StateAccount allocs per block.
	scratch account.StateAccount
}

func NewWitnessReplayReader(stream []byte, codeTx kv.Tx) *WitnessReplayReader {
	return &WitnessReplayReader{stream: stream, codeTx: codeTx}
}

// SetCodesFreezer attaches an address-indexed code source. The reader
// then looks up bytecode via codes-freezer first, falling back to the
// MDBX codeTx only if codes-freezer is absent.
func (r *WitnessReplayReader) SetCodesFreezer(codes *CodesFreezerReader) {
	r.codes = codes
}

// SetCodeFetcher attaches an on-demand bytecode fetcher (codeHash → code), used
// by a minimal client that pulls missing contract code from a producer /code
// endpoint instead of holding a full code DB.
func (r *WitnessReplayReader) SetCodeFetcher(fn func(types.Hash) ([]byte, error)) {
	r.codeFetch = fn
}

// SetFreezerCoverage records the codes-freezer's coverage height (codes.Coverage())
// so a final all-tiers-miss error can say whether the requested code is plausibly
// past the cold freezer's boundary (→ expected in the hot MDBX tier) or within it
// (→ the freezer is genuinely incomplete). Reads are unaffected; this only refines
// diagnostics. Pass 0 to leave it unknown.
func (r *WitnessReplayReader) SetFreezerCoverage(block uint64) { r.freezerCoverage = block }

// Reset rebinds the reader to a new witness stream and clears the read
// cursor. codeTx is preserved across calls — workers reuse one RoTx for
// the lifetime of the goroutine.
func (r *WitnessReplayReader) Reset(stream []byte) {
	r.stream = stream
	r.pos = 0
}

func (r *WitnessReplayReader) ReadAccountData(address types.Address) (*account.StateAccount, error) {
	if r.pos >= len(r.stream) {
		return nil, nil
	}
	length := int(r.stream[r.pos])
	r.pos++
	if length == 0 {
		return nil, nil
	}
	r.scratch = account.StateAccount{}
	if err := r.scratch.DecodeForStorage(r.stream[r.pos : r.pos+length]); err != nil {
		return nil, err
	}
	r.pos += length
	return &r.scratch, nil
}

func (r *WitnessReplayReader) ReadAccountStorage(address types.Address, key *types.Hash) ([]byte, error) {
	if r.pos >= len(r.stream) {
		return nil, nil
	}
	length := int(r.stream[r.pos])
	r.pos++
	if length == 0 {
		return nil, nil
	}
	// Slice directly into the witness stream — zero alloc on the hot
	// SLOAD path (was the top allocator on long replays). Safety:
	// witness-replay calls state.New(reader) per block, so IBS.snap
	// is nil and stateObject.GetCommittedState's only retaining caller
	// (snap.AddStorage in entire.go:240) is unreachable. The other
	// consumer is uint256.SetBytes which copies on ingest. If snap is
	// ever wired up here, this MUST go back to a copy.
	val := r.stream[r.pos : r.pos+length : r.pos+length]
	r.pos += length
	return val, nil
}

func (r *WitnessReplayReader) ReadAccountCode(address types.Address, codeHash types.Hash) ([]byte, error) {
	// Cache hit (process-wide) — codeHash key. Same key for both code
	// sources because content-addressing is universal.
	if GlobalBytecodeCache != nil {
		if code, ok := GlobalBytecodeCache.Get(codeHash); ok {
			return code, nil
		}
	}
	// Codes-freezer (address-indexed) is tried first when configured.
	// Both sources verify keccak256(stored)==codeHash on cache miss
	// — a corrupt or mis-keyed entry surfaces as a hard error instead
	// of being silently treated as an EOA, which would otherwise drift
	// gas by the missing fallback cost. The verify cost is bounded by
	// the cache eviction rate (touched contracts × keccak256 ≈ a few
	// seconds total over a 24M-block replay).
	if r.codes != nil {
		code, err := r.codes.LookupByAddress(address)
		if err != nil {
			return nil, fmt.Errorf("codes-freezer: %w", err)
		}
		if len(code) > 0 {
			if h := crypto.Keccak256Hash(code); h == codeHash {
				if GlobalBytecodeCache != nil {
					GlobalBytecodeCache.Put(codeHash, code)
				}
				return code, nil
			}
			// keccak mismatch = the freezer holds OLD bytecode for this address:
			// the contract was redeployed (SELFDESTRUCT+CREATE) or first deployed
			// past the freezer's coverage, so the current code lives only in the
			// hot tier (codeFetch / MDBX). Fall through rather than fail loud —
			// the cold freezer is a snapshot at its coverage height, and the
			// full-node readers (HashedStateReader/PlainStateReader) tier exactly
			// this way. Downstream keccak checks still guarantee correctness.
		}
	}
	// On-demand fetcher (e.g. minimal client → producer /code). Tried before the
	// fail-loud paths; verifies keccak256(code)==codeHash so a lying server can't
	// substitute bytecode.
	if r.codeFetch != nil && codeHash != witnessReplayEmptyCodeHash {
		code, err := r.codeFetch(codeHash)
		if err != nil {
			return nil, fmt.Errorf("witness-replay: code fetch for %x: %w", codeHash[:8], err)
		}
		if len(code) > 0 {
			if h := crypto.Keccak256Hash(code); h != codeHash {
				return nil, fmt.Errorf("witness-replay: fetched code for %x hashes to %x (server returned wrong bytecode)", codeHash[:8], h[:8])
			}
			if GlobalBytecodeCache != nil {
				GlobalBytecodeCache.Put(codeHash, code)
			}
			return code, nil
		}
	}
	// Fallback: MDBX Code table (codeHash → bytecode). When codeTx is
	// absent the only source was codes-freezer; if it didn't have this
	// address the caller wants a contract that we can't deliver, and
	// silently returning nil makes the EVM treat it as an EOA — that
	// shifts SLOAD reads off the witness stream and surfaces miles
	// downstream as garbage account data ("nonce too high", impossible
	// nonce values). Fail loud here, same as the post-MDBX safety net
	// below.
	if r.codeTx == nil {
		if codeHash != witnessReplayEmptyCodeHash {
			return nil, fmt.Errorf("witness-replay: bytecode for addr=%x codeHash=%x not in codes-freezer and no MDBX fallback configured — codes-freezer is incomplete; rerun code-import2fz against a full state DB or pass --datadir for MDBX Code-table fallback%s",
				address[:], codeHash[:], r.coverageHint())
		}
		return nil, nil
	}
	code, err := r.codeTx.GetOne(kv.Code, codeHash[:])
	if err != nil {
		return nil, err
	}
	if len(code) > 0 {
		// Verify even on the codeHash-keyed MDBX path. The key IS
		// supposed to be keccak256(value), but real-world Code tables
		// can be corrupt (a writer bug or aborted commit can leave
		// (key=hash_X, value=code_for_hash_Y) entries — observed on
		// block 14530048 tx 43 → 40-gas drift). Verifying here turns
		// silent EVM-treats-contract-as-EOA into a loud failure.
		if h := crypto.Keccak256Hash(code); h != codeHash {
			return nil, fmt.Errorf("mdbx code: corrupt entry for codeHash=%x — stored value hashes to %x; codeDB at --datadir is corrupt and silently coercing the EVM into EOA-mode for this contract (40-gas-style drift)",
				codeHash[:], h[:])
		}
	}
	if GlobalBytecodeCache != nil && len(code) > 0 {
		// MDBX returns a slice into mmap memory; copy before
		// caching so the slice survives RoTx rotation.
		cached := make([]byte, len(code))
		copy(cached, code)
		GlobalBytecodeCache.Put(codeHash, cached)
		return cached, nil
	}
	// Final safety net: caller passed a non-empty codeHash but neither
	// source has the bytecode. The IBS Code() method already short-
	// circuits emptyCodeHash before calling here (state_object.go:401),
	// so reaching this point with code==nil means we'd silently treat a
	// real contract as an EOA — exactly the failure mode that produced
	// the 40-gas drift on block 14530048 tx 43. Fail loud instead.
	if len(code) == 0 && codeHash != witnessReplayEmptyCodeHash {
		return nil, fmt.Errorf("witness-replay: bytecode for codeHash=%x not found in codes-freezer or mdbx — both code sources are incomplete; populate codes-freezer (run code-import2fz against a full state DB) or fix the codeDB%s",
			codeHash[:], r.coverageHint())
	}
	return code, nil
}

// coverageHint annotates an all-tiers-miss error with the cold freezer's coverage
// height when known, so the operator can tell a coverage gap (code deployed past
// the snapshot, belongs in the hot MDBX tier) from genuine store corruption.
func (r *WitnessReplayReader) coverageHint() string {
	if r.freezerCoverage == 0 {
		return ""
	}
	return fmt.Sprintf(" [codes-freezer coverage=%d; a contract deployed/redeployed after this height lives only in the hot MDBX Code tier]", r.freezerCoverage)
}

// witnessReplayEmptyCodeHash mirrors common/account.emptyCodeHash so we
// don't have to import a private symbol just for one comparison. It is
// keccak256(nil), the codeHash of any code-less account.
var witnessReplayEmptyCodeHash = crypto.Keccak256Hash(nil)

func (r *WitnessReplayReader) ReadAccountCodeSize(address types.Address, codeHash types.Hash) (int, error) {
	code, err := r.ReadAccountCode(address, codeHash)
	return len(code), err
}
