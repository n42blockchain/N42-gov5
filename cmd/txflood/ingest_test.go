package main

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

func TestBinaryBroadcastMatchesRPCWorkload(t *testing.T) {
	for _, protocol := range []string{"gov5", "n42-rs"} {
		t.Run(protocol, func(t *testing.T) { testBinaryBroadcastMatchesRPCWorkload(t, protocol) })
	}
}

func testBinaryBroadcastMatchesRPCWorkload(t *testing.T, protocol string) {
	rust := protocol == "n42-rs"
	var mu sync.Mutex
	var httpTransactions [7][]string
	var nativeTransactions [7][]string
	urls, endpoints := make([]string, 7), make([]string, 7)
	for node := range urls {
		node := node
		rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
					t.Error("invalid HTTP batch")
					return
				}
				mu.Lock()
				httpTransactions[node] = append(httpTransactions[node], batch...)
				mu.Unlock()
				fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":[]}`)
			default:
				t.Errorf("unexpected RPC %s", req.Method)
			}
		}))
		defer rpc.Close()
		urls[node] = rpc.URL
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		endpoints[node] = listener.Addr().String()
		go func() {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
			for {
				var header [4]byte
				if _, err := io.ReadFull(conn, header[:]); err != nil {
					if err != io.EOF {
						t.Error(err)
					}
					return
				}
				count := binary.LittleEndian.Uint32(header[:])
				if count > 5 {
					t.Error("oversized frame")
					return
				}
				for i := uint32(0); i < count; i++ {
					var size [4]byte
					sizeBytes := 2
					if rust {
						sizeBytes = 4
					}
					if _, err := io.ReadFull(conn, size[:sizeBytes]); err != nil {
						t.Error(err)
						return
					}
					raw := make([]byte, binary.LittleEndian.Uint32(size[:]))
					if _, err := io.ReadFull(conn, raw); err != nil {
						t.Error(err)
						return
					}
					var claim types.Address
					if !rust {
						if _, err := io.ReadFull(conn, claim[:]); err != nil {
							t.Error(err)
							return
						}
					}
					txn := new(transaction.Transaction)
					var err error
					if rust {
						txn, err = transaction.DecodeEthereumTransaction(raw)
					} else {
						err = txn.Unmarshal(raw)
					}
					if err != nil {
						t.Error(err)
						return
					}
					from, err := transaction.RecoverSenderFromSig(transaction.NewLondonSigner(big.NewInt(94)), txn)
					if err != nil || (!rust && (from != claim || txn.From() == nil || *txn.From() != claim)) {
						t.Errorf("bad sender claim: %v", err)
						return
					}
					eth, err := transaction.EncodeEthereumTransaction(txn)
					if err != nil {
						t.Error(err)
						return
					}
					mu.Lock()
					nativeTransactions[node] = append(nativeTransactions[node], "0x"+hex.EncodeToString(eth))
					mu.Unlock()
				}
				reply := header[:]
				if rust {
					// Nonzero depth must be consumed, not read as the next ACK.
					reply = binary.LittleEndian.AppendUint32(reply, 123456)
				}
				if _, err := conn.Write(reply); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	for _, native := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		args := []string{"-test.run=^TestTxFloodCLIHelper$", "--", "-rpc", strings.Join(urls, ","), "-senders", "3", "-pertx", "4", "-recipients", "7", "-rpcbatch", "5", "-conc", "1", "-skip-funding", "-stream", "-broadcast"}
		if native {
			args = append(args, "-ingest", strings.Join(endpoints, ","), "-ingest-protocol", protocol)
		}
		cmd := exec.CommandContext(ctx, os.Args[0], args...)
		cmd.Env = append(os.Environ(), "N42_TXFLOOD_TEST_HELPER=1", "N42_DEV_FAUCET_KEY=")
		output, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("native=%v: %v\n%s", native, err, output)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for node := range urls {
		if len(nativeTransactions[node]) != 12 || !reflect.DeepEqual(nativeTransactions[node], httpTransactions[node]) || !reflect.DeepEqual(nativeTransactions[node], nativeTransactions[0]) {
			t.Fatalf("node %d did not receive the identical 12 signed transactions", node)
		}
	}
}

func TestIngestClientRejectsPartialAcknowledgement(t *testing.T) {
	for _, rust := range []bool{false, true} {
		t.Run(fmt.Sprint(rust), func(t *testing.T) {
			client, server := net.Pipe()
			defer server.Close()
			c := &ingestClient{conn: client, rust: rust}
			defer c.close()
			go func() {
				var request [4]byte
				_, _ = io.ReadFull(server, request[:])
				reply := binary.LittleEndian.AppendUint32(nil, 1)
				if rust {
					reply = binary.LittleEndian.AppendUint32(reply, 123456)
				}
				_, _ = server.Write(reply)
			}()
			if err := c.submit(make([]byte, 4), 2); err == nil {
				t.Fatal("partial acknowledgement counted as a full batch")
			}
			if c.conn != nil {
				t.Fatal("uncertain stream retained")
			}
		})
	}
}
