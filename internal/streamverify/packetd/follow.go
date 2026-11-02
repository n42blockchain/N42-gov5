// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package packetd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/n42blockchain/N42/cmd/evmsdk"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/params"
)

// Follower follows a node's head over JSON-RPC and builds a packet for every
// block, in order, never skipping one: a transient failure (the node did not
// answer) retries the same block, a permanent one (see PermanentError) drops
// it with an error and moves on.
type Follower struct {
	RPC    *Client
	Chain  *params.ChainConfig
	Engine consensus.Engine // nil: apos.NewFaker()

	// IngestURL is the node's packet ingest (PUT <url>/mobileverify/packet).
	// Empty: no PUT.
	IngestURL string
	HTTP      *http.Client
	// OutDir, when set, receives <hash>.bin for every packet.
	OutDir string

	// StateAtNumber reads the parent state by number instead of by hash.
	StateAtNumber bool
	Concurrency   int
	// Poll is the head poll interval (eth_blockNumber) while idle; a
	// backlog is processed without sleeping. Default DefaultPoll, floor
	// MinPoll. It bounds how late a packet lands after its block.
	Poll time.Duration

	// Built, when set, is called for every packet handed off (tests).
	Built func(n uint64, b *Block, built *Built, ingest *IngestResult)

	done     map[types.Hash]struct{}
	doneRing []types.Hash
	doneNext int
	hashes   sync.Map // number -> types.Hash, for BLOCKHASH
}

// IngestResult is the node's answer to a PUT.
type IngestResult struct {
	Hash   string `json:"hash"`
	New    bool   `json:"new"`
	Number uint64 `json:"number"`
}

const doneCap = 1024

// DefaultPoll and MinPoll bound Follower.Poll.
const (
	DefaultPoll = 250 * time.Millisecond
	MinPoll     = 50 * time.Millisecond
)

// Run follows from block from (0 = the node's current head) until ctx ends.
func (f *Follower) Run(ctx context.Context, from uint64) error {
	if f.Poll <= 0 {
		f.Poll = DefaultPoll
	}
	if f.Poll < MinPoll {
		f.Poll = MinPoll
	}
	next := from
	if next == 0 {
		head, err := f.waitHead(ctx)
		if err != nil {
			return err
		}
		next = head
	}
	log.Info("packetd: following", "rpc", f.RPC.URL, "from", next, "ingest", f.IngestURL, "out", f.OutDir)
	failures := 0
	for {
		head, err := BlockNumber(ctx, f.RPC)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Warn("packetd: eth_blockNumber failed", "err", err)
		}
		progressed := false
		for err == nil && next <= head {
			perr := f.Process(ctx, next)
			switch {
			case perr == nil:
				failures = 0
				next++
				progressed = true
			case ctx.Err() != nil:
				return ctx.Err()
			case IsPermanent(perr):
				log.Error("packetd: block dropped, no packet", "number", next, "err", perr)
				failures = 0
				next++
				progressed = true
			default:
				failures++
				log.Warn("packetd: block failed, retrying", "number", next, "attempt", failures, "err", perr)
				err = perr // back off one poll before retrying
			}
		}
		if err == nil && progressed {
			// Blocks may have landed while this batch was built: ask again
			// at once, sleep only when caught up (idle) or failing.
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(f.Poll):
		}
	}
}

func (f *Follower) waitHead(ctx context.Context) (uint64, error) {
	for {
		head, err := BlockNumber(ctx, f.RPC)
		if err == nil {
			return head, nil
		}
		log.Warn("packetd: waiting for the node", "err", err)
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(f.Poll):
		}
	}
}

// Process builds and hands off the packet of block n.
func (f *Follower) Process(ctx context.Context, n uint64) error {
	start := time.Now()
	b, err := FetchBlock(ctx, f.RPC, f.Chain, n)
	if err != nil {
		return err
	}
	f.hashes.Store(n, b.Hash)
	if n > 512 {
		f.hashes.Delete(n - 512) // BLOCKHASH reaches 256 back
	}
	if f.isDone(b.Hash) {
		return nil
	}
	if n == 0 {
		return permanent("block 0 has no parent state")
	}
	var at any = BlockHashParam(b.Header.ParentHash)
	if f.StateAtNumber {
		at = hexutil.EncodeUint64(n - 1)
	}
	reader := NewRPCStateReader(ctx, f.RPC, at, f.Concurrency)
	reader.Prefetch(prefetchSet(b))
	hashFn, hashErr := f.hashFn(ctx, b)
	built, err := BuildPacket(f.Chain, f.Engine, b.Header, b.Txs, reader, hashFn)
	if herr := hashErr(); herr != nil {
		return fmt.Errorf("block %d: BLOCKHASH lookup: %w", n, herr)
	}
	if err != nil {
		return err
	}
	enc, err := evmsdk.EncodeStreamPacket(built.Packet)
	if err != nil {
		return permanent("block %d: encode packet: %v", n, err)
	}
	if f.OutDir != "" {
		p := filepath.Join(f.OutDir, b.Hash.Hex()+".bin")
		if err := os.WriteFile(p, enc, 0o644); err != nil {
			return fmt.Errorf("block %d: write %s: %w", n, p, err)
		}
	}
	var res *IngestResult
	if f.IngestURL != "" {
		res, err = f.put(ctx, enc)
		if err != nil {
			return fmt.Errorf("block %d: ingest: %w", n, err)
		}
		if res.Hash != "" && types.HexToHash(res.Hash) != b.Hash {
			return permanent("block %d: ingest answered hash %s, packet is %v", n, res.Hash, b.Hash)
		}
	}
	f.markDone(b.Hash)
	isNew := res != nil && res.New
	log.Info("packetd: packet", "number", n, "hash", b.Hash, "txs", len(b.Txs), "reads", built.Reads,
		"bytecodes", built.Bytecodes, "bytes", len(enc), "src", b.Source, "rpcReqs", f.RPC.Requests(),
		"elapsed", time.Since(start), "new", isNew)
	if f.Built != nil {
		f.Built(n, b, built, res)
	}
	return nil
}

// prefetchSet lists what the block's transactions certainly read: senders,
// recipients, the fee recipient and access-list entries. The cache only
// warms; the execution decides what is read and in which order.
func prefetchSet(b *Block) ([]types.Address, map[types.Address][]types.Hash) {
	addrs := []types.Address{b.Header.Coinbase}
	slots := make(map[types.Address][]types.Hash)
	for _, tx := range b.Txs {
		if from := tx.From(); from != nil {
			addrs = append(addrs, *from)
		}
		if to := tx.To(); to != nil {
			addrs = append(addrs, *to)
		}
		for _, t := range tx.AccessList() {
			addrs = append(addrs, t.Address)
			slots[t.Address] = append(slots[t.Address], t.StorageKeys...)
		}
	}
	return addrs, slots
}

// hashFn answers BLOCKHASH like internal.GetHashFn on a canonical chain:
// zero for n >= the block's number, the parent hash, else the node's
// canonical hash at n. A lookup the node fails is kept and returned by the
// second function (the block is then retried).
func (f *Follower) hashFn(ctx context.Context, b *Block) (func(uint64) types.Hash, func() error) {
	var firstErr error
	num := b.Header.Number.Uint64()
	fn := func(n uint64) types.Hash {
		if n >= num {
			return types.Hash{}
		}
		if n == num-1 {
			return b.Header.ParentHash
		}
		if h, ok := f.hashes.Load(n); ok {
			return h.(types.Hash)
		}
		h, err := BlockHashByNumber(ctx, f.RPC, n)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return types.Hash{}
		}
		f.hashes.Store(n, h)
		return h
	}
	return fn, func() error { return firstErr }
}

func (f *Follower) put(ctx context.Context, enc []byte) (*IngestResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, f.IngestURL+"/mobileverify/packet", bytes.NewReader(enc))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	hc := f.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	switch resp.StatusCode {
	case http.StatusOK:
		var res IngestResult
		if err := json.Unmarshal(body, &res); err != nil {
			return nil, fmt.Errorf("decode answer %q: %w", bytes.TrimSpace(body), err)
		}
		return &res, nil
	case http.StatusBadRequest, http.StatusForbidden, http.StatusRequestEntityTooLarge:
		// The node refused this packet; sending it again changes nothing.
		return nil, &PermanentError{Err: fmt.Errorf("HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(body))}
	default:
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(body))
	}
}

func (f *Follower) isDone(h types.Hash) bool {
	_, ok := f.done[h]
	return ok
}

// markDone remembers the last doneCap packet hashes.
func (f *Follower) markDone(h types.Hash) {
	if f.done == nil {
		f.done = make(map[types.Hash]struct{}, doneCap)
		f.doneRing = make([]types.Hash, doneCap)
	}
	if old := f.doneRing[f.doneNext]; old != (types.Hash{}) {
		delete(f.done, old)
	}
	f.doneRing[f.doneNext] = h
	f.doneNext = (f.doneNext + 1) % doneCap
	f.done[h] = struct{}{}
}
