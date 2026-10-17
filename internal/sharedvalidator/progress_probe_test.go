package sharedvalidator

import "testing"

func TestCanonicalProbeHeaderRejectsMalformedLocalEncoding(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  []byte
	}{
		{"illegal protobuf", []byte{0xff}},
		{"missing nested fixed width fields", []byte{0x0a, 0x00}},
		{"missing all fields", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			header, err := decodeCanonicalProbeHeader(tc.raw)
			if err == nil || header != nil {
				t.Fatalf("malformed local header decoded: header=%v err=%v", header, err)
			}
		})
	}
}
