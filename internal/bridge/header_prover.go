// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Package bridge provides the ZK-native cross-chain bridge for N42.
// The bridge uses mathematical proofs (SP1 ZK proofs + JMT Merkle proofs
// + HotStuff-2 BLS aggregate signatures) to verify N42 state on Ethereum
// without any trusted third party.
package bridge

import (
	"encoding/binary"
	"fmt"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/internal/zkprover"
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
	Headers         []SerializedHeader        // Encoded block headers
	QCs             []hotstuff.QuorumCertificate // One QC per header
	ValidatorPubKeys [][]byte                  // BLS public keys of the validator set
	ValidatorCount  uint32                     // Number of validators
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

// ProveHeaderRange generates a header chain proof for a block range.
// This is the shared implementation used by both Relayer and DAPublisher.
func ProveHeaderRange(
	chain common.IBlockChain,
	vs *hotstuff.ValidatorSet,
	startBlock, endBlock uint64,
) (*HeaderChainProof, error) {
	count := endBlock - startBlock + 1
	headers := make([]*block.Header, 0, count)
	qcs := make([]hotstuff.QuorumCertificate, 0, count)

	for num := startBlock; num <= endBlock; num++ {
		blk, err := chain.GetBlockByNumber(uint256.NewInt(num))
		if err != nil || blk == nil {
			return nil, fmt.Errorf("block %d not found", num)
		}
		hdr, ok := blk.Header().(*block.Header)
		if !ok {
			return nil, fmt.Errorf("block %d: invalid header type", num)
		}
		headers = append(headers, hdr)
		qcs = append(qcs, extractQCFromHeader(hdr))
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
		ProofData:    nil, // SP1 proof will be filled by prover
	}, nil
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

		// Verify QC view matches header
		if qcs[i].View != headers[i].Number.Uint64() {
			// Genesis QC (view=0) is exempt
			if qcs[i].View != 0 {
				return fmt.Errorf("QC view %d != header number %d", qcs[i].View, headers[i].Number.Uint64())
			}
		}

		// Verify QC BLS aggregate signature
		if qcs[i].View > 0 {
			if err := hotstuff.VerifyQCAnyDomain(&qcs[i], vs); err != nil {
				return fmt.Errorf("QC verification failed at block %d: %w", headers[i].Number.Uint64(), err)
			}
		}
	}

	return nil
}
