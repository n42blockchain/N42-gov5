// Package aggregate never silently resolves provider disagreement by voting.
package aggregate

import (
	"bytes"
	"encoding/json"
	"errors"

	chain "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	d "github.com/n42blockchain/N42/internal/ddn/types"
)

// Outcome records one configured provider's result or explicit failure.
type Outcome struct {
	ProviderDID string
	ModelHash   chain.Hash
	Family      string
	Result      d.DecisionResult
	Failed      bool
}
type Combined struct {
	Result             d.DecisionResult
	EvidenceCommitment chain.Hash
	Disagreement       bool
}

func clone(r d.DecisionResult) d.DecisionResult {
	r.ProbabilitiesPPM = append([]uint32{}, r.ProbabilitiesPPM...)
	r.Answers = append([]d.QuantizedAnswer{}, r.Answers...)
	for i := range r.Answers {
		r.Answers[i].ProbabilitiesPPM = append([]uint32{}, r.Answers[i].ProbabilitiesPPM...)
	}
	return r
}
func comparable(r d.DecisionResult) []byte {
	r = clone(r)
	r.ConfidencePPM = 0
	r.NeedEscalation = false
	for i := range r.Answers {
		r.Answers[i].ConfidencePPM = 0
	}
	b, _ := json.Marshal(r)
	return b
}

// Combine requires the entire requested quorum to agree on typed values and
// distributions. Confidence is the minimum, and escalation is ORed. A split,
// missing/invalid output or correlated-only quorum returns UNKNOWN/escalation.
func Combine(req d.DecisionRequest, outcomes []Outcome) (Combined, error) {
	if req.Quorum < 1 || req.Quorum > 16 || len(outcomes) > 16 {
		return Combined{}, errors.New("invalid aggregate quorum")
	}
	result := d.DecisionResult{Label: "UNKNOWN", NeedEscalation: true}
	bad := len(outcomes) != int(req.Quorum)
	seen := map[string]bool{}
	families := map[string]bool{}
	type evidence struct {
		ProviderDID string     `json:"provider_did"`
		ModelHash   chain.Hash `json:"model_hash"`
		ResultHash  chain.Hash `json:"result_hash"`
		Failed      bool       `json:"failed"`
	}
	ev := make([]evidence, 0, len(outcomes))
	var agreed []byte
	first := true
	for _, o := range outcomes {
		invalid := o.Failed || o.ProviderDID == "" || seen[o.ProviderDID] || o.ModelHash == (chain.Hash{}) || o.Result.Validate() != nil
		seen[o.ProviderDID] = true
		if o.Family != "" {
			families[o.Family] = true
		}
		normalized := clone(o.Result)
		receipt := d.DecisionReceipt{Version: d.Version, ChainID: req.ChainID, RequestID: req.RequestID, ProviderDID: o.ProviderDID, ModelHash: o.ModelHash, InputHash: req.InputHash, PolicyHash: req.PolicyHash, Nonce: req.Nonce, Result: normalized}
		hash, err := receipt.CanonicalHash()
		if err != nil {
			invalid = true
		}
		ev = append(ev, evidence{o.ProviderDID, o.ModelHash, hash, invalid})
		if invalid {
			bad = true
			continue
		}
		if first {
			result = normalized
			agreed = comparable(normalized)
			first = false
		} else {
			if !bytes.Equal(agreed, comparable(normalized)) {
				bad = true
			}
			if normalized.ConfidencePPM < result.ConfidencePPM {
				result.ConfidencePPM = normalized.ConfidencePPM
			}
			result.NeedEscalation = result.NeedEscalation || normalized.NeedEscalation
			if len(normalized.Answers) == len(result.Answers) {
				for i := range result.Answers {
					if normalized.Answers[i].ConfidencePPM < result.Answers[i].ConfidencePPM {
						result.Answers[i].ConfidencePPM = normalized.Answers[i].ConfidencePPM
					}
				}
			}
		}
	}
	if req.Quorum > 1 && len(families) < 2 {
		bad = true
	}
	if bad || first {
		result = d.DecisionResult{Label: "UNKNOWN", NeedEscalation: true}
	}
	result = result.EnforcePolicy(req)
	b, err := json.Marshal(ev)
	if err != nil {
		return Combined{}, err
	}
	return Combined{result, crypto.Keccak256Hash(b), bad}, nil
}
