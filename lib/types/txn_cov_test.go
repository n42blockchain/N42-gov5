package types

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/lib/common"
	"github.com/n42blockchain/N42/lib/common/hexutility"
)

// A valid legacy transaction RLP, chainID 1 — shared with txn_test.go's
// TestTransactionSignatureValidity1.
var g41ValidLegacyTxnRlp = hexutility.MustDecodeHex("f83f800182520894095e7baea6a6c7c4c2dfeb977efac326af552d870b801ba048b55bfa915ac795c431978d8a6a992b628d557da5ff759b307d495a3664935301")

func TestTxParseContextValidateRLP(t *testing.T) {
	chainID := new(uint256.Int).SetUint64(1)
	ctx := NewTxParseContext(*chainID)

	called := false
	ctx.ValidateRLP(func(rlp []byte) error {
		called = true
		return nil
	})

	tx := &TxSlot{}
	var sender [20]byte
	if _, err := ctx.ParseTransaction(g41ValidLegacyTxnRlp, 0, tx, sender[:], false, true, nil); err != nil {
		t.Fatalf("ParseTransaction error: %v", err)
	}
	if !called {
		t.Fatal("expected the ValidateRLP callback to run")
	}
}

func TestTxParseContextValidateRLPRejects(t *testing.T) {
	chainID := new(uint256.Int).SetUint64(1)
	ctx := NewTxParseContext(*chainID)
	ctx.ValidateRLP(func(rlp []byte) error {
		return errBoom
	})

	tx := &TxSlot{}
	var sender [20]byte
	if _, err := ctx.ParseTransaction(g41ValidLegacyTxnRlp, 0, tx, sender[:], false, true, nil); err == nil {
		t.Fatal("expected ParseTransaction to surface the validateRlp error")
	}
}

var errBoom = &g41TestError{"boom"}

type g41TestError struct{ msg string }

func (e *g41TestError) Error() string { return e.msg }

func TestTxParseContextWithSenderDisabled(t *testing.T) {
	chainID := new(uint256.Int).SetUint64(1)
	ctx := NewTxParseContext(*chainID)
	ctx.WithSender(false)

	tx := &TxSlot{}
	if _, err := ctx.ParseTransaction(g41ValidLegacyTxnRlp, 0, tx, nil, false, true, nil); err != nil {
		t.Fatalf("ParseTransaction with WithSender(false) error: %v", err)
	}
}

func TestTxParseContextChainIDRequired(t *testing.T) {
	chainID := new(uint256.Int).SetUint64(1)
	base := NewTxParseContext(*chainID)
	ctx := base.ChainIDRequired()

	// ChainIDRequired is fluent and only affects typed (non-legacy)
	// transactions; it must not break parsing of a legacy transaction.
	if ctx != base {
		t.Fatal("expected ChainIDRequired to return the same context for chaining")
	}

	tx := &TxSlot{}
	var sender [20]byte
	if _, err := ctx.ParseTransaction(g41ValidLegacyTxnRlp, 0, tx, sender[:], false, true, nil); err != nil {
		t.Fatalf("ParseTransaction with ChainIDRequired on a legacy tx: %v", err)
	}
}

func TestEncodeDecodeSenderRoundTrip(t *testing.T) {
	nonce := uint64(42)
	balance := *uint256.NewInt(123456789)

	buf := make([]byte, EncodeSenderLengthForStorage(nonce, balance))
	EncodeSender(nonce, balance, buf)

	gotNonce, gotBalance, err := DecodeSender(buf)
	if err != nil {
		t.Fatalf("DecodeSender error: %v", err)
	}
	if gotNonce != nonce {
		t.Fatalf("nonce round trip: got %d, want %d", gotNonce, nonce)
	}
	if gotBalance.Cmp(&balance) != 0 {
		t.Fatalf("balance round trip: got %s, want %s", gotBalance.String(), balance.String())
	}
}

func TestEncodeDecodeSenderZeroValues(t *testing.T) {
	buf := make([]byte, EncodeSenderLengthForStorage(0, *uint256.NewInt(0)))
	EncodeSender(0, *uint256.NewInt(0), buf)

	nonce, balance, err := DecodeSender(buf)
	if err != nil {
		t.Fatalf("DecodeSender error: %v", err)
	}
	if nonce != 0 || !balance.IsZero() {
		t.Fatalf("expected zero nonce/balance, got nonce=%d balance=%s", nonce, balance.String())
	}
}

func TestDecodeSenderEmpty(t *testing.T) {
	nonce, balance, err := DecodeSender(nil)
	if err != nil {
		t.Fatalf("DecodeSender(nil) error: %v", err)
	}
	if nonce != 0 || !balance.IsZero() {
		t.Fatal("expected zero values for empty input")
	}
}

func TestDecodeSenderMalformed(t *testing.T) {
	// fieldSet=1 (nonce present) but the declared length overruns the buffer.
	enc := []byte{0x01, 0x05, 0x01}
	if _, _, err := DecodeSender(enc); err == nil {
		t.Fatal("expected error for malformed sender encoding")
	}
}

func TestPeekTransactionTypeLegacy(t *testing.T) {
	typ, err := PeekTransactionType(g41ValidLegacyTxnRlp)
	if err != nil {
		t.Fatalf("PeekTransactionType error: %v", err)
	}
	if typ != LegacyTxType {
		t.Fatalf("PeekTransactionType = %d, want LegacyTxType", typ)
	}
}

func TestPeekTransactionTypeTyped(t *testing.T) {
	// A minimal typed (EIP-2930) envelope: type byte 0x01 followed by a list.
	typed := append([]byte{AccessListTxType}, []byte{0xc0}...)
	typ, err := PeekTransactionType(typed)
	if err != nil {
		t.Fatalf("PeekTransactionType error: %v", err)
	}
	if typ != AccessListTxType {
		t.Fatalf("PeekTransactionType = %d, want AccessListTxType", typ)
	}
}

func TestUnwrapTxPlayloadRlpNonBlob(t *testing.T) {
	out, err := UnwrapTxPlayloadRlp(g41ValidLegacyTxnRlp)
	if err != nil {
		t.Fatalf("UnwrapTxPlayloadRlp error: %v", err)
	}
	if &out[0] != &g41ValidLegacyTxnRlp[0] {
		t.Fatal("expected non-blob input to be returned unchanged (no copy)")
	}
}

func TestAccessListStorageKeysAndHasAddr(t *testing.T) {
	addr1 := common.Address{1}
	addr2 := common.Address{2}
	al := AccessList{
		{Address: addr1, StorageKeys: []common.Hash{{1}, {2}}},
		{Address: addr2, StorageKeys: []common.Hash{{3}}},
	}

	if got := al.StorageKeys(); got != 3 {
		t.Fatalf("StorageKeys() = %d, want 3", got)
	}
	if !al.HasAddr(addr1) {
		t.Fatal("expected HasAddr to find addr1")
	}
	if al.HasAddr(common.Address{9}) {
		t.Fatal("did not expect HasAddr to find an absent address")
	}
}

func TestTxSlotPrintDebug(t *testing.T) {
	tx := &TxSlot{SenderID: 1, Nonce: 2}
	tx.Value.SetUint64(100)
	tx.Tip.SetUint64(5)
	// Just exercise for panics; output goes to stdout.
	tx.PrintDebug("test")
}
