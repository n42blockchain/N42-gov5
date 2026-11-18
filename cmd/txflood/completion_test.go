package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Exercise the real CLI without a listener, including a pool that never drains
// after accepting the workload. Excess workers must not await another permit.
type completionTransport struct{ submitted atomic.Bool }

func (tr *completionTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var call struct{ Method string }
	if err := json.NewDecoder(req.Body).Decode(&call); err != nil {
		return nil, err
	}
	result := `"0x0"`
	switch call.Method {
	case "eth_getTransactionCount":
	case "txpool_status":
		pending := 0
		if tr.submitted.Load() {
			pending = 2
		}
		result = fmt.Sprintf(`{"pending":"0x%x","queued":"0x0"}`, pending)
	case "eth_sendRawTransaction", "eth_batchRawTransaction":
		tr.submitted.Store(true)
		result = `[]`
	default:
		return nil, fmt.Errorf("unexpected method %s", call.Method)
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":1,"result":` + result + `}`)), Header: make(http.Header)}, nil
}

func TestTxFloodCompletionHelper(t *testing.T) {
	if os.Getenv("N42_TXFLOOD_COMPLETION_HELPER") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"txflood"}, os.Args[i+1:]...)
			break
		}
	}
	flag.CommandLine = flag.NewFlagSet("txflood", flag.ExitOnError)
	httpClient = &http.Client{Transport: new(completionTransport)}
	main()
}

func TestTxFloodExhaustedWorkersExitWithoutCredit(t *testing.T) {
	for _, mode := range []string{"rate-single", "rate-batch", "depth-single", "depth-batch"} {
		t.Run(mode, func(t *testing.T) {
			args := []string{"-test.run=^TestTxFloodCompletionHelper$", "--", "-rpc", "http://fixture.invalid", "-conc", "4"}
			count := 1
			if strings.HasSuffix(mode, "batch") {
				count = 2
				args = append(args, "-rpcbatch", "2")
			}
			args = append(args, "-count", fmt.Sprint(count))
			if strings.HasPrefix(mode, "rate") {
				args = append(args, "-rate", fmt.Sprint(count))
			} else {
				args = append(args, "-target-depth", fmt.Sprint(count))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], args...)
			cmd.Env = append(os.Environ(), "N42_TXFLOOD_COMPLETION_HELPER=1", "N42_DEV_FAUCET_KEY=")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("CLI failed to finish: %v\n%s", err, out)
			}
			if !strings.Contains(string(out), fmt.Sprintf("DONE submitted=%d failed=0", count)) {
				t.Fatalf("workload did not finish: %s", out)
			}
		})
	}
}
