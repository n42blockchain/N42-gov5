package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// Diagnose the already verified live entries without accepting any missing
// PlainState row. In particular, a recognized empty system account remains a
// failure until a separate policy check is implemented and reviewed.
func unmatchedPlainState(live map[qmdb.Hash]liveValue) error {
	type sample struct {
		Key           string `json:"key_hash"`
		Slot          uint64 `json:"slot"`
		ValueBytes    int    `json:"value_bytes"`
		ValuePrefix   string `json:"value_prefix_hex"`
		SystemAccount bool   `json:"system_account_key"`
	}
	diagnostic := struct {
		Count         int         `json:"unmatched_count"`
		CanonicalZero int         `json:"single_zero_byte_values"`
		ValueLengths  map[int]int `json:"value_length_counts"`
		Samples       []sample    `json:"samples"`
	}{Count: len(live), ValueLengths: make(map[int]int)}
	keys := make([]qmdb.Hash, 0, len(live))
	for kh, entry := range live {
		keys = append(keys, kh)
		diagnostic.ValueLengths[len(entry.value)]++
		if bytes.Equal(entry.value, []byte{0}) {
			diagnostic.CanonicalZero++
		}
	}
	sort.Slice(keys, func(i, j int) bool { return bytes.Compare(keys[i][:], keys[j][:]) < 0 })
	system := qmdb.Hash(commitment.AccountKeyHash(state.SystemAddress))
	for _, kh := range keys[:min(len(keys), 8)] {
		entry := live[kh]
		diagnostic.Samples = append(diagnostic.Samples, sample{
			Key: hex.EncodeToString(kh[:]), Slot: entry.slot, ValueBytes: len(entry.value),
			ValuePrefix: hex.EncodeToString(entry.value[:min(len(entry.value), 64)]), SystemAccount: kh == system,
		})
	}
	encoded, err := json.Marshal(diagnostic)
	if err != nil {
		return fmt.Errorf("encode unmatched-state diagnostics: %w", err)
	}
	return fmt.Errorf("committed live keys absent from plain state: %s", encoded)
}
