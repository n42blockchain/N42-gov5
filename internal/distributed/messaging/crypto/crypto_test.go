package crypto

import (
	"bytes"
	"fmt"
	"testing"

	ncrypto "github.com/n42blockchain/N42/common/crypto"
)

func TestGenerateKeyPair(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	var zero [32]byte
	if kp.PublicKey == zero {
		t.Fatal("PublicKey should not be zero")
	}
	if kp.PrivateKey == zero {
		t.Fatal("PrivateKey should not be zero")
	}

	// Two generated key pairs should be different.
	kp2, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair (2nd): %v", err)
	}
	if kp.PrivateKey == kp2.PrivateKey {
		t.Fatal("two generated key pairs should have different private keys")
	}
	if kp.PublicKey == kp2.PublicKey {
		t.Fatal("two generated key pairs should have different public keys")
	}
}

func TestDeriveFromWallet(t *testing.T) {
	walletKey, err := ncrypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	kp1, err := DeriveFromWallet(walletKey)
	if err != nil {
		t.Fatalf("DeriveFromWallet: %v", err)
	}

	var zero [32]byte
	if kp1.PublicKey == zero {
		t.Fatal("derived PublicKey should not be zero")
	}
	if kp1.PrivateKey == zero {
		t.Fatal("derived PrivateKey should not be zero")
	}

	// Same wallet key produces same messaging key (deterministic).
	kp2, err := DeriveFromWallet(walletKey)
	if err != nil {
		t.Fatalf("DeriveFromWallet (2nd): %v", err)
	}
	if kp1.PrivateKey != kp2.PrivateKey {
		t.Fatal("same wallet key should produce same messaging private key")
	}
	if kp1.PublicKey != kp2.PublicKey {
		t.Fatal("same wallet key should produce same messaging public key")
	}

	// Different wallet key produces different messaging key.
	walletKey2, err := ncrypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey (2nd): %v", err)
	}
	kp3, err := DeriveFromWallet(walletKey2)
	if err != nil {
		t.Fatalf("DeriveFromWallet (different wallet): %v", err)
	}
	if kp1.PrivateKey == kp3.PrivateKey {
		t.Fatal("different wallet keys should produce different messaging keys")
	}
}

func TestDeriveFromWalletNil(t *testing.T) {
	_, err := DeriveFromWallet(nil)
	if err == nil {
		t.Fatal("expected error for nil wallet key")
	}
}

func TestSealOpenEnvelope(t *testing.T) {
	recipient, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	plaintext := []byte("secret message for the recipient")

	enc, err := SealEnvelope(plaintext, recipient.PublicKey)
	if err != nil {
		t.Fatalf("SealEnvelope: %v", err)
	}

	if enc == nil {
		t.Fatal("encrypted envelope should not be nil")
	}
	if len(enc.Ciphertext) == 0 {
		t.Fatal("ciphertext should not be empty")
	}

	// Open with recipient's private key.
	decrypted, err := OpenEnvelope(enc, recipient.PrivateKey)
	if err != nil {
		t.Fatalf("OpenEnvelope: %v", err)
	}

	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("decrypted = %q, want %q", decrypted, plaintext)
	}
}

func TestSealOpenDifferentKey(t *testing.T) {
	recipient, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair (recipient): %v", err)
	}

	wrongKey, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair (wrong): %v", err)
	}

	plaintext := []byte("this should only be readable by the recipient")

	enc, err := SealEnvelope(plaintext, recipient.PublicKey)
	if err != nil {
		t.Fatalf("SealEnvelope: %v", err)
	}

	// Try to open with the wrong key.
	_, err = OpenEnvelope(enc, wrongKey.PrivateKey)
	if err == nil {
		t.Fatal("expected error when opening with wrong key")
	}
}

func TestECDH(t *testing.T) {
	alice, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair (alice): %v", err)
	}
	bob, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair (bob): %v", err)
	}

	// Alice computes shared secret with Bob's public key.
	sharedAB, err := ECDH(alice.PrivateKey, bob.PublicKey)
	if err != nil {
		t.Fatalf("ECDH (alice->bob): %v", err)
	}

	// Bob computes shared secret with Alice's public key.
	sharedBA, err := ECDH(bob.PrivateKey, alice.PublicKey)
	if err != nil {
		t.Fatalf("ECDH (bob->alice): %v", err)
	}

	// Both sides should derive the same shared secret.
	if sharedAB != sharedBA {
		t.Fatal("ECDH shared secrets should match")
	}

	// Shared secret should not be zero.
	var zero [32]byte
	if sharedAB == zero {
		t.Fatal("shared secret should not be zero")
	}
}

func TestSessionEncryptDecrypt(t *testing.T) {
	alice, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair (alice): %v", err)
	}
	bob, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair (bob): %v", err)
	}

	// Both sides create a session.
	aliceSession, err := InitiateSession(alice.PrivateKey, bob.PublicKey)
	if err != nil {
		t.Fatalf("InitiateSession (alice): %v", err)
	}
	bobSession, err := InitiateSession(bob.PrivateKey, alice.PublicKey)
	if err != nil {
		t.Fatalf("InitiateSession (bob): %v", err)
	}

	// They should have the same session ID.
	if aliceSession.SessionID != bobSession.SessionID {
		t.Fatal("session IDs should match")
	}

	// Alice encrypts, Bob decrypts.
	plaintext := []byte("hello bob, this is alice")
	ciphertext, err := aliceSession.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	decrypted, err := bobSession.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}

	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("decrypted = %q, want %q", decrypted, plaintext)
	}
}

func TestSessionForwardSecrecy(t *testing.T) {
	alice, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair (alice): %v", err)
	}
	bob, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair (bob): %v", err)
	}

	aliceSession, err := InitiateSession(alice.PrivateKey, bob.PublicKey)
	if err != nil {
		t.Fatalf("InitiateSession: %v", err)
	}

	// Encrypt the same plaintext 3 times. Each should produce different ciphertext
	// because different message keys are used due to chain key ratcheting.
	plaintext := []byte("same message")
	ciphertexts := make([][]byte, 3)
	for i := 0; i < 3; i++ {
		ct, err := aliceSession.Encrypt(plaintext)
		if err != nil {
			t.Fatalf("Encrypt %d: %v", i, err)
		}
		ciphertexts[i] = ct
	}

	// Verify all ciphertexts are different.
	for i := 0; i < len(ciphertexts); i++ {
		for j := i + 1; j < len(ciphertexts); j++ {
			if bytes.Equal(ciphertexts[i], ciphertexts[j]) {
				t.Fatalf("ciphertext %d and %d are identical (no forward secrecy)", i, j)
			}
		}
	}
}

func TestSessionMultipleMessages(t *testing.T) {
	alice, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair (alice): %v", err)
	}
	bob, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair (bob): %v", err)
	}

	aliceSession, err := InitiateSession(alice.PrivateKey, bob.PublicKey)
	if err != nil {
		t.Fatalf("InitiateSession (alice): %v", err)
	}
	bobSession, err := InitiateSession(bob.PrivateKey, alice.PublicKey)
	if err != nil {
		t.Fatalf("InitiateSession (bob): %v", err)
	}

	// Send 10 messages from Alice to Bob.
	for i := 0; i < 10; i++ {
		msg := []byte(fmt.Sprintf("message number %d", i))
		ct, err := aliceSession.Encrypt(msg)
		if err != nil {
			t.Fatalf("Encrypt msg %d: %v", i, err)
		}
		pt, err := bobSession.Decrypt(ct)
		if err != nil {
			t.Fatalf("Decrypt msg %d: %v", i, err)
		}
		if !bytes.Equal(pt, msg) {
			t.Fatalf("msg %d: decrypted = %q, want %q", i, pt, msg)
		}
	}
}

func TestSessionExportImport(t *testing.T) {
	alice, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair (alice): %v", err)
	}
	bob, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair (bob): %v", err)
	}

	aliceSession, err := InitiateSession(alice.PrivateKey, bob.PublicKey)
	if err != nil {
		t.Fatalf("InitiateSession (alice): %v", err)
	}
	bobSession, err := InitiateSession(bob.PrivateKey, alice.PublicKey)
	if err != nil {
		t.Fatalf("InitiateSession (bob): %v", err)
	}

	// Send a few messages to advance the ratchet state.
	for i := 0; i < 3; i++ {
		ct, err := aliceSession.Encrypt([]byte(fmt.Sprintf("pre-export %d", i)))
		if err != nil {
			t.Fatalf("Encrypt pre-export %d: %v", i, err)
		}
		if _, err := bobSession.Decrypt(ct); err != nil {
			t.Fatalf("Decrypt pre-export %d: %v", i, err)
		}
	}

	// Export Alice's session state.
	state := aliceSession.Export()
	if state.SessionID != aliceSession.SessionID {
		t.Fatal("exported SessionID mismatch")
	}
	if state.SendCounter != 3 {
		t.Fatalf("exported SendCounter = %d, want 3", state.SendCounter)
	}

	// Import into a new session object.
	restoredAlice := ImportSession(state)

	// Continue encrypting with the restored session.
	postExportMsg := []byte("message after export/import")
	ct, err := restoredAlice.Encrypt(postExportMsg)
	if err != nil {
		t.Fatalf("Encrypt post-import: %v", err)
	}

	// Bob should be able to decrypt.
	pt, err := bobSession.Decrypt(ct)
	if err != nil {
		t.Fatalf("Decrypt post-import: %v", err)
	}
	if !bytes.Equal(pt, postExportMsg) {
		t.Fatalf("post-import decrypted = %q, want %q", pt, postExportMsg)
	}
}

func TestKeyStore(t *testing.T) {
	localKey, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	ks := NewKeyStore(localKey)

	// LocalKey should return the key we passed.
	if ks.LocalKey().PublicKey != localKey.PublicKey {
		t.Fatal("LocalKey mismatch")
	}

	// Add a peer bundle.
	bundle := &KeyBundle{
		IdentityKey:  [32]byte{1, 2, 3},
		SignedPreKey: [32]byte{4, 5, 6},
		OneTimeKeys:  [][32]byte{{7, 8, 9}},
	}
	ks.AddPeerBundle("peer-alice", bundle)

	// Get it back.
	got, ok := ks.GetPeerBundle("peer-alice")
	if !ok {
		t.Fatal("expected to find peer-alice bundle")
	}
	if got.IdentityKey != bundle.IdentityKey {
		t.Fatal("IdentityKey mismatch")
	}
	if got.SignedPreKey != bundle.SignedPreKey {
		t.Fatal("SignedPreKey mismatch")
	}

	// Unknown peer returns false.
	_, ok = ks.GetPeerBundle("peer-unknown")
	if ok {
		t.Fatal("expected false for unknown peer")
	}

	// Remove peer bundle.
	ks.RemovePeerBundle("peer-alice")
	_, ok = ks.GetPeerBundle("peer-alice")
	if ok {
		t.Fatal("expected peer-alice to be removed")
	}

	// Removing a non-existent peer should not panic.
	ks.RemovePeerBundle("non-existent")
}
