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
// N42 mixed production standard, Profile 1: the genesis switch
// (config.n42Profile = 1) under which every rule of the standard holds from
// block 0 / view 0 without an activation field.

package params

import "math/big"

// N42Profile1 is the config.n42Profile value of Standard Profile 1.
const N42Profile1 = 1

// IsProfile1 reports whether the chain runs Standard Profile 1.
func (c *ChainConfig) IsProfile1() bool {
	return c != nil && c.N42Profile == N42Profile1
}

// normalizeProfile1 maps the Profile-1 spellings onto the fields the rest of
// the node reads: amsterdamTime becomes GlamsterdamTime (gov5's name for the
// Amsterdam execution rules). A legacy config is left untouched.
func normalizeProfile1(cfg *ChainConfig) {
	if !cfg.IsProfile1() || cfg.AmsterdamTime == nil || cfg.GlamsterdamTime != nil {
		return
	}
	cfg.GlamsterdamTime = new(big.Int).Set(cfg.AmsterdamTime)
}
