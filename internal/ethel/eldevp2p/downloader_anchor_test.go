//go:build n42el

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

package eldevp2p

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// TestVerifyAnchorRoundTripEmptyProof pins the ⑤b loud-failure guard: an empty
// captured proof must error (never silently pass), so a Merkle stage that
// produced no anchor nodes cannot masquerade as a verified anchor. The happy
// path + wrong-root rejection are covered by stateless.TestVerifyAnchorRoundTrip.
func TestVerifyAnchorRoundTripEmptyProof(t *testing.T) {
	if err := verifyAnchorRoundTrip(1, types.Hash{}, nil); err == nil {
		t.Fatal("expected error for nil proof")
	}
	if err := verifyAnchorRoundTrip(1, types.Hash{}, [][]byte{}); err == nil {
		t.Fatal("expected error for empty proof slice")
	}
}
