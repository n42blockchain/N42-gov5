package types

import "testing"

func TestTxSlotsValid(t *testing.T) {
	s := &TxSlots{
		Txs:     []*TxSlot{{}, {}},
		Senders: make(Addresses, 40), // 2 addresses
		IsLocal: []bool{true, false},
	}
	if err := s.Valid(); err != nil {
		t.Fatalf("Valid() error: %v", err)
	}
}

func TestTxSlotsValidMismatchedIsLocal(t *testing.T) {
	s := &TxSlots{
		Txs:     []*TxSlot{{}},
		Senders: make(Addresses, 20),
		IsLocal: []bool{true, false},
	}
	if err := s.Valid(); err == nil {
		t.Fatal("expected error for mismatched IsLocal length")
	}
}

func TestTxSlotsValidMismatchedSenders(t *testing.T) {
	s := &TxSlots{
		Txs:     []*TxSlot{{}, {}},
		Senders: make(Addresses, 20), // only 1 address
		IsLocal: []bool{true, false},
	}
	if err := s.Valid(); err == nil {
		t.Fatal("expected error for mismatched senders length")
	}
}

func TestTxSlotsResizeGrowAndShrink(t *testing.T) {
	s := &TxSlots{}
	s.Resize(3)
	if len(s.Txs) != 3 || s.Senders.Len() != 3 || len(s.IsLocal) != 3 {
		t.Fatalf("Resize(3) lengths: txs=%d senders=%d isLocal=%d", len(s.Txs), s.Senders.Len(), len(s.IsLocal))
	}

	s.Resize(1)
	if len(s.Txs) != 1 || s.Senders.Len() != 1 || len(s.IsLocal) != 1 {
		t.Fatalf("Resize(1) lengths: txs=%d senders=%d isLocal=%d", len(s.Txs), s.Senders.Len(), len(s.IsLocal))
	}
}

func TestTxSlotsAppend(t *testing.T) {
	s := &TxSlots{}
	slot := &TxSlot{Nonce: 1}
	sender := make([]byte, 20)
	sender[0] = 0xAB

	s.Append(slot, sender, true)

	if len(s.Txs) != 1 || s.Txs[0] != slot {
		t.Fatalf("expected appended slot to be present, got %+v", s.Txs)
	}
	if !s.IsLocal[0] {
		t.Fatal("expected IsLocal[0] to be true")
	}
	if s.Senders.AddressAt(0)[0] != 0xAB {
		t.Fatalf("expected sender byte 0xAB, got %x", s.Senders.AddressAt(0))
	}
}

func TestTxsRlpResize(t *testing.T) {
	s := &TxsRlp{}
	s.Resize(2)
	if len(s.Txs) != 2 || s.Senders.Len() != 2 || len(s.IsLocal) != 2 {
		t.Fatalf("Resize(2) lengths: txs=%d senders=%d isLocal=%d", len(s.Txs), s.Senders.Len(), len(s.IsLocal))
	}

	s.Resize(1)
	if len(s.Txs) != 1 || s.Senders.Len() != 1 || len(s.IsLocal) != 1 {
		t.Fatalf("Resize(1) lengths: txs=%d senders=%d isLocal=%d", len(s.Txs), s.Senders.Len(), len(s.IsLocal))
	}
}

func TestAddressesAtAndLen(t *testing.T) {
	addrs := make(Addresses, 40)
	addrs[20] = 0x01
	if addrs.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", addrs.Len())
	}
	if addrs.At(1)[0] != 0x01 {
		t.Fatalf("At(1)[0] = %x, want 0x01", addrs.At(1)[0])
	}
	if addrs.AddressAt(1)[0] != 0x01 {
		t.Fatalf("AddressAt(1)[0] = %x, want 0x01", addrs.AddressAt(1)[0])
	}
}
