package bridge

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/log"
)

// Relayer watches N42 blocks and periodically submits header chain proofs
// to the Ethereum verifier contract. This is the off-chain component that
// bridges N42 state to Ethereum using ZK proofs.
//
// Flow: N42 new blocks → batch headers → SP1 prove → submit to ETH Verifier
type Relayer struct {
	chain        common.IBlockChain
	headerProver *HeaderChainProver
	validatorSet *hotstuff.ValidatorSet

	// Configuration
	batchSize    uint64        // Number of blocks per proof batch
	pollInterval time.Duration // How often to check for new blocks

	// Thread-safe: accessed from Run() goroutine and ZKRouter queries.
	lastProvenBlock atomic.Uint64

	// ETH submission (interface for testability)
	submitter ProofSubmitter
}

// ProofSubmitter submits header chain proofs to the Ethereum verifier.
type ProofSubmitter interface {
	SubmitHeaderChainProof(ctx context.Context, proof *HeaderChainProof) error
}

// RelayerConfig holds relayer configuration.
type RelayerConfig struct {
	BatchSize    uint64        `json:"batchSize" yaml:"batchSize"`       // Blocks per proof (default: 100)
	PollInterval time.Duration `json:"pollInterval" yaml:"pollInterval"` // Poll interval (default: 12s)
	StartBlock   uint64        `json:"startBlock" yaml:"startBlock"`     // Block to start relaying from
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
	if cfg.BatchSize == 0 {
		cfg.BatchSize = 100
	}
	r := &Relayer{
		chain:        chain,
		headerProver: headerProver,
		validatorSet: vs,
		batchSize:    cfg.BatchSize,
		pollInterval: cfg.PollInterval,
		submitter:    submitter,
	}
	r.lastProvenBlock.Store(cfg.StartBlock)
	return r
}

// Run starts the relayer main loop.
func (r *Relayer) Run(ctx context.Context) error {
	log.Info("Bridge relayer started",
		"batchSize", r.batchSize,
		"pollInterval", r.pollInterval,
		"startBlock", r.lastProvenBlock.Load(),
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

// LastProvenBlock returns the last proven block number (thread-safe).
func (r *Relayer) LastProvenBlock() uint64 {
	return r.lastProvenBlock.Load()
}

// checkAndProve checks if enough new blocks exist for a proof batch.
func (r *Relayer) checkAndProve(ctx context.Context) error {
	current := r.chain.CurrentBlock()
	if current == nil {
		return nil
	}
	currentNum := current.Number64().Uint64()
	lastProven := r.lastProvenBlock.Load()

	if currentNum < lastProven+r.batchSize {
		return nil
	}

	startBlock := lastProven + 1
	endBlock := startBlock + r.batchSize - 1
	if endBlock > currentNum {
		endBlock = currentNum
	}

	log.Info("Generating header chain proof",
		"startBlock", startBlock,
		"endBlock", endBlock,
	)

	proof, err := ProveHeaderRange(r.chain, r.validatorSet, startBlock, endBlock)
	if err != nil {
		return fmt.Errorf("prove range %d-%d: %w", startBlock, endBlock, err)
	}

	headerProofsGenerated.Inc()

	if r.submitter != nil {
		submitStart := time.Now()
		if err := r.submitter.SubmitHeaderChainProof(ctx, proof); err != nil {
			return fmt.Errorf("submit proof: %w", err)
		}
		proofLatency.Observe(time.Since(submitStart).Seconds())
		headerProofsSubmitted.Inc()
	}

	r.lastProvenBlock.Store(endBlock)
	latestVerifiedBlock.Set(float64(endBlock))

	log.Info("Header chain proof submitted",
		"startBlock", startBlock,
		"endBlock", endBlock,
		"stateRoot", proof.StateRoot,
	)

	return nil
}

// extractQCFromHeader extracts the HotStuff QC from header extra-data.
// Extra-data layout: magic("N42H",4) + view(8,LE) + QC(variable SSZ) or BLS seal(96)
// Returns genesis QC if no QC is embedded (pre-HotStuff or non-HotStuff blocks).
func extractQCFromHeader(h *block.Header) hotstuff.QuorumCertificate {
	const magicLen = 4
	const viewLen = 8
	const minExtra = magicLen + viewLen // 12
	const blsSigLen = 96

	if len(h.Extra) <= minExtra {
		return hotstuff.GenesisQC()
	}

	// Verify HotStuff magic "N42H"
	if string(h.Extra[:magicLen]) != "N42H" {
		return hotstuff.GenesisQC()
	}

	qcData := h.Extra[minExtra:]
	if len(qcData) == blsSigLen {
		// BLS seal only (no embedded QC)
		return hotstuff.GenesisQC()
	}

	// Decode the SSZ-encoded QC
	qc, err := hotstuff.DecodeQC(qcData)
	if err != nil {
		return hotstuff.GenesisQC()
	}
	return *qc
}
