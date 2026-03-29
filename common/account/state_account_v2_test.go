package account

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
)

func TestV2RoundTrip(t *testing.T) {
	tests := []struct {
		name    string
		acc     StateAccount
		wantLen int
	}{
		{"empty", NewAccount(), 1},
		{"nonce_only", func() StateAccount {
			a := NewAccount()
			a.Nonce = 5
			a.Initialised = true
			return a
		}(), 2},
		{"nonce_large", func() StateAccount {
			a := NewAccount()
			a.Nonce = 100000
			a.Initialised = true
			return a
		}(), 4},
		{"balance_1eth", func() StateAccount {
			a := NewAccount()
			a.Balance = *uint256.NewInt(1000000000000000000)
			return a
		}(), 10}, // 1 + 1(len) + 8(balance bytes)
		{"full_eoa", func() StateAccount {
			a := NewAccount()
			a.Nonce = 42
			a.Balance = *uint256.NewInt(5000000000000000000)
			return a
		}(), 0}, // don't check len, just roundtrip
		{"contract", func() StateAccount {
			a := NewAccount()
			a.Nonce = 1
			a.Incarnation = 1
			a.CodeHash = types.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")
			return a
		}(), 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := make([]byte, 128)
			n := tt.acc.EncodeForStorageV2(buf)
			calcLen := tt.acc.EncodingLengthForStorageV2()
			if n != calcLen {
				t.Fatalf("length mismatch: encoded %d, calculated %d", n, calcLen)
			}
			if tt.wantLen > 0 && n != tt.wantLen {
				t.Fatalf("unexpected length: got %d, want %d", n, tt.wantLen)
			}

			var dec StateAccount
			if err := dec.DecodeForStorageV2(buf[:n]); err != nil {
				t.Fatalf("decode error: %v", err)
			}
			if dec.Nonce != tt.acc.Nonce {
				t.Errorf("nonce: got %d, want %d", dec.Nonce, tt.acc.Nonce)
			}
			if dec.Balance.Cmp(&tt.acc.Balance) != 0 {
				t.Errorf("balance: got %s, want %s", dec.Balance.String(), tt.acc.Balance.String())
			}
			if dec.Incarnation != tt.acc.Incarnation {
				t.Errorf("incarnation: got %d, want %d", dec.Incarnation, tt.acc.Incarnation)
			}
			if dec.CodeHash != tt.acc.CodeHash {
				t.Errorf("codeHash: got %x, want %x", dec.CodeHash, tt.acc.CodeHash)
			}
		})
	}
}

func TestV2EmptyIsOneByte(t *testing.T) {
	var acc StateAccount
	buf := make([]byte, 128)
	n := acc.EncodeForStorageV2(buf)
	if n != 1 {
		t.Fatalf("empty account should be 1 byte, got %d", n)
	}
	if buf[0] != 0 {
		t.Fatalf("empty account fieldBits should be 0, got %d", buf[0])
	}
}

func TestV2DecodeEmpty(t *testing.T) {
	var acc StateAccount
	err := acc.DecodeForStorageV2([]byte{})
	if err != nil {
		t.Fatalf("decode empty: %v", err)
	}
	if acc.Nonce != 0 || !acc.Balance.IsZero() {
		t.Fatal("empty decode should produce zero account")
	}
}
