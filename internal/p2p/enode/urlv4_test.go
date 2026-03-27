package enode

import (
	"fmt"
	"net"
	"testing"

	"github.com/n42blockchain/N42/crypto"
)

func TestParseV4RejectsHostWithoutResolvedIPs(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	pubkeyHex := fmt.Sprintf("%x", crypto.FromECDSAPub(&key.PublicKey)[1:])
	rawurl := fmt.Sprintf("enode://%s@example.org:30303", pubkeyHex)

	originalLookup := lookupIPFunc
	lookupIPFunc = func(string) ([]net.IP, error) { return nil, nil }
	defer func() { lookupIPFunc = originalLookup }()

	_, err = ParseV4(rawurl)
	if err == nil || err.Error() != "no IP addresses found for host" {
		t.Fatalf("ParseV4() error = %v, want no IP addresses found for host", err)
	}
}
