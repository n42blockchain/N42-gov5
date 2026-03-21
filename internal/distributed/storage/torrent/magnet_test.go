package torrent

import (
	"strings"
	"testing"

	"github.com/anacrolix/torrent/metainfo"
)

func TestFormatMagnetURI(t *testing.T) {
	var ih metainfo.Hash
	for i := range ih {
		ih[i] = byte(i)
	}

	uri := FormatMagnetURI(ih, "test_file.dat", []string{"udp://tracker.example.com:1234"})
	if !strings.HasPrefix(uri, "magnet:?") {
		t.Fatalf("not a magnet URI: %s", uri)
	}
	if !strings.Contains(uri, "xt=urn:btih:") {
		t.Fatalf("missing xt param: %s", uri)
	}
	if !strings.Contains(uri, "dn=test_file.dat") {
		t.Fatalf("missing name: %s", uri)
	}
}

func TestParseMagnetURI(t *testing.T) {
	var ih metainfo.Hash
	for i := range ih {
		ih[i] = byte(i * 3)
	}
	original := FormatMagnetURI(ih, "myfile.bin", nil)

	parsed, err := ParseMagnetURI(original)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.InfoHash != ih {
		t.Fatal("infohash mismatch")
	}
	if parsed.Name != "myfile.bin" {
		t.Fatalf("name = %q, want myfile.bin", parsed.Name)
	}
}

func TestParseMagnetURIErrors(t *testing.T) {
	tests := []string{
		"http://example.com",
		"magnet:?dn=test",
		"magnet:?xt=urn:tree:tiger:abc",
	}
	for _, uri := range tests {
		_, err := ParseMagnetURI(uri)
		if err == nil {
			t.Errorf("expected error for %q", uri)
		}
	}
}

func TestParseInfoHash(t *testing.T) {
	hexStr := "0102030405060708091011121314151617181920"
	ih, err := ParseInfoHash(hexStr)
	if err != nil {
		t.Fatal(err)
	}
	if ih[0] != 1 || ih[19] != 0x20 {
		t.Fatalf("unexpected infohash bytes")
	}
}

func TestParseInfoHashErrors(t *testing.T) {
	tests := []string{
		"abc",
		"zzzz0000000000000000000000000000000000000",
		"",
	}
	for _, s := range tests {
		_, err := ParseInfoHash(s)
		if err == nil {
			t.Errorf("expected error for %q", s)
		}
	}
}

func TestFormatMagnetNoTrackers(t *testing.T) {
	var ih metainfo.Hash
	ih[0] = 0xFF
	uri := FormatMagnetURI(ih, "", nil)
	if !strings.HasPrefix(uri, "magnet:?") {
		t.Fatal("invalid format")
	}
	if strings.Contains(uri, "dn=") {
		t.Fatal("should not have dn when name is empty")
	}
	if strings.Contains(uri, "tr=") {
		t.Fatal("should not have tr when no trackers")
	}
}
