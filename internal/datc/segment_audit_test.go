package datc

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestAuditSegmentFooterRejectsCorruptionWithoutPanic(t *testing.T) {
	for _, mode := range []string{"negative-length", "frame-count", "truncated-varint", "key-length", "raw-overflow", "compressed-span", "trailing-footer"} {
		t.Run(mode, func(t *testing.T) {
			defer func() {
				if value := recover(); value != nil {
					t.Errorf("malformed segment panicked: %v", value)
				}
			}()
			foot := []byte{1, 1, 1, 1, 'k'}
			var body []byte
			body = append(body, []byte(leafSegMagic)...)
			body = append(body, 0)
			switch mode {
			case "frame-count":
				foot = binary.AppendUvarint(nil, ^uint64(0))
			case "truncated-varint":
				foot = []byte{1, 0x80}
			case "key-length":
				foot = []byte{1, 1, 1, 127}
			case "raw-overflow":
				foot = []byte{1, 1}
				foot = binary.AppendUvarint(foot, uint64(1)<<32)
				foot = append(foot, 1, 'k')
			case "compressed-span":
				foot = []byte{1, 127, 1, 1, 'k'}
			case "trailing-footer":
				foot = append(foot, 0)
			}
			body = append(body, foot...)
			var tail [16]byte
			binary.BigEndian.PutUint64(tail[:8], uint64(len(foot)))
			if mode == "negative-length" {
				binary.BigEndian.PutUint64(tail[:8], ^uint64(0))
			}
			copy(tail[8:], leafSegMagic)
			body = append(body, tail[:]...)
			path := filepath.Join(t.TempDir(), "bad.seg")
			if err := os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if _, err := loadLeafSegFile(f); err == nil {
				t.Fatal("malformed segment accepted")
			}
		})
	}
}

func TestAuditSegmentFrameRecordBounds(t *testing.T) {
	for _, raw := range [][]byte{{0x80}, {127, 1}, {1, 'k', 0x80}, {1, 'k', 127, 1}, {1, 'k', 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 2}} {
		if _, err := decodeFrameOffsets(raw); err == nil {
			t.Fatalf("malformed record accepted: %x", raw)
		}
	}
	if offsets, err := decodeFrameOffsets([]byte{1, 'k', 0, 1, 'z', 1, 'v'}); err != nil || len(offsets) != 2 || offsets[1] != 3 {
		t.Fatalf("valid rows rejected: %v %v", offsets, err)
	}
}
