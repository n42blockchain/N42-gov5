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
	"encoding/json"
	"io"
	"net/http"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel/stateless"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

// AttestHandler exposes the multi-IDC verification-count aggregator (P8 workflow
// #9) over HTTP. Independent verifier nodes POST a signed Attestation after they
// have statelessly verified a block; anyone can query the distinct-verifier count
// and finalisation state for a (block, roots) tuple. Optional per-IP request rate
// limiting (→ HTTP 429). This is a separate service from the producer Service —
// the aggregator may run on its own node.
//
//	POST /attest         body = 137-byte wire Attestation → {"signer","counted","count","finalized"}
//	GET  /attest-status  ?n=N&state=0x..&receipt=0x..     → {"count","finalized"}
func AttestHandler(agg *stateless.Aggregator, rl *jsonrpc.RateLimiter) http.Handler {
	mux := http.NewServeMux()

	// An aggregator with no signer allowlist counts ANY valid signature, so its
	// distinct-signer threshold is sybil-defeatable (an attacker signs the public
	// roots with N self-generated keys). Refuse to serve it — exposing such a
	// count publicly would be worse than useless (it looks authoritative).
	allowlisted := agg.HasAllowlist()

	mux.HandleFunc("/attest", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		if !allowlisted {
			http.Error(w, "attestation aggregator requires a signer allowlist", http.StatusForbidden)
			return
		}
		// The wire form is a fixed 137 bytes; cap the read so a hostile client
		// cannot stream an unbounded body.
		body, err := io.ReadAll(io.LimitReader(r.Body, 256))
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		a, err := stateless.DecodeAttestation(body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		res, err := agg.Submit(a) // recovers + validates the signature
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"signer":    res.Signer.Hex(),
			"counted":   res.Counted,
			"count":     res.Count,
			"finalized": res.Finalized,
		})
	})

	mux.HandleFunc("/attest-status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}
		if !allowlisted {
			http.Error(w, "attestation aggregator requires a signer allowlist", http.StatusForbidden)
			return
		}
		n, err := qn(r)
		if err != nil {
			http.Error(w, "bad n", http.StatusBadRequest)
			return
		}
		sr, ok1 := qhash(r, "state")
		rr, ok2 := qhash(r, "receipt")
		if !ok1 || !ok2 {
			http.Error(w, "bad state/receipt", http.StatusBadRequest)
			return
		}
		count, finalized := agg.Status(n, sr, rr)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"count": count, "finalized": finalized})
	})

	var h http.Handler = mux
	if rl != nil {
		h = jsonrpc.RateLimitMiddleware(rl, h)
	}
	return h
}

// qhash parses a 0x-prefixed 32-byte hash query parameter.
func qhash(r *http.Request, key string) (types.Hash, bool) {
	v := r.URL.Query().Get(key)
	if v == "" {
		return types.Hash{}, false
	}
	b, err := hexutil.Decode(v)
	if err != nil || len(b) != 32 {
		return types.Hash{}, false
	}
	var h types.Hash
	copy(h[:], b)
	return h, true
}
