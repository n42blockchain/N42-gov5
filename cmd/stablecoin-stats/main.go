// stablecoin-stats: measure how much of Ethereum mainnet's recent traffic is
// USDT/USDC, and how much of that is a plain token transfer a native fast path
// could execute without the EVM.
//
// Reads geth ancient headers, bodies and receipts read-only. For every
// transaction in the window it classifies:
//
//   - top-level: tx.To is a tracked stablecoin, broken down by selector
//   - pure transfer: transfer(address,uint256) with exactly 68 bytes of
//     calldata, zero value, success, and exactly one log -- the token's own
//     Transfer event. This is the shape a native handler would replace.
//   - touch: the receipt carries a Transfer event emitted by the token, so the
//     token moved somewhere inside the call tree (routers, wallets, bridges)
//
// It also estimates execution gas (gasUsed minus intrinsic gas) as a proxy for
// interpreter work, and measures how often pure transfers in the same block
// share an account, which bounds how freely a native path could parallelise.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/crypto"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

type tokenDef struct {
	Name string
	Addr types.Address
}

var tokens = []tokenDef{
	{"USDT", types.HexToAddress("0xdAC17F958D2ee523a2206206994597C13D831ec7")},
	{"USDC", types.HexToAddress("0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48")},
	{"DAI", types.HexToAddress("0x6B175474E89094C44Da98b954EedeAC495271d0F")},
	{"USDe", types.HexToAddress("0x4c9EDD5852cd905f086C759E8383e09bff1E68B3")},
	{"USDS", types.HexToAddress("0xdC035D45d973E3EC169d2276DDab16f1e407384F")},
	{"PYUSD", types.HexToAddress("0x6c3ea9036406852006290770BEdFcAbA0e23A0e8")},
	{"FDUSD", types.HexToAddress("0xc5f0f7b66764F6ec8C8Dff7BA683102295E16409")},
}

const (
	idxUSDT = 0
	idxUSDC = 1
	nTok    = 7
)

var (
	selTransfer     = [4]byte{0xa9, 0x05, 0x9c, 0xbb}
	selTransferFrom = [4]byte{0x23, 0xb8, 0x72, 0xdd}
	selApprove      = [4]byte{0x09, 0x5e, 0xa7, 0xb3}
	transferTopic   types.Hash
	tokenIndex      = map[types.Address]int{}
)

// gasHistEdges are the upper bounds of the pure-transfer gasUsed buckets. The
// spread is mostly SSTORE pricing: a recipient going from zero balance costs
// far more than one that already holds the token.
var gasHistEdges = []uint64{35_000, 45_000, 55_000, 65_000, 80_000}

type tokenStats struct {
	TopTx           uint64
	TopGas          uint64
	TopFailed       uint64
	SelTransfer     uint64
	SelTransferFrom uint64
	SelApprove      uint64
	SelOther        uint64
	PureTx          uint64
	PureGas         uint64
	PureExecGas     uint64
	TouchTx         uint64
	TouchGas        uint64
	Events          uint64
	PureEvents      uint64
}

type monthStats struct {
	Blocks   uint64
	Txs      uint64
	Gas      uint64
	UTopTx   uint64
	UPureTx  uint64
	UTouchTx uint64
}

type stats struct {
	Blocks  uint64
	Txs     uint64
	Gas     uint64
	ExecGas uint64
	Tok     [nTok]tokenStats

	// USDT or USDC, counted once per transaction.
	UTopTx       uint64
	UTopGas      uint64
	UTopExecGas  uint64
	UPureTx      uint64
	UPureGas     uint64
	UPureExecGas uint64
	UTouchTx     uint64
	UTouchGas    uint64
	UTouchExec   uint64
	AnyTouchTx   uint64 // any tracked stablecoin
	AnyTouchGas  uint64

	PureTypes      [5]uint64
	PureGasHist    [6]uint64
	AmountHist     [6]uint64 // USD units: 0, <1, <10, <1k, <100k, >=100k
	BlocksWithPure uint64
	PureConflictTx uint64
	// PureSameSender: shares a sender with another pure transfer in the block.
	// Those already run in nonce order, so they cost a native path nothing extra.
	PureSameSender uint64
	// PureCrossConflict: shares an account across different senders -- a
	// common recipient, or a recipient that also sends in the same block.
	// This is what actually limits parallel execution.
	PureCrossConflict uint64
	MaxPureInBlock    uint64

	Monthly map[string]*monthStats
	Errors  uint64
}

func newStats() *stats { return &stats{Monthly: map[string]*monthStats{}} }

func (s *stats) merge(o *stats) {
	s.Blocks += o.Blocks
	s.Txs += o.Txs
	s.Gas += o.Gas
	s.ExecGas += o.ExecGas
	for i := range s.Tok {
		a, b := &s.Tok[i], &o.Tok[i]
		a.TopTx += b.TopTx
		a.TopGas += b.TopGas
		a.TopFailed += b.TopFailed
		a.SelTransfer += b.SelTransfer
		a.SelTransferFrom += b.SelTransferFrom
		a.SelApprove += b.SelApprove
		a.SelOther += b.SelOther
		a.PureTx += b.PureTx
		a.PureGas += b.PureGas
		a.PureExecGas += b.PureExecGas
		a.TouchTx += b.TouchTx
		a.TouchGas += b.TouchGas
		a.Events += b.Events
		a.PureEvents += b.PureEvents
	}
	s.UTopTx += o.UTopTx
	s.UTopGas += o.UTopGas
	s.UTopExecGas += o.UTopExecGas
	s.UPureTx += o.UPureTx
	s.UPureGas += o.UPureGas
	s.UPureExecGas += o.UPureExecGas
	s.UTouchTx += o.UTouchTx
	s.UTouchGas += o.UTouchGas
	s.UTouchExec += o.UTouchExec
	s.AnyTouchTx += o.AnyTouchTx
	s.AnyTouchGas += o.AnyTouchGas
	for i := range s.PureTypes {
		s.PureTypes[i] += o.PureTypes[i]
	}
	for i := range s.PureGasHist {
		s.PureGasHist[i] += o.PureGasHist[i]
	}
	for i := range s.AmountHist {
		s.AmountHist[i] += o.AmountHist[i]
	}
	s.BlocksWithPure += o.BlocksWithPure
	s.PureConflictTx += o.PureConflictTx
	s.PureSameSender += o.PureSameSender
	s.PureCrossConflict += o.PureCrossConflict
	if o.MaxPureInBlock > s.MaxPureInBlock {
		s.MaxPureInBlock = o.MaxPureInBlock
	}
	for k, m := range o.Monthly {
		d := s.Monthly[k]
		if d == nil {
			d = &monthStats{}
			s.Monthly[k] = d
		}
		d.Blocks += m.Blocks
		d.Txs += m.Txs
		d.Gas += m.Gas
		d.UTopTx += m.UTopTx
		d.UPureTx += m.UPureTx
		d.UTouchTx += m.UTouchTx
	}
	s.Errors += o.Errors
}

// intrinsicGas approximates the pre-execution charge. It ignores the EIP-7623
// calldata floor, which only binds for calldata-heavy transactions and never
// for a 68-byte transfer.
func intrinsicGas(create bool, data []byte, alAddrs, alSlots, auths int) uint64 {
	g := uint64(21_000)
	if create {
		g += 32_000 + 2*uint64((len(data)+31)/32)
	}
	for _, b := range data {
		if b == 0 {
			g += 4
		} else {
			g += 16
		}
	}
	g += uint64(alAddrs)*2_400 + uint64(alSlots)*1_900 + uint64(auths)*25_000
	return g
}

type pureKey [21]byte

func addrKey(tok int, topic types.Hash) pureKey {
	var k pureKey
	k[0] = byte(tok)
	copy(k[1:], topic[12:])
	return k
}

func processBlock(fz *freezer.Freezer, n uint64, s *stats) error {
	hdrRaw, err := fz.Ancient(freezer.TableHeaders, n)
	if err != nil {
		return fmt.Errorf("header: %w", err)
	}
	hdr, err := ethel.DecodeGethHeader(hdrRaw)
	if err != nil {
		return fmt.Errorf("decode header: %w", err)
	}
	bodyRaw, err := fz.Ancient(freezer.TableBodies, n)
	if err != nil {
		return fmt.Errorf("body: %w", err)
	}
	body, err := ethel.DecodeGethBody(bodyRaw)
	if err != nil {
		return fmt.Errorf("decode body: %w", err)
	}
	rcRaw, err := fz.Ancient(freezer.TableReceipts, n)
	if err != nil {
		return fmt.Errorf("receipts: %w", err)
	}
	receipts, err := ethel.DecodeGethReceipts(rcRaw)
	if err != nil {
		return fmt.Errorf("decode receipts: %w", err)
	}
	if len(receipts) != len(body.Transactions) {
		return fmt.Errorf("receipts %d != txs %d", len(receipts), len(body.Transactions))
	}

	month := time.Unix(int64(hdr.Time), 0).UTC().Format("2006-01")
	ms := s.Monthly[month]
	if ms == nil {
		ms = &monthStats{}
		s.Monthly[month] = ms
	}
	s.Blocks++
	ms.Blocks++

	type pureTx struct{ from, to pureKey }
	var pures []pureTx

	var prevCum uint64
	for i, tx := range body.Transactions {
		r := receipts[i]
		gasUsed := r.CumulativeGasUsed - prevCum
		prevCum = r.CumulativeGasUsed

		to := tx.To()
		data := tx.Data()
		al := tx.AccessList()
		alSlots := 0
		for _, t := range al {
			alSlots += len(t.StorageKeys)
		}
		intr := intrinsicGas(to == nil, data, len(al), alSlots, len(tx.AuthList()))
		var exec uint64
		if gasUsed > intr {
			exec = gasUsed - intr
		}

		s.Txs++
		s.Gas += gasUsed
		s.ExecGas += exec
		ms.Txs++
		ms.Gas += gasUsed

		// Transfer events per tracked token anywhere in this tx.
		var touched [nTok]uint64
		for _, l := range r.Logs {
			if len(l.Topics) != 3 || l.Topics[0] != transferTopic {
				continue
			}
			if ti, ok := tokenIndex[l.Address]; ok {
				touched[ti]++
			}
		}
		anyTouch, uTouch := false, false
		for ti, c := range touched {
			if c == 0 {
				continue
			}
			ts := &s.Tok[ti]
			ts.TouchTx++
			ts.TouchGas += gasUsed
			ts.Events += c
			anyTouch = true
			if ti == idxUSDT || ti == idxUSDC {
				uTouch = true
			}
		}
		if anyTouch {
			s.AnyTouchTx++
			s.AnyTouchGas += gasUsed
		}
		if uTouch {
			s.UTouchTx++
			s.UTouchGas += gasUsed
			s.UTouchExec += exec
			ms.UTouchTx++
		}

		if to == nil {
			continue
		}
		ti, ok := tokenIndex[*to]
		if !ok {
			continue
		}
		ts := &s.Tok[ti]
		isU := ti == idxUSDT || ti == idxUSDC
		ts.TopTx++
		ts.TopGas += gasUsed
		if isU {
			s.UTopTx++
			s.UTopGas += gasUsed
			s.UTopExecGas += exec
			ms.UTopTx++
		}
		success := r.Status == 1
		if !success {
			ts.TopFailed++
		}
		var sel [4]byte
		if len(data) >= 4 {
			copy(sel[:], data[:4])
		}
		switch sel {
		case selTransfer:
			ts.SelTransfer++
		case selTransferFrom:
			ts.SelTransferFrom++
		case selApprove:
			ts.SelApprove++
		default:
			ts.SelOther++
		}

		pure := sel == selTransfer && len(data) == 68 && success && tx.Value().IsZero() &&
			len(r.Logs) == 1 && r.Logs[0].Address == *to &&
			len(r.Logs[0].Topics) == 3 && r.Logs[0].Topics[0] == transferTopic
		if !pure {
			continue
		}
		ts.PureTx++
		ts.PureGas += gasUsed
		ts.PureExecGas += exec
		ts.PureEvents++
		if !isU {
			continue
		}
		s.UPureTx++
		s.UPureGas += gasUsed
		s.UPureExecGas += exec
		ms.UPureTx++
		if t := tx.Type(); int(t) < len(s.PureTypes) {
			s.PureTypes[t]++
		}
		b := len(gasHistEdges)
		for j, edge := range gasHistEdges {
			if gasUsed <= edge {
				b = j
				break
			}
		}
		s.PureGasHist[b]++
		lg := r.Logs[0]
		s.AmountHist[amountBucket(lg.Data)]++
		pures = append(pures, pureTx{from: addrKey(ti, lg.Topics[1]), to: addrKey(ti, lg.Topics[2])})
	}

	if len(pures) > 0 {
		s.BlocksWithPure++
		if uint64(len(pures)) > s.MaxPureInBlock {
			s.MaxPureInBlock = uint64(len(pures))
		}
		seen := make(map[pureKey]int, 2*len(pures))
		fromCnt := make(map[pureKey]int, len(pures))
		toCnt := make(map[pureKey]int, len(pures))
		for _, p := range pures {
			seen[p.from]++
			if p.to != p.from {
				seen[p.to]++
			}
			fromCnt[p.from]++
			toCnt[p.to]++
		}
		for _, p := range pures {
			if seen[p.from] > 1 || seen[p.to] > 1 {
				s.PureConflictTx++
			}
			if fromCnt[p.from] > 1 {
				s.PureSameSender++
			}
			self := 0
			if p.from == p.to {
				self = 1
			}
			if toCnt[p.to] > 1 || fromCnt[p.to]-self > 0 || toCnt[p.from]-self > 0 {
				s.PureCrossConflict++
			}
		}
	}
	return nil
}

// amountBucket buckets a Transfer amount in whole USD. USDT and USDC both use
// 6 decimals; anything that does not fit in 64 bits lands in the top bucket.
func amountBucket(data []byte) int {
	if len(data) != 32 {
		return 5
	}
	for _, b := range data[:24] {
		if b != 0 {
			return 5
		}
	}
	var v uint64
	for _, b := range data[24:] {
		v = v<<8 | uint64(b)
	}
	switch {
	case v == 0:
		return 0
	case v < 1_000_000:
		return 1
	case v < 10_000_000:
		return 2
	case v < 1_000_000_000:
		return 3
	case v < 100_000_000_000:
		return 4
	default:
		return 5
	}
}

func main() {
	ancient := flag.String("ancient", "d:/geth/geth/chaindata/ancient/chain", "geth ancient chain dir (read-only)")
	days := flag.Int("days", 365, "window length in days, ending at the frozen tip")
	maxBlocks := flag.Uint64("max-blocks", 0, "if >0, scan only this many blocks ending at the tip (smoke test)")
	workers := flag.Int("workers", runtime.NumCPU(), "decode workers")
	out := flag.String("out", "", "write the raw counters as JSON here")
	flag.Parse()

	transferTopic = types.BytesToHash(crypto.Keccak256([]byte("Transfer(address,address,uint256)")))
	for i, t := range tokens {
		tokenIndex[t.Addr] = i
	}

	fz, err := freezer.NewReadOnly(*ancient)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open ancient: %v\n", err)
		os.Exit(1)
	}
	defer fz.Close()

	frozen := fz.Frozen()
	if frozen == 0 {
		fmt.Fprintln(os.Stderr, "empty freezer")
		os.Exit(1)
	}
	end := frozen - 1
	headerTime := func(n uint64) uint64 {
		raw, err := fz.Ancient(freezer.TableHeaders, n)
		if err != nil {
			fmt.Fprintf(os.Stderr, "header %d: %v\n", n, err)
			os.Exit(1)
		}
		h, err := ethel.DecodeGethHeader(raw)
		if err != nil {
			fmt.Fprintf(os.Stderr, "decode header %d: %v\n", n, err)
			os.Exit(1)
		}
		if h.Number.Uint64() != n {
			fmt.Fprintf(os.Stderr, "header %d reports number %d\n", n, h.Number.Uint64())
			os.Exit(1)
		}
		return h.Time
	}
	endTime := headerTime(end)

	var start uint64
	if *maxBlocks > 0 {
		if *maxBlocks <= end {
			start = end - *maxBlocks + 1
		}
	} else {
		target := endTime - uint64(*days)*86_400
		lo, hi := uint64(0), end
		for lo < hi {
			mid := lo + (hi-lo)/2
			if headerTime(mid) < target {
				lo = mid + 1
			} else {
				hi = mid
			}
		}
		start = lo
	}
	startTime := headerTime(start)
	total := end - start + 1
	fmt.Printf("window: blocks %d..%d (%d blocks)\n", start, end, total)
	fmt.Printf("        %s .. %s UTC\n",
		time.Unix(int64(startTime), 0).UTC().Format("2006-01-02 15:04"),
		time.Unix(int64(endTime), 0).UTC().Format("2006-01-02 15:04"))

	jobs := make(chan uint64, 4096)
	results := make([]*stats, *workers)
	var done atomic.Uint64
	var wg sync.WaitGroup
	for w := 0; w < *workers; w++ {
		results[w] = newStats()
		wg.Add(1)
		go func(s *stats) {
			defer wg.Done()
			for n := range jobs {
				if err := processBlock(fz, n, s); err != nil {
					if s.Errors < 5 {
						fmt.Fprintf(os.Stderr, "block %d: %v\n", n, err)
					}
					s.Errors++
				}
				done.Add(1)
			}
		}(results[w])
	}

	t0 := time.Now()
	stop := make(chan struct{})
	go func() {
		tk := time.NewTicker(30 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
				d := done.Load()
				el := time.Since(t0).Seconds()
				rate := float64(d) / el
				eta := time.Duration(float64(total-d)/rate) * time.Second
				fmt.Fprintf(os.Stderr, "progress %d/%d (%.1f%%) %.0f blk/s eta %s\n",
					d, total, 100*float64(d)/float64(total), rate, eta.Truncate(time.Second))
			}
		}
	}()

	for n := start; n <= end; n++ {
		jobs <- n
	}
	close(jobs)
	wg.Wait()
	close(stop)

	agg := newStats()
	for _, r := range results {
		agg.merge(r)
	}
	fmt.Printf("scanned in %s (%.0f blk/s), decode errors %d\n\n",
		time.Since(t0).Truncate(time.Second), float64(total)/time.Since(t0).Seconds(), agg.Errors)

	report(agg)

	if *out != "" {
		payload := map[string]any{
			"startBlock": start, "endBlock": end,
			"startTime": startTime, "endTime": endTime,
			"tokens": tokens, "stats": agg,
		}
		b, _ := json.MarshalIndent(payload, "", "  ")
		if err := os.WriteFile(*out, b, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write %s: %v\n", *out, err)
			os.Exit(1)
		}
		fmt.Printf("\nraw counters written to %s\n", *out)
	}
}

func pct(a, b uint64) float64 {
	if b == 0 {
		return 0
	}
	return 100 * float64(a) / float64(b)
}

func report(s *stats) {
	fmt.Printf("totals: %d txs, %.3f Tgas used, est. execution gas %.1f%% of used\n\n",
		s.Txs, float64(s.Gas)/1e12, pct(s.ExecGas, s.Gas))

	fmt.Println("USDT + USDC (each tx counted once)")
	fmt.Printf("  touch (Transfer event anywhere) %12d txs %6.2f%% | gas %6.2f%%\n", s.UTouchTx, pct(s.UTouchTx, s.Txs), pct(s.UTouchGas, s.Gas))
	fmt.Printf("  top-level call to the token     %12d txs %6.2f%% | gas %6.2f%% | exec gas %6.2f%%\n", s.UTopTx, pct(s.UTopTx, s.Txs), pct(s.UTopGas, s.Gas), pct(s.UTopExecGas, s.ExecGas))
	fmt.Printf("  pure transfer (fast-path shape) %12d txs %6.2f%% | gas %6.2f%% | exec gas %6.2f%%\n", s.UPureTx, pct(s.UPureTx, s.Txs), pct(s.UPureGas, s.Gas), pct(s.UPureExecGas, s.ExecGas))
	if s.UPureTx > 0 {
		fmt.Printf("  pure transfer avg gasUsed %.0f, avg exec gas %.0f\n", float64(s.UPureGas)/float64(s.UPureTx), float64(s.UPureExecGas)/float64(s.UPureTx))
	}
	fmt.Printf("  any tracked stablecoin touch    %12d txs %6.2f%% | gas %6.2f%%\n\n", s.AnyTouchTx, pct(s.AnyTouchTx, s.Txs), pct(s.AnyTouchGas, s.Gas))

	fmt.Println("per token")
	fmt.Printf("  %-6s %12s %12s %8s %12s %12s %12s %10s %10s %10s %10s\n",
		"token", "touchTx", "events", "topTx", "pureTx", "pure/top%", "pureEv/ev%", "transfer", "xferFrom", "approve", "failed")
	for i, t := range tokens {
		ts := s.Tok[i]
		fmt.Printf("  %-6s %12d %12d %8d %12d %11.1f%% %11.1f%% %10d %10d %10d %10d\n",
			t.Name, ts.TouchTx, ts.Events, ts.TopTx, ts.PureTx, pct(ts.PureTx, ts.TopTx), pct(ts.PureEvents, ts.Events),
			ts.SelTransfer, ts.SelTransferFrom, ts.SelApprove, ts.TopFailed)
	}

	fmt.Println("\nUSDT+USDC pure transfers")
	fmt.Printf("  tx type: legacy %.1f%%  2930 %.1f%%  1559 %.1f%%  4844 %.1f%%  7702 %.1f%%\n",
		pct(s.PureTypes[0], s.UPureTx), pct(s.PureTypes[1], s.UPureTx), pct(s.PureTypes[2], s.UPureTx),
		pct(s.PureTypes[3], s.UPureTx), pct(s.PureTypes[4], s.UPureTx))
	fmt.Print("  gasUsed:")
	lo := uint64(0)
	for i, edge := range gasHistEdges {
		fmt.Printf("  %dk-%dk %.1f%%", lo/1000, edge/1000, pct(s.PureGasHist[i], s.UPureTx))
		lo = edge
	}
	fmt.Printf("  >%dk %.1f%%\n", lo/1000, pct(s.PureGasHist[len(gasHistEdges)], s.UPureTx))
	fmt.Printf("  blocks with >=1: %d, max in one block: %d, avg per such block %.1f\n",
		s.BlocksWithPure, s.MaxPureInBlock, float64(s.UPureTx)/float64(max(s.BlocksWithPure, 1)))
	fmt.Printf("  amount (USD): zero %.1f%%  <1 %.1f%%  1-10 %.1f%%  10-1k %.1f%%  1k-100k %.1f%%  >=100k %.1f%%\n",
		pct(s.AmountHist[0], s.UPureTx), pct(s.AmountHist[1], s.UPureTx), pct(s.AmountHist[2], s.UPureTx),
		pct(s.AmountHist[3], s.UPureTx), pct(s.AmountHist[4], s.UPureTx), pct(s.AmountHist[5], s.UPureTx))
	fmt.Printf("  sharing any account with another pure transfer in the block: %.1f%%\n", pct(s.PureConflictTx, s.UPureTx))
	fmt.Printf("    same sender (already nonce-ordered):                     %.1f%%\n", pct(s.PureSameSender, s.UPureTx))
	fmt.Printf("    cross-sender conflict (limits parallelism):              %.1f%%\n", pct(s.PureCrossConflict, s.UPureTx))

	fmt.Println("\nmonthly (USDT+USDC)")
	months := make([]string, 0, len(s.Monthly))
	for k := range s.Monthly {
		months = append(months, k)
	}
	sort.Strings(months)
	fmt.Printf("  %-7s %9s %12s %8s %8s %8s\n", "month", "blocks", "txs", "touch%", "top%", "pure%")
	for _, k := range months {
		m := s.Monthly[k]
		fmt.Printf("  %-7s %9d %12d %7.2f%% %7.2f%% %7.2f%%\n", k, m.Blocks, m.Txs, pct(m.UTouchTx, m.Txs), pct(m.UTopTx, m.Txs), pct(m.UPureTx, m.Txs))
	}
}
