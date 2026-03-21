package torrent

import (
	"crypto/sha1"
	"encoding/hex"
	"testing"

	"github.com/anacrolix/torrent/metainfo"
)

func TestCreateTorrent(t *testing.T) {
	bridge := &Bridge{mappings: make(map[[32]byte]*HashMapping)}

	data := []byte("hello torrent bridge test data content")
	mi, ih, err := bridge.CreateTorrent("test.txt", data, 256*1024)
	if err != nil {
		t.Fatal(err)
	}
	if mi == nil {
		t.Fatal("metainfo is nil")
	}
	if ih == (metainfo.Hash{}) {
		t.Fatal("empty infohash")
	}

	// Verify metainfo contents
	info, err := mi.UnmarshalInfo()
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "test.txt" {
		t.Fatalf("name = %q, want test.txt", info.Name)
	}
	if info.TotalLength() != int64(len(data)) {
		t.Fatalf("length = %d, want %d", info.TotalLength(), len(data))
	}
	if info.PieceLength != 256*1024 {
		t.Fatalf("piece length = %d", info.PieceLength)
	}
	// Pieces should contain one SHA1 hash (20 bytes) since data fits in one piece
	if len(info.Pieces) != sha1.Size {
		t.Fatalf("pieces len = %d, want %d", len(info.Pieces), sha1.Size)
	}
}

func TestCreateTorrentSmallPieces(t *testing.T) {
	bridge := &Bridge{mappings: make(map[[32]byte]*HashMapping)}
	data := make([]byte, 1000)
	for i := range data {
		data[i] = byte(i)
	}

	mi, _, err := bridge.CreateTorrent("multi.bin", data, 100)
	if err != nil {
		t.Fatal(err)
	}

	info, _ := mi.UnmarshalInfo()
	// 1000 bytes / 100 byte pieces = 10 pieces
	expectedPieces := 10
	if len(info.Pieces) != expectedPieces*sha1.Size {
		t.Fatalf("pieces len = %d, want %d", len(info.Pieces), expectedPieces*sha1.Size)
	}
}

func TestCreateTorrentDefaultPieceSize(t *testing.T) {
	bridge := &Bridge{mappings: make(map[[32]byte]*HashMapping)}
	data := []byte("default piece size test")

	mi, _, err := bridge.CreateTorrent("default.bin", data, 0) // pieceSize <= 0 triggers default
	if err != nil {
		t.Fatal(err)
	}

	info, _ := mi.UnmarshalInfo()
	if info.PieceLength != 256*1024 {
		t.Fatalf("default piece length = %d, want %d", info.PieceLength, 256*1024)
	}
}

func TestCreateTorrentEmptyData(t *testing.T) {
	bridge := &Bridge{mappings: make(map[[32]byte]*HashMapping)}
	_, _, err := bridge.CreateTorrent("empty.bin", nil, 256*1024)
	if err == nil {
		t.Fatal("expected error for empty data")
	}
}

func TestBridgeMappingCRUD(t *testing.T) {
	bridge := &Bridge{mappings: make(map[[32]byte]*HashMapping)}

	var ch [32]byte
	ch[0] = 0xAA
	mapping := &HashMapping{
		ContentHash: ch,
		InfoHash:    metainfo.Hash{1, 2, 3},
	}

	// Add
	bridge.AddMapping(mapping)

	// Get
	m, ok := bridge.GetMapping(ch)
	if !ok {
		t.Fatal("mapping not found")
	}
	if m.InfoHash != mapping.InfoHash {
		t.Fatal("infohash mismatch")
	}

	// InfoHashFromContentHash
	ih, ok := bridge.InfoHashFromContentHash(ch)
	if !ok {
		t.Fatal("infohash lookup failed")
	}
	if ih != mapping.InfoHash {
		t.Fatal("infohash mismatch")
	}

	// ContentHashFromInfoHash (reverse)
	found, ok := bridge.ContentHashFromInfoHash(mapping.InfoHash)
	if !ok {
		t.Fatal("reverse lookup failed")
	}
	if found != ch {
		t.Fatal("content hash mismatch")
	}

	// Not found cases
	_, ok = bridge.GetMapping([32]byte{0xFF})
	if ok {
		t.Fatal("should not find unmapped hash")
	}
	_, ok = bridge.InfoHashFromContentHash([32]byte{0xFF})
	if ok {
		t.Fatal("should not find unmapped infohash")
	}
	_, ok = bridge.ContentHashFromInfoHash(metainfo.Hash{0xFF})
	if ok {
		t.Fatal("should not find unmapped reverse")
	}
}

func TestBridgeFormatMagnet(t *testing.T) {
	bridge := &Bridge{mappings: make(map[[32]byte]*HashMapping)}

	var ch [32]byte
	ch[0] = 0xBB
	bridge.AddMapping(&HashMapping{
		ContentHash: ch,
		InfoHash:    metainfo.Hash{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x13, 0x14},
	})

	magnet, err := bridge.FormatMagnet(ch)
	if err != nil {
		t.Fatal(err)
	}
	if magnet == "" {
		t.Fatal("empty magnet")
	}
	t.Logf("magnet: %s", magnet)
}

func TestBridgeFormatMagnetNotFound(t *testing.T) {
	bridge := &Bridge{mappings: make(map[[32]byte]*HashMapping)}
	_, err := bridge.FormatMagnet([32]byte{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParseInfoHashWithPrefix(t *testing.T) {
	ih, err := ParseInfoHash("0x0102030405060708091011121314151617181920")
	if err != nil {
		t.Fatal(err)
	}
	if ih[0] != 1 {
		t.Fatal("wrong first byte")
	}
}

func TestMagnetFromMetaInfo(t *testing.T) {
	bridge := &Bridge{mappings: make(map[[32]byte]*HashMapping)}

	data := []byte("magnet from metainfo test")
	mi, _, err := bridge.CreateTorrent("test.dat", data, 1024)
	if err != nil {
		t.Fatal(err)
	}

	magnet := MagnetFromMetaInfo(mi)
	if magnet == "" {
		t.Fatal("empty magnet")
	}

	// Should round-trip parse
	parsed, err := ParseMagnetURI(magnet)
	if err != nil {
		t.Fatal(err)
	}
	expectedIH := mi.HashInfoBytes()
	if parsed.InfoHash != expectedIH {
		t.Fatalf("infohash: %s != %s",
			hex.EncodeToString(parsed.InfoHash[:]),
			hex.EncodeToString(expectedIH[:]))
	}
}
