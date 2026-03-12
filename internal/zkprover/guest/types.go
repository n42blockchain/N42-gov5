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

// Package guest defines execution logic for the ZISK zkVM guest program.
// It re-exports types from internal/zkprover for convenience.
package guest

import (
	"github.com/n42blockchain/N42/internal/zkprover"
)

// Type aliases from internal/zkprover to maintain guest package API.
type (
	ForkConfig  = zkprover.ForkConfig
	GuestInput  = zkprover.GuestInput
	GuestOutput = zkprover.GuestOutput
)

// Re-export encoding functions.
var (
	EncodeGuestInput  = zkprover.EncodeGuestInput
	DecodeGuestInput  = zkprover.DecodeGuestInput
	EncodeGuestOutput = zkprover.EncodeGuestOutput
	DecodeGuestOutput = zkprover.DecodeGuestOutput
)
