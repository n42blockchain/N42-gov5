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
//
// Authenticated JSON-RPC module registration. Filters the RPC API
// list exposed on the engine / authenticated endpoint (port 20014)
// so only consensus-layer endpoints (engine_*, eth_*, debug_*) are
// reachable through the JWT-protected transport.

package node

import "github.com/n42blockchain/N42/modules/rpc/jsonrpc"

func authenticatedModules(apis []jsonrpc.API) []string {
	seen := make(map[string]struct{})
	modules := make([]string, 0)
	for _, api := range apis {
		if !api.Authenticated {
			continue
		}
		if _, ok := seen[api.Namespace]; ok {
			continue
		}
		seen[api.Namespace] = struct{}{}
		modules = append(modules, api.Namespace)
	}
	return modules
}
