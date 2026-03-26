// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package bridge

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

// BeaconFetcher fetches ETH beacon chain light client updates and feeds
// them to the EthLightClient for sync committee signature verification.
// This makes the ETH→N42 reverse bridge operational.
type BeaconFetcher struct {
	beaconEndpoint string
	lightClient    *EthLightClient
	httpClient     *http.Client
	pollInterval   time.Duration
}

// NewBeaconFetcher creates a new beacon API fetcher.
func NewBeaconFetcher(beaconEndpoint string, lc *EthLightClient, pollInterval time.Duration) *BeaconFetcher {
	if pollInterval == 0 {
		pollInterval = 12 * time.Second
	}
	return &BeaconFetcher{
		beaconEndpoint: beaconEndpoint,
		lightClient:    lc,
		httpClient:     &http.Client{Timeout: 30 * time.Second},
		pollInterval:   pollInterval,
	}
}

// Run starts the beacon fetcher main loop.
func (f *BeaconFetcher) Run(ctx context.Context) error {
	log.Info("Beacon fetcher started", "endpoint", f.beaconEndpoint, "pollInterval", f.pollInterval)

	ticker := time.NewTicker(f.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("Beacon fetcher stopped")
			return ctx.Err()
		case <-ticker.C:
			if err := f.fetchAndProcess(ctx); err != nil {
				log.Warn("Beacon fetch failed", "err", err)
			}
		}
	}
}

// fetchAndProcess fetches the latest finality update from the beacon API.
func (f *BeaconFetcher) fetchAndProcess(ctx context.Context) error {
	update, err := f.fetchFinalityUpdate(ctx)
	if err != nil {
		return fmt.Errorf("fetch finality update: %w", err)
	}
	if update == nil {
		return nil
	}

	if err := f.lightClient.ProcessUpdate(ctx, update); err != nil {
		return fmt.Errorf("process update: %w", err)
	}
	return nil
}

// fetchFinalityUpdate calls the beacon API to get the latest finality update.
// Endpoint: GET /eth/v1/beacon/light_client/finality_update
func (f *BeaconFetcher) fetchFinalityUpdate(ctx context.Context) (*SyncCommitteeUpdate, error) {
	url := f.beaconEndpoint + "/eth/v1/beacon/light_client/finality_update"

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := f.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("beacon API returned %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	return parseBeaconFinalityUpdate(body)
}

// --- Beacon API response parsing ---

// beaconFinalityUpdate matches the beacon API /eth/v1/beacon/light_client/finality_update response.
type beaconFinalityUpdate struct {
	Data struct {
		AttestedHeader  beaconHeader    `json:"attested_header"`
		FinalizedHeader beaconHeader    `json:"finalized_header"`
		FinalityBranch  []string        `json:"finality_branch"`
		SyncAggregate   beaconAggregate `json:"sync_aggregate"`
	} `json:"data"`
}

type beaconHeader struct {
	Beacon struct {
		Slot          string `json:"slot"`
		ProposerIndex string `json:"proposer_index"`
		ParentRoot    string `json:"parent_root"`
		StateRoot     string `json:"state_root"`
		BodyRoot      string `json:"body_root"`
	} `json:"beacon"`
}

type beaconAggregate struct {
	SyncCommitteeBits      string `json:"sync_committee_bits"`
	SyncCommitteeSignature string `json:"sync_committee_signature"`
}

// parseBeaconFinalityUpdate converts the beacon API JSON response to our internal types.
func parseBeaconFinalityUpdate(body []byte) (*SyncCommitteeUpdate, error) {
	var resp beaconFinalityUpdate
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}

	attested, err := parseBeaconHeaderToEthHeader(resp.Data.AttestedHeader)
	if err != nil {
		return nil, fmt.Errorf("parse attested header: %w", err)
	}

	finalized, err := parseBeaconHeaderToEthHeader(resp.Data.FinalizedHeader)
	if err != nil {
		return nil, fmt.Errorf("parse finalized header: %w", err)
	}

	// Parse finality branch (hex strings to types.Hash)
	finalityBranch := make([]types.Hash, len(resp.Data.FinalityBranch))
	for i, hex := range resp.Data.FinalityBranch {
		finalityBranch[i] = types.HexToHash(hex)
	}

	// Parse sync aggregate
	sigBytes, err := decodeHex(resp.Data.SyncAggregate.SyncCommitteeSignature)
	if err != nil {
		return nil, fmt.Errorf("decode signature hex: %w", err)
	}
	bitsBytes, err := decodeHex(resp.Data.SyncAggregate.SyncCommitteeBits)
	if err != nil {
		return nil, fmt.Errorf("decode bits hex: %w", err)
	}

	var bits [SyncCommitteeSize / 8]byte
	copy(bits[:], bitsBytes)

	return &SyncCommitteeUpdate{
		AttestedHeader:  attested,
		FinalizedHeader: finalized,
		SyncAggregate: &SyncAggregate{
			SyncCommitteeBits:      bits,
			SyncCommitteeSignature: sigBytes,
		},
		FinalityBranch: finalityBranch,
	}, nil
}

func parseBeaconHeaderToEthHeader(bh beaconHeader) (*EthHeader, error) {
	slot, err := strconv.ParseUint(bh.Beacon.Slot, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse slot %q: %w", bh.Beacon.Slot, err)
	}
	proposerIndex, err := strconv.ParseUint(bh.Beacon.ProposerIndex, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse proposer_index %q: %w", bh.Beacon.ProposerIndex, err)
	}

	return &EthHeader{
		Slot:          slot,
		ProposerIndex: proposerIndex,
		ParentRoot:    types.HexToHash(bh.Beacon.ParentRoot),
		StateRoot:     types.HexToHash(bh.Beacon.StateRoot),
		BodyRoot:      types.HexToHash(bh.Beacon.BodyRoot),
	}, nil
}

func decodeHex(s string) ([]byte, error) {
	if len(s) >= 2 && s[:2] == "0x" {
		s = s[2:]
	}
	if len(s)%2 != 0 {
		s = "0" + s
	}
	return hex.DecodeString(s)
}
