package api

import "testing"

func TestValidateLogsBloomRequiresZeroBloomFromUntrustedPayload(t *testing.T) {
	actual := []byte{0x01}
	expected := []byte{0x00}
	if err := validateLogsBloom(actual, expected, false); err == nil {
		t.Fatal("untrusted zero bloom must not bypass validation")
	}
}

func TestValidateLogsBloomAllowsMissingTrustedColumnarBloom(t *testing.T) {
	if err := validateLogsBloom([]byte{0x01}, []byte{0x00}, true); err != nil {
		t.Fatalf("trusted columnar bloom should be reconstructable: %v", err)
	}
}

func TestValidateLogsBloomStillChecksPresentTrustedBloom(t *testing.T) {
	if err := validateLogsBloom([]byte{0x01}, []byte{0x02}, true); err == nil {
		t.Fatal("trusted path must validate a bloom that is present")
	}
}
