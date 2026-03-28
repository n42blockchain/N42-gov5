package params

import "testing"

func TestBeaconRootsCode(t *testing.T) {
	if len(BeaconRootsCode) == 0 {
		t.Fatal("BeaconRootsCode should not be empty")
	}
}
