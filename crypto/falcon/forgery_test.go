package falcon

import (
	"crypto/rand"
	"testing"
)

func TestPublicStructureWithoutSecretDoesNotVerify(t *testing.T) {
	pk, _, err := GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	fake := make([]byte, SignatureSize)
	fake[0] = 0x39
	for i := 41; i < 105; i++ {
		fake[i] = byte(i)
	}
	if Verify(pk, []byte("payment"), fake) {
		t.Fatal("public structure accepted without a Falcon signature")
	}
}
func TestMalformedImportedSecretIsRejected(t *testing.T) {
	raw := make([]byte, PrivateKeySize)
	raw[0] = 0x59
	if _, err := PrivateKeyFromBytes(raw); err == nil {
		t.Fatal("zero NTRU trapdoor accepted")
	}
}
