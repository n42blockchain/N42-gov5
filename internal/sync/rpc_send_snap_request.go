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
//
// Client-side helpers for the snap-sync RPCs. SendGetAccountRange and
// related senders wrap p2p.SenderEncoder.Send, attach the correct
// topic, and decode typed sync_pb responses used by the snap syncer.

package sync

import (
	"context"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/pkg/errors"

	"github.com/n42blockchain/N42/proto/sync_pb"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/log"
)

// SendGetAccountRange sends a GetAccountRange request to a peer and returns the response.
func SendGetAccountRange(
	ctx context.Context,
	p2pProvider p2p.SenderEncoder,
	pid peer.ID,
	req *sync_pb.GetAccountRangeRequest,
) (*sync_pb.AccountRangeResponse, error) {
	topic, err := p2p.TopicFromMessage(p2p.GetAccountRangeMessageName)
	if err != nil {
		return nil, err
	}

	stream, err := p2pProvider.Send(ctx, req, topic, pid)
	if err != nil {
		return nil, err
	}
	defer closeStream(stream)

	// Read status code.
	code, errMsg, err := ReadStatusCode(stream, p2pProvider.Encoding())
	if err != nil {
		return nil, errors.Wrap(err, "failed to read account range status code")
	}
	if code != 0 {
		return nil, errors.Errorf("peer returned error code %d: %s", code, errMsg)
	}

	resp := new(sync_pb.AccountRangeResponse)
	if err := p2pProvider.Encoding().DecodeWithMaxLength(stream, resp); err != nil {
		return nil, errors.Wrap(err, "failed to decode account range response")
	}

	log.Debug("Received account range", "accounts", len(resp.Accounts), "completed", resp.Completed, "peer", pid.String())
	return resp, nil
}

// SendGetStorageRange sends a GetStorageRange request to a peer and returns the response.
func SendGetStorageRange(
	ctx context.Context,
	p2pProvider p2p.SenderEncoder,
	pid peer.ID,
	req *sync_pb.GetStorageRangeRequest,
) (*sync_pb.StorageRangeResponse, error) {
	topic, err := p2p.TopicFromMessage(p2p.GetStorageRangeMessageName)
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
		return nil, errors.Wrap(err, "failed to read storage range status code")
	}
	if code != 0 {
		return nil, errors.Errorf("peer returned error code %d: %s", code, errMsg)
	}

	resp := new(sync_pb.StorageRangeResponse)
	if err := p2pProvider.Encoding().DecodeWithMaxLength(stream, resp); err != nil {
		return nil, errors.Wrap(err, "failed to decode storage range response")
	}

	log.Debug("Received storage range", "slots", len(resp.Slots), "completed", resp.Completed, "peer", pid.String())
	return resp, nil
}

// SendGetCode sends a GetCode request to a peer and returns the response.
func SendGetCode(
	ctx context.Context,
	p2pProvider p2p.SenderEncoder,
	pid peer.ID,
	req *sync_pb.GetCodeRequest,
) (*sync_pb.CodeResponse, error) {
	topic, err := p2p.TopicFromMessage(p2p.GetCodeMessageName)
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
		return nil, errors.Wrap(err, "failed to read code response status code")
	}
	if code != 0 {
		return nil, errors.Errorf("peer returned error code %d: %s", code, errMsg)
	}

	resp := new(sync_pb.CodeResponse)
	if err := p2pProvider.Encoding().DecodeWithMaxLength(stream, resp); err != nil {
		return nil, errors.Wrap(err, "failed to decode code response")
	}

	log.Debug("Received code", "codes", len(resp.Codes), "peer", pid.String())
	return resp, nil
}
