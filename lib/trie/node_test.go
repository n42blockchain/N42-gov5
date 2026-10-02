package trie

import (
	"bytes"
	"strings"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
)

func TestNewShortNodeAndEncodeAsValue(t *testing.T) {
	v := valueNode([]byte("hello"))
	n := NewShortNode([]byte{1, 2, 3}, v)
	if !bytes.Equal(n.Key, []byte{1, 2, 3}) {
		t.Errorf("Key mismatch")
	}
	if vv, ok := n.Val.(valueNode); !ok || !bytes.Equal(vv, v) {
		t.Errorf("Val mismatch")
	}

	encoded, err := EncodeAsValue([]byte("world"))
	if err != nil {
		t.Fatalf("EncodeAsValue error: %v", err)
	}
	if len(encoded) == 0 {
		t.Error("expected non-empty encoding")
	}
}

func TestFullNodeEncodeRLPAndString(t *testing.T) {
	fn := &fullNode{}
	fn.Children[0] = valueNode([]byte("a"))
	fn.Children[5] = valueNode([]byte("b"))

	var buf bytes.Buffer
	if err := fn.EncodeRLP(&buf); err != nil {
		t.Fatalf("EncodeRLP error: %v", err)
	}
	if buf.Len() == 0 {
		t.Error("expected non-empty RLP encoding")
	}

	s := fn.String()
	if !strings.Contains(s, "full") {
		t.Errorf("String() = %q, expected to contain 'full'", s)
	}

	var out bytes.Buffer
	fn.print(&out)
	if !strings.Contains(out.String(), "f(") {
		t.Errorf("print() = %q, expected prefix f(", out.String())
	}
}

func TestDuoNodeEncodeRLPAndString(t *testing.T) {
	dn := &duoNode{
		mask:   (1 << 2) | (1 << 7),
		child1: valueNode([]byte("x")),
		child2: valueNode([]byte("y")),
	}
	i1, i2 := dn.childrenIdx()
	if i1 != 2 || i2 != 7 {
		t.Errorf("childrenIdx = %d, %d, want 2, 7", i1, i2)
	}

	var buf bytes.Buffer
	if err := dn.EncodeRLP(&buf); err != nil {
		t.Fatalf("EncodeRLP error: %v", err)
	}
	if buf.Len() == 0 {
		t.Error("expected non-empty RLP encoding")
	}

	s := dn.String()
	if !strings.Contains(s, "duo") {
		t.Errorf("String() = %q, expected to contain 'duo'", s)
	}

	var out bytes.Buffer
	dn.print(&out)
	if !strings.Contains(out.String(), "d(") {
		t.Errorf("print() = %q, expected prefix d(", out.String())
	}
}

func TestShortNodeStringAndPrint(t *testing.T) {
	sn := &shortNode{Key: []byte{1, 2}, Val: valueNode([]byte("v"))}
	s := sn.String()
	if s == "" {
		t.Error("expected non-empty string")
	}
	var out bytes.Buffer
	sn.print(&out)
	if !strings.HasPrefix(out.String(), "s(") {
		t.Errorf("print() = %q, expected prefix s(", out.String())
	}
}

func TestHashNodeValueNodeCodeNodeStringPrint(t *testing.T) {
	hn := hashNode{hash: []byte{0xAB, 0xCD}}
	if !strings.Contains(hn.String(), "abcd") {
		t.Errorf("hashNode String() = %q", hn.String())
	}
	if hn.reference() == nil {
		t.Error("hashNode reference should return hash")
	}

	vn := valueNode([]byte("val"))
	if !strings.Contains(vn.String(), "76616c") { // hex("val")
		t.Errorf("valueNode String() = %q", vn.String())
	}
	if vn.reference() != nil {
		t.Error("valueNode reference should be nil")
	}

	cn := codeNode([]byte{0x01, 0x02})
	if !strings.Contains(cn.String(), "code") {
		t.Errorf("codeNode String() = %q", cn.String())
	}
	if cn.reference() != nil {
		t.Error("codeNode reference should be nil")
	}

	var outH, outV, outC bytes.Buffer
	hn.print(&outH)
	vn.print(&outV)
	cn.print(&outC)
	if !strings.HasPrefix(outH.String(), "h(") || !strings.HasPrefix(outV.String(), "v(") || !strings.HasPrefix(outC.String(), "code(") {
		t.Errorf("print prefixes wrong: %q %q %q", outH.String(), outV.String(), outC.String())
	}
}

func TestAccountNodeStringAndPrint(t *testing.T) {
	an := accountNode{}
	an.Nonce = 7
	an.Balance = *uint256.NewInt(100)
	s := an.String()
	if !strings.Contains(s, "nonce=7") {
		t.Errorf("accountNode String() = %q", s)
	}
	var out bytes.Buffer
	an.print(&out)
	if !strings.Contains(out.String(), "nonce=7") {
		t.Errorf("accountNode print() = %q", out.String())
	}

	an.storage = valueNode([]byte("s"))
	s2 := an.fstring("")
	if !strings.Contains(s2, "nonce=7") {
		t.Errorf("accountNode fstring with storage = %q", s2)
	}
}

func TestResetRefs(t *testing.T) {
	leaf := &shortNode{Key: []byte{1}, Val: valueNode([]byte("v"))}
	leaf.ref.len = 5
	dn := &duoNode{mask: 3, child1: leaf, child2: valueNode([]byte("w"))}
	dn.ref.len = 3
	fn := &fullNode{}
	fn.Children[0] = dn
	fn.ref.len = 2

	resetRefs(fn)
	if fn.ref.len != 0 || dn.ref.len != 0 || leaf.ref.len != 0 {
		t.Errorf("resetRefs did not clear all refs: fn=%d dn=%d leaf=%d", fn.ref.len, dn.ref.len, leaf.ref.len)
	}
}

func TestReferenceAccessors(t *testing.T) {
	fn := &fullNode{}
	fn.ref.len = 2
	fn.ref.data[0] = 0xAA
	fn.ref.data[1] = 0xBB
	if !bytes.Equal(fn.reference(), []byte{0xAA, 0xBB}) {
		t.Errorf("fullNode reference mismatch: %x", fn.reference())
	}

	dn := &duoNode{}
	dn.ref.len = 1
	dn.ref.data[0] = 0xCC
	if !bytes.Equal(dn.reference(), []byte{0xCC}) {
		t.Errorf("duoNode reference mismatch: %x", dn.reference())
	}

	sn := &shortNode{}
	sn.ref.len = 1
	sn.ref.data[0] = 0xDD
	if !bytes.Equal(sn.reference(), []byte{0xDD}) {
		t.Errorf("shortNode reference mismatch: %x", sn.reference())
	}

	an := &accountNode{}
	if an.reference() != nil {
		t.Error("accountNode reference should be nil")
	}
}

func TestCodeKeyHelpers(t *testing.T) {
	addrHash := []byte{1, 2, 3}
	codeKey := CodeKeyFromAddrHash(addrHash)
	if !IsPointingToCode(codeKey) {
		t.Error("expected IsPointingToCode true for generated code key")
	}
	back := AddrHashFromCodeKey(codeKey)
	if !bytes.Equal(back, []byte{1, 2, 3}) {
		t.Errorf("AddrHashFromCodeKey = %x, want %x", back, addrHash)
	}

	if IsPointingToCode([]byte{1}) {
		t.Error("expected false for short key")
	}
	if IsPointingToCode([]byte{1, 2, 3}) {
		t.Error("expected false for key without 0xC0DE suffix")
	}

	hexKey := []byte{1, 2, 3}
	codeHex := CodeHexFromHex(hexKey)
	if len(codeHex) != len(hexKey)+4 {
		t.Errorf("CodeHexFromHex length = %d, want %d", len(codeHex), len(hexKey)+4)
	}
}

func TestCalcSubtreeSizeAndNodes(t *testing.T) {
	if calcSubtreeSize(nil) != 0 || calcSubtreeNodes(nil) != 0 {
		t.Error("expected 0 for nil node")
	}
	v := valueNode([]byte("v"))
	if calcSubtreeSize(v) != 0 || calcSubtreeNodes(v) != 0 {
		t.Error("expected 0 for valueNode")
	}
	hn := hashNode{hash: []byte{1}}
	if calcSubtreeSize(hn) != 0 || calcSubtreeNodes(hn) != 0 {
		t.Error("expected 0 for hashNode")
	}

	sn := &shortNode{Key: []byte{1}, Val: v}
	if calcSubtreeSize(sn) != 0 || calcSubtreeNodes(sn) != 0 {
		t.Error("expected 0 for shortNode wrapping valueNode")
	}

	dn := &duoNode{child1: v, child2: v}
	if calcSubtreeSize(dn) != 1 || calcSubtreeNodes(dn) != 1 {
		t.Errorf("duoNode size/nodes mismatch: %d %d", calcSubtreeSize(dn), calcSubtreeNodes(dn))
	}

	fn := &fullNode{}
	fn.Children[0] = dn
	if calcSubtreeSize(fn) != 2 || calcSubtreeNodes(fn) != 2 {
		t.Errorf("fullNode size/nodes mismatch: %d %d", calcSubtreeSize(fn), calcSubtreeNodes(fn))
	}

	an := &accountNode{code: codeNode([]byte{1, 2, 3}), storage: v}
	if calcSubtreeSize(an) != 3 {
		t.Errorf("accountNode size = %d, want 3", calcSubtreeSize(an))
	}
	if calcSubtreeNodes(an) != 1 {
		t.Errorf("accountNode nodes = %d, want 1", calcSubtreeNodes(an))
	}

	an2 := &accountNode{storage: v}
	if calcSubtreeNodes(an2) != 0 {
		t.Errorf("accountNode without code nodes = %d, want 0", calcSubtreeNodes(an2))
	}

	_ = account.StateAccount{}
}
