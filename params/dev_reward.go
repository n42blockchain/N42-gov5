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
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/holiman/uint256"
)

// DevReward is hotstuff.devBlockReward in wei. Legacy chainspecs write it as
// a JSON number that fits a uint64 (as the field always was); a Profile-1
// genesis writes it as a decimal string, which spans the full uint256 range.
// The form it was read in is kept, so a stored chain config re-encodes the
// way it was written and a Profile-1 config read back from the database is
// still recognisably a string.
type DevReward struct {
	v      uint256.Int
	quoted bool
}

// NewDevReward returns a legacy (JSON number) reward of v wei.
func NewDevReward(v uint64) DevReward {
	var r DevReward
	r.v.SetUint64(v)
	return r
}

// NewDevRewardDecimal parses a Profile-1 (decimal string) reward.
func NewDevRewardDecimal(s string) (DevReward, error) {
	var r DevReward
	if err := r.setDecimal(s); err != nil {
		return DevReward{}, err
	}
	return r, nil
}

// IsZero reports whether the field is absent: zero and not written as a
// string. It drives `omitzero`, so a Profile-1 "0" still encodes.
func (r DevReward) IsZero() bool { return r.v.IsZero() && !r.quoted }

// Positive reports whether the reward pays anything.
func (r DevReward) Positive() bool { return !r.v.IsZero() }

// Quoted reports whether the value was written as a decimal string.
func (r DevReward) Quoted() bool { return r.quoted }

// Amount returns a fresh copy of the reward in wei.
func (r DevReward) Amount() *uint256.Int { return new(uint256.Int).Set(&r.v) }

// String returns the decimal value.
func (r DevReward) String() string { return r.v.Dec() }

// MarshalJSON writes the value in the form it was read in.
func (r DevReward) MarshalJSON() ([]byte, error) {
	if r.quoted {
		return json.Marshal(r.v.Dec())
	}
	if !r.v.IsUint64() {
		return nil, fmt.Errorf("devBlockReward %s does not fit the legacy number form", r.v.Dec())
	}
	return []byte(strconv.FormatUint(r.v.Uint64(), 10)), nil
}

// UnmarshalJSON accepts the legacy JSON number (uint64, exactly as the old
// uint64 field did) or a decimal string of at most 2^256-1 with no sign,
// prefix or leading zero. Whether a Profile-1 genesis used the string form
// is checked by the profile validation, not here.
func (r *DevReward) UnmarshalJSON(input []byte) error {
	input = bytes.TrimSpace(input)
	if string(input) == "null" {
		return nil
	}
	if len(input) > 0 && input[0] == '"' {
		var s string
		if err := json.Unmarshal(input, &s); err != nil {
			return fmt.Errorf("devBlockReward: %w", err)
		}
		return r.setDecimal(s)
	}
	n, err := strconv.ParseUint(string(input), 10, 64)
	if err != nil {
		return fmt.Errorf("devBlockReward: %q is not a uint64 number: %w", input, err)
	}
	r.v.SetUint64(n)
	r.quoted = false
	return nil
}

func (r *DevReward) setDecimal(s string) error {
	if !isCanonicalDecimal(s) {
		return fmt.Errorf("devBlockReward: %q is not a canonical decimal integer", s)
	}
	v, err := uint256.FromDecimal(s)
	if err != nil {
		return fmt.Errorf("devBlockReward: %q exceeds 2^256-1: %w", s, err)
	}
	r.v = *v
	r.quoted = true
	return nil
}

// isCanonicalDecimal accepts "0" or a digit string without a leading zero.
func isCanonicalDecimal(s string) bool {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
