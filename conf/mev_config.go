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

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/holiman/uint256"
)

const (
	// DefaultMinBidWei is the default minimum bid (0 = accept any profitable bid).
	DefaultMinBidWei = "0"

	// MaxRelayURLs is the maximum number of relay URLs allowed.
	MaxRelayURLs = 32
)

// MEVBoostCfg holds configuration for MEV-Boost relay integration.
type MEVBoostCfg struct {
	// Enabled activates MEV-Boost integration. When false, the node always
	// uses locally built blocks.
	Enabled bool `json:"enabled" yaml:"enabled"`

	// RelayURLs is the list of MEV-Boost relay endpoints to query for bids.
	// Each URL should include the scheme (http:// or https://).
	RelayURLs []string `json:"relay_urls" yaml:"relay_urls"`

	// MinBidWei is the minimum bid value (in wei) that a relay block must
	// offer to be considered over the locally built block. This prevents
	// accepting relay blocks with negligible value improvement.
	// Format: decimal string (e.g. "1000000000000000" for 0.001 ETH).
	MinBidWei string `json:"min_bid_wei" yaml:"min_bid_wei"`
}

// Validate checks that the MEV-Boost configuration is well-formed.
// It normalizes relay URLs (trims trailing slashes), validates URL format,
// and parses the minimum bid value. Returns an error for invalid configurations.
func (c *MEVBoostCfg) Validate() error {
	if !c.Enabled {
		return nil
	}

	if len(c.RelayURLs) == 0 {
		return fmt.Errorf("mev-boost: enabled but no relay_urls configured")
	}

	if len(c.RelayURLs) > MaxRelayURLs {
		return fmt.Errorf("mev-boost: too many relay URLs (%d), max is %d", len(c.RelayURLs), MaxRelayURLs)
	}

	// Validate and normalize each relay URL.
	for i, rawURL := range c.RelayURLs {
		rawURL = strings.TrimSpace(rawURL)
		rawURL = strings.TrimRight(rawURL, "/")
		c.RelayURLs[i] = rawURL

		if rawURL == "" {
			return fmt.Errorf("mev-boost: relay_urls[%d] is empty", i)
		}

		parsed, err := url.Parse(rawURL)
		if err != nil {
			return fmt.Errorf("mev-boost: relay_urls[%d] invalid URL %q: %w", i, rawURL, err)
		}

		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return fmt.Errorf("mev-boost: relay_urls[%d] must use http or https scheme, got %q", i, parsed.Scheme)
		}

		if parsed.Host == "" {
			return fmt.Errorf("mev-boost: relay_urls[%d] has no host", i)
		}
	}

	// Check for duplicates.
	seen := make(map[string]struct{}, len(c.RelayURLs))
	for _, u := range c.RelayURLs {
		if _, dup := seen[u]; dup {
			return fmt.Errorf("mev-boost: duplicate relay URL %q", u)
		}
		seen[u] = struct{}{}
	}

	// Validate minimum bid.
	if c.MinBidWei == "" {
		c.MinBidWei = DefaultMinBidWei
	}
	if _, err := c.ParseMinBidWei(); err != nil {
		return err
	}

	return nil
}

// ParseMinBidWei parses the MinBidWei string into a uint256.Int.
// Returns a zero value if MinBidWei is empty or "0".
func (c *MEVBoostCfg) ParseMinBidWei() (*uint256.Int, error) {
	if c.MinBidWei == "" || c.MinBidWei == "0" {
		return new(uint256.Int), nil
	}

	val := new(uint256.Int)
	if err := val.SetFromDecimal(c.MinBidWei); err != nil {
		return nil, fmt.Errorf("mev-boost: invalid min_bid_wei %q: %w", c.MinBidWei, err)
	}

	return val, nil
}

// IsEnabled returns true if MEV-Boost integration is explicitly enabled
// and at least one relay URL is configured.
func (c *MEVBoostCfg) IsEnabled() bool {
	return c.Enabled && len(c.RelayURLs) > 0
}
