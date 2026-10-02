package main

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/lib/jmt"
)

func TestN42DecodeAccount(t *testing.T) {
	acc := account.NewAccount()
	acc.Nonce = 42
	acc.Balance = *uint256.NewInt(123456)

	buf := make([]byte, 128)
	n := acc.EncodeForStorageV2(buf)

	decoded, err := decodeAccount(buf[:n])
	if err != nil {
		t.Fatalf("decodeAccount error: %v", err)
	}
	if decoded.Nonce != 42 {
		t.Fatalf("Nonce = %d, want 42", decoded.Nonce)
	}
	if decoded.Balance.Uint64() != 123456 {
		t.Fatalf("Balance = %d, want 123456", decoded.Balance.Uint64())
	}
}

func TestN42DecodeAccountInvalid(t *testing.T) {
	if _, err := decodeAccount([]byte{0xff, 0xff, 0xff}); err == nil {
		t.Fatal("expected an error decoding malformed account bytes")
	}
}

func TestN42ReadOnlyMDBXStorePutDeleteAreReadOnly(t *testing.T) {
	s := &readOnlyMDBXStore{}
	var h jmt.Hash
	if err := s.Put(h, []byte("x")); err != jmt.ErrReadOnly {
		t.Fatalf("Put error = %v, want ErrReadOnly", err)
	}
	if err := s.Delete(h); err != jmt.ErrReadOnly {
		t.Fatalf("Delete error = %v, want ErrReadOnly", err)
	}
}
