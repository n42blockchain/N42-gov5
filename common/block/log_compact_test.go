// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package block

import (
	"bytes"
	"math/rand"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
)

func sampleLogs() Logs {
	return Logs{
		{
			Address: types.Address{0x01, 0x02, 0x03},
			Topics:  []types.Hash{{0xaa}, {0xbb}, {0xcc}},
			Data:    bytes.Repeat([]byte{0x7f}, 96),
			// Context fields: set so the test proves they are the ones dropped.
			BlockNumber: uint256.NewInt(1234),
			TxHash:      types.Hash{0xde, 0xad},
			TxIndex:     7,
			BlockHash:   types.Hash{0xbe, 0xef},
			Index:       3,
		},
		{
			Address: types.Address{0xff},
			Topics:  nil,
			Data:    nil,
		},
		{
			Address: types.Address{0x42},
			Topics:  []types.Hash{{0x11}},
			Data:    []byte{0x00},
		},
	}
}

// TestCompactLogsRoundTrip pins the consensus content of a log across the
// compact codec: address, topics and data. The context fields are deliberately
// not carried -- the table is keyed by block number and transaction id, so they
// are already known to any reader, and storing them was most of what the proto
// encoding cost.
func TestCompactLogsRoundTrip(t *testing.T) {
	original := sampleLogs()

	encoded := original.MarshalCompact()
	if !IsCompactLogs(encoded) {
		t.Fatal("MarshalCompact output is not recognised by IsCompactLogs")
	}

	var decoded Logs
	if err := decoded.Unmarshal(encoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(decoded) != len(original) {
		t.Fatalf("log count %d != %d", len(decoded), len(original))
	}
	for i := range original {
		if decoded[i].Address != original[i].Address {
			t.Errorf("log %d address %x != %x", i, decoded[i].Address, original[i].Address)
		}
		if len(decoded[i].Topics) != len(original[i].Topics) {
			t.Fatalf("log %d topic count %d != %d", i, len(decoded[i].Topics), len(original[i].Topics))
		}
		for j := range original[i].Topics {
			if decoded[i].Topics[j] != original[i].Topics[j] {
				t.Errorf("log %d topic %d differs", i, j)
			}
		}
		if !bytes.Equal(decoded[i].Data, original[i].Data) {
			t.Errorf("log %d data differs", i)
		}
	}
}

// TestLogsUnmarshalAcceptsBothFormats is the compatibility guarantee that lets
// the write path switch without a migration: databases hold proto records
// written before the change and compact records written after, in the same
// table.
func TestLogsUnmarshalAcceptsBothFormats(t *testing.T) {
	original := sampleLogs()

	protoBytes, err := original.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if IsCompactLogs(protoBytes) {
		t.Fatal("proto output must not be mistaken for the compact format")
	}

	var fromProto Logs
	if err := fromProto.Unmarshal(protoBytes); err != nil {
		t.Fatalf("Unmarshal(proto): %v", err)
	}
	var fromCompact Logs
	if err := fromCompact.Unmarshal(original.MarshalCompact()); err != nil {
		t.Fatalf("Unmarshal(compact): %v", err)
	}

	if len(fromProto) != len(fromCompact) {
		t.Fatalf("formats disagree on count: %d vs %d", len(fromProto), len(fromCompact))
	}
	for i := range fromProto {
		if fromProto[i].Address != fromCompact[i].Address {
			t.Errorf("log %d: formats disagree on address", i)
		}
		if !bytes.Equal(fromProto[i].Data, fromCompact[i].Data) {
			t.Errorf("log %d: formats disagree on data", i)
		}
	}

	// Sizes are not compared here: sampleLogs leaves context fields unset on
	// two of its three logs, and those fields are exactly what the compact codec
	// drops. TestCompactLogsSizeOnProductionShape measures it on logs shaped
	// like the ones write paths store.
}

// TestCompactLogsSizeOnProductionShape measures the two formats on logs shaped
// like the ones write paths actually store: every context field populated,
// because execution fills them in before the receipt is written. Those fields
// are exactly what the compact codec drops, so this is where the difference
// shows up -- the sample in sampleLogs leaves some unset and understates it.
func TestCompactLogsSizeOnProductionShape(t *testing.T) {
	// Full-entropy addresses, topics and hashes. This matters: protobuf encodes
	// a 32-byte hash as four uint64 varints, and a mostly-zero hash collapses to
	// almost nothing because proto3 omits zero-valued fields. Real topics are
	// keccak output, so every one of those varints runs to its full ten bytes.
	// Low-entropy test values make protobuf look far more compact than it is.
	r := rand.New(rand.NewSource(1))
	mk := func(topics int, dataLen int) *Log {
		lg := &Log{
			Data:        make([]byte, dataLen),
			BlockNumber: uint256.NewInt(13478855),
			TxIndex:     7,
			Index:       3,
		}
		r.Read(lg.Address[:])
		r.Read(lg.TxHash[:])
		r.Read(lg.BlockHash[:])
		r.Read(lg.Data)
		for i := 0; i < topics; i++ {
			var t types.Hash
			r.Read(t[:])
			lg.Topics = append(lg.Topics, t)
		}
		return lg
	}

	for _, c := range []struct {
		name string
		logs Logs
	}{
		{"ERC20 Transfer (3 topics, 32 B data)", Logs{mk(3, 32)}},
		{"single topic, no data", Logs{mk(1, 0)}},
		{"8 logs, mixed", Logs{mk(3, 32), mk(1, 0), mk(2, 64), mk(4, 128), mk(3, 32), mk(1, 96), mk(2, 0), mk(3, 32)}},
	} {
		t.Run(c.name, func(t *testing.T) {
			protoBytes, err := c.logs.Marshal()
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			compactBytes := c.logs.MarshalCompact()
			t.Logf("proto %4d B, compact %4d B -> %.1f%% smaller",
				len(protoBytes), len(compactBytes),
				float64(len(protoBytes)-len(compactBytes))*100/float64(len(protoBytes)))
			if len(compactBytes) >= len(protoBytes) {
				t.Errorf("compact (%d B) is not smaller than proto (%d B)", len(compactBytes), len(protoBytes))
			}
		})
	}
}

// TestCompactLogsRejectsMalformed checks the decoder fails rather than
// allocating on a corrupt length, which is how a truncated or damaged record
// reaches it.
func TestCompactLogsRejectsMalformed(t *testing.T) {
	sample := sampleLogs()
	valid := sample.MarshalCompact()

	// Empty input is not in this list: it is a valid protobuf encoding of zero
	// logs, and TestLogsUnmarshalEmpty pins that as intended.
	cases := map[string][]byte{
		"marker only":        {compactLogsMarker},
		"wrong version":      {compactLogsMarker, 0x99, 0x00},
		"truncated mid-log":  valid[:len(valid)/2],
		"count beyond input": {compactLogsMarker, compactLogsVersion, 0xff, 0xff, 0xff, 0x7f},
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			var logs Logs
			if err := logs.Unmarshal(data); err == nil {
				t.Errorf("expected an error, decoded %d logs", len(logs))
			}
		})
	}
}

// TestCompactLogsHandlesUnsetContext covers a log whose block context was never
// filled in. The compact codec does not carry those fields at all, so it is
// indifferent to them; the proto path used to panic here, dereferencing a nil
// BlockNumber inside ConvertUint256IntToH256.
func TestCompactLogsHandlesUnsetContext(t *testing.T) {
	logs := Logs{{
		Address: types.Address{0x99},
		Topics:  []types.Hash{{0x01}},
		Data:    []byte{0xab, 0xcd},
		// BlockNumber, TxHash, BlockHash deliberately unset.
	}}

	var decoded Logs
	if err := decoded.Unmarshal(logs.MarshalCompact()); err != nil {
		t.Fatalf("compact round trip: %v", err)
	}
	if len(decoded) != 1 || decoded[0].Address != logs[0].Address {
		t.Fatal("compact round trip lost the log")
	}

	// The proto path must now survive it too rather than panicking.
	if _, err := logs.Marshal(); err != nil {
		t.Fatalf("proto marshal of a context-less log: %v", err)
	}
}

// TestCompactLogsEmpty covers the common case: most transactions emit no logs.
func TestCompactLogsEmpty(t *testing.T) {
	var empty Logs
	encoded := empty.MarshalCompact()

	var decoded Logs
	if err := decoded.Unmarshal(encoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(decoded) != 0 {
		t.Fatalf("expected no logs, got %d", len(decoded))
	}
}
