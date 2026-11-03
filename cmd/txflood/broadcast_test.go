package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTxFloodCLIHelper(t *testing.T) {
	if os.Getenv("N42_TXFLOOD_TEST_HELPER") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"txflood"}, os.Args[i+1:]...)
			break
		}
	}
	flag.CommandLine = flag.NewFlagSet("txflood", flag.ExitOnError)
	main()
	os.Exit(0)
}

func TestBatchBroadcastReachesAllSevenRPCs(t *testing.T) {
	var mu sync.Mutex
	var batches [7][][]string
	urls := make([]string, 7)
	for i := range urls {
		i := i
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				Method string
				Params []json.RawMessage
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
				return
			}
			switch req.Method {
			case "eth_getTransactionCount":
				fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":"0x0"}`)
			case "eth_batchRawTransaction":
				var batch []string
				if len(req.Params) != 1 || json.Unmarshal(req.Params[0], &batch) != nil {
					t.Error("invalid batch parameters")
					return
				}
				mu.Lock()
				batches[i] = append(batches[i], batch)
				mu.Unlock()
				fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":[]}`)
			default:
				t.Errorf("unexpected RPC %s", req.Method)
			}
		}))
		defer server.Close()
		urls[i] = server.URL
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTxFloodCLIHelper$", "--",
		"-rpc", strings.Join(urls, ","), "-count", "3", "-conc", "1", "-rpcbatch", "3", "-broadcast")
	cmd.Env = append(os.Environ(), "N42_TXFLOOD_TEST_HELPER=1", "N42_DEV_FAUCET_KEY=")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("txflood: %v\n%s", err, output)
	}
	mu.Lock()
	defer mu.Unlock()
	for i := range batches {
		if len(batches[i]) != 1 || len(batches[i][0]) != 3 {
			t.Fatalf("node %d received %v batches; want one complete 3-tx broadcast", i, len(batches[i]))
		}
		if !reflect.DeepEqual(batches[i], batches[0]) {
			t.Fatalf("node %d received a different signed transaction set", i)
		}
	}
}

func TestStreamMatchesPreSignedMultiSenderWorkload(t *testing.T) {
	var mu sync.Mutex
	var received []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string
			Params []json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		switch req.Method {
		case "eth_getTransactionCount":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":"0x7"}`)
		case "eth_batchRawTransaction":
			var batch []string
			if len(req.Params) != 1 || json.Unmarshal(req.Params[0], &batch) != nil {
				t.Error("invalid batch")
				return
			}
			mu.Lock()
			received = append(received, batch...)
			mu.Unlock()
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":[]}`)
		default:
			t.Errorf("unexpected method %s", req.Method)
		}
	}))
	defer server.Close()
	var baseline []string
	for _, stream := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		args := []string{"-test.run=^TestTxFloodCLIHelper$", "--", "-rpc", server.URL, "-senders", "3", "-pertx", "5", "-recipients", "7", "-rpcbatch", "3", "-conc", "1", "-skip-funding"}
		if stream {
			args = append(args, "-stream")
		}
		cmd := exec.CommandContext(ctx, os.Args[0], args...)
		cmd.Env = append(os.Environ(), "N42_TXFLOOD_TEST_HELPER=1", "N42_DEV_FAUCET_KEY=")
		output, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("stream=%v: %v\n%s", stream, err, output)
		}
		mu.Lock()
		got := append([]string(nil), received...)
		received = nil
		mu.Unlock()
		if len(got) != 15 {
			t.Fatalf("stream=%v received %d, want 15", stream, len(got))
		}
		if !stream {
			baseline = got
		} else if !reflect.DeepEqual(got, baseline) {
			t.Fatal("stream changed signed bytes, sender/nonce order, or recipient assignment")
		}
	}
}
