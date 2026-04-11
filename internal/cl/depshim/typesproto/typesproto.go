// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

//go:build n42el

// Package typesproto is a stub of erigon's node/gointerfaces/typesproto
// package. The cl/ tree only references RequestsBundle from this package
// (specifically as the return type of GetAssembledBlock), so that's the
// only symbol we provide.
//
// Avoiding the real package keeps gRPC, protobuf, and the entire erigon
// node/gointerfaces tree out of the n42el dependency graph.
package typesproto

// RequestsBundle mirrors erigon's typesproto.RequestsBundle just enough to
// hold the slice of opaque request blobs. Caplin treats it as an opaque
// container; we do not need protobuf marshaling for the n42el seam because
// the requests stay in-process.
type RequestsBundle struct {
	Requests [][]byte
}

// GetRequests is the accessor cl/ uses to read the request blobs.
func (b *RequestsBundle) GetRequests() [][]byte {
	if b == nil {
		return nil
	}
	return b.Requests
}
