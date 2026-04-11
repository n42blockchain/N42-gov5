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
// Server-side snap-sync RPC handlers. accountRangeRPCHandler and
// companions stream flat-table account, storage and bytecode pages
// bounded by maxSnapAccountEntries, maxSnapStorageEntries,
// maxSnapCodeHashes and maxSnapCodeBytes limits.

package sync

import (
	"bytes"
	"context"

	libp2pcore "github.com/libp2p/go-libp2p/core"
	"github.com/pkg/errors"
	"go.opencensus.io/trace"

	ssz "github.com/prysmaticlabs/fastssz"

	"github.com/n42blockchain/N42/proto/sync_pb"
	"github.com/n42blockchain/N42/internal/p2p/p2ptypes"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
)

const (
	// maxSnapResponseBytes is the soft limit for snap sync responses (512KB).
	maxSnapResponseBytes = 512 * 1024
	// maxSnapAccountEntries is the hard limit on account entries per response.
	maxSnapAccountEntries = 4096
	// maxSnapStorageEntries is the hard limit on storage entries per response.
	maxSnapStorageEntries = 4096
	// maxSnapCodeHashes is the hard limit on code hashes per request.
	maxSnapCodeHashes = 256
	// maxSnapCodeBytes is the hard limit on total code bytes per response (2MB).
	maxSnapCodeBytes = 2 * 1024 * 1024
)

// accountRangeRPCHandler handles GetAccountRange requests.
// It reads a range of accounts from the flat Account table and returns them.
func (s *Service) accountRangeRPCHandler(ctx context.Context, msg interface{}, stream libp2pcore.Stream) error {
	ctx, span := trace.StartSpan(ctx, "sync.AccountRangeHandler")
	defer span.End()
	ctx, cancel := context.WithTimeout(ctx, respTimeout)
	defer cancel()
	SetRPCStreamDeadlines(stream)

	req, ok := msg.(*sync_pb.GetAccountRangeRequest)
	if !ok {
		return errors.New("message is not type *sync_pb.GetAccountRangeRequest")
	}

	// Validate key lengths when provided.
	if len(req.Start) > 0 && len(req.Start) != 20 {
		s.writeErrorResponseToStream(responseCodeInvalidRequest, "start key must be 20 bytes", stream)
		return p2ptypes.ErrInvalidRequest
	}
	if len(req.End) > 0 && len(req.End) != 20 {
		s.writeErrorResponseToStream(responseCodeInvalidRequest, "end key must be 20 bytes", stream)
		return p2ptypes.ErrInvalidRequest
	}
	if len(req.Start) > 0 && len(req.End) > 0 && bytes.Compare(req.Start, req.End) >= 0 {
		s.writeErrorResponseToStream(responseCodeInvalidRequest, "start must be less than end", stream)
		return p2ptypes.ErrInvalidRequest
	}

	maxBytes := req.MaxBytes
	if maxBytes == 0 || maxBytes > maxSnapResponseBytes {
		maxBytes = maxSnapResponseBytes
	}

	resp := &sync_pb.AccountRangeResponse{}
	var totalBytes uint64

	if err := s.cfg.chain.DB().View(ctx, func(tx kv.Tx) error {
		c, err := tx.Cursor(modules.Account)
		if err != nil {
			return err
		}
		defer c.Close()

		// Seek to start position.
		var k, v []byte
		if len(req.Start) > 0 {
			k, v, err = c.Seek(req.Start)
		} else {
			k, v, err = c.First()
		}
		if err != nil {
			return err
		}

		for k != nil {
			// Check end boundary.
			if len(req.End) > 0 && bytes.Compare(k, req.End) >= 0 {
				resp.Completed = true
				break
			}

			// Check limits.
			entrySize := uint64(len(k) + len(v))
			if len(resp.Accounts) > 0 && (totalBytes+entrySize > maxBytes || len(resp.Accounts) >= maxSnapAccountEntries) {
				resp.NextStart = make([]byte, len(k))
				copy(resp.NextStart, k)
				break
			}

			entry := &sync_pb.AccountEntry{
				Address: make([]byte, len(k)),
				Account: make([]byte, len(v)),
			}
			copy(entry.Address, k)
			copy(entry.Account, v)
			resp.Accounts = append(resp.Accounts, entry)
			totalBytes += entrySize

			k, v, err = c.Next()
			if err != nil {
				return err
			}
		}

		// If we exhausted the cursor without hitting end boundary.
		if k == nil && !resp.Completed {
			resp.Completed = true
		}

		return nil
	}); err != nil {
		s.writeErrorResponseToStream(responseCodeServerError, err.Error(), stream)
		return err
	}

	if err := s.writeSnapResponse(stream, resp); err != nil {
		return err
	}
	closeStream(stream)
	return nil
}

// storageRangeRPCHandler handles GetStorageRange requests.
// It reads storage slots for a specific account from the flat Storage table.
func (s *Service) storageRangeRPCHandler(ctx context.Context, msg interface{}, stream libp2pcore.Stream) error {
	ctx, span := trace.StartSpan(ctx, "sync.StorageRangeHandler")
	defer span.End()
	ctx, cancel := context.WithTimeout(ctx, respTimeout)
	defer cancel()
	SetRPCStreamDeadlines(stream)

	req, ok := msg.(*sync_pb.GetStorageRangeRequest)
	if !ok {
		return errors.New("message is not type *sync_pb.GetStorageRangeRequest")
	}

	if len(req.Account) != 20 {
		s.writeErrorResponseToStream(responseCodeInvalidRequest, "account address must be 20 bytes", stream)
		return p2ptypes.ErrInvalidRequest
	}

	maxBytes := req.MaxBytes
	if maxBytes == 0 || maxBytes > maxSnapResponseBytes {
		maxBytes = maxSnapResponseBytes
	}

	resp := &sync_pb.StorageRangeResponse{}
	var totalBytes uint64

	if err := s.cfg.chain.DB().View(ctx, func(tx kv.Tx) error {
		c, err := tx.Cursor(modules.Storage)
		if err != nil {
			return err
		}
		defer c.Close()

		// Storage keys are: address(20) + incarnation(2) + storageKey(32)
		// We use the account address as a prefix to scope the scan.
		var k, v []byte
		if len(req.Start) > 0 {
			k, v, err = c.Seek(req.Start)
		} else {
			k, v, err = c.Seek(req.Account)
		}
		if err != nil {
			return err
		}

		for k != nil {
			// Stop if we left this account's prefix.
			if !bytes.HasPrefix(k, req.Account) {
				resp.Completed = true
				break
			}

			// Check end boundary.
			if len(req.End) > 0 && bytes.Compare(k, req.End) >= 0 {
				resp.Completed = true
				break
			}

			entrySize := uint64(len(k) + len(v))
			if len(resp.Slots) > 0 && (totalBytes+entrySize > maxBytes || len(resp.Slots) >= maxSnapStorageEntries) {
				resp.NextStart = make([]byte, len(k))
				copy(resp.NextStart, k)
				break
			}

			entry := &sync_pb.StorageEntry{
				Key:   make([]byte, len(k)),
				Value: make([]byte, len(v)),
			}
			copy(entry.Key, k)
			copy(entry.Value, v)
			resp.Slots = append(resp.Slots, entry)
			totalBytes += entrySize

			k, v, err = c.Next()
			if err != nil {
				return err
			}
		}

		if k == nil && !resp.Completed {
			resp.Completed = true
		}

		return nil
	}); err != nil {
		s.writeErrorResponseToStream(responseCodeServerError, err.Error(), stream)
		return err
	}

	if err := s.writeSnapResponse(stream, resp); err != nil {
		return err
	}
	closeStream(stream)
	return nil
}

// codeRPCHandler handles GetCode requests.
// It looks up contract bytecodes by hash from the Code table.
func (s *Service) codeRPCHandler(ctx context.Context, msg interface{}, stream libp2pcore.Stream) error {
	ctx, span := trace.StartSpan(ctx, "sync.CodeHandler")
	defer span.End()
	ctx, cancel := context.WithTimeout(ctx, respTimeout)
	defer cancel()
	SetRPCStreamDeadlines(stream)

	req, ok := msg.(*sync_pb.GetCodeRequest)
	if !ok {
		return errors.New("message is not type *sync_pb.GetCodeRequest")
	}

	if len(req.Hashes) == 0 {
		s.writeErrorResponseToStream(responseCodeInvalidRequest, "no code hashes provided", stream)
		return p2ptypes.ErrInvalidRequest
	}
	if len(req.Hashes) > maxSnapCodeHashes {
		s.writeErrorResponseToStream(responseCodeInvalidRequest, "too many code hashes", stream)
		return p2ptypes.ErrInvalidRequest
	}
	for _, hash := range req.Hashes {
		if len(hash) != 32 {
			s.writeErrorResponseToStream(responseCodeInvalidRequest, "code hash must be 32 bytes", stream)
			return p2ptypes.ErrInvalidRequest
		}
	}

	resp := &sync_pb.CodeResponse{}
	var totalBytes uint64

	if err := s.cfg.chain.DB().View(ctx, func(tx kv.Tx) error {
		for _, hash := range req.Hashes {
			code, err := tx.GetOne(modules.Code, hash)
			if err != nil {
				return err
			}
			if code == nil {
				continue // skip missing codes
			}
			if totalBytes+uint64(len(code)) > maxSnapCodeBytes {
				break // response size limit reached
			}

			entry := &sync_pb.CodeEntry{
				Hash: make([]byte, len(hash)),
				Code: make([]byte, len(code)),
			}
			copy(entry.Hash, hash)
			copy(entry.Code, code)
			resp.Codes = append(resp.Codes, entry)
			totalBytes += uint64(len(code))
		}
		return nil
	}); err != nil {
		s.writeErrorResponseToStream(responseCodeServerError, err.Error(), stream)
		return err
	}

	if err := s.writeSnapResponse(stream, resp); err != nil {
		return err
	}
	closeStream(stream)
	return nil
}

// writeSnapResponse writes a snap sync response to the stream.
func (s *Service) writeSnapResponse(stream libp2pcore.Stream, msg ssz.Marshaler) error {
	SetStreamWriteDeadline(stream, defaultWriteDuration)

	if _, err := stream.Write([]byte{responseCodeSuccess}); err != nil {
		log.Debug("Failed to write snap response status", "err", err)
		return err
	}

	_, err := s.cfg.p2p.Encoding().EncodeWithMaxLength(stream, msg)
	if err != nil {
		log.Debug("Failed to encode snap response", "err", err)
	}
	return err
}
