// Command txflood is a throwaway load generator for the local qs fleet. Two modes:
//   - single faucet (default): sequential-nonce transfers from the dev faucet key.
//   - multi-sender (-senders N): funds N derived accounts from the faucet, then floods
//     transfers from ALL of them in parallel — removes the single-nonce serialization
//     so every rotating proposer can fill a block from many independent senders.
// Pre-signs the whole batch first (bounded workers) so the flood loop does NO CGO.
// NOT for production.
package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"crypto/ecdsa"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

var httpClient = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{MaxIdleConns: 512, MaxIdleConnsPerHost: 128, MaxConnsPerHost: 128, IdleConnTimeout: 30 * time.Second},
}

func rpcCall(url, method string, params []interface{}) (json.RawMessage, error) {
	body, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	resp, err := httpClient.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.Error != nil {
		return nil, fmt.Errorf("%s", out.Error.Message)
	}
	return out.Result, nil
}

func hexToU64(s string) uint64 {
	n := new(big.Int)
	n.SetString(strings.TrimPrefix(s, "0x"), 16)
	return n.Uint64()
}

func getNonce(url string, a types.Address) uint64 {
	r, err := rpcCall(url, "eth_getTransactionCount", []interface{}{a.Hex(), "pending"})
	if err != nil {
		return 0
	}
	var h string
	json.Unmarshal(r, &h)
	return hexToU64(h)
}

func deriveKey(i int) *ecdsa.PrivateKey {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(i)+1)
	seed := crypto.Keccak256([]byte("n42-txflood-sender-v1"), b[:])
	k, _ := crypto.ToECDSA(seed)
	return k
}

func main() {
	debug.SetMaxThreads(200000)
	key := flag.String("key", "922c1ad85fb8691315b1ae54b39f7111ae3cfb2c36b038740af36844e9673eee", "faucet privkey hex")
	rpcs := flag.String("rpc", "http://127.0.0.1:20012", "comma-separated rpc urls")
	chainID := flag.Int64("chainid", 94, "chain id")
	gasPrice := flag.Uint64("gasprice", 1000000008, "gas price (wei)")
	conc := flag.Int("conc", 48, "concurrent HTTP submitters")
	senders := flag.Int("senders", 0, "0=single faucet; N=fund+flood from N derived accounts")
	perTx := flag.Int("pertx", 300, "txs per sender (multi-sender mode)")
	count := flag.Int("count", 80000, "txs to submit (single-faucet mode)")
	broadcast := flag.Bool("broadcast", false, "submit each tx to ALL rpcs")
	shardSenders := flag.Bool("shard-senders", false, "route each sender's txs to one node (sender%rpcs) so every proposer owns full nonce sequences")
	flag.Parse()

	priv, err := crypto.HexToECDSA(strings.TrimPrefix(*key, "0x"))
	if err != nil {
		panic(err)
	}
	from := crypto.PubkeyToAddress(priv.PublicKey)
	urls := strings.Split(*rpcs, ",")
	signer := transaction.NewLondonSigner(big.NewInt(*chainID))
	dead := types.HexToAddress("0x000000000000000000000000000000000000dEaD")
	fmt.Printf("faucet=%s chainId=%d rpcs=%d senders=%d\n", from.Hex(), *chainID, len(urls), *senders)

	signOne := func(priv *ecdsa.PrivateKey, from, to types.Address, nonce uint64, value *uint256.Int, gas uint64) string {
		inner := &transaction.LegacyTx{Nonce: nonce, GasPrice: uint256.NewInt(*gasPrice), Gas: gas, To: &to, Value: value, From: &from}
		signed, _ := transaction.SignTx(transaction.NewTx(inner), signer, priv)
		raw, _ := transaction.EncodeEthereumTransaction(signed)
		return "0x" + fmt.Sprintf("%x", raw)
	}

	// ---------- build the raw tx list ----------
	var raws []string
	if *senders <= 0 {
		startNonce := getNonce(urls[0], from)
		fmt.Printf("single-faucet: startNonce=%d, pre-signing %d...\n", startNonce, *count)
		raws = make([]string, *count)
		var wg sync.WaitGroup
		sc := make(chan int, 16)
		for i := 0; i < *count; i++ {
			sc <- 1
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				defer func() { <-sc }()
				raws[idx] = signOne(priv, from, dead, startNonce+uint64(idx), uint256.NewInt(1), 21000)
			}(i)
		}
		wg.Wait()
	} else {
		// derive + fund N senders
		keys := make([]*ecdsa.PrivateKey, *senders)
		addrs := make([]types.Address, *senders)
		for i := 0; i < *senders; i++ {
			keys[i] = deriveKey(i)
			addrs[i] = crypto.PubkeyToAddress(keys[i].PublicKey)
		}
		fn := getNonce(urls[0], from)
		fundVal := uint256.NewInt(1)
		fundVal.Mul(uint256.NewInt(uint64(*perTx)+10), uint256.NewInt(21000*(*gasPrice))) // enough for perTx transfers + gas
		fmt.Printf("funding %d senders (nonce %d..), value=%s wei each...\n", *senders, fn, fundVal.Dec())
		for i := 0; i < *senders; i++ {
			raw := signOne(priv, from, addrs[i], fn+uint64(i), fundVal, 21000)
			if _, err := rpcCall(urls[0], "eth_sendRawTransaction", []interface{}{raw}); err != nil {
				fmt.Printf("fund %d err: %v\n", i, err)
			}
		}
		// wait until the last sender is funded (balance > 0)
		fmt.Println("waiting for funding to mine...")
		for w := 0; w < 40; w++ {
			time.Sleep(2 * time.Second)
			r, _ := rpcCall(urls[0], "eth_getBalance", []interface{}{addrs[*senders-1].Hex(), "latest"})
			var h string
			json.Unmarshal(r, &h)
			if hexToU64(h) > 0 {
				fmt.Printf("funded after %ds (last sender balance=0x%s)\n", (w+1)*2, strings.TrimPrefix(h, "0x"))
				break
			}
		}
		// pre-sign perTx transfers from each sender
		total := *senders * *perTx
		fmt.Printf("pre-signing %d txs (%d senders x %d)...\n", total, *senders, *perTx)
		raws = make([]string, total)
		var wg sync.WaitGroup
		sc := make(chan int, 16)
		for s := 0; s < *senders; s++ {
			sc <- 1
			wg.Add(1)
			go func(s int) {
				defer wg.Done()
				defer func() { <-sc }()
				// Start from the sender's CURRENT nonce so re-runs against already-used
				// accounts don't sign a batch of stale (already-mined) nonces.
				base := getNonce(urls[s%len(urls)], addrs[s])
				for j := 0; j < *perTx; j++ {
					raws[s*(*perTx)+j] = signOne(keys[s], addrs[s], dead, base+uint64(j), uint256.NewInt(1), 21000)
				}
			}(s)
		}
		wg.Wait()
	}

	// ---------- flood ----------
	fmt.Printf("flooding %d txs to %d node(s) (broadcast=%v conc=%d)...\n", len(raws), len(urls), *broadcast, *conc)
	var idx int64 = -1
	var submitted, failed int64
	tf := time.Now()
	var wg sync.WaitGroup
	for w := 0; w < *conc; w++ {
		wg.Add(1)
		go func(wid int) {
			defer wg.Done()
			for {
				i := atomic.AddInt64(&idx, 1)
				if i >= int64(len(raws)) {
					return
				}
				if *broadcast {
					ok := false
					for _, url := range urls {
						if _, err := rpcCall(url, "eth_sendRawTransaction", []interface{}{raws[i]}); err == nil {
							ok = true
						}
					}
					if ok {
						atomic.AddInt64(&submitted, 1)
					} else {
						atomic.AddInt64(&failed, 1)
					}
					continue
				}
				var url string
				if *shardSenders && *senders > 0 {
					url = urls[int(i/int64(*perTx))%len(urls)] // all of a sender's txs → one node
				} else {
					url = urls[int(i)%len(urls)]
				}
				if _, err := rpcCall(url, "eth_sendRawTransaction", []interface{}{raws[i]}); err != nil {
					atomic.AddInt64(&failed, 1)
				} else {
					atomic.AddInt64(&submitted, 1)
				}
			}
		}(w)
	}
	wg.Wait()
	el := time.Since(tf)
	fmt.Printf("DONE submitted=%d failed=%d in %s (%.0f tx/s offered)\n", submitted, failed, el.Round(time.Millisecond), float64(len(raws))/el.Seconds())
}
