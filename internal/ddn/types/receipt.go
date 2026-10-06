package types

import (
	"errors"
	chain "github.com/n42blockchain/N42/common/types"
	"unicode/utf8"
)

// QuantizedAnswer matches the n42-26 governance Answer tuple.
type QuantizedAnswer struct {
	Kind             uint8    `json:"kind"`
	Selected         uint8    `json:"selected"`
	ValuePPM         uint32   `json:"value_ppm"`
	ConfidencePPM    uint32   `json:"confidence_ppm"`
	ProbabilitiesPPM []uint32 `json:"probabilities_ppm"`
}

// DecisionResult makes abstention/escalation explicit. No floating point
// values enter the signed protocol.
type DecisionResult struct {
	Label            string            `json:"label"`
	ProbabilitiesPPM []uint32          `json:"probabilities_ppm"`
	ConfidencePPM    uint32            `json:"confidence_ppm"`
	NeedEscalation   bool              `json:"need_escalation"`
	Answers          []QuantizedAnswer `json:"answers"`
}

func (r DecisionResult) Validate() error {
	if r.Label == "" || len(r.Label) > 128 || !utf8.ValidString(r.Label) || r.ConfidencePPM > PPM {
		return errors.New("invalid result label or confidence")
	}
	if err := validateDistribution(r.ProbabilitiesPPM); err != nil {
		return err
	}
	if len(r.Answers) > 64 {
		return errors.New("too many answers")
	}
	for _, a := range r.Answers {
		if a.Kind > 2 || a.ValuePPM > PPM || a.ConfidencePPM > PPM {
			return errors.New("invalid answer")
		}
		if err := validateDistribution(a.ProbabilitiesPPM); err != nil {
			return err
		}
		if len(a.ProbabilitiesPPM) > 0 && int(a.Selected) >= len(a.ProbabilitiesPPM) {
			return errors.New("selected answer outside distribution")
		}
	}
	return nil
}
func validateDistribution(p []uint32) error {
	if len(p) > 256 {
		return errors.New("too many probabilities")
	}
	var sum uint64
	for _, v := range p {
		if v > PPM {
			return errors.New("invalid probability")
		}
		sum += uint64(v)
	}
	if len(p) > 0 && sum != PPM {
		return errors.New("probabilities must sum to one million ppm")
	}
	return nil
}

type DecisionReceipt struct {
	ChainID            uint64         `json:"chain_id"`
	Version            uint32         `json:"version"`
	RequestID          chain.Hash     `json:"request_id"`
	ProviderDID        string         `json:"provider_did"`
	Model              string         `json:"model"`
	ModelVersion       string         `json:"model_version"`
	ModelHash          chain.Hash     `json:"model_hash"`
	PolicyHash         chain.Hash     `json:"policy_hash"`
	InputHash          chain.Hash     `json:"input_hash"`
	Result             DecisionResult `json:"result"`
	StartedAt          uint64         `json:"started_at"`
	CompletedAt        uint64         `json:"completed_at"`
	LatencyMs          uint64         `json:"latency_ms"`
	Expiry             uint64         `json:"expiry"`
	Nonce              uint64         `json:"nonce"`
	EvidenceCommitment chain.Hash     `json:"evidence_commitment"`
	ProviderSignature  string         `json:"provider_signature"`
}
