// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package storage

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/n42blockchain/N42/internal/distributed/storage/ed2k"
	dtorrent "github.com/n42blockchain/N42/internal/distributed/storage/torrent"
	"github.com/n42blockchain/N42/log"
)

// Protocol identifies a content retrieval protocol.
type Protocol string

const (
	ProtocolCAS        Protocol = "cas"        // on-chain CAS precompile
	ProtocolIPFS       Protocol = "ipfs"       // IPFS/Filecoin
	ProtocolBitTorrent Protocol = "bittorrent" // BitTorrent
	ProtocolEd2k       Protocol = "ed2k"       // eDonkey2000
)

// ContentResult holds the result of a content resolution.
type ContentResult struct {
	Data        []byte
	Protocol    Protocol
	ContentHash [32]byte
	ResolvedAt  time.Time
}

// ContentInfo aggregates all protocol identifiers for a piece of content.
type ContentInfo struct {
	ContentHash [32]byte // keccak256
	CID         string   // IPFS CIDv1
	InfoHash    string   // BitTorrent infohash (hex)
	Ed2kHash    string   // ed2k hash (hex)
	MagnetURI   string   // magnet link
	Ed2kLink    string   // ed2k:// link
	Size        int64
	Name        string
}

// UniversalResolver resolves content across multiple protocols with fallback.
// Resolution order: CAS (on-chain) -> IPFS -> BitTorrent -> ed2k
type UniversalResolver struct {
	ipfsBridge    *Bridge
	torrentBridge *dtorrent.Bridge
	ed2kBridge    *ed2k.Bridge
	contentBridge *ContentBridge

	// CAS data provider (typically backed by MDBX)
	casGet func(contentHash [32]byte) ([]byte, error)
}

// NewUniversalResolver creates a resolver with the given backends.
// Any backend can be nil (disabled).
func NewUniversalResolver(
	ipfs *Bridge,
	tb *dtorrent.Bridge,
	eb *ed2k.Bridge,
	cb *ContentBridge,
	casGet func(contentHash [32]byte) ([]byte, error),
) *UniversalResolver {
	return &UniversalResolver{
		ipfsBridge:    ipfs,
		torrentBridge: tb,
		ed2kBridge:    eb,
		contentBridge: cb,
		casGet:        casGet,
	}
}

// Resolve attempts to retrieve content by keccak256 hash using all available
// protocols in order: CAS -> IPFS -> BitTorrent.
func (ur *UniversalResolver) Resolve(ctx context.Context, contentHash [32]byte) (*ContentResult, error) {
	hashHex := hex.EncodeToString(contentHash[:8]) + "..."

	// 1. Try on-chain CAS
	if ur.casGet != nil {
		data, err := ur.casGet(contentHash)
		if err == nil && len(data) > 0 {
			return &ContentResult{
				Data:        data,
				Protocol:    ProtocolCAS,
				ContentHash: contentHash,
				ResolvedAt:  time.Now(),
			}, nil
		}
	}

	// 2. Try IPFS
	if ur.ipfsBridge != nil && ur.contentBridge != nil {
		cid := ur.contentBridge.HashToCID(contentHash[:])
		if cid != "" {
			data, err := ur.ipfsBridge.Get(ctx, cid)
			if err == nil && len(data) > 0 {
				log.Debug("Content resolved via IPFS", "hash", hashHex, "cid", cid)
				return &ContentResult{
					Data:        data,
					Protocol:    ProtocolIPFS,
					ContentHash: contentHash,
					ResolvedAt:  time.Now(),
				}, nil
			}
		}
	}

	// 3. Try BitTorrent
	if ur.torrentBridge != nil {
		data, err := ur.torrentBridge.FetchByContentHash(ctx, contentHash)
		if err == nil && len(data) > 0 {
			log.Debug("Content resolved via BitTorrent", "hash", hashHex)
			return &ContentResult{
				Data:        data,
				Protocol:    ProtocolBitTorrent,
				ContentHash: contentHash,
				ResolvedAt:  time.Now(),
			}, nil
		}
	}

	return nil, fmt.Errorf("content %s not found via any protocol", hashHex)
}

// Register indexes content across all enabled protocols.
// Call this when content is stored via CAS to make it discoverable.
func (ur *UniversalResolver) Register(contentHash [32]byte, name string, data []byte, pieceSize int) (*ContentInfo, error) {
	info := &ContentInfo{
		ContentHash: contentHash,
		Size:        int64(len(data)),
		Name:        name,
	}

	// Generate IPFS CID
	if ur.contentBridge != nil {
		info.CID = ur.contentBridge.HashToCID(contentHash[:])
	}

	// Create torrent and start seeding
	if ur.torrentBridge != nil {
		mapping, err := ur.torrentBridge.SeedContent(context.Background(), contentHash, name, data, pieceSize)
		if err == nil {
			info.InfoHash = mapping.InfoHash.HexString()
			magnet, _ := ur.torrentBridge.FormatMagnet(contentHash)
			info.MagnetURI = magnet
		} else {
			log.Warn("Failed to create torrent", "hash", hex.EncodeToString(contentHash[:8])+"...", "err", err)
		}
	}

	// Compute ed2k hash
	if ur.ed2kBridge != nil {
		mapping, err := ur.ed2kBridge.ComputeAndMap(contentHash, name, data)
		if err == nil {
			info.Ed2kHash = hex.EncodeToString(mapping.Ed2kHash[:])
			link, _ := ur.ed2kBridge.FormatLink(contentHash)
			info.Ed2kLink = link
		}
	}

	return info, nil
}

// GetInfo returns all protocol identifiers for a content hash without fetching data.
func (ur *UniversalResolver) GetInfo(contentHash [32]byte) *ContentInfo {
	info := &ContentInfo{ContentHash: contentHash}

	if ur.contentBridge != nil {
		info.CID = ur.contentBridge.HashToCID(contentHash[:])
	}

	if ur.torrentBridge != nil {
		if ih, ok := ur.torrentBridge.InfoHashFromContentHash(contentHash); ok {
			info.InfoHash = ih.HexString()
			magnet, _ := ur.torrentBridge.FormatMagnet(contentHash)
			info.MagnetURI = magnet
		}
	}

	if ur.ed2kBridge != nil {
		if ed2kHash, ok := ur.ed2kBridge.Ed2kHashFromContentHash(contentHash); ok {
			info.Ed2kHash = hex.EncodeToString(ed2kHash[:])
			link, _ := ur.ed2kBridge.FormatLink(contentHash)
			info.Ed2kLink = link
		}
	}

	return info
}

// ResolveByURI resolves content from a protocol-specific URI string.
// Supports: ipfs://<cid>, magnet:?..., ed2k://|file|..., 0x<keccak256>
func (ur *UniversalResolver) ResolveByURI(ctx context.Context, uri string) (*ContentResult, error) {
	switch {
	case strings.HasPrefix(uri, "magnet:?"):
		return ur.resolveByMagnet(ctx, uri)
	case strings.HasPrefix(uri, "ed2k://"):
		return ur.resolveByEd2k(ctx, uri)
	case strings.HasPrefix(uri, "0x") && len(uri) == 66:
		// keccak256 hash
		hashBytes, err := hex.DecodeString(uri[2:])
		if err != nil {
			return nil, fmt.Errorf("invalid hash: %w", err)
		}
		var contentHash [32]byte
		copy(contentHash[:], hashBytes)
		return ur.Resolve(ctx, contentHash)
	default:
		// Try as IPFS CID
		if ur.ipfsBridge != nil {
			data, err := ur.ipfsBridge.Get(ctx, uri)
			if err == nil {
				return &ContentResult{
					Data:       data,
					Protocol:   ProtocolIPFS,
					ResolvedAt: time.Now(),
				}, nil
			}
		}
		return nil, fmt.Errorf("unrecognized URI: %s", uri)
	}
}

func (ur *UniversalResolver) resolveByMagnet(ctx context.Context, uri string) (*ContentResult, error) {
	if ur.torrentBridge == nil {
		return nil, fmt.Errorf("bittorrent not enabled")
	}

	params, err := dtorrent.ParseMagnetURI(uri)
	if err != nil {
		return nil, err
	}

	data, err := ur.torrentBridge.FetchByInfoHash(ctx, params.InfoHash)
	if err != nil {
		return nil, err
	}

	// Try to find content hash
	contentHash, _ := ur.torrentBridge.ContentHashFromInfoHash(params.InfoHash)

	return &ContentResult{
		Data:        data,
		Protocol:    ProtocolBitTorrent,
		ContentHash: contentHash,
		ResolvedAt:  time.Now(),
	}, nil
}

func (ur *UniversalResolver) resolveByEd2k(ctx context.Context, uri string) (*ContentResult, error) {
	if ur.ed2kBridge == nil {
		return nil, fmt.Errorf("ed2k not enabled")
	}

	link, err := ed2k.ParseLink(uri)
	if err != nil {
		return nil, err
	}

	// Look up content hash from ed2k hash
	contentHash, ok := ur.ed2kBridge.ContentHashFromEd2k(link.Hash)
	if !ok {
		return nil, fmt.Errorf("no content mapping for ed2k hash %s", hex.EncodeToString(link.Hash[:]))
	}

	// Resolve via the normal fallback chain
	return ur.Resolve(ctx, contentHash)
}
