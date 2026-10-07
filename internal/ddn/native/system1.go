package native

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"errors"
	chain "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	d "github.com/n42blockchain/N42/internal/ddn/types"
)

var systemLabels = d.System1Labels()
var systemRules = []healthRule{
	{"CONSENSUS", []string{"consensus", "finality", "quorum", "qc mismatch"}},
	{"STORAGE", []string{"qmdb", "storage", "state root", "database"}},
	{"NETWORK", []string{"peer", "p2p", "peerdas", "network", "rpc timeout"}},
	{"EXECUTION", []string{"block-stm", "evm", "execution", "transaction conflict"}},
	{"CONFIGURATION", []string{"config", "invalid flag", "missing env", "genesis mismatch"}},
	{"PERFORMANCE", []string{"regression", "tps", "latency", "slow block"}},
}
var criticalPhrases = []string{"finality stalled", "qc mismatch", "state root mismatch", "data unavailable", "consensus halted"}
var unknownPhrases = []string{"error", "failed", "panic", "timeout"}
var degradedPhrases = []string{"failed", "error", "regression", "timeout", "conflict", "latency", "unavailable"}

func SystemLabels() []string { return append([]string{}, systemLabels...) }
func SystemHash() chain.Hash {
	b, _ := json.Marshal(struct {
		Algorithm                   string
		Labels                      []string
		Rules                       []healthRule
		Critical, Degraded, Unknown []string
	}{"n42-system1-rules-v2", systemLabels, systemRules, criticalPhrases, degradedPhrases, unknownPhrases})
	return crypto.Keccak256Hash(b)
}
func containsAny(s string, phrases []string) bool {
	for _, p := range phrases {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}

// System1 mirrors the n42-26 benchmark classification and health severity rules.
// One-hot outputs encode deterministic rules, not calibrated probabilities.
// Answers are health (HEALTHY/DEGRADED/CRITICAL), category, then Noul escalation.
func System1(ctx context.Context, input string) (d.DecisionResult, error) {
	if err := ctx.Err(); err != nil {
		return d.DecisionResult{}, err
	}
	if len(input) == 0 || len(input) > 8192 || !utf8.ValidString(input) {
		return d.DecisionResult{}, errors.New("invalid System1 event text")
	}
	s := strings.ToLower(input)
	label := "NORMAL"
	matched := false
	for _, r := range systemRules {
		if containsAny(s, r.Phrases) {
			label = r.Label
			matched = true
			break
		}
	}
	if !matched && containsAny(s, unknownPhrases) {
		label = "UNKNOWN"
	}
	severity := uint8(0)
	if containsAny(s, degradedPhrases) {
		severity = 1
	}
	critical := containsAny(s, criticalPhrases)
	if critical {
		severity = 2
	}
	escalation := critical || label == "UNKNOWN"
	index := uint8(0)
	for i, l := range systemLabels {
		if l == label {
			index = uint8(i)
		}
	}
	choice := func(n int, index uint8) d.QuantizedAnswer {
		p := make([]uint32, n)
		p[index] = d.PPM
		return d.QuantizedAnswer{Kind: 1, Selected: index, ProbabilitiesPPM: p}
	}
	value := uint32(0)
	if escalation {
		value = d.PPM
	}
	return d.DecisionResult{Label: label, NeedEscalation: escalation, ProbabilitiesPPM: choice(8, index).ProbabilitiesPPM, Answers: []d.QuantizedAnswer{choice(3, severity), choice(8, index), {Kind: 3, ValuePPM: value}}}, nil
}
