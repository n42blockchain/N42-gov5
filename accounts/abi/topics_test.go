package abi

import (
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

func TestMakeTopicsSimpleTypes(t *testing.T) {
	addr := types.Address{1, 2, 3}
	hash := types.Hash{4, 5, 6}
	topics, err := MakeTopics([]interface{}{
		hash,
		addr,
		big.NewInt(42),
		true,
		false,
		int8(-1),
		int16(-1),
		int32(100),
		int64(200),
		uint8(1),
		uint16(2),
		uint32(3),
		uint64(4),
		"hello",
		[]byte("world"),
	})
	if err != nil {
		t.Fatalf("MakeTopics error: %v", err)
	}
	if len(topics) != 1 {
		t.Fatalf("expected 1 filter group, got %d", len(topics))
	}
	got := topics[0]
	if len(got) != 15 {
		t.Fatalf("expected 15 topics, got %d", len(got))
	}
	if got[0] != hash {
		t.Errorf("hash topic mismatch")
	}
	wantAddrTopic := types.Hash{}
	copy(wantAddrTopic[types.HashLength-types.AddressLength:], addr[:])
	if got[1] != wantAddrTopic {
		t.Errorf("address topic mismatch")
	}
	// bool true
	if got[3][types.HashLength-1] != 1 {
		t.Errorf("bool-true topic mismatch: %x", got[3])
	}
	// bool false - all zero
	allZero := true
	for _, b := range got[4] {
		if b != 0 {
			allZero = false
		}
	}
	if !allZero {
		t.Errorf("bool-false topic should be all zero: %x", got[4])
	}
	// string hashed
	wantStrHash := crypto.Keccak256Hash([]byte("hello"))
	if got[13] != wantStrHash {
		t.Errorf("string topic mismatch")
	}
	// []byte hashed
	wantBytesHash := crypto.Keccak256Hash([]byte("world"))
	if got[14] != wantBytesHash {
		t.Errorf("bytes topic mismatch")
	}
}

func TestMakeTopicsNegativeInt(t *testing.T) {
	topics, err := MakeTopics([]interface{}{int64(-1)})
	if err != nil {
		t.Fatalf("MakeTopics error: %v", err)
	}
	got := topics[0][0]
	for _, b := range got {
		if b != 0xff {
			t.Fatalf("expected all-0xff two's complement encoding, got %x", got)
		}
	}
}

func TestMakeTopicsMultipleFilters(t *testing.T) {
	topics, err := MakeTopics(
		[]interface{}{uint64(1), uint64(2)},
		[]interface{}{true},
	)
	if err != nil {
		t.Fatalf("MakeTopics error: %v", err)
	}
	if len(topics) != 2 {
		t.Fatalf("expected 2 filter groups, got %d", len(topics))
	}
	if len(topics[0]) != 2 || len(topics[1]) != 1 {
		t.Errorf("unexpected topic group sizes: %v", topics)
	}
}

func TestMakeTopicsUnsupportedType(t *testing.T) {
	type custom struct{ A int }
	if _, err := MakeTopics([]interface{}{custom{A: 1}}); err == nil {
		t.Error("expected error for unsupported indexed type")
	}
}

func TestMakeTopicsFixedByteArray(t *testing.T) {
	var arr [4]byte
	arr[0] = 0xAA
	topics, err := MakeTopics([]interface{}{arr})
	if err != nil {
		t.Fatalf("MakeTopics error: %v", err)
	}
	if topics[0][0][0] != 0xAA {
		t.Errorf("fixed byte array topic mismatch: %x", topics[0][0])
	}
}

func mustArg(t *testing.T, typ, name string, indexed bool) Argument {
	t.Helper()
	ty, err := NewType(typ, "", nil)
	if err != nil {
		t.Fatalf("NewType(%q) error: %v", typ, err)
	}
	return Argument{Name: name, Type: ty, Indexed: indexed}
}

func TestParseTopicsIntoMap(t *testing.T) {
	fields := Arguments{
		mustArg(t, "uint256", "amount", true),
		mustArg(t, "address", "sender", true),
		mustArg(t, "bool", "flag", true),
	}
	amountHash := types.Hash{}
	amountHash[31] = 42
	addr := types.Address{9, 9, 9}
	addrHash := types.Hash{}
	copy(addrHash[types.HashLength-types.AddressLength:], addr[:])
	boolHash := types.Hash{}
	boolHash[31] = 1

	out := map[string]interface{}{}
	err := ParseTopicsIntoMap(out, fields, []types.Hash{amountHash, addrHash, boolHash})
	if err != nil {
		t.Fatalf("ParseTopicsIntoMap error: %v", err)
	}
	if out["flag"] != true {
		t.Errorf("flag = %v, want true", out["flag"])
	}
	gotAddr, ok := out["sender"].(types.Address)
	if !ok || gotAddr != addr {
		t.Errorf("sender = %v, want %v", out["sender"], addr)
	}
}

func TestParseTopicsCountMismatch(t *testing.T) {
	fields := Arguments{mustArg(t, "uint256", "amount", true)}
	err := ParseTopicsIntoMap(map[string]interface{}{}, fields, nil)
	if err == nil {
		t.Error("expected count mismatch error")
	}
}

func TestParseTopicsNonIndexedField(t *testing.T) {
	fields := Arguments{mustArg(t, "uint256", "amount", false)}
	err := ParseTopicsIntoMap(map[string]interface{}{}, fields, []types.Hash{{}})
	if err == nil {
		t.Error("expected error for non-indexed field")
	}
}

func TestParseTopicsTupleUnsupported(t *testing.T) {
	ty, err := NewType("tuple", "", []ArgumentMarshaling{{Name: "a", Type: "uint256"}})
	if err != nil {
		t.Fatalf("NewType error: %v", err)
	}
	fields := Arguments{{Name: "t", Type: ty, Indexed: true}}
	err = ParseTopicsIntoMap(map[string]interface{}{}, fields, []types.Hash{{}})
	if err == nil {
		t.Error("expected error for tuple type in topic reconstruction")
	}
}

func TestParseTopicsDynamicTypeReturnsHash(t *testing.T) {
	fields := Arguments{mustArg(t, "string", "s", true)}
	h := crypto.Keccak256Hash([]byte("dynamic"))
	out := map[string]interface{}{}
	if err := ParseTopicsIntoMap(out, fields, []types.Hash{h}); err != nil {
		t.Fatalf("ParseTopicsIntoMap error: %v", err)
	}
	if out["s"] != h {
		t.Errorf("expected raw hash for dynamic type, got %v", out["s"])
	}
}
