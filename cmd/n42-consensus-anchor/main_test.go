package main

import (
	"encoding/json"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"path/filepath"
	"strings"
	"testing"
)

func TestQCReportPreservesPublicIdentityAndBitmap(t *testing.T) {
	value := hotstuff.QuorumCertificate{View: 42, BlockHash: types.HexToHash("0x01"), AggregateSignature: []byte{1, 2, 3}, Signers: []bool{true, false, true}}
	result := qc(value)
	if result["view"] != value.View || result["blockHash"] != value.BlockHash.Hex() || result["signature"] != "0x010203" {
		t.Fatalf("identity: %#v", result)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"signers":[true,false,true]`) {
		t.Fatalf("bitmap: %s", raw)
	}
}

func TestMissingSourceFailsBeforeDatabaseOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing")
	if err := run(path); err == nil {
		t.Fatal("missing database accepted")
	}
}
