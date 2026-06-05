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

package serve

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/ethel/stateless"
)

// TestAttestHandler exercises the aggregator HTTP endpoints end-to-end: distinct
// verifiers POST attestations, the count rises and finalises at threshold, and
// the status endpoint reflects it. A malformed body is rejected.
func TestAttestHandler(t *testing.T) {
	agg := stateless.NewAggregator(nil, 2, 0)
	srv := httptest.NewServer(AttestHandler(agg, nil))
	defer srv.Close()

	sr := types.HexToHash("0x00000000000000000000000000000000000000000000000000000000000000aa")
	rr := types.HexToHash("0x00000000000000000000000000000000000000000000000000000000000000bb")

	post := func(t *testing.T, wire []byte) (int, map[string]any) {
		t.Helper()
		resp, err := http.Post(srv.URL+"/attest", "application/octet-stream", bytes.NewReader(wire))
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	// Two distinct verifiers → finalised at threshold 2.
	for i := 0; i < 2; i++ {
		key, _ := crypto.GenerateKey()
		a, _ := stateless.SignAttestation(key, 100, sr, rr)
		wire, _ := stateless.EncodeAttestation(a)
		code, out := post(t, wire)
		if code != http.StatusOK {
			t.Fatalf("submit %d: status %d", i, code)
		}
		if out["counted"] != true {
			t.Fatalf("submit %d: counted=%v", i, out["counted"])
		}
		wantFinal := i == 1
		if out["finalized"] != wantFinal {
			t.Fatalf("submit %d: finalized=%v want %v (count=%v)", i, out["finalized"], wantFinal, out["count"])
		}
	}

	// Status endpoint reflects the finalised group.
	url := fmt.Sprintf("%s/attest-status?n=100&state=%s&receipt=%s", srv.URL, sr.Hex(), rr.Hex())
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("status get: %v", err)
	}
	defer resp.Body.Close()
	var st map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&st)
	if st["count"].(float64) != 2 || st["finalized"] != true {
		t.Fatalf("status: %+v want count=2 finalized=true", st)
	}

	// Malformed body → 400.
	if code, _ := post(t, []byte("garbage")); code != http.StatusBadRequest {
		t.Fatalf("garbage body: status %d want 400", code)
	}
}
