package v5wire

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"strings"
	"testing"
)

func TestEncryptGCMRejectsInvalidKeySize(t *testing.T) {
	_, err := encryptGCM(nil, []byte("short"), make([]byte, gcmNonceSize), []byte("payload"), nil)
	if err == nil {
		t.Fatal("expected invalid key size error")
	}
	if !strings.Contains(err.Error(), "can't create block cipher") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEncryptDecryptGCMRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{0x11}, aesKeySize)
	nonce := bytes.Repeat([]byte{0x22}, gcmNonceSize)
	plaintext := []byte("hello v5wire")
	authData := []byte("header")

	ct, err := encryptGCM(nil, key, nonce, plaintext, authData)
	if err != nil {
		t.Fatalf("encryptGCM returned error: %v", err)
	}

	got, err := decryptGCM(key, nonce, ct, authData)
	if err != nil {
		t.Fatalf("decryptGCM returned error: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("plaintext = %x, want %x", got, plaintext)
	}
}

func TestEncodePubkeyNilReturnsNil(t *testing.T) {
	if got := EncodePubkey(nil); got != nil {
		t.Fatalf("EncodePubkey(nil) = %x, want nil", got)
	}
}

func TestEncodePubkeyUnsupportedCurveReturnsNil(t *testing.T) {
	if got := EncodePubkey(&ecdsa.PublicKey{Curve: elliptic.P256()}); got != nil {
		t.Fatalf("EncodePubkey(P256) = %x, want nil", got)
	}
}
