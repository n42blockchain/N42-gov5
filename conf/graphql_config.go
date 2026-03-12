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

// GraphQLCfg holds configuration for the GraphQL API endpoint.
type GraphQLCfg struct {
	// Enabled controls whether the GraphQL endpoint is started.
	Enabled bool `json:"enabled" yaml:"enabled"`

	// Endpoint is the URL path at which the GraphQL handler is mounted.
	// Defaults to "/graphql" when empty.
	Endpoint string `json:"endpoint" yaml:"endpoint"`
}

// DefaultGraphQLCfg returns the default GraphQL configuration.
func DefaultGraphQLCfg() GraphQLCfg {
	return GraphQLCfg{
		Enabled:  false,
		Endpoint: "/graphql",
	}
}

// EffectiveEndpoint returns the configured endpoint, falling back to the
// default "/graphql" when the Endpoint field is empty.
func (c *GraphQLCfg) EffectiveEndpoint() string {
	if c.Endpoint == "" {
		return "/graphql"
	}
	return c.Endpoint
}
