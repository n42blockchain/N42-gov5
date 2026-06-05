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
	"net/http/httptest"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/ethel/stateless"
	"github.com/n42blockchain/N42/internal/ethel/stateless/serve"
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

// TestSignAndPostAttestation closes the multi-IDC loop in a test: a verifier
// signs an attestation and POSTs it to a real AttestHandler-backed aggregator,
// which counts and finalises it. A dead aggregator yields a (non-fatal) error.
func TestSignAndPostAttestation(t *testing.T) {
	agg := stateless.NewAggregator(nil, 1, 0)
	srv := httptest.NewServer(serve.AttestHandler(agg, nil))
	defer srv.Close()

	key, _ := crypto.GenerateKey()
	sr := types.HexToHash("0x00000000000000000000000000000000000000000000000000000000000000aa")
	rr := types.HexToHash("0x00000000000000000000000000000000000000000000000000000000000000bb")

	if err := signAndPostAttestation(key, srv.URL, 100, sr, rr); err != nil {
		t.Fatalf("signAndPostAttestation: %v", err)
	}
	if c, fin := agg.Status(100, sr, rr); c != 1 || !fin {
		t.Fatalf("aggregator after submit: count=%d fin=%v want 1,true", c, fin)
	}

	// A dead aggregator must surface an error (caller logs it, never halts sync).
	if err := signAndPostAttestation(key, "http://127.0.0.1:1", 1, sr, rr); err == nil {
		t.Fatal("expected error posting to a dead aggregator")
	}
}
