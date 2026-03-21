// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ed2k

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Link represents a parsed ed2k file link.
//
// Format: ed2k://|file|<name>|<size>|<hash>|/
// Optional extensions: |h=<root_hash>|/ |p=<part_hashes>|/ |s=<url>|/
type Link struct {
	Name     string
	Size     int64
	Hash     [HashSize]byte
	RootHash []byte // optional AICH root hash
	Sources  string // optional source URL
}

// ParseLink parses an ed2k:// file link string.
func ParseLink(uri string) (*Link, error) {
	if !strings.HasPrefix(uri, "ed2k://|file|") {
		return nil, fmt.Errorf("ed2k: not a file link")
	}

	// Strip prefix and trailing "|/" or "|"
	body := strings.TrimPrefix(uri, "ed2k://|file|")
	body = strings.TrimSuffix(body, "/")
	body = strings.TrimSuffix(body, "|")

	parts := strings.Split(body, "|")
	if len(parts) < 3 {
		return nil, fmt.Errorf("ed2k: link has %d parts, need at least 3", len(parts))
	}

	// Decode URL-encoded name
	name, err := url.PathUnescape(parts[0])
	if err != nil {
		name = parts[0] // fallback to raw
	}
	if name == "" {
		return nil, fmt.Errorf("ed2k: empty filename")
	}

	size, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || size < 0 {
		return nil, fmt.Errorf("ed2k: invalid size %q", parts[1])
	}

	hashBytes, err := hex.DecodeString(strings.ToLower(parts[2]))
	if err != nil || len(hashBytes) != HashSize {
		return nil, fmt.Errorf("ed2k: invalid hash %q", parts[2])
	}

	link := &Link{
		Name: name,
		Size: size,
	}
	copy(link.Hash[:], hashBytes)

	// Parse optional extensions
	for i := 3; i < len(parts); i++ {
		ext := parts[i]
		switch {
		case strings.HasPrefix(ext, "h="):
			rootHash, err := hex.DecodeString(ext[2:])
			if err == nil {
				link.RootHash = rootHash
			}
		case strings.HasPrefix(ext, "s="):
			link.Sources = ext[2:]
		}
	}

	return link, nil
}

// String formats the Link as an ed2k:// URI.
func (l *Link) String() string {
	hashHex := strings.ToUpper(hex.EncodeToString(l.Hash[:]))
	escapedName := url.PathEscape(l.Name)

	uri := fmt.Sprintf("ed2k://|file|%s|%d|%s|", escapedName, l.Size, hashHex)

	if len(l.RootHash) > 0 {
		uri += fmt.Sprintf("h=%s|", strings.ToUpper(hex.EncodeToString(l.RootHash)))
	}

	if l.Sources != "" {
		uri += fmt.Sprintf("s=%s|", l.Sources)
	}

	return uri + "/"
}

// FormatLink creates an ed2k link from file metadata.
func FormatLink(name string, size int64, hash [HashSize]byte) string {
	l := &Link{Name: name, Size: size, Hash: hash}
	return l.String()
}
