package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

func TestUnmatchedPlainStateDiagnosticsRemainFailure(t *testing.T) {
	system := qmdb.Hash(commitment.AccountKeyHash(state.SystemAddress))
	live := map[qmdb.Hash]liveValue{system: {slot: 123, value: []byte{0}}}
	err := unmatchedPlainState(live)
	if err == nil || !strings.Contains(err.Error(), `"system_account_key":true`) || !strings.Contains(err.Error(), `"single_zero_byte_values":1`) {
		t.Fatalf("system account was accepted or not identified: %v", err)
	}
	if len(live) != 1 || live[system].value[0] != 0 {
		t.Fatal("diagnostics mutated committed entries")
	}
}

func TestUnmatchedPlainStateDiagnosticsBoundedAndDeterministic(t *testing.T) {
	live := make(map[qmdb.Hash]liveValue)
	for i := range byte(20) {
		var kh qmdb.Hash
		kh[0] = 20 - i
		live[kh] = liveValue{slot: uint64(i), value: make([]byte, 4096)}
	}
	err := unmatchedPlainState(live)
	if err.Error() != unmatchedPlainState(live).Error() {
		t.Fatal("nondeterministic diagnostics")
	}
	var got struct {
		Count   int `json:"unmatched_count"`
		Samples []struct {
			ValuePrefix string `json:"value_prefix_hex"`
		} `json:"samples"`
	}
	const prefix = "committed live keys absent from plain state: "
	if e := json.Unmarshal([]byte(strings.TrimPrefix(err.Error(), prefix)), &got); e != nil {
		t.Fatal(e)
	}
	if got.Count != 20 || len(got.Samples) != 8 || len(got.Samples[0].ValuePrefix) != 128 {
		t.Fatalf("diagnostics not bounded: %+v", got)
	}
}
