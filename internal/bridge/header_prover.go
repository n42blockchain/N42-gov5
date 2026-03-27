// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Package bridge provides the ZK-native cross-chain bridge for N42.
// The bridge uses mathematical proofs (SP1 ZK proofs + JMT Merkle proofs
// + HotStuff-2 BLS aggregate signatures) to verify N42 state on Ethereum
// without any trusted third party.
package bridge

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/internal/zkprover"
	"github.com/n42blockchain/N42/log"
)

// HeaderChainProof proves that a sequence of N42 block headers is valid,
// meaning each header was endorsed by a HotStuff-2 quorum (2f+1 BLS
// aggregate signature) and the parent hash chain is intact.
//
// Trust chain: HotStuff-2 QC → SP1 ZK proof → ETH on-chain verification
type HeaderChainProof struct {
	StartBlock   uint64     // First block in the proven range
	EndBlock     uint64     // Last block in the proven range
	StateRoot    types.Hash // JMT state root at EndBlock
	ProofData    []byte     // SP1 Groth16/PLONK succinct proof
	PublicInputs []byte     // startBlock(8) + endBlock(8) + stateRoot(32) = 48 bytes
}

// HeaderChainInput is the witness data for the SP1 header chain circuit.
type HeaderChainInput struct {
	Headers          []SerializedHeader           // Encoded block headers
	QCs              []hotstuff.QuorumCertificate // One QC per header
	ValidatorPubKeys [][]byte                     // BLS public keys of the validator set
	ValidatorCount   uint32                       // Number of validators
}

// SerializedHeader is a compact block header for the ZK circuit.
type SerializedHeader struct {
	Number     uint64
	Hash       types.Hash
	ParentHash types.Hash
	StateRoot  types.Hash
	Time       uint64
}

// HeaderChainProver generates ZK proofs that N42 block header sequences
// are valid, using the existing SP1 prover infrastructure.
type HeaderChainProver struct {
	prover *zkprover.SP1ProverClient
}

// NewHeaderChainProver creates a prover using the existing SP1 client.
func NewHeaderChainProver(prover *zkprover.SP1ProverClient) *HeaderChainProver {
	return &HeaderChainProver{prover: prover}
}

// BuildInput constructs the ZK circuit input from block headers and QCs.
func BuildHeaderChainInput(
	headers []*block.Header,
	qcs []hotstuff.QuorumCertificate,
	vs *hotstuff.ValidatorSet,
) (*HeaderChainInput, error) {
	if vs == nil {
		return nil, fmt.Errorf("validator set is nil, cannot build SP1 input")
	}
	if len(headers) == 0 {
		return nil, fmt.Errorf("no headers provided")
	}
	if len(headers) != len(qcs) {
		return nil, fmt.Errorf("header/QC count mismatch: %d headers, %d QCs", len(headers), len(qcs))
	}

	serialized := make([]SerializedHeader, len(headers))
	for i, h := range headers {
		serialized[i] = SerializedHeader{
			Number:     h.Number.Uint64(),
			Hash:       h.Hash(),
			ParentHash: h.ParentHash,
			StateRoot:  h.Root,
			Time:       h.Time,
		}
	}

	// Extract validator public keys
	nValidators := int(vs.Len())
	pubKeys := make([][]byte, nValidators)
	for i := 0; i < nValidators; i++ {
		pk, err := vs.GetPublicKey(hotstuff.ValidatorIndex(i))
		if err != nil {
			return nil, fmt.Errorf("get validator %d pubkey: %w", i, err)
		}
		pubKeys[i] = pk.Marshal()
	}

	return &HeaderChainInput{
		Headers:          serialized,
		QCs:              qcs,
		ValidatorPubKeys: pubKeys,
		ValidatorCount:   uint32(vs.Len()),
	}, nil
}

// EncodePublicInputs encodes the public inputs for the Verifier contract.
// Format: startBlock(8 LE) + endBlock(8 LE) + stateRoot(32) = 48 bytes
func EncodePublicInputs(startBlock, endBlock uint64, stateRoot types.Hash) []byte {
	buf := make([]byte, 48)
	binary.LittleEndian.PutUint64(buf[0:8], startBlock)
	binary.LittleEndian.PutUint64(buf[8:16], endBlock)
	copy(buf[16:48], stateRoot[:])
	return buf
}

// DecodePublicInputs decodes the 48-byte public inputs.
func DecodePublicInputs(data []byte) (startBlock, endBlock uint64, stateRoot types.Hash, err error) {
	if len(data) < 48 {
		return 0, 0, types.Hash{}, fmt.Errorf("public inputs too short: %d < 48", len(data))
	}
	startBlock = binary.LittleEndian.Uint64(data[0:8])
	endBlock = binary.LittleEndian.Uint64(data[8:16])
	copy(stateRoot[:], data[16:48])
	return
}

// fetchHeadersAndQCs fetches block headers and QCs for a range from the chain.
func fetchHeadersAndQCs(
	chain common.IBlockChain,
	startBlock, endBlock uint64,
) ([]*block.Header, []hotstuff.QuorumCertificate, error) {
	if startBlock > endBlock {
		return nil, nil, fmt.Errorf("invalid range: startBlock %d > endBlock %d", startBlock, endBlock)
	}
	count := endBlock - startBlock + 1
	headers := make([]*block.Header, 0, count)
	qcs := make([]hotstuff.QuorumCertificate, 0, count)

	num256 := new(uint256.Int)
	for num := startBlock; num <= endBlock; num++ {
		num256.SetUint64(num)
		blk, err := chain.GetBlockByNumber(num256)
		if err != nil || blk == nil {
			return nil, nil, fmt.Errorf("block %d not found", num)
		}
		hdr, ok := blk.Header().(*block.Header)
		if !ok {
			return nil, nil, fmt.Errorf("block %d: invalid header type", num)
		}
		headers = append(headers, hdr)
		qcs = append(qcs, extractQCFromHeader(hdr))
	}
	return headers, qcs, nil
}

// ProveHeaderRange generates a header chain proof for a block range (local verification only).
// For ZK-backed proofs, use ProveHeaderRangeWithSP1.
func ProveHeaderRange(
	chain common.IBlockChain,
	vs *hotstuff.ValidatorSet,
	startBlock, endBlock uint64,
) (*HeaderChainProof, error) {
	headers, qcs, err := fetchHeadersAndQCs(chain, startBlock, endBlock)
	if err != nil {
		return nil, err
	}

	if err := VerifyHeaderChainLocally(headers, qcs, vs); err != nil {
		return nil, fmt.Errorf("local verification failed: %w", err)
	}

	lastHeader := headers[len(headers)-1]
	stateRoot := lastHeader.Root
	publicInputs := EncodePublicInputs(startBlock, endBlock, types.Hash(stateRoot))

	return &HeaderChainProof{
		StartBlock:   startBlock,
		EndBlock:     endBlock,
		StateRoot:    types.Hash(stateRoot),
		PublicInputs: publicInputs,
		ProofData:    nil,
	}, nil
}

// ProveHeaderRangeWithSP1 generates a header chain proof with real SP1 ZK proving.
// Falls back to local-only proof (nil ProofData) if prover is nil or unavailable.
func ProveHeaderRangeWithSP1(
	ctx context.Context,
	chain common.IBlockChain,
	vs *hotstuff.ValidatorSet,
	prover *HeaderChainProver,
	startBlock, endBlock uint64,
) (*HeaderChainProof, error) {
	// Fetch headers once (shared between local verify and SP1 input)
	headers, qcs, err := fetchHeadersAndQCs(chain, startBlock, endBlock)
	if err != nil {
		return nil, err
	}

	if err := VerifyHeaderChainLocally(headers, qcs, vs); err != nil {
		return nil, fmt.Errorf("local verification failed: %w", err)
	}

	lastHeader := headers[len(headers)-1]
	stateRoot := lastHeader.Root
	publicInputs := EncodePublicInputs(startBlock, endBlock, types.Hash(stateRoot))

	proof := &HeaderChainProof{
		StartBlock:   startBlock,
		EndBlock:     endBlock,
		StateRoot:    types.Hash(stateRoot),
		PublicInputs: publicInputs,
	}

	if prover == nil || prover.prover == nil {
		return proof, nil
	}

	input, err := BuildHeaderChainInput(headers, qcs, vs)
	if err != nil {
		log.Warn("Failed to build SP1 input, returning local proof", "err", err)
		return proof, nil
	}

	guestInput, err := json.Marshal(input)
	if err != nil {
		log.Warn("Failed to serialize SP1 input", "err", err)
		return proof, nil
	}

	lastHash := headers[len(headers)-1].Hash()
	jobID, err := prover.prover.Submit(ctx, lastHash, endBlock, guestInput)
	if err != nil {
		log.Warn("SP1 proof submission failed, using local proof", "err", err)
		return proof, nil
	}

	log.Info("SP1 proof submitted", "jobID", jobID, "startBlock", startBlock, "endBlock", endBlock)

	pollCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-pollCtx.Done():
			log.Warn("SP1 proof timed out, using local proof", "jobID", jobID)
			return proof, nil
		case <-ticker.C:
			sp1Proof, err := prover.prover.Status(pollCtx, jobID)
			if err != nil {
				log.Warn("SP1 status check failed", "err", err, "jobID", jobID)
				continue
			}
			if sp1Proof != nil {
				proof.ProofData = sp1Proof.ProofData
				log.Info("SP1 proof completed", "jobID", jobID, "proofSize", len(sp1Proof.ProofData))
				return proof, nil
			}
		}
	}
}

// VerifyHeaderChainLocally verifies a header chain without ZK, using direct
// BLS signature verification. Used for testing and local validation.
func VerifyHeaderChainLocally(
	headers []*block.Header,
	qcs []hotstuff.QuorumCertificate,
	vs *hotstuff.ValidatorSet,
) error {
	if len(headers) != len(qcs) {
		return fmt.Errorf("header/QC count mismatch")
	}

	for i := range headers {
		// Verify parent hash chain
		if i > 0 && headers[i].ParentHash != headers[i-1].Hash() {
			return fmt.Errorf("broken parent chain at block %d", headers[i].Number.Uint64())
		}

		if qcs[i].View > 0 {
			headerView, err := hotstuff.ExtractViewFromExtra(headers[i].Extra)
			if err != nil {
				return fmt.Errorf("extract header view at block %d: %w", headers[i].Number.Uint64(), err)
			}

			parentCommitLayout := qcs[i].BlockHash == headers[i].ParentHash && qcs[i].View <= headerView
			legacySameViewLayout := qcs[i].View == headerView
			if !parentCommitLayout && !legacySameViewLayout {
				return fmt.Errorf(
					"QC in header %d is incompatible: qc.view=%d header.view=%d qc.block=%s parent=%s",
					headers[i].Number.Uint64(),
					qcs[i].View,
					headerView,
					qcs[i].BlockHash,
					headers[i].ParentHash,
				)
			}

			// For the current layout, the header carries the most recent committed QC,
			// which should certify the parent block. Older layouts embedded a same-view QC.
			if parentCommitLayout && i > 0 && qcs[i].BlockHash != headers[i-1].Hash() {
				return fmt.Errorf("QC block hash %s != previous header hash %s at block %d", qcs[i].BlockHash, headers[i-1].Hash(), headers[i].Number.Uint64())
			}
		}

		// Genesis / no-QC cases are exempt from aggregate verification.
		if qcs[i].View == 0 {
			continue
		}

		// Verify QC BLS aggregate signature.
		if vs != nil {
			if err := hotstuff.VerifyQCAnyDomain(&qcs[i], vs); err != nil {
				return fmt.Errorf("QC verification failed at block %d: %w", headers[i].Number.Uint64(), err)
			}
		}
	}

	return nil
}

// extractQCFromHeader extracts the optional HotStuff QC from header extra-data.
// It accepts both the legacy layouts and the current layout where header extra
// may carry an embedded committed QC followed by the BLS seal.
func extractQCFromHeader(h *block.Header) hotstuff.QuorumCertificate {
	qc, err := hotstuff.ExtractHeaderQC(h.Extra)
	if err != nil || qc == nil {
		return hotstuff.GenesisQC()
	}
	return *qc
}
