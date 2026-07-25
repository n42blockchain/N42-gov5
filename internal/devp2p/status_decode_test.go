package devp2p

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/rlp"
	"github.com/n42blockchain/N42/common/types"
)

// TestStatusLayoutsAreDistinguishable proves the invariant the flexible Status
// decode in runPeer relies on: the eth/68 (6-field, TD+head) and eth/69 (7-field,
// genesis+range) Status layouts are mutually exclusive under RLP's strict struct
// decode, so decoding by "try one, fall back to the other" reliably recovers the
// right one regardless of the negotiated version. This is why a peer that
// advertises eth/69 but sends an eth/68 Status (the observed 24×+ drop) is now
// accepted instead of failing on "input string too short for Genesis".
func TestStatusLayoutsAreDistinguishable(t *testing.T) {
	genesis := types.HexToHash("0xd4e56740f876aef8c010b86a40d5f56745a118d0906a34e69aec8c0db1cb8fa3")
	head := types.HexToHash("0x00017439820000000000000000000000000000000000000000000000000c8762")
	fork := forkID{Hash: [4]byte{0xde, 0xad, 0xbe, 0xef}, Next: 42}

	s68 := statusPacket68{
		ProtocolVersion: 68,
		NetworkID:       1,
		TD:              big.NewInt(58750003716598352816469 % (1 << 62)), // any big int
		Head:            head,
		Genesis:         genesis,
		ForkID:          fork,
	}
	s69 := statusPacket{
		ProtocolVersion: 69,
		NetworkID:       1,
		Genesis:         genesis,
		ForkID:          fork,
		EarliestBlock:   0,
		LatestBlock:     25594400,
		LatestBlockHash: head,
	}

	b68, err := rlp.EncodeToBytes(&s68)
	if err != nil {
		t.Fatalf("encode eth/68: %v", err)
	}
	b69, err := rlp.EncodeToBytes(&s69)
	if err != nil {
		t.Fatalf("encode eth/69: %v", err)
	}

	// eth/68 bytes: decode into the eth/68 struct succeeds, into eth/69 fails.
	var d68 statusPacket68
	if err := rlp.DecodeBytes(b68, &d68); err != nil {
		t.Fatalf("eth/68 bytes → statusPacket68 should succeed, got %v", err)
	}
	if d68.Genesis != genesis || d68.Head != head || d68.NetworkID != 1 {
		t.Fatalf("eth/68 round-trip fields wrong: %+v", d68)
	}
	var x69 statusPacket
	if err := rlp.DecodeBytes(b68, &x69); err == nil {
		t.Fatalf("eth/68 bytes → statusPacket (eth/69) MUST fail (else the fallback would mis-decode), but it succeeded: %+v", x69)
	}

	// eth/69 bytes: decode into the eth/69 struct succeeds, into eth/68 fails.
	var d69 statusPacket
	if err := rlp.DecodeBytes(b69, &d69); err != nil {
		t.Fatalf("eth/69 bytes → statusPacket should succeed, got %v", err)
	}
	if d69.Genesis != genesis || d69.LatestBlockHash != head || d69.LatestBlock != 25594400 {
		t.Fatalf("eth/69 round-trip fields wrong: %+v", d69)
	}
	var x68 statusPacket68
	if err := rlp.DecodeBytes(b69, &x68); err == nil {
		t.Fatalf("eth/69 bytes → statusPacket68 MUST fail, but it succeeded: %+v", x68)
	}

	t.Logf("OK: eth/68 (%d B) and eth/69 (%d B) Status layouts are mutually exclusive under RLP strict decode", len(b68), len(b69))
}

// TestRequestIDTailToleratesQueryShape proves the GetBlockHeaders handler's
// lenient decode: requestIDTail recovers the request-id from the standard
// `[reqID, [origin, amount, skip, reverse]]` and would also survive extra
// trailing elements — so a peer whose query encoding differs isn't dropped on
// "rlp: too many elements".
func TestRequestIDTailToleratesQueryShape(t *testing.T) {
	// Standard eth/66+ GetBlockHeaders wire form.
	pkt := getBlockHeadersPacket{
		RequestID: 0xABCD,
		getBlockHeadersQuery: &getBlockHeadersQuery{
			Origin:  hashOrNumber{Number: 25594400},
			Amount:  192,
			Skip:    0,
			Reverse: false,
		},
	}
	b, err := rlp.EncodeToBytes(&pkt)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var got requestIDTail
	if err := rlp.DecodeBytes(b, &got); err != nil {
		t.Fatalf("requestIDTail decode of standard query failed: %v", err)
	}
	if got.RequestID != 0xABCD {
		t.Fatalf("RequestID=%d, want 0xABCD", got.RequestID)
	}

	// A malformed/extended form `[reqID, a, b, c]` (extra top-level elements) —
	// the strict getBlockHeadersPacket would reject this, requestIDTail must not.
	ext, err := rlp.EncodeToBytes([]interface{}{uint64(0x1234), uint64(1), uint64(2), uint64(3)})
	if err != nil {
		t.Fatalf("encode ext: %v", err)
	}
	var got2 requestIDTail
	if err := rlp.DecodeBytes(ext, &got2); err != nil {
		t.Fatalf("requestIDTail should tolerate extra elements, got %v", err)
	}
	if got2.RequestID != 0x1234 {
		t.Fatalf("RequestID=%d, want 0x1234", got2.RequestID)
	}
	t.Logf("OK: requestIDTail recovers reqID from standard and extended query shapes")
}
