// Command txflood is a throwaway load generator for the local qs fleet. Two modes:
//   - single faucet (default): sequential-nonce transfers from the dev faucet key.
//   - multi-sender (-senders N): funds N derived accounts from the faucet, then floods
//     transfers from ALL of them in parallel — removes the single-nonce serialization
//     so every rotating proposer can fill a block from many independent senders.
//
// Pre-signs the whole batch first (bounded workers) so the flood loop does NO CGO.
// NOT for production.
package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"runtime/debug"
	"strconv"
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
	Timeout:   10 * time.Second,
	Transport: &http.Transport{MaxIdleConns: 512, MaxIdleConnsPerHost: 128, MaxConnsPerHost: 128, IdleConnTimeout: 30 * time.Second},
}

// poolDepth reports how much EXECUTABLE work is waiting, as the largest
// single-node pending count.
//
// Queued transactions are excluded on purpose. A queued transaction is one
// whose nonce is ahead of its account, so the chain cannot include it and it
// is not work the pool can deliver. Counting it made the loop read a pool as
// full when it had nothing to give: a stale queue of 118,530 transactions,
// stranded behind a one-nonce hole and surviving restarts because the pool is
// persisted, held the measured depth at ~139,000 and the loop injected nothing
// for the whole run. Throughput read 39 TPS on a chain that was idle.
//
// Summing was wrong: gossip replicates every transaction to every peer, so
// once propagation settles each of the seven nodes holds the same set and the
// sum reads about 7x the real depth. A closed loop fed that number believes
// the pool is full when it is nearly empty and stops injecting.
func poolDepth(urls []string) (int, error) {
	deepest, ok := 0, 0
	var lastErr error
	for _, u := range urls {
		raw, err := rpcCall(u, "txpool_status", []interface{}{})
		if err != nil {
			lastErr = err
			continue
		}
		ok++
		var st struct {
			Pending string `json:"pending"`
			Queued  string `json:"queued"`
		}
		if json.Unmarshal(raw, &st) != nil {
			continue
		}
		if d := parseHexUint(st.Pending); d > deepest {
			deepest = d
		}
	}
	if ok == 0 {
		// Reporting 0 here would look like an empty pool and make the loop
		// inject its full target every second -- open-loop behaviour wearing
		// closed-loop clothes. The usual cause is the txpool RPC namespace not
		// being enabled on the node (--http.api must include txpool).
		return 0, fmt.Errorf("no node answered txpool_status (is the txpool namespace enabled?): %v", lastErr)
	}
	return deepest, nil
}

func parseHexUint(s string) int {
	s = strings.TrimPrefix(s, "0x")
	n, err := strconv.ParseUint(s, 16, 64)
	if err != nil {
		return 0
	}
	return int(n)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func rpcCall(url, method string, params []interface{}) (json.RawMessage, error) {
	body, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	resp, err := httpClient.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	// Drain to EOF before closing, or the connection is not reused. Decode
	// stops at the end of the first JSON value and leaves whatever follows --
	// even a single newline -- unread, and net/http will not put a body that
	// still has bytes pending back in the idle pool. With keep-alive silently
	// defeated this way every transaction cost a fresh TCP connection: a
	// sustained flood pushed the host to 45,000 sockets in TIME_WAIT, ran the
	// ephemeral port range (49152-65535) dry, and then failed with WinError
	// 10048 -- not only for the load tool, but for the nodes' own outbound P2P
	// connections, which stalled consensus. It capped measured throughput at a
	// number that said more about the client than the chain.
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()
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

// getNonce reads the sender's next nonce, retrying rather than guessing.
//
// It used to return 0 on any error. A sender that has already been used --
// which every derived account is, after the first run against a chain -- then
// got a batch signed from nonce 0: the mined ones came back "nonce too low"
// and everything above the account's real nonce sat in the queue, unexecutable.
// A run could therefore load a million transactions into the pool and have
// blocks come out nearly empty, with nothing in the output saying why. With
// 2000 senders probing at once, a handful of failures is enough to do it.
func getNonce(url string, a types.Address) (uint64, error) {
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		r, err := rpcCall(url, "eth_getTransactionCount", []interface{}{a.Hex(), "pending"})
		if err != nil {
			lastErr = err
			time.Sleep(time.Duration(50*(attempt+1)) * time.Millisecond)
			continue
		}
		var h string
		if err := json.Unmarshal(r, &h); err != nil {
			lastErr = err
			continue
		}
		return hexToU64(h), nil
	}
	return 0, fmt.Errorf("nonce for %s: %w", a.Hex(), lastErr)
}

// senderOffset shifts the derived sender set. Derived accounts accumulate
// nonces across every run against the same chain, and a single transaction
// lost anywhere in that history -- rejected, or dropped from the pool before
// it was mined -- leaves a permanent hole: everything above it stays queued
// and can never be promoted, because promotion needs the account's exact next
// nonce. Observed here as a pool holding 118,530 queued transactions with zero
// pending, and blocks coming out nearly empty. Moving the offset gives a fresh
// account set whose nonces start at zero, which cannot have a hole.
var senderOffset uint64

func deriveKey(i int) *ecdsa.PrivateKey {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], senderOffset+uint64(i)+1)
	seed := crypto.Keccak256([]byte("n42-txflood-sender-v1"), b[:])
	k, _ := crypto.ToECDSA(seed)
	return k
}

var nonceFailures int64

func main() {
	debug.SetMaxThreads(200000)
	key := flag.String("key", "922c1ad85fb8691315b1ae54b39f7111ae3cfb2c36b038740af36844e9673eee", "faucet privkey hex")
	rpcs := flag.String("rpc", "http://127.0.0.1:20012", "comma-separated rpc urls")
	chainID := flag.Int64("chainid", 94, "chain id")
	gasPrice := flag.Uint64("gasprice", 1000000008, "gas price (wei)")
	conc := flag.Int("conc", 48, "concurrent HTTP submitters")
	rate := flag.Int("rate", 0, "submissions per second (0 = as fast as possible)")
	targetDepth := flag.Int("target-depth", 0, "keep this many txs pending in the pool; each second top up only the shortfall (0 = off, requires the txpool RPC namespace)")
	senders := flag.Int("senders", 0, "0=single faucet; N=fund+flood from N derived accounts")
	perTx := flag.Int("pertx", 300, "txs per sender (multi-sender mode)")
	count := flag.Int("count", 80000, "txs to submit (single-faucet mode)")
	broadcast := flag.Bool("broadcast", false, "submit each tx to ALL rpcs")
	offset := flag.Uint64("sender-offset", 0, "shift the derived sender set; use a fresh offset to get accounts with no nonce history")
	shardSenders := flag.Bool("shard-senders", false, "route each sender's txs to one node (sender%rpcs) so every proposer owns full nonce sequences")
	skipFunding := flag.Bool("skip-funding", false, "assume the derived senders are already funded (re-run after a funding round that mined but aborted)")
	rpcBatch := flag.Int("rpcbatch", 0, "submit N txs per eth_batchRawTransaction call (0 = one eth_sendRawTransaction per tx; max 200)")
	flag.Parse()
	senderOffset = *offset

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
		startNonce, err := getNonce(urls[0], from)
		if err != nil {
			fmt.Fprintf(os.Stderr, "faucet nonce: %v\n", err)
			os.Exit(1)
		}
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
		fn, err := getNonce(urls[0], from)
		if err != nil {
			fmt.Fprintf(os.Stderr, "faucet nonce: %v\n", err)
			os.Exit(1)
		}
		fundVal := uint256.NewInt(1)
		fundVal.Mul(uint256.NewInt(uint64(*perTx)+10), uint256.NewInt(21000*(*gasPrice))) // enough for perTx transfers + gas
		fmt.Printf("funding %d senders (nonce %d..), value=%s wei each...\n", *senders, fn, fundVal.Dec())
		for i := 0; !*skipFunding && i < *senders; i++ {
			raw := signOne(priv, from, addrs[i], fn+uint64(i), fundVal, 21000)
			if _, err := rpcCall(urls[0], "eth_sendRawTransaction", []interface{}{raw}); err != nil {
				fmt.Printf("fund %d err: %v\n", i, err)
			}
		}
		// Wait until the last sender is funded (balance > 0), and ABORT if it
		// never happens. The old loop timed out silently and fell through to
		// the flood, which then offered millions of transactions from unfunded
		// senders -- 8.88M rejections at 36k/s, wearing the shape of a node
		// problem. A funding round can genuinely fail wholesale (e.g. right
		// after a fleet-wide restart, before the gossip mesh has re-formed,
		// the funding batch published from one node reaches no one).
		funded := *skipFunding
		if funded {
			fmt.Println("skipping funding (senders assumed already funded)")
		} else {
			fmt.Println("waiting for funding to mine...")
		}
		for w := 0; !funded && w < 40; w++ {
			time.Sleep(2 * time.Second)
			r, _ := rpcCall(urls[0], "eth_getBalance", []interface{}{addrs[*senders-1].Hex(), "latest"})
			var h string
			json.Unmarshal(r, &h)
			if hexToU64(h) > 0 {
				fmt.Printf("funded after %ds (last sender balance=0x%s)\n", (w+1)*2, strings.TrimPrefix(h, "0x"))
				funded = true
				break
			}
		}
		if !funded {
			// The balance probe swallows RPC errors (a busy node reads as
			// balance 0 forty times in a row), so double-check with the faucet
			// nonce before declaring failure: every funding transaction mined
			// means every sender was credited, probe or no probe. This misfired
			// once live — nonce said 900/900 mined, and the abort cost the
			// round its funding.
			faucetNonce, _ := getNonce(urls[0], from)
			if faucetNonce >= fn+uint64(*senders) {
				fmt.Printf("funded (faucet nonce advanced to %d; balance probe was unavailable)\n", faucetNonce)
			} else {
				fmt.Fprintf(os.Stderr, "FATAL: funding did not mine within 80s (faucet nonce %d, expected >= %d); aborting instead of flooding from unfunded senders\n",
					faucetNonce, fn+uint64(*senders))
				os.Exit(1)
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
				base, err := getNonce(urls[s%len(urls)], addrs[s])
				if err != nil {
					atomic.AddInt64(&nonceFailures, 1)
					return // leave this sender's slots empty rather than sign from a guessed nonce
				}
				for j := 0; j < *perTx; j++ {
					raws[s*(*perTx)+j] = signOne(keys[s], addrs[s], dead, base+uint64(j), uint256.NewInt(1), 21000)
				}
			}(s)
		}
		wg.Wait()
	}

	// ---------- flood ----------
	if nf := atomic.LoadInt64(&nonceFailures); nf > 0 {
		fmt.Printf("WARNING: %d senders had no usable nonce and were skipped; their slots are empty\n", nf)
	}
	fmt.Printf("flooding %d txs to %d node(s) (broadcast=%v conc=%d)...\n", len(raws), len(urls), *broadcast, *conc)
	var idx int64 = -1
	var submitted, failed int64
	tf := time.Now()
	var wg sync.WaitGroup

	// Optional paced submission. Firing everything at once makes the pool the
	// thing under test rather than the chain: the backlog overruns the pool's
	// capacity, the excess is rejected, and the measured rate says how fast
	// transactions can be REFUSED. A steady offered rate lets the chain drain
	// at its own pace and keeps the pool at a stable depth. The ticker hands
	// out permits in 10ms batches so a high rate does not spend its time in
	// timer wakeups.
	var permits chan struct{}
	if *targetDepth > 0 {
		// Closed-loop injection. Handing the pool a fixed rate regardless of
		// what it already holds just builds a backlog, and then the measurement
		// reports how fast transactions can be QUEUED rather than how fast the
		// chain drains them -- and once the backlog passes the pool's capacity
		// the surplus is rejected outright. Read the depth each second and top
		// up only the shortfall, so the pool sits at a known level and the
		// offered rate converges on what the chain actually consumes.
		permits = make(chan struct{}, *targetDepth)
		go func() {
			t := time.NewTicker(time.Second)
			defer t.Stop()
			for range t.C {
				depth, err := poolDepth(urls)
				if err != nil {
					fmt.Printf("  !! depth probe failed, refusing to inject blind: %v\n", err)
					continue
				}
				short := *targetDepth - depth
				if *rate > 0 && short > *rate {
					short = *rate // never exceed the requested ceiling
				}
				if *rpcBatch > 1 {
					// One permit lets a batch worker submit rpcBatch txs, so
					// scale the credit or the loop overshoots by that factor.
					short = (short + *rpcBatch - 1) / *rpcBatch
				}
				for i := 0; i < short; i++ {
					select {
					case permits <- struct{}{}:
					default:
					}
				}
				fmt.Printf("  pool=%d topup=%d\n", depth, max(short, 0))
			}
		}()
	} else if *rate > 0 {
		permits = make(chan struct{}, *rate)
		per10ms := *rate / 100
		if per10ms < 1 {
			per10ms = 1
		}
		go func() {
			t := time.NewTicker(10 * time.Millisecond)
			defer t.Stop()
			for range t.C {
				for i := 0; i < per10ms; i++ {
					select {
					case permits <- struct{}{}:
					default: // submitters are behind; do not let credit pile up
					}
				}
			}
		}()
	}
	// Batched submission: claim a contiguous run of pre-signed transactions and
	// hand them to eth_batchRawTransaction in one HTTP round trip. One permit
	// covers the whole run (the top-up loop's shortfall is a transaction count,
	// so a batch worker consumes depth credit at the same rate as a single-tx
	// worker submitting the same number). Runs are split at URL boundaries so
	// shard routing still holds a sender's nonces together on one node.
	if *rpcBatch > 1 {
		bn := int64(*rpcBatch)
		if bn > 200 {
			bn = 200 // API-side MaxBatchSize
		}
		urlFor := func(i int64) string {
			if *shardSenders && *senders > 0 {
				return urls[int(i/int64(*perTx))%len(urls)]
			}
			return urls[int(i)%len(urls)]
		}
		for w := 0; w < *conc; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					if permits != nil {
						<-permits
					}
					// idx starts at -1 and holds the LAST claimed index (the
					// single-tx path does AddInt64(+1) then uses the result), so
					// a bn-sized claim owns [last-bn+1, last].
					last := atomic.AddInt64(&idx, bn)
					start := last - bn + 1
					if start >= int64(len(raws)) {
						return
					}
					end := last + 1
					if end > int64(len(raws)) {
						end = int64(len(raws))
					}
					for lo := start; lo < end; {
						u := urlFor(lo)
						hi := lo + 1
						for hi < end && urlFor(hi) == u {
							hi++
						}
						batch := make([]string, hi-lo)
						copy(batch, raws[lo:hi])
						if _, err := rpcCall(u, "eth_batchRawTransaction", []interface{}{batch}); err != nil {
							if n := atomic.AddInt64(&failed, hi-lo); n <= int64(5*bn) || n%1000000 < bn {
								fmt.Printf("  batch submit err (i=%d n=%d %s): %v\n", lo, hi-lo, u, err)
							}
						} else {
							atomic.AddInt64(&submitted, hi-lo)
						}
						lo = hi
					}
				}
			}()
		}
		wg.Wait()
		el := time.Since(tf)
		fmt.Printf("DONE submitted=%d failed=%d in %s (%.0f tx/s offered)\n", submitted, failed, el.Round(time.Millisecond), float64(len(raws))/el.Seconds())
		return
	}

	for w := 0; w < *conc; w++ {
		wg.Add(1)
		go func(wid int) {
			defer wg.Done()
			for {
				if permits != nil {
					<-permits
				}
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
					// The first few distinct failures are the diagnosis; a
					// counter alone hid an 8.88M-transaction rejection.
					if n := atomic.AddInt64(&failed, 1); n <= 5 || n%1000000 == 0 {
						fmt.Printf("  submit err #%d (i=%d %s): %v\n", n, i, url, err)
					}
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
