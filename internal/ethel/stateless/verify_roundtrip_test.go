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

package stateless

import "testing"

// TestVerifyAnchorRoundTrip mirrors the eth-el downloader ⑤b verifier
// (eldevp2p.verifyAnchorRoundTrip): a captured multiproof must survive the full
// minimal-client path — VerifyProofAnchors on the raw nodes, then compact-wire
// encode → decode (the exact form a minimal client receives), then
// VerifyProofAnchors on the decoded nodes — and a wrong root must be rejected.
func TestVerifyAnchorRoundTrip(t *testing.T) {
	bt := fullTrie()
	for i := 0; i < 120; i++ {
		k := k32(uint64(i)*131 + 9)
		bt.update(keybytesToHex(k), []byte{byte(i), byte(i >> 8), 'x'})
	}
	root := bt.hash()

	var proof [][]byte
	collectNodes(bt.root, &proof)
	if e := encodeNode(bt.root); len(e) < 32 {
		proof = append(proof, e)
	}

	// 1) raw proof anchors to the production root (stateless engine agrees).
	if err := VerifyProofAnchors(root, proof); err != nil {
		t.Fatalf("raw VerifyProofAnchors: %v", err)
	}
	// 2) full compact-wire round-trip (encode → decode), then re-verify — exactly
	//    what a minimal client does with the bytes it receives.
	wire, err := CompactProofFromNodes(root, proof)
	if err != nil {
		t.Fatalf("CompactProofFromNodes: %v", err)
	}
	decoded, err := DecodeCompactToNodes(wire)
	if err != nil {
		t.Fatalf("DecodeCompactToNodes: %v", err)
	}
	if err := VerifyProofAnchors(root, decoded); err != nil {
		t.Fatalf("decoded-wire VerifyProofAnchors: %v", err)
	}

	// 3) a wrong root must be rejected (the loud-failure guard).
	bad := make([]byte, len(root))
	copy(bad, root)
	bad[0] ^= 0xFF
	if err := VerifyProofAnchors(bad, decoded); err == nil {
		t.Fatal("VerifyProofAnchors accepted a wrong root")
	}
}
