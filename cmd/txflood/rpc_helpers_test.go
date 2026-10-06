package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func TestTxfloodParseHexUint(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"0x10", 16},
		{"10", 16},
		{"0x0", 0},
		{"not-hex", 0},
		{"", 0},
	}
	for _, c := range cases {
		if got := parseHexUint(c.in); got != c.want {
			t.Errorf("parseHexUint(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestTxfloodHexToU64(t *testing.T) {
	if got := hexToU64("0x1f"); got != 31 {
		t.Errorf("hexToU64 = %d, want 31", got)
	}
	if got := hexToU64("0x0"); got != 0 {
		t.Errorf("hexToU64(0x0) = %d, want 0", got)
	}
}

func TestTxfloodMax(t *testing.T) {
	if max(1, 2) != 2 {
		t.Fatal("max(1,2) != 2")
	}
	if max(5, 3) != 5 {
		t.Fatal("max(5,3) != 5")
	}
}

// txfloodRPCServer builds an httptest server that answers single JSON-RPC
// requests via handle, which receives the decoded method/params and returns
// either a result value or an error message.
func txfloodRPCServer(t *testing.T, handle func(method string) (interface{}, string)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		result, errMsg := handle(req.Method)
		resp := map[string]interface{}{"jsonrpc": "2.0", "id": req.ID}
		if errMsg != "" {
			resp["error"] = map[string]string{"message": errMsg}
		} else {
			resp["result"] = result
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func TestTxfloodRpcCall(t *testing.T) {
	srv := txfloodRPCServer(t, func(method string) (interface{}, string) {
		if method == "eth_blockNumber" {
			return "0x5", ""
		}
		return nil, "unknown method"
	})
	defer srv.Close()

	res, err := rpcCall(srv.URL, "eth_blockNumber", nil)
	if err != nil {
		t.Fatalf("rpcCall error: %v", err)
	}
	var h string
	if err := json.Unmarshal(res, &h); err != nil || h != "0x5" {
		t.Fatalf("unexpected result %q err %v", res, err)
	}

	if _, err := rpcCall(srv.URL, "bogus", nil); err == nil {
		t.Fatal("expected error for unknown method")
	}
}

func TestTxfloodRpcCallTransportError(t *testing.T) {
	if _, err := rpcCall("http://127.0.0.1:1", "eth_blockNumber", nil); err == nil {
		t.Fatal("expected transport error for unreachable server")
	}
}

func TestTxfloodGetNonceAt(t *testing.T) {
	srv := txfloodRPCServer(t, func(method string) (interface{}, string) {
		if method == "eth_getTransactionCount" {
			return "0x7", ""
		}
		return nil, "bad method"
	})
	defer srv.Close()

	var addr types.Address
	n, err := getNonceAt(srv.URL, addr, "pending")
	if err != nil {
		t.Fatalf("getNonceAt error: %v", err)
	}
	if n != 7 {
		t.Fatalf("nonce = %d, want 7", n)
	}

	n2, err := getNonce(srv.URL, addr)
	if err != nil || n2 != 7 {
		t.Fatalf("getNonce = %d, %v", n2, err)
	}
}

func TestTxfloodGetNonceAtRetriesThenFails(t *testing.T) {
	srv := txfloodRPCServer(t, func(method string) (interface{}, string) {
		return nil, "boom"
	})
	defer srv.Close()

	var addr types.Address
	if _, err := getNonceAt(srv.URL, addr, "pending"); err == nil {
		t.Fatal("expected error after exhausting retries")
	}
}

func TestTxfloodFaucetNonce(t *testing.T) {
	srvHigh := txfloodRPCServer(t, func(method string) (interface{}, string) {
		return "0x9", ""
	})
	defer srvHigh.Close()
	srvLow := txfloodRPCServer(t, func(method string) (interface{}, string) {
		return "0x2", ""
	})
	defer srvLow.Close()
	srvErr := txfloodRPCServer(t, func(method string) (interface{}, string) {
		return nil, "down"
	})
	defer srvErr.Close()

	var addr types.Address
	best, err := faucetNonce([]string{srvLow.URL, srvHigh.URL, srvErr.URL}, addr)
	if err != nil {
		t.Fatalf("faucetNonce error: %v", err)
	}
	if best != 9 {
		t.Fatalf("faucetNonce = %d, want 9", best)
	}

	if _, err := faucetNonce([]string{srvErr.URL}, addr); err == nil {
		t.Fatal("expected error when every node fails")
	}
}

func TestTxfloodGetBalanceAt(t *testing.T) {
	srv := txfloodRPCServer(t, func(method string) (interface{}, string) {
		return "0x64", ""
	})
	defer srv.Close()

	var addr types.Address
	bal, err := getBalanceAt(srv.URL, addr, "latest")
	if err != nil {
		t.Fatalf("getBalanceAt error: %v", err)
	}
	if bal.Int64() != 100 {
		t.Fatalf("balance = %s, want 100", bal.String())
	}
}

func TestTxfloodGetBalanceAtInvalidHex(t *testing.T) {
	first := true
	srv := txfloodRPCServer(t, func(method string) (interface{}, string) {
		if first {
			first = false
			return "not-hex-quantity", ""
		}
		return "0x1", ""
	})
	defer srv.Close()

	var addr types.Address
	bal, err := getBalanceAt(srv.URL, addr, "latest")
	if err != nil {
		t.Fatalf("expected recovery on retry, got error: %v", err)
	}
	if bal.Int64() != 1 {
		t.Fatalf("balance = %s, want 1", bal.String())
	}
}

func TestTxfloodPoolDepth(t *testing.T) {
	mkSrv := func(pending string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				ID     int    `json:"id"`
				Method string `json:"method"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			resp := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  map[string]string{"pending": pending, "queued": "0x0"},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}))
	}
	srvA := mkSrv("0x10")
	defer srvA.Close()
	srvB := mkSrv("0x20")
	defer srvB.Close()

	depth, err := poolDepth([]string{srvA.URL, srvB.URL})
	if err != nil {
		t.Fatalf("poolDepth error: %v", err)
	}
	if depth != 32 {
		t.Fatalf("poolDepth = %d, want 32 (0x20)", depth)
	}
}

func TestTxfloodPoolDepthAllFail(t *testing.T) {
	if _, err := poolDepth([]string{"http://127.0.0.1:1"}); err == nil {
		t.Fatal("expected error when no node answers")
	}
}

func TestTxfloodRpcBatchNonces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqs []struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&reqs)
		out := make([]map[string]interface{}, 0, len(reqs))
		for _, req := range reqs {
			if req.ID == 1 {
				out = append(out, map[string]interface{}{"id": req.ID, "error": map[string]string{"message": "nope"}})
				continue
			}
			out = append(out, map[string]interface{}{"id": req.ID, "result": fmt.Sprintf("0x%x", req.ID*2)})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()

	addrs := make([]types.Address, 4)
	res, err := rpcBatchNonces(srv.URL, addrs, []int{0, 1, 2, 3}, "pending")
	if err != nil {
		t.Fatalf("rpcBatchNonces error: %v", err)
	}
	if _, ok := res[1]; ok {
		t.Fatal("index 1 should have been dropped due to error")
	}
	if res[2] != 4 {
		t.Fatalf("res[2] = %d, want 4", res[2])
	}
	if res[3] != 6 {
		t.Fatalf("res[3] = %d, want 6", res[3])
	}
}

func TestTxfloodRpcBatchNoncesEmpty(t *testing.T) {
	res, err := rpcBatchNonces("http://unused", nil, nil, "pending")
	if err != nil || res != nil {
		t.Fatalf("expected nil,nil for empty sample, got %v, %v", res, err)
	}
}

func TestTxfloodIsPoolBackpressureVariants(t *testing.T) {
	if !strings.Contains("txpool is full", "txpool is full") {
		t.Fatal("sanity")
	}
	if !isPoolBackpressure("above high water mark") {
		t.Fatal("expected backpressure true for high water message")
	}
	if isPoolBackpressure("nonce too low") {
		t.Fatal("expected false for unrelated error")
	}
}
