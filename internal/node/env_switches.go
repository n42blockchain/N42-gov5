// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package node

import (
	"fmt"
	"os"
	"strings"

	"github.com/n42blockchain/N42/common/envswitch"
	"github.com/n42blockchain/N42/log"
)

// logEnvSwitches logs, once at start-up, which N42_* environment switches are
// set, warns about the bench-only and consensus-scope ones among them, and
// warns about N42_* names in the environment that no code reads (a typo fails
// silently otherwise).
func logEnvSwitches() {
	logEnvSwitchReport(envswitch.Set(), envswitch.Unknown(os.Environ()))
}

func logEnvSwitchReport(set []envswitch.Info, unknown []string) {
	var all, bench, consensus []string
	for _, s := range set {
		item := fmt.Sprintf("%s=%s", s.Name, s.Raw)
		all = append(all, fmt.Sprintf("%s (%s)", item, s.Scope))
		switch s.Scope {
		case envswitch.BenchOnly:
			bench = append(bench, item)
		case envswitch.Consensus:
			consensus = append(consensus, item)
		}
	}
	if len(all) > 0 {
		log.Info("N42 environment switches set", "count", len(all), "switches", strings.Join(all, " "))
	}
	if len(bench) > 0 {
		log.Warn("Bench-only N42 environment switches are set; do not run production with them", "switches", strings.Join(bench, " "))
	}
	if len(consensus) > 0 {
		log.Warn("Consensus-scope N42 environment switches are set; every validator must run the same values", "switches", strings.Join(consensus, " "))
	}
	if len(unknown) > 0 {
		log.Warn("Unknown N42_* environment variables (no code reads them; check for typos)", "names", strings.Join(unknown, " "))
	}
}
