package types

import (
	"errors"
	"unicode/utf8"

	chain "github.com/n42blockchain/N42/common/types"
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
		if err := a.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Validate enforces the governance tuple shape, including floor quantization.
// Generic choice answers can contain up to 256 classes; templates impose their
// narrower contract limits separately.
func (a QuantizedAnswer) Validate() error {
	if a.ConfidencePPM > PPM {
		return errors.New("invalid answer confidence")
	}
	if a.Kind == 3 {
		if a.ValuePPM > PPM || a.Selected != 0 || a.ConfidencePPM != 0 || len(a.ProbabilitiesPPM) != 0 {
			return errors.New("invalid Noul answer")
		}
		return nil
	}
	n := len(a.ProbabilitiesPPM)
	if (a.Kind != 1 && a.Kind != 2) || n < 2 || n > 256 || int(a.Selected) >= n {
		return errors.New("invalid answer kind or dimensions")
	}
	var sum uint64
	var maximum uint32
	for _, v := range a.ProbabilitiesPPM {
		if v > PPM {
			return errors.New("invalid answer probability")
		}
		sum += uint64(v)
		if v > maximum {
			maximum = v
		}
	}
	if sum > PPM || sum+uint64(n) < PPM {
		return errors.New("invalid quantized distribution")
	}
	if a.Kind == 1 && (a.ValuePPM != 0 || a.ProbabilitiesPPM[a.Selected] != maximum) {
		return errors.New("invalid choice selection")
	}
	if a.Kind == 2 && (n > 10 || a.Selected != 0 || uint64(a.ValuePPM) > uint64(n-1)*PPM) {
		return errors.New("invalid score answer")
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
