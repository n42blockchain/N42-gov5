// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package messaging

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/n42blockchain/N42/log"
)

const (
	// StoreQueryProtocolID is the libp2p protocol for message store queries.
	StoreQueryProtocolID = "/n42/msg/store_query/1.0.0"

	// maxStoreQueryResults is the maximum number of messages returned per query.
	maxStoreQueryResults = 100

	// storeQueryTimeout is the timeout for store query requests.
	storeQueryTimeout = 30 * time.Second
)

// StoreQueryRequest represents a request for historical messages.
type StoreQueryRequest struct {
	Topic string
	From  int64 // unix nano
	To    int64 // unix nano
	Limit int
}

// StoreQueryResponse contains the results of a store query.
type StoreQueryResponse struct {
	Messages []*Envelope
}

// PeerHandler manages message store query protocol.
type PeerHandler struct {
	host    host.Host
	service *Service
}

// NewPeerHandler creates a new peer handler.
func NewPeerHandler(h host.Host, service *Service) *PeerHandler {
	ph := &PeerHandler{
		host:    h,
		service: service,
	}
	h.SetStreamHandler(protocol.ID(StoreQueryProtocolID), ph.HandleStoreQuery)
	return ph
}

// HandleStoreQuery handles incoming store query requests from peers.
func (ph *PeerHandler) HandleStoreQuery(s network.Stream) {
	defer s.Close()
	_ = s.SetReadDeadline(time.Now().Add(storeQueryTimeout))

	// Read request: [topicLen(2)][topic][from(8)][to(8)][limit(4)]
	var topicLenBuf [2]byte
	if _, err := io.ReadFull(s, topicLenBuf[:]); err != nil {
		log.Debug("Failed to read store query topic length", "err", err)
		return
	}
	topicLen := int(binary.BigEndian.Uint16(topicLenBuf[:]))
	if topicLen > 1024 {
		log.Debug("Store query topic too long", "len", topicLen)
		return
	}
	topicBuf := make([]byte, topicLen)
	if _, err := io.ReadFull(s, topicBuf); err != nil {
		log.Debug("Failed to read store query topic", "err", err)
		return
	}

	var timeBuf [16]byte
	if _, err := io.ReadFull(s, timeBuf[:]); err != nil {
		log.Debug("Failed to read store query time range", "err", err)
		return
	}
	from := int64(binary.BigEndian.Uint64(timeBuf[:8]))
	to := int64(binary.BigEndian.Uint64(timeBuf[8:]))

	var limitBuf [4]byte
	if _, err := io.ReadFull(s, limitBuf[:]); err != nil {
		log.Debug("Failed to read store query limit", "err", err)
		return
	}
	limit := int(binary.BigEndian.Uint32(limitBuf[:]))
	if limit <= 0 || limit > maxStoreQueryResults {
		limit = maxStoreQueryResults
	}

	// Query local store
	fromTime := time.Unix(0, from)
	toTime := time.Unix(0, to)
	msgs := ph.service.GetMessages(string(topicBuf), fromTime, toTime, limit)

	_ = s.SetWriteDeadline(time.Now().Add(storeQueryTimeout))

	// Write response: [count(4)] then for each message: [envLen(4)][envData]
	var countBuf [4]byte
	binary.BigEndian.PutUint32(countBuf[:], uint32(len(msgs)))
	if _, err := s.Write(countBuf[:]); err != nil {
		return
	}

	for _, msg := range msgs {
		env := &Envelope{
			Version:   EnvelopeVersion1,
			Topic:     msg.Topic,
			Payload:   msg.Payload,
			Timestamp: msg.Timestamp.UnixNano(),
			Nonce:     0,
			Sender:    []byte(msg.SenderID),
		}
		data, err := EncodeEnvelope(env)
		if err != nil {
			continue
		}
		var lenBuf [4]byte
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(data)))
		if _, err := s.Write(lenBuf[:]); err != nil {
			return
		}
		if _, err := s.Write(data); err != nil {
			return
		}
	}
}

// QueryPeerStore sends a store query request to a peer and returns the results.
func (ph *PeerHandler) QueryPeerStore(peerID peer.ID, topic string, from, to time.Time) ([]*Message, error) {
	ctx, cancel := context.WithTimeout(context.Background(), storeQueryTimeout)
	defer cancel()

	s, err := ph.host.NewStream(ctx, peerID, protocol.ID(StoreQueryProtocolID))
	if err != nil {
		return nil, fmt.Errorf("open stream: %w", err)
	}
	defer s.Close()

	_ = s.SetDeadline(time.Now().Add(storeQueryTimeout))

	// Write request
	topicBytes := []byte(topic)
	buf := make([]byte, 0, 2+len(topicBytes)+16+4)
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(topicBytes)))
	buf = append(buf, topicBytes...)
	var timeBuf [8]byte
	binary.BigEndian.PutUint64(timeBuf[:], uint64(from.UnixNano()))
	buf = append(buf, timeBuf[:]...)
	binary.BigEndian.PutUint64(timeBuf[:], uint64(to.UnixNano()))
	buf = append(buf, timeBuf[:]...)
	buf = binary.BigEndian.AppendUint32(buf, uint32(maxStoreQueryResults))
	if _, err := s.Write(buf); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	// Read response
	var countBuf [4]byte
	if _, err := io.ReadFull(s, countBuf[:]); err != nil {
		return nil, fmt.Errorf("read count: %w", err)
	}
	count := int(binary.BigEndian.Uint32(countBuf[:]))
	if count > maxStoreQueryResults {
		count = maxStoreQueryResults
	}

	messages := make([]*Message, 0, count)
	for i := 0; i < count; i++ {
		var lenBuf [4]byte
		if _, err := io.ReadFull(s, lenBuf[:]); err != nil {
			break
		}
		dataLen := int(binary.BigEndian.Uint32(lenBuf[:]))
		if dataLen > MaxEnvelopeSize {
			break
		}
		data := make([]byte, dataLen)
		if _, err := io.ReadFull(s, data); err != nil {
			break
		}
		env, err := DecodeEnvelope(data)
		if err != nil {
			continue
		}
		id := EnvelopeID(env)
		messages = append(messages, &Message{
			ID:        id,
			Topic:     env.Topic,
			Payload:   env.Payload,
			Timestamp: time.Unix(0, env.Timestamp),
			SenderID:  string(env.Sender),
		})
	}

	return messages, nil
}
