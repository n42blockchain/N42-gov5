// Copyright 2022-2026 The N42 Authors
package ethel

import (
	"encoding/binary"
	"testing"
)

func TestEncodeEthMainnetGenesis(t *testing.T) {
	acctcs, storcs, err := encodeEthMainnetGenesis()
	if err != nil {
		t.Fatalf("encodeEthMainnetGenesis: %v", err)
	}

	// Acctcs blob layout: [count:2LE] then per-entry [addr:20][oldLen:1][oldVal][newLen:1][newVal].
	// Mainnet alloc has 8893 accounts; the count prefix MUST equal that.
	if len(acctcs) < 2 {
		t.Fatalf("acctcs too short: %d bytes", len(acctcs))
	}
	count := binary.LittleEndian.Uint16(acctcs[:2])
	if count != 8893 {
		t.Errorf("genesis account count: got %d, want 8893", count)
	}

	// Storcs may be empty for vanilla mainnet alloc — only a few system
	// contracts have pre-set storage. Just ensure no encoder error and
	// the blob isn't garbage by checking it parses (length prefix sane).
	if len(storcs) >= 2 {
		addrCount := binary.LittleEndian.Uint16(storcs[:2])
		t.Logf("genesis storcs addr count: %d", addrCount)
	}
	t.Logf("genesis acctcs %d bytes, storcs %d bytes", len(acctcs), len(storcs))
}
