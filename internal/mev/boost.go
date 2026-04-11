// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.
//
// MEV-Boost relay HTTP client. Implements the builder-spec endpoints
// (/eth/v1/builder/validators, /eth/v1/builder/header, blinded block
// submission) with a bounded relayTimeout (4s) and maxResponseBytes
// (10 MiB) guard against hostile relays. Handles validator registration,
// signed header fetching and blinded-block unblinding for the proposer.

// Package mev implements MEV-Boost integration for N42,
// enabling communication with external block builders through relay infrastructure.
package mev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"go.uber.org/zap"
)

const (
	// relayTimeout is the maximum time to wait for a relay response.
	relayTimeout = 4 * time.Second

	// maxResponseBytes limits relay response body to prevent OOM.
	maxResponseBytes = 10 * 1024 * 1024 // 10 MB

	// pathRegisterValidator is the relay endpoint for validator registration.
	pathRegisterValidator = "/eth/v1/builder/validators"

	// pathGetHeader is the relay endpoint for fetching the best execution header.
	pathGetHeader = "/eth/v1/builder/header"

	// pathGetPayload is the relay endpoint for unblinding a signed blinded block.
	pathGetPayload = "/eth/v1/builder/blinded_blocks"
)

var (
	ErrNoRelays        = errors.New("mev-boost: no relay URLs configured")
	ErrNoValidBid      = errors.New("mev-boost: no valid bid received from any relay")
	ErrRelayResponse   = errors.New("mev-boost: relay returned error")
	ErrInvalidPayload  = errors.New("mev-boost: invalid execution payload from relay")
	ErrContextCanceled = errors.New("mev-boost: context canceled during relay request")
)

// BuilderBid represents a bid from an external block builder received through a relay.
type BuilderBid struct {
	Header   *block.Header `json:"header"`
	Value    *uint256.Int  `json:"value"`
	Pubkey   []byte        `json:"pubkey"`
	RelayURL string        `json:"relay_url"`
}

// RegisterValidatorRequest contains the parameters for registering a validator
// with MEV-Boost relays so that builders can construct blocks on its behalf.
type RegisterValidatorRequest struct {
	FeeRecipient types.Address `json:"fee_recipient"`
	GasLimit     uint64        `json:"gas_limit"`
	Timestamp    uint64        `json:"timestamp"`
	Pubkey       []byte        `json:"pubkey"`
}

// RelayClient communicates with one or more MEV-Boost relays to fetch builder bids
// and submit blinded blocks for payload retrieval.
type RelayClient struct {
	relayURLs   []string
	httpClient  *http.Client
	validatorPK []byte
	mu          sync.RWMutex
	bestBid     *BuilderBid
	bestBidSlot uint64
	logger      *zap.Logger
}

// NewRelayClient creates a new RelayClient configured with the given relay URLs.
// Returns nil if no relay URLs are provided.
func NewRelayClient(relayURLs []string, logger *zap.Logger) *RelayClient {
	if len(relayURLs) == 0 {
		return nil
	}
	return &RelayClient{
		relayURLs: relayURLs,
		httpClient: &http.Client{
			Timeout: relayTimeout,
			Transport: &http.Transport{
				MaxIdleConns:        len(relayURLs) * 2,
				MaxIdleConnsPerHost: 2,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		logger: logger,
	}
}

// RegisterValidator sends a validator registration to all configured relays.
// This allows builders to know the validator's fee recipient and gas limit
// preferences for constructing blocks.
func (rc *RelayClient) RegisterValidator(ctx context.Context, req *RegisterValidatorRequest) error {
	if len(rc.relayURLs) == 0 {
		return ErrNoRelays
	}

	body, err := json.Marshal([]RegisterValidatorRequest{*req})
	if err != nil {
		return fmt.Errorf("mev-boost: failed to marshal registration: %w", err)
	}

	rc.mu.Lock()
	rc.validatorPK = make([]byte, len(req.Pubkey))
	copy(rc.validatorPK, req.Pubkey)
	rc.mu.Unlock()

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		lastErr error
		success int
	)

	for _, relayURL := range rc.relayURLs {
		wg.Add(1)
		go func(url string) {
			defer wg.Done()

			reqCtx, cancel := context.WithTimeout(ctx, relayTimeout)
			defer cancel()

			if err := rc.postJSON(reqCtx, url+pathRegisterValidator, body); err != nil {
				rc.logger.Warn("failed to register with relay",
					zap.String("relay", url),
					zap.Error(err),
				)
				mu.Lock()
				lastErr = err
				mu.Unlock()
				return
			}

			rc.logger.Info("registered validator with relay",
				zap.String("relay", url),
			)
			mu.Lock()
			success++
			mu.Unlock()
		}(relayURL)
	}

	wg.Wait()

	if success == 0 {
		return fmt.Errorf("mev-boost: failed to register with any relay: %w", lastErr)
	}
	return nil
}

// GetHeader fetches the best execution payload header from all configured relays
// for the given slot, parent hash, and validator public key. It queries relays
// concurrently and returns the highest-value bid.
func (rc *RelayClient) GetHeader(ctx context.Context, slot uint64, parentHash types.Hash, pubkey []byte) (*BuilderBid, error) {
	if len(rc.relayURLs) == 0 {
		return nil, ErrNoRelays
	}

	type result struct {
		bid *BuilderBid
		err error
	}

	results := make(chan result, len(rc.relayURLs))

	for _, relayURL := range rc.relayURLs {
		go func(url string) {
			reqCtx, cancel := context.WithTimeout(ctx, relayTimeout)
			defer cancel()

			path := fmt.Sprintf("%s%s/%d/%s/0x%x", url, pathGetHeader, slot, parentHash.Hex(), pubkey)

			bid, err := rc.getHeaderFromRelay(reqCtx, path, url)
			results <- result{bid: bid, err: err}
		}(relayURL)
	}

	var bestBid *BuilderBid
	var lastErr error

	for i := 0; i < len(rc.relayURLs); i++ {
		select {
		case <-ctx.Done():
			return nil, ErrContextCanceled
		case r := <-results:
			if r.err != nil {
				lastErr = r.err
				continue
			}
			if r.bid == nil {
				continue
			}
			if bestBid == nil || r.bid.Value.Cmp(bestBid.Value) > 0 {
				bestBid = r.bid
			}
		}
	}

	if bestBid == nil {
		if lastErr != nil {
			return nil, fmt.Errorf("%w: %v", ErrNoValidBid, lastErr)
		}
		return nil, ErrNoValidBid
	}

	rc.mu.Lock()
	rc.bestBid = bestBid
	rc.bestBidSlot = slot
	rc.mu.Unlock()

	rc.logger.Info("received builder bid",
		zap.Uint64("slot", slot),
		zap.String("value", bestBid.Value.String()),
		zap.String("relay", bestBid.RelayURL),
	)

	return bestBid, nil
}

// GetPayload submits a signed blinded block to the relay that provided the
// winning bid, receiving the full execution payload in return.
func (rc *RelayClient) GetPayload(ctx context.Context, signedBlindedBlock []byte) (*ExecutionPayload, error) {
	rc.mu.RLock()
	bid := rc.bestBid
	rc.mu.RUnlock()

	if bid == nil {
		return nil, ErrNoValidBid
	}

	reqCtx, cancel := context.WithTimeout(ctx, relayTimeout)
	defer cancel()

	url := bid.RelayURL + pathGetPayload

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(signedBlindedBlock))
	if err != nil {
		return nil, fmt.Errorf("mev-boost: failed to create payload request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := rc.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("mev-boost: payload request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("mev-boost: failed to read payload response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		bodyStr := string(respBody)
		if len(bodyStr) > 512 {
			bodyStr = bodyStr[:512] + "...(truncated)"
		}
		return nil, fmt.Errorf("%w: status=%d body=%s", ErrRelayResponse, resp.StatusCode, bodyStr)
	}

	var payload ExecutionPayload
	if err := json.Unmarshal(respBody, &payload); err != nil {
		return nil, fmt.Errorf("mev-boost: failed to decode payload: %w", err)
	}

	if payload.BlockHash == (types.Hash{}) {
		return nil, ErrInvalidPayload
	}

	rc.logger.Info("received execution payload from relay",
		zap.String("relay", bid.RelayURL),
		zap.Uint64("block_number", payload.BlockNumber),
		zap.String("block_hash", payload.BlockHash.Hex()),
	)

	return &payload, nil
}

// ValidatorPubkey returns a copy of the registered validator public key.
func (rc *RelayClient) ValidatorPubkey() []byte {
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	out := make([]byte, len(rc.validatorPK))
	copy(out, rc.validatorPK)
	return out
}

// BestBid returns the most recent best bid in a thread-safe manner.
// Returns nil if no bid has been received.
func (rc *RelayClient) BestBid() *BuilderBid {
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	return rc.bestBid
}

// getHeaderFromRelay fetches a builder bid from a single relay endpoint.
func (rc *RelayClient) getHeaderFromRelay(ctx context.Context, url, relayURL string) (*BuilderBid, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("mev-boost: failed to create header request: %w", err)
	}
	httpReq.Header.Set("Accept", "application/json")

	resp, err := rc.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("mev-boost: header request to %s failed: %w", relayURL, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("mev-boost: failed to read header response: %w", err)
	}

	if resp.StatusCode == http.StatusNoContent {
		// No bid available from this relay.
		return nil, nil
	}

	if resp.StatusCode != http.StatusOK {
		bodyStr := string(respBody)
		if len(bodyStr) > 512 {
			bodyStr = bodyStr[:512] + "...(truncated)"
		}
		rc.logger.Debug("relay returned non-OK status",
			zap.String("relay", relayURL),
			zap.Int("status", resp.StatusCode),
			zap.String("body", bodyStr),
		)
		return nil, fmt.Errorf("%w: relay=%s status=%d", ErrRelayResponse, relayURL, resp.StatusCode)
	}

	var bid BuilderBid
	if err := json.Unmarshal(respBody, &bid); err != nil {
		return nil, fmt.Errorf("mev-boost: failed to decode bid from %s: %w", relayURL, err)
	}

	bid.RelayURL = relayURL

	if bid.Value == nil || bid.Value.IsZero() {
		return nil, nil
	}

	return &bid, nil
}

// postJSON sends a POST request with JSON body to the given URL.
func (rc *RelayClient) postJSON(ctx context.Context, url string, body []byte) error {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("mev-boost: failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := rc.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("mev-boost: request to %s failed: %w", url, err)
	}
	defer resp.Body.Close()

	// Drain body to allow connection reuse.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%w: url=%s status=%d", ErrRelayResponse, url, resp.StatusCode)
	}

	return nil
}
