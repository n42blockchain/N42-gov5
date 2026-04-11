// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Magnet URI parsing for the torrent bridge. MagnetParams captures
// InfoHash, Name, Trackers and WebSeeds extracted from a
// magnet:?xt=urn:btih:... URI, so CAS content can be published or
// resolved via compact magnet links.

package torrent

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/anacrolix/torrent/metainfo"
)

// MagnetParams holds parsed magnet link parameters.
type MagnetParams struct {
	InfoHash metainfo.Hash
	Name     string
	Trackers []string
	WebSeeds []string
}

// ParseMagnetURI parses a magnet:?xt=urn:btih:... URI.
func ParseMagnetURI(uri string) (*MagnetParams, error) {
	if !strings.HasPrefix(uri, "magnet:?") {
		return nil, fmt.Errorf("not a magnet URI")
	}

	u, err := url.Parse(uri)
	if err != nil {
		return nil, fmt.Errorf("parse magnet: %w", err)
	}

	params := u.Query()
	xt := params.Get("xt")
	if !strings.HasPrefix(xt, "urn:btih:") {
		return nil, fmt.Errorf("magnet: missing urn:btih")
	}

	hashStr := strings.TrimPrefix(xt, "urn:btih:")
	ih, err := ParseInfoHash(hashStr)
	if err != nil {
		return nil, fmt.Errorf("magnet infohash: %w", err)
	}

	mp := &MagnetParams{
		InfoHash: ih,
		Name:     params.Get("dn"),
		Trackers: params["tr"],
		WebSeeds: params["ws"],
	}

	return mp, nil
}

// FormatMagnetURI creates a magnet URI from components.
// Uses raw string construction to avoid URL-encoding colons in urn:btih:,
// which is the standard magnet URI format per BEP 9.
func FormatMagnetURI(ih metainfo.Hash, name string, trackers []string) string {
	uri := fmt.Sprintf("magnet:?xt=urn:btih:%s", ih.HexString())
	if name != "" {
		uri += "&dn=" + url.QueryEscape(name)
	}
	for _, tr := range trackers {
		uri += "&tr=" + url.QueryEscape(tr)
	}
	return uri
}

// MagnetFromMetaInfo creates a magnet URI from torrent metainfo.
func MagnetFromMetaInfo(mi *metainfo.MetaInfo) string {
	ih := mi.HashInfoBytes()
	info, err := mi.UnmarshalInfo()
	name := ""
	if err == nil {
		name = info.Name
	}

	var trackers []string
	for _, tier := range mi.AnnounceList {
		trackers = append(trackers, tier...)
	}
	if mi.Announce != "" {
		trackers = append(trackers, mi.Announce)
	}

	return FormatMagnetURI(ih, name, trackers)
}
