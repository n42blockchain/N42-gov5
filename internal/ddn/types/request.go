// Package types defines the version-1 DDN wire protocol. DDN is never a
// consensus input. See docs/DDN_CANONICAL.md for the canonical byte format.
package types

import (
	"errors"
	"fmt"
	"math/big"
	"unicode/utf8"

	chain "github.com/n42blockchain/N42/common/types"
)

const Version = 1
const PPM = 1_000_000

// DecisionRequest binds a bounded decision to input, policy and requester.
// Times are Unix milliseconds; cost is in wei, encoded as a decimal string.
type DecisionRequest struct {
	ChainID           uint64            `json:"chain_id"`
	Version           uint32            `json:"version"`
	RequestID         chain.Hash        `json:"request_id"`
	Task              string            `json:"task"`
	SchemaID          string            `json:"schema_id"`
	InputHash         chain.Hash        `json:"input_hash"`
	InputLocation     string            `json:"input_location"`
	PolicyHash        chain.Hash        `json:"policy_hash"`
	PolicyParameters  PolicyParameters  `json:"policy_parameters"`
	ModelRequirements ModelRequirements `json:"model_requirements"`
	PrivacyMode       string            `json:"privacy_mode"`
	Quorum            uint32            `json:"quorum"`
	MaxLatencyMs      uint64            `json:"max_latency_ms"`
	MaxCost           string            `json:"max_cost"`
	Deadline          uint64            `json:"deadline"`
	Nonce             uint64            `json:"nonce"`
	Requester         string            `json:"requester"`
	Signature         string            `json:"signature"`
}

type PolicyParameters struct {
	MinConfidencePPM uint32 `json:"min_confidence_ppm"`
	RequireHuman     bool   `json:"require_human"`
}

type ModelRequirements struct {
	ModelHash chain.Hash `json:"model_hash"`
	Family    string     `json:"family"`
}

func (r DecisionRequest) Validate(now uint64) error {
	if r.Version != Version {
		return errors.New("unsupported DDN version")
	}
	if r.Task == "" || r.SchemaID == "" || r.Requester == "" {
		return errors.New("task, schema_id and requester are required")
	}
	for _, s := range []string{r.Task, r.SchemaID, r.Requester, r.InputLocation, r.ModelRequirements.Family} {
		if !utf8.ValidString(s) || len(s) > 2048 {
			return errors.New("invalid or oversized request string")
		}
	}
	if r.InputHash == (chain.Hash{}) {
		return errors.New("input_hash is required")
	}
	if r.Quorum < 1 || r.Quorum > 16 || r.MaxLatencyMs < 1 || r.MaxLatencyMs > 60000 {
		return errors.New("invalid quorum or latency bound")
	}
	if r.Deadline <= now || r.Deadline-now > 86400000 {
		return errors.New("request deadline must be within the next 24 hours")
	}
	if r.PolicyParameters.MinConfidencePPM > PPM {
		return errors.New("confidence exceeds one million ppm")
	}
	if r.PrivacyMode != "public" && r.PrivacyMode != "private" {
		return errors.New("privacy_mode must be public or private")
	}
	if _, err := ParseMaxCost(r.MaxCost); err != nil {
		return err
	}
	id, err := r.CanonicalHash()
	if err != nil {
		return err
	}
	if r.RequestID != (chain.Hash{}) && r.RequestID != id {
		return fmt.Errorf("request_id does not match canonical hash")
	}
	return nil
}

// ParseMaxCost bounds the input before big-integer parsing on every entry path.
func ParseMaxCost(value string) (*big.Int, error) {
	if !decimal(value) {
		return nil, errors.New("max_cost must be canonical unsigned decimal")
	}
	cost, ok := new(big.Int).SetString(value, 10)
	if !ok || cost.BitLen() > 256 {
		return nil, errors.New("max_cost exceeds uint256")
	}
	return cost, nil
}

func decimal(s string) bool {
	if s == "" || len(s) > 78 || (len(s) > 1 && s[0] == '0') {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
