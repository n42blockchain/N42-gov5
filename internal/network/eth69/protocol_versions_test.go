package eth69

import (
	"reflect"
	"testing"
)

// The node advertises only the eth versions it speaks to spec. eth/70
// (EIP-7975 partial receipts) and eth/71 (EIP-7928 block access lists) stay
// off until they are implemented; geth, reth and erigon all speak eth/69.
func TestAdvertisedProtocolVersionsArePinned(t *testing.T) {
	if want := []uint{ETH69, ETH68}; !reflect.DeepEqual(ProtocolVersions, want) {
		t.Fatalf("ProtocolVersions = %v, want %v", ProtocolVersions, want)
	}
	if ProtocolVersion != ETH69 {
		t.Fatalf("current protocol = %d, want eth/69", ProtocolVersion)
	}
	for _, v := range []uint{ETH70, ETH71} {
		if IsProtocolVersionSupported(v) {
			t.Fatalf("eth/%d must not be supported", v)
		}
		if _, err := GetProtocolLength(v); err == nil {
			t.Fatalf("eth/%d must have no protocol length", v)
		}
	}
}

// The protocol length bounds the message codes a peer may send: devp2p rejects
// any code at or above it. Every advertised version must cover receipts and,
// from eth/69, BlockRangeUpdate, and must exclude the eth/71 BAL messages.
func TestProtocolLengthsCoverAdvertisedVersions(t *testing.T) {
	want := map[uint]uint64{
		ETH68: 17,
		ETH69: 18,
	}
	for _, version := range ProtocolVersions {
		got, err := GetProtocolLength(version)
		if err != nil {
			t.Fatalf("version %d: %v", version, err)
		}
		if got != want[version] {
			t.Fatalf("version %d length = %d, want %d", version, got, want[version])
		}
		if uint64(ReceiptsMsg) >= got || uint64(GetReceiptsMsg) >= got {
			t.Fatalf("version %d length %d excludes the receipts messages", version, got)
		}
		if version >= ETH69 && uint64(BlockRangeUpdateMsg) >= got {
			t.Fatalf("version %d length %d excludes BlockRangeUpdate", version, got)
		}
		if uint64(GetBlockAccessListsMsg) < got || uint64(BlockAccessListsMsg) < got {
			t.Fatalf("version %d length %d admits the eth/71 BAL messages", version, got)
		}
	}
}
