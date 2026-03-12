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

package conf

// MCPCfg holds configuration for the Model Context Protocol (MCP) server,
// which enables AI agents to query on-chain data through a standardized
// JSON-RPC interface.
type MCPCfg struct {
	// Enabled controls whether the MCP server starts with the node.
	Enabled bool `json:"enabled" yaml:"enabled"`

	// Port is the TCP port for the MCP HTTP server. Default: 8553.
	Port int `json:"port" yaml:"port"`

	// AllowedTools restricts which tools are exposed. Empty means all tools
	// are allowed. When set, only the listed tool names will be available
	// to MCP clients.
	AllowedTools []string `json:"allowed_tools,omitempty" yaml:"allowed_tools,omitempty"`
}

// DefaultMCPCfg returns the default MCP configuration with the server
// disabled and the default port set.
func DefaultMCPCfg() MCPCfg {
	return MCPCfg{
		Enabled: false,
		Port:    8553,
	}
}
