// Copyright 2024-2026 The N42 Authors
// This file is part of the N42 library.
//
// Ported from erigon cl/rpc/rpc.go — the real BeaconRpcP2P (replaces the
// Phase 7.4 rpc_stub.go). Adaptations for the B+ block-only path (#34):
//   - import paths rewritten to N42 in-repo equivalents + depshim/sentinelproto.
//   - SetStatus uses common.Hash directly (sentinelproto.Status roots are
//     common.Hash; no gointerfaces H256 conversion).
//   - the PeerDAS columnDataPeers helper is dropped (separate erigon file,
//     not needed for block fork choice); SendColumnSidecarsByRootIdentifierReq
//     returns errBlockOnlyTransport.
//
// The block req/resp path — SendBeaconBlocksBy{Range,Root}Req via the in-process
// sentinel SendRequest — is the live B+ backfill path; blob/column/exec-payload
// methods are present for interface satisfaction but not exercised by block-only
// fork choice.

//go:build n42el

package rpc

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/golang/snappy"
	"go.uber.org/zap/buffer"

	"github.com/n42blockchain/N42/internal/cl/clparams"
	"github.com/n42blockchain/N42/internal/cl/cltypes"
	"github.com/n42blockchain/N42/internal/cl/cltypes/solid"
	"github.com/n42blockchain/N42/internal/cl/phase1/core/state"
	"github.com/n42blockchain/N42/internal/cl/sentinel/communication"
	"github.com/n42blockchain/N42/internal/cl/sentinel/communication/ssz_snappy"
	"github.com/n42blockchain/N42/internal/cl/utils"
	"github.com/n42blockchain/N42/internal/cl/utils/eth_clock"
	common "github.com/n42blockchain/N42/internal/cl/depshim/common"
	"github.com/n42blockchain/N42/internal/cl/depshim/sentinelproto"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

const maxMessageLength = 18 * datasize.MB

// errBlockOnlyTransport is returned by PeerDAS column-by-root requests, which
// the B+ block-only transport does not implement (no column peer selection).
var errBlockOnlyTransport = errors.New("rpc: PeerDAS column-by-root not supported in block-only transport")

// BeaconRpcP2P represents a beacon chain RPC client.
type BeaconRpcP2P struct {
	ctx          context.Context
	sentinel     sentinelproto.SentinelClient
	beaconConfig *clparams.BeaconChainConfig
	ethClock     eth_clock.EthereumClock
}

// NewBeaconRpcP2P creates a new BeaconRpcP2P. beaconState is accepted for
// signature compatibility with erigon's ClStages wiring; the block-only
// transport does not use it (it fed the dropped PeerDAS columnDataPeers).
func NewBeaconRpcP2P(ctx context.Context, sentinel sentinelproto.SentinelClient, beaconConfig *clparams.BeaconChainConfig, ethClock eth_clock.EthereumClock, _ *state.CachingBeaconState) *BeaconRpcP2P {
	return &BeaconRpcP2P{
		ctx:          ctx,
		sentinel:     sentinel,
		beaconConfig: beaconConfig,
		ethClock:     ethClock,
	}
}

func (b *BeaconRpcP2P) sendBlocksRequest(ctx context.Context, topic string, reqData []byte) ([]*cltypes.SignedBeaconBlock, string, error) {
	responses, pid, err := b.sendRequest(ctx, topic, reqData)
	if err != nil {
		return nil, pid, err
	}

	responsePacket := []*cltypes.SignedBeaconBlock{}
	for _, data := range responses {
		responseChunk := cltypes.NewSignedBeaconBlock(b.beaconConfig, data.version)
		if err := responseChunk.DecodeSSZ(data.raw, int(data.version)); err != nil {
			return nil, pid, err
		}
		responsePacket = append(responsePacket, responseChunk)
	}

	return responsePacket, pid, nil
}

func (b *BeaconRpcP2P) sendBlobsSidecar(ctx context.Context, topic string, reqData []byte, count uint64) ([]*cltypes.BlobSidecar, string, error) {
	responses, pid, err := b.sendRequest(ctx, topic, reqData)
	if err != nil {
		return nil, pid, err
	}

	responsePacket := []*cltypes.BlobSidecar{}
	for _, data := range responses {
		responseChunk := &cltypes.BlobSidecar{}
		if err := responseChunk.DecodeSSZ(data.raw, int(data.version)); err != nil {
			return nil, pid, err
		}
		responsePacket = append(responsePacket, responseChunk)
	}

	return responsePacket, pid, nil
}

// SendColumnSidecarsByRootIdentifierReq is not supported by the block-only
// transport (it needs the dropped PeerDAS column peer selector).
func (b *BeaconRpcP2P) SendColumnSidecarsByRootIdentifierReq(
	ctx context.Context,
	req *solid.ListSSZ[*cltypes.DataColumnsByRootIdentifier],
) ([]*cltypes.DataColumnSidecar, string, error) {
	return nil, "", errBlockOnlyTransport
}

func (b *BeaconRpcP2P) SendColumnSidecarsByRangeReqV1(
	ctx context.Context,
	start, count uint64,
	columns []uint64,
) ([]*cltypes.DataColumnSidecar, string, error) {
	req := &cltypes.ColumnSidecarsByRangeRequest{
		StartSlot: start,
		Count:     count,
		Columns:   solid.NewUint64ListSSZ(int(b.beaconConfig.NumberOfColumns)),
	}
	for _, column := range columns {
		req.Columns.Append(column)
	}
	var buffer buffer.Buffer
	if err := ssz_snappy.EncodeAndWrite(&buffer, req); err != nil {
		return nil, "", err
	}

	responsePacket, pid, err := b.sendRequest(ctx, communication.DataColumnSidecarsByRangeProtocolV1, buffer.Bytes())
	if err != nil {
		return nil, pid, err
	}

	ColumnSidecars := []*cltypes.DataColumnSidecar{}
	for _, data := range responsePacket {
		columnSidecar := &cltypes.DataColumnSidecar{}
		if err := columnSidecar.DecodeSSZ(data.raw, int(data.version)); err != nil {
			return nil, pid, err
		}
		ColumnSidecars = append(ColumnSidecars, columnSidecar)
	}
	return ColumnSidecars, pid, nil
}

// SendExecutionPayloadEnvelopesByRangeReq retrieves execution payload envelopes by slot range.
// [New in Gloas:EIP7732]
func (b *BeaconRpcP2P) SendExecutionPayloadEnvelopesByRangeReq(ctx context.Context, start, count uint64) ([]*cltypes.SignedExecutionPayloadEnvelope, string, error) {
	var buf buffer.Buffer
	if err := ssz_snappy.EncodeAndWrite(&buf, &cltypes.ExecutionPayloadEnvelopesByRangeRequest{
		StartSlot: start,
		Count:     count,
	}); err != nil {
		return nil, "", err
	}

	responsePacket, pid, err := b.sendRequest(ctx, communication.ExecutionPayloadEnvelopesByRangeProtocolV1, buf.Bytes())
	if err != nil {
		return nil, pid, err
	}

	envelopes := make([]*cltypes.SignedExecutionPayloadEnvelope, 0, len(responsePacket))
	for _, data := range responsePacket {
		envelope := &cltypes.SignedExecutionPayloadEnvelope{
			Message: cltypes.NewExecutionPayloadEnvelope(b.beaconConfig),
		}
		if err := envelope.DecodeSSZ(data.raw, int(data.version)); err != nil {
			return nil, pid, err
		}
		envelopes = append(envelopes, envelope)
	}
	return envelopes, pid, nil
}

// SendExecutionPayloadEnvelopesByRootReq retrieves execution payload envelopes by block root.
// [New in Gloas:EIP7732]
func (b *BeaconRpcP2P) SendExecutionPayloadEnvelopesByRootReq(ctx context.Context, roots [][32]byte) ([]*cltypes.SignedExecutionPayloadEnvelope, string, error) {
	var req solid.HashListSSZ = solid.NewHashList(int(b.beaconConfig.MaxRequestBlocksDeneb))
	for _, root := range roots {
		req.Append(root)
	}
	var buf buffer.Buffer
	if err := ssz_snappy.EncodeAndWrite(&buf, req); err != nil {
		return nil, "", err
	}

	responsePacket, pid, err := b.sendRequest(ctx, communication.ExecutionPayloadEnvelopesByRootProtocolV1, buf.Bytes())
	if err != nil {
		return nil, pid, err
	}

	envelopes := make([]*cltypes.SignedExecutionPayloadEnvelope, 0, len(responsePacket))
	for _, data := range responsePacket {
		envelope := &cltypes.SignedExecutionPayloadEnvelope{
			Message: cltypes.NewExecutionPayloadEnvelope(b.beaconConfig),
		}
		if err := envelope.DecodeSSZ(data.raw, int(data.version)); err != nil {
			return nil, pid, err
		}
		envelopes = append(envelopes, envelope)
	}
	return envelopes, pid, nil
}

func (b *BeaconRpcP2P) SendBlobsSidecarByIdentifierReq(ctx context.Context, req *solid.ListSSZ[*cltypes.BlobIdentifier]) ([]*cltypes.BlobSidecar, string, error) {
	var buffer buffer.Buffer
	if err := ssz_snappy.EncodeAndWrite(&buffer, req); err != nil {
		return nil, "", err
	}

	data := buffer.Bytes()
	blobs, pid, err := b.sendBlobsSidecar(ctx, communication.BlobSidecarByRootProtocolV1, data, uint64(req.Len()))
	if err != nil {
		if strings.Contains(err.Error(), "invalid request") {
			b.BanPeer(pid)
		}
		return nil, pid, err
	}
	return blobs, pid, nil
}

func (b *BeaconRpcP2P) SendBlobsSidecarByRangerReq(ctx context.Context, start, count uint64) ([]*cltypes.BlobSidecar, string, error) {
	var buffer buffer.Buffer
	if err := ssz_snappy.EncodeAndWrite(&buffer, &cltypes.BlobsByRangeRequest{
		StartSlot: start,
		Count:     count,
	}); err != nil {
		return nil, "", err
	}

	data := buffer.Bytes()
	return b.sendBlobsSidecar(ctx, communication.BlobSidecarByRangeProtocolV1, data, count*b.beaconConfig.MaxBlobsPerBlock)
}

// SendBeaconBlocksByRangeReq retrieves a block range from the beacon chain.
func (b *BeaconRpcP2P) SendBeaconBlocksByRangeReq(ctx context.Context, start, count uint64) ([]*cltypes.SignedBeaconBlock, string, error) {
	req := &cltypes.BeaconBlocksByRangeRequest{
		StartSlot: start,
		Count:     count,
		Step:      1, // deprecated, and must be set to 1.
	}
	var buffer buffer.Buffer
	if err := ssz_snappy.EncodeAndWrite(&buffer, req); err != nil {
		return nil, "", err
	}

	data := buffer.Bytes()
	// Prefer v2 but accept v1 for peers that haven't upgraded yet.
	blocksByRangeTopic := communication.BeaconBlocksByRangeProtocolV2 + "," + communication.BeaconBlocksByRangeProtocolV1
	return b.sendBlocksRequest(ctx, blocksByRangeTopic, data)
}

// SendBeaconBlocksByRootReq retrieves blocks by root from the beacon chain.
func (b *BeaconRpcP2P) SendBeaconBlocksByRootReq(ctx context.Context, roots [][32]byte) ([]*cltypes.SignedBeaconBlock, string, error) {
	var req solid.HashListSSZ = solid.NewHashList(69696969) // The number is used for hashing, it is innofensive here.
	for _, root := range roots {
		req.Append(root)
	}
	var buffer buffer.Buffer
	if err := ssz_snappy.EncodeAndWrite(&buffer, req); err != nil {
		return nil, "", err
	}
	data := buffer.Bytes()
	// Prefer v2 but accept v1 for peers that haven't upgraded yet.
	blocksByRootTopic := communication.BeaconBlocksByRootProtocolV2 + "," + communication.BeaconBlocksByRootProtocolV1
	return b.sendBlocksRequest(ctx, blocksByRootTopic, data)
}

// Peers retrieves the active peer count.
func (b *BeaconRpcP2P) Peers() (uint64, error) {
	amount, err := b.sentinel.GetPeers(b.ctx, &sentinelproto.EmptyMessage{})
	if err != nil {
		return 0, err
	}
	return amount.Active, nil
}

func (b *BeaconRpcP2P) SetStatus(finalizedRoot common.Hash, finalizedEpoch uint64, headRoot common.Hash, headSlot uint64) error {
	forkDigest, err := b.ethClock.CurrentForkDigest()
	if err != nil {
		return err
	}
	_, err = b.sentinel.SetStatus(b.ctx, &sentinelproto.Status{
		ForkDigest:     utils.Bytes4ToUint32(forkDigest),
		FinalizedRoot:  finalizedRoot,
		FinalizedEpoch: finalizedEpoch,
		HeadRoot:       headRoot,
		HeadSlot:       headSlot,
	})
	return err
}

func (b *BeaconRpcP2P) BanPeer(pid string) {
	b.sentinel.BanPeer(b.ctx, &sentinelproto.Peer{Pid: pid})
}

// responseData stores the version and raw data of a response chunk.
type responseData struct {
	version clparams.StateVersion
	raw     []byte
}

// parseResponseData parses the multi-chunk response from a sentinel message.
func (b *BeaconRpcP2P) parseResponseData(message *sentinelproto.ResponseData) ([]responseData, string, error) {
	if message.Error {
		rd := snappy.NewReader(bytes.NewBuffer(message.Data))
		errBytes, _ := io.ReadAll(rd)
		errMsg := string(errBytes)
		log.Trace("received range req error", "err", errMsg, "raw", string(message.Data))
		return nil, message.Peer.Pid, fmt.Errorf("peer error response: %s", errMsg)
	}

	responsePacket := []responseData{}
	r := bytes.NewReader(message.Data)
	for {
		forkDigest := make([]byte, 4)
		if n, err := r.Read(forkDigest); err != nil {
			if err == io.EOF {
				break
			}
			return nil, message.Peer.Pid, err
		} else if n == 0 {
			break
		}

		// Read varint for length of message.
		encodedLn, _, err := ssz_snappy.ReadUvarint(r)
		if err != nil {
			return nil, message.Peer.Pid, fmt.Errorf("sendRequest failed. Unable to read varint from message prefix: %w", err)
		}
		// Sanity check for message size.
		if encodedLn > uint64(maxMessageLength) {
			return nil, message.Peer.Pid, errors.New("received message too big")
		}

		// Read bytes using snappy into a new raw buffer of size encodedLn.
		raw := make([]byte, encodedLn)
		sr := snappy.NewReader(r)
		bytesRead := 0
		for bytesRead < int(encodedLn) {
			n, err := sr.Read(raw[bytesRead:])
			if err != nil {
				return nil, message.Peer.Pid, fmt.Errorf("read error: %w", err)
			}
			bytesRead += n
		}
		// Fork digests
		respForkDigest := binary.BigEndian.Uint32(forkDigest)
		if respForkDigest == 0 {
			return nil, message.Peer.Pid, errors.New("null fork digest")
		}

		version, err := b.ethClock.StateVersionByForkDigest(utils.Uint32ToBytes4(respForkDigest))
		if err != nil {
			return nil, message.Peer.Pid, fmt.Errorf("unknown fork digest %x: %w", respForkDigest, err)
		}
		responsePacket = append(responsePacket, responseData{
			version: version,
			raw:     raw,
		})

		// read next result byte
		if _, err := r.ReadByte(); err == io.EOF {
			break
		} else if err != nil {
			log.Debug("failed to read byte", "err", err)
			return nil, message.Peer.Pid, err
		}
	}
	return responsePacket, message.Peer.Pid, nil
}

// sendRequest sends a request to the sentinel and decodes the response chunks.
func (b *BeaconRpcP2P) sendRequest(
	ctx context.Context,
	topic string,
	reqPayload []byte,
) ([]responseData, string, error) {
	ctx, cn := context.WithTimeout(ctx, time.Second*2)
	defer cn()
	message, err := b.sentinel.SendRequest(ctx, &sentinelproto.RequestData{
		Data:  reqPayload,
		Topic: topic,
	})
	if err != nil {
		return nil, "", err
	}
	return b.parseResponseData(message)
}

func (b *BeaconRpcP2P) sendRequestWithPeer(
	ctx context.Context,
	topic string,
	reqPayload []byte,
	peerId string,
) ([]responseData, string, error) {
	ctx, cn := context.WithTimeout(ctx, time.Second*2)
	defer cn()
	message, err := b.sentinel.SendPeerRequest(ctx, &sentinelproto.RequestDataWithPeer{
		Pid:   peerId,
		Data:  reqPayload,
		Topic: topic,
	})
	if err != nil {
		return nil, "", err
	}
	return b.parseResponseData(message)
}
