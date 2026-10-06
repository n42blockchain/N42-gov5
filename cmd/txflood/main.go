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
	"net"
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
		var st struct {
			Pending string `json:"pending"`
			Queued  string `json:"queued"`
		}
		if err := json.Unmarshal(raw, &st); err != nil {
			lastErr = fmt.Errorf("%s: invalid txpool_status: %w", u, err)
			continue
		}
		if !strings.HasPrefix(st.Pending, "0x") {
			lastErr = fmt.Errorf("%s: missing/invalid pending quantity", u)
			continue
		}
		d, err := strconv.ParseUint(st.Pending[2:], 16, strconv.IntSize-1)
		if err != nil {
			lastErr = fmt.Errorf("%s: invalid pending quantity: %w", u, err)
			continue
		}
		ok++
		if int(d) > deepest {
			deepest = int(d)
		}
	}
	if ok == 0 || ok != len(urls) {
		// Reporting 0 here would look like an empty pool and make the loop
		// inject its full target every second -- open-loop behaviour wearing
		// closed-loop clothes. The usual cause is the txpool RPC namespace not
		// being enabled on the node (--http.api must include txpool).
		return 0, fmt.Errorf("valid txpool_status from %d/%d nodes (is the txpool namespace enabled?): %v", ok, len(urls), lastErr)
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

// refillDepthPermits reconciles unsent credit with the latest pool snapshot.
// One credit represents at most batchSize transactions. Queued and in-flight
// requests already own credit: adding the whole shortfall on every poll lets
// slow RPC accumulate an unbounded backlog. A saturated pool revokes queued
// credit; requests already in flight are allowed to finish.
func refillDepthPermits(permits chan struct{}, target, depth, batchSize, inFlight, rate int) int {
	batchSize = max(batchSize, 1)
	shortfall := max(target-depth, 0)
	frames := shortfall / batchSize
	if shortfall%batchSize != 0 {
		frames++
	}
	desired := max(frames-inFlight, 0)
	delta := desired - len(permits)
	if delta > 0 && rate > 0 {
		limit := rate / batchSize
		if rate%batchSize != 0 {
			limit++
		}
		if delta > limit {
			delta = limit
		}
	}
	changed := 0
	for ; delta > 0; delta-- {
		select {
		case permits <- struct{}{}:
			changed++
		default:
			return changed
		}
	}
	for ; delta < 0; delta++ {
		select {
		case <-permits:
			changed--
		default:
			return changed
		}
	}
	return changed
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

func hexToBig(s string) (*big.Int, error) {
	n, ok := new(big.Int).SetString(strings.TrimPrefix(s, "0x"), 16)
	if !ok {
		return nil, fmt.Errorf("invalid hex quantity %q", s)
	}
	return n, nil
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
func getNonceAt(url string, a types.Address, tag string) (uint64, error) {
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		r, err := rpcCall(url, "eth_getTransactionCount", []interface{}{a.Hex(), tag})
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
	return 0, fmt.Errorf("%s nonce for %s: %w", tag, a.Hex(), lastErr)
}

func getNonce(url string, a types.Address) (uint64, error) {
	return getNonceAt(url, a, "pending")
}

func getBalanceAt(url string, a types.Address, tag string) (*big.Int, error) {
	r, err := rpcCall(url, "eth_getBalance", []interface{}{a.Hex(), tag})
	if err != nil {
		return nil, err
	}
	var h string
	if err := json.Unmarshal(r, &h); err != nil {
		return nil, err
	}
	n, err := hexToBig(h)
	if err != nil {
		return nil, fmt.Errorf("invalid %s balance for %s: %w", tag, a.Hex(), err)
	}
	return n, nil
}

func fundingAmounts(senders, perTx int, gasPrice uint64) (*uint256.Int, *big.Int) {
	perTransferGas := new(big.Int).Mul(big.NewInt(21000), new(big.Int).SetUint64(gasPrice))
	fundValBig := new(big.Int).Mul(big.NewInt(int64(perTx+10)), perTransferGas)
	// Besides the value credited to every derived sender, the faucet pays gas
	// for the funding transfer itself.
	perSenderCost := new(big.Int).Add(new(big.Int).Set(fundValBig), perTransferGas)
	totalCost := new(big.Int).Mul(perSenderCost, big.NewInt(int64(senders)))
	return new(uint256.Int).SetBytes(fundValBig.Bytes()), totalCost
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

// deriveRecipient returns the i-th flood recipient. A separate domain from
// deriveKey's, so a recipient can never collide with a sender: the two sets
// touching would silently turn a spread workload back into a partly-shared one.
//
// Only the address is derived, never a key -- these accounts receive and never
// send, so there is nothing to sign for them and no funding to do.
func deriveRecipient(i int) types.Address {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(i))
	h := crypto.Keccak256([]byte("n42-txflood-recipient-v1"), b[:])
	var a types.Address
	copy(a[:], h[12:])
	return a
}

var nonceFailures int64

func main() {
	debug.SetMaxThreads(200000)
	defaultFaucetKey := os.Getenv("N42_DEV_FAUCET_KEY")
	if defaultFaucetKey == "" {
		defaultFaucetKey = "922c1ad85fb8691315b1ae54b39f7111ae3cfb2c36b038740af36844e9673eee"
	}
	key := flag.String("key", "", "faucet privkey hex (default: N42_DEV_FAUCET_KEY or the qs dev faucet)")
	rpcs := flag.String("rpc", "http://127.0.0.1:20012", "comma-separated rpc urls")
	ingest := flag.String("ingest", "", "comma-separated binary ingest host:ports matching the RPC endpoints (funding/depth still use RPC; requires -rpcbatch >= 2)")
	ingestProtocol := flag.String("ingest-protocol", "gov5", "binary ingest wire protocol: gov5 or n42-rs; both send the same signed transfer workload")
	chainID := flag.Int64("chainid", 94, "chain id")
	gasPrice := flag.Uint64("gasprice", 1000000008, "gas price (wei)")
	conc := flag.Int("conc", 48, "concurrent HTTP submitters")
	rate := flag.Int("rate", 0, "submissions per second (0 = as fast as possible)")
	targetDepth := flag.Int("target-depth", 0, "keep this many txs pending in the pool; each second top up only the shortfall (0 = off, requires the txpool RPC namespace)")
	senders := flag.Int("senders", 0, "0=single faucet; N=fund+flood from N derived accounts")
	perTx := flag.Int("pertx", 300, "txs per sender (multi-sender mode)")
	count := flag.Int("count", 80000, "txs to submit (single-faucet mode)")
	broadcast := flag.Bool("broadcast", false, "submit each tx to ALL rpcs")
	stream := flag.Bool("stream", false, "sign transactions on demand in submitters instead of retaining the whole signed workload")
	offset := flag.Uint64("sender-offset", 0, "shift the derived sender set; use a fresh offset to get accounts with no nonce history")
	shardSenders := flag.Bool("shard-senders", false, "route each sender's txs to one node (sender%rpcs) so every proposer owns full nonce sequences")
	// The flood used to pay ONE hard-coded address, so a full 22,857-tx block
	// wrote ~1,201 distinct accounts (the senders, plus the sink) where a block
	// spread over N recipients writes up to N more. That is the difference
	// between measuring a chain's transfer path and measuring its state growth,
	// and it made every cross-client per-transaction comparison in
	// docs/QS_TPS_BENCHMARK.md incomparable. Default 0 keeps the old behaviour
	// so recorded rounds stay reproducible; set it to make the state work real.
	recipients := flag.Int("recipients", 0, "spread transfers over N derived recipients (0 = the single 0x..dEaD sink, the historical behaviour)")
	skipFunding := flag.Bool("skip-funding", false, "assume the derived senders are already funded (re-run after a funding round that mined but aborted)")
	rpcBatch := flag.Int("rpcbatch", 0, "submit N txs per eth_batchRawTransaction call (0 = one eth_sendRawTransaction per tx; max 200)")
	flag.Parse()
	if *key == "" {
		*key = defaultFaucetKey
	}
	// Keep the permit accounting and actual submitted batch size identical.
	// Silently clamping only the worker to 200 underfed -rate/-target-depth
	// when a caller supplied a larger batch (for example the Rust rig's 500).
	if *rpcBatch < 0 || *rpcBatch > 200 {
		fmt.Fprintln(os.Stderr, "rpcbatch must be between 0 and 200 (eth_batchRawTransaction limit)")
		os.Exit(2)
	}
	if *senders < 0 || *perTx <= 0 || *count <= 0 || (*senders > 0 && *perTx > int(^uint(0)>>1) / *senders) {
		fmt.Fprintln(os.Stderr, "senders must be non-negative; pertx/count must be positive and the total must fit an int")
		os.Exit(2)
	}
	if *targetDepth < 0 || *rate < 0 || *conc <= 0 {
		fmt.Fprintln(os.Stderr, "target-depth/rate must be non-negative and conc must be positive")
		os.Exit(2)
	}
	senderOffset = *offset

	priv, err := crypto.HexToECDSA(strings.TrimPrefix(*key, "0x"))
	if err != nil {
		panic(err)
	}
	from := crypto.PubkeyToAddress(priv.PublicKey)
	urls := strings.Split(*rpcs, ",")
	ingestTargets := make(map[string]string)
	if *ingest != "" {
		addrs := strings.Split(*ingest, ",")
		if len(addrs) != len(urls) || *rpcBatch < 2 {
			fmt.Fprintln(os.Stderr, "ingest requires one address per RPC URL and rpcbatch >= 2")
			os.Exit(2)
		}
		for i, addr := range addrs {
			if _, _, err := net.SplitHostPort(addr); err != nil {
				fmt.Fprintf(os.Stderr, "invalid ingest address: %v\n", err)
				os.Exit(2)
			}
			ingestTargets[urls[i]] = addr
		}
	}
	useIngest := len(ingestTargets) > 0
	if *ingestProtocol != "gov5" && *ingestProtocol != "n42-rs" {
		fmt.Fprintln(os.Stderr, "ingest-protocol must be gov5 or n42-rs")
		os.Exit(2)
	}
	if *ingestProtocol == "n42-rs" && !useIngest {
		fmt.Fprintln(os.Stderr, "n42-rs ingest-protocol requires -ingest")
		os.Exit(2)
	}
	rustIngest := useIngest && *ingestProtocol == "n42-rs"
	nativeEncoding := useIngest && !rustIngest
	signer := transaction.NewLondonSigner(big.NewInt(*chainID))
	dead := types.HexToAddress("0x000000000000000000000000000000000000dEaD")
	sink := "single 0x..dEaD sink"
	if *recipients > 0 {
		sink = fmt.Sprintf("%d derived recipients", *recipients)
	}
	fmt.Printf("faucet=%s chainId=%d rpcs=%d senders=%d recipients=%s\n", from.Hex(), *chainID, len(urls), *senders, sink)

	signOne := func(priv *ecdsa.PrivateKey, from, to types.Address, nonce uint64, value *uint256.Int, gas uint64, native bool) string {
		inner := &transaction.LegacyTx{Nonce: nonce, GasPrice: uint256.NewInt(*gasPrice), Gas: gas, To: &to, Value: value, From: &from}
		signed, _ := transaction.SignTx(transaction.NewTx(inner), signer, priv)
		var raw []byte
		if native {
			raw, _ = signed.Marshal()
		} else {
			raw, _ = transaction.EncodeEthereumTransaction(signed)
		}
		return "0x" + fmt.Sprintf("%x", raw)
	}

	// ---------- build the raw tx list ----------
	var raws []string
	var rawCount int
	var rawAt func(int) string
	var senderAt func(int) types.Address
	if *senders <= 0 {
		startNonce, err := getNonce(urls[0], from)
		if err != nil {
			fmt.Fprintf(os.Stderr, "faucet nonce: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("single-faucet: startNonce=%d, transactions=%d stream=%v...\n", startNonce, *count, *stream)
		rawCount = *count
		senderAt = func(int) types.Address { return from }
		rawAt = func(i int) string {
			return signOne(priv, from, dead, startNonce+uint64(i), uint256.NewInt(1), 21000, nativeEncoding)
		}
		if !*stream {
			raws = make([]string, *count)
			var wg sync.WaitGroup
			sc := make(chan int, 16)
			for i := 0; i < *count; i++ {
				sc <- 1
				wg.Add(1)
				go func(idx int) {
					defer wg.Done()
					defer func() { <-sc }()
					raws[idx] = rawAt(idx)
				}(i)
			}
			wg.Wait()
		}
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
		fundVal, fundingCost := fundingAmounts(*senders, *perTx, *gasPrice)
		fmt.Printf("funding %d senders (nonce %d..), value=%s wei each...\n", *senders, fn, fundVal.Dec())
		if !*skipFunding {
			faucetBalance, err := getBalanceAt(urls[0], from, "latest")
			if err != nil {
				fmt.Fprintf(os.Stderr, "FATAL: faucet balance preflight failed: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("funding preflight: faucet=%s wei required=%s wei\n", faucetBalance, fundingCost)
			if faucetBalance.Cmp(fundingCost) < 0 {
				fmt.Fprintf(os.Stderr, "FATAL: faucet balance is insufficient before funding (have %s wei, need %s wei, deficit %s wei)\n",
					faucetBalance, fundingCost, new(big.Int).Sub(fundingCost, faucetBalance))
				os.Exit(1)
			}
		}
		var lastFundHash string
		for i := 0; !*skipFunding && i < *senders; i++ {
			raw := signOne(priv, from, addrs[i], fn+uint64(i), fundVal, 21000, false)
			r, err := rpcCall(urls[0], "eth_sendRawTransaction", []interface{}{raw})
			if err != nil {
				fmt.Printf("fund %d err: %v\n", i, err)
				continue
			}
			if i == *senders-1 {
				_ = json.Unmarshal(r, &lastFundHash)
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
			// A mined transaction advances the faucet nonce even when execution
			// fails (for example after an earlier round exhausted the faucet).
			// Check the receipt instead of treating nonce advancement as proof
			// that the sender was credited.
			if lastFundHash != "" {
				r, _ = rpcCall(urls[0], "eth_getTransactionReceipt", []interface{}{lastFundHash})
				var receipt struct {
					Status string `json:"status"`
				}
				if json.Unmarshal(r, &receipt) == nil && receipt.Status != "" {
					if hexToU64(receipt.Status) == 1 {
						fmt.Printf("funded after %ds (last funding receipt succeeded)\n", (w+1)*2)
						funded = true
						break
					}
					fmt.Fprintln(os.Stderr, "FATAL: last funding transaction reverted; faucet balance is insufficient")
					os.Exit(1)
				}
			}
		}
		if !funded {
			latestNonce, latestErr := getNonceAt(urls[0], from, "latest")
			pendingNonce, pendingErr := getNonceAt(urls[0], from, "pending")
			fmt.Fprintf(os.Stderr, "FATAL: funding was not confirmed within 80s (latest nonce %d err=%v; pending nonce %d err=%v; expected latest >= %d)\n",
				latestNonce, latestErr, pendingNonce, pendingErr, fn+uint64(*senders))
			os.Exit(1)
		}
		// pre-sign perTx transfers from each sender
		total := *senders * *perTx
		fmt.Printf("preparing %d txs (%d senders x %d), stream=%v...\n", total, *senders, *perTx, *stream)
		rawCount = total
		senderAt = func(i int) types.Address { return addrs[i / *perTx] }
		baseNonces := make([]uint64, *senders)
		rawAt = func(i int) string {
			s, j := i / *perTx, i%*perTx
			to := dead
			if *recipients > 0 {
				to = deriveRecipient(i % *recipients)
			}
			return signOne(keys[s], addrs[s], to, baseNonces[s]+uint64(j), uint256.NewInt(1), 21000, nativeEncoding)
		}
		if !*stream {
			raws = make([]string, total)
		}
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
				baseNonces[s] = base
				if !*stream {
					for j := 0; j < *perTx; j++ {
						raws[s*(*perTx)+j] = rawAt(s*(*perTx) + j)
					}
				}
			}(s)
		}
		wg.Wait()
	}

	// All nonce reads and optional pre-signing have completed before workers
	// call rawAt. Stream mode keeps only a worker batch alive, including while
	// broadcasting it: every endpoint receives the exact same signed bytes.
	if !*stream {
		rawAt = func(i int) string { return raws[i] }
	}

	// ---------- flood ----------
	if nf := atomic.LoadInt64(&nonceFailures); nf > 0 {
		fmt.Fprintf(os.Stderr, "FATAL: %d senders had no usable nonce after retries; refusing to submit a partial benchmark load\n", nf)
		os.Exit(1)
	}
	fmt.Printf("flooding %d txs to %d node(s) (broadcast=%v conc=%d)...\n", rawCount, len(urls), *broadcast, *conc)
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
	var inFlight atomic.Int64
	if *targetDepth > 0 {
		// Closed-loop injection. Handing the pool a fixed rate regardless of
		// what it already holds just builds a backlog, and then the measurement
		// reports how fast transactions can be QUEUED rather than how fast the
		// chain drains them -- and once the backlog passes the pool's capacity
		// the surplus is rejected outright. Read the depth each second and top
		// up only the shortfall, so the pool sits at a known level and the
		// offered rate converges on what the chain actually consumes.
		batchSize := max(*rpcBatch, 1)
		frames := *targetDepth/batchSize + 1
		permits = make(chan struct{}, frames)
		go func() {
			t := time.NewTicker(time.Second)
			defer t.Stop()
			for range t.C {
				depth, err := poolDepth(urls)
				if err != nil {
					refillDepthPermits(permits, 0, 0, batchSize, int(inFlight.Load()), 0)
					fmt.Printf("  !! depth probe failed, refusing to inject blind: %v\n", err)
					continue
				}
				active := int(inFlight.Load())
				change := refillDepthPermits(permits, *targetDepth, depth, batchSize, active, *rate)
				fmt.Printf("  pool=%d credit_delta=%d queued=%d in_flight=%d\n", depth, change, len(permits), active)
			}
		}()
	} else if *rate > 0 {
		permits = make(chan struct{}, *rate)
		per10ms := *rate / 100
		// A permit releases a WHOLE batch on the batched path below, so issuing
		// rate/100 of them per 10 ms offers rate*rpcBatch transactions a second
		// -- at the rig's -rpcbatch 200 that is a 200x ceiling, i.e. none at
		// all. The closed-loop branch above already divides its shortfall by
		// rpcBatch for exactly this reason; -rate has to as well, or the flag
		// silently does nothing and the round reports a supply collapse
		// (12M transactions offered in 80 s, then three windows draining a
		// 300k-slot pool) as though it were the chain slowing down.
		if *rpcBatch > 1 {
			per10ms = (per10ms + *rpcBatch - 1) / *rpcBatch
		}
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
				clients := make(map[string]*ingestClient)
				defer func() {
					for _, client := range clients {
						client.close()
					}
				}()
				for {
					if permits != nil {
						<-permits
						inFlight.Add(1)
					}
					// idx starts at -1 and holds the LAST claimed index (the
					// single-tx path does AddInt64(+1) then uses the result), so
					// a bn-sized claim owns [last-bn+1, last].
					last := atomic.AddInt64(&idx, bn)
					start := last - bn + 1
					if start >= int64(rawCount) {
						if permits != nil {
							inFlight.Add(-1)
						}
						return
					}
					end := last + 1
					if end > int64(rawCount) {
						end = int64(rawCount)
					}
					for lo := start; lo < end; {
						u := urlFor(lo)
						hi := lo + 1
						for hi < end && urlFor(hi) == u {
							hi++
						}
						targets := []string{u}
						if *broadcast {
							// A broadcast frame owns the whole claimed batch. The
							// per-index URL split otherwise degenerates to singles
							// and silently ignores -broadcast in this branch.
							hi = end
							targets = urls
						}
						batch := make([]string, hi-lo)
						for i := range batch {
							batch[i] = rawAt(int(lo) + i)
						}
						var frame []byte
						if useIngest {
							claims := make([]types.Address, len(batch))
							for i := range claims {
								claims[i] = senderAt(int(lo) + i)
							}
							var err error
							frame, err = encodeIngestProtocolFrame(batch, claims, rustIngest)
							if err != nil {
								fmt.Fprintln(os.Stderr, err)
								os.Exit(1)
							}
						}
						ok := false
						var lastErr error
						for _, target := range targets {
							var err error
							if useIngest {
								client := clients[target]
								if client == nil {
									client = &ingestClient{addr: ingestTargets[target], rust: rustIngest}
									clients[target] = client
								}
								err = client.submit(frame, len(batch))
							} else {
								_, err = rpcCall(target, "eth_batchRawTransaction", []interface{}{batch})
							}
							if err != nil {
								lastErr = fmt.Errorf("%s: %w", target, err)
							} else {
								ok = true
							}
						}
						if !ok {
							if n := atomic.AddInt64(&failed, hi-lo); n <= int64(5*bn) || n%1000000 < bn {
								fmt.Printf("  batch submit err (i=%d n=%d): %v\n", lo, hi-lo, lastErr)
							}
						} else {
							atomic.AddInt64(&submitted, hi-lo)
						}
						lo = hi
					}
					if permits != nil {
						inFlight.Add(-1)
					}
				}
			}()
		}
		wg.Wait()
		el := time.Since(tf)
		fmt.Printf("DONE submitted=%d failed=%d in %s (%.0f tx/s offered)\n", submitted, failed, el.Round(time.Millisecond), float64(rawCount)/el.Seconds())
		return
	}

	for w := 0; w < *conc; w++ {
		wg.Add(1)
		go func(wid int) {
			defer wg.Done()
			for {
				if permits != nil {
					<-permits
					inFlight.Add(1)
				}
				i := atomic.AddInt64(&idx, 1)
				if i >= int64(rawCount) {
					if permits != nil {
						inFlight.Add(-1)
					}
					return
				}
				raw := rawAt(int(i))
				if *broadcast {
					ok := false
					for _, url := range urls {
						if _, err := rpcCall(url, "eth_sendRawTransaction", []interface{}{raw}); err == nil {
							ok = true
						}
					}
					if ok {
						atomic.AddInt64(&submitted, 1)
					} else {
						atomic.AddInt64(&failed, 1)
					}
					if permits != nil {
						inFlight.Add(-1)
					}
					continue
				}
				var url string
				if *shardSenders && *senders > 0 {
					url = urls[int(i/int64(*perTx))%len(urls)] // all of a sender's txs → one node
				} else {
					url = urls[int(i)%len(urls)]
				}
				if _, err := rpcCall(url, "eth_sendRawTransaction", []interface{}{raw}); err != nil {
					// The first few distinct failures are the diagnosis; a
					// counter alone hid an 8.88M-transaction rejection.
					if n := atomic.AddInt64(&failed, 1); n <= 5 || n%1000000 == 0 {
						fmt.Printf("  submit err #%d (i=%d %s): %v\n", n, i, url, err)
					}
				} else {
					atomic.AddInt64(&submitted, 1)
				}
				if permits != nil {
					inFlight.Add(-1)
				}
			}
		}(w)
	}
	wg.Wait()
	el := time.Since(tf)
	fmt.Printf("DONE submitted=%d failed=%d in %s (%.0f tx/s offered)\n", submitted, failed, el.Round(time.Millisecond), float64(rawCount)/el.Seconds())
}
