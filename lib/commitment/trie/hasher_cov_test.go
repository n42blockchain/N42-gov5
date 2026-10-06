package trie

import (
	"bytes"
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

func TestEncodeNodeNil(t *testing.T) {
	out, err := encodeNode(nil)
	if err != nil {
		t.Fatalf("encodeNode(nil) error: %v", err)
	}
	if !bytes.Equal(out, []byte{0x80}) {
		t.Fatalf("encodeNode(nil) = %x, want 80", out)
	}
}

func TestEncodeNodeHashNode(t *testing.T) {
	h := types.HexToHash("0x0102")
	out, err := encodeNode(&HashNode{Hash: h})
	if err != nil {
		t.Fatalf("encodeNode(HashNode) error: %v", err)
	}
	if len(out) != 33 || out[0] != 0xa0 {
		t.Fatalf("encodeNode(HashNode) = %x, want 33-byte 0xa0-prefixed", out)
	}
}

func TestEncodeNodeValueNode(t *testing.T) {
	out, err := encodeNode(ValueNode("hi"))
	if err != nil {
		t.Fatalf("encodeNode(ValueNode) error: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("expected non-empty encoding")
	}
}

func TestEncodeNodeAccountNode(t *testing.T) {
	acc := &AccountNode{Account: account.StateAccount{Nonce: 1}}
	out, err := encodeNode(acc)
	if err != nil {
		t.Fatalf("encodeNode(AccountNode) error: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("expected non-empty account encoding")
	}
}

func TestEncodeNodeUnsupportedType(t *testing.T) {
	if _, err := encodeNode(struct{ Node }{}); err == nil {
		t.Fatal("expected error for unsupported node type")
	}
}

func TestNodeRefNil(t *testing.T) {
	out, err := nodeRef(nil)
	if err != nil {
		t.Fatalf("nodeRef(nil) error: %v", err)
	}
	if !bytes.Equal(out, []byte{0x80}) {
		t.Fatalf("nodeRef(nil) = %x, want 80", out)
	}
}

func TestNodeRefHashNode(t *testing.T) {
	h := types.HexToHash("0xabcd")
	out, err := nodeRef(&HashNode{Hash: h})
	if err != nil {
		t.Fatalf("nodeRef(HashNode) error: %v", err)
	}
	if len(out) != 33 || out[0] != 0xa0 {
		t.Fatalf("nodeRef(HashNode) = %x", out)
	}
}

func TestNodeRefShortInlineVsLongHashed(t *testing.T) {
	// A ValueNode short enough that its RLP encoding is < 32 bytes: inlined.
	short, err := nodeRef(ValueNode("x"))
	if err != nil {
		t.Fatalf("nodeRef(short) error: %v", err)
	}
	if len(short) >= 33 {
		t.Fatalf("expected short value to be inlined, got %d bytes", len(short))
	}

	// A ValueNode long enough that its encoding is >= 32 bytes: hashed.
	long, err := nodeRef(ValueNode(bytes.Repeat([]byte{0xAA}, 40)))
	if err != nil {
		t.Fatalf("nodeRef(long) error: %v", err)
	}
	if len(long) != 33 || long[0] != 0xa0 {
		t.Fatalf("expected long value to be hashed to 33 bytes, got %d", len(long))
	}
}

func TestNodeRefPropagatesEncodeError(t *testing.T) {
	if _, err := nodeRef(struct{ Node }{}); err == nil {
		t.Fatal("expected nodeRef to propagate encodeNode's error")
	}
}

func TestEncodeShortNodeNilValErrors(t *testing.T) {
	sn := &ShortNode{Key: []byte{1, 2}, Val: nil}
	if _, err := encodeShortNode(sn); err == nil {
		t.Fatal("expected error for ShortNode with nil Val")
	}
}

func TestEncodeShortNodeValueChild(t *testing.T) {
	sn := &ShortNode{Key: keybytesToHex([]byte{0x12}), Val: ValueNode("v")}
	out, err := encodeShortNode(sn)
	if err != nil {
		t.Fatalf("encodeShortNode(value) error: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("expected non-empty encoding")
	}
}

func TestEncodeShortNodeAccountChild(t *testing.T) {
	sn := &ShortNode{Key: keybytesToHex([]byte{0x12}), Val: &AccountNode{Account: account.StateAccount{Nonce: 2}}}
	out, err := encodeShortNode(sn)
	if err != nil {
		t.Fatalf("encodeShortNode(account) error: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("expected non-empty encoding")
	}
}

func TestEncodeShortNodeOtherChildRef(t *testing.T) {
	sn := &ShortNode{Key: []byte{1}, Val: &FullNode{}}
	out, err := encodeShortNode(sn)
	if err != nil {
		t.Fatalf("encodeShortNode(full child) error: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("expected non-empty encoding")
	}
}

func TestEncodeShortNodeChildRefError(t *testing.T) {
	// A child that itself fails to encode should bubble the error up through
	// the default nodeRef branch.
	sn := &ShortNode{Key: []byte{1}, Val: &ShortNode{Key: []byte{2}, Val: nil}}
	if _, err := encodeShortNode(sn); err == nil {
		t.Fatal("expected error to propagate from a bad child")
	}
}

func TestEncodeFullNodeAllSlots(t *testing.T) {
	fn := &FullNode{}
	fn.Children[0] = ValueNode("a")
	fn.Children[16] = ValueNode("term")
	out, err := encodeFullNode(fn)
	if err != nil {
		t.Fatalf("encodeFullNode(value slot) error: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("expected non-empty encoding")
	}
}

func TestEncodeFullNodeAccountSlot(t *testing.T) {
	fn := &FullNode{}
	fn.Children[16] = &AccountNode{Account: account.StateAccount{Nonce: 3}}
	out, err := encodeFullNode(fn)
	if err != nil {
		t.Fatalf("encodeFullNode(account slot) error: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("expected non-empty encoding")
	}
}

func TestEncodeFullNodeNilSlot(t *testing.T) {
	fn := &FullNode{}
	out, err := encodeFullNode(fn)
	if err != nil {
		t.Fatalf("encodeFullNode(empty) error: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("expected non-empty encoding even for an empty branch")
	}
}

func TestEncodeFullNodeChildError(t *testing.T) {
	fn := &FullNode{}
	fn.Children[0] = struct{ Node }{}
	if _, err := encodeFullNode(fn); err == nil {
		t.Fatal("expected error propagated from a bad child at index 0")
	}
}

func TestEncodeFullNodeValueSlotRefError(t *testing.T) {
	fn := &FullNode{}
	fn.Children[16] = struct{ Node }{}
	if _, err := encodeFullNode(fn); err == nil {
		t.Fatal("expected error propagated from a bad value-slot child")
	}
}

func TestEncodeListHeaderBoundaries(t *testing.T) {
	cases := []int{0, 55, 56, 255, 256, 65535, 65536}
	for _, l := range cases {
		h := encodeListHeader(l)
		if len(h) == 0 {
			t.Errorf("encodeListHeader(%d) returned empty header", l)
		}
	}
}
