// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package params

import (
	"sort"
	"strings"
)

// profile1ForbiddenEnv are the process environment variables that set,
// override or default a consensus-visible value (standard P3, J4). A
// Profile-1 node refuses to start while any of them is set, whatever its
// value: the genesis is the only source. Some exist only on bench branches;
// they are listed so a bench binary cannot carry them onto a Profile-1 chain.
var profile1ForbiddenEnv = []string{
	"N42_MAX_GOSSIP_MB",              // block size cap (B15 is maxBlockBytes)
	"N42_STRESS_GASLIMIT",            // gas-limit step check bypass (A23)
	"N42_FRAME_BLOCKS",               // frame blocks, not in profile 1 (A12)
	"N42_VOTE_ON_AVAILABILITY",       // what a vote attests (A12, A13)
	"N42_ATTEST_COMMITTEE",           // committee attestation selector (bench branch)
	"N42_HOTSTUFF_LEADER_TENURE",     // leaderTenure (A15)
	"N42_HOTSTUFF_ROTATE_ON_TIMEOUT", // rotate-on-timeout (A22)
	"N42_DEFERRED_EXECUTION_TIME",    // deferred execution activation (A10)
	"N42_TXROOT_BLAKE3_TIME",         // BLAKE3 transactions root (A12)
}

// profile1ForbiddenEnvPrefixes refuse every variable with the prefix.
var profile1ForbiddenEnvPrefixes = []string{
	"N42_DEFERRED_DEPTH", // deferredExecutionDepth (A10), N42_DEFERRED_DEPTH*
	"N42_SYNC_LEGACY_",   // legacy sync tolerances (P5)
}

// profile1EnvAllowedValue permits a variable only with the value the profile
// prescribes anyway.
var profile1EnvAllowedValue = map[string]string{
	"N42_SYNC_ANCHOR": "enforce", // anchored ranges are enforced (standard §3)
}

// Profile1ForbiddenEnv returns, sorted, the names in environ ("KEY=VALUE"
// entries, as os.Environ returns) that a Profile-1 node must refuse.
func Profile1ForbiddenEnv(environ []string) []string {
	exact := make(map[string]bool, len(profile1ForbiddenEnv))
	for _, k := range profile1ForbiddenEnv {
		exact[k] = true
	}
	var out []string
	for _, kv := range environ {
		key, value, _ := strings.Cut(kv, "=")
		if exact[key] {
			out = append(out, key)
			continue
		}
		if want, ok := profile1EnvAllowedValue[key]; ok {
			if value != want {
				out = append(out, key)
			}
			continue
		}
		for _, p := range profile1ForbiddenEnvPrefixes {
			if strings.HasPrefix(key, p) {
				out = append(out, key)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}
