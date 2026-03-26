package bridge

import (
	"context"
	"fmt"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/log"
)

// Relayer watches N42 blocks and periodically submits header chain proofs
// to the Ethereum verifier contract. This is the off-chain component that
// bridges N42 state to Ethereum using ZK proofs.
//
// Flow: N42 new blocks → batch headers → SP1 prove → submit to ETH Verifier
type Relayer struct {
	chain          common.IBlockChain
	headerProver   *HeaderChainProver
	validatorSet   *hotstuff.ValidatorSet

	// Configuration
	batchSize      uint64        // Number of blocks per proof batch
	pollInterval   time.Duration // How often to check for new blocks
	lastProvenBlock uint64       // Last block that has been proven

	// ETH submission (interface for testability)
	submitter      ProofSubmitter
}

// ProofSubmitter submits header chain proofs to the Ethereum verifier.
// Abstracted for testing — production implementation uses ethclient.
type ProofSubmitter interface {
	SubmitHeaderChainProof(ctx context.Context, proof *HeaderChainProof) error
}

// RelayerConfig holds relayer configuration.
type RelayerConfig struct {
	BatchSize    uint64        // Blocks per proof (default: 100)
	PollInterval time.Duration // Poll interval (default: 12s)
	StartBlock   uint64        // Block to start relaying from
}

// DefaultRelayerConfig returns sensible defaults.
func DefaultRelayerConfig() *RelayerConfig {
	return &RelayerConfig{
		BatchSize:    100,
		PollInterval: 12 * time.Second,
	}
}

// NewRelayer creates a new bridge relayer.
func NewRelayer(
	chain common.IBlockChain,
	headerProver *HeaderChainProver,
	vs *hotstuff.ValidatorSet,
	submitter ProofSubmitter,
	cfg *RelayerConfig,
) *Relayer {
	if cfg == nil {
		cfg = DefaultRelayerConfig()
	}
	return &Relayer{
		chain:           chain,
		headerProver:    headerProver,
		validatorSet:    vs,
		batchSize:       cfg.BatchSize,
		pollInterval:    cfg.PollInterval,
		lastProvenBlock: cfg.StartBlock,
		submitter:       submitter,
	}
}

// Run starts the relayer main loop. It watches for new N42 blocks,
// batches them, generates ZK proofs, and submits to Ethereum.
func (r *Relayer) Run(ctx context.Context) error {
	log.Info("Bridge relayer started",
		"batchSize", r.batchSize,
		"pollInterval", r.pollInterval,
		"startBlock", r.lastProvenBlock,
	)

	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("Bridge relayer stopped")
			return ctx.Err()
		case <-ticker.C:
			if err := r.checkAndProve(ctx); err != nil {
				log.Warn("Relayer proof cycle failed", "err", err)
			}
		}
	}
}

// checkAndProve checks if enough new blocks exist for a proof batch.
func (r *Relayer) checkAndProve(ctx context.Context) error {
	current := r.chain.CurrentBlock()
	if current == nil {
		return nil
	}
	currentNum := current.Number64().Uint64()

	// Need at least batchSize new blocks
	if currentNum < r.lastProvenBlock+r.batchSize {
		return nil
	}

	startBlock := r.lastProvenBlock + 1
	endBlock := startBlock + r.batchSize - 1
	if endBlock > currentNum {
		endBlock = currentNum
	}

	log.Info("Generating header chain proof",
		"startBlock", startBlock,
		"endBlock", endBlock,
	)

	proof, err := r.proveRange(ctx, startBlock, endBlock)
	if err != nil {
		return fmt.Errorf("prove range %d-%d: %w", startBlock, endBlock, err)
	}

	if r.submitter != nil {
		if err := r.submitter.SubmitHeaderChainProof(ctx, proof); err != nil {
			return fmt.Errorf("submit proof: %w", err)
		}
	}

	r.lastProvenBlock = endBlock
	log.Info("Header chain proof submitted",
		"startBlock", startBlock,
		"endBlock", endBlock,
		"stateRoot", proof.StateRoot,
	)

	return nil
}

// proveRange generates a header chain proof for a block range.
func (r *Relayer) proveRange(_ context.Context, startBlock, endBlock uint64) (*HeaderChainProof, error) {
	count := endBlock - startBlock + 1
	headers := make([]*block.Header, 0, count)
	qcs := make([]hotstuff.QuorumCertificate, 0, count)

	for num := startBlock; num <= endBlock; num++ {
		blk, err := r.chain.GetBlockByNumber(uint256.NewInt(num))
		if err != nil || blk == nil {
			return nil, fmt.Errorf("block %d not found", num)
		}
		hdr, ok := blk.Header().(*block.Header)
		if !ok {
			return nil, fmt.Errorf("block %d: invalid header type", num)
		}
		headers = append(headers, hdr)

		// Extract QC from header extra-data (if present)
		qc := extractQCFromHeader(hdr)
		qcs = append(qcs, qc)
	}

	// Verify locally before generating ZK proof
	if err := VerifyHeaderChainLocally(headers, qcs, r.validatorSet); err != nil {
		return nil, fmt.Errorf("local verification failed: %w", err)
	}

	lastHeader := headers[len(headers)-1]
	stateRoot := lastHeader.Root
	publicInputs := EncodePublicInputs(startBlock, endBlock, types.Hash(stateRoot))

	// TODO: Submit to SP1 prover for ZK proof generation.
	// For now, return a proof with local verification only.
	return &HeaderChainProof{
		StartBlock:   startBlock,
		EndBlock:     endBlock,
		StateRoot:    types.Hash(stateRoot),
		PublicInputs: publicInputs,
		ProofData:    nil, // SP1 proof will be filled by prover
	}, nil
}

// extractQCFromHeader extracts the HotStuff QC from header extra-data.
// Returns genesis QC if no QC is embedded (pre-HotStuff blocks).
func extractQCFromHeader(h *block.Header) hotstuff.QuorumCertificate {
	// HotStuff extra-data layout: magic(4) + view(8) + QC(variable) or BLS seal(96)
	const minExtra = 12
	const blsSigLen = 96

	if len(h.Extra) <= minExtra {
		return hotstuff.GenesisQC()
	}

	qcData := h.Extra[minExtra:]
	if len(qcData) == blsSigLen {
		// BLS seal only, no QC
		return hotstuff.GenesisQC()
	}

	// Try to decode QC (may fail for non-HotStuff blocks)
	// Reuse the codec from the hotstuff package via a thin wrapper
	return hotstuff.GenesisQC() // Placeholder — full QC extraction requires codec access
}

// GetBlockByNumber is a helper type alias for chain access.
type GetBlockByNumber = func(num uint64) block.IBlock
