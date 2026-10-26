// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// E=1 benchmark mode (docs/QS_MULTI_MILLION_DESIGN.md P2b,
// docs/GOV5_5M_GAP_20261026.md): one node of the fleet executes every
// block; the others vote on availability, never execute, and store each
// block with the executor's result instead of their own. A light node's
// leader gate (LagReady), its header stamping (the lag build) and its
// availability check then read that result like one it executed. This is
// the n42-rs import-once trust model: a light node trusts the executor it
// names. Bench-only; a production node never sets either switch.
//
// N42_E1_SERVE=<addr> on the executor serves
// GET /e1/result?hash=0x..&wait_ms=N -> 200 with the encoded result
// (rawdb.EncodeExecutedResult), or 204 when it is not known within wait_ms.
// The result is served from the in-flight write as soon as it is complete
// in memory, before the write is durable, as N42_LAG_GATE_APPLIED does for
// the executor's own gate.
//
// N42_E1_SOURCE=http://<addr> makes a node light: its lag executor fetches
// each queued block's result from there and stores the block with it.
var (
	e1ServeAddr = os.Getenv("N42_E1_SERVE")
	e1SourceURL = os.Getenv("N42_E1_SOURCE")
)

// e1FetchWait bounds one long poll for a result.
const e1FetchWait = 200 * time.Millisecond

// e1PollStep is the executor's re-check interval inside a long poll.
const e1PollStep = 500 * time.Microsecond

// E1Light reports whether this node takes execution results from an
// executor (N42_E1_SOURCE).
func (bc *BlockChain) E1Light() bool { return bc.e1Source() != "" }

func (bc *BlockChain) e1Source() string {
	if p := bc.e1Src.Load(); p != nil {
		return *p
	}
	return e1SourceURL
}

// SetE1SourceForTest makes this chain light, taking results from url.
func (bc *BlockChain) SetE1SourceForTest(url string) { bc.e1Src.Store(&url) }

var e1Client = &http.Client{Timeout: e1FetchWait + 2*time.Second}

// e1Stats counts a light node's imports and the time it waited for results.
var e1Stats struct {
	imported, notReady, failed atomic.Uint64
	waitNs                     atomic.Int64
}

// E1LightStats returns the blocks stored with a fetched result, the polls
// that came back without one, the failed fetches and the total wait.
func E1LightStats() (imported, notReady, failed uint64, wait time.Duration) {
	return e1Stats.imported.Load(), e1Stats.notReady.Load(), e1Stats.failed.Load(), time.Duration(e1Stats.waitNs.Load())
}

// e1Result returns this node's executed result of hash: the in-flight
// write's when it is complete in memory, else the stored one.
func (bc *BlockChain) e1Result(hash types.Hash) (rawdb.ExecutedResult, bool, error) {
	if r, ok := bc.inflightExecutedResult(hash); ok {
		return r, true, nil
	}
	var (
		r  rawdb.ExecutedResult
		ok bool
	)
	err := bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
		var err error
		r, ok, err = rawdb.ReadExecutedResult(tx, hash)
		return err
	})
	return r, ok, err
}

// StartE1Server starts the executor's result server when N42_E1_SERVE is
// set; it stops with ctx.
func (bc *BlockChain) StartE1Server(ctx context.Context) error {
	if e1ServeAddr == "" {
		return nil
	}
	if bc.E1Light() {
		return errors.New("e1: N42_E1_SERVE and N42_E1_SOURCE are exclusive")
	}
	ln, err := net.Listen("tcp", e1ServeAddr)
	if err != nil {
		return fmt.Errorf("e1: listen %s: %w", e1ServeAddr, err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/e1/result", bc.serveE1Result)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("e1: result server stopped", "err", err)
		}
	}()
	log.Warn("E=1 executor: serving execution results (bench rule)", "addr", ln.Addr().String())
	return nil
}

func (bc *BlockChain) serveE1Result(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	hash := types.HexToHash(q.Get("hash"))
	if hash == (types.Hash{}) {
		http.Error(w, "missing hash", http.StatusBadRequest)
		return
	}
	wait := time.Duration(0)
	if ms, err := strconv.Atoi(q.Get("wait_ms")); err == nil && ms > 0 {
		wait = time.Duration(ms) * time.Millisecond
		if wait > 2*e1FetchWait {
			wait = 2 * e1FetchWait
		}
	}
	deadline := time.Now().Add(wait)
	for {
		r, ok, err := bc.e1Result(hash)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if ok {
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(rawdb.EncodeExecutedResult(r))
			return
		}
		if !time.Now().Before(deadline) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		select {
		case <-req.Context().Done():
			return
		case <-time.After(e1PollStep):
		}
	}
}

// e1Fetch asks the executor for hash's result, waiting up to e1FetchWait.
func e1Fetch(ctx context.Context, source string, hash types.Hash) (rawdb.ExecutedResult, bool, error) {
	url := fmt.Sprintf("%s/e1/result?hash=%s&wait_ms=%d", source, hash.Hex(), e1FetchWait.Milliseconds())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return rawdb.ExecutedResult{}, false, err
	}
	resp, err := e1Client.Do(req)
	if err != nil {
		return rawdb.ExecutedResult{}, false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent:
		return rawdb.ExecutedResult{}, false, nil
	case http.StatusOK:
	default:
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return rawdb.ExecutedResult{}, false, fmt.Errorf("e1: executor answered %d: %s", resp.StatusCode, bytes.TrimSpace(msg))
	}
	v, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return rawdb.ExecutedResult{}, false, err
	}
	r, ok := rawdb.DecodeExecutedResult(v)
	if !ok {
		return rawdb.ExecutedResult{}, false, fmt.Errorf("e1: executor result of %d bytes", len(v))
	}
	return r, true, nil
}

// e1LightImport stores b, a block queued for the lag executor of a light
// node, with the executor's result of it. done is false while the executor
// has not produced the result or b's parent is not stored here yet.
func (bc *BlockChain) e1LightImport(b *block.Block) (done bool, err error) {
	n := b.Number64().Uint64()
	if !bc.blockStored(b.ParentHash(), n-1) {
		return false, nil
	}
	ctx := bc.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	t0 := time.Now()
	r, ok, err := e1Fetch(ctx, bc.e1Source(), b.Hash())
	e1Stats.waitNs.Add(int64(time.Since(t0)))
	if err != nil {
		e1Stats.failed.Add(1)
		return false, err
	}
	if !ok {
		e1Stats.notReady.Add(1)
		return false, nil
	}
	if err := bc.ResolveFrames(b); err != nil {
		return false, err
	}
	stored := bc.blockStored(b.Hash(), n)
	var td *uint256.Int
	bc.lock.Lock()
	err = bc.ChainDB.Update(ctx, func(tx kv.RwTx) error {
		if !stored {
			ptd, err := rawdb.ReadTd(tx, b.ParentHash(), n-1)
			if err != nil {
				return fmt.Errorf("reading parent td for block %d: %w", n, err)
			}
			if ptd == nil {
				return consensus.ErrUnknownAncestor
			}
			td = new(uint256.Int).Add(ptd, b.Difficulty())
			if err := rawdb.WriteTd(tx, b.Hash(), n, td); err != nil {
				return err
			}
			if err := rawdb.WriteBlock(tx, b); err != nil {
				return err
			}
			if err := bc.writeConsensusEvidence(tx, b); err != nil {
				return err
			}
		}
		return rawdb.WriteExecutedResult(tx, b.Hash(), r)
	})
	bc.lock.Unlock()
	if err != nil {
		return false, err
	}
	if !stored {
		bc.framesPersisted(b)
		if bc.blockCache != nil {
			bc.blockCache.Add(b.Hash(), b)
		}
		if td != nil {
			bc.tdCache.Add(b.Hash(), td)
		}
	}
	e1Stats.imported.Add(1)
	return true, nil
}

// e1LightStep is lagStep on a light node: the lowest queued block whose
// parent is stored is stored with the executor's result.
func (bc *BlockChain) e1LightStep(list []*lagQueued, committedNum uint64) bool {
	for _, q := range list {
		b := q.blk
		n := b.Number64().Uint64()
		if bc.e1HasResult(b.Hash()) {
			bc.lagDone(b, bc.blockStored(b.Hash(), n))
			return true
		}
		if n <= committedNum {
			bc.lagForget(b.Hash())
			return true
		}
		done, err := bc.e1LightImport(b)
		if done {
			bc.lagDone(b, true)
			return true
		}
		if err != nil && !q.stuckLog && time.Since(q.since) > 5*time.Second {
			q.stuckLog = true
			log.Warn("e1 light: block not stored yet", "number", n, "hash", b.Hash().Hex()[:12], "err", err)
		}
		if err == nil && bc.blockStored(b.ParentHash(), n-1) {
			// Its result is not ready: a later block's is not either.
			return false
		}
	}
	return false
}

// e1HasResult reports whether hash's result is stored here.
func (bc *BlockChain) e1HasResult(hash types.Hash) bool {
	ok := false
	_ = bc.ChainDB.View(context.Background(), func(tx kv.Tx) error {
		_, ok, _ = rawdb.ReadExecutedResult(tx, hash)
		return nil
	})
	return ok
}
