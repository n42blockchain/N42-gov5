// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package sync

import (
	"context"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/pkg/errors"

	"github.com/n42blockchain/N42/proto/sync_pb"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/log"
)

// SendBlobSidecarsByRange sends a BlobSidecarsByRange request and returns the response.
func SendBlobSidecarsByRange(
	ctx context.Context,
	p2pProvider p2p.SenderEncoder,
	pid peer.ID,
	req *sync_pb.BlobSidecarsByRangeRequest,
) (*sync_pb.BlobSidecarsResponse, error) {
	topic, err := p2p.TopicFromMessage(p2p.BlobSidecarsByRangeMessageName)
	if err != nil {
		return nil, err
	}

	stream, err := p2pProvider.Send(ctx, req, topic, pid)
	if err != nil {
		return nil, err
	}
	defer closeStream(stream)

	code, errMsg, err := ReadStatusCode(stream, p2pProvider.Encoding())
	if err != nil {
		return nil, errors.Wrap(err, "failed to read blob sidecars by range status code")
	}
	if code != 0 {
		return nil, errors.Errorf("peer returned error code %d: %s", code, errMsg)
	}

	resp := new(sync_pb.BlobSidecarsResponse)
	if err := p2pProvider.Encoding().DecodeWithMaxLength(stream, resp); err != nil {
		return nil, errors.Wrap(err, "failed to decode blob sidecars by range response")
	}

	log.Debug("Received blob sidecars by range", "sidecars", len(resp.Sidecars), "peer", pid.String())
	return resp, nil
}

// SendBlobSidecarsByRoot sends a BlobSidecarsByRoot request and returns the response.
func SendBlobSidecarsByRoot(
	ctx context.Context,
	p2pProvider p2p.SenderEncoder,
	pid peer.ID,
	req *sync_pb.BlobSidecarsByRootRequest,
) (*sync_pb.BlobSidecarsResponse, error) {
	topic, err := p2p.TopicFromMessage(p2p.BlobSidecarsByRootMessageName)
	if err != nil {
		return nil, err
	}

	stream, err := p2pProvider.Send(ctx, req, topic, pid)
	if err != nil {
		return nil, err
	}
	defer closeStream(stream)

	code, errMsg, err := ReadStatusCode(stream, p2pProvider.Encoding())
	if err != nil {
		return nil, errors.Wrap(err, "failed to read blob sidecars by root status code")
	}
	if code != 0 {
		return nil, errors.Errorf("peer returned error code %d: %s", code, errMsg)
	}

	resp := new(sync_pb.BlobSidecarsResponse)
	if err := p2pProvider.Encoding().DecodeWithMaxLength(stream, resp); err != nil {
		return nil, errors.Wrap(err, "failed to decode blob sidecars by root response")
	}

	log.Debug("Received blob sidecars by root", "sidecars", len(resp.Sidecars), "peer", pid.String())
	return resp, nil
}
