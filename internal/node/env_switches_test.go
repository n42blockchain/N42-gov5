// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package node

import (
	"testing"

	"github.com/n42blockchain/N42/common/envswitch"
)

func TestLogEnvSwitchReport(t *testing.T) {
	// The report only logs; it must cope with every combination of inputs.
	logEnvSwitchReport(nil, nil)
	logEnvSwitchReport([]envswitch.Info{
		{Name: "N42_A", Raw: "1", Scope: envswitch.Perf},
		{Name: "N42_B", Raw: "2", Scope: envswitch.BenchOnly},
		{Name: "N42_C", Raw: "3", Scope: envswitch.Consensus},
	}, []string{"N42_TYPO"})
}
