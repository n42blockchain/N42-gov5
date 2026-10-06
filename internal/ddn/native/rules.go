package native

import (
	"context"
	"encoding/json"
	"strings"

	chain "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	d "github.com/n42blockchain/N42/internal/ddn/types"
)

type healthRule struct {
	Label   string   `json:"label"`
	Phrases []string `json:"phrases"`
}

var healthRules = []healthRule{
	{"CONSENSUS", []string{"finality stalled", "consensus stalled", "conflicting finalized", "double vote", "quorum lost"}},
	{"STORAGE", []string{"database corrupted", "disk full", "state root mismatch", "mdbx error", "checksum mismatch"}},
	{"NETWORK", []string{"no peers", "connection refused", "peer timeout", "network unreachable"}},
	{"EXECUTION", []string{"execution reverted", "invalid transaction", "out of gas", "vm panic"}},
}

// RulesHash binds the actual embedded rule set and algorithm version.
func RulesHash() chain.Hash {
	b, _ := json.Marshal(struct {
		Algorithm string       `json:"algorithm"`
		Rules     []healthRule `json:"rules"`
	}{"health-rules-unicode-v1", healthRules})
	return crypto.Keccak256Hash(b)
}

// Health deliberately does not infer NORMAL from the absence of known errors.
// Rule matches have no calibrated probability and always require review.
func Health(ctx context.Context, input string) (d.DecisionResult, error) {
	r, _, err := HealthSignal(ctx, input)
	return r, err
}

// HealthSignal preserves evidence of multiple conflicting critical rule matches.
func HealthSignal(ctx context.Context, input string) (d.DecisionResult, bool, error) {
	if err := ctx.Err(); err != nil {
		return d.DecisionResult{}, false, err
	}
	ts, err := tokens(input)
	if err != nil {
		return d.DecisionResult{}, false, err
	}
	normalized := " " + strings.Join(ts, " ") + " "
	found := ""
	for _, r := range healthRules {
		for _, p := range r.Phrases {
			if strings.Contains(normalized, " "+p+" ") {
				if found != "" && found != r.Label {
					return unknown(), true, nil
				}
				found = r.Label
			}
		}
	}
	if found == "" {
		return unknown(), false, nil
	}
	r := unknown()
	r.Label = found
	return r, true, nil
}
