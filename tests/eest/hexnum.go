// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// hexnum.go — lenient number types for EEST fixture JSON. Fixtures write
// quantities with leading zeros ("0x07270e00", "0x00"), which the strict
// Engine-API hexutil decoders reject; geth decodes them with
// math.HexOrDecimal64 / HexOrDecimal256.

package eest

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
)

// Uint64 is a hex ("0x..") or decimal uint64 that tolerates leading zeros.
type Uint64 uint64

// UnmarshalJSON accepts a JSON string or number.
func (u *Uint64) UnmarshalJSON(input []byte) error {
	b, err := parseNumber(input)
	if err != nil {
		return err
	}
	if !b.IsUint64() {
		return fmt.Errorf("number %s overflows uint64", b)
	}
	*u = Uint64(b.Uint64())
	return nil
}

// Big is a hex ("0x..") or decimal big integer that tolerates leading zeros.
type Big big.Int

// UnmarshalJSON accepts a JSON string or number.
func (b *Big) UnmarshalJSON(input []byte) error {
	v, err := parseNumber(input)
	if err != nil {
		return err
	}
	*b = Big(*v)
	return nil
}

// ToInt returns the value as *big.Int.
func (b *Big) ToInt() *big.Int { return (*big.Int)(b) }

// ParseBig parses a fixture quantity string.
func ParseBig(s string) (*big.Int, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "0x" || s == "0X" {
		return new(big.Int), nil
	}
	v := new(big.Int)
	ok := false
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		_, ok = v.SetString(s[2:], 16)
	} else {
		_, ok = v.SetString(s, 10)
	}
	if !ok {
		return nil, fmt.Errorf("invalid number %q", s)
	}
	return v, nil
}

func parseNumber(input []byte) (*big.Int, error) {
	var s string
	if len(input) > 0 && input[0] == '"' {
		if err := json.Unmarshal(input, &s); err != nil {
			return nil, err
		}
	} else {
		s = string(input)
	}
	return ParseBig(s)
}
