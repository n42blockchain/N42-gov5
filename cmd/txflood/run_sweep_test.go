package main

import (
	"crypto/ecdsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// txfloodFakeSignAt stands in for the real signer: runSweep only cares that
// it receives a string back and never inspects its content.
func txfloodFakeSignAt(priv *ecdsa.PrivateKey, from, to types.Address, nonce uint64, value *uint256.Int, gas, price uint64) string {
	return "0xdeadbeef"
}

func TestTxfloodRunSweep(t *testing.T) {
	// Sender 0 gets a healthy balance and sweeps; sender 1 has too little
	// balance to cover gas and is skipped.
	sender0 := senderHexForIndex(t, 0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var result interface{}
		switch req.Method {
		case "eth_getBalance":
			var params []string
			_ = json.Unmarshal(req.Params, &params)
			if len(params) > 0 && params[0] == sender0 {
				result = "0xde0b6b3a7640000" // plenty
			} else {
				result = "0x1" // dust, below gas cost
			}
		case "eth_getTransactionCount":
			result = "0x0"
		case "eth_sendRawTransaction":
			result = "0xhash"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer srv.Close()

	priv, _ := crypto.HexToECDSA("922c1ad85fb8691315b1ae54b39f7111ae3cfb2c36b038740af36844e9673eee")
	faucet := crypto.PubkeyToAddress(priv.PublicKey)

	code := runSweep([]string{srv.URL}, priv, faucet, 2, 2, 1000000000, txfloodFakeSignAt)
	if code != 0 {
		t.Fatalf("runSweep returned %d, want 0", code)
	}
}

func senderHexForIndex(t *testing.T, i int) string {
	t.Helper()
	k := deriveKey(i)
	return crypto.PubkeyToAddress(k.PublicKey).Hex()
}

func TestTxfloodRunSweepAllFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		resp := map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": "0xde0b6b3a7640000"}
		if req.Method == "eth_getTransactionCount" {
			resp = map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "error": map[string]string{"message": "boom"}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	priv, _ := crypto.HexToECDSA("922c1ad85fb8691315b1ae54b39f7111ae3cfb2c36b038740af36844e9673eee")
	faucet := crypto.PubkeyToAddress(priv.PublicKey)
	code := runSweep([]string{srv.URL}, priv, faucet, 1, 1, 1000000000, txfloodFakeSignAt)
	if code != 1 {
		t.Fatalf("runSweep returned %d, want 1 on nonce failure", code)
	}
}
