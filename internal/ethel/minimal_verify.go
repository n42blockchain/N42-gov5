// Copyright 2026 The N42 Authors
// This file is part of the N42 library.
//
// minimal_verify.go — the minimal client's unified three-layer block verifier.
// It composes the trust layers that previously lived apart: HeaderChain (①,
// internal/ethel/stateless), the witness EVM replay (②, replayWitnessBlock here
// in internal/ethel), and the MPT-stateless state-root check (③, stateless).
// Living in internal/ethel avoids an import cycle (stateless is the lower layer;
// it cannot import the EVM replay, so the composer sits above both).

package ethel

import (
	"context"
	"fmt"
	"os"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/ethel/stateless"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// MinimalVerifyInput is the decoded per-block input a minimal client verifies.
// It is the consumable form of a stateless.StatelessBundle: Header (①), Body +
// Witness + Code (②), Proof (③). Senders are recovered locally by the caller
// (ecrecover over the body) — never trusted from the wire.
type MinimalVerifyInput struct {
	Header  *block.Header
	Body    *GethBodyResult
	Witness []byte
	Senders []types.Address
	Code    map[types.Hash][]byte // verified codeHash -> bytecode (bundle NewCode + fetched)
	Proof   *stateless.BlockProof
}

// VerifyBlockFull runs all three trust layers for one block against a trusted
// HeaderChain. A nil return means the block is zero-trust verified: its
// execution and its state transition both reconcile with a header whose
// authenticity traces (via the parentHash chain) to the trusted anchor.
//
//	① header  — the supplied header must hash to the chain's trusted hash at this
//	            number, so its stateRoot/receiptRoot are the trusted targets.
//	② execute — replay the witness (+ body + code) through the EVM; gasUsed and
//	            receiptRoot must match the header (replayWitnessBlock).
//	③ state   — recompute the post-state root from the MPT multiproof; it must
//	            equal the header's stateRoot (stateless.VerifyAgainstChain).
func VerifyBlockFull(hc *stateless.HeaderChain, chainCfg *params.ChainConfig, engine consensus.Engine, in *MinimalVerifyInput) error {
	if in == nil || in.Header == nil || in.Header.Number == nil {
		return fmt.Errorf("minimal: nil input or header")
	}
	num := in.Header.Number.Uint64()

	// ① header trust.
	trustedHash, ok := hc.TrustedHash(num)
	if !ok {
		return fmt.Errorf("minimal: block %d not in trusted header chain", num)
	}
	if got := in.Header.Hash(); got != trustedHash {
		return fmt.Errorf("minimal: block %d header hash %x != trusted %x", num, got[:8], trustedHash[:8])
	}

	// ② stateless EVM execution → gasUsed + receiptRoot vs header.
	if err := verifyExecution(hc, chainCfg, engine, in); err != nil {
		return fmt.Errorf("minimal: block %d layer②: %w", num, err)
	}

	// ③ MPT stateless state-root check — only on anchor blocks (the cadence
	// model: every block carries a witness for ①②; an MPT proof is attached
	// every K blocks). A nil Proof means a light (non-anchor) bundle: its
	// stateRoot is trusted through the header chain (①), independently
	// re-derived only at the next anchor. The caller enforces the cadence
	// (every K-th block MUST carry a Proof) — see VerifyWindowCadence.
	if in.Proof == nil {
		return nil
	}
	if in.Proof.Number != num {
		return fmt.Errorf("minimal: block %d proof is for block %d", num, in.Proof.Number)
	}
	if err := stateless.VerifyAgainstChain(hc, in.Proof); err != nil {
		return fmt.Errorf("minimal: block %d layer③: %w", num, err)
	}
	return nil
}

// VerifyWindowCadence verifies a contiguous window of per-block inputs under the
// cadence model (the chosen "option B"): every block's ① (header trust) + ②
// (witness execution) is checked, and an MPT proof (③) is REQUIRED on anchor
// blocks — those whose number is a multiple of anchorEvery, plus the window's
// last block. A missing proof on an anchor, or any layer failure, fails that
// block. Light (non-anchor) bundles carry no proof; their stateRoot is trusted
// through the header chain and independently re-derived at the next anchor.
// Results are returned in input order.
func VerifyWindowCadence(hc *stateless.HeaderChain, chainCfg *params.ChainConfig, engine consensus.Engine, inputs []*MinimalVerifyInput, anchorEvery uint64) []stateless.BlockResult {
	results := make([]stateless.BlockResult, len(inputs))
	for i, in := range inputs {
		if in == nil || in.Header == nil || in.Header.Number == nil {
			results[i] = stateless.BlockResult{Err: fmt.Errorf("minimal: nil input/header at index %d", i)}
			continue
		}
		num := in.Header.Number.Uint64()
		isAnchor := anchorEvery > 0 && (num%anchorEvery == 0 || i == len(inputs)-1)
		if isAnchor && in.Proof == nil {
			results[i] = stateless.BlockResult{Number: num, Err: fmt.Errorf("minimal: anchor block %d missing MPT proof", num)}
			continue
		}
		results[i] = stateless.BlockResult{Number: num, Err: VerifyBlockFull(hc, chainCfg, engine, in)}
	}
	return results
}

// WireMinimalExec installs layer ② on a stateless minimal client: per block it
// fetches body/witness/code (and recovers senders) via the given fetchers and
// replays the block, checking gasUsed + receiptRoot against the trusted header.
// Composing this with the client's ① (header chain) + ③ (MPT anchor) yields full
// three-layer verification while keeping MinimalClient in the stateless package
// (the EVM replay can't be imported there without a cycle). Any fetcher may be
// nil (e.g. an empty-block test needs no code).
func WireMinimalExec(
	c *stateless.MinimalClient,
	chainCfg *params.ChainConfig,
	engine consensus.Engine,
	body func(uint64) (*GethBodyResult, error),
	witness func(uint64) ([]byte, error),
	senders func(uint64) ([]types.Address, error),
	code func(uint64) (map[types.Hash][]byte, error),
) {
	c.ExecVerify = func(n uint64, h *block.Header, ancestor func(uint64) types.Hash) error {
		var b *GethBodyResult
		if body != nil {
			var err error
			if b, err = body(n); err != nil {
				return fmt.Errorf("fetch body: %w", err)
			}
		}
		var w []byte
		if witness != nil {
			var err error
			if w, err = witness(n); err != nil {
				return fmt.Errorf("fetch witness: %w", err)
			}
		}
		var sn []types.Address
		if senders != nil {
			sn, _ = senders(n)
		}
		var cd map[types.Hash][]byte
		if code != nil {
			cd, _ = code(n)
		}
		in := &MinimalVerifyInput{Header: h, Body: b, Witness: w, Senders: sn, Code: cd}
		return verifyExecutionAncestor(ancestor, chainCfg, engine, in)
	}
}

// verifyExecution is layer ②: replay the witness through the EVM and let
// replayWitnessBlock check gasUsed + receiptRoot against the header. The trusted
// HeaderChain supplies BLOCKHASH ancestor hashes.
func verifyExecution(hc *stateless.HeaderChain, chainCfg *params.ChainConfig, engine consensus.Engine, in *MinimalVerifyInput) error {
	return verifyExecutionAncestor(func(n uint64) types.Hash { h, _ := hc.TrustedHash(n); return h }, chainCfg, engine, in)
}

// verifyExecutionAncestor is verifyExecution with the BLOCKHASH ancestor lookup
// passed directly (used by the minimal client's ExecVerify hook, which has the
// ancestor func but not the HeaderChain). Code is served from an in-memory Code
// table built from the verified code map.
func verifyExecutionAncestor(ancestor func(uint64) types.Hash, chainCfg *params.ChainConfig, engine consensus.Engine, in *MinimalVerifyInput) error {
	dir, err := os.MkdirTemp("", "minimal-code-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	codeDB := memdb.New(dir)
	defer codeDB.Close()

	if len(in.Code) > 0 {
		if err := codeDB.Update(context.Background(), func(tx kv.RwTx) error {
			for h, c := range in.Code {
				hh := h
				if err := tx.Put(kv.Code, hh[:], c); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	codeTx, err := codeDB.BeginRo(context.Background())
	if err != nil {
		return err
	}
	defer codeTx.Rollback()

	reader := NewWitnessReplayReader(nil, codeTx)
	ibs := state.New(reader)

	body := in.Body
	if body == nil {
		body = &GethBodyResult{}
	}
	job := WitnessJob{
		BlockNum:    in.Header.Number.Uint64(),
		Header:      in.Header,
		Body:        body,
		Witness:     in.Witness,
		Senders:     in.Senders,
		BlockHashFn: ancestor,
	}
	res := replayWitnessBlock(job, codeTx, chainCfg, engine, ReplayMode{NoOutput: true}, ibs, reader)
	return res.Err
}
