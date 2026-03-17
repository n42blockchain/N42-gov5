package encrypted

import (
	"errors"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

type decryptorStub struct {
	decryptFn func([]byte, []byte) ([]byte, error)
}

func (d *decryptorStub) Decrypt(encryptedData []byte, encryptionKey []byte) ([]byte, error) {
	return d.decryptFn(encryptedData, encryptionKey)
}

func (d *decryptorStub) PublicKey() []byte {
	return []byte("pubkey")
}

func TestEncryptAESGCMRoundTrip(t *testing.T) {
	key := []byte("01234567890123456789012345678901")
	plaintext := []byte("secret payload")

	ciphertext, err := encryptAESGCM(key, plaintext)
	if err != nil {
		t.Fatalf("encryptAESGCM() error = %v", err)
	}
	roundtrip, err := decryptAESGCM(key, ciphertext)
	if err != nil {
		t.Fatalf("decryptAESGCM() error = %v", err)
	}
	if string(roundtrip) != string(plaintext) {
		t.Fatalf("roundtrip = %q, want %q", roundtrip, plaintext)
	}
}

func TestDecryptAESGCMRejectsShortCiphertext(t *testing.T) {
	_, err := decryptAESGCM([]byte("01234567890123456789012345678901"), []byte("short"))
	if !errors.Is(err, ErrCiphertextTooShort) {
		t.Fatalf("decryptAESGCM() error = %v, want ErrCiphertextTooShort", err)
	}
}

func TestSimpleKeyperLifecycle(t *testing.T) {
	keyper := NewSimpleKeyper()

	if _, err := keyper.GetPublicKey(); !errors.Is(err, ErrNoCurrentKey) {
		t.Fatalf("GetPublicKey() error = %v, want ErrNoCurrentKey", err)
	}
	pub, err := keyper.GenerateEpochKey(7)
	if err != nil {
		t.Fatalf("GenerateEpochKey() error = %v", err)
	}
	if len(pub) != aesKeySize {
		t.Fatalf("public key len = %d, want %d", len(pub), aesKeySize)
	}
	if got := keyper.EpochCount(); got != 1 {
		t.Fatalf("EpochCount() = %d, want 1", got)
	}
	if _, err := keyper.GetPrivateShare(7); err != nil {
		t.Fatalf("GetPrivateShare() error = %v", err)
	}

	keyper.PurgeEpoch(7)
	if got := keyper.EpochCount(); got != 0 {
		t.Fatalf("EpochCount() = %d, want 0", got)
	}
	if _, err := keyper.GetPrivateShare(7); !errors.Is(err, ErrEpochNotFound) {
		t.Fatalf("GetPrivateShare() error = %v, want ErrEpochNotFound", err)
	}
}

func TestNewThresholdDecryptorValidatesInputs(t *testing.T) {
	if _, err := NewThresholdDecryptor(nil, []byte("pub"), 1, 1, nil); !errors.Is(err, ErrInvalidKeyShare) {
		t.Fatalf("NewThresholdDecryptor() error = %v, want ErrInvalidKeyShare", err)
	}
	if _, err := NewThresholdDecryptor([]byte("priv"), nil, 1, 1, nil); !errors.Is(err, ErrInvalidPublicKey) {
		t.Fatalf("NewThresholdDecryptor() error = %v, want ErrInvalidPublicKey", err)
	}
	if _, err := NewThresholdDecryptor([]byte("priv"), []byte("pub"), 2, 1, nil); !errors.Is(err, ErrInvalidThreshold) {
		t.Fatalf("NewThresholdDecryptor() error = %v, want ErrInvalidThreshold", err)
	}
}

func TestEncryptedPoolDecryptForBlockRejectsNilDecryptor(t *testing.T) {
	pool := NewEncryptedPool(16, nil, nil)
	if _, err := pool.DecryptForBlock(1); !errors.Is(err, ErrNilDecryptor) {
		t.Fatalf("DecryptForBlock() error = %v, want ErrNilDecryptor", err)
	}
}

func TestEncryptedPoolGetPendingReturnsSortedCopies(t *testing.T) {
	pool := NewEncryptedPool(16, &decryptorStub{}, nil)
	txLow := newEncryptedTx(1, 1, 10)
	txHigh := newEncryptedTx(1, 2, 20)
	if err := pool.Add(txLow); err != nil {
		t.Fatalf("Add(low) error = %v", err)
	}
	if err := pool.Add(txHigh); err != nil {
		t.Fatalf("Add(high) error = %v", err)
	}

	pending := pool.GetPending(1)
	if len(pending) != 2 {
		t.Fatalf("GetPending() len = %d, want 2", len(pending))
	}
	if pending[0].ID != txHigh.ID {
		t.Fatalf("pending[0].ID = %s, want %s", pending[0].ID.Hex(), txHigh.ID.Hex())
	}
	pending[0].EncryptedData[0] = 0xff

	again := pool.GetPending(1)
	if again[0].EncryptedData[0] == 0xff {
		t.Fatal("GetPending() returned internal slice without defensive copy")
	}
}

func TestEncryptedPoolGetBySenderReturnsSortedCopies(t *testing.T) {
	pool := NewEncryptedPool(16, &decryptorStub{}, nil)
	sender := types.HexToAddress("0x1234567890123456789012345678901234567890")
	tx2 := newEncryptedTx(5, 2, 10)
	tx2.Sender = sender
	tx1 := newEncryptedTx(5, 1, 10)
	tx1.Sender = sender
	if err := pool.Add(tx2); err != nil {
		t.Fatalf("Add(tx2) error = %v", err)
	}
	if err := pool.Add(tx1); err != nil {
		t.Fatalf("Add(tx1) error = %v", err)
	}

	txs := pool.GetBySender(sender)
	if len(txs) != 2 {
		t.Fatalf("GetBySender() len = %d, want 2", len(txs))
	}
	if txs[0].Nonce != 1 || txs[1].Nonce != 2 {
		t.Fatalf("nonces = [%d %d], want [1 2]", txs[0].Nonce, txs[1].Nonce)
	}
	txs[0].EncryptedData[0] = 0xff

	again := pool.GetBySender(sender)
	if again[0].EncryptedData[0] == 0xff {
		t.Fatal("GetBySender() returned internal slice without defensive copy")
	}
}

func TestEncryptedPoolDecryptForBlockRemovesSuccessfulTransactions(t *testing.T) {
	rawTx := transaction.NewTx(&transaction.LegacyTx{
		Nonce:    9,
		GasPrice: uint256.NewInt(3),
		Gas:      21000,
	})
	encoded, err := rawTx.Marshal()
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	pool := NewEncryptedPool(16, &decryptorStub{
		decryptFn: func(encryptedData []byte, encryptionKey []byte) ([]byte, error) {
			if string(encryptedData) == "fail" {
				return nil, errors.New("boom")
			}
			return encoded, nil
		},
	}, nil)

	okTx := newEncryptedTx(9, 1, 10)
	failTx := newEncryptedTx(9, 2, 5)
	failTx.EncryptedData = []byte("fail")
	if err := pool.Add(okTx); err != nil {
		t.Fatalf("Add(okTx) error = %v", err)
	}
	if err := pool.Add(failTx); err != nil {
		t.Fatalf("Add(failTx) error = %v", err)
	}

	decrypted, err := pool.DecryptForBlock(9)
	if err != nil {
		t.Fatalf("DecryptForBlock() error = %v", err)
	}
	if len(decrypted) != 1 {
		t.Fatalf("DecryptForBlock() len = %d, want 1", len(decrypted))
	}
	if pool.Len() != 1 {
		t.Fatalf("pool.Len() = %d, want 1 remaining failed tx", pool.Len())
	}
}

func newEncryptedTx(blockTarget, nonce uint64, gasPrice uint64) *EncryptedTx {
	return &EncryptedTx{
		ID:            types.HexToHash("0x" + stringHashByte(byte(blockTarget)) + stringHashByte(byte(nonce))),
		EncryptedData: []byte{0x01, 0x02, 0x03},
		EncryptionKey: []byte{0x04, 0x05, 0x06},
		Sender:        types.HexToAddress("0x1234567890123456789012345678901234567890"),
		GasLimit:      21000,
		GasPrice:      uint256.NewInt(gasPrice),
		SubmittedAt:   time.Unix(0, 0),
		BlockTarget:   blockTarget,
		Nonce:         nonce,
	}
}

func stringHashByte(b byte) string {
	const hexChars = "0123456789abcdef"
	buf := make([]byte, 64)
	for i := range buf {
		buf[i] = hexChars[int(b)%len(hexChars)]
	}
	return string(buf)
}
