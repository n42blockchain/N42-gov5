// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// MobileVerifySession is the gomobile-bindable face of MobileVerifyClient.
// gomobile skips every method whose signature carries a context, a fixed
// array, a struct pointer it cannot bind or a ChainConfig, which is all of
// MobileVerifyClient; a session takes and returns strings and ints only,
// owns the long-lived CodeCache the re-execution needs, and puts a timeout
// on every call. Results are JSON objects so the app decodes one shape on
// Android and iOS alike.
//
// The calls block; the app runs them off the UI thread, one loop per
// session: Register once per launch, then Latest -> VerifyBlock for each
// new head, and Cert for a verified block once its window has closed.

package evmsdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/n42blockchain/N42/params"
)

// mobileVerifySessionCodeCache bounds a session's bytecode cache, the same
// size cmd/mobileverify-device gives each simulated device.
const mobileVerifySessionCodeCache = 4096

// MobileVerifySession runs the mobile verification loop against one node.
// Its methods are safe for concurrent use.
type MobileVerifySession struct {
	client *MobileVerifyClient
	codes  *CodeCache
	cfg    *params.ChainConfig

	mu      sync.Mutex
	timeout time.Duration
	ctx     context.Context
	cancel  context.CancelFunc
}

// NewMobileVerifySession binds a device identity to a node's mobileverify
// base URL (e.g. "https://mv.example.com"). chainName selects the chain
// the packets are re-executed under and must name the chain the node runs
// (e.g. "mainnet_qmdb_staggered"): under another chain's rules the
// computed receipts root does not match and every block fails.
func NewMobileVerifySession(baseURL, privKeyHex, chainName string) (*MobileVerifySession, error) {
	cfg := params.ChainConfigByChainName(chainName)
	if cfg == nil {
		return nil, fmt.Errorf("mobileverify session: unknown chain %q", chainName)
	}
	client, err := NewMobileVerifyClient(strings.TrimRight(baseURL, "/"), privKeyHex)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &MobileVerifySession{
		client:  client,
		codes:   NewCodeCache(mobileVerifySessionCodeCache),
		cfg:     cfg,
		timeout: 30 * time.Second,
		ctx:     ctx,
		cancel:  cancel,
	}, nil
}

// PubkeyHex returns the device's 48-byte BLS public key hex.
func (s *MobileVerifySession) PubkeyHex() string {
	pub := s.client.Pubkey()
	return fmt.Sprintf("%x", pub[:])
}

// SetTimeoutMs sets the limit for each later call; 0 or less keeps the
// current one (30 s by default).
func (s *MobileVerifySession) SetTimeoutMs(ms int64) {
	if ms <= 0 {
		return
	}
	s.mu.Lock()
	s.timeout = time.Duration(ms) * time.Millisecond
	s.mu.Unlock()
}

// Close aborts the calls in flight; every later call fails.
func (s *MobileVerifySession) Close() { s.cancel() }

func (s *MobileVerifySession) call() (context.Context, context.CancelFunc, error) {
	if err := s.ctx.Err(); err != nil {
		return nil, nil, errors.New("mobileverify session: closed")
	}
	s.mu.Lock()
	timeout := s.timeout
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(s.ctx, timeout)
	return ctx, cancel, nil
}

func sessionJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Register registers the device (solving the proof of work when the node
// demands one) and returns {"index":N,"committed":bool}. It is idempotent;
// call it on every launch. Until committed is true the registration waits
// for the next epoch, the device's receipts do not count and the index is
// provisional, so the app calls it again until it is.
func (s *MobileVerifySession) Register() (string, error) {
	ctx, cancel, err := s.call()
	if err != nil {
		return "", err
	}
	defer cancel()
	idx, committed, err := s.client.RegisterStatus(ctx)
	if err != nil {
		return "", err
	}
	return sessionJSON(map[string]any{"index": idx, "committed": committed})
}

// Latest returns the node's canonical head,
// {"number":N,"hash":"0x..","packet":bool}. VerifyBlock it once packet is
// true.
func (s *MobileVerifySession) Latest() (string, error) {
	ctx, cancel, err := s.call()
	if err != nil {
		return "", err
	}
	defer cancel()
	head, err := s.client.Latest(ctx)
	if err != nil {
		return "", err
	}
	return sessionJSON(head)
}

// VerifyBlock fetches a block's packet, re-executes it, and signs and
// submits the receipt. It returns {"blockNumber":N,"blockHash":"0x..",
// "receiptsRoot":"0x..","txs":N,"index":N,"elapsedMs":N}, index being the
// device index the node accepted the receipt under. A block whose
// re-execution does not reproduce the header's receipts root is an error,
// and nothing is submitted for it.
func (s *MobileVerifySession) VerifyBlock(blockHashHex string) (string, error) {
	ctx, cancel, err := s.call()
	if err != nil {
		return "", err
	}
	defer cancel()
	v, err := verifyAndSubmit(ctx, s.client, s.codes, s.cfg, blockHashHex)
	if err != nil {
		return "", err
	}
	return sessionJSON(v)
}

// mobileVerifyResult is one verified and accepted block, the JSON
// VerifyBlock returns and the miner reports.
type mobileVerifyResult struct {
	BlockNumber  uint64 `json:"blockNumber"`
	BlockHash    string `json:"blockHash"`
	ReceiptsRoot string `json:"receiptsRoot"`
	Txs          int    `json:"txs"`
	Index        uint32 `json:"index"`
	ElapsedMs    int64  `json:"elapsedMs"`
}

// verifyAndSubmit fetches a block's packet, re-executes it under cfg and
// signs and submits the receipt.
func verifyAndSubmit(ctx context.Context, client *MobileVerifyClient, codes *CodeCache, cfg *params.ChainConfig, blockHashHex string) (*mobileVerifyResult, error) {
	start := time.Now()
	pkt, err := client.FetchPacket(ctx, blockHashHex)
	if err != nil {
		return nil, err
	}
	res, err := ExecuteAndVerifyV2(pkt, codes, cfg)
	if err != nil {
		return nil, err
	}
	idx, err := client.SubmitReceipt(ctx, client.SignReceipt(res))
	if err != nil {
		return nil, fmt.Errorf("mobileverify: verified block %d but submit failed: %w", res.BlockNumber, err)
	}
	return &mobileVerifyResult{
		BlockNumber:  res.BlockNumber,
		BlockHash:    res.BlockHash.Hex(),
		ReceiptsRoot: res.ComputedReceiptsRoot.Hex(),
		Txs:          res.TxCount,
		Index:        idx,
		ElapsedMs:    time.Since(start).Milliseconds(),
	}, nil
}

// Health returns the node's /mobileverify/health JSON.
func (s *MobileVerifySession) Health() (string, error) {
	ctx, cancel, err := s.call()
	if err != nil {
		return "", err
	}
	defer cancel()
	raw, err := s.client.Health(ctx)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// Cert returns the certificates the node holds for a block, a JSON list
// of {"blockNumber","receiptsRoot","signers",...}. It is "[]" while the
// block's window is still open.
func (s *MobileVerifySession) Cert(blockHashHex string) (string, error) {
	ctx, cancel, err := s.call()
	if err != nil {
		return "", err
	}
	defer cancel()
	raw, err := s.client.Cert(ctx, blockHashHex)
	if errors.Is(err, ErrMobileVerifyNotFound) {
		return "[]", nil
	}
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
