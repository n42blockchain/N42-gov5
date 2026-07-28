package eth69

import "testing"

func TestProtocolLengthsCoverAdvertisedVersions(t *testing.T) {
	want := map[uint]uint64{
		ETH68: 17,
		ETH69: 18,
		ETH70: 18,
		ETH71: 20,
	}
	for _, version := range ProtocolVersions {
		got, err := GetProtocolLength(version)
		if err != nil {
			t.Fatalf("version %d: %v", version, err)
		}
		if got != want[version] {
			t.Fatalf("version %d length = %d, want %d", version, got, want[version])
		}
	}
	if ProtocolVersion != ETH71 {
		t.Fatalf("current protocol = %d, want eth/71", ProtocolVersion)
	}
}
