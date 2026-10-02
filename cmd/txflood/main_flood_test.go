package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestTxfloodMainSingleFaucetFlood drives main() end to end in single-faucet
// mode (senders=0) against a local httptest JSON-RPC server, via the
// subprocess re-exec pattern -- main calls flag.Parse() on the process's own
// os.Args and would collide with the test binary's flags if run in-process.
func TestTxfloodMainSingleFaucetFlood(t *testing.T) {
	if os.Getenv("TXFLOOD_MAIN_SUBPROCESS") == "1" {
		rpcURL := os.Getenv("TXFLOOD_RPC_URL")
		os.Args = []string{"txflood", "-rpc=" + rpcURL, "-count=2", "-conc=1", "-senders=0"}
		main()
		return
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var result interface{}
		switch req.Method {
		case "eth_getTransactionCount":
			result = "0x0"
		case "eth_sendRawTransaction":
			result = "0xhash"
		default:
			result = "0x0"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer srv.Close()

	cmd := exec.Command(os.Args[0], "-test.run=TestTxfloodMainSingleFaucetFlood")
	cmd.Env = append(os.Environ(),
		"TXFLOOD_MAIN_SUBPROCESS=1",
		fmt.Sprintf("TXFLOOD_RPC_URL=%s", srv.URL),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("subprocess failed: %v (output: %s)", err, out)
	}
	if !strings.Contains(string(out), "DONE submitted=2") {
		t.Fatalf("output = %q, expected a DONE line reporting 2 submitted", out)
	}
}
